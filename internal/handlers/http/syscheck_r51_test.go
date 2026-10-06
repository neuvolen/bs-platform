package http

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/bnursik/business_surgery_backend/internal/ai"
	"github.com/bnursik/business_surgery_backend/internal/buildinfo"
	"github.com/bnursik/business_surgery_backend/internal/club"
)

// r51Fake: Anthropic without balance and Google that takes geminiKey in
// the x-goog-api-key header (never in the URL).
func r51Fake(t *testing.T, geminiKey string) *httptest.Server {
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		if strings.HasPrefix(r.URL.Path, "/v1/messages") {
			w.WriteHeader(400)
			_, _ = w.Write([]byte(`{"type":"error","error":{"type":"invalid_request_error","message":"Your credit balance is too low to access the Anthropic API. Please go to Plans & Billing to upgrade or purchase credits."}}`))
			return
		}
		if r.URL.Query().Get("key") != "" {
			t.Errorf("Gemini key in the URL")
		}
		if r.Header.Get("x-goog-api-key") != geminiKey {
			w.WriteHeader(400)
			_, _ = w.Write([]byte(`{"error":{"code":400,"message":"API key not valid. Please pass a valid API key.","status":"INVALID_ARGUMENT"}}`))
			return
		}
		text := "ок"
		if strings.Contains(string(b), "google_search") {
			text = "almaty.gov.kz"
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"candidates": []any{map[string]any{"content": map[string]any{"parts": []any{map[string]any{"text": text}}}}}})
	}))
	t.Cleanup(s.Close)
	return s
}

func r51Check(t *testing.T, srv *httptest.Server, key string) *SysCheck {
	c := &ai.Client{Anthropic: "sk-ant-api03-x", AnthropicBase: srv.URL, ClaudeModel: "claude-sonnet-5",
		Gemini: key, GeminiModel: "gemini-3.8-flash", GeminiLightModel: "gemini-3.5-flash-lite", GeminiBase: srv.URL, HTTP: srv.Client()}
	return &SysCheck{AI: c, Now: func() time.Time { return time.Date(2026, 10, 6, 14, 45, 0, 0, club.Almaty) },
		DB:      func(context.Context) error { return nil },
		Webhook: func(context.Context) (bool, string) { return true, "" },
		Export:  func(context.Context) bool { return false },
		Builtin: func() (int, int) { return 33, 33 },
		Build:   func() buildinfo.Info { return buildinfo.Info{Commit: "51515151aaaa"} }}
}

// R51, prod 06.10.2026: Claude without balance, Gemini answers. Before:
// «⚠️ ИИ Claude: ошибка … 400 (billing)», «❌ Поиск…», «✅ ИИ: кто отвечает»
// with raw API text. Now one calm line, nothing to send after a deploy.
func TestR51StatusClaudeBillingGeminiAnswers(t *testing.T) {
	const key = "AQ.Ab8RN6L-free_key.0123456789abcdefghijklmnop"
	s := r51Check(t, r51Fake(t, key), key)
	r := s.Run(context.Background())
	it := r.Items[0]
	want := "отвечает Gemini (бесплатно) · Claude без баланса: не используется · поиск в интернете работает"
	if it.Key != "ai" || it.State != "ok" || it.Text != want || it.Action != "" {
		t.Fatalf("ai: %+v", it)
	}
	txt := r.Text()
	for _, no := range []string{"⚠️", "❌", "400", "billing", "Anthropic", "API key", "ИИ Claude", "Поиск в интернете:", "кто отвечает", "Экспорт"} {
		if strings.Contains(txt, no) {
			t.Errorf("%q in the short report:\n%s", no, txt)
		}
	}
	if !strings.Contains(txt, "✅ ИИ: "+want) {
		t.Fatalf("report:\n%s", txt)
	}
	full := r.TextFull()
	for _, w := range []string{"Claude: Закончился баланс Claude", "ответ Anthropic 400", "Gemini: работает", "Поиск: gemini-3.8-flash, google_search"} {
		if !strings.Contains(full, w) {
			t.Errorf("no %q in the full report:\n%s", w, full)
		}
	}
	if strings.Contains(full, key) || strings.Contains(full, "sk-ant-api03-x") {
		t.Fatal("a key in the report")
	}
	if n := DeployNote(r, nil); n != "" {
		t.Fatalf("deploy message with nothing to do: %q", n)
	}
}

// The new key is refused: one problem, one action, no Google text; the
// deploy message has it once.
func TestR51StatusRefusedGeminiKey(t *testing.T) {
	s := r51Check(t, r51Fake(t, "AQ.the-right-one-000000000000000000000000000"), "AQ.Ab8RN6L-wrong-0000000000000000000000000")
	r := s.Run(context.Background())
	it := r.Items[0]
	if it.State != "fail" || !strings.HasPrefix(it.Text, "никто не отвечает") || !strings.Contains(it.Problem, "ключ Gemini не принят") ||
		!strings.Contains(it.Action, "aistudio.google.com/api-keys") || !strings.Contains(it.Action, "GEMINI_API_KEY") {
		t.Fatalf("ai: %+v", it)
	}
	txt := r.Text()
	if strings.Contains(txt, "API key not valid") || strings.Count(txt, "GEMINI_API_KEY") != 1 {
		t.Fatalf("report:\n%s", txt)
	}
	n := DeployNote(r, nil)
	if strings.Count(n, "→") != 1 || !strings.Contains(n, "❌ ИИ: не отвечает: ключ Gemini не принят") || strings.Contains(n, "Рекомендации") {
		t.Fatalf("deploy: %q", n)
	}
	// the same problem at the next deploy: not repeated
	if n2 := DeployNote(r, sigs(r)); n2 != "" {
		t.Fatalf("repeated: %q", n2)
	}
}
