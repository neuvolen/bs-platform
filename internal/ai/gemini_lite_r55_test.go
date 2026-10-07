package ai

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// R55, prod 07.10.2026 00:36: the owner's new key was accepted, but the main
// model answered 429 «You exceeded your current quota» (its free tier gives
// that model «limit: 0»): all of Gemini rested 6 hours and every AI task
// stopped. Google counts quota per model: the light model now answers, and
// the log names the metric, the limit and the model.
func TestR55MainModelQuotaLiteAnswers(t *testing.T) {
	var mu sync.Mutex
	var paths []string
	const quota = `{"error":{"code":429,"message":"You exceeded your current quota, please check your plan and billing details. For more information on this error, head to: https://ai.google.dev/gemini-api/docs/rate-limits. To monitor your current usage, head to: https://ai.dev/rate-limit. \n* Quota exceeded for metric: generativelanguage.googleapis.com/generate_content_free_tier_requests, limit: 0, model: gemini-3.8-flash","status":"RESOURCE_EXHAUSTED"}}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		paths = append(paths, r.URL.Path)
		mu.Unlock()
		if strings.Contains(r.URL.Path, "gemini-3.8-flash:") {
			w.WriteHeader(429)
			_, _ = w.Write([]byte(quota))
			return
		}
		_, _ = w.Write([]byte(`{"candidates":[{"content":{"parts":[{"text":"ок"}]}}]}`))
	}))
	defer srv.Close()
	c := &Client{Gemini: "AQ.test", GeminiModel: "gemini-3.8-flash", GeminiLightModel: "gemini-3.5-flash-lite", GeminiBase: srv.URL, HTTP: srv.Client()}
	for i := 0; i < 2; i++ {
		ans, err := c.Text(context.Background(), "s", "p")
		if err != nil || ans != "ок" {
			t.Fatalf("call %d: %q %v", i, ans, err)
		}
	}
	mu.Lock()
	defer mu.Unlock()
	main := 0
	for _, p := range paths {
		if strings.Contains(p, "gemini-3.8-flash:") {
			main++
		}
	}
	if main != 1 {
		t.Fatalf("the closed main model was asked %d times: %v", main, paths)
	}
	if d := QuotaDetail(quota); d != "generate_content_free_tier_requests limit 0 model gemini-3.8-flash" {
		t.Fatalf("detail: %q", d)
	}
}

func TestR55QuotaDetailViolations(t *testing.T) {
	body := `{"error":{"code":429,"message":"x","details":[{"@type":"type.googleapis.com/google.rpc.QuotaFailure","violations":[{"quotaMetric":"m","quotaId":"GenerateRequestsPerDayPerProjectPerModel-FreeTier","quotaDimensions":{"model":"gemini-3.8-flash"},"quotaValue":"20"}]}]}}`
	if d := QuotaDetail(body); d != "GenerateRequestsPerDayPerProjectPerModel-FreeTier=20 (gemini-3.8-flash)" {
		t.Fatalf("detail: %q", d)
	}
	if QuotaDetail(`{"error":{"message":"nothing"}}`) != "" {
		t.Fatal("no detail expected")
	}
}

// Both models closed: the quota error comes back (the chain moves on).
func TestR55BothClosed(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(429)
		_, _ = w.Write([]byte(`{"error":{"code":429,"message":"You exceeded your current quota","status":"RESOURCE_EXHAUSTED"}}`))
	}))
	defer srv.Close()
	c := &Client{Gemini: "AQ.test", GeminiModel: "gemini-3.8-flash", GeminiLightModel: "gemini-3.5-flash-lite", GeminiBase: srv.URL, HTTP: srv.Client()}
	if _, err := c.geminiCall(context.Background(), "generateContent", map[string]any{}); !IsQuota(err) {
		t.Fatalf("want quota, got %v", err)
	}
}
