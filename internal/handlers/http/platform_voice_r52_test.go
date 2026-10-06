package http

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"regexp"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	pg "github.com/bnursik/business_surgery_backend/internal/repository/pg"
	"github.com/bnursik/business_surgery_backend/web"
	"github.com/gin-gonic/gin"
	"github.com/golang-jwt/jwt/v5"
)

// R52: «Озвучка: почему в кнопке «Обучение» не поменялась на ту, что на главной
// до входа?». The voice was ready (status: 33 из 33), the login demo spoke
// with it, but after a restart the tour kept the built-in Piper files: Load
// looked for the tour's map before app.go wired the tour's phrases, found
// none and nothing looked again (every phrase was there, no reading ran).
// Now the start loop and the page request look again, and the served pages
// carry both ElevenLabs maps.
func TestR52TourVoiceAfterRestart(t *testing.T) {
	repo, ctx := testPlatformDB(t, elevenKeyDoc, premiumCfgDoc)
	db, err := pg.NewDB(ctx, os.Getenv("BS_TEST_DSN"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Pool.Close()
	if _, err := db.Pool.Exec(ctx, `DELETE FROM tts_audio WHERE style = ANY($1)`, []string{premiumStyle, loginStyle}); err != nil {
		t.Fatal(err)
	}
	t.Setenv("ELEVENLABS_API_KEY", "")
	t.Setenv("ELEVENLABS_VOICE_ID", "")
	tour, login := web.TourTexts(), web.LoginLines()
	if len(tour) < 10 || len(login) < 3 {
		t.Fatalf("tour %d, login %d", len(tour), len(login))
	}
	// Before the restart: Daniel has every phrase of the tour and of the demo.
	daniel := premiumVoice{ID: "onwK4e9ZLuTAKqWW03F9", Name: "Daniel", Model: "eleven_multilingual_v2"}
	for _, x := range tour {
		if err := repo.PutTTSMime(ctx, premiumKey(daniel, x), "elevenlabs:"+daniel.ID, premiumStyle, x, "audio/mpeg", []byte("ID3 tour "+x)); err != nil {
			t.Fatal(err)
		}
	}
	lv := daniel
	lv.Settings.Speed = LoginSpeed
	for _, x := range login {
		if err := repo.PutTTSMime(ctx, premiumKey(lv, x), "elevenlabs:"+daniel.ID, loginStyle, x, "audio/mpeg", []byte("ID3 login "+x)); err != nil {
			t.Fatal(err)
		}
	}
	cfg, _ := json.Marshal(premiumCfg{Voice: daniel, Active: daniel})
	if !putServerDoc(ctx, repo, premiumCfgDoc, string(cfg), false, "test") {
		t.Fatal("cfg not saved")
	}

	// The restart, in the order of app.go: the module loads the voice, the
	// tour's phrases are wired afterwards.
	var wired atomic.Bool
	secret := func() []byte { return []byte("jwt-secret-for-tests-0123456789") }
	p := NewPremiumVoice(repo, secret, func() []string {
		if !wired.Load() {
			return nil
		}
		return tour
	})
	p.Load(ctx)
	if v, _ := p.Overlay(); v != "" {
		t.Fatalf("no phrases yet, still a map %q", v)
	}
	p.overlayTry.Store(0)
	wired.Store(true)
	p.SetLoginTexts(web.LoginLines)
	p.refreshLoginOverlay(ctx)

	// 1. A page request finds the map at once (no wait for the 15 min loop).
	ver, m := p.Overlay()
	if ver == "" || len(m) != len(tour) {
		t.Fatalf("tour map after restart: %q, %d of %d", ver, len(m), len(tour))
	}
	for _, x := range tour {
		if !strings.HasPrefix(m[x], premiumURLPath+"el_") {
			t.Fatalf("phrase %q plays %q", x, m[x])
		}
	}
	// 2. The start loop alone finds it too.
	p2 := NewPremiumVoice(repo, secret, func() []string { return tour })
	p2.overlayTry.Store(time.Now().UnixNano()) // the page path is not taken
	p2.Load(ctx)
	p2.overlay.Store(nil)
	sctx, cancel := context.WithCancel(ctx)
	p2.Start(sctx)
	for i := 0; i < 200 && p2.overlay.Load() == nil; i++ {
		time.Sleep(10 * time.Millisecond)
	}
	cancel()
	if o := p2.overlay.Load(); o == nil || len(o.m) != len(tour) {
		t.Fatal("the start loop did not find the ready voice")
	}

	// 3. Both pages as the server sends them: the tour and the login demo play Daniel.
	oldV, oldL := web.VoiceOverlay, web.LoginVoiceOverlay
	web.VoiceOverlay, web.LoginVoiceOverlay = p.Overlay, p.LoginOverlay
	defer func() { web.VoiceOverlay, web.LoginVoiceOverlay = oldV, oldL }()
	gin.SetMode(gin.TestMode)
	r := gin.New()
	web.Register(r, "jwt-secret-for-pages-0123456789", "bs_session")
	tok, _ := jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.MapClaims{"typ": "access", "role": "admin", "sub": "1", "exp": time.Now().Add(time.Hour).Unix()}).SignedString([]byte("jwt-secret-for-pages-0123456789"))
	get := func(cookie bool) string {
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		if cookie {
			req.AddCookie(&http.Cookie{Name: "bs_session", Value: tok})
		}
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		if w.Code != 200 {
			t.Fatalf("page %d", w.Code)
		}
		return w.Body.String()
	}
	mapOf := func(page, id string) map[string]string {
		mm := regexp.MustCompile(`<script type="application/json" id="` + id + `">(.*?)</script>`).FindStringSubmatch(page)
		if mm == nil {
			t.Fatalf("%s not in the page", id)
		}
		out := map[string]string{}
		if err := json.Unmarshal([]byte(mm[1]), &out); err != nil {
			t.Fatal(err)
		}
		return out
	}
	app := mapOf(get(true), "bsVoiceStatic")
	for _, x := range tour {
		if !strings.HasPrefix(app[x], premiumURLPath) {
			t.Fatalf("the tour plays %q for %q (Piper is /voice/…)", app[x], x)
		}
	}
	lg := mapOf(get(false), "bsLoginVoice")
	for _, x := range login {
		if !strings.HasPrefix(lg[x], loginURLPath) {
			t.Fatalf("the login demo plays %q for %q", lg[x], x)
		}
	}
	// the same voice: the files of both maps belong to Daniel
	var n int
	if err := db.Pool.QueryRow(ctx, `SELECT count(*) FROM tts_audio WHERE voice = $1`, "elevenlabs:"+daniel.ID).Scan(&n); err != nil || n != len(tour)+len(login) {
		t.Fatalf("Daniel's files %d, %v", n, err)
	}
}
