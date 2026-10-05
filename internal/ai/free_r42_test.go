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

// fakeFree: Claude, Gemini, Groq and OpenRouter on one test server.
type fakeFree struct {
	mu                        sync.Mutex
	claude, gem, groq, or     atomic.Int32
	gemSearch                 atomic.Int32
	claudeMode                string // "" ok | "billing"
	gemStatus, groqStatus     int
	gemBody, groqBody         string
	orStatus                  int
	gemModels, groqModels     []string
	groqAuth, orAuth, orTitle []string
	groqJSONMode              []bool
}

const geminiDaily = `{"error":{"code":429,"message":"You exceeded your current quota.","status":"RESOURCE_EXHAUSTED","details":[{"@type":"type.googleapis.com/google.rpc.QuotaFailure","violations":[{"quotaId":"GenerateRequestsPerDayPerProjectPerModel-FreeTier"}]}]}}`
const geminiMinute = `{"error":{"code":429,"message":"You exceeded your current quota.","status":"RESOURCE_EXHAUSTED","details":[{"@type":"type.googleapis.com/google.rpc.QuotaFailure","violations":[{"quotaId":"GenerateRequestsPerMinutePerProjectPerModel-FreeTier"}]},{"@type":"type.googleapis.com/google.rpc.RetryInfo","retryDelay":"20s"}]}}`

func (f *fakeFree) server(t *testing.T) *httptest.Server {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		var body map[string]any
		_ = json.Unmarshal(b, &body)
		switch {
		case r.URL.Path == "/v1/messages":
			f.claude.Add(1)
			if f.claudeMode == "billing" {
				w.WriteHeader(400)
				io.WriteString(w, `{"type":"error","error":{"type":"invalid_request_error","message":"Your credit balance is too low to access the Anthropic API."}}`)
				return
			}
			io.WriteString(w, `{"stop_reason":"end_turn","content":[{"type":"text","text":"claude says"}]}`)
		case strings.HasPrefix(r.URL.Path, "/v1beta/models/"):
			f.gem.Add(1)
			model := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/v1beta/models/"), ":generateContent")
			f.mu.Lock()
			f.gemModels = append(f.gemModels, model)
			f.mu.Unlock()
			if strings.Contains(string(b), "google_search") {
				f.gemSearch.Add(1)
			}
			if f.gemStatus != 0 {
				w.WriteHeader(f.gemStatus)
				io.WriteString(w, f.gemBody)
				return
			}
			io.WriteString(w, `{"candidates":[{"content":{"parts":[{"text":"thinking","thought":true},{"text":"gemini says {\"ok\":1}"}]}}]}`)
		case r.URL.Path == "/groq/chat/completions":
			f.groq.Add(1)
			f.mu.Lock()
			f.groqModels = append(f.groqModels, body["model"].(string))
			f.groqAuth = append(f.groqAuth, r.Header.Get("Authorization"))
			_, jm := body["response_format"]
			f.groqJSONMode = append(f.groqJSONMode, jm)
			f.mu.Unlock()
			if f.groqStatus != 0 {
				w.Header().Set("retry-after", "30")
				w.WriteHeader(f.groqStatus)
				io.WriteString(w, f.groqBody)
				return
			}
			io.WriteString(w, `{"model":"`+body["model"].(string)+`","choices":[{"message":{"content":"groq says"},"finish_reason":"stop"}]}`)
		case r.URL.Path == "/or/chat/completions":
			f.or.Add(1)
			f.mu.Lock()
			f.orAuth = append(f.orAuth, r.Header.Get("Authorization"))
			f.orTitle = append(f.orTitle, r.Header.Get("X-Title"))
			f.mu.Unlock()
			if f.orStatus != 0 {
				w.WriteHeader(f.orStatus)
				io.WriteString(w, `{"error":{"message":"Rate limit exceeded: free-models-per-day. Add 10 credits to unlock 1000 free model requests per day","code":429}}`)
				return
			}
			io.WriteString(w, `{"model":"some/model:free","choices":[{"message":{"content":"openrouter says"},"finish_reason":"stop"}]}`)
		default:
			w.WriteHeader(404)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func freeClient(srv *httptest.Server, claude bool) *Client {
	c := &Client{Gemini: "gk", GeminiModel: "gemini-main", GeminiLightModel: "gemini-lite", GeminiBase: srv.URL,
		Groq: "gsk", GroqBase: srv.URL + "/groq", GroqModel: "big", GroqLightModel: "small",
		OpenRouter: "ork", OpenRouterBase: srv.URL + "/or", OpenRouterModel: "openrouter/free",
		AnthropicBase: srv.URL, ClaudeModel: "claude-sonnet-5", HTTP: srv.Client(), GeminiTextOnly: true}
	if claude {
		c.Anthropic = "sk-ant-x"
	}
	return c
}

func switches(c *Client) (func() []string, func(int)) {
	var mu sync.Mutex
	var got []string
	ch := make(chan struct{}, 16)
	c.OnSwitch = func(from, to string) {
		mu.Lock()
		got = append(got, from+">"+to)
		mu.Unlock()
		ch <- struct{}{}
	}
	wait := func(n int) {
		for i := 0; i < n; i++ {
			select {
			case <-ch:
			case <-time.After(2 * time.Second):
				return
			}
		}
	}
	return func() []string {
		mu.Lock()
		defer mu.Unlock()
		return append([]string{}, got...)
	}, wait
}

// Claude without balance: Gemini answers, the owner is told once, Claude is not hammered.
func TestR42ChainClaudeBalanceToGemini(t *testing.T) {
	f := &fakeFree{claudeMode: "billing"}
	c := freeClient(f.server(t), true)
	got, wait := switches(c)
	for i := 0; i < 3; i++ {
		ans, err := c.Text(context.Background(), "s", "p")
		if err != nil || ans != "gemini says {\"ok\":1}" {
			t.Fatalf("answer %d: %q %v", i, ans, err)
		}
	}
	wait(1)
	if f.claude.Load() != 1 || f.gem.Load() != 3 || f.groq.Load() != 0 {
		t.Fatalf("calls: claude=%d gem=%d groq=%d", f.claude.Load(), f.gem.Load(), f.groq.Load())
	}
	if g := got(); len(g) != 1 || g[0] != ">gemini" {
		t.Fatalf("switches: %v", g)
	}
	if c.Answering() != "gemini" || c.Used("gemini") != 3 {
		t.Fatalf("answering %s used %d", c.Answering(), c.Used("gemini"))
	}
	st := c.Status()
	ch := st["chain"].([]ProviderState)
	if ch[0].Name != "claude" || ch[0].State != "billing" || ch[1].Name != "gemini" || !ch[1].Active || ch[1].Used != 3 || ch[1].Budget != 1000 {
		t.Fatalf("chain: %+v", ch)
	}
	if st["answering"] != "gemini" || st["paused"] == true {
		t.Fatalf("status: %+v", st)
	}
	if !strings.Contains(c.SwitchReason("gemini"), "Claude: нет баланса") {
		t.Fatal(c.SwitchReason("gemini"))
	}
}

// Gemini's day is over: Groq answers (one message); a per-minute limit is no message.
func TestR42ChainGeminiQuotaToGroq(t *testing.T) {
	f := &fakeFree{gemStatus: 429, gemBody: geminiMinute}
	c := freeClient(f.server(t), false)
	got, wait := switches(c)
	if ans, err := c.Text(context.Background(), "s", "p"); err != nil || ans != "groq says" {
		t.Fatalf("minute: %q %v", ans, err)
	}
	// the first answer of all is a start, but Gemini rests only a minute: not durable
	wait(1)
	if g := got(); len(g) != 0 {
		t.Fatalf("minute limit told: %v", g)
	}
	c.SetQuotaUntil("gemini", time.Time{})
	f.gemBody = geminiDaily
	if ans, err := c.Text(context.Background(), "s", "p"); err != nil || ans != "groq says" {
		t.Fatalf("daily: %q %v", ans, err)
	}
	wait(1)
	if g := got(); len(g) != 1 || g[0] != ">groq" {
		t.Fatalf("switches: %v", g)
	}
	n := f.gem.Load()
	_, _ = c.Text(context.Background(), "s", "p")
	if f.gem.Load() != n {
		t.Fatal("Gemini called during its daily hold")
	}
	f.mu.Lock()
	auth := f.groqAuth[0]
	f.mu.Unlock()
	if auth != "Bearer gsk" {
		t.Fatalf("groq auth: %q", auth)
	}
	if st := c.state("gemini"); st.State != "quota" || st.Until == "" {
		t.Fatalf("gemini state: %+v", st)
	}
}

// Budgets keep the platform inside the free limits: Groq's spent, OpenRouter answers.
func TestR42Budgets(t *testing.T) {
	t.Setenv("AI_BUDGET_GROQ", "2")
	t.Setenv("AI_BUDGET_OPENROUTER", "1")
	f := &fakeFree{}
	c := freeClient(f.server(t), false)
	c.Gemini = ""
	for i := 0; i < 2; i++ {
		if ans, _ := c.Text(context.Background(), "s", "p"); ans != "groq says" {
			t.Fatalf("groq %d: %q", i, ans)
		}
	}
	if !c.BudgetAllows(0) || c.BudgetAllows(100) {
		t.Fatal("budget allows")
	}
	ans, err := c.Text(context.Background(), "s", "p")
	if err != nil || ans != "openrouter says" || f.groq.Load() != 2 {
		t.Fatalf("openrouter: %q %v groq=%d", ans, err, f.groq.Load())
	}
	f.mu.Lock()
	if f.orAuth[0] != "Bearer ork" || f.orTitle[0] == "" {
		t.Fatalf("openrouter headers: %v %v", f.orAuth, f.orTitle)
	}
	f.mu.Unlock()
	if st := c.state("groq"); st.State != "budget" || st.Used != 2 || st.Budget != 2 {
		t.Fatalf("groq state: %+v", st)
	}
	// everything spent: one clean sentence, no call
	_, err = c.Text(context.Background(), "s", "p")
	if !IsQuota(err) || UserMessage(err) != AllPausedMessage || !c.Paused() || c.BudgetAllows(0) {
		t.Fatalf("all spent: %v paused=%v", err, c.Paused())
	}
	if f.groq.Load() != 2 || f.or.Load() != 1 {
		t.Fatal("spent providers called")
	}
	if c.Status()["message"] != AllPausedMessage {
		t.Fatalf("status message: %v", c.Status()["message"])
	}
	// the next day the budget is back
	quotaNow = func() time.Time { return time.Now().Add(25 * time.Hour) }
	defer func() { quotaNow = time.Now }()
	if ans, _ := c.Text(context.Background(), "s", "p"); ans != "groq says" {
		t.Fatalf("next day: %q", ans)
	}
}

// The smallest adequate model: a short JSON task on the light model, Heavy on the main one.
func TestR42ModelPerTask(t *testing.T) {
	f := &fakeFree{}
	c := freeClient(f.server(t), false)
	if _, err := c.JSON(context.Background(), "s", "short"); err != nil {
		t.Fatal(err)
	}
	if _, err := c.JSON(Heavy(context.Background()), "s", "deep"); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Text(context.Background(), "s", strings.Repeat("долгий текст ", 1000)); err != nil {
		t.Fatal(err)
	}
	f.mu.Lock()
	ms := strings.Join(f.gemModels, ",")
	f.mu.Unlock()
	if ms != "gemini-lite,gemini-main,gemini-main" {
		t.Fatalf("gemini models: %s", ms)
	}
	// the light model refused (its own daily quota): the main model answers
	f2 := &fakeFree{}
	srv := f2.server(t)
	c2 := freeClient(srv, false)
	c2.SetQuotaUntil("gemini-lite", time.Now().Add(time.Hour))
	if _, err := c2.JSON(context.Background(), "s", "short"); err != nil {
		t.Fatal(err)
	}
	f2.mu.Lock()
	if strings.Join(f2.gemModels, ",") != "gemini-main" {
		t.Fatalf("lite closed: %v", f2.gemModels)
	}
	f2.mu.Unlock()
	// Groq: the light model and JSON mode
	c.Gemini = ""
	if _, err := c.JSON(context.Background(), "s", "short"); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Text(Heavy(context.Background()), "s", "deep"); err != nil {
		t.Fatal(err)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if strings.Join(f.groqModels, ",") != "small,big" || !f.groqJSONMode[0] || f.groqJSONMode[1] {
		t.Fatalf("groq: %v %v", f.groqModels, f.groqJSONMode)
	}
}

// Web search: Claude out of balance → Gemini with Google Search grounding,
// within its own budget; then the search is unavailable (callers degrade).
func TestR42SearchFallbackAndBudget(t *testing.T) {
	t.Setenv("AI_BUDGET_GEMINI_SEARCH", "1")
	f := &fakeFree{claudeMode: "billing"}
	c := freeClient(f.server(t), true)
	SearchBackoff = []time.Duration{time.Millisecond}
	ans, err := c.Search(context.Background(), "events")
	if err != nil || !strings.Contains(ans, "gemini says") || f.gemSearch.Load() != 1 {
		t.Fatalf("grounded: %q %v %d", ans, err, f.gemSearch.Load())
	}
	if si := c.LastSearch(); !si.OK || si.Tool != "google_search" {
		t.Fatalf("last search: %+v", si)
	}
	_, err = c.Search(context.Background(), "events")
	if !SearchUnavailable(err) || f.gemSearch.Load() != 1 {
		t.Fatalf("budget: %v %d", err, f.gemSearch.Load())
	}
	if _, err := c.FindEvents(context.Background(), 21, time.Now()); !SearchUnavailable(err) {
		t.Fatalf("events: %v", err)
	}
	// text still works (Gemini's text budget is separate)
	if _, err := c.Text(context.Background(), "s", "p"); err != nil {
		t.Fatal(err)
	}
	// only Groq: no model can search, text works
	g := freeClient(f.server(t), false)
	g.Gemini = ""
	if _, err := g.Search(context.Background(), "x"); !errors.Is(err, ErrNoSearch) || !SearchUnavailable(err) {
		t.Fatalf("groq only: %v", err)
	}
	if len(g.SearchModels()) != 0 || !g.HasText() {
		t.Fatal("search models")
	}
	if _, err := g.SearchPing(context.Background()); !SearchUnavailable(err) {
		t.Fatalf("ping: %v", err)
	}
}

// A broken free key says whose key; OpenRouter's daily 429 holds it until UTC midnight.
func TestR42ProviderErrors(t *testing.T) {
	f := &fakeFree{groqStatus: 401, groqBody: `{"error":{"message":"Invalid API Key","type":"invalid_request_error","code":"invalid_api_key"}}`, orStatus: 429}
	c := freeClient(f.server(t), false)
	c.Gemini = ""
	_, err := c.Text(context.Background(), "s", "p")
	if err == nil || !strings.Contains(UserMessage(err), "GROQ_API_KEY") {
		t.Fatalf("groq key: %v / %s", err, UserMessage(err))
	}
	st := c.state("openrouter")
	if st.State != "quota" {
		t.Fatalf("openrouter: %+v", st)
	}
	u, _ := time.Parse(time.RFC3339, st.Until)
	if time.Until(u) < time.Minute || time.Until(u) > 25*time.Hour {
		t.Fatalf("until: %s", st.Until)
	}
	// Groq's per-minute 429 with retry-after: held 30 s, not a day
	f.groqStatus, f.groqBody = 429, `{"error":{"message":"Rate limit reached for model on requests per minute (RPM): Limit 30, Used 30","type":"requests","code":"rate_limit_exceeded"}}`
	_, _ = c.Text(context.Background(), "s", "p")
	q := c.quotaClosed("groq")
	if q == nil || q.Daily || time.Until(q.Until) > time.Minute {
		t.Fatalf("groq minute: %+v", q)
	}
}

// The provider lines: every provider, «нет ключа» for the missing ones, one active.
func TestR42ProviderStates(t *testing.T) {
	t.Setenv("GEMINI_ENABLED", "")
	f := &fakeFree{}
	c := freeClient(f.server(t), false)
	c.OpenRouter = ""
	ps := c.ProviderStates()
	names := []string{}
	active := 0
	for _, p := range ps {
		names = append(names, p.Name+":"+p.State)
		if p.Active {
			active++
		}
	}
	if strings.Join(names, ",") != "gemini:ok,groq:ok,claude:none,openrouter:none" || active != 1 || !ps[0].Active {
		t.Fatalf("states: %v", names)
	}
	if m, err := c.PingProvider(context.Background(), "groq"); err != nil || m != "small" {
		t.Fatalf("ping groq: %s %v", m, err)
	}
	if m, err := c.PingProvider(context.Background(), "gemini"); err != nil || m != "gemini-lite" {
		t.Fatalf("ping gemini: %s %v", m, err)
	}
	n := f.groq.Load()
	_, _ = c.PingProvider(context.Background(), "groq")
	if f.groq.Load() != n {
		t.Fatal("ping not kept")
	}
}
