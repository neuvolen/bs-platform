package ai

import "time"

// R56: the package's tests do not wait out real pauses: a 503 is asked
// again at once and a per-minute 429 is not waited out (tests that check
// these set their own values).
func init() {
	Gemini503Backoff = []time.Duration{time.Millisecond, time.Millisecond}
	GeminiMinuteRetryMax = 0
}
