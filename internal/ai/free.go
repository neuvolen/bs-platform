package ai

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"
)

// R42: the platform's AI keeps working on free tiers.
//
// The owner pays for no AI API. The text chain is
//
//	Claude (a key with balance) → Gemini (a free AI Studio key) → Groq (free)
//	→ OpenRouter (":free" models) → OpenAI (only when its key is set)
//
// Every provider with a key is tried in turn; one whose quota, balance or
// daily budget is used up is skipped without a call (quota.go holds). A
// per-provider daily budget (AI_BUDGET_<NAME>) keeps the platform inside the
// free limits:
//
//	gemini       1000 requests a day (free Flash: about 1 500; resets at
//	             midnight Pacific, as Google does)
//	gemini_search 100 grounded searches a day (free: 5 000 a month)
//	groq          900 (free: 1 000 a day per model)
//	openrouter     45 (free: 50 a day without a credit top-up)
//	claude, openai  no budget (0 = unlimited)
//
// The smallest adequate model answers: a short JSON task goes to the light
// model (Gemini Flash-Lite, Groq gpt-oss-20b), a Heavy(ctx) task (Gallup deep
// analysis, the разбор summary) to the best free model (Gemini Flash, Groq
// gpt-oss-120b). Web search: Claude's web search tool, then Gemini with
// Google Search grounding; with neither the callers degrade (events keep the
// feed, AI recs come from the library and the model's knowledge, «без поиска»).
//
// Keys come from GEMINI_API_KEY (or GOOGLE_API_KEY), GROQ_API_KEY and
// OPENROUTER_API_KEY. A key is never logged or sent to the page.

// compat: an OpenAI-compatible chat completions provider (Groq, OpenRouter,
// OpenAI itself).
type compat struct {
	Name, Label       string
	Base              string // e.g. https://api.groq.com/openai/v1
	Key               string
	Model, LightModel string
	JSONMode          bool // response_format json_object is accepted
}

// providerOrder: the default order of the text chain.
var providerOrder = []string{"claude", "gemini", "groq", "openrouter", "openai"}

// ProviderLabel: the provider's name for people.
func ProviderLabel(name string) string {
	switch name {
	case "claude":
		return "Claude"
	case "gemini":
		return "Gemini"
	case "groq":
		return "Groq"
	case "openrouter":
		return "OpenRouter"
	case "openai":
		return "OpenAI"
	}
	return name
}

// freeProvider: costs nothing (a free tier).
func freeProvider(name string) bool {
	return name == "gemini" || name == "groq" || name == "openrouter"
}

// envClean: a key from the environment, without quotes, spaces or «Bearer ».
func envClean(names ...string) string {
	for _, n := range names {
		if v := strings.TrimSpace(os.Getenv(n)); v != "" {
			k := cleanAPIKey(v) // R51: also «NAME=key» and anything after a space
			if k != "" {
				return k
			}
		}
	}
	return ""
}

// GeminiDisabled: GEMINI_ENABLED=0 (or false, off, no) opts out of Gemini.
func GeminiDisabled() bool {
	v := strings.ToLower(strings.TrimSpace(os.Getenv("GEMINI_ENABLED")))
	return v == "0" || v == "false" || v == "off" || v == "no"
}

// ── daily budgets ──

var defaultBudgets = map[string]int{"gemini": 1000, "gemini_search": 100, "groq": 900, "openrouter": 45}

// Budget: the provider's daily request budget (AI_BUDGET_GEMINI, …); 0: none.
func Budget(name string) int {
	env := "AI_BUDGET_" + strings.ToUpper(name)
	if v, err := strconv.Atoi(strings.TrimSpace(os.Getenv(env))); err == nil && v >= 0 {
		return v
	}
	return defaultBudgets[name]
}

// budgetDay: the provider's day; Google resets at midnight Pacific, the others at midnight UTC.
func budgetDay(name string, now time.Time) string {
	if strings.HasPrefix(name, "gemini") {
		return now.In(pacific).Format("2006-01-02")
	}
	return now.UTC().Format("2006-01-02")
}

// budgetReset: when the provider's day ends.
func budgetReset(name string, now time.Time) time.Time {
	if strings.HasPrefix(name, "gemini") {
		return nextPacificMidnight(now)
	}
	u := now.UTC()
	return time.Date(u.Year(), u.Month(), u.Day()+1, 0, 0, 0, 0, time.UTC)
}

type budgets struct {
	mu   sync.Mutex
	day  map[string]string
	used map[string]int
}

// Used: requests made today by the provider.
func (c *Client) Used(name string) int {
	c.budget.mu.Lock()
	defer c.budget.mu.Unlock()
	if c.budget.day[name] != budgetDay(name, quotaNow()) {
		return 0
	}
	return c.budget.used[name]
}

// spend takes one request of the provider's budget; a used-up budget answers
// a *QuotaError (Budget) until the day ends.
func (c *Client) spend(name string) error {
	limit := Budget(name)
	now := quotaNow()
	day := budgetDay(name, now)
	c.budget.mu.Lock()
	defer c.budget.mu.Unlock()
	if c.budget.day == nil {
		c.budget.day, c.budget.used = map[string]string{}, map[string]int{}
	}
	if c.budget.day[name] != day {
		c.budget.day[name], c.budget.used[name] = day, 0
	}
	if limit > 0 && c.budget.used[name] >= limit {
		svc := strings.TrimSuffix(name, "_search")
		return &QuotaError{Service: svc, Until: budgetReset(name, now), Daily: true, Budget: true}
	}
	c.budget.used[name]++
	return nil
}

// budgetLeft: the provider's budget is not used up (no budget: always).
func (c *Client) budgetLeft(name string) bool {
	limit := Budget(name)
	return limit == 0 || c.Used(name) < limit
}

// SetUsed: tests and a restart's restore.
func (c *Client) SetUsed(name string, n int) {
	c.budget.mu.Lock()
	defer c.budget.mu.Unlock()
	if c.budget.day == nil {
		c.budget.day, c.budget.used = map[string]string{}, map[string]int{}
	}
	c.budget.day[name], c.budget.used[name] = budgetDay(name, quotaNow()), n
}

// ── task size ──

type lightKey struct{}

// Light marks ctx as a short task (a classification, a small JSON): the
// light free model answers.
func Light(ctx context.Context) context.Context { return context.WithValue(ctx, lightKey{}, true) }

// isLight: marked Light, or a short JSON task that is not Heavy.
func isLight(ctx context.Context, asJSON bool, system, prompt string) bool {
	if isHeavy(ctx) {
		return false
	}
	if v, _ := ctx.Value(lightKey{}).(bool); v {
		return true
	}
	return asJSON && len([]rune(system))+len([]rune(prompt)) < lightRunes
}

// lightRunes: a JSON task shorter than this goes to the light model.
const lightRunes = 6000

// ── OpenAI-compatible chat ──

func (c *Client) compats() map[string]*compat {
	out := map[string]*compat{}
	if c.Groq != "" {
		out["groq"] = &compat{Name: "groq", Label: "Groq", Base: orDef(c.GroqBase, "https://api.groq.com/openai/v1"), Key: c.Groq,
			Model: orDef(c.GroqModel, "openai/gpt-oss-120b"), LightModel: orDef(c.GroqLightModel, "openai/gpt-oss-20b"), JSONMode: true}
	}
	if c.OpenRouter != "" {
		m := orDef(c.OpenRouterModel, "openrouter/free")
		out["openrouter"] = &compat{Name: "openrouter", Label: "OpenRouter", Base: orDef(c.OpenRouterBase, "https://openrouter.ai/api/v1"), Key: c.OpenRouter,
			Model: m, LightModel: orDef(c.OpenRouterLightModel, m)}
	}
	if c.OpenAI != "" {
		out["openai"] = &compat{Name: "openai", Label: "OpenAI", Base: strings.TrimRight(orDef(c.OpenAIBase, "https://api.openai.com"), "/") + "/v1", Key: c.OpenAI,
			Model: orDef(c.OpenAIModel, "gpt-4o-mini"), LightModel: orDef(c.OpenAIModel, "gpt-4o-mini"), JSONMode: true}
	}
	return out
}

func orDef(v, def string) string {
	if strings.TrimSpace(v) != "" {
		return strings.TrimSpace(v)
	}
	return def
}

// ProviderError: which provider refused (its key, its limits).
type ProviderError struct {
	Name string
	Err  error
}

func (e *ProviderError) Error() string { return ProviderLabel(e.Name) + ": " + UserMessage(e.Err) }
func (e *ProviderError) Unwrap() error { return e.Err }

// compatQuota: a refusal of an OpenAI-compatible provider that means its
// limit or balance: 429 (per minute, or per day), 402 (no credits).
func compatQuota(err error) (QuotaInfo, bool) {
	var he *HTTPError
	if !errors.As(err, &he) {
		return QuotaInfo{}, false
	}
	low := strings.ToLower(he.Body)
	switch {
	case he.Status == 402:
		return QuotaInfo{Billing: true}, true
	case he.Status == 429:
		q := QuotaInfo{Retry: he.RetryAfter}
		if strings.Contains(low, "per day") || strings.Contains(low, "per-day") || strings.Contains(low, "(rpd)") ||
			strings.Contains(low, "(tpd)") || strings.Contains(low, "free-models-per-day") || strings.Contains(low, "daily") {
			q.Daily = true
		}
		return q, true
	}
	return QuotaInfo{}, false
}

// compatChat: one chat completions request.
func (c *Client) compatChat(ctx context.Context, p *compat, system, prompt string, asJSON, light bool) (string, string, error) {
	if q := c.quotaClosed(p.Name); q != nil {
		return "", "", q
	}
	model := p.Model
	if light && p.LightModel != "" {
		model = p.LightModel
	}
	if asJSON {
		system = strings.TrimSpace(system + jsonOnly)
	}
	body := map[string]any{
		"model":    model,
		"messages": []map[string]any{{"role": "system", "content": system}, {"role": "user", "content": prompt}},
	}
	if asJSON {
		body["temperature"] = 0.2
		if p.JSONMode {
			body["response_format"] = map[string]any{"type": "json_object"}
		}
	}
	r := jsonReq("POST", strings.TrimRight(p.Base, "/")+"/chat/completions", body)
	r.Header.Set("Authorization", "Bearer "+p.Key)
	if p.Name == "openrouter" {
		r.Header.Set("HTTP-Referer", "https://bxclub.kz")
		r.Header.Set("X-Title", "Business Surgery")
	}
	b, err := c.do(ctx, r)
	if err != nil {
		if q, ok := compatQuota(err); ok {
			return "", model, c.hold(p.Name, q, err)
		}
		return "", model, &ProviderError{Name: p.Name, Err: err}
	}
	var out struct {
		Model   string `json:"model"`
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
			FinishReason string `json:"finish_reason"`
		} `json:"choices"`
		Error *struct {
			Message string `json:"message"`
			Code    any    `json:"code"`
		} `json:"error"`
	}
	if err := json.Unmarshal(b, &out); err != nil {
		return "", model, fmt.Errorf("ответ ИИ не читается: %v", err)
	}
	if out.Error != nil && out.Error.Message != "" {
		// OpenRouter puts an upstream failure in a 200 answer
		return "", model, &ProviderError{Name: p.Name, Err: errors.New(out.Error.Message)}
	}
	if len(out.Choices) == 0 || strings.TrimSpace(out.Choices[0].Message.Content) == "" {
		reason := ""
		if len(out.Choices) > 0 {
			reason = out.Choices[0].FinishReason
		}
		return "", model, &ErrEmptyAnswer{Reason: reason}
	}
	if out.Model != "" {
		model = out.Model
	}
	return out.Choices[0].Message.Content, model, nil
}

// ── who answers ──

// ProviderState: one provider for «Состояние ИИ» and /status.
type ProviderState struct {
	Name   string `json:"name"`
	Label  string `json:"label"`
	State  string `json:"state"` // ok | none | quota | billing | budget | off
	Until  string `json:"until,omitempty"`
	Used   int    `json:"used"`
	Budget int    `json:"budget"`
	Model  string `json:"model,omitempty"`
	Free   bool   `json:"free"`
	Active bool   `json:"active,omitempty"`
	Env    string `json:"env"` // the variable that holds the key (a name only)
	// Problem: why the provider is not used, in Russian (R51: «badkey»)
	Problem string `json:"problem,omitempty"`
	Action  string `json:"action,omitempty"` // what the owner does about it
	// legacy fields read by older pages
	Daily   bool `json:"daily,omitempty"`
	Billing bool `json:"billing,omitempty"`
}

var providerEnv = map[string]string{"claude": "ANTHROPIC_API_KEY", "gemini": "GEMINI_API_KEY", "groq": "GROQ_API_KEY",
	"openrouter": "OPENROUTER_API_KEY", "openai": "OPENAI_API_KEY"}

func (c *Client) providerKey(name string) string {
	switch name {
	case "claude":
		return c.claudeKey()
	case "gemini":
		return c.Gemini
	case "groq":
		return c.Groq
	case "openrouter":
		return c.OpenRouter
	case "openai":
		return c.OpenAI
	}
	return ""
}

// state: the provider's state now.
func (c *Client) state(name string) ProviderState {
	p := ProviderState{Name: name, Label: ProviderLabel(name), State: "ok", Used: c.Used(name), Budget: Budget(name),
		Free: freeProvider(name), Env: providerEnv[name]}
	switch name {
	case "claude":
		if c.HasClaude() {
			p.Model = c.models(context.Background())[0]
		}
	case "gemini":
		p.Model = c.geminiModel()
		if GeminiDisabled() && c.Gemini == "" && strings.TrimSpace(os.Getenv("GEMINI_API_KEY")) != "" {
			p.State = "off"
			return p
		}
	default:
		if cp := c.compats()[name]; cp != nil {
			p.Model = cp.Model
		}
	}
	if c.providerKey(name) == "" {
		p.State = "none"
		return p
	}
	if ke := c.keyClosed(name); ke != nil && name != "claude" { // R51: the key was refused
		p.State, p.Problem, p.Action = "badkey", ke.Problem(), ke.Action()
		return p
	}
	if q := c.quotaClosed(name); q != nil {
		p.State, p.Until = "quota", q.Until.UTC().Format(time.RFC3339)
		p.Daily, p.Billing = q.Daily, q.Billing
		if q.Billing {
			p.State = "billing"
		}
		if q.Budget {
			p.State = "budget"
		}
		return p
	}
	if !c.budgetLeft(name) {
		p.State, p.Until = "budget", budgetReset(name, quotaNow()).UTC().Format(time.RFC3339)
	}
	return p
}

// ProviderStates: every known provider in the chain's order (those without
// a key too, so the page can say «нет ключа»). The one that would answer a
// text task now is Active.
func (c *Client) ProviderStates() []ProviderState {
	seen := map[string]bool{}
	var out []ProviderState
	for _, n := range c.TextModels() {
		seen[n] = true
		out = append(out, c.state(n))
	}
	for _, n := range providerOrder {
		if !seen[n] && n != "openai" {
			out = append(out, c.state(n))
		}
	}
	for i := range out {
		if out[i].State == "ok" {
			out[i].Active = true
			break
		}
	}
	return out
}

// Answering: the provider that would answer a text task now ("" when none).
func (c *Client) Answering() string {
	for _, p := range c.ProviderStates() {
		if p.Active {
			return p.Name
		}
	}
	return ""
}

// BudgetAllows: a heavy optional task (the Threads batch) may use the AI:
// the provider answering now has no budget, or keeps more than reservePct of it.
func (c *Client) BudgetAllows(reservePct int) bool {
	for _, p := range c.ProviderStates() {
		if !p.Active {
			continue
		}
		if p.Budget == 0 {
			return true
		}
		return (p.Budget-p.Used)*100 > p.Budget*reservePct
	}
	return false
}

// ── the owner hears when the answering provider changes ──

type switchState struct {
	mu   sync.Mutex
	last string // the provider that answered last (durably)
}

// longHold: the provider rests long enough (a day, the balance, the budget)
// for a switch away from it to be worth a message; a minute's limit is not.
func (c *Client) longHold(name string) bool {
	if c.providerKey(name) == "" {
		return true
	}
	if q := c.quotaClosed(name); q != nil {
		return q.Daily || q.Billing || q.Budget || q.Until.Sub(quotaNow()) >= 15*time.Minute
	}
	return !c.budgetLeft(name)
}

// noteAnswered: name answered a text task. When it is not the provider that
// answered before and the switch is durable (every provider before it in the
// chain rests long or has no key), OnSwitch is told once.
func (c *Client) noteAnswered(name string, order []string) {
	durable := true
	for _, m := range order {
		if m == name {
			break
		}
		if !c.longHold(m) {
			durable = false
		}
	}
	c.sw.mu.Lock()
	prev := c.sw.last
	changed := prev != name && durable
	if changed {
		c.sw.last = name
	}
	c.sw.mu.Unlock()
	if changed && c.OnSwitch != nil {
		go c.OnSwitch(prev, name)
	}
}

// SwitchReason: why the providers before name are not answering, in words.
func (c *Client) SwitchReason(name string) string {
	var parts []string
	for _, m := range c.TextModels() {
		if m == name {
			break
		}
		st := c.state(m)
		switch st.State {
		case "billing":
			parts = append(parts, st.Label+": нет баланса")
		case "quota", "budget":
			parts = append(parts, st.Label+": лимит на сегодня исчерпан")
		case "none":
		default:
			parts = append(parts, st.Label+": не отвечает")
		}
	}
	return strings.Join(parts, ", ")
}

// ── web search without a paid model ──

// ErrNoSearch: no model can search the web now (no Claude balance, no
// Gemini key or its grounding budget is used up).
var ErrNoSearch = errors.New("Поиск в интернете сейчас недоступен: у Claude нет баланса, а бесплатный лимит поиска Gemini исчерпан или ключа GEMINI_API_KEY нет")

// SearchUnavailable: err means the web search cannot run now (callers degrade).
func SearchUnavailable(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, ErrNoSearch) || errors.Is(err, ErrNoKey) || IsQuota(err) || IsKeyRejected(err) {
		return true
	}
	var se *SearchError
	return errors.As(err, &se) && se.Kind == "nosearch"
}

// ── a live check of one provider (/status) ──

type pingMemo struct {
	mu  sync.Mutex
	res map[string]pingRes
}

type pingRes struct {
	at    time.Time
	model string
	err   error
}

var pings sync.Map // *Client → *pingMemo

// PingProvider makes a tiny text call to one free provider (kept 10 minutes,
// so repeated checks do not spend the budget). Claude has its own Ping.
func (c *Client) PingProvider(ctx context.Context, name string) (string, error) {
	v, _ := pings.LoadOrStore(c, &pingMemo{res: map[string]pingRes{}})
	pm := v.(*pingMemo)
	pm.mu.Lock()
	r, ok := pm.res[name]
	pm.mu.Unlock()
	if ok && time.Since(r.at) < 10*time.Minute {
		return r.model, r.err
	}
	model, err := c.pingOnce(ctx, name)
	pm.mu.Lock()
	pm.res[name] = pingRes{at: time.Now(), model: model, err: err}
	pm.mu.Unlock()
	return model, err
}

func (c *Client) pingOnce(ctx context.Context, name string) (string, error) {
	if c.providerKey(name) == "" {
		return "", ErrNoKey
	}
	if q := c.quotaClosed(name); q != nil {
		return "", q
	}
	if err := c.spend(name); err != nil {
		return "", err
	}
	const sys, ask = "Отвечай одним словом.", "Ответь одним словом: ок"
	switch name {
	case "gemini":
		m := c.GeminiLightModel
		_, err := c.geminiCfgModel(ctx, m, sys, []map[string]any{{"text": ask}}, nil)
		if err != nil {
			return "", err
		}
		if m == "" || c.quotaClosed("gemini-lite") != nil {
			m = c.geminiModel()
		}
		return m, nil
	case "claude":
		return c.Ping(ctx, "")
	}
	p := c.compats()[name]
	if p == nil {
		return "", ErrNoKey
	}
	_, model, err := c.compatChat(ctx, p, sys, ask, false, true)
	return model, err
}
