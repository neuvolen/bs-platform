package app

import (
	"context"
	"log"
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
				"\n\nПосле подключения платформа сама красит прошедшие встречи в зелёный с ✅, создаёт события новых встреч и не шлёт уведомления и письма."
		})
	}
	go func() {
		time.Sleep(45 * time.Second) // after the start's own work
		setup.AtStart()
	}()
	return setup
}
