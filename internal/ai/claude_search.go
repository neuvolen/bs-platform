package ai

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"
)

// R39: Claude web search that survives API changes.
//
// The web search server tool (docs: platform.claude.com, «Web search tool»)
// has three versions: web_search_20260318 (response inclusion control),
// web_search_20260209 (dynamic filtering) and web_search_20250305 (basic).
// The newer two run the search from code execution unless
// "allowed_callers": ["direct"] is set; the server asks for direct calls.
//
// A request goes with the newest version first. A 400 about the tool walks
// down to the older versions; a 400 about user_location (prod 05.10.2026:
// "tools.0.web_search_20250305: Country code KZ is not supported.") sends
// the same version again without the location; then the model known to
// search (AI_SEARCH_MODEL, default claude-sonnet-4-6) is tried. Only then
// the error is reported, as one Russian sentence with Anthropic's own
// message. The working combination is remembered for the next requests.
//
// Answers: server_tool_use, web_search_tool_result (a list of
// web_search_result, or a web_search_tool_result_error with error_code) and
// text blocks with web_search_result_location citations.

var webSearchVersions = []string{"web_search_20260318", "web_search_20260209", "web_search_20250305"}

// DefaultSearchModel: the fallback model for web search (AI_SEARCH_MODEL).
const DefaultSearchModel = "claude-sonnet-4-6"

type modelKey struct{}

// withModel: claudeMessages asks exactly this model.
func withModel(ctx context.Context, m string) context.Context {
	return context.WithValue(ctx, modelKey{}, m)
}

// SearchError: a web search failure in words for the page and /status.
type SearchError struct {
	Msg  string
	Kind string // disabled | unsupported | rejected | results
	Err  error
}

func (e *SearchError) Error() string { return e.Msg }
func (e *SearchError) Unwrap() error { return e.Err }

// SearchInfo: the last web search, for «Состояние ИИ» and /status.
type SearchInfo struct {
	At       time.Time `json:"at"`
	OK       bool      `json:"ok"`
	Model    string    `json:"model,omitempty"`
	Tool     string    `json:"tool,omitempty"`
	Searches int       `json:"searches"`
	Sources  int       `json:"sources"`
	Error    string    `json:"error,omitempty"`
}

type searchCfg struct {
	version   string
	model     string // "" = the task's model (AI_MODEL)
	noLoc     bool
	noCallers bool
}

type searchMemo struct {
	mu   sync.Mutex
	cfg  *searchCfg
	head string // searchVersions()[0] when cfg was found (AI_WEB_SEARCH_TOOL changed: search again)
	last SearchInfo
	ping SearchInfo
}

var searchMemos sync.Map // *Client → *searchMemo

func (c *Client) smemo() *searchMemo {
	v, _ := searchMemos.LoadOrStore(c, &searchMemo{})
	return v.(*searchMemo)
}

// LastSearch: the last web search of this client (zero At: none yet).
func (c *Client) LastSearch() SearchInfo {
	m := c.smemo()
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.last
}

func (c *Client) noteSearch(si SearchInfo) {
	m := c.smemo()
	m.mu.Lock()
	m.last = si
	m.mu.Unlock()
}

func searchVersions() []string {
	out := []string{}
	if t := strings.TrimSpace(os.Getenv("AI_WEB_SEARCH_TOOL")); t != "" {
		out = append(out, t)
	}
	for _, v := range webSearchVersions {
		if len(out) == 0 || out[0] != v {
			out = append(out, v)
		}
	}
	return out
}

func searchModel() string {
	if m := strings.TrimSpace(os.Getenv("AI_SEARCH_MODEL")); m != "" {
		return m
	}
	return DefaultSearchModel
}

func searchUses(def int) int {
	if v, err := strconv.Atoi(os.Getenv("AI_WEB_SEARCH_MAX_USES")); err == nil && v > 0 {
		return v
	}
	return def
}

// tool: the web search tool block. No country in user_location: the API
// refuses "KZ"; the city and the time zone still point the search at Almaty.
func (s searchCfg) tool(uses int) map[string]any {
	t := map[string]any{"type": s.version, "name": "web_search", "max_uses": uses}
	if !s.noLoc {
		t["user_location"] = map[string]any{"type": "approximate", "city": "Almaty", "timezone": "Asia/Almaty"}
	}
	if s.version != "web_search_20250305" && !s.noCallers {
		t["allowed_callers"] = []string{"direct"}
	}
	return t
}

// searchErrKind: what a refused search request means.
//
//	disabled  web search is off for the organisation (Console → Capabilities)
//	location  user_location is refused (a country, a city)
//	callers   allowed_callers is not known
//	tool      the tool version is not known or not supported by the model
//	""        anything else (the key, the balance, an overload)
func searchErrKind(err error) string {
	var he *HTTPError
	if !errors.As(err, &he) || he.Status != 400 {
		return ""
	}
	low := strings.ToLower(he.Body)
	if _, ok := ParseQuota(he); ok {
		return ""
	}
	switch {
	case strings.Contains(low, "credit balance") || strings.Contains(low, "billing"):
		return ""
	case strings.Contains(low, "not enabled") && strings.Contains(low, "search"),
		strings.Contains(low, "web search is disabled"), strings.Contains(low, "web_search is disabled"):
		return "disabled"
	case strings.Contains(low, "user_location") || strings.Contains(low, "country") || strings.Contains(low, "city") ||
		strings.Contains(low, "timezone") || strings.Contains(low, "region"):
		return "location"
	case strings.Contains(low, "allowed_callers"):
		return "callers"
	case strings.Contains(low, "tools.") || strings.Contains(low, "web_search") ||
		strings.Contains(low, "tool") && (strings.Contains(low, "not supported") || strings.Contains(low, "does not support") ||
			strings.Contains(low, "unknown") || strings.Contains(low, "invalid") || strings.Contains(low, "not available") ||
			strings.Contains(low, "input tag") || strings.Contains(low, "unexpected")):
		return "tool"
	}
	return ""
}

func anthropicSays(err error) string {
	var he *HTTPError
	if errors.As(err, &he) {
		if m := apiMessage(he.Body); m != "" {
			return m
		}
		return fmt.Sprintf("ошибка %d", he.Status)
	}
	return UserMessage(err)
}

// searchOut: one search answer, parsed.
type searchOut struct {
	text     string
	searches int
	sources  int
	cites    int
	errCodes []string
}

func parseSearch(content []json.RawMessage, so *searchOut) {
	for _, raw := range content {
		var b struct {
			Type      string            `json:"type"`
			Name      string            `json:"name"`
			Text      string            `json:"text"`
			Citations []json.RawMessage `json:"citations"`
			Content   json.RawMessage   `json:"content"`
		}
		if json.Unmarshal(raw, &b) != nil {
			continue
		}
		switch b.Type {
		case "text":
			so.text += b.Text
			so.cites += len(b.Citations)
		case "server_tool_use":
			if b.Name == "web_search" {
				so.searches++
			}
		case "web_search_tool_result":
			var list []struct {
				Type string `json:"type"`
				URL  string `json:"url"`
			}
			if json.Unmarshal(b.Content, &list) == nil {
				for _, r := range list {
					if r.Type == "web_search_result" {
						so.sources++
					}
				}
				continue
			}
			var e struct {
				Type      string `json:"type"`
				ErrorCode string `json:"error_code"`
			}
			if json.Unmarshal(b.Content, &e) == nil && e.ErrorCode != "" {
				so.errCodes = append(so.errCodes, e.ErrorCode)
			}
		}
	}
}

// searchCodeText: a web_search_tool_result_error code in words.
func searchCodeText(code string) string {
	switch code {
	case "too_many_requests":
		return "поиск ограничил частоту запросов (too_many_requests)"
	case "invalid_tool_input":
		return "неверный поисковый запрос (invalid_tool_input)"
	case "max_uses_exceeded":
		return "исчерпан лимит поисков в одном ответе (max_uses_exceeded)"
	case "query_too_long":
		return "слишком длинный запрос (query_too_long)"
	case "request_too_large":
		return "слишком большой запрос (request_too_large)"
	case "unavailable":
		return "поиск Anthropic временно недоступен (unavailable)"
	}
	return code
}

// searchOnce: one search conversation with cfg (pause_turn goes on).
func (c *Client) searchOnce(ctx context.Context, cfg searchCfg, prompt string, uses, maxTok int) (searchOut, string, error) {
	if cfg.model != "" {
		ctx = withModel(ctx, cfg.model)
	}
	msgs := []map[string]any{{"role": "user", "content": prompt}}
	var so searchOut
	model := ""
	for turn := 0; turn < 5; turn++ {
		body := map[string]any{"max_tokens": maxTok, "tools": []map[string]any{cfg.tool(uses)}, "messages": msgs}
		b, err := c.claudeMessages(ctx, body)
		if err != nil {
			return so, model, err
		}
		model, _ = body["model"].(string)
		var out claudeOut
		if err := json.Unmarshal(b, &out); err != nil {
			return so, model, fmt.Errorf("ответ ИИ не читается: %v", err)
		}
		parseSearch(out.Content, &so)
		if out.StopReason != "pause_turn" {
			break
		}
		msgs = append(msgs, map[string]any{"role": "assistant", "content": out.Content})
	}
	if strings.TrimSpace(so.text) == "" {
		if len(so.errCodes) > 0 {
			return so, model, &SearchError{Kind: "results", Msg: "Поиск в интернете не сработал: " + searchCodeText(so.errCodes[len(so.errCodes)-1])}
		}
		return so, model, &ErrEmptyAnswer{}
	}
	return so, model, nil
}

// claudeSearch: Claude with the web search server tool (see the top).
func (c *Client) claudeSearch(ctx context.Context, prompt string) (string, error) {
	so, err := c.searchRun(ctx, prompt, searchUses(8), 16000)
	return so.text, err
}

func (c *Client) searchRun(ctx context.Context, prompt string, uses, maxTok int) (searchOut, error) {
	m := c.smemo()
	done := func(so searchOut, cfg searchCfg, model string, err error) (searchOut, error) {
		si := SearchInfo{At: time.Now(), OK: err == nil, Model: model, Tool: cfg.version, Searches: so.searches, Sources: so.sources}
		if err != nil {
			si.Error = SearchMessage(err)
		}
		c.noteSearch(si)
		return so, err
	}
	m.mu.Lock()
	cached := m.cfg
	if m.head != searchVersions()[0] {
		cached = nil
	}
	m.mu.Unlock()
	if cached != nil {
		so, model, err := c.searchOnce(ctx, *cached, prompt, uses, maxTok)
		if err == nil || searchErrKind(err) == "" {
			return done(so, *cached, model, err)
		}
		m.mu.Lock()
		m.cfg = nil
		m.mu.Unlock()
	}
	base := c.models(ctx)[0]
	models := []string{""}
	if sm := searchModel(); sm != base {
		models = append(models, sm)
	}
	var toolErr error
	last := searchCfg{}
	for _, model := range models {
		for _, v := range searchVersions() {
			cfg := searchCfg{version: v, model: model}
			for fix := 0; fix < 3; fix++ {
				so, mdl, err := c.searchOnce(ctx, cfg, prompt, uses, maxTok)
				last = cfg
				if err == nil {
					m.mu.Lock()
					cp := cfg
					m.cfg, m.head = &cp, searchVersions()[0]
					m.mu.Unlock()
					if model != "" || v != searchVersions()[0] || cfg.noLoc {
						log.Printf("ai: web search works with %s, %s (location %v)", mdl, v, !cfg.noLoc)
					}
					return done(so, cfg, mdl, nil)
				}
				kind := searchErrKind(err)
				switch {
				case kind == "location" && !cfg.noLoc:
					log.Printf("ai: web search %s: location refused (%s), without it", v, anthropicSays(err))
					cfg.noLoc = true
					continue
				case kind == "callers" && !cfg.noCallers:
					cfg.noCallers = true
					continue
				case kind == "disabled":
					return done(so, cfg, mdl, &SearchError{Kind: "disabled", Err: err,
						Msg: "Поиск в интернете выключен в настройках организации Anthropic: Claude Console → Settings → Capabilities → Web search (ответ Anthropic: " + anthropicSays(err) + ")"})
				case kind == "":
					return done(so, cfg, mdl, err)
				}
				// the tool version (or the rest of the request) is refused: the next version
				log.Printf("ai: web search %s on %s refused: %s", v, orModel(mdl, base), anthropicSays(err))
				toolErr = err
				break
			}
		}
	}
	name := searchModel()
	return done(searchOut{}, last, name, &SearchError{Kind: "unsupported", Err: toolErr,
		Msg: "Модель " + base + " (и запасная " + name + ") не принимает поиск в интернете: " + anthropicSays(toolErr) + ". Укажите модель с поиском в AI_SEARCH_MODEL (например " + DefaultSearchModel + ")"})
}

func orModel(m, def string) string {
	if m != "" {
		return m
	}
	return def
}

// SearchMessage: a search error in words (Anthropic's message kept, no JSON).
func SearchMessage(err error) string {
	if err == nil {
		return ""
	}
	var se *SearchError
	if errors.As(err, &se) {
		return se.Msg
	}
	return UserMessage(err)
}

// SearchPing: a tiny live search for /status (at most one search; the
// result is kept 10 minutes so repeated checks do not pay again).
func (c *Client) SearchPing(ctx context.Context) (SearchInfo, error) {
	if !c.HasClaude() {
		return SearchInfo{}, ErrNoKey
	}
	m := c.smemo()
	m.mu.Lock()
	p := m.ping
	m.mu.Unlock()
	if !p.At.IsZero() && time.Since(p.At) < 10*time.Minute {
		if p.OK {
			return p, nil
		}
		return p, errors.New(p.Error)
	}
	so, err := c.searchRun(ctx, "Найди в интернете официальный сайт акимата города Алматы и ответь одной строкой: его адрес.", 1, 400)
	si := c.LastSearch()
	si.Searches, si.Sources = so.searches, so.sources
	m.mu.Lock()
	m.ping = si
	m.mu.Unlock()
	return si, err
}
