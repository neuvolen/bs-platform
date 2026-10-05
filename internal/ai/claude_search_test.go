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
	"testing"
	"time"
)

// fakeSearch: a Messages API that answers with the documented web search
// shapes and refuses what the rules say (prod 05.10.2026 included).
type fakeSearch struct {
	mu    sync.Mutex
	tools []map[string]any
	model []string
	// rules
	noLocation  bool            // any user_location → 400 like prod
	badVersions map[string]bool // these tool types → 400 unknown tag
	noSearch    map[string]bool // these models → 400 tool not supported
	disabled    bool
	resultErr   string // web_search_tool_result_error code, no text
}

func (f *fakeSearch) server(t *testing.T) *httptest.Server {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		var body map[string]any
		_ = json.Unmarshal(raw, &body)
		model, _ := body["model"].(string)
		tools, _ := body["tools"].([]any)
		var tool map[string]any
		if len(tools) > 0 {
			tool, _ = tools[0].(map[string]any)
		}
		f.mu.Lock()
		f.tools = append(f.tools, tool)
		f.model = append(f.model, model)
		f.mu.Unlock()
		bad := func(msg string) {
			w.WriteHeader(400)
			b, _ := json.Marshal(map[string]any{"type": "error", "error": map[string]any{"type": "invalid_request_error", "message": msg}})
			_, _ = w.Write(b)
		}
		if tool != nil {
			typ, _ := tool["type"].(string)
			switch {
			case f.disabled:
				bad("Web search is not enabled for this organization.")
				return
			case f.badVersions[typ]:
				bad("tools.0: Input tag '" + typ + "' found using 'type' does not match any of the expected tags")
				return
			case f.noSearch[model]:
				bad("tools.0." + typ + ": this tool is not supported for model " + model)
				return
			case f.noLocation && tool["user_location"] != nil:
				bad("tools.0." + typ + ": Country code KZ is not supported.")
				return
			}
			if _, ok := tool["allowed_callers"]; ok && typ == "web_search_20250305" {
				bad("tools.0.web_search_20250305.allowed_callers: Extra inputs are not permitted")
				return
			}
		}
		var content []any
		if f.resultErr != "" {
			content = []any{
				map[string]any{"type": "server_tool_use", "id": "srvtoolu_1", "name": "web_search", "input": map[string]any{"query": "q"}},
				map[string]any{"type": "web_search_tool_result", "tool_use_id": "srvtoolu_1", "content": map[string]any{"type": "web_search_tool_result_error", "error_code": f.resultErr}},
			}
		} else {
			content = []any{
				map[string]any{"type": "text", "text": "Ищу. "},
				map[string]any{"type": "server_tool_use", "id": "srvtoolu_1", "name": "web_search", "input": map[string]any{"query": "акимат Алматы"}},
				map[string]any{"type": "web_search_tool_result", "tool_use_id": "srvtoolu_1", "content": []any{
					map[string]any{"type": "web_search_result", "url": "https://almaty.gov.kz", "title": "Акимат", "encrypted_content": "Eqg", "page_age": "May 1, 2026"},
					map[string]any{"type": "web_search_result", "url": "https://gov.kz", "title": "Gov", "encrypted_content": "Eqh"},
				}},
				map[string]any{"type": "text", "text": "almaty.gov.kz", "citations": []any{
					map[string]any{"type": "web_search_result_location", "url": "https://almaty.gov.kz", "title": "Акимат", "encrypted_index": "Eo8", "cited_text": "Акимат города Алматы"},
				}},
			}
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"stop_reason": "end_turn", "content": content,
			"usage": map[string]any{"server_tool_use": map[string]any{"web_search_requests": 1}}})
	}))
	t.Cleanup(srv.Close)
	return srv
}

func (f *fakeSearch) calls() int { f.mu.Lock(); defer f.mu.Unlock(); return len(f.tools) }
func (f *fakeSearch) lastTool() map[string]any {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.tools[len(f.tools)-1]
}

func searchClient(srv *httptest.Server) *Client {
	return &Client{Anthropic: "claude-key", AnthropicBase: srv.URL, ClaudeModel: "claude-sonnet-5", HTTP: srv.Client()}
}

// Prod 05.10.2026: "Country code KZ is not supported." The search goes on
// without the location, and the next search goes without it at once.
func TestR39SearchLocationRefused(t *testing.T) {
	f := &fakeSearch{noLocation: true}
	c := searchClient(f.server(t))
	ans, err := c.Search(context.Background(), "x")
	if err != nil || ans != "Ищу. almaty.gov.kz" {
		t.Fatalf("answer %q %v", ans, err)
	}
	if f.calls() != 2 {
		t.Fatalf("calls %d", f.calls())
	}
	tl := f.lastTool()
	if tl["type"] != "web_search_20260318" || tl["user_location"] != nil {
		t.Fatalf("tool %+v", tl)
	}
	if cs, _ := tl["allowed_callers"].([]any); len(cs) != 1 || cs[0] != "direct" {
		t.Fatalf("allowed_callers %+v", tl["allowed_callers"])
	}
	if _, err := c.Search(context.Background(), "y"); err != nil || f.calls() != 3 {
		t.Fatalf("remembered: calls %d %v", f.calls(), err)
	}
	si := c.LastSearch()
	if !si.OK || si.Tool != "web_search_20260318" || si.Searches != 1 || si.Sources != 2 || si.Model != "claude-sonnet-5" {
		t.Fatalf("info %+v", si)
	}
	if st := c.Status(); st["search"] == nil {
		t.Fatal("Status has no search")
	}
}

// The default location never carries a country.
func TestR39SearchToolShape(t *testing.T) {
	tl := searchCfg{version: "web_search_20250305"}.tool(3)
	loc := tl["user_location"].(map[string]any)
	if _, ok := loc["country"]; ok || loc["city"] != "Almaty" || tl["max_uses"] != 3 || tl["name"] != "web_search" {
		t.Fatalf("%+v", tl)
	}
	if _, ok := tl["allowed_callers"]; ok {
		t.Fatal("basic version has no allowed_callers")
	}
}

// Newer versions refused: down to web_search_20250305 (without allowed_callers).
func TestR39SearchVersionFallback(t *testing.T) {
	f := &fakeSearch{badVersions: map[string]bool{"web_search_20260318": true, "web_search_20260209": true}}
	c := searchClient(f.server(t))
	if _, err := c.Search(context.Background(), "x"); err != nil {
		t.Fatal(err)
	}
	if tl := f.lastTool(); tl["type"] != "web_search_20250305" || tl["allowed_callers"] != nil {
		t.Fatalf("tool %+v", tl)
	}
	if f.calls() != 3 {
		t.Fatalf("calls %d", f.calls())
	}
}

// The model cannot search with any version: AI_SEARCH_MODEL answers.
func TestR39SearchModelFallback(t *testing.T) {
	t.Setenv("AI_SEARCH_MODEL", "")
	f := &fakeSearch{noSearch: map[string]bool{"claude-sonnet-5": true}}
	c := searchClient(f.server(t))
	if _, err := c.Search(context.Background(), "x"); err != nil {
		t.Fatal(err)
	}
	f.mu.Lock()
	last := f.model[len(f.model)-1]
	f.mu.Unlock()
	if last != DefaultSearchModel || c.LastSearch().Model != DefaultSearchModel {
		t.Fatalf("model %s %+v", last, c.LastSearch())
	}
	// both refuse: one sentence with Anthropic's message, never the old «не умеет»
	f2 := &fakeSearch{noSearch: map[string]bool{"claude-sonnet-5": true, DefaultSearchModel: true}}
	c2 := searchClient(f2.server(t))
	_, err := c2.Search(context.Background(), "x")
	var se *SearchError
	if !errors.As(err, &se) || se.Kind != "unsupported" {
		t.Fatalf("err %v", err)
	}
	msg := FriendlyError(err)
	if !strings.Contains(msg, "AI_SEARCH_MODEL") || !strings.Contains(msg, "not supported for model") || strings.Contains(msg, "{") {
		t.Fatalf("message %q", msg)
	}
	if f2.calls() != 6 {
		t.Fatalf("calls %d", f2.calls())
	}
}

func TestR39SearchDisabledAndResultError(t *testing.T) {
	f := &fakeSearch{disabled: true}
	c := searchClient(f.server(t))
	_, err := c.Search(context.Background(), "x")
	if msg := FriendlyError(err); !strings.Contains(msg, "Capabilities") || f.calls() != 1 {
		t.Fatalf("%q calls %d", msg, f.calls())
	}
	if UserMessage(err) != FriendlyError(err) || !strings.Contains(c.LastSearch().Error, "Console") {
		t.Fatalf("user message %q", UserMessage(err))
	}
	SearchBackoff = []time.Duration{time.Millisecond, time.Millisecond}
	f2 := &fakeSearch{resultErr: "unavailable"}
	c2 := searchClient(f2.server(t))
	_, err = c2.Search(context.Background(), "x")
	if msg := FriendlyError(err); !strings.Contains(msg, "unavailable") {
		t.Fatalf("%q", msg)
	}
	if f2.calls() != 3 { // the search itself was busy: asked again (SearchBackoff)
		t.Fatalf("calls %d", f2.calls())
	}
}

func TestR39SearchPingCached(t *testing.T) {
	f := &fakeSearch{}
	c := searchClient(f.server(t))
	si, err := c.SearchPing(context.Background())
	if err != nil || !si.OK || si.Searches != 1 || si.Sources != 2 {
		t.Fatalf("%+v %v", si, err)
	}
	if tl := f.lastTool(); tl["max_uses"] != float64(1) {
		t.Fatalf("ping tool %+v", tl)
	}
	if _, err := c.SearchPing(context.Background()); err != nil || f.calls() != 1 {
		t.Fatalf("not cached: %d", f.calls())
	}
}
