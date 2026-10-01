package ai

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestRetiredGeminiModelIsReplaced(t *testing.T) {
	var paths []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.URL.Path)
		switch {
		case strings.Contains(r.URL.Path, "gemini-2.5-flash"):
			w.WriteHeader(404)
			_, _ = w.Write([]byte(`{"error":{"code":404,"message":"This model models/gemini-2.5-flash is no longer available to new users. Please update your code to use models/gemini-3.8-flash for the latest features and improvements. We recommend you to use the Interactions API (https://ai.google.dev/gemini-api/docs/interactions)"}}`))
		case strings.Contains(r.URL.Path, "gemini-3.8-flash"):
			_ = json.NewEncoder(w).Encode(map[string]any{"candidates": []any{map[string]any{"content": map[string]any{"parts": []any{map[string]any{"text": "ok"}}}}}})
		case strings.HasSuffix(r.URL.Path, "/v1beta/models"):
			_, _ = w.Write([]byte(`{"models":[{"name":"models/gemini-3.9-flash-lite","supportedGenerationMethods":["generateContent"]},{"name":"models/gemini-4.0-flash","supportedGenerationMethods":["generateContent"]},{"name":"models/gemini-3.8-flash","supportedGenerationMethods":["generateContent"]}]}`))
		default:
			w.WriteHeader(404)
			_, _ = w.Write([]byte(`{"error":{"message":"not found"}}`))
		}
	}))
	defer srv.Close()
	c := &Client{Gemini: "k", GeminiModel: "gemini-2.5-flash", GeminiBase: srv.URL, HTTP: srv.Client()}
	out, err := c.Text(context.Background(), "sys", "hi")
	if err != nil || out != "ok" || c.geminiModel() != "gemini-3.8-flash" {
		t.Fatalf("suggested model: %q %v %s %v", out, err, c.geminiModel(), paths)
	}
	// No suggestion in the error: the newest plain flash model is picked.
	c2 := &Client{Gemini: "k", GeminiModel: "gemini-1.0-flash", GeminiBase: srv.URL, HTTP: srv.Client()}
	if got := c2.newestFlash(context.Background(), "gemini-1.0-flash"); got != "gemini-4.0-flash" {
		t.Fatalf("newest: %s", got)
	}
	if ans, err := c.Search(context.Background(), "x"); err != nil || ans != "ok" {
		t.Fatalf("search: %q %v", ans, err)
	}
}
