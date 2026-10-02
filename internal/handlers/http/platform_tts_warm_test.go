package http

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/bnursik/business_surgery_backend/internal/ai"
	"github.com/gin-gonic/gin"
)

// The tour's phrases are made ahead of time, one at a time, through a slow
// TTS that rate-limits some calls and fails one phrase outright once; a
// listener asking for a phrase meanwhile is served first; every phrase ends
// up kept, each made once.
func TestTTSWarmMakesTheTourAhead(t *testing.T) {
	repo, db := platformTestRepo(t)
	ctx := context.Background()
	_, _ = db.Pool.Exec(ctx, `DELETE FROM platform_files WHERE id LIKE 'tts\_%'`)
	t.Setenv("AI_GEMINI_TTS_MODEL", "gemini-3.1-flash-tts")
	t.Setenv("AI_TTS_VOICE", "")
	ai.TTSBackoff = func(time.Duration) time.Duration { return 20 * time.Millisecond }
	oldGap, oldRetry := ttsWarmGap, ttsWarmRetry
	ttsWarmGap, ttsWarmRetry = 10*time.Millisecond, 50*time.Millisecond
	defer func() {
		ai.TTSBackoff = func(d time.Duration) time.Duration { return d }
		ttsWarmGap, ttsWarmRetry = oldGap, oldRetry
	}()

	var mu sync.Mutex
	calls, inFlight, maxInFlight := 0, 0, 0
	made := map[string]int{}
	failedOnce := false
	pcm := make([]byte, 480)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		var body struct {
			Contents []struct {
				Parts []struct{ Text string } `json:"parts"`
			} `json:"contents"`
		}
		_ = json.Unmarshal(b, &body)
		text := body.Contents[0].Parts[0].Text
		mu.Lock()
		calls++
		n := calls
		inFlight++
		if inFlight > maxInFlight {
			maxInFlight = inFlight
		}
		fail := strings.Contains(text, "шаг 4") && !failedOnce
		if fail {
			failedOnce = true
		}
		mu.Unlock()
		defer func() { mu.Lock(); inFlight--; mu.Unlock() }()
		time.Sleep(60 * time.Millisecond) // slow model
		switch {
		case n%3 == 0: // rate limit on every third call
			w.WriteHeader(429)
			_, _ = w.Write([]byte(`{"error":{"code":429,"message":"Resource has been exhausted (e.g. check quota)."}}`))
			return
		case fail:
			w.WriteHeader(500)
			_, _ = w.Write([]byte(`{"error":{"code":500,"message":"internal"}}`))
			return
		}
		mu.Lock()
		made[text]++
		mu.Unlock()
		_ = json.NewEncoder(w).Encode(map[string]any{"candidates": []any{map[string]any{"content": map[string]any{"parts": []any{
			map[string]any{"inlineData": map[string]any{"mimeType": "audio/L16;codec=pcm;rate=24000", "data": base64.StdEncoding.EncodeToString(pcm)}}}}}}})
	}))
	defer srv.Close()
	h := NewPlatformAI(repo, &ai.Client{Gemini: "k", GeminiModel: "gemini-3.5-flash", GeminiBase: srv.URL, HTTP: srv.Client()})
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(func(c *gin.Context) { c.Set("role", "resident"); c.Set("userID", "tg:1") })
	r.POST("/tts", h.TTS)
	r.POST("/tts/warm", h.TTSWarm)
	post := func(path string, v any) *httptest.ResponseRecorder {
		jb, _ := json.Marshal(v)
		w := httptest.NewRecorder()
		req := httptest.NewRequest("POST", path, strings.NewReader(string(jb)))
		req.Header.Set("Content-Type", "application/json")
		r.ServeHTTP(w, req)
		return w
	}
	var texts []string
	for i := 1; i <= 9; i++ {
		texts = append(texts, fmt.Sprintf("Обучение, шаг %d: раздел платформы.", i))
	}
	warm := func() (ready int, items []bool) {
		var out struct {
			Ready int    `json:"ready"`
			Items []bool `json:"items"`
		}
		_ = json.Unmarshal(post("/tts/warm", map[string]any{"texts": append(texts, texts[0], "  ")}).Body.Bytes(), &out)
		return out.Ready, out.Items
	}
	if n, items := warm(); n != 0 || len(items) != 11 {
		t.Fatalf("first warm: %d %v", n, items)
	}
	// A listener on the last step meanwhile: served, not after the whole queue.
	start := time.Now()
	if w := post("/tts", map[string]string{"text": texts[8]}); w.Code != 200 {
		t.Fatalf("listener: %d %s", w.Code, w.Body.String())
	}
	if d := time.Since(start); d > 2*time.Second {
		t.Fatalf("listener waited %v", d)
	}
	deadline := time.Now().Add(20 * time.Second)
	for {
		n, _ := warm()
		if n == 10 { // nine phrases, the first twice
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("not warmed: %d ready, made %v", n, made)
		}
		time.Sleep(50 * time.Millisecond)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(made) != 9 || maxInFlight != 1 || !failedOnce {
		t.Fatalf("made %d phrases, %d at once, failed once %v", len(made), maxInFlight, failedOnce)
	}
	for tx, n := range made {
		if n != 1 {
			t.Fatalf("%q made %d times", tx, n)
		}
	}
	// Every phrase is now a cache hit.
	for _, tx := range texts {
		if w := post("/tts", map[string]string{"text": tx}); w.Code != 200 || w.Header().Get("X-TTS-Cache") != "hit" {
			t.Fatalf("%q: %d %s", tx, w.Code, w.Header().Get("X-TTS-Cache"))
		}
	}
}
