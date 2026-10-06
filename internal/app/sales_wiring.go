package app

import (
	"context"
	"io"
	"log"
	"os"
	"time"

	"github.com/bnursik/business_surgery_backend/internal/bot"
	httpapi "github.com/bnursik/business_surgery_backend/internal/handlers/http"
	"github.com/bnursik/business_surgery_backend/internal/repository/pg"
	"github.com/bnursik/business_surgery_backend/web"
)

// R51: продажи клуба (handlers/http/sales*.go): сценарий после
// экспресс-разбора, кейсы из замеров с согласия, итог периода резидента и
// продление в 1 клик, еженедельный отчёт владельцу.

var (
	appGW       *httpapi.AppGateway // BuildAppGateway
	outreachRef *httpapi.Outreach   // WireOutreach
	// SysCheckRef: the system check WireSysCheck made (the weekly report calls its Run).
	SysCheckRef *httpapi.SysCheck
)

// WireSales: after BuildAppGateway, WireSysCheck and WireOutreach.
func WireSales(d *Deps, pm *httpapi.PlatformModule, botSvc *bot.Service, team, jwtSecret string) httpapi.RoutesRegistrar {
	s := httpapi.NewClubSales(d.PlatformRepo, []byte(jwtSecret))
	clubRepo := pg.NewClubRepo(d.DB)
	s.Club, s.Boards = clubRepo, d.PlatformRepo
	s.Owner = httpapi.FirstTeamID(team)
	for id := range httpapi.ParsePlatformTeam(team) {
		s.Admins = append(s.Admins, id)
	}
	s.Meta = pg.NewBotRepo(d.DB)
	// the error lines of the log, counted for the weekly report
	s.Errors = httpapi.NewErrCounter()
	log.SetOutput(io.MultiWriter(os.Stderr, s.Errors))
	if pm != nil && pm.AI != nil {
		if c := pm.AI.AI; c != nil {
			s.AI = c.Text
			s.AIUsed = func() map[string]int {
				out := map[string]int{}
				for _, p := range c.ProviderStates() {
					if p.Used > 0 {
						out[p.Name] = p.Used
					}
				}
				return out
			}
		}
		pm.AI.OnCallPublished = s.OnCallPublished
	}
	if SysCheckRef != nil {
		s.Check = SysCheckRef.Run
	}
	s.OnCases = func(list []httpapi.PublicCaseView) {
		out := make([]web.PublicCase, 0, len(list))
		for _, c := range list {
			out = append(out, web.PublicCase{Title: c.Title, Who: c.Who, Metrics: c.Metrics, Story: c.Story})
		}
		web.SetPublicCases(out)
	}
	if appGW != nil {
		if f := appGW.Funnel; f != nil {
			s.F = f
			f.AfterRazbor = s.AfterRazbor
		}
		if w := appGW.Writes; w != nil {
			owner := s.Owner
			s.Write = func(ctx context.Context, action string, p map[string]string) error {
				return w.ServerWrite(ctx, owner, "Сервер: продление", action, p)
			}
			w.AfterWrite = s.OnClubWrite
		}
	}
	if outreachRef != nil {
		s.WA, s.WARoute = outreachRef.DeliverWA, outreachRef.Route
	}
	if botSvc != nil && botSvc.Enabled() {
		s.Send = botSvc.SendMessageKB
		s.Doc = botSvc.SendDocumentKB
		s.Resident = botSvc.SendResident
		botSvc.SetPublicCallbackHook("pz:", s.HandleCallback)  // «Хочу в клуб», «Есть вопрос», «Не сейчас»
		botSvc.SetPublicCallbackHook("cs:", s.ConsentCallback) // согласие на кейс
		botSvc.SetPublicCallbackHook("rn:", s.RenewCallback)   // «Продлить на 3 месяца / на год»
	}
	if d.PlatformRepo != nil && os.Getenv("SALES_LOOP") != "off" {
		go func() {
			time.Sleep(20 * time.Second)
			ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
			s.SyncPublic(ctx)
			cancel()
			s.Loop(context.Background())
		}()
	}
	return &httpapi.SalesModule{S: s, G: appGW, Secret: []byte(jwtSecret)}
}
