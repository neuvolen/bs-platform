package ai

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestParseEventsTakesUntidyAnswers(t *testing.T) {
	from := time.Date(2026, 10, 2, 0, 0, 0, 0, time.UTC)
	cases := []struct {
		name, ans string
		want      int
	}{
		{"fenced with text", "Вот что нашёл:\n```json\n{\"items\":[{\"title\":\"Форум\",\"date\":\"2026-10-10\",\"url\":\"https://a.kz\"}]}\n```\nИсточники: [1]", 1},
		{"trailing comma", `{"items":[{"title":"Форум","date":"2026-10-10","url":"https://a.kz"},]}`, 1},
		{"events key, dotted date, number price", `{"events":[{"title":"Завтрак","date":"05.10.2026","time":"9:00","url":"www.b.kz","price":5000,"tags":"нетворкинг, завтрак"}]}`, 1},
		{"bare array", `[{"title":"A","date":"2026-10-11T18:30:00","url":"https://c.kz"},{"title":"B","date":"2026-10-12","url":"https://d.kz"}]`, 2},
		{"cut off at the limit", `{"items":[{"title":"A","date":"2026-10-11","url":"https://c.kz"},{"title":"B","date":"2026-10-12","url":"https://d.kz"},{"title":"C","da`, 2},
		{"braces in text before", `Ищу {аккуратно}. {"items":[{"title":"A","date":"2026-10-11","url":"https://c.kz"}]}`, 1},
		{"past and doubles dropped", `{"items":[{"title":"A","date":"2026-10-11","url":"https://c.kz"},{"title":"a","date":"2026-10-11","url":"https://c.kz"},{"title":"Old","date":"2026-01-01","url":"https://c.kz"}]}`, 1},
	}
	for _, c := range cases {
		got, err := ParseEvents(c.ans, from)
		if err != nil || len(got) != c.want {
			t.Fatalf("%s: %v %+v", c.name, err, got)
		}
	}
	got, _ := ParseEvents(`{"events":[{"title":"Завтрак","date":"05.10.2026","time":"9:00","url":"www.b.kz","price":5000,"tags":"нетворкинг, завтрак"}]}`, from)
	if e := got[0]; e.Date != "2026-10-05" || e.Time != "09:00" || e.URL != "https://www.b.kz" || e.Price != "5000" || len(e.Tags) != 2 {
		t.Fatalf("normalized: %+v", e)
	}
	got, _ = ParseEvents(`[{"title":"A","date":"2026-10-11T18:30:00","url":"https://c.kz"}]`, from)
	if got[0].Time != "18:30" {
		t.Fatalf("time from date: %+v", got[0])
	}
	if _, err := ParseEvents("Мероприятий не нашёл.", from); err == nil || !strings.Contains(FriendlyError(err), "без списка") {
		t.Fatalf("no json: %v", err)
	}
	if got, err := ParseEvents(`{"items":[]}`, from); err != nil || len(got) != 0 {
		t.Fatalf("empty list: %v", err)
	}
}

func TestSearchRetriesOverloadAndExplainsErrors(t *testing.T) {
	SearchBackoff = []time.Duration{10 * time.Millisecond, 10 * time.Millisecond}
	defer func() { SearchBackoff = []time.Duration{4 * time.Second, 12 * time.Second} }()
	var n atomic.Int32
	mode := "flaky"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		k := n.Add(1)
		switch {
		case mode == "flaky" && k == 1:
			w.WriteHeader(503)
			_, _ = w.Write([]byte(`{"error":{"code":503,"message":"The model is overloaded. Please try again later.","status":"UNAVAILABLE"}}`))
		case mode == "flaky" && k == 2:
			// grounded answer without text (finish reason only)
			_, _ = w.Write([]byte(`{"candidates":[{"finishReason":"RECITATION","content":{"parts":[]}}]}`))
		case mode == "quota":
			w.WriteHeader(429)
			_, _ = w.Write([]byte(`{"error":{"code":429,"message":"You exceeded your current quota, please check your plan and billing details.","status":"RESOURCE_EXHAUSTED"}}`))
		case mode == "badkey":
			w.WriteHeader(403)
			_, _ = w.Write([]byte(`{"error":{"code":403,"message":"API key not valid"}}`))
		default:
			_ = json.NewEncoder(w).Encode(map[string]any{"candidates": []any{map[string]any{"content": map[string]any{"parts": []any{
				map[string]any{"text": "думаю…", "thought": true},
				map[string]any{"text": `{"items":[{"title":"Форум","date":"2099-01-10","url":"https://a.kz"}]}`}}}}}})
		}
	}))
	defer srv.Close()
	c := &Client{Gemini: "k", GeminiModel: "gemini-3.8-flash", GeminiBase: srv.URL, HTTP: srv.Client()}
	got, err := c.FindEvents(context.Background(), 21, time.Now())
	if err != nil || len(got) != 1 || n.Load() != 3 {
		t.Fatalf("retried: %v %+v calls=%d", err, got, n.Load())
	}
	mode = "quota"
	n.Store(0)
	_, err = c.FindEvents(context.Background(), 21, time.Now())
	if err == nil || FriendlyError(err) != GeminiNoFreeMessage || n.Load() != 1 {
		t.Fatalf("quota: %v / %s calls=%d", err, FriendlyError(err), n.Load())
	}
	// R32d: the quota is not asked again until it is back
	if _, err = c.FindEvents(context.Background(), 21, time.Now()); !IsQuota(err) || n.Load() != 1 {
		t.Fatalf("closed quota called: %v calls=%d", err, n.Load())
	}
	c.SetQuotaUntil("gemini", time.Time{})
	mode = "badkey"
	n.Store(0)
	_, err = c.FindEvents(context.Background(), 21, time.Now())
	if err == nil || n.Load() != 1 || !strings.Contains(FriendlyError(err), "GEMINI_API_KEY") {
		t.Fatalf("bad key is not retried: %v calls=%d", err, n.Load())
	}
	if s := FriendlyError(context.DeadlineExceeded); !strings.Contains(s, "слишком долго") {
		t.Fatal(s)
	}
}

func TestClaudeSearchGoesOnAfterPause(t *testing.T) {
	var n atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if n.Add(1) == 1 {
			_, _ = w.Write([]byte(`{"stop_reason":"pause_turn","content":[{"type":"server_tool_use","id":"x","name":"web_search","input":{"query":"q"}}]}`))
			return
		}
		_, _ = w.Write([]byte(`{"stop_reason":"end_turn","content":[{"type":"text","text":"{\"items\":[{\"title\":\"A\","},{"type":"text","text":"\"date\":\"2099-01-10\",\"url\":\"https://a.kz\"}]}","citations":[]}]}`))
	}))
	defer srv.Close()
	c := &Client{Anthropic: "k", ClaudeModel: "m", AnthropicBase: srv.URL, HTTP: srv.Client()}
	got, err := c.FindEvents(context.Background(), 21, time.Now())
	if err != nil || len(got) != 1 || n.Load() != 2 {
		t.Fatalf("claude: %v %+v %d", err, got, n.Load())
	}
}
