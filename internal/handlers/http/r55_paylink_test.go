package http

import (
	"strings"
	"testing"

	"github.com/bnursik/business_surgery_backend/internal/bot"
)

// R55: the breakfast (10 000 ₸) had no payment link: the owner's Kaspi link
// goes in once, the invitation names the sum to type (the link opens Kaspi
// without one), and a link the team cleared afterwards stays cleared.
func TestR55BreakfastPayLink(t *testing.T) {
	o, _, repo, ctx, _ := r38cSetup(t, nil)
	if _, err := repo.PutDoc(ctx, "server", payLinkSeedDoc, 0, `{}`, true, "test"); err != nil {
		if d, _ := repo.GetDoc(ctx, "server", payLinkSeedDoc); d != nil {
			_, _ = repo.PutDoc(ctx, "server", payLinkSeedDoc, d.Version, `{}`, false, "test")
		}
	}
	o.seedEvents(ctx)
	if n := o.SeedEventPayLinks(ctx); n != 1 {
		t.Fatalf("seeded %d", n)
	}
	e, err := o.repo.EventGet(ctx, BreakfastID)
	if err != nil || e == nil || e.PayLink != bot.KaspiLink {
		t.Fatalf("event %+v %v", e, err)
	}
	if p := evPay(*e); !strings.Contains(p, "10 000 ₸") || !strings.Contains(p, "введите сумму 10 000 ₸") || !strings.Contains(p, bot.KaspiLink) {
		t.Fatal(p)
	}
	e.PayLink = ""
	if err := o.repo.EventPut(ctx, *e); err != nil {
		t.Fatal(err)
	}
	if n := o.SeedEventPayLinks(ctx); n != 0 {
		t.Fatal("a cleared link came back")
	}
}
