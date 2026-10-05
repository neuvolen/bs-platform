package http

import (
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/bnursik/business_surgery_backend/internal/ai"
	pg "github.com/bnursik/business_surgery_backend/internal/repository/pg"
)

// R39: prod /status «ElevenLabs 402 paid_plan_required: Free users cannot
// use library voices via the API». The chosen library voice stays the
// target, the premade Daniel reads the tour, the quota stop is not retried
// by the loop, and after a plan upgrade the tour is voiced with the chosen
// voice by itself.
func TestR39VoiceFallbackPaidPlan(t *testing.T) {
	repo, ctx := testPlatformDB(t, elevenKeyDoc, premiumCfgDoc)
	db, err := pg.NewDB(ctx, os.Getenv("BS_TEST_DSN"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Pool.Close()
	if _, err := db.Pool.Exec(ctx, `DELETE FROM tts_audio WHERE style = $1`, premiumStyle); err != nil {
		t.Fatal(err)
	}
	t.Setenv("ELEVENLABS_API_KEY", "sk_r39_free_____0123456789abcdef0123456789abcdef01")
	t.Setenv("ELEVENLABS_VOICE_ID", "")
	t.Setenv("ELEVENLABS_FALLBACK_VOICE_ID", "")
	var paid, quota atomic.Bool
	var mu sync.Mutex
	tts := map[string]int{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/v1/text-to-speech/") {
			id := strings.TrimPrefix(r.URL.Path, "/v1/text-to-speech/")
			id = strings.SplitN(id, "/", 2)[0]
			mu.Lock()
			tts[id]++
			mu.Unlock()
			switch {
			case id == defaultElevenVoice && !paid.Load():
				w.WriteHeader(402)
				_, _ = w.Write([]byte(`{"detail":{"type":"payment_required","code":"paid_plan_required","message":"Free users cannot use library voices via the API. Please upgrade your subscription to use this voice.","status":"paid_plan_required"}}`))
				return
			case quota.Load():
				w.WriteHeader(401)
				_, _ = w.Write([]byte(`{"detail":{"status":"quota_exceeded","message":"This request exceeds your quota of 10000."}}`))
				return
			}
			w.Header().Set("Content-Type", "audio/mpeg")
			_, _ = w.Write(append([]byte("ID3"), make([]byte, 600)...))
			return
		}
		w.WriteHeader(404)
	}))
	defer srv.Close()
	oldPace := PremiumPace
	PremiumPace = time.Millisecond
	defer func() { PremiumPace = oldPace }()
	secret := func() []byte { return []byte("jwt-secret-for-tests-0123456789") }
	texts := []string{"Раз.", "Два.", "Три."}
	p := NewPremiumVoice(repo, secret, func() []string { return texts })
	p.EL = &ai.Eleven{Base: srv.URL, HTTP: srv.Client(), Key: p.key, Wait: func(int, time.Duration) time.Duration { return time.Millisecond }}
	p.Load(ctx)
	waitJob := func() {
		for i := 0; i < 300; i++ {
			p.mu.Lock()
			r := p.job.Running
			p.mu.Unlock()
			if !r {
				return
			}
			time.Sleep(10 * time.Millisecond)
		}
		t.Fatal("job still running")
	}
	check := func() CheckItem {
		s := &SysCheck{Premium: p, Builtin: func() (int, int) { return 33, 33 }}
		return s.voice(ctx)
	}

	// the owner's library voice is taken as the target even though the plan refuses it
	p.applyEnvVoice(ctx)
	c := p.config()
	if c.Voice.ID != defaultElevenVoice || p.State(ctx).EnvError != "" {
		t.Fatalf("target: %+v env %q", c.Voice, p.State(ctx).EnvError)
	}
	p.maybeRun(ctx)
	waitJob()
	c = p.config()
	if !c.fallbackOn() || c.Fallback.Voice.ID != "onwK4e9ZLuTAKqWW03F9" || c.Fallback.Voice.Name != "Daniel" || c.Voice.ID != defaultElevenVoice {
		t.Fatalf("fallback: %+v", c.Fallback)
	}
	// the Kick goes to the Start loop; here the loop's step is run by hand
	p.maybeRunAuto(ctx)
	waitJob()
	c = p.config()
	if c.Active.ID != "onwK4e9ZLuTAKqWW03F9" {
		t.Fatalf("active: %+v", c.Active)
	}
	it := check()
	if it.Text != "ElevenLabs «Daniel» (запасной: выбранный голос требует платного тарифа), готово 3 из 3" || it.State != "ok" {
		t.Fatalf("status: %+v", it)
	}
	v := p.view(ctx)
	if v["fallback"] == nil || v["chars"] != 12 || !strings.Contains(v["planNote"].(string), "коммерческой лицензии") {
		t.Fatalf("view: %+v", v)
	}
	mu.Lock()
	libTries := tts[defaultElevenVoice]
	mu.Unlock()

	// within the hour the target is not probed again
	p.maybeRunAuto(ctx)
	mu.Lock()
	if tts[defaultElevenVoice] != libTries {
		t.Fatal("target probed again within the hour")
	}
	mu.Unlock()

	// a new phrase with the quota used up: one call, then the loop waits
	texts = append(texts, "Четыре.")
	quota.Store(true)
	p.maybeRunAuto(ctx)
	waitJob()
	mu.Lock()
	n1 := tts["onwK4e9ZLuTAKqWW03F9"]
	mu.Unlock()
	for i := 0; i < 3; i++ {
		p.maybeRunAuto(ctx)
		waitJob()
	}
	mu.Lock()
	if tts["onwK4e9ZLuTAKqWW03F9"] != n1 {
		t.Fatalf("quota looped: %d → %d", n1, tts["onwK4e9ZLuTAKqWW03F9"])
	}
	mu.Unlock()
	if it := check(); !strings.Contains(it.Text, "лимит символов ElevenLabs исчерпан") || !strings.Contains(it.Text, "весь тур: 19 символов") {
		t.Fatalf("quota status: %+v", it)
	}

	// the plan is upgraded: an hour later the target reads, the fallback goes,
	// the tour is voiced with the chosen voice
	quota.Store(false)
	paid.Store(true)
	c = p.config()
	c.Fallback.Checked = time.Now().Add(-2 * time.Hour).UTC().Format(time.RFC3339)
	if err := p.saveCfg(ctx, c); err != nil {
		t.Fatal(err)
	}
	p.maybeRunAuto(ctx)
	waitJob()
	c = p.config()
	if c.Fallback != nil || c.Active.ID != defaultElevenVoice {
		t.Fatalf("after upgrade: fallback %+v active %+v", c.Fallback, c.Active)
	}
	if it := check(); strings.Contains(it.Text, "запасной") || !strings.Contains(it.Text, "готово 4 из 4") {
		t.Fatalf("status after upgrade: %+v", it)
	}
	if spaced(2799) != "2 799" || spaced(10000) != "10 000" || spaced(999) != "999" {
		t.Fatal(spaced(2799))
	}
}
