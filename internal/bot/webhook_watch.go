package bot

import (
	"context"
	"encoding/json"
	"log"
	"time"

	"github.com/bnursik/business_surgery_backend/internal/club"
)

// R32d: the Go bot owns the Telegram webhook. It is set at start and checked
// every WebhookWatchEvery: if anything (an old copy of the sheet's script,
// its menu «Переподключить бота напрямую») pointed Telegram elsewhere, the
// server takes the webhook back, keeping the updates waiting in Telegram's
// queue. Not in legacy mode: there the rollback may point it at the script.
var WebhookWatchEvery = 15 * time.Minute

func (s *Service) watchWebhook(ctx context.Context) {
	s.ensureWebhook(ctx)
	t := time.NewTicker(WebhookWatchEvery)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		if club.SheetLegacy() {
			continue
		}
		s.ensureWebhook(ctx)
	}
}

// WebhookOwned: Telegram calls the server (for the platform's status).
func (s *Service) WebhookOwned(ctx context.Context) (bool, string) {
	if s.publicURL == "" || !s.Enabled() {
		return false, ""
	}
	c, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	info, err := s.GetWebhookInfo(c)
	if err != nil {
		log.Printf("bot: webhook info: %v", err)
		return false, ""
	}
	return info.URL == s.publicURL+"/api/v1/bot/webhook", info.URL
}

// API calls one Bot API method with the server's bot (the app's actions the
// script used to make, app_ported.go).
func (s *Service) API(ctx context.Context, method string, params map[string]any) (json.RawMessage, error) {
	return s.call(ctx, method, params)
}
