package http

import (
	"encoding/json"
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
	"github.com/gin-gonic/gin"
)

// R40d: «Голос в "Посмотреть, как это работает" сделай как в озвучке
// обучения, побыстрее». The login demo lines are read with the voice the
// tour plays (the premade fallback while the chosen voice needs a paid plan,
// the chosen one after the upgrade), with voice_settings.speed; the tour's
// own requests carry no speed. The files are public only for the current
// demo map, and /status says «демо на входе: N из N».
func TestR40dLoginDemoVoice(t *testing.T) {
	repo, ctx := testPlatformDB(t, elevenKeyDoc, premiumCfgDoc)
	db, err := pg.NewDB(ctx, os.Getenv("BS_TEST_DSN"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Pool.Close()
	if _, err := db.Pool.Exec(ctx, `DELETE FROM tts_audio WHERE style = ANY($1)`, []string{premiumStyle, loginStyle}); err != nil {
		t.Fatal(err)
	}
	t.Setenv("ELEVENLABS_API_KEY", "sk_r40d_free_____0123456789abcdef0123456789abcdef01")
	t.Setenv("ELEVENLABS_VOICE_ID", "")
	t.Setenv("ELEVENLABS_FALLBACK_VOICE_ID", "")
	const daniel = "onwK4e9ZLuTAKqWW03F9"
	var paid atomic.Bool
	var mu sync.Mutex
	type req struct {
		voice, text string
		speed       any
		hasSpeed    bool
	}
	var reqs []req
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.URL.Path, "/v1/text-to-speech/") {
			w.WriteHeader(404)
			return
		}
		id := strings.TrimPrefix(r.URL.Path, "/v1/text-to-speech/")
		var b struct {
			Text     string         `json:"text"`
			Settings map[string]any `json:"voice_settings"`
		}
		_ = json.NewDecoder(r.Body).Decode(&b)
		sp, has := b.Settings["speed"]
		mu.Lock()
		reqs = append(reqs, req{id, b.Text, sp, has})
		mu.Unlock()
		if id == defaultElevenVoice && !paid.Load() {
			w.WriteHeader(402)
			_, _ = w.Write([]byte(`{"detail":{"type":"payment_required","code":"paid_plan_required","message":"Free users cannot use library voices via the API.","status":"paid_plan_required"}}`))
			return
		}
		w.Header().Set("Content-Type", "audio/mpeg")
		_, _ = w.Write(append([]byte("ID3:"+id+":"+b.Text), make([]byte, 300)...))
	}))
	defer srv.Close()
	oldPace := PremiumPace
	PremiumPace = time.Millisecond
	defer func() { PremiumPace = oldPace }()

	tour := []string{"Раз.", "Два.", "Три."}
	login := []string{"Это ваш кабинет.", "Карта здоровья.", "Войдите через бот."} // R71: no dictionary word, the text goes as written
	secret := func() []byte { return []byte("jwt-secret-for-tests-0123456789") }
	p := NewPremiumVoice(repo, secret, func() []string { return tour })
	p.EL = &ai.Eleven{Base: srv.URL, HTTP: srv.Client(), Key: p.key, Wait: func(int, time.Duration) time.Duration { return time.Millisecond }}
	p.Load(ctx)
	p.SetLoginTexts(func() []string { return login })
	settle := func() {
		for i := 0; i < 400; i++ {
			p.mu.Lock()
			r := p.job.Running || p.login.Running
			p.mu.Unlock()
			if !r {
				return
			}
			time.Sleep(10 * time.Millisecond)
		}
		t.Fatal("still reading")
	}
	// the tour's finish starts the demo's reading: wait for the result itself
	waitFor := func(what string, ok func() bool) {
		for i := 0; i < 400; i++ {
			if ok() {
				settle()
				return
			}
			time.Sleep(10 * time.Millisecond)
		}
		t.Fatalf("timeout: %s", what)
	}
	check := func() CheckItem {
		s := &SysCheck{Premium: p, Builtin: func() (int, int) { return 33, 33 }}
		return s.voice(ctx)
	}

	// before any ElevenLabs voice: the bundled recordings, said so
	if v, _ := p.LoginOverlay(); v != "" {
		t.Fatal("overlay without a voice")
	}
	if it := check(); !strings.Contains(it.Text, "демо на входе: встроенные записи") {
		t.Fatalf("builtin status: %+v", it)
	}

	// the chosen voice needs a paid plan: Daniel reads the tour, then the demo
	p.applyEnvVoice(ctx)
	p.maybeRun(ctx)
	settle()
	p.maybeRunAuto(ctx) // the Start loop's step
	waitFor("demo voiced", func() bool { _, m := p.LoginOverlay(); return len(m) == 3 })
	if c := p.config(); c.Active.ID != daniel {
		t.Fatalf("active: %+v", c.Active)
	}
	ver1, m1 := p.LoginOverlay()
	if ver1 == "" || len(m1) != 3 || !strings.HasPrefix(m1[login[0]], loginURLPath+"el_") {
		t.Fatalf("login overlay: %s %v", ver1, m1)
	}
	mu.Lock()
	for _, r := range reqs {
		isLogin := r.text == login[0] || r.text == login[1] || r.text == login[2]
		switch {
		case isLogin && r.voice != daniel:
			t.Errorf("login line read with %s", r.voice)
		case isLogin && r.speed != LoginSpeed:
			t.Errorf("login line speed %v, want %v", r.speed, LoginSpeed)
		case !isLogin && r.hasSpeed:
			t.Errorf("tour phrase %q sent a speed %v: the tour's pace and keys must not change", r.text, r.speed)
		}
	}
	nLogin := 0
	for _, r := range reqs {
		if r.text == login[0] {
			nLogin++
		}
	}
	mu.Unlock()
	if nLogin != 1 {
		t.Fatalf("login line read %d times", nLogin)
	}
	if LoginSpeed < 0.7 || LoginSpeed > 1.2 || LoginSpeed <= 1 {
		t.Fatalf("speed %v outside ElevenLabs' 0.7-1.2 or not faster", LoginSpeed)
	}
	// the tour's files keep their keys (speed is not in a tour key)
	if _, n, _ := p.have(ctx, p.config().Active, tour); n != 3 {
		t.Fatalf("tour files: %d", n)
	}
	it := check()
	if !strings.Contains(it.Text, "демо на входе: 3 из 3") || it.State != "ok" {
		t.Fatalf("status: %+v", it)
	}
	if v := p.view(ctx); v["login"] == nil {
		t.Fatalf("view: %+v", v)
	}

	// the files: public, immutable, only the demo's own
	gin.SetMode(gin.TestMode)
	r := gin.New()
	p.Register(r.Group("/api/v1/platform"), r.Group("/api/v1/platform"))
	get := func(path string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		r.ServeHTTP(w, httptest.NewRequest("GET", path, nil))
		return w
	}
	w := get(m1[login[1]])
	if w.Code != 200 || w.Header().Get("Content-Type") != "audio/mpeg" || !strings.Contains(w.Header().Get("Cache-Control"), "immutable") || !strings.Contains(w.Body.String(), daniel+":"+login[1]) {
		t.Fatalf("login file: %d %v", w.Code, w.Header())
	}
	_, tm := p.Overlay()
	tourKey := strings.TrimPrefix(tm[tour[0]], premiumURLPath)
	for _, bad := range []string{loginURLPath + tourKey, loginURLPath + "el_" + strings.Repeat("0", 48) + ".mp3", loginURLPath + "x.mp3"} {
		if w := get(bad); w.Code != 404 {
			t.Fatalf("%s: %d", bad, w.Code)
		}
	}

	// a restart: the map comes back from the database, nothing is read again
	mu.Lock()
	before := len(reqs)
	mu.Unlock()
	p2 := NewPremiumVoice(repo, secret, func() []string { return tour })
	p2.EL = p.EL
	p2.SetLoginTexts(func() []string { return login })
	p2.Load(ctx)
	p2.maybeRunLogin(ctx)
	if v, _ := p2.LoginOverlay(); v != ver1 {
		t.Fatalf("reload overlay %q want %q", v, ver1)
	}
	time.Sleep(30 * time.Millisecond)
	mu.Lock()
	if len(reqs) != before {
		t.Fatalf("restart read again: %d → %d", before, len(reqs))
	}
	mu.Unlock()

	// the plan is upgraded: the tour goes to the chosen voice and the demo follows
	paid.Store(true)
	c := p.config()
	c.Fallback.Checked = time.Now().Add(-2 * time.Hour).UTC().Format(time.RFC3339)
	if err := p.saveCfg(ctx, c); err != nil {
		t.Fatal(err)
	}
	p.maybeRunAuto(ctx)
	waitFor("demo re-voiced", func() bool { v, _ := p.LoginOverlay(); return v != ver1 })
	if c := p.config(); c.Active.ID != defaultElevenVoice {
		t.Fatalf("after upgrade active %+v", c.Active)
	}
	ver2, m2 := p.LoginOverlay()
	if ver2 == ver1 || len(m2) != 3 {
		t.Fatalf("overlay after upgrade: %s %v", ver2, m2)
	}
	if w := get(m2[login[0]]); w.Code != 200 || !strings.Contains(w.Body.String(), defaultElevenVoice+":"+login[0]) {
		t.Fatalf("upgraded file: %d", w.Code)
	}
	if w := get(m1[login[0]]); w.Code != 404 {
		t.Fatalf("the previous voice's demo file still public: %d", w.Code)
	}
}

func TestR40dSpeedSig(t *testing.T) {
	s := ai.DefaultElevenSettings
	if s.Sig() != "0.55/0.80/0.10/true" {
		t.Fatalf("tour sig changed: %s", s.Sig())
	}
	s.Speed = 1.12
	if s.Sig() == ai.DefaultElevenSettings.Sig() || !strings.HasSuffix(s.Sig(), "/x1.12") {
		t.Fatalf("speed sig: %s", s.Sig())
	}
	b, _ := json.Marshal(ai.DefaultElevenSettings)
	if strings.Contains(string(b), "speed") {
		t.Fatalf("the tour sends a speed: %s", b)
	}
	b, _ = json.Marshal(s)
	if !strings.Contains(string(b), `"speed":1.12`) {
		t.Fatalf("speed not sent: %s", b)
	}
}
