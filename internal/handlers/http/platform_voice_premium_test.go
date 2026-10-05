package http

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/bnursik/business_surgery_backend/internal/ai"
	pg "github.com/bnursik/business_surgery_backend/internal/repository/pg"
	"github.com/gin-gonic/gin"
)

// R36: «Голос ElevenLabs»: the key (sealed, admin only), «Подобрать голос»,
// the pick reads every tour phrase in the background (a 429 is waited out),
// the page gets the files only when all are there, a stop on used-up
// characters keeps the previous voice playing, «Продолжить» finishes it.
func TestR36PremiumVoice(t *testing.T) {
	repo, ctx := testPlatformDB(t, elevenKeyDoc, premiumCfgDoc)
	db, err := pg.NewDB(ctx, os.Getenv("BS_TEST_DSN"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Pool.Close()
	if _, err := db.Pool.Exec(ctx, `DELETE FROM tts_audio WHERE style = $1`, premiumStyle); err != nil {
		t.Fatal(err)
	}
	t.Setenv("ELEVENLABS_API_KEY", "")
	t.Setenv("AI_KEYS_SECRET", "")
	const key = "sk_r36_eleven_test_key_0123456789abcdef"
	var mu sync.Mutex
	hits := map[string]int{}
	quota := false
	busyOnce := true
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		hits[r.URL.Path]++
		if r.Header.Get("xi-api-key") != key {
			w.WriteHeader(401)
			_, _ = w.Write([]byte(`{"detail":{"status":"invalid_api_key","message":"bad"}}`))
			return
		}
		switch {
		case r.URL.Path == "/v1/user/subscription":
			_, _ = w.Write([]byte(`{"tier":"creator","character_count":5000,"character_limit":100000}`))
		case r.URL.Path == "/v1/voices":
			_, _ = w.Write([]byte(`{"voices":[{"voice_id":"mine1","name":"Мой дворецкий","category":"generated","preview_url":"https://p/m.mp3","labels":{"gender":"male"}}]}`))
		case r.URL.Path == "/v1/shared-voices":
			_, _ = w.Write([]byte(`{"voices":[{"voice_id":"v_deep","public_owner_id":"own2","name":"Deep Butler","preview_url":"https://p/d.mp3","gender":"male","descriptive":"deep","use_case":"narrative_story"}]}`))
		case r.URL.Path == "/v1/voices/add/own2/v_deep":
			_, _ = w.Write([]byte(`{"voice_id":"added_deep"}`))
		case strings.HasPrefix(r.URL.Path, "/v1/text-to-speech/"):
			var b map[string]any
			_ = json.NewDecoder(r.Body).Decode(&b)
			if busyOnce {
				busyOnce = false
				w.Header().Set("Retry-After", "1")
				w.WriteHeader(429)
				_, _ = w.Write([]byte(`{"detail":{"status":"too_many_concurrent_requests","message":"wait"}}`))
				return
			}
			if quota && strings.HasSuffix(r.URL.Path, "/mine1") && hits[r.URL.Path] >= 3 {
				w.WriteHeader(401)
				_, _ = w.Write([]byte(`{"detail":{"status":"quota_exceeded","message":"quota"}}`))
				return
			}
			w.Header().Set("Content-Type", "audio/mpeg")
			_, _ = w.Write(append([]byte("ID3:"+strings.TrimPrefix(r.URL.Path, "/v1/text-to-speech/")+":"+b["text"].(string)), make([]byte, 300)...))
		default:
			w.WriteHeader(404)
		}
	}))
	defer srv.Close()
	old := PremiumPace
	PremiumPace = time.Millisecond
	defer func() { PremiumPace = old }()

	texts := []string{"Добро пожаловать в Business Surgery.", "Здесь ваши задачи.", "Здесь финансы.", "Здесь клуб.", "Удачи!"}
	secret := func() []byte { return []byte("jwt-secret-for-tests-0123456789") }
	mk := func() *PremiumVoice {
		p := NewPremiumVoice(repo, secret, func() []string { return texts })
		p.EL = &ai.Eleven{Base: srv.URL, HTTP: srv.Client(), Key: p.key, Wait: func(int, time.Duration) time.Duration { return time.Millisecond }}
		return p
	}
	p := mk()
	var ready []string
	p.OnReady = func(_ context.Context, name string) { mu.Lock(); ready = append(ready, name); mu.Unlock() }
	gin.SetMode(gin.TestMode)
	router := func(role string) *gin.Engine {
		r := gin.New()
		pub := r.Group("/api/v1/platform")
		g := r.Group("/api/v1/platform")
		g.Use(func(c *gin.Context) { c.Set("role", role); c.Set("userID", "tg:1") })
		p.Register(pub, g)
		return r
	}
	call := func(r *gin.Engine, method, path, body string) (int, map[string]any, string) {
		w := httptest.NewRecorder()
		req := httptest.NewRequest(method, "/api/v1/platform"+path, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		r.ServeHTTP(w, req)
		var j map[string]any
		_ = json.Unmarshal(w.Body.Bytes(), &j)
		return w.Code, j, w.Body.String()
	}
	adm := router("admin")
	for _, role := range []string{"resident", "lead", "moderator"} {
		for _, m := range [][2]string{{"GET", "/tts/premium"}, {"PUT", "/tts/premium/key"}, {"GET", "/tts/premium/voices"}, {"POST", "/tts/premium/voice"}, {"DELETE", "/tts/premium/voice"}} {
			if code, _, _ := call(router(role), m[0], m[1], `{"key":"`+key+`","id":"v_deep"}`); code != 403 {
				t.Fatalf("%s %s by %s: %d", m[0], m[1], role, code)
			}
		}
	}
	if _, j, _ := call(adm, "GET", "/tts/premium", ""); j["source"] != "" || j["playing"] != "builtin" || j["total"] != 5.0 {
		t.Fatalf("empty: %+v", j)
	}
	// no key: the pick explains what to do
	if _, j, _ := call(adm, "POST", "/tts/premium/voice", `{"id":"mine1","name":"x"}`); j["ok"] != false || !strings.Contains(j["message"].(string), "Нет ключа ElevenLabs") {
		t.Fatalf("pick without key: %+v", j)
	}
	if code, j, _ := call(adm, "PUT", "/tts/premium/key", `{"key":"короткий"}`); code != 400 || j["error"] != "bad_key" {
		t.Fatalf("bad key: %d %+v", code, j)
	}
	if _, j, _ := call(adm, "POST", "/tts/premium/key/test", `{"key":"sk_wrong_0000000000000000000"}`); j["ok"] != false || !strings.Contains(j["message"].(string), "не принят") {
		t.Fatalf("wrong key test: %+v", j)
	}
	code, j, body := call(adm, "PUT", "/tts/premium/key", `{"key":"`+key+`"}`)
	if code != 200 || j["source"] != "settings" || j["last4"] != "…cdef" || strings.Contains(body, key) {
		t.Fatalf("save key: %d %s", code, body)
	}
	d, _ := repo.GetDoc(ctx, "server", elevenKeyDoc)
	if d == nil || !strings.HasPrefix(d.Value, "v1:") || strings.Contains(d.Value, "sk_r36") {
		t.Fatalf("sealed: %+v", d)
	}
	if _, j, _ := call(adm, "POST", "/tts/premium/key/test", `{}`); j["ok"] != true || j["message"] != "Ключ работает: голос ogi2DyUAKJb7CEdqqvlU прочитал «Тест» (1 КБ звука), тариф creator, осталось символов 95 000 из 100 000" {
		t.Fatalf("test: %+v", j)
	}
	// «Подобрать голос»
	_, j, body = call(adm, "GET", "/tts/premium/voices", "")
	if j["ok"] != true || !strings.Contains(body, `"id":"mine1"`) || !strings.Contains(body, `"ownerId":"own2"`) || !strings.Contains(body, `"previewUrl":"https://p/d.mp3"`) {
		t.Fatalf("voices: %s", body)
	}
	wait := func(cond func(map[string]any) bool) map[string]any {
		var j map[string]any
		for i := 0; i < 300; i++ {
			_, j, _ = call(adm, "GET", "/tts/premium", "")
			if cond(j) {
				return j
			}
			time.Sleep(10 * time.Millisecond)
		}
		t.Fatalf("timeout: %+v", j)
		return nil
	}
	jobOf := func(j map[string]any) map[string]any { m, _ := j["job"].(map[string]any); return m }

	// pick a library voice: added to the account, then read phrase by phrase
	if _, j, _ := call(adm, "POST", "/tts/premium/voice", `{"id":"v_deep","name":"Deep Butler","ownerId":"own2","previewUrl":"https://p/d.mp3"}`); j["ok"] != true {
		t.Fatalf("pick: %+v", j)
	}
	j = wait(func(j map[string]any) bool { return j["playing"] == "premium" })
	if j["ready"] != 5.0 || hits["/v1/voices/add/own2/v_deep"] != 1 || hits["/v1/text-to-speech/added_deep"] != 5 { // R38c: the one 429 went to «Проверить»
		t.Fatalf("read: %+v hits %v", j, hits)
	}
	ver1, m1 := p.Overlay()
	if ver1 == "" || len(m1) != 5 || !strings.HasPrefix(m1[texts[0]], "/api/v1/platform/tts/p/el_") {
		t.Fatalf("overlay: %s %v", ver1, m1)
	}
	if len(ready) != 1 || ready[0] != "Deep Butler" {
		t.Fatalf("ready note: %v", ready)
	}
	// the file: public, immutable, the voice's MP3
	w := httptest.NewRecorder()
	adm.ServeHTTP(w, httptest.NewRequest("GET", m1[texts[1]], nil))
	if w.Code != 200 || w.Header().Get("Content-Type") != "audio/mpeg" || !strings.Contains(w.Header().Get("Cache-Control"), "immutable") || !strings.Contains(w.Body.String(), "added_deep:"+texts[1]) {
		t.Fatalf("file: %d %v %q", w.Code, w.Header(), w.Body.String()[:40])
	}
	for _, bad := range []string{"/api/v1/platform/tts/p/el_123.mp3", "/api/v1/platform/tts/p/tts_" + strings.Repeat("a", 48) + ".mp3"} {
		w := httptest.NewRecorder()
		adm.ServeHTTP(w, httptest.NewRequest("GET", bad, nil))
		if w.Code != 404 {
			t.Fatalf("%s: %d", bad, w.Code)
		}
	}

	// another voice, the characters run out on its 3rd phrase: stopped, the first voice keeps playing
	quota = true
	if _, j, _ := call(adm, "POST", "/tts/premium/voice", `{"id":"mine1","name":"Мой дворецкий"}`); j["ok"] != true {
		t.Fatalf("pick 2: %+v", j)
	}
	j = wait(func(j map[string]any) bool { jb := jobOf(j); return jb != nil && jb["running"] == false })
	jb := jobOf(j)
	if jb["stopped"] != "quota" || !strings.Contains(jb["error"].(string), "закончились символы") || j["ready"] != 2.0 {
		t.Fatalf("quota stop: %+v", j)
	}
	if ver, _ := p.Overlay(); ver != ver1 || j["playing"] != "premium" || j["active"].(map[string]any)["name"] != "Deep Butler" {
		t.Fatalf("the previous voice must keep playing: %s %+v", ver, j)
	}
	st := p.State(ctx)
	if !st.On || st.Voice != "Deep Butler" || st.Picked != "Мой дворецкий" || st.Error == "" {
		t.Fatalf("state: %+v", st)
	}
	// topped up: «Продолжить» reads the rest only
	quota = false
	n0 := hits["/v1/text-to-speech/mine1"]
	call(adm, "POST", "/tts/premium/run", "")
	wait(func(j map[string]any) bool {
		a, _ := j["active"].(map[string]any)
		return a != nil && a["id"] == "mine1"
	})
	if got := hits["/v1/text-to-speech/mine1"] - n0; got != 3 {
		t.Fatalf("resume made %d phrases, want the 3 missing", got)
	}
	ver2, m2 := p.Overlay()
	if ver2 == ver1 || len(m2) != 5 || m2[texts[0]] == m1[texts[0]] {
		t.Fatalf("new voice overlay: %s %v", ver2, m2)
	}

	// after a restart: the key and the choice come from the database
	p2 := mk()
	p2.Load(ctx)
	if p2.KeySource() != "settings" || p2.config().Active.ID != "mine1" {
		t.Fatalf("reload: %s %+v", p2.KeySource(), p2.config())
	}
	if v, m := p2.Overlay(); v != ver2 || len(m) != 5 {
		t.Fatalf("reload overlay: %s", v)
	}
	// the env key wins
	t.Setenv("ELEVENLABS_API_KEY", "sk_env_key_9999999999999999999")
	if p2.KeySource() != "env" {
		t.Fatal("env key")
	}
	t.Setenv("ELEVENLABS_API_KEY", "")

	// back to the built-in recordings
	if _, j, _ := call(adm, "DELETE", "/tts/premium/voice", ""); j["playing"] != "builtin" || j["voice"] != nil {
		t.Fatalf("off: %+v", j)
	}
	if v, _ := p.Overlay(); v != "" {
		t.Fatal("overlay after off")
	}
	if _, j, _ := call(adm, "DELETE", "/tts/premium/key", ""); j["source"] != "" {
		t.Fatalf("key delete: %+v", j)
	}
}

// R36c: ELEVENLABS_VOICE_ID (+ ELEVENLABS_API_KEY) in Railway: the server
// takes the voice once (an account voice, or a library one added to the
// account) and voices the tour by itself; the system check says why not.
func TestR36cEnvVoice(t *testing.T) {
	repo, ctx := testPlatformDB(t, elevenKeyDoc, premiumCfgDoc)
	db, err := pg.NewDB(ctx, os.Getenv("BS_TEST_DSN"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Pool.Close()
	if _, err := db.Pool.Exec(ctx, `DELETE FROM tts_audio WHERE style = $1`, premiumStyle); err != nil {
		t.Fatal(err)
	}
	t.Setenv("AI_KEYS_SECRET", "")
	const key = "sk_r36c_env_key_0123456789abcdef0123"
	var mu sync.Mutex
	hits := map[string]int{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		hits[r.URL.Path]++
		if r.Header.Get("xi-api-key") != key {
			w.WriteHeader(401)
			_, _ = w.Write([]byte(`{"detail":{"status":"invalid_api_key","message":"bad"}}`))
			return
		}
		switch {
		case r.URL.Path == "/v1/voices/envVoice01":
			_, _ = w.Write([]byte(`{"voice_id":"envVoice01","name":"Тимур","category":"cloned"}`))
		case r.URL.Path == "/v1/shared-voices" && r.URL.Query().Get("voice_id") == "libVoice02":
			_, _ = w.Write([]byte(`{"voices":[{"voice_id":"libVoice02","public_owner_id":"own9","name":"Lib Narrator"}]}`))
		case r.URL.Path == "/v1/shared-voices":
			_, _ = w.Write([]byte(`{"voices":[]}`))
		case r.URL.Path == "/v1/voices/add/own9/libVoice02":
			_, _ = w.Write([]byte(`{"voice_id":"addedLib02"}`))
		case r.URL.Path == "/v1/text-to-speech/libVoice02" || r.URL.Path == "/v1/text-to-speech/nopeVoice03":
			// R38c: a voice not in the account does not speak until added
			w.WriteHeader(404)
			_, _ = w.Write([]byte(`{"detail":{"type":"not_found","code":"voice_not_found","status":"voice_not_found","message":"A voice with the voice_id was not found."}}`))
		case strings.HasPrefix(r.URL.Path, "/v1/text-to-speech/"):
			w.Header().Set("Content-Type", "audio/mpeg")
			_, _ = w.Write(append([]byte("ID3:"+r.URL.Path), make([]byte, 300)...))
		default:
			w.WriteHeader(404)
			_, _ = w.Write([]byte(`{"detail":{"status":"voice_not_found","message":"not found"}}`))
		}
	}))
	defer srv.Close()
	old := PremiumPace
	PremiumPace = time.Millisecond
	defer func() { PremiumPace = old }()
	texts := []string{"Раз.", "Два.", "Три."}
	secret := func() []byte { return []byte("jwt-secret-for-tests-0123456789") }
	mk := func() *PremiumVoice {
		p := NewPremiumVoice(repo, secret, func() []string { return texts })
		p.EL = &ai.Eleven{Base: srv.URL, HTTP: srv.Client(), Key: p.key, Wait: func(int, time.Duration) time.Duration { return time.Millisecond }}
		p.Load(ctx)
		return p
	}
	check := func(p *PremiumVoice) CheckItem {
		s := &SysCheck{Premium: p, Builtin: func() (int, int) { return 33, 33 }}
		return s.voice(ctx)
	}
	wait := func(p *PremiumVoice) PremiumState {
		for i := 0; i < 300; i++ {
			if st := p.State(ctx); st.On && st.Ready == st.Total {
				return st
			}
			time.Sleep(20 * time.Millisecond)
		}
		t.Fatalf("not voiced: %+v", p.State(ctx))
		return PremiumState{}
	}

	// only the voice id: the check says the key is missing
	t.Setenv("ELEVENLABS_VOICE_ID", "envVoice01")
	t.Setenv("ELEVENLABS_API_KEY", "")
	p := mk()
	p.applyEnvVoice(ctx)
	if it := check(p); it.State != "warn" || !strings.Contains(it.Text, "встроенные записи") || !strings.Contains(it.Text, "нет ключа: добавьте ELEVENLABS_API_KEY") {
		t.Fatalf("no key: %+v", it)
	}
	// a voice id pasted as the key
	t.Setenv("ELEVENLABS_API_KEY", "envVoice01xxxxxxxxxx")
	p.applyEnvVoice(ctx)
	if it := check(p); !strings.Contains(it.Text, "в ELEVENLABS_API_KEY вставлен id голоса") {
		t.Fatalf("voice id as key: %+v", it)
	}
	// both right: taken, voiced, the check names the voice
	t.Setenv("ELEVENLABS_API_KEY", key)
	p.applyEnvVoice(ctx)
	p.maybeRun(ctx)
	st := wait(p)
	if st.Voice != "Тимур" || st.Total != 3 {
		t.Fatalf("state %+v", st)
	}
	if it := check(p); it.Text != "ElevenLabs «Тимур», готово 3 из 3 фраз" || it.State != "ok" {
		t.Fatalf("voiced: %+v", it)
	}
	// a restart: not taken again, no new reading
	mu.Lock()
	before, tts := hits["/v1/voices/envVoice01"], hits["/v1/text-to-speech/envVoice01"]
	mu.Unlock()
	p2 := mk()
	p2.applyEnvVoice(ctx)
	p2.maybeRun(ctx)
	time.Sleep(50 * time.Millisecond)
	mu.Lock()
	if hits["/v1/voices/envVoice01"] != before || hits["/v1/text-to-speech/envVoice01"] != tts {
		t.Fatalf("asked again: %v", hits)
	}
	mu.Unlock()
	if st := p2.State(ctx); !st.On || st.Voice != "Тимур" {
		t.Fatalf("after restart %+v", st)
	}
	// a library voice id: added to the account, then voiced
	t.Setenv("ELEVENLABS_VOICE_ID", "libVoice02")
	p2.applyEnvVoice(ctx)
	if c := p2.config(); c.Voice.ID != "addedLib02" || c.Voice.LibraryID != "libVoice02" || c.EnvVoice != "libVoice02" {
		t.Fatalf("library voice %+v", c)
	}
	// an id ElevenLabs does not know
	t.Setenv("ELEVENLABS_VOICE_ID", "nopeVoice03")
	p2.applyEnvVoice(ctx)
	if it := check(p2); !strings.Contains(it.Text, "голос nopeVoice03 не найден ни в вашем аккаунте") || it.State != "warn" {
		t.Fatalf("unknown voice: %+v", it)
	}
	if strings.Contains(check(p2).Text, "—") {
		t.Fatal("em dash")
	}
}
