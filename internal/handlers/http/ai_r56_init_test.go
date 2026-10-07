package http

import (
	"time"

	"github.com/bnursik/business_surgery_backend/internal/ai"
)

// R56: the fake model servers answer 503 on purpose: ask again at once.
func init() {
	ai.Gemini503Backoff = []time.Duration{time.Millisecond, time.Millisecond}
	ai.GeminiMinuteRetryMax = 0
}
