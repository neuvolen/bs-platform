package http

import (
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

// R38c: the prod case: a correctly made key restricted to «Text to Speech».
// The server must not ask /v1/voices/{id} or /v1/user (they need Voices: Read
// and User: Read) to decide the key is wrong: «Тест» is read with the voice
// directly, the name is a nicety, «Проверить» reads «Тест» end to end.
func TestR38cElevenRestrictedKey(t *testing.T) {
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
	const ttsOnly = "sk_r38c_tts_only_0123456789abcdef0123456789abcdef01"
	const noTTS = "sk_r38c_no_tts___0123456789abcdef0123456789abcdef01"
	const full = "sk_r38c_full_____0123456789abcdef0123456789abcdef01"
	var mu sync.Mutex
	hits := map[string]int{}
	perm := func(w http.ResponseWriter, p string) {
		w.WriteHeader(401)
		_, _ = w.Write([]byte(`{"detail":{"status":"missing_permissions","message":"The API key you used is missing the permission ` + p + ` to execute this operation."}}`))
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		hits[r.URL.Path]++
		mu.Unlock()
		k := r.Header.Get("xi-api-key")
		tts := strings.HasPrefix(r.URL.Path, "/v1/text-to-speech/")
		switch {
		case k != ttsOnly && k != noTTS && k != full:
			w.WriteHeader(401)
			_, _ = w.Write([]byte(`{"detail":{"status":"invalid_api_key","message":"Invalid API key"}}`))
		case tts && k == noTTS:
			perm(w, "text_to_speech")
		case tts && strings.HasSuffix(r.URL.Path, "/libOnly0000000000001"):
			w.WriteHeader(404)
			_, _ = w.Write([]byte(`{"detail":{"status":"voice_not_found","message":"A voice with the voice_id libOnly0000000000001 was not found."}}`))
		case tts:
			w.Header().Set("Content-Type", "audio/mpeg")
			_, _ = w.Write(append([]byte("ID3"), make([]byte, 600)...))
		case k != full && (r.URL.Path == "/v1/user/subscription" || r.URL.Path == "/v1/user"):
			perm(w, "user_read")
		case k == ttsOnly && strings.HasPrefix(r.URL.Path, "/v1/voices/add/"):
			perm(w, "voices_write")
		case k == ttsOnly && (strings.HasPrefix(r.URL.Path, "/v1/voices") || r.URL.Path == "/v1/shared-voices"):
			if r.URL.Path == "/v1/shared-voices" {
				_, _ = w.Write([]byte(`{"voices":[{"voice_id":"libOnly0000000000001","public_owner_id":"ownL","name":"Library Baritone"}]}`))
				return
			}
			perm(w, "voices_read")
		case r.URL.Path == "/v1/voices/ogi2DyUAKJb7CEdqqvlU":
			_, _ = w.Write([]byte(`{"voice_id":"ogi2DyUAKJb7CEdqqvlU","name":"Голос владельца"}`))
		case r.URL.Path == "/v1/user/subscription":
			_, _ = w.Write([]byte(`{"tier":"starter","character_count":1000,"character_limit":30000}`))
		default:
			w.WriteHeader(404)
		}
	}))
	defer srv.Close()
	secret := func() []byte { return []byte("jwt-secret-for-tests-0123456789") }
	mk := func() *PremiumVoice {
		p := NewPremiumVoice(repo, secret, func() []string { return []string{"Раз."} })
		p.EL = &ai.Eleven{Base: srv.URL, HTTP: srv.Client(), Key: p.key, Wait: func(int, time.Duration) time.Duration { return time.Millisecond }}
		p.Load(ctx)
		return p
	}
	check := func(p *PremiumVoice) CheckItem {
		s := &SysCheck{Premium: p, Builtin: func() (int, int) { return 33, 33 }}
		return s.voice(ctx)
	}
	t.Setenv("ELEVENLABS_VOICE_ID", "")

	// 1. the key lacks Text to Speech: the check says exactly that, not «не принят»
	t.Setenv("ELEVENLABS_API_KEY", noTTS)
	p := mk()
	p.applyEnvVoice(ctx)
	it := check(p)
	if it.State != "warn" || strings.Contains(it.Text, "не принят") || !strings.Contains(it.Text, "«Text to Speech: Access»") ||
		!strings.Contains(it.Text, "missing the permission text_to_speech") || strings.Contains(it.Text, noTTS) || strings.Contains(it.Text, "—") {
		t.Fatalf("no tts: %+v", it)
	}
	// not retried every 15 minutes with the same key: one «Тест» an hour
	mu.Lock()
	n0 := hits["/v1/text-to-speech/ogi2DyUAKJb7CEdqqvlU"]
	mu.Unlock()
	p.applyEnvVoice(ctx)
	mu.Lock()
	if hits["/v1/text-to-speech/ogi2DyUAKJb7CEdqqvlU"] != n0 {
		t.Fatalf("retried at once: %v", hits)
	}
	mu.Unlock()

	// 2. the prod case: Text to Speech only. Voices: Read and User: Read are
	// missing, the default voice is taken anyway, nothing says «не подключён».
	t.Setenv("ELEVENLABS_API_KEY", ttsOnly)
	p = mk()
	p.applyEnvVoice(ctx)
	if c := p.config(); c.Voice.ID != "ogi2DyUAKJb7CEdqqvlU" || c.EnvVoice != "ogi2DyUAKJb7CEdqqvlU" {
		t.Fatalf("restricted key: %+v env %q", c, p.envErr)
	}
	if it := check(p); strings.Contains(it.Text, "не подключён") || strings.Contains(it.Text, "не принят") {
		t.Fatalf("restricted: %+v", it)
	}
	// «Проверить»: «Тест» end to end, ok without User: Read
	r := gin.New()
	r.Use(func(c *gin.Context) { c.Set("role", "admin"); c.Set("platformRole", "admin"); c.Set("userID", "tg:1"); c.Next() })
	g := r.Group("/api/v1/platform")
	p.Register(g, g)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest("POST", "/api/v1/platform/tts/premium/key/test", strings.NewReader(`{}`)))
	var j map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &j)
	if j["ok"] != true || !strings.Contains(j["message"].(string), "прочитал «Тест»") || strings.Contains(w.Body.String(), ttsOnly) {
		t.Fatalf("test key: %d %s", w.Code, w.Body.String())
	}
	// a wrong key pasted for the check: «не принят» and the provider's words
	w = httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest("POST", "/api/v1/platform/tts/premium/key/test", strings.NewReader(`{"key":"sk_wrong_0123456789abcdef0123456789abcdef0123456789"}`)))
	_ = json.Unmarshal(w.Body.Bytes(), &j)
	if j["ok"] != false || !strings.Contains(j["message"].(string), "не принят") || !strings.Contains(j["message"].(string), "Invalid API key") {
		t.Fatalf("wrong key: %s", w.Body.String())
	}

	// 3. a library voice not in the account: adding it needs Voices: Write, said precisely
	t.Setenv("ELEVENLABS_VOICE_ID", "libOnly0000000000001")
	p.applyEnvVoice(ctx)
	it = check(p)
	if !strings.Contains(it.Text, "«Library Baritone» из библиотеки нужно добавить") || !strings.Contains(it.Text, "«Voices: Write»") || !strings.Contains(it.Text, "Add to my voices") {
		t.Fatalf("library voice: %+v", it)
	}

	// 4. a full key: the name of the voice and the plan come along
	t.Setenv("ELEVENLABS_VOICE_ID", "")
	t.Setenv("ELEVENLABS_API_KEY", full)
	if _, err := db.Pool.Exec(ctx, `DELETE FROM platform_docs WHERE scope = 'server' AND key = $1`, premiumCfgDoc); err != nil {
		t.Fatal(err)
	}
	p = mk()
	p.applyEnvVoice(ctx)
	if c := p.config(); c.Voice.Name != "Голос владельца" {
		t.Fatalf("full key: %+v", c)
	}
}
