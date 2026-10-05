package app

import (
	"context"
	"os"
	"time"

	"github.com/bnursik/business_surgery_backend/internal/bot"
	httpapi "github.com/bnursik/business_surgery_backend/internal/handlers/http"
	"github.com/bnursik/business_surgery_backend/internal/repository/pg"
	"github.com/bnursik/business_surgery_backend/web"
)

// SysCheckDelay: the check after a deploy waits for the server to settle.
var SysCheckDelay = 2 * time.Minute

// WireSysCheck: R36: «Проверка системы»: the admins' /status in the bot,
// the settings' button, and the owner's message after a deploy when
// something changed or fails (handlers/http/syscheck.go).
func WireSysCheck(d *Deps, pm *httpapi.PlatformModule, botSvc *bot.Service, team, jwtSecret string) httpapi.RoutesRegistrar {
	s := &httpapi.SysCheck{
		Meta:  pg.NewBotRepo(d.DB),
		Owner: httpapi.FirstTeamID(team),
		DB:    func(ctx context.Context) error { return d.DB.Pool.Ping(ctx) },
		Builtin: func() (int, int) {
			all := web.TourTexts()
			return len(all) - len(web.TourTextsUnvoiced()), len(all)
		},
	}
	if pm != nil && pm.AI != nil {
		s.AI, s.Premium, s.Recs = pm.AI.AI, pm.AI.Premium, pm.AI
	}
	if botSvc != nil && botSvc.Enabled() {
		s.Send = botSvc.SendMessage
		s.Webhook = botSvc.WebhookOwned
		botSvc.SetSystemCheck(func(ctx context.Context) string { return s.Run(ctx).Text() })
		s.NoQuiet = os.Getenv("SYSCHECK_QUIET") == "off"
		delay := SysCheckDelay
		if d, err := time.ParseDuration(os.Getenv("SYSCHECK_DELAY")); err == nil && d >= 0 {
			delay = d
		}
		if os.Getenv("SYSCHECK_NOTIFY") != "off" {
			go s.AfterDeploy(context.Background(), delay)
		}
	}
	return httpapi.NewSysCheckModule(s, []byte(jwtSecret))
}
