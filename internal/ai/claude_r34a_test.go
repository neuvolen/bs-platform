package ai

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// fakeClaude: a Messages API with web search, pause_turn, 529, a credit
// balance refusal and a models list.
type fakeClaude struct {
	mu      sync.Mutex
	bodies  []map[string]any
	keys    []string
	n       atomic.Int32
	mode    string // "", "overload1", "credit", "nomodel"
	missing map[string]bool
}

func (f *fakeClaude) server(t *testing.T) *httptest.Server {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/models" {
			_, _ = w.Write([]byte(`{"data":[{"id":"claude-sonnet-4-5","created_at":"2025-09-29T00:00:00Z"},{"id":"claude-sonnet-9","created_at":"2027-01-01T00:00:00Z"},{"id":"claude-opus-9","created_at":"2027-02-01T00:00:00Z"}]}`))
			return
		}
		if r.URL.Path != "/v1/messages" {
			w.WriteHeader(404)
			return
		}
		k := f.n.Add(1)
		raw, _ := io.ReadAll(r.Body)
		var body map[string]any
		_ = json.Unmarshal(raw, &body)
		f.mu.Lock()
		f.bodies = append(f.bodies, body)
		f.keys = append(f.keys, r.Header.Get("x-api-key"))
		f.mu.Unlock()
		if r.Header.Get("anthropic-version") != anthropicVersion || r.Header.Get("x-api-key") == "" {
			w.WriteHeader(401)
			_, _ = w.Write([]byte(`{"type":"error","error":{"type":"authentication_error","message":"invalid x-api-key"}}`))
			return
		}
		if r.Header.Get("x-api-key") == "bad-key-0000000000000000" {
			w.WriteHeader(401)
			_, _ = w.Write([]byte(`{"type":"error","error":{"type":"authentication_error","message":"invalid x-api-key"}}`))
			return
		}
		model, _ := body["model"].(string)
		if f.missing[model] {
			w.WriteHeader(404)
			_, _ = w.Write([]byte(`{"type":"error","error":{"type":"not_found_error","message":"model: ` + model + `"}}`))
			return
		}
		switch {
		case f.mode == "overload1" && k == 1:
			w.Header().Set("retry-after", "0")
			w.WriteHeader(529)
			_, _ = w.Write([]byte(`{"type":"error","error":{"type":"overloaded_error","message":"Overloaded"}}`))
			return
		case f.mode == "credit":
			w.WriteHeader(400)
			_, _ = w.Write([]byte(`{"type":"error","error":{"type":"invalid_request_error","message":"Your credit balance is too low to access the Anthropic API. Please go to Plans & Billing to upgrade or purchase credits."}}`))
			return
		}
		out := map[string]any{"stop_reason": "end_turn", "content": []any{map[string]any{"type": "text", "text": `{"ok":"` + model + `"}`}}}
		if tools, _ := body["tools"].([]any); len(tools) > 0 {
			msgs, _ := body["messages"].([]any)
			if len(msgs) == 1 { // first turn: a search, then a pause
				out = map[string]any{"stop_reason": "pause_turn", "content": []any{
					map[string]any{"type": "server_tool_use", "id": "srv_1", "name": "web_search", "input": map[string]any{"query": "бизнес Алматы"}},
					map[string]any{"type": "web_search_tool_result", "tool_use_id": "srv_1", "content": []any{map[string]any{"type": "web_search_result", "url": "https://a.kz", "title": "A"}}},
					map[string]any{"type": "text", "text": `{"items":[{"title":"Форум предпринимателей",`},
				}}
			} else {
				out = map[string]any{"stop_reason": "end_turn", "content": []any{map[string]any{"type": "text", "text": `"date":"2099-01-10","url":"https://a.kz"}]}`}}}
			}
		}
		_ = json.NewEncoder(w).Encode(out)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func (f *fakeClaude) last() map[string]any {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.bodies[len(f.bodies)-1]
}

func newClaude(srv *httptest.Server) *Client {
	return &Client{Anthropic: "claude-key", AnthropicBase: srv.URL, ClaudeModel: "claude-sonnet-5", HeavyModel: "claude-opus-5", HTTP: srv.Client()}
}

func TestR34aClaudeTextJSONAndCaching(t *testing.T) {
	f := &fakeClaude{}
	c := newClaude(f.server(t))
	ans, err := c.Text(context.Background(), "коротко", "привет")
	if err != nil || !strings.Contains(ans, "claude-sonnet-5") {
		t.Fatalf("text: %q %v", ans, err)
	}
	b := f.last()
	if b["system"] != "коротко" || b["max_tokens"].(float64) != 16000 {
		t.Fatalf("body: %+v", b)
	}
	// JSON: told to answer JSON only, a low temperature
	if _, err := c.JSON(context.Background(), "Редактор.", "p"); err != nil {
		t.Fatal(err)
	}
	if s, _ := f.last()["system"].(string); !strings.Contains(s, "только один JSON-объект") || f.last()["temperature"].(float64) != 0.2 {
		t.Fatalf("json body: %+v", f.last())
	}
	// a long system prompt is a cached block
	long := strings.Repeat("Правило библиотеки. ", 300)
	if _, err := c.JSON(context.Background(), long, "p"); err != nil {
		t.Fatal(err)
	}
	sys, _ := f.last()["system"].([]any)
	if len(sys) != 1 || sys[0].(map[string]any)["cache_control"].(map[string]any)["type"] != "ephemeral" {
		t.Fatalf("cache: %+v", f.last()["system"])
	}
	// heavy tasks: AI_MODEL_HEAVY with a bigger budget
	ans, err = c.Text(Heavy(context.Background()), "s", "p")
	if err != nil || !strings.Contains(ans, "claude-opus-5") || f.last()["max_tokens"].(float64) != 24000 {
		t.Fatalf("heavy: %q %v %+v", ans, err, f.last())
	}
	if ms := c.TextModels(); strings.Join(ms, ",") != "claude" {
		t.Fatal(ms)
	}
}

func TestR34aClaudeWebSearchWithPauseTurn(t *testing.T) {
	f := &fakeClaude{}
	c := newClaude(f.server(t))
	evs, err := c.FindEvents(context.Background(), 21, time.Now())
	if err != nil || len(evs) != 1 || evs[0].Title != "Форум предпринимателей" {
		t.Fatalf("events: %+v %v", evs, err)
	}
	if f.n.Load() != 2 {
		t.Fatalf("pause_turn not continued: %d calls", f.n.Load())
	}
	b := f.last()
	tool := b["tools"].([]any)[0].(map[string]any)
	if tool["type"] != DefaultWebSearchTool || tool["name"] != "web_search" {
		t.Fatalf("tool: %+v", tool)
	}
	msgs := b["messages"].([]any)
	if len(msgs) != 2 || msgs[1].(map[string]any)["role"] != "assistant" {
		t.Fatalf("continuation: %+v", msgs)
	}
	// the assistant turn goes back with the server tool blocks as they were
	if c0 := msgs[1].(map[string]any)["content"].([]any); len(c0) != 3 || c0[0].(map[string]any)["type"] != "server_tool_use" {
		t.Fatalf("content back: %+v", c0)
	}
	t.Setenv("AI_WEB_SEARCH_TOOL", "web_search_20990101")
	if _, err := c.Search(context.Background(), "x"); err != nil {
		t.Fatal(err)
	}
	f.mu.Lock()
	first := f.bodies[len(f.bodies)-2]
	f.mu.Unlock()
	if first["tools"].([]any)[0].(map[string]any)["type"] != "web_search_20990101" {
		t.Fatal("AI_WEB_SEARCH_TOOL ignored")
	}
}

func TestR34aOverloadRetriedAndBalanceClean(t *testing.T) {
	ClaudeBackoff = []time.Duration{time.Millisecond, time.Millisecond, time.Millisecond}
	f := &fakeClaude{mode: "overload1"}
	c := newClaude(f.server(t))
	if ans, err := c.Text(context.Background(), "s", "p"); err != nil || !strings.Contains(ans, "ok") || f.n.Load() != 2 {
		t.Fatalf("529 retried: %q %v calls=%d", ans, err, f.n.Load())
	}
	// the balance is out: one call, one sentence, a pause, the owner is told once
	f.mode = "credit"
	f.n.Store(0)
	told := make(chan *QuotaError, 4)
	c.OnQuota = func(q *QuotaError) { told <- q }
	_, err := c.JSON(context.Background(), "s", "p")
	if err == nil || UserMessage(err) != QuotaMessage || err.Error() != QuotaMessage || FriendlyError(err) != QuotaMessage || strings.Contains(err.Error(), "{") {
		t.Fatalf("credit: %v", err)
	}
	if _, err := c.Search(context.Background(), "x"); !IsQuota(err) || f.n.Load() != 1 {
		t.Fatalf("closed Claude called again: %v calls=%d", err, f.n.Load())
	}
	select {
	case q := <-told:
		if !q.Billing || q.Service != "claude" {
			t.Fatalf("%+v", q)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("owner not told")
	}
	if !c.Paused() || c.Status()["paused"] != true || c.Status()["message"] != QuotaMessage {
		t.Fatalf("paused: %+v", c.Status())
	}
	ps := c.Providers()
	if ps[0]["name"] != "claude" || ps[0]["state"] != "quota" {
		t.Fatalf("providers: %+v", ps)
	}
	// a new key opens it at once
	c.SetQuotaUntil("claude", time.Time{})
	f.mode = ""
	if _, err := c.Text(context.Background(), "s", "p"); err != nil {
		t.Fatal(err)
	}
	// a rate limit (429 rate_limit_error) is not a quota
	if IsQuota(&HTTPError{Status: 429, Body: `{"type":"error","error":{"type":"rate_limit_error","message":"Number of request tokens has exceeded your per-minute rate limit"}}`}) {
		t.Fatal("rate limit taken for the balance")
	}
	if m := UserMessage(&HTTPError{Status: 401, Body: `{"type":"error","error":{"type":"authentication_error","message":"invalid x-api-key"}}`}); m != KeyRejected {
		t.Fatal(m)
	}
}

func TestR34aModelFallbacks(t *testing.T) {
	f := &fakeClaude{missing: map[string]bool{"claude-opus-5": true}}
	c := newClaude(f.server(t))
	// the heavy model the key cannot use: AI_MODEL answers
	ans, err := c.Text(Heavy(context.Background()), "s", "p")
	if err != nil || !strings.Contains(ans, "claude-sonnet-5") {
		t.Fatalf("heavy fallback: %q %v", ans, err)
	}
	// AI_MODEL itself unknown: the newest model of its family the key lists
	f.missing["claude-sonnet-5"] = true
	ans, err = c.Text(context.Background(), "s", "p")
	if err != nil || !strings.Contains(ans, "claude-sonnet-9") {
		t.Fatalf("newest: %q %v", ans, err)
	}
	if m := c.models(context.Background())[0]; m != "claude-sonnet-9" {
		t.Fatalf("remembered: %s", m)
	}
}

func TestR34aSettingsKeyAndPing(t *testing.T) {
	f := &fakeClaude{}
	srv := f.server(t)
	box := &KeyBox{}
	c := &Client{AnthropicBase: srv.URL, ClaudeModel: "claude-sonnet-5", HTTP: srv.Client(), Keys: box}
	if c.HasText() || c.KeySource() != "" {
		t.Fatal("no key yet")
	}
	if _, err := c.Text(context.Background(), "s", "p"); !errors.Is(err, ErrNoKey) || !strings.Contains(UserMessage(err), "Настройках") {
		t.Fatalf("no key: %v", err)
	}
	box.Set("sk-ant-settings-key-1234")
	if !c.HasText() || c.KeySource() != "settings" {
		t.Fatal("settings key not used")
	}
	if _, err := c.Text(context.Background(), "s", "p"); err != nil {
		t.Fatal(err)
	}
	f.mu.Lock()
	k := f.keys[len(f.keys)-1]
	f.mu.Unlock()
	if k != "sk-ant-settings-key-1234" {
		t.Fatal("wrong key sent")
	}
	// the env key wins
	c.Anthropic = "claude-key"
	if c.KeySource() != "env" {
		t.Fatal("env")
	}
	if m, err := c.Ping(context.Background(), ""); err != nil || m != "claude-sonnet-5" {
		t.Fatalf("ping: %s %v", m, err)
	}
	if _, err := c.Ping(context.Background(), "bad-key-0000000000000000"); err == nil || UserMessage(err) != KeyRejected {
		t.Fatalf("bad key ping: %v", err)
	}
	if b := f.last(); b["max_tokens"].(float64) > 16 {
		t.Fatal("ping is not tiny")
	}
}

func TestR34aFromEnvGeminiOffByDefault(t *testing.T) {
	t.Setenv("GEMINI_API_KEY", "g")
	t.Setenv("GEMINI_ENABLED", "")
	t.Setenv("ANTHROPIC_API_KEY", "sk-ant-test-a-0123456789")
	t.Setenv("AI_MODEL", "")
	t.Setenv("AI_CLAUDE_MODEL", "")
	t.Setenv("AI_MODEL_HEAVY", "")
	t.Setenv("AI_TEXT_ORDER", "")
	t.Setenv("ASR_LOCAL", "0")
	c := FromEnv()
	if c.Gemini != "" || strings.Join(c.TextModels(), ",") != "claude" || c.ClaudeModel != DefaultModel || c.HeavyModel != DefaultHeavyModel || c.Keys != SharedKeys {
		t.Fatalf("default: %+v %v", c.TextModels(), c.ClaudeModel)
	}
	if c.ASR != nil || len(c.SpeechModels()) != 0 {
		t.Fatal("ASR_LOCAL=0")
	}
	if _, err := c.Transcribe(context.Background(), []byte("x"), "audio/webm"); !errors.Is(err, ErrNoSpeech) {
		t.Fatal(err)
	}
	for _, p := range c.Providers() {
		if p["name"] == "gemini" {
			t.Fatal("gemini in the providers line")
		}
	}
	t.Setenv("GEMINI_ENABLED", "1")
	t.Setenv("AI_MODEL", "claude-x")
	t.Setenv("AI_MODEL_HEAVY", "claude-y")
	t.Setenv("ASR_LOCAL", "")
	c = FromEnv()
	if c.Gemini != "g" || strings.Join(c.TextModels(), ",") != "claude,gemini" || c.ClaudeModel != "claude-x" || c.HeavyModel != "claude-y" {
		t.Fatalf("emergency: %v %s", c.TextModels(), c.ClaudeModel)
	}
	if strings.Join(c.SpeechModels(), ",") != "local,gemini" {
		t.Fatal(c.SpeechModels())
	}
}

func TestR34aKeySealing(t *testing.T) {
	secret := []byte("jwt-secret-0123456789")
	a, err := Seal(secret, "sk-ant-api03-secret-ABCD")
	if err != nil {
		t.Fatal(err)
	}
	b, _ := Seal(secret, "sk-ant-api03-secret-ABCD")
	if a == b || strings.Contains(a, "sk-ant") || !strings.HasPrefix(a, "v1:") {
		t.Fatalf("sealed: %s %s", a, b)
	}
	if p, err := Open(secret, a); err != nil || p != "sk-ant-api03-secret-ABCD" {
		t.Fatalf("open: %q %v", p, err)
	}
	if _, err := Open([]byte("other"), a); err == nil {
		t.Fatal("opened with another secret")
	}
	if _, err := Seal(nil, "x"); err == nil {
		t.Fatal("no secret")
	}
	if Last4("sk-ant-api03-secret-ABCD") != "…ABCD" || Last4("") != "" || Last4("short") != "…" {
		t.Fatal("last4")
	}
	if !ValidKeyShape("sk-ant-api03-abcdefghijklmnop") || ValidKeyShape("sk-ant api03 abcdefghijklmnop") || ValidKeyShape("short") || ValidKeyShape("sk-ant-ключ-abcdefghijklmnop") {
		t.Fatal("shape")
	}
}
