package ai

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"
)

// R56, prod 07.10.2026: a new Gemini key, and at once «You exceeded your
// current quota» on both models while a moment later Gemini answered 503
// «high demand» (so calls did get through). The free tier counts requests
// per minute per model; the platform's start (AI recs, Gallup's three
// parallel calls, events, trends) sent a burst. Now:
//
//   - every generateContent goes through one process-wide throttle: a token
//     bucket per model (AI_GEMINI_RPM, 8 a minute for the main model;
//     AI_GEMINI_LITE_RPM, 12 for the light one; AI_GEMINI_BURST, 2 at once)
//     and at most AI_GEMINI_CONCURRENCY (2) requests in flight. Jobs that
//     start together wait their turn instead of bursting;
//   - a 503 «high demand» is asked again twice with a pause, then the light
//     model answers; it never pauses Gemini;
//   - a per-minute 429 pauses only that model for Google's retryDelay (a
//     minute without it); the call is retried once after it when the light
//     model cannot stand in;
//   - ~60 s after the start one tiny request to each model logs what Google
//     says («gemini selftest <model>: ok / 429 <quotaId> <value> …»).

// Gemini503Backoff: the pauses before asking again after a 503 (two tries).
var Gemini503Backoff = []time.Duration{2 * time.Second, 5 * time.Second}

// GeminiMinuteRetryMax: the longest retryDelay a call waits out itself.
var GeminiMinuteRetryMax = 65 * time.Second

// gemThrottleAlways: tests throttle their fake server too.
var gemThrottleAlways bool

type gemBucket struct {
	tokens, cap, rate float64 // rate: tokens a second
	last              time.Time
}

type gemLimiter struct {
	mu      sync.Mutex
	buckets map[string]*gemBucket
	sem     chan struct{}
	rpm     func(model string) float64
	burst   float64
}

func envNum(k string, def float64) float64 {
	if v, err := strconv.ParseFloat(strings.TrimSpace(os.Getenv(k)), 64); err == nil && v > 0 {
		return v
	}
	return def
}

func newGemLimiter() *gemLimiter {
	n := int(envNum("AI_GEMINI_CONCURRENCY", 2))
	main, lite := envNum("AI_GEMINI_RPM", 8), envNum("AI_GEMINI_LITE_RPM", 12)
	return &gemLimiter{
		buckets: map[string]*gemBucket{},
		sem:     make(chan struct{}, n),
		burst:   envNum("AI_GEMINI_BURST", 2),
		rpm: func(model string) float64 {
			if strings.Contains(strings.ToLower(model), "lite") {
				return lite
			}
			return main
		},
	}
}

var (
	gemLimOnce sync.Once
	gemLim     *gemLimiter
)

func gemLimit() *gemLimiter {
	gemLimOnce.Do(func() { gemLim = newGemLimiter() })
	return gemLim
}

// acquire waits for the model's token and a free slot; release frees the slot.
func (l *gemLimiter) acquire(ctx context.Context, model string) (release func(), err error) {
	for {
		l.mu.Lock()
		b := l.buckets[model]
		now := time.Now()
		if b == nil {
			b = &gemBucket{cap: l.burst, tokens: l.burst, rate: l.rpm(model) / 60, last: now}
			l.buckets[model] = b
		}
		b.tokens += now.Sub(b.last).Seconds() * b.rate
		if b.tokens > b.cap {
			b.tokens = b.cap
		}
		b.last = now
		if b.tokens >= 1 {
			b.tokens--
			l.mu.Unlock()
			break
		}
		wait := time.Duration((1 - b.tokens) / b.rate * float64(time.Second))
		l.mu.Unlock()
		t := time.NewTimer(wait)
		select {
		case <-ctx.Done():
			t.Stop()
			return nil, ctx.Err()
		case <-t.C:
		}
	}
	select {
	case l.sem <- struct{}{}:
		return func() { <-l.sem }, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// throttled: real Google traffic (AI_GEMINI_THROTTLE=0 turns it off).
func (c *Client) throttled() bool {
	if os.Getenv("AI_GEMINI_THROTTLE") == "0" {
		return false
	}
	return gemThrottleAlways || strings.Contains(c.GeminiBase, "googleapis.com")
}

// overloaded: Google's «model is overloaded / high demand» (5xx, not a quota).
func overloaded(err error) bool {
	var he *HTTPError
	if !errors.As(err, &he) {
		return false
	}
	return he.Status == 503 || he.Status == 500 || he.Status == 502 || he.Status == 504
}

// GeminiCallTimeout: how long one Gemini request may hang (AI_GEMINI_TIMEOUT
// seconds, 100 by default). Prod 08:14 07.10.2026: under «high demand» the
// main model held requests until the caller's deadline, leaving no time for
// the light model; a request cut here counts as overloaded (504) and the
// light model answers.
func GeminiCallTimeout() time.Duration {
	return time.Duration(envNum("AI_GEMINI_TIMEOUT", 100) * float64(time.Second))
}

// gemPost: one generateContent of a model through the throttle; a 503 is
// asked again (Gemini503Backoff), never paused.
func (c *Client) gemPost(ctx context.Context, model, method string, body any) ([]byte, error) {
	return c.gemPostTimeout(ctx, model, method, body, GeminiCallTimeout())
}

func (c *Client) gemPostTimeout(ctx context.Context, model, method string, body any, limit time.Duration) ([]byte, error) {
	url := fmt.Sprintf("%s/v1beta/models/%s:%s", c.GeminiBase, model, method)
	for try := 0; ; try++ {
		var release func()
		if c.throttled() {
			r, err := gemLimit().acquire(ctx, model)
			if err != nil {
				return nil, err
			}
			release = r
		}
		actx, cancel := ctx, context.CancelFunc(func() {})
		if limit > 0 {
			actx, cancel = context.WithTimeout(ctx, limit)
		}
		b, err := c.do(actx, c.gemAuth(jsonReq("POST", url, body)))
		cut := err != nil && actx.Err() != nil && ctx.Err() == nil
		cancel()
		if release != nil {
			release()
		}
		if cut {
			log.Printf("ai: gemini %s did not answer in %s (cut, counted as overloaded)", model, limit)
			return nil, &HTTPError{Status: 504, Body: fmt.Sprintf(`{"error":{"code":504,"message":"Gemini %s не ответил за %s","status":"DEADLINE_EXCEEDED"}}`, model, limit)}
		}
		if err == nil || !overloaded(err) || try >= len(Gemini503Backoff) || ctx.Err() != nil {
			return b, err
		}
		var he *HTTPError
		errors.As(err, &he)
		log.Printf("ai: gemini %s %d (%s), asking again in %s", model, he.Status, apiMessage(he.Body), Gemini503Backoff[try])
		t := time.NewTimer(Gemini503Backoff[try])
		select {
		case <-ctx.Done():
			t.Stop()
			return nil, err
		case <-t.C:
		}
	}
}

// waitMinute: a per-minute 429 that the call may wait out itself (the
// deadline allows); false: give up now.
func waitMinute(ctx context.Context, q *QuotaError) bool {
	if q == nil || q.Daily || q.Billing {
		return false
	}
	d := time.Until(q.Until)
	if d <= 0 {
		d = time.Second
	}
	if d > GeminiMinuteRetryMax {
		return false
	}
	if dl, ok := ctx.Deadline(); ok && time.Until(dl) < d+10*time.Second {
		return false
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	}
}

// GeminiSelfTestTimeout: one self-test request at most.
var GeminiSelfTestTimeout = 40 * time.Second

// GeminiSelfTestDelay: how long after the start the self-test runs.
var GeminiSelfTestDelay = 60 * time.Second

// StartGeminiSelfTest runs GeminiSelfTest once, GeminiSelfTestDelay after
// the start (AI_GEMINI_SELFTEST=0 turns it off).
func (c *Client) StartGeminiSelfTest() {
	if c == nil || c.Gemini == "" || os.Getenv("AI_GEMINI_SELFTEST") == "0" {
		return
	}
	go func() {
		time.Sleep(GeminiSelfTestDelay)
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
		defer cancel()
		c.GeminiSelfTest(ctx)
	}()
}

// GeminiSelfTest asks each Gemini model one tiny question (through the
// throttle, past any pause) and logs what Google says; a model that answers
// is opened again, a refusal is classified as any other. It returns the
// lines it logged.
func (c *Client) GeminiSelfTest(ctx context.Context) []string {
	if c == nil || c.Gemini == "" {
		return nil
	}
	models := []string{c.geminiModel()}
	if lite := strings.TrimSpace(c.GeminiLightModel); lite != "" && lite != models[0] {
		models = append(models, lite)
	}
	body := map[string]any{
		"contents":         []map[string]any{{"role": "user", "parts": []map[string]any{{"text": "Ответь одним словом: ок"}}}},
		"generationConfig": map[string]any{"maxOutputTokens": 8},
	}
	var out []string
	for i, m := range models {
		svc := "gemini"
		if i > 0 {
			svc = "gemini-lite"
		}
		_, err := c.gemPostTimeout(ctx, m, "generateContent", body, GeminiSelfTestTimeout)
		line := "gemini selftest " + m + ": "
		var he *HTTPError
		switch {
		case err == nil:
			line += "ok"
			c.SetQuotaUntil(svc, time.Time{})
		case errors.As(err, &he) && he.Status == 429:
			q, _ := ParseQuota(err)
			id, val := q.QuotaID, q.QuotaValue
			if id == "" {
				id = "no-quota-id"
			}
			if val == "" {
				val = "?"
			}
			retry := ""
			if q.Retry > 0 {
				retry = " retry=" + q.Retry.String()
			}
			line += fmt.Sprintf("429 %s %s (%s%s, details=%s): %s", id, val, q.Kind(), retry, detailTypes(he.Body), apiMessage(he.Body))
			c.noteQuota(svc, err)
		case errors.As(err, &he):
			line += fmt.Sprintf("%d %s", he.Status, apiMessage(he.Body))
			c.noteBadKey("gemini", err)
		default:
			line += "error " + err.Error()
		}
		log.Print(line)
		out = append(out, line)
	}
	return out
}
