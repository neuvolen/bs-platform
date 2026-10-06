package app

import (
	"context"
	"log"
	"net/url"
	"os"
	"time"

	"github.com/bnursik/business_surgery_backend/internal/bot"
	httpapi "github.com/bnursik/business_surgery_backend/internal/handlers/http"
	"github.com/bnursik/business_surgery_backend/internal/repository/pg"
)

// WireOutreach: R38c: the club's events with «Иду» in the bot, the
// broadcasts the owner launches from the platform, and WhatsApp for the
// residents who chose it (handlers/http/outreach_*.go).
func WireOutreach(d *Deps, pm *httpapi.PlatformModule, botSvc *bot.Service, team, jwtSecret string) httpapi.RoutesRegistrar {
	var admins []int64
	for id := range httpapi.ParsePlatformTeam(team) {
		admins = append(admins, id)
	}
	o := httpapi.NewOutreach(pg.NewOutreachRepo(d.DB), d.PlatformRepo, pg.NewClubRepo(d.DB), []byte(jwtSecret), admins, httpapi.FirstTeamID(team))
	if at, ok := standClock(); ok {
		boot := time.Now()
		o.SetClock(func() time.Time { return at.Add(time.Since(boot)) })
		log.Printf("outreach: stand clock from %s", at.Format(time.RFC3339))
	}
	if botSvc != nil && botSvc.Enabled() {
		o.Send, o.Contact = botSvc.SendMessageKB, botSvc.RequestContact
		botSvc.SetPublicCallbackHook("ev:", o.EventButton)
		botSvc.SetContactHook(o.EventContact)
		botSvc.SetWhatsApp(o.Route, o.DeliverWA)
	}
	if pm != nil && pm.AI != nil {
		pm.AI.WAResident = o.HandleResidentWA // the call summary
	}
	outreachRef = o // R51: sales_wiring.go (WhatsApp for leads without Telegram)
	if d.PlatformRepo != nil {
		o.Start(context.Background())
	}
	return &httpapi.OutreachModule{O: o, Secret: []byte(jwtSecret)}
}

// standClock: OUTREACH_NOW (RFC3339) starts the events' clock at a fixed
// moment, so a test stand does not depend on today's date. Honoured only
// with a local Telegram (TELEGRAM_API_BASE on localhost): never in production.
func standClock() (time.Time, bool) {
	v := os.Getenv("OUTREACH_NOW")
	if v == "" {
		return time.Time{}, false
	}
	u, err := url.Parse(os.Getenv("TELEGRAM_API_BASE"))
	if err != nil || (u.Hostname() != "localhost" && u.Hostname() != "127.0.0.1") {
		log.Printf("outreach: OUTREACH_NOW ignored: Telegram is not local")
		return time.Time{}, false
	}
	at, err := time.Parse(time.RFC3339, v)
	if err != nil {
		log.Printf("outreach: OUTREACH_NOW: %v", err)
		return time.Time{}, false
	}
	return at, true
}
