package ai

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// The answer prod got when the key's balance ran out: no per-day or
// per-minute limit named.
const billingQuota = `{"error":{"code":429,"message":"You exceeded your current quota, please check your plan and billing details. For more information on this error, head to: https://ai.google.dev/gemini-api/docs/rate-limits.","status":"RESOURCE_EXHAUSTED"}}`

type fakeAI struct {
	gem, cl, clSearch atomic.Int32
	gemBody           string
	gemStatus         int
	mu                sync.Mutex
	clBodies          []string
}

func (f *fakeAI) server(t *testing.T) *httptest.Server {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		if strings.Contains(r.URL.Path, "/v1/messages") {
			f.cl.Add(1)
			f.mu.Lock()
			f.clBodies = append(f.clBodies, string(b))
			f.mu.Unlock()
			if r.Header.Get("x-api-key") != "claude-key" {
				w.WriteHeader(401)
				return
			}
			txt := `{"ok":"claude"}`
			if strings.Contains(string(b), "web_search") {
				f.clSearch.Add(1)
				txt = `{"items":[{"title":"Форум предпринимателей","date":"2099-01-10","url":"https://a.kz"}]}`
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"stop_reason": "end_turn", "content": []any{map[string]any{"type": "text", "text": txt}}})
			return
		}
		f.gem.Add(1)
		w.WriteHeader(f.gemStatus)
		_, _ = w.Write([]byte(f.gemBody))
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestR32cBillingQuotaIsRecognised(t *testing.T) {
	q, ok := ParseQuota(&HTTPError{Status: 429, Body: billingQuota})
	if !ok || !q.Billing || q.Daily {
		t.Fatalf("billing: %+v %v", q, ok)
	}
	if q, _ := ParseQuota(&HTTPError{Status: 429, Body: minuteQuota}); q.Billing {
		t.Fatal("a per-minute limit is not billing")
	}
	if q, _ := ParseQuota(&HTTPError{Status: 429, Body: dailyQuota}); q.Billing || !q.Daily {
		t.Fatal("a per-day limit is daily, not billing")
	}
	// a generic «Resource has been exhausted»: a short rate limit
	if q, ok := ParseQuota(&HTTPError{Status: 429, Body: `{"error":{"code":429,"message":"Resource has been exhausted (e.g. check quota).","status":"RESOURCE_EXHAUSTED"}}`}); !ok || q.Billing || q.Daily {
		t.Fatalf("generic: %+v", q)
	}
	t.Setenv("AI_QUOTA_HOLD_HOURS", "3")
	if QuotaHold() != 3*time.Hour {
		t.Fatal(QuotaHold())
	}
	t.Setenv("AI_QUOTA_HOLD_HOURS", "")
	if QuotaHold() != 6*time.Hour {
		t.Fatal(QuotaHold())
	}
}

// Gemini's balance is out: one call, Gemini rests for hours, every text,
// JSON and search task goes to Claude at once, the owner is told once.
// R34a: Gemini is first only when the owner says so (AI_TEXT_ORDER).
func TestR32cGeminiBillingFallsBackToClaude(t *testing.T) {
	f := &fakeAI{gemStatus: 429, gemBody: billingQuota}
	srv := f.server(t)
	var told atomic.Int32
	var toldQ *QuotaError
	done := make(chan struct{}, 4)
	c := &Client{Gemini: "g", GeminiModel: "gemini-x", GeminiBase: srv.URL, Anthropic: "claude-key", AnthropicBase: srv.URL, ClaudeModel: "c", HTTP: srv.Client(),
		TextOrder: []string{"gemini"}, OnQuota: func(q *QuotaError) { told.Add(1); toldQ = q; done <- struct{}{} }}
	if got := c.TextModels(); strings.Join(got, ",") != "gemini,claude" {
		t.Fatalf("order: %v", got)
	}
	ans, err := c.JSON(context.Background(), "s", "p")
	if err != nil || !strings.Contains(ans, "claude") || f.gem.Load() != 1 || f.cl.Load() != 1 {
		t.Fatalf("first: %q %v gem=%d cl=%d", ans, err, f.gem.Load(), f.cl.Load())
	}
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("owner not told")
	}
	if !toldQ.Billing || toldQ.Until.Sub(time.Now()) < GeminiBareHold()-time.Minute { // R66: a bare refusal names no limit: 30 minutes
		t.Fatalf("hold: %+v", toldQ)
	}
	SearchBackoff = []time.Duration{time.Millisecond, time.Millisecond}
	for i := 0; i < 5; i++ {
		if _, err := c.Text(context.Background(), "s", "p"); err != nil {
			t.Fatal(err)
		}
	}
	evs, err := c.FindEvents(context.Background(), 21, time.Now())
	if err != nil || len(evs) != 1 || f.clSearch.Load() != 1 {
		t.Fatalf("events via Claude web search: %v %v search=%d", evs, err, f.clSearch.Load())
	}
	if f.gem.Load() != 1 {
		t.Fatalf("Gemini hammered: %d calls", f.gem.Load())
	}
	if told.Load() != 1 {
		t.Fatalf("owner told %d times", told.Load())
	}
	// Claude's search request carries the web search tool
	f.mu.Lock()
	last := f.clBodies[len(f.clBodies)-1]
	f.mu.Unlock()
	if !strings.Contains(last, `"`+DefaultWebSearchTool+`"`) {
		t.Fatalf("search body: %s", last)
	}
	st := c.Status()
	ps := st["providers"].([]map[string]any)
	if ps[1]["name"] != "gemini" || ps[1]["state"] != "quota" || ps[1]["billing"] != true || ps[0]["name"] != "claude" || ps[0]["state"] != "ok" || c.Paused() {
		t.Fatalf("status: %+v paused=%v", ps, c.Paused())
	}
	// hours later Gemini is asked again
	quotaNow = func() time.Time { return time.Now().Add(7 * time.Hour) }
	defer func() { quotaNow = time.Now }()
	_, _ = c.Text(context.Background(), "s", "p")
	if f.gem.Load() != 2 {
		t.Fatalf("after the hold: gem=%d", f.gem.Load())
	}
}

// No Claude key: one clean sentence, never Google's JSON, Gemini not hammered.
func TestR32cNoFallbackCleanMessage(t *testing.T) {
	f := &fakeAI{gemStatus: 429, gemBody: billingQuota}
	srv := f.server(t)
	c := &Client{Gemini: "g", GeminiModel: "gemini-x", GeminiBase: srv.URL, HTTP: srv.Client()}
	SearchBackoff = []time.Duration{time.Millisecond, time.Millisecond}
	for i := 0; i < 4; i++ {
		_, err := c.Search(context.Background(), "recs")
		// R56: a refused search closes only the search (its own quota at Google)
		if err == nil || err.Error() != GeminiSearchQuotaMessage || UserMessage(err) != GeminiSearchQuotaMessage || FriendlyError(err) != GeminiSearchQuotaMessage {
			t.Fatalf("message: %v", err)
		}
		_, err = c.JSON(context.Background(), "s", "p")
		if err == nil || strings.Contains(err.Error(), "{") || UserMessage(err) != GeminiNoFreeMessage {
			t.Fatalf("json: %v", err)
		}
	}
	if f.gem.Load() != 2 { // one search, one text
		t.Fatalf("Gemini hammered: %d", f.gem.Load())
	}
	if !c.Paused() {
		t.Fatal("paused")
	}
	// any API error: no raw JSON either
	he := &HTTPError{Status: 500, Body: `{"error":{"code":500,"message":"Internal error encountered.","status":"INTERNAL"}}`}
	if he.Error() != "ИИ ответил ошибкой 500: Internal error encountered." {
		t.Fatal(he.Error())
	}
	if (&HTTPError{Status: 502, Body: "<html>bad gateway</html>"}).Error() != "ИИ ответил ошибкой 502" {
		t.Fatal("html body leaked")
	}
	if m := UserMessage(errFrom(`ИИ ответил 429: {"error":{"code":429}}`)); strings.Contains(m, "{") {
		t.Fatal(m)
	}
}

// A per-minute limit is short: no hold, no message to the owner.
func TestR32cMinuteLimitIsShort(t *testing.T) {
	f := &fakeAI{gemStatus: 429, gemBody: minuteQuota}
	srv := f.server(t)
	var told atomic.Int32
	c := &Client{Gemini: "g", GeminiModel: "gemini-x", GeminiBase: srv.URL, Anthropic: "claude-key", AnthropicBase: srv.URL, ClaudeModel: "c", HTTP: srv.Client(),
		TextOrder: []string{"gemini"}, OnQuota: func(*QuotaError) { told.Add(1) }}
	if _, err := c.Text(context.Background(), "s", "p"); err != nil {
		t.Fatal(err)
	}
	if u := c.QuotaUntil("gemini"); u.IsZero() || time.Until(u) > 6*time.Minute {
		t.Fatalf("until %v", u)
	}
	time.Sleep(50 * time.Millisecond)
	if told.Load() != 0 {
		t.Fatal("owner told about a minute limit")
	}
}

func TestR32cClaudeKeyAlias(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "")
	t.Setenv("CLAUDE_API_KEY", "sk-ant-test-ck-0123456789")
	t.Setenv("AI_TEXT_ORDER", "")
	c := FromEnv()
	if c.Anthropic != "sk-ant-test-ck-0123456789" {
		t.Fatal("CLAUDE_API_KEY")
	}
	t.Setenv("AI_TEXT_ORDER", "claude, gemini")
	c = FromEnv()
	c.Gemini = "g"
	if strings.Join(c.TextModels(), ",") != "claude,gemini" {
		t.Fatal(c.TextModels())
	}
}

type strErr string

func (e strErr) Error() string { return string(e) }
func errFrom(s string) error   { return strErr(s) }
