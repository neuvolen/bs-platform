package ai

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"
)

// R34a: Claude is the one model of the platform. Text, JSON, reasoning and
// web search go to the Messages API; Gemini is asked only when the owner
// switches it on for an emergency (GEMINI_ENABLED=1).
//
//	AI_MODEL        most tasks (default claude-sonnet-5)
//	AI_MODEL_HEAVY  long reasoning: Gallup deep analysis, разбор summary
//	                (default claude-opus-5; a model the key cannot use falls
//	                back to AI_MODEL)
//
// The key: ANTHROPIC_API_KEY (Railway) or, when it is not set, the key the
// owner pasted in the platform settings (keys.go). Neither is ever logged.

const (
	DefaultModel      = "claude-sonnet-5"
	DefaultHeavyModel = "claude-opus-5"
	anthropicVersion  = "2023-06-01"
	// DefaultWebSearchTool: the web search server tool (AI_WEB_SEARCH_TOOL).
	DefaultWebSearchTool = "web_search_20250305"
	// cacheSystemMin: a system prompt this long (runes) is marked for prompt
	// caching; shorter ones are below the API's minimum cacheable size anyway.
	cacheSystemMin = 3000
)

// ClaudeBackoff: the waits between attempts on 429 / 529 / 5xx (tests shorten them).
var ClaudeBackoff = []time.Duration{3 * time.Second, 10 * time.Second, 30 * time.Second}

type heavyKey struct{}

// Heavy marks ctx for the heavy model (AI_MODEL_HEAVY): Gallup deep analysis,
// the разбор summary. Everything else runs on AI_MODEL.
func Heavy(ctx context.Context) context.Context { return context.WithValue(ctx, heavyKey{}, true) }

func isHeavy(ctx context.Context) bool { v, _ := ctx.Value(heavyKey{}).(bool); return v }

// claudeKey: the env key, else the key saved in the settings.
func (c *Client) claudeKey() string {
	if c.Anthropic != "" {
		return c.Anthropic
	}
	if c.Keys != nil {
		return c.Keys.Get()
	}
	return ""
}

// HasClaude: a Claude key is there (env or settings).
func (c *Client) HasClaude() bool { return c != nil && c.claudeKey() != "" }

// HasText: some model can answer a text task.
func (c *Client) HasText() bool { return c != nil && len(c.TextModels()) > 0 }

// KeySource: "env", "settings" or "" (no key), for the settings page.
func (c *Client) KeySource() string {
	switch {
	case c.Anthropic != "":
		return "env"
	case c.Keys != nil && c.Keys.Get() != "":
		return "settings"
	}
	return ""
}

func (c *Client) models(ctx context.Context) []string {
	c.modelMu.Lock()
	base := c.ClaudeModel
	c.modelMu.Unlock()
	if base == "" {
		base = DefaultModel
	}
	if isHeavy(ctx) && c.HeavyModel != "" && c.HeavyModel != base {
		return []string{c.HeavyModel, base}
	}
	return []string{base}
}

func maxTokens(model string, heavy bool) int {
	if heavy {
		return 24000
	}
	return 16000
}

// claudeSystem: the system prompt; a long one becomes a cached block.
func claudeSystem(system string) any {
	if strings.TrimSpace(system) == "" {
		return nil
	}
	if len([]rune(system)) < cacheSystemMin {
		return system
	}
	return []map[string]any{{"type": "text", "text": system, "cache_control": map[string]any{"type": "ephemeral"}}}
}

// claudePost sends one Messages request with the given key, retrying an
// overloaded or rate-limited API (429, 529, 5xx) with backoff and the
// API's retry-after.
func (c *Client) claudePost(ctx context.Context, key string, body map[string]any) ([]byte, error) {
	if key == "" {
		return nil, ErrNoKey
	}
	if q := c.quotaClosed("claude"); q != nil {
		return nil, q
	}
	for i := 0; ; i++ {
		r := jsonReq("POST", strings.TrimRight(c.AnthropicBase, "/")+"/v1/messages", body)
		r.Header.Set("x-api-key", key)
		r.Header.Set("anthropic-version", anthropicVersion)
		b, err := c.do(ctx, r)
		if err == nil {
			return b, nil
		}
		if q := c.noteQuota("claude", err); q != nil {
			return nil, q
		}
		if !claudeRetry(err) || i >= len(ClaudeBackoff) || ctx.Err() != nil {
			return nil, err
		}
		wait := ClaudeBackoff[i]
		var he *HTTPError
		if errors.As(err, &he) && he.RetryAfter > 0 && he.RetryAfter < 2*time.Minute && he.RetryAfter > wait {
			wait = he.RetryAfter
		}
		if serr := sleepCtx(ctx, wait); serr != nil {
			return nil, err
		}
	}
}

func claudeRetry(err error) bool {
	var he *HTTPError
	if errors.As(err, &he) {
		switch he.Status {
		case 408, 409, 429, 500, 502, 503, 504, 529:
			return true
		}
		return false
	}
	return transient(err)
}

// modelMissing: the API does not know the model (or the key cannot use it).
func modelMissing(err error) bool {
	var he *HTTPError
	if !errors.As(err, &he) {
		return false
	}
	low := strings.ToLower(he.Body)
	return he.Status == 404 || (he.Status == 400 && strings.Contains(low, "model") && (strings.Contains(low, "not found") || strings.Contains(low, "invalid") || strings.Contains(low, "does not exist")))
}

// claudeMessages runs a request on the task's model; a model the API does
// not know is replaced by the next one (heavy → AI_MODEL), and as a last
// resort by the newest model of the same family the key can use.
func (c *Client) claudeMessages(ctx context.Context, body map[string]any) ([]byte, error) {
	key := c.claudeKey()
	var lastErr error
	tried := map[string]bool{}
	ms := c.models(ctx)
	for i := 0; i < len(ms); i++ {
		m := ms[i]
		if tried[m] {
			continue
		}
		tried[m] = true
		body["model"] = m
		if _, ok := body["max_tokens"]; !ok || i > 0 {
			body["max_tokens"] = maxTokens(m, isHeavy(ctx) && i == 0)
		}
		b, err := c.claudePost(ctx, key, body)
		if err == nil {
			return b, nil
		}
		lastErr = err
		if !modelMissing(err) {
			return nil, err
		}
		log.Printf("ai: model %s is not available (%s)", m, UserMessage(err))
		if i == len(ms)-1 {
			if alt := c.newestClaude(ctx, key, m); alt != "" && !tried[alt] {
				ms = append(ms, alt)
				c.modelMu.Lock()
				if m == c.ClaudeModel || c.ClaudeModel == "" {
					c.ClaudeModel = alt
				}
				c.modelMu.Unlock()
			}
		}
	}
	return nil, lastErr
}

// newestClaude: the newest model of the family of cur ("sonnet", "opus",
// "haiku") that the key lists in GET /v1/models.
func (c *Client) newestClaude(ctx context.Context, key, cur string) string {
	fam := "sonnet"
	for _, f := range []string{"opus", "haiku", "sonnet"} {
		if strings.Contains(cur, f) {
			fam = f
			break
		}
	}
	r, _ := http.NewRequest("GET", strings.TrimRight(c.AnthropicBase, "/")+"/v1/models?limit=100", nil)
	r.Header.Set("x-api-key", key)
	r.Header.Set("anthropic-version", anthropicVersion)
	b, err := c.do(ctx, r)
	if err != nil {
		return ""
	}
	var out struct {
		Data []struct {
			ID        string `json:"id"`
			CreatedAt string `json:"created_at"`
		} `json:"data"`
	}
	_ = json.Unmarshal(b, &out)
	var cs []struct{ id, at string }
	for _, m := range out.Data {
		if strings.Contains(m.ID, fam) && m.ID != cur {
			cs = append(cs, struct{ id, at string }{m.ID, m.CreatedAt})
		}
	}
	sort.SliceStable(cs, func(i, j int) bool { return cs[i].at > cs[j].at })
	if len(cs) == 0 {
		return ""
	}
	return cs[0].id
}

type claudeOut struct {
	StopReason string            `json:"stop_reason"`
	Content    []json.RawMessage `json:"content"`
}

func (o claudeOut) text() string {
	var sb strings.Builder
	for _, raw := range o.Content {
		var p struct {
			Type string `json:"type"`
			Text string `json:"text"`
		}
		if json.Unmarshal(raw, &p) == nil && p.Type == "text" {
			sb.WriteString(p.Text)
		}
	}
	return sb.String()
}

const jsonOnly = "\n\nОтвет: только один JSON-объект, без пояснений до и после, без ```."

// claude answers a text (or JSON) task.
func (c *Client) claude(ctx context.Context, system, prompt string, asJSON bool) (string, error) {
	if asJSON {
		system = strings.TrimSpace(system + jsonOnly)
	}
	body := map[string]any{"messages": []map[string]any{{"role": "user", "content": prompt}}}
	if s := claudeSystem(system); s != nil {
		body["system"] = s
	}
	if asJSON {
		body["temperature"] = 0.2
	}
	b, err := c.claudeMessages(ctx, body)
	if err != nil {
		return "", err
	}
	var out claudeOut
	if err := json.Unmarshal(b, &out); err != nil {
		return "", fmt.Errorf("ответ ИИ не читается: %v", err)
	}
	ans := out.text()
	if strings.TrimSpace(ans) == "" {
		return "", &ErrEmptyAnswer{Reason: out.StopReason}
	}
	return ans, nil
}

func (c *Client) webSearchTool() map[string]any {
	t := strings.TrimSpace(os.Getenv("AI_WEB_SEARCH_TOOL"))
	if t == "" {
		t = DefaultWebSearchTool
	}
	uses := 8
	if v, err := strconv.Atoi(os.Getenv("AI_WEB_SEARCH_MAX_USES")); err == nil && v > 0 {
		uses = v
	}
	return map[string]any{"type": t, "name": "web_search", "max_uses": uses, "user_location": map[string]any{"type": "approximate", "city": "Almaty", "country": "KZ", "timezone": "Asia/Almaty"}}
}

// claudeSearch: Claude with the web search server tool. A long search
// pauses the turn (pause_turn): the answer so far goes back and Claude goes on.
func (c *Client) claudeSearch(ctx context.Context, prompt string) (string, error) {
	msgs := []map[string]any{{"role": "user", "content": prompt}}
	ans := ""
	for turn := 0; turn < 5; turn++ {
		body := map[string]any{"max_tokens": 16000, "tools": []map[string]any{c.webSearchTool()}, "messages": msgs}
		b, err := c.claudeMessages(ctx, body)
		if err != nil {
			return "", err
		}
		var out claudeOut
		if err := json.Unmarshal(b, &out); err != nil {
			return "", fmt.Errorf("ответ ИИ не читается: %v", err)
		}
		ans += out.text()
		if out.StopReason != "pause_turn" {
			break
		}
		msgs = append(msgs, map[string]any{"role": "assistant", "content": out.Content})
	}
	if strings.TrimSpace(ans) == "" {
		return "", &ErrEmptyAnswer{}
	}
	return ans, nil
}

// Ping makes a tiny call with key (the current key when empty): the
// settings' «Проверить». The answer is the model that replied.
func (c *Client) Ping(ctx context.Context, key string) (string, error) {
	if key == "" {
		key = c.claudeKey()
	}
	if key == "" {
		return "", ErrNoKey
	}
	m := c.models(ctx)[0]
	// no quota bookkeeping and no retries: the owner waits for the answer
	r := jsonReq("POST", strings.TrimRight(c.AnthropicBase, "/")+"/v1/messages", map[string]any{
		"model": m, "max_tokens": 8, "messages": []map[string]any{{"role": "user", "content": "Ответь одним словом: ок"}},
	})
	r.Header.Set("x-api-key", key)
	r.Header.Set("anthropic-version", anthropicVersion)
	if _, err := c.do(ctx, r); err != nil {
		if modelMissing(err) {
			if alt := c.newestClaude(ctx, key, m); alt != "" {
				return alt, nil
			}
		}
		return "", err
	}
	return m, nil
}
