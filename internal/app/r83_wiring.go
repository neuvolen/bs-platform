package app

import (
	"context"
	"os"
	"time"

	"github.com/bnursik/business_surgery_backend/internal/bot"
	httpapi "github.com/bnursik/business_surgery_backend/internal/handlers/http"
	"github.com/bnursik/business_surgery_backend/internal/repository/pg"
	"github.com/gin-gonic/gin"
)

type r83Routes struct{ cycle *httpapi.CyclePlanner }

func (r r83Routes) Register(e *gin.Engine) {
	if r.cycle != nil {
		r.cycle.Register(e)
	}
}

// WireR83: after BuildAppGateway. The bot's «Быстрые заметки» (forwarded
// messages of the team) and the next cycle's meetings 48 hours after an
// offline разбор (handlers/http/r83_*.go). The sales managers are wired in
// NewPlatformModule and WireCalls (their bot).
func WireR83(d *Deps, pm *httpapi.PlatformModule, botSvc *bot.Service, team, token, jwtSecret string) httpapi.RoutesRegistrar {
	teamMap := httpapi.ParsePlatformTeam(team)
	if pm != nil && pm.Inbox != nil {
		pm.Inbox.Names = func(tg int64) string { return teamMap[tg] }
		if botSvc != nil && botSvc.Enabled() {
			botSvc.SetInboxHook(pm.Inbox.FromBot)
		}
	}
	if d == nil || d.PlatformRepo == nil || d.DB == nil {
		return r83Routes{}
	}
	clubRepo := pg.NewClubRepo(d.DB)
	owner := httpapi.FirstTeamID(team)
	p := &httpapi.CyclePlanner{Docs: d.PlatformRepo, Load: clubRepo.Load, Owner: owner, Team: teamMap,
		BotToken: token, JWT: []byte(jwtSecret)}
	if appGW != nil && appGW.Writes != nil {
		w := appGW.Writes
		p.Write = func(ctx context.Context, action string, params map[string]string) error {
			return w.ServerWriteNotify(ctx, owner, "Сервер: следующий цикл", action, params)
		}
	}
	if botSvc != nil && botSvc.Enabled() {
		p.Send, p.Edit = botSvc.SendMessageID, botSvc.EditMessageKB
		botSvc.SetTeamCallbackHook(httpapi.CyclePrefix, p.HandleCallback)
		if os.Getenv("CYCLE_TASKS") != "off" {
			go func() {
				time.Sleep(3 * time.Minute) // after the start's own work and the first import
				p.Loop(context.Background())
			}()
		}
	}
	return r83Routes{cycle: p}
}
