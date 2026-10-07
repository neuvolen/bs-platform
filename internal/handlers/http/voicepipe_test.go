package http

import (
	"crypto/rand"
	"crypto/rsa"
	"encoding/base64"
	"encoding/json"
	"io"
	"math/big"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/bnursik/business_surgery_backend/internal/ai"
	"github.com/gin-gonic/gin"
	"github.com/golang-jwt/jwt/v5"
)

// R57: the voice workflow of GitHub Actions reaches ElevenLabs only through
// the server: a GitHub OIDC token of our repo and branch opens a short list
// of calls, the key goes from the server and is never in the answer.
func TestR57VoicePipe(t *testing.T) {
	gin.SetMode(gin.TestMode)
	t.Setenv("VOICEPIPE", "")
	t.Setenv("VOICEPIPE_REPO", "")
	t.Setenv("VOICEPIPE_REF", "")
	pk, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	other, _ := rsa.GenerateKey(rand.Reader, 2048)
	jwks := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		e := big.NewInt(int64(pk.E)).Bytes()
		_ = json.NewEncoder(w).Encode(gin.H{"keys": []gin.H{{"kty": "RSA", "kid": "k1", "alg": "RS256",
			"n": base64.RawURLEncoding.EncodeToString(pk.N.Bytes()), "e": base64.RawURLEncoding.EncodeToString(e)}}})
	}))
	defer jwks.Close()
	const key = "sk_r57_server_key_0123456789abcdef0123456789abcdef"
	var calls atomic.Int32
	var gotKey, gotPath, gotQuery, gotBody string
	el := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		gotKey, gotPath, gotQuery = r.Header.Get("xi-api-key"), r.URL.Path, r.URL.RawQuery
		b, _ := io.ReadAll(r.Body)
		gotBody = string(b)
		if r.URL.Path == "/v1/user/subscription" {
			w.WriteHeader(401)
			_, _ = w.Write([]byte(`{"detail":{"status":"missing_permissions"}}`))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"audio_base64":"SUQz","alignment":{"characters":["Д"]}}`))
	}))
	defer el.Close()
	vp := NewVoicePipe(func() string { return key })
	vp.Base, vp.JWKSURL = el.URL, jwks.URL
	r := gin.New()
	r.POST("/api/v1/platform/voicepipe/eleven", vp.Eleven)

	tok := func(signer *rsa.PrivateKey, mod func(m jwt.MapClaims)) string {
		m := jwt.MapClaims{"iss": ghOIDCIssuer, "aud": voicepipeAud, "exp": time.Now().Add(5 * time.Minute).Unix(),
			"iat": time.Now().Unix(), "repository": "neuvolen/bs-platform", "repository_owner": "neuvolen",
			"ref": "refs/heads/voice-jobs", "event_name": "push", "actor": "neuvolen", "run_id": "42"}
		if mod != nil {
			mod(m)
		}
		tk := jwt.NewWithClaims(jwt.SigningMethodRS256, m)
		tk.Header["kid"] = "k1"
		s, err := tk.SignedString(signer)
		if err != nil {
			t.Fatal(err)
		}
		return s
	}
	do := func(token, body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest("POST", "/api/v1/platform/voicepipe/eleven", strings.NewReader(body))
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		return w
	}
	tts := `{"method":"POST","path":"/v1/text-to-speech/ogi2DyUAKJb7CEdqqvlU/with-timestamps","query":{"output_format":"mp3_44100_128","xi-api-key":"evil"},"body":{"text":"Выручка растёт","model_id":"eleven_multilingual_v2"}}`

	// the right token: forwarded with the server's key, the answer passes through
	w := do(tok(pk, nil), tts)
	if w.Code != 200 || w.Header().Get("X-Voicepipe") != "eleven" || !strings.Contains(w.Body.String(), "audio_base64") {
		t.Fatalf("tts: %d %s", w.Code, w.Body.String())
	}
	if gotKey != key || gotPath != "/v1/text-to-speech/ogi2DyUAKJb7CEdqqvlU/with-timestamps" || gotQuery != "output_format=mp3_44100_128" || !strings.Contains(gotBody, "Выручка") {
		t.Fatalf("forwarded: path %s query %s body %s key ok %v", gotPath, gotQuery, gotBody, gotKey == key)
	}
	if strings.Contains(w.Body.String(), key) {
		t.Fatal("key in the answer")
	}
	// ElevenLabs' errors keep their status
	if w := do(tok(pk, nil), `{"method":"GET","path":"/v1/user/subscription"}`); w.Code != 401 || !strings.Contains(w.Body.String(), "missing_permissions") {
		t.Fatalf("subscription: %d %s", w.Code, w.Body.String())
	}
	n := calls.Load()
	// refused: no token, another key, a fork, another branch, a pull request,
	// another audience, an expired token, a call off the list
	bad := map[string]*httptest.ResponseRecorder{
		"no token":    do("", tts),
		"other key":   do(tok(other, nil), tts),
		"fork":        do(tok(pk, func(m jwt.MapClaims) { m["repository"], m["repository_owner"] = "evil/bs-platform", "evil" }), tts),
		"branch":      do(tok(pk, func(m jwt.MapClaims) { m["ref"] = "refs/heads/main" }), tts),
		"pr":          do(tok(pk, func(m jwt.MapClaims) { m["event_name"] = "pull_request" }), tts),
		"audience":    do(tok(pk, func(m jwt.MapClaims) { m["aud"] = "sts.amazonaws.com" }), tts),
		"expired":     do(tok(pk, func(m jwt.MapClaims) { m["exp"] = time.Now().Add(-time.Hour).Unix() }), tts),
		"delete":      do(tok(pk, nil), `{"method":"DELETE","path":"/v1/voices/ogi2DyUAKJb7CEdqqvlU"}`),
		"api keys":    do(tok(pk, nil), `{"method":"GET","path":"/v1/workspace/api-keys"}`),
		"path escape": do(tok(pk, nil), `{"method":"GET","path":"/v1/voices/../user"}`),
	}
	for name, w := range bad {
		if w.Code < 400 || w.Code >= 500 {
			t.Errorf("%s: %d", name, w.Code)
		}
	}
	if calls.Load() != n {
		t.Fatalf("a refused call reached ElevenLabs: %d", calls.Load()-n)
	}
	// off
	t.Setenv("VOICEPIPE", "off")
	if w := do(tok(pk, nil), tts); w.Code != 404 {
		t.Fatalf("off: %d", w.Code)
	}
}

// R57: a plan bought while the server ran: the start probes the chosen voice
// at once instead of waiting for the hourly probe.
func TestR57StartProbesTarget(t *testing.T) {
	repo, ctx := testPlatformDB(t, elevenKeyDoc, premiumCfgDoc)
	t.Setenv("ELEVENLABS_API_KEY", "sk_r57_paid_____0123456789abcdef0123456789abcdef01")
	var probes atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/v1/text-to-speech/") {
			probes.Add(1)
			w.Header().Set("Content-Type", "audio/mpeg")
			_, _ = w.Write(append([]byte("ID3"), make([]byte, 600)...))
			return
		}
		w.WriteHeader(404)
	}))
	defer srv.Close()
	p := NewPremiumVoice(repo, func() []byte { return []byte("jwt-secret-for-tests-0123456789") }, func() []string { return nil })
	p.EL = &ai.Eleven{Base: srv.URL, HTTP: srv.Client(), Key: p.key, Wait: func(int, time.Duration) time.Duration { return time.Millisecond }}
	now := time.Now().UTC().Format(time.RFC3339)
	target := premiumVoice{ID: defaultElevenVoice, Name: "голос владельца", Model: "eleven_multilingual_v2"}
	c := premiumCfg{Voice: target, Fallback: &premiumFallback{For: target.ID, Why: "paid_plan_required", At: now, Checked: now,
		Voice: premiumVoice{ID: defaultFallbackVoice, Name: "Daniel", Model: "eleven_multilingual_v2"}}}
	if err := p.saveCfg(ctx, c); err != nil {
		t.Fatal(err)
	}
	// probed a minute ago: the hourly loop waits
	if p.recheckTarget(ctx) || probes.Load() != 0 {
		t.Fatalf("probed before the hour: %d", probes.Load())
	}
	// a start: probed at once, the plan reads it now: the fallback is dropped
	p.probeNow.Store(true)
	if !p.recheckTarget(ctx) || probes.Load() != 1 {
		t.Fatalf("start probe: %d", probes.Load())
	}
	if got := p.config(); got.fallbackOn() || got.effective().ID != defaultElevenVoice {
		t.Fatalf("fallback kept: %+v", got.Fallback)
	}
	if p.probeNow.Load() {
		t.Fatal("start flag not spent")
	}
}
