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

// Gemini's quota (R32d). A key on the free tier gets a few speech and text
// requests a day per model; past that every call answers 429
// RESOURCE_EXHAUSTED «You exceeded your current quota». Asking again only
// burns the next calls, so the client remembers the refusal per service
// ("tts" for speech synthesis, "gemini" for text, search and transcription)
// and stops calling until the quota is back:
//
//   - a daily quota (quotaId …PerDay…) comes back at midnight Pacific time,
//     when Google resets it;
//   - otherwise Google's RetryInfo.retryDelay says when (a minute if absent).
//
// While a service is closed its calls fail at once with *QuotaError, and the
// text tasks go on with the next model that has a key (Text, JSON, Search).

// R32c: three kinds of a 429 «You exceeded your current quota»:
//
//   - a per-minute limit (quotaId …PerMinute…): back in a minute or after
//     Google's retryDelay;
//   - a daily limit (…PerDay…): back at midnight Pacific time;
//   - the key's billing (no per-minute or per-day limit named: the prepaid
//     balance or the plan is used up): it does not come back by itself, so
//     Gemini is put on hold for AI_QUOTA_HOLD_HOURS (6 by default) and the
//     text tasks go straight to Claude when ANTHROPIC_API_KEY is set.
//
// Whatever the kind, the page and the bot get one clean sentence
// (QuotaMessage), never Google's JSON.

// QuotaMessage: what people see while Gemini's quota is used up.
const QuotaMessage = "Закончилась квота Gemini: пополните баланс в Google AI Studio или добавьте ключ Claude. Сервисы ИИ временно на паузе"

// QuotaError: the service's quota is used up until Until.
type QuotaError struct {
	Service string
	Until   time.Time
	Daily   bool
	Billing bool  // the key's balance or plan, not a time window
	Err     error // Google's answer, when this call got it
}

func (e *QuotaError) Error() string { return QuotaMessage }

// When: «до 05.10 13:01 по Алматы», for the ops line.
func (e *QuotaError) When() string {
	return fmt.Sprintf("до %s по Алматы", e.Until.In(almatyTZ).Format("02.01 15:04"))
}

// QuotaHold: how long Gemini rests after a billing refusal (AI_QUOTA_HOLD_HOURS).
func QuotaHold() time.Duration {
	if v, err := strconv.ParseFloat(strings.TrimSpace(os.Getenv("AI_QUOTA_HOLD_HOURS")), 64); err == nil && v > 0 {
		return time.Duration(v * float64(time.Hour))
	}
	return 6 * time.Hour
}

func (e *QuotaError) Unwrap() error { return e.Err }

var almatyTZ = time.FixedZone("Almaty", 5*3600)

// pacific: Google resets daily quotas at midnight Pacific time.
var pacific = func() *time.Location {
	if l, err := time.LoadLocation("America/Los_Angeles"); err == nil {
		return l
	}
	return time.FixedZone("PT", -7*3600)
}()

// quotaNow is replaced in tests.
var quotaNow = time.Now

type quotaState struct {
	mu      sync.Mutex
	until   map[string]time.Time
	daily   map[string]bool
	billing map[string]bool
}

// QuotaInfo: what Google said in a 429 answer.
type QuotaInfo struct {
	Daily   bool
	Billing bool          // no time window named: the balance or the plan (R32c)
	Retry   time.Duration // RetryInfo.retryDelay, 0 when absent
}

// ParseQuota reads a 429 answer of Gemini. ok is false for anything else
// (a 429 without a word of quota is a short rate limit).
func ParseQuota(err error) (QuotaInfo, bool) {
	var he *HTTPError
	if !errors.As(err, &he) || he.Status != 429 {
		return QuotaInfo{}, false
	}
	low := strings.ToLower(he.Body)
	q := QuotaInfo{}
	var body struct {
		Error struct {
			Status  string `json:"status"`
			Details []struct {
				Type       string `json:"@type"`
				RetryDelay string `json:"retryDelay"`
				Violations []struct {
					QuotaID string `json:"quotaId"`
				} `json:"violations"`
			} `json:"details"`
		} `json:"error"`
	}
	_ = json.Unmarshal([]byte(he.Body), &body)
	minute := false
	for _, d := range body.Error.Details {
		if d.RetryDelay != "" {
			if v, e := time.ParseDuration(d.RetryDelay); e == nil {
				q.Retry = v
			}
		}
		for _, v := range d.Violations {
			id := strings.ToLower(v.QuotaID)
			if strings.Contains(id, "perday") {
				q.Daily = true
			}
			if strings.Contains(id, "perminute") || strings.Contains(id, "persecond") {
				minute = true
			}
		}
	}
	if strings.Contains(low, "perday") || strings.Contains(low, "per day") || strings.Contains(low, "daily") {
		q.Daily = true
	}
	quota := strings.Contains(low, "quota") || strings.Contains(low, "resource_exhausted") || strings.Contains(low, "billing")
	if quota && !q.Daily && !minute && (strings.Contains(low, "billing") || strings.Contains(low, "exceeded your current quota") ||
		strings.Contains(low, "prepay") || strings.Contains(low, "credit")) {
		q.Billing = true
	}
	return q, quota
}

// nextPacificMidnight: when a daily quota comes back.
func nextPacificMidnight(now time.Time) time.Time {
	p := now.In(pacific)
	return time.Date(p.Year(), p.Month(), p.Day()+1, 0, 1, 0, 0, pacific)
}

// noteQuota closes the service when err is a quota refusal; it returns the
// *QuotaError to pass on (nil when err is something else).
func (c *Client) noteQuota(service string, err error) *QuotaError {
	q, ok := ParseQuota(err)
	if !ok {
		return nil
	}
	now := quotaNow()
	until := now.Add(time.Minute)
	switch {
	case q.Daily:
		until = nextPacificMidnight(now)
	case q.Billing:
		until = now.Add(QuotaHold())
	case q.Retry > 0:
		until = now.Add(q.Retry)
	}
	c.quota.mu.Lock()
	if c.quota.until == nil {
		c.quota.until, c.quota.daily = map[string]time.Time{}, map[string]bool{}
	}
	if c.quota.billing == nil {
		c.quota.billing = map[string]bool{}
	}
	wasOpen := !now.Before(c.quota.until[service])
	if until.After(c.quota.until[service]) {
		c.quota.until[service] = until
		c.quota.daily[service] = q.Daily
		c.quota.billing[service] = q.Billing
	}
	until, daily, billing := c.quota.until[service], c.quota.daily[service], c.quota.billing[service]
	c.quota.mu.Unlock()
	qe := &QuotaError{Service: service, Until: until, Daily: daily, Billing: billing, Err: err}
	// The owner hears about a long pause once (OnQuota dedupes by day); a
	// per-minute limit is not worth a message.
	if wasOpen && (daily || billing) && c.OnQuota != nil {
		go c.OnQuota(qe)
	}
	return qe
}

// quotaClosed: the service may not be called now.
func (c *Client) quotaClosed(service string) *QuotaError {
	c.quota.mu.Lock()
	defer c.quota.mu.Unlock()
	u := c.quota.until[service]
	if u.IsZero() || !quotaNow().Before(u) {
		return nil
	}
	return &QuotaError{Service: service, Until: u, Daily: c.quota.daily[service], Billing: c.quota.billing[service]}
}

// QuotaUntil: when the service's quota comes back (zero: it is open).
func (c *Client) QuotaUntil(service string) time.Time {
	if q := c.quotaClosed(service); q != nil {
		return q.Until
	}
	return time.Time{}
}

// SetQuotaUntil closes (or with a zero time opens) a service by hand: tests
// and the ops page.
func (c *Client) SetQuotaUntil(service string, until time.Time) {
	c.quota.mu.Lock()
	defer c.quota.mu.Unlock()
	if c.quota.until == nil {
		c.quota.until, c.quota.daily = map[string]time.Time{}, map[string]bool{}
	}
	c.quota.until[service] = until
}

// IsQuota: err says the quota is used up (closed service or Google's 429).
func IsQuota(err error) bool {
	var qe *QuotaError
	if errors.As(err, &qe) {
		return true
	}
	_, ok := ParseQuota(err)
	return ok
}

// fallbackWorthy: another model may answer where this one did not. Everything
// but a cancelled request: quota, overload, a broken key, a hang.
func fallbackWorthy(ctx context.Context, err error) bool {
	if err == nil || ctx.Err() != nil {
		return false
	}
	return !errors.Is(err, context.Canceled)
}
