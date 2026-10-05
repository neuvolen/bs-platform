package app

import (
	"testing"
	"time"
)

func TestStandClockOnlyWithLocalTelegram(t *testing.T) {
	t.Setenv("OUTREACH_NOW", "2026-10-05T12:00:00+05:00")
	t.Setenv("TELEGRAM_API_BASE", "")
	if _, ok := standClock(); ok {
		t.Fatal("the real Telegram: the clock must stay real")
	}
	t.Setenv("TELEGRAM_API_BASE", "https://api.telegram.org")
	if _, ok := standClock(); ok {
		t.Fatal("api.telegram.org: the clock must stay real")
	}
	t.Setenv("TELEGRAM_API_BASE", "http://127.0.0.1:8382/tg")
	at, ok := standClock()
	if !ok || !at.Equal(time.Date(2026, 10, 5, 7, 0, 0, 0, time.UTC)) {
		t.Fatalf("stand: %v %v", at, ok)
	}
	t.Setenv("OUTREACH_NOW", "5 октября")
	if _, ok := standClock(); ok {
		t.Fatal("a bad time accepted")
	}
}
