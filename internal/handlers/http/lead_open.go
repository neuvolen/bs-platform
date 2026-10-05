package http

import (
	"context"
	"time"

	"github.com/bnursik/business_surgery_backend/internal/content"
)

// R40b: «открыл гайд» считается, когда лид открыл гайд в приложении, а не
// только когда отметил пункт: раньше чтение без галочек выглядело в CRM как
// «не изучает». Пишется один раз на гайд, в фоне, ответ приложению не ждёт.

func (g *AppGateway) noteGuideOpen(u *platformTgUser, id string) {
	if g.Funnel == nil || u == nil || u.ID == 0 {
		return
	}
	if _, admin := g.Admins[u.ID]; admin {
		return
	}
	title := content.GuideTitle(id)
	if title == "" {
		return
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		if g.Boards != nil {
			if n, _, err := g.Boards.ResidentByTg(ctx, u.ID); err != nil || n != "" {
				return
			}
		}
		g.Funnel.GuideOpened(ctx, u.ID, id, title)
	}()
}

// GuideOpened notes the first open of a guide in the lead's card.
func (f *LeadFunnel) GuideOpened(ctx context.Context, tg int64, id, title string) {
	now := f.now()
	_ = f.mutate(ctx, "bs_crm", func(crm map[string]any) bool {
		lead := findLeadByTg(asList(crm["leads"]), tg)
		if lead == nil {
			return false
		}
		ck, _ := lead["ck"].(map[string]any)
		if ck == nil {
			ck = map[string]any{}
		}
		started, _ := ck["started"].([]any)
		for _, s := range started {
			if s == id {
				return false
			}
		}
		ck["started"] = append(started, id)
		ck["last"], ck["at"] = title, now.UTC().Format(time.RFC3339)
		lead["ck"] = ck
		addLog(lead, now, "Открыл гайд «"+title+"»")
		return true
	})
}
