package app

import (
	"context"

	"github.com/bnursik/business_surgery_backend/internal/ai"
	"github.com/bnursik/business_surgery_backend/internal/bot"
	httpapi "github.com/bnursik/business_surgery_backend/internal/handlers/http"
	pg "github.com/bnursik/business_surgery_backend/internal/repository/pg"
)

// wireSheetOwner (R32d): the server owns the club's data; the sheet gets an
// optional read-only copy and is compared once at the deploy.
func wireSheetOwner(d *Deps, g *httpapi.AppGateway, writes *httpapi.ClubWrites, clubRepo *pg.ClubRepo,
	meta httpapi.MetaStore, cut *httpapi.SheetCutover, token, jwtSecret string, botSvc *bot.Service) httpapi.RoutesRegistrar {
	owner := httpapi.NewSheetOwner(clubRepo, meta, cut, token)
	owner.Install()
	if d.Club != nil {
		d.Club.Owner = owner
	}
	owner.OnChange = func() {
		g.Reset()
		if writes.Tables != nil {
			writes.Tables()
		}
	}
	go owner.Loop(context.Background())
	// The app's last script-only actions answered by the server (app_ported.go).
	p := &httpapi.AppPorted{Meta: meta, Channel: "@bsurgery_kz"}
	for id := range g.Admins {
		p.Team = append(p.Team, id)
	}
	if botSvc != nil && botSvc.Enabled() {
		p.TG = botSvc.API
	}
	if c := ai.FromEnv(); c != nil {
		p.AI = c
	}
	g.Ported = p
	return httpapi.NewSheetOwnerModule(owner, []byte(jwtSecret))
}
