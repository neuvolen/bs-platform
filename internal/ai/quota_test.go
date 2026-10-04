package ai

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

const dailyQuota = `{"error":{"code":429,"message":"You exceeded your current quota, please check your plan and billing details.","status":"RESOURCE_EXHAUSTED","details":[{"@type":"type.googleapis.com/google.rpc.QuotaFailure","violations":[{"quotaMetric":"generativelanguage.googleapis.com/generate_content_free_tier_requests","quotaId":"GenerateRequestsPerDayPerProjectPerModel-FreeTier"}]},{"@type":"type.googleapis.com/google.rpc.RetryInfo","retryDelay":"37s"}]}}`

const minuteQuota = `{"error":{"code":429,"message":"You exceeded your current quota.","status":"RESOURCE_EXHAUSTED","details":[{"@type":"type.googleapis.com/google.rpc.QuotaFailure","violations":[{"quotaId":"GenerateRequestsPerMinutePerProjectPerModel-FreeTier"}]},{"@type":"type.googleapis.com/google.rpc.RetryInfo","retryDelay":"300s"}]}}`

func TestParseQuota(t *testing.T) {
	q, ok := ParseQuota(&HTTPError{Status: 429, Body: dailyQuota})
	if !ok || !q.Daily || q.Retry != 37*time.Second {
		t.Fatalf("daily: %+v %v", q, ok)
	}
	q, ok = ParseQuota(&HTTPError{Status: 429, Body: minuteQuota})
	if !ok || q.Daily || q.Retry != 300*time.Second {
		t.Fatalf("minute: %+v %v", q, ok)
	}
	if _, ok := ParseQuota(&HTTPError{Status: 429, Body: `{"error":{"message":"slow down"}}`}); ok {
		t.Fatal("a plain rate limit is not a quota")
	}
	if _, ok := ParseQuota(&HTTPError{Status: 503, Body: "quota"}); ok {
		t.Fatal("503 is not a quota")
	}
	// Daily quota: back at the next midnight Pacific time.
	now := time.Date(2026, 10, 3, 12, 0, 0, 0, time.UTC) // 05:00 PDT
	n := nextPacificMidnight(now)
	if n.In(pacific).Day() != 4 || n.In(pacific).Hour() != 0 || !n.After(now) {
		t.Fatalf("midnight: %v", n.In(pacific))
	}
}

// The speech quota is used up: one call, then nothing until it is back.
func TestSpeakStopsOnDailyQuota(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.WriteHeader(429)
		_, _ = w.Write([]byte(dailyQuota))
	}))
	defer srv.Close()
	t.Setenv("AI_GEMINI_TTS_MODEL", "gemini-tts")
	TTSBackoff = func(time.Duration) time.Duration { return time.Millisecond }
	defer func() { TTSBackoff = func(d time.Duration) time.Duration { return d } }()
	c := &Client{Gemini: "k", GeminiBase: srv.URL, HTTP: srv.Client()}
	_, err := c.Speak(context.Background(), "Привет", "Iapetus", "")
	var qe *QuotaError
	if !errors.As(err, &qe) || !qe.Daily || hits.Load() != 1 {
		t.Fatalf("first: %v hits=%d", err, hits.Load())
	}
	for i := 0; i < 5; i++ {
		if _, err := c.Speak(context.Background(), "Ещё", "Iapetus", ""); !IsQuota(err) {
			t.Fatalf("closed: %v", err)
		}
	}
	if hits.Load() != 1 {
		t.Fatalf("burnt calls: %d", hits.Load())
	}
	if c.QuotaUntil("tts").IsZero() || !c.QuotaUntil("gemini").IsZero() {
		t.Fatal("only the speech service is closed")
	}
	// The next day it is open again.
	quotaNow = func() time.Time { return time.Now().Add(26 * time.Hour) }
	defer func() { quotaNow = time.Now }()
	if !c.QuotaUntil("tts").IsZero() {
		t.Fatal("quota did not come back")
	}
	_, _ = c.Speak(context.Background(), "Завтра", "Iapetus", "")
	if hits.Load() != 2 {
		t.Fatalf("next day: hits=%d", hits.Load())
	}
}

// Text: Gemini out of quota or down → Claude answers; a closed Gemini is not called.
func TestTextFallsBackToClaude(t *testing.T) {
	var gem, cl atomic.Int32
	status := 429
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.Contains(r.URL.Path, "/v1/messages"):
			cl.Add(1)
			_ = json.NewEncoder(w).Encode(map[string]any{"content": []any{map[string]any{"type": "text", "text": `{"ok":"claude"}`}}})
		default:
			gem.Add(1)
			w.WriteHeader(status)
			if status == 429 {
				_, _ = w.Write([]byte(dailyQuota))
			} else {
				_, _ = w.Write([]byte(`{"error":{"message":"overloaded"}}`))
			}
		}
	}))
	defer srv.Close()
	// AI_TEXT_ORDER=claude,gemini: Claude answers, Gemini is not touched.
	cc := &Client{Gemini: "k", GeminiModel: "gemini-x", GeminiBase: srv.URL, Anthropic: "a", AnthropicBase: srv.URL, ClaudeModel: "c", HTTP: srv.Client(), TextOrder: []string{"claude"}}
	if ans, err := cc.JSON(context.Background(), "s", "p"); err != nil || !strings.Contains(ans, "claude") || gem.Load() != 0 {
		t.Fatalf("claude first: %q %v gem=%d", ans, err, gem.Load())
	}
	// Gemini first (AI_TEXT_ORDER=gemini, R34a: never by default); its quota → Claude at once, no retries.
	c := &Client{Gemini: "k", GeminiModel: "gemini-x", GeminiBase: srv.URL, Anthropic: "a", AnthropicBase: srv.URL, ClaudeModel: "c", HTTP: srv.Client(), TextOrder: []string{"gemini"}}
	SearchBackoff = []time.Duration{time.Millisecond, time.Millisecond}
	ans, err := c.Search(context.Background(), "events")
	if err != nil || !strings.Contains(ans, "claude") || gem.Load() != 1 {
		t.Fatalf("search: %q %v gem=%d", ans, err, gem.Load())
	}
	// Gemini closed now: the next search does not call it at all.
	if _, err := c.Search(context.Background(), "events"); err != nil || gem.Load() != 1 {
		t.Fatalf("closed gemini called: %v gem=%d", err, gem.Load())
	}
	// Gemini alone with Claude second (no Anthropic first): a 5xx falls through to OpenAI.
	status = 503
	c2 := &Client{Gemini: "k", GeminiModel: "gemini-x", GeminiBase: srv.URL, OpenAI: "o", OpenAIBase: srv.URL, OpenAIModel: "m", HTTP: srv.Client()}
	srv2 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "chat/completions") {
			_ = json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"message": map[string]any{"content": "openai"}}}})
			return
		}
		w.WriteHeader(503)
	}))
	defer srv2.Close()
	c2.GeminiBase, c2.OpenAIBase, c2.HTTP = srv2.URL, srv2.URL, srv2.Client()
	if ans, err := c2.Text(context.Background(), "s", "p"); err != nil || ans != "openai" {
		t.Fatalf("5xx fallback: %q %v", ans, err)
	}
	if FriendlyError(&QuotaError{Service: "gemini", Until: time.Now().Add(time.Hour)}) == "" {
		t.Fatal("friendly")
	}
	_ = cl.Load()
}
