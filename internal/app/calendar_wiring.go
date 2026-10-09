package app

import (
	"context"
	"log"
	"os"
	"strings"
	"time"

	"github.com/bnursik/business_surgery_backend/internal/bot"
	"github.com/bnursik/business_surgery_backend/internal/club"
	"github.com/bnursik/business_surgery_backend/internal/gcal"
	httpapi "github.com/bnursik/business_surgery_backend/internal/handlers/http"
	pg "github.com/bnursik/business_surgery_backend/internal/repository/pg"
)

// wireCalendar (R67): the server keeps the club's Google Calendar itself: a
// held meeting turns green with «✅ », a new one gets its event (online: a
// Meet link), a moved or deleted one follows; all of it without e-mails and
// without reminders. The owner connects it once through /calendar in the bot.
func wireCalendar(g *httpapi.AppGateway, writes *httpapi.ClubWrites, clubRepo *pg.ClubRepo, meta *pg.BotRepo, botSvc *bot.Service) httpapi.RoutesRegistrar {
	s := &gcal.Sync{C: gcal.New(meta)}
	s.Load = func(ctx context.Context) (*club.Snapshot, error) {
		return clubRepo.LoadBundle(ctx, time.Now().Add(-48*time.Hour))
	}
	s.SetLink = func(ctx context.Context, res string, day time.Time, link, eventID string) error {
		if err := clubRepo.SetMeetingEvent(ctx, res, day, link, eventID); err != nil {
			return err
		}
		g.Reset()
		if writes.Tables != nil {
			writes.Tables()
		}
		return nil
	}
	writes.OnMeeting = s.OnWrite
	setup := httpapi.NewGcalSetup(s)
	if botSvc != nil && botSvc.Enabled() {
		tell := func(ctx context.Context, text string) {
			for _, id := range botSvc.NotifyIDs() {
				if err := botSvc.SendMessage(ctx, id, text); err != nil {
					log.Printf("gcal notify %d: %v", id, err)
				}
			}
		}
		setup.Tell = tell
		botSvc.SetCalendarHook(func(ctx context.Context) string {
			link, err := setup.SetupLink(ctx)
			if err != nil {
				return "📅 Google Календарь: " + setup.Status(ctx) + "\nСсылку сделать не вышло: " + err.Error()
			}
			return "📅 Google Календарь: " + setup.Status(ctx) +
				"\n\nСсылка для подключения (действует 3 дня, только для вас):\n" + link +
				"\n\nПосле подключения платформа сама красит прошедшие встречи в зелёный с ✅, создаёт события новых встреч и не шлёт уведомления и письма." +
				"\n\nТот же ключ Google нужен резидентам и команде: в «Календаре работы» у каждого кнопка «Подключить Google Календарь». Проверьте, что приложение Google опубликовано (Audience → In production), иначе Google отключает их через 7 дней."
		})
	}
	go func() {
		time.Sleep(45 * time.Second) // after the start's own work
		setup.AtStart()
	}()
	return setup
}

// calClient: the owner's Google client of the R67 setup (its OAuth client
// serves every person's connection too).
func calClient(m httpapi.RoutesRegistrar) *gcal.Client {
	if s, ok := m.(*httpapi.GcalSetup); ok && s.Sync != nil {
		return s.Sync.C
	}
	return nil
}

// wireUserCalendars (R71): each person's «Календарь работы» syncs with
// their own Google Calendar (internal/gcal/user.go): every 5 minutes, when
// the calendar opens and a few seconds after an edit.
func wireUserCalendars(d *Deps, g *httpapi.AppGateway, owner *gcal.Client, jwtSecret string, botSvc *bot.Service) httpapi.RoutesRegistrar {
	m := httpapi.NewGcalUsersModule(g, []byte(jwtSecret))
	if owner == nil || d == nil || d.DB == nil || d.PlatformRepo == nil {
		return m
	}
	repo := pg.NewGcalUserRepo(d.DB, d.PlatformRepo)
	u := &gcal.UserSync{Owner: owner, Store: repo, Docs: repo, Secret: []byte(jwtSecret)}
	if botSvc != nil && botSvc.Enabled() {
		u.Notify = func(ctx context.Context, text string) {
			for _, id := range botSvc.NotifyIDs() {
				if err := botSvc.SendMessage(ctx, id, text); err != nil {
					log.Printf("gcal users notify %d: %v", id, err)
				}
			}
		}
	}
	httpapi.SetGcalUsers(u)
	if !strings.EqualFold(os.Getenv("GCAL_USERS"), "off") {
		go func() {
			time.Sleep(90 * time.Second)
			u.SyncAll(context.Background())
			u.Loop(context.Background())
		}()
	}
	return m
}
