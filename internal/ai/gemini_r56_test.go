package ai

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// Google's 429 bodies (R56).
const (
	r56Minute = `{"error":{"code":429,"message":"You exceeded your current quota, please check your plan and billing details.","status":"RESOURCE_EXHAUSTED","details":[` +
		`{"@type":"type.googleapis.com/google.rpc.QuotaFailure","violations":[{"quotaMetric":"generativelanguage.googleapis.com/generate_content_free_tier_requests","quotaId":"GenerateRequestsPerMinutePerProjectPerModel-FreeTier","quotaDimensions":{"location":"global","model":"gemini-3.8-flash"},"quotaValue":"10"}]},` +
		`{"@type":"type.googleapis.com/google.rpc.RetryInfo","retryDelay":"1s"}]}}`
	r56Day = `{"error":{"code":429,"message":"You exceeded your current quota, please check your plan and billing details.","status":"RESOURCE_EXHAUSTED","details":[` +
		`{"@type":"type.googleapis.com/google.rpc.QuotaFailure","violations":[{"quotaId":"GenerateRequestsPerDayPerProjectPerModel-FreeTier","quotaDimensions":{"model":"gemini-3.8-flash"},"quotaValue":"250"}]},` +
		`{"@type":"type.googleapis.com/google.rpc.RetryInfo","retryDelay":"37s"}]}}`
	r56Zero = `{"error":{"code":429,"message":"You exceeded your current quota, please check your plan and billing details.","status":"RESOURCE_EXHAUSTED","details":[` +
		`{"@type":"type.googleapis.com/google.rpc.QuotaFailure","violations":[{"quotaId":"GenerateRequestsPerMinutePerProjectPerModel-FreeTier","quotaDimensions":{"model":"gemini-3.8-flash"},"quotaValue":"0"}]},` +
		`{"@type":"type.googleapis.com/google.rpc.RetryInfo","retryDelay":"20s"}]}}`
	r56Bare   = `{"error":{"code":429,"message":"You exceeded your current quota, please check your plan and billing details. For more information on this error, head to: https://ai.google.dev/gemini-api/docs/rate-limits.","status":"RESOURCE_EXHAUSTED"}}`
	r56Demand = `{"error":{"code":503,"message":"This model is currently experiencing high demand. Spikes in demand are usually temporary. Please try again later.","status":"UNAVAILABLE"}}`
	r56OK     = `{"candidates":[{"content":{"parts":[{"text":"ок"}]}}]}`
)

func TestR56ParseQuotaKinds(t *testing.T) {
	cases := []struct {
		body, kind, id, val string
		retry               time.Duration
	}{
		{r56Minute, "per-minute", "GenerateRequestsPerMinutePerProjectPerModel-FreeTier", "10", time.Second},
		{r56Day, "per-day", "GenerateRequestsPerDayPerProjectPerModel-FreeTier", "250", 37 * time.Second},
		{r56Zero, "no free tier", "GenerateRequestsPerMinutePerProjectPerModel-FreeTier", "0", 20 * time.Second},
		{r56Bare, "billing", "", "", 0},
	}
	for _, tc := range cases {
		q, ok := ParseQuota(&HTTPError{Status: 429, Body: tc.body})
		if !ok || q.Kind() != tc.kind || q.QuotaID != tc.id || q.QuotaValue != tc.val || q.Retry != tc.retry {
			t.Fatalf("%s: %+v ok=%v", tc.kind, q, ok)
		}
	}
	if q, _ := ParseQuota(&HTTPError{Status: 429, Body: r56Minute}); q.Model != "gemini-3.8-flash" || q.Billing || q.Daily {
		t.Fatalf("minute: %+v", q)
	}
}

type r56Fake struct {
	mu    sync.Mutex
	calls map[string]int
	reply func(model string, n int) (int, string)
}

func (f *r56Fake) server(t *testing.T) *httptest.Server {
	f.calls = map[string]int{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		model := strings.TrimPrefix(r.URL.Path, "/v1beta/models/")
		model = model[:strings.Index(model, ":")]
		f.mu.Lock()
		f.calls[model]++
		n := f.calls[model]
		f.mu.Unlock()
		code, body := f.reply(model, n)
		w.WriteHeader(code)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return srv
}

func (f *r56Fake) n(model string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls[model]
}

func r56Client(srv *httptest.Server, lite string) *Client {
	return &Client{Gemini: "AQ.test", GeminiModel: "gemini-3.8-flash", GeminiLightModel: lite, GeminiBase: srv.URL, HTTP: srv.Client(), TextOrder: []string{"gemini"}}
}

// A per-minute limit pauses only the model, for Google's retryDelay; with no
// light model the call waits it out and asks once more.
func TestR56MinuteWaitsAndRetriesOnce(t *testing.T) {
	GeminiMinuteRetryMax = 5 * time.Second
	defer func() { GeminiMinuteRetryMax = 0 }()
	f := &r56Fake{reply: func(m string, n int) (int, string) {
		if n == 1 {
			return 429, r56Minute
		}
		return 200, r56OK
	}}
	c := r56Client(f.server(t), "")
	start := time.Now()
	ans, err := c.Text(context.Background(), "s", "p")
	if err != nil || ans != "ок" || f.n("gemini-3.8-flash") != 2 {
		t.Fatalf("ans %q err %v calls %d", ans, err, f.n("gemini-3.8-flash"))
	}
	if time.Since(start) < 900*time.Millisecond {
		t.Fatalf("did not wait the retryDelay: %s", time.Since(start))
	}
	if !c.QuotaUntil("gemini").IsZero() {
		t.Fatal("still paused after the answer")
	}
}

// A per-minute limit of the main model: the light model answers at once,
// the main model rests a minute at most, never 6 hours.
func TestR56MinuteLiteStandsIn(t *testing.T) {
	f := &r56Fake{reply: func(m string, n int) (int, string) {
		if m == "gemini-3.8-flash" {
			return 429, r56Minute
		}
		return 200, r56OK
	}}
	c := r56Client(f.server(t), "gemini-3.5-flash-lite")
	ans, err := c.Text(context.Background(), "s", "a long enough prompt that is not light "+strings.Repeat("x", 3000))
	if err != nil || ans != "ок" {
		t.Fatalf("%q %v", ans, err)
	}
	if u := c.QuotaUntil("gemini"); u.IsZero() || time.Until(u) > time.Minute+time.Second {
		t.Fatalf("main paused until %v", u)
	}
	if !c.QuotaUntil("gemini-lite").IsZero() {
		t.Fatal("lite paused")
	}
}

// A per-day limit: until the next Pacific midnight; a limit of 0 or a bare
// «check your plan and billing»: the long hold and the plain owner sentence.
func TestR56DayZeroBare(t *testing.T) {
	for _, tc := range []struct {
		body    string
		billing bool
	}{{r56Day, false}, {r56Zero, true}, {r56Bare, true}} {
		f := &r56Fake{reply: func(m string, n int) (int, string) { return 429, tc.body }}
		c := r56Client(f.server(t), "")
		_, err := c.geminiCall(context.Background(), "generateContent", map[string]any{})
		qe, ok := err.(*QuotaError)
		if !ok {
			t.Fatalf("want *QuotaError, got %T %v", err, err)
		}
		if tc.billing {
			hold := QuotaHold()
			if tc.body == r56Bare {
				hold = GeminiBareHold() // R66: a bare refusal names no limit: a short pause
			}
			if !qe.Billing || time.Until(qe.Until) < hold-time.Minute || time.Until(qe.Until) > hold+time.Minute || qe.Error() != GeminiNoFreeMessage {
				t.Fatalf("billing: %+v %s", qe, qe.Error())
			}
		} else {
			want := nextPacificMidnight(time.Now())
			if !qe.Daily || qe.Billing || qe.Until.Sub(want).Abs() > time.Second || qe.Error() != GeminiQuotaMessage {
				t.Fatalf("daily: %+v", qe)
			}
		}
		if f.n("gemini-3.8-flash") != 1 {
			t.Fatalf("asked %d times", f.n("gemini-3.8-flash"))
		}
	}
}

// 503 «high demand»: asked again twice, then the light model, no pause.
func TestR56HighDemandRetriesThenLite(t *testing.T) {
	f := &r56Fake{reply: func(m string, n int) (int, string) {
		if m == "gemini-3.8-flash" {
			return 503, r56Demand
		}
		return 200, r56OK
	}}
	c := r56Client(f.server(t), "gemini-3.5-flash-lite")
	b, err := c.geminiCall(context.Background(), "generateContent", map[string]any{})
	if err != nil || !strings.Contains(string(b), "ок") {
		t.Fatalf("%s %v", b, err)
	}
	if f.n("gemini-3.8-flash") != 3 || f.n("gemini-3.5-flash-lite") != 1 {
		t.Fatalf("calls %v", f.calls)
	}
	if !c.QuotaUntil("gemini").IsZero() || c.Paused() {
		t.Fatal("a 503 paused Gemini")
	}
	// a passing spike: the second try answers
	f2 := &r56Fake{reply: func(m string, n int) (int, string) {
		if n == 1 {
			return 503, r56Demand
		}
		return 200, r56OK
	}}
	c2 := r56Client(f2.server(t), "")
	if _, err := c2.geminiCall(context.Background(), "generateContent", map[string]any{}); err != nil || f2.n("gemini-3.8-flash") != 2 {
		t.Fatalf("%v calls %v", err, f2.calls)
	}
}

// The throttle: never more than the concurrency in flight, and the bucket
// spreads a burst over time.
func TestR56Throttle(t *testing.T) {
	gemLimOnce.Do(func() {})
	old := gemLim
	gemLim = &gemLimiter{buckets: map[string]*gemBucket{}, sem: make(chan struct{}, 2), burst: 2,
		rpm: func(string) float64 { return 600 }} // 10 a second
	gemThrottleAlways = true
	defer func() { gemLim, gemThrottleAlways = old, false }()
	var inFlight, peak atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := inFlight.Add(1)
		for {
			p := peak.Load()
			if n <= p || peak.CompareAndSwap(p, n) {
				break
			}
		}
		time.Sleep(30 * time.Millisecond)
		inFlight.Add(-1)
		_, _ = w.Write([]byte(r56OK))
	}))
	defer srv.Close()
	c := r56Client(srv, "")
	start := time.Now()
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := c.geminiCall(context.Background(), "generateContent", map[string]any{}); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	if peak.Load() > 2 {
		t.Fatalf("in flight %d", peak.Load())
	}
	// 2 at once, then 6 more at 10 a second
	if d := time.Since(start); d < 500*time.Millisecond {
		t.Fatalf("burst not spread: %s", d)
	}
	// a cancelled wait gives up
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	gemLim.buckets["slow"] = &gemBucket{cap: 1, rate: 1.0 / 60, last: time.Now()}
	if _, err := gemLim.acquire(ctx, "slow"); err == nil {
		t.Fatal("waited past the deadline")
	}
}

// The start self-test: one line per model with what Google said.
func TestR56SelfTest(t *testing.T) {
	f := &r56Fake{reply: func(m string, n int) (int, string) {
		if m == "gemini-3.8-flash" {
			return 429, r56Zero
		}
		return 200, r56OK
	}}
	c := r56Client(f.server(t), "gemini-3.5-flash-lite")
	c.SetQuotaUntil("gemini-lite", time.Now().Add(6*time.Hour)) // a stale pause
	lines := c.GeminiSelfTest(context.Background())
	if len(lines) != 2 ||
		!strings.HasPrefix(lines[0], "gemini selftest gemini-3.8-flash: 429 GenerateRequestsPerMinutePerProjectPerModel-FreeTier 0 (no free tier") ||
		lines[1] != "gemini selftest gemini-3.5-flash-lite: ok" {
		t.Fatalf("lines: %q", lines)
	}
	if !c.QuotaUntil("gemini-lite").IsZero() {
		t.Fatal("an answering model stays paused")
	}
	if u := c.QuotaUntil("gemini"); time.Until(u) < QuotaHold()-time.Minute {
		t.Fatalf("limit 0 not held: %v", u)
	}
	// a bare 429 and a 503 are named too
	f2 := &r56Fake{reply: func(m string, n int) (int, string) {
		if m == "gemini-3.8-flash" {
			return 429, r56Bare
		}
		return 503, r56Demand
	}}
	c2 := r56Client(f2.server(t), "gemini-3.5-flash-lite")
	lines = c2.GeminiSelfTest(context.Background())
	if !strings.HasPrefix(lines[0], "gemini selftest gemini-3.8-flash: 429 no-quota-id ? (billing, details=none)") ||
		!strings.HasPrefix(lines[1], "gemini selftest gemini-3.5-flash-lite: 503 This model is currently experiencing high demand") {
		t.Fatalf("lines: %q", lines)
	}
}

// Prod 07.10.2026 08:03: the morning events search (google_search tool) got
// a bare 429 on both models a minute after both answered the self-test. Only
// the search closes; text tasks keep Gemini and the owner is not told.
func TestR56SearchQuotaClosesOnlySearch(t *testing.T) {
	SearchBackoff = []time.Duration{time.Millisecond}
	defer func() { SearchBackoff = []time.Duration{4 * time.Second, 12 * time.Second} }()
	var searches, texts atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		buf := new(strings.Builder)
		_, _ = io.Copy(buf, r.Body)
		if strings.Contains(buf.String(), "google_search") {
			searches.Add(1)
			w.WriteHeader(429)
			_, _ = w.Write([]byte(`{"error":{"code":429,"message":"You exceeded your current quota, please check your plan and billing details.","status":"RESOURCE_EXHAUSTED","details":[{"@type":"type.googleapis.com/google.rpc.Help","links":[{"description":"Learn more","url":"https://ai.google.dev/gemini-api/docs/rate-limits"}]}]}}`))
			return
		}
		texts.Add(1)
		_, _ = w.Write([]byte(r56OK))
	}))
	defer srv.Close()
	c := r56Client(srv, "gemini-3.5-flash-lite")
	var told atomic.Int32
	c.OnQuota = func(*QuotaError) { told.Add(1) }
	for i := 0; i < 2; i++ {
		_, err := c.Search(context.Background(), "events")
		if err == nil || UserMessage(err) != GeminiSearchQuotaMessage {
			t.Fatalf("search: %v", err)
		}
	}
	if searches.Load() != 1 {
		t.Fatalf("closed search asked %d times", searches.Load())
	}
	ans, err := c.Text(context.Background(), "s", "p")
	if err != nil || ans != "ок" || texts.Load() != 1 {
		t.Fatalf("text: %q %v", ans, err)
	}
	if !c.QuotaUntil("gemini").IsZero() || !c.QuotaUntil("gemini-lite").IsZero() || c.QuotaUntil("gemini-search").IsZero() || c.Paused() {
		t.Fatal("the search refusal paused text")
	}
	time.Sleep(20 * time.Millisecond)
	if told.Load() != 0 {
		t.Fatal("owner told about the search")
	}
}

// Prod 08:14: the main model held requests until the caller's deadline. A
// request is cut after AI_GEMINI_TIMEOUT and the light model answers.
func TestR56HangCutLiteAnswers(t *testing.T) {
	t.Setenv("AI_GEMINI_TIMEOUT", "0.2")
	f := &r56Fake{reply: func(m string, n int) (int, string) {
		if m == "gemini-3.8-flash" {
			time.Sleep(time.Second)
		}
		return 200, r56OK
	}}
	c := r56Client(f.server(t), "gemini-3.5-flash-lite")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	start := time.Now()
	b, err := c.geminiCall(ctx, "generateContent", map[string]any{})
	if err != nil || !strings.Contains(string(b), "ок") || time.Since(start) > 900*time.Millisecond {
		t.Fatalf("%s %v in %s", b, err, time.Since(start))
	}
	if f.n("gemini-3.8-flash") != 1 || c.Paused() {
		t.Fatalf("calls %v paused %v", f.calls, c.Paused())
	}
	GeminiSelfTestTimeout = 200 * time.Millisecond
	defer func() { GeminiSelfTestTimeout = 40 * time.Second }()
	lines := c.GeminiSelfTest(context.Background())
	if !strings.HasPrefix(lines[0], "gemini selftest gemini-3.8-flash: 504 ") || lines[1] != "gemini selftest gemini-3.5-flash-lite: ok" {
		t.Fatalf("lines %q", lines)
	}
}
