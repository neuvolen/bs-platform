package http

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"sync"
	"testing"
	"time"

	"github.com/bnursik/business_surgery_backend/internal/ai"
	pg "github.com/bnursik/business_surgery_backend/internal/repository/pg"
	"github.com/gin-gonic/gin"
)

// R62: «Слово "Замер": ударение не туда в озвучке». The stress mark goes to
// ElevenLabs only; the shown text and every other word stay as written.
func TestR62SpeakText(t *testing.T) {
	for in, want := range map[string]string{
		"Раз в цикл вы делаете замер.":              "Раз в цикл вы делаете заме́р.",
		"Замеры. Колесо бизнеса и Gallup.":          "Заме́ры. Колесо бизнеса и Гэ́ллап.",
		"Тест Гэ́ллап мы переводим.":                "Тест Гэ́ллап мы переводим.",
		"Продажи. CRM лидов, запись на разбор.":     "Продажи. Си Ар Эм лидов, запись на разбо́р.",
		"запишитесь на экспресс-разбор":             "запишитесь на экспресс-разбо́р",
		"И трекинг. Трекер видит, саммари в тенге.": "И трэ́кинг. Трэ́кер видит, са́ммари в те́нге.",
		"Клуб в Алматы.":                            "Клуб в Алматы́.",
		"С Рустамом и Береке на разборе.":           "С Рустамом и Береке́ на разбо́ре.",
		"BS. Цели и задачи, рекомендации ИИ.":       "Би Эс. Цели и задачи, рекомендации искусственного интеллекта.",
		"Замерить нельзя, замереть можно.":          "Замерить нельзя, замереть можно.",
		"  Добро   пожаловать в Business Surgery ":  "Добро пожаловать в Business Surgery",
	} {
		if got := SpeakText(in); got != want {
			t.Errorf("SpeakText(%q) = %q, want %q", in, got, want)
		}
	}
}

func r62Tone(t *testing.T, ff string, db float64) []byte {
	t.Helper()
	out, e, err := runFF(context.Background(), ff, nil, "-hide_banner", "-nostats", "-f", "lavfi",
		"-i", "aevalsrc=0.5*sin(2*PI*220*t)*(0.6+0.4*sin(2*PI*3*t)):s=44100:d=2.5",
		"-af", fmt.Sprintf("volume=%gdB", db),
		"-ac", "1", "-c:a", "libmp3lame", "-b:a", "128k", "-f", "mp3", "pipe:1")
	if err != nil {
		t.Fatalf("tone: %v %s", err, lastLines(e, 3))
	}
	return out
}

// R62: «Шаг 7 в озвучке очень тихо». A quiet take and a loud one come out
// at the same loudness, under the peak.
func TestR62NormalizeMP3(t *testing.T) {
	ff, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("no ffmpeg")
	}
	ctx := context.Background()
	for _, db := range []float64{-30, -4} {
		in := r62Tone(t, ff, db)
		before, err := MeasureLoudness(ctx, ff, in)
		if err != nil {
			t.Fatal(err)
		}
		out, b2, after, err := NormalizeMP3(ctx, ff, in)
		if err != nil {
			t.Fatal(err)
		}
		got, err := MeasureLoudness(ctx, ff, out)
		if err != nil {
			t.Fatal(err)
		}
		if math.Abs(b2.I-before.I) > 0.1 {
			t.Fatalf("before %v vs %v", b2, before)
		}
		if math.Abs(got.I-normTarget) > 1 || got.TP > normPeak+0.6 {
			t.Fatalf("%g dB: before %v, after %v (loudnorm says %v)", db, before, got, after)
		}
	}
	if _, err := MeasureLoudness(ctx, ff, []byte("not audio")); err == nil {
		t.Fatal("garbage measured")
	}
}

// R62: the premium files. A phrase the dictionary changes is read again
// (with the stress mark), its earlier file plays meanwhile; every kept file,
// old or new, gets a normalized copy under a new URL; the pages play those.
func TestR62PremiumLoudnessAndStress(t *testing.T) {
	ff, ferr := exec.LookPath("ffmpeg")
	if ferr != nil {
		t.Skip("no ffmpeg")
	}
	repo, ctx := testPlatformDB(t, elevenKeyDoc, premiumCfgDoc)
	db, err := pg.NewDB(ctx, os.Getenv("BS_TEST_DSN"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Pool.Close()
	if _, err := db.Pool.Exec(ctx, `DELETE FROM tts_audio WHERE style = ANY($1)`, []string{premiumStyle, loginStyle, premiumStyle + "n", loginStyle + "n"}); err != nil {
		t.Fatal(err)
	}
	t.Setenv("ELEVENLABS_API_KEY", "sk_r62_test______0123456789abcdef0123456789abcdef01")
	t.Setenv("ELEVENLABS_VOICE_ID", "")
	quiet, loud := r62Tone(t, ff, -32), r62Tone(t, ff, -6)
	var mu sync.Mutex
	var asked []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var b struct {
			Text string `json:"text"`
		}
		_ = json.NewDecoder(r.Body).Decode(&b)
		mu.Lock()
		asked = append(asked, b.Text)
		mu.Unlock()
		w.Header().Set("Content-Type", "audio/mpeg")
		_, _ = w.Write(quiet)
	}))
	defer srv.Close()
	oldPace := PremiumPace
	PremiumPace = time.Millisecond
	defer func() { PremiumPace = oldPace }()

	tour := []string{"Привет.", "Раз в цикл вы делаете замер."}
	login := []string{"Это ваш кабинет.", "Замеры раз в цикл."}
	p := NewPremiumVoice(repo, func() []byte { return []byte("jwt-secret-for-tests-0123456789") }, func() []string { return tour })
	p.EL = &ai.Eleven{Base: srv.URL, HTTP: srv.Client(), Key: p.key, Wait: func(int, time.Duration) time.Duration { return time.Millisecond }}
	v := premiumVoice{ID: "voiceR62aaaaaaaaaaaaa", Name: "Тест", Model: ai.ElevenModel(), Settings: ai.DefaultElevenSettings}
	if err := p.saveCfg(ctx, premiumCfg{Voice: v, Active: v, EnvVoice: defaultElevenVoice}); err != nil {
		t.Fatal(err)
	}
	// before R62: the files were keyed by the shown text, loud and quiet takes
	for i, txt := range tour {
		a := loud
		if i == 1 {
			a = quiet
		}
		if err := repo.PutTTSMime(ctx, premiumKey(v, txt), "elevenlabs:"+v.ID, premiumStyle, txt, "audio/mpeg", a); err != nil {
			t.Fatal(err)
		}
	}
	p.SetLoginTexts(func() []string { return login })
	p.refreshOverlay(ctx)
	_, m0 := p.Overlay()
	if m0[tour[1]] != premiumURLPath+premiumKey(v, tour[1])+".mp3" {
		t.Fatalf("the earlier file must play until the phrase is read again: %v", m0)
	}
	if _, n, _ := p.have(ctx, v, tour); n != 1 {
		t.Fatalf("ready %d, want 1 (the замер phrase is to be read again)", n)
	}

	p.SetFFmpeg(func(context.Context) (string, error) { return ff, nil })
	settle := func() {
		for i := 0; i < 1500; i++ {
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
	p.maybeRun(ctx)
	settle()
	p.maybeRunLogin(ctx)
	settle()
	p.normalizeKept(ctx)

	mu.Lock()
	got := append([]string(nil), asked...)
	mu.Unlock()
	want := map[string]bool{"Раз в цикл вы делаете заме́р.": true, "Это ваш кабинет.": true, "Заме́ры раз в цикл.": true}
	if len(got) != 3 {
		t.Fatalf("ElevenLabs asked %q", got)
	}
	for _, g := range got {
		if !want[g] {
			t.Fatalf("ElevenLabs asked %q, want the stressed texts %v", g, want)
		}
	}

	gin.SetMode(gin.TestMode)
	r := gin.New()
	p.Register(r.Group("/api/v1/platform"), r.Group("/api/v1/platform"))
	fetch := func(u string) []byte {
		w := httptest.NewRecorder()
		r.ServeHTTP(w, httptest.NewRequest("GET", u, nil))
		if w.Code != 200 {
			t.Fatalf("%s: %d", u, w.Code)
		}
		return w.Body.Bytes()
	}
	check := func(what string, m map[string]string, base string, texts []string, vv premiumVoice) {
		for _, txt := range texts {
			u := m[txt]
			k := normKey(premiumKey(vv, spokenKey(txt)))
			if u != base+k+".mp3" {
				t.Fatalf("%s %q: %s, want the normalized copy %s", what, txt, u, k)
			}
			l, err := MeasureLoudness(ctx, ff, fetch(u))
			if err != nil || math.Abs(l.I-normTarget) > 1 || l.TP > normPeak+0.6 {
				t.Fatalf("%s %q: %v %v", what, txt, l, err)
			}
		}
	}
	_, tm := p.Overlay()
	check("tour", tm, premiumURLPath, tour, v)
	_, lm := p.LoginOverlay()
	check("login", lm, loginURLPath, login, p.loginVoice())

	// a restart: nothing is read or normalized again
	p2 := NewPremiumVoice(repo, p.Secret, func() []string { return tour })
	p2.EL = p.EL
	p2.SetLoginTexts(func() []string { return login })
	p2.Load(ctx)
	p2.SetFFmpeg(func(context.Context) (string, error) { t.Fatal("ffmpeg asked after a restart"); return "", nil })
	p2.normalizeKept(ctx)
	p2.maybeRun(ctx)
	p2.maybeRunLogin(ctx)
	if v1, _ := p.LoginOverlay(); v1 == "" {
		t.Fatal("no login overlay")
	} else if v2, _ := p2.LoginOverlay(); v2 != v1 {
		t.Fatalf("login overlay after restart %q, want %q", v2, v1)
	}
	mu.Lock()
	if len(asked) != 3 {
		t.Fatalf("read again after a restart: %q", asked[3:])
	}
	mu.Unlock()
}
