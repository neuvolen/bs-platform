package http

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/bnursik/business_surgery_backend/internal/bot"
	"github.com/bnursik/business_surgery_backend/internal/club"
	"github.com/bnursik/business_surgery_backend/internal/middleware"
	pg "github.com/bnursik/business_surgery_backend/internal/repository/pg"
	"github.com/gin-gonic/gin"
)

// R38c: the club's own events with an RSVP in the bot, and broadcasts.
//
// An event (club_events) is announced by a broadcast (bc_campaigns) the
// owner prepares on the platform: the text, the audience (Резиденты, Лиды из
// CRM по стадиям, Подписчики бота, Все), the count, a test to himself and
// one «Отправить». The message carries «Иду» / «Подробнее». «Иду» books a
// seat (the waitlist when the seats are gone), asks the phone when the
// person is unknown, puts a lead into the CRM and answers with the payment
// note. «Указать адрес» sends the address to everyone going; the day before
// at 18:00 and on the day at 08:30 the bot reminds them.
//
// Nothing goes out by itself: a broadcast is a draft until the owner sends
// it; it goes from 10:00 to 20:00 Almaty unless «Отправить сейчас» is
// forced, about 14 messages a second, never twice to the same chat
// (bc_sends' primary key; a row claimed before a restart is not resent).
//
// Team only (/api/v1/platform):
//
//	GET  /outreach/events                     upcoming events with counts
//	GET  /outreach/events/:id                 the event, participants, its broadcasts
//	PUT  /outreach/events/:id                 title, date, time, price, seats, payLink, about, status
//	POST /outreach/events/:id/address {address}
//	POST /outreach/events/:id/participants {name, phone}   added by the team (WhatsApp)
//	POST /outreach/events/:id/participants/:who/cancel
//	GET  /outreach/campaigns/:id              the draft, the counts, the progress
//	PUT  /outreach/campaigns/:id {text, audience}
//	POST /outreach/campaigns/:id/preview {audience}
//	POST /outreach/campaigns/:id/test
//	POST /outreach/campaigns/:id/send {force}

// The business breakfast of 08.10.2026: created once (seedEvents).
const (
	BreakfastID       = "bb20261008"
	BreakfastCampaign = "bb20261008-invite"
)

// BreakfastText: the announcement as the owner wrote it (he may edit the draft).
const BreakfastText = "☕ Бизнес-завтрак Business Surgery\n" +
	"📅 Четверг, 8 октября, 10:00 · Алматы\n\n" +
	"Утро с собственниками бизнеса, которые хотят расти без хаоса.\n\n" +
	"За завтраком:\n" +
	"▪️ разберём реальные ситуации участников: где бизнес теряет деньги и что лечить в первую очередь\n" +
	"▪️ покажем, как Business Surgery ставит диагноз бизнесу за 10 дней\n" +
	"▪️ познакомитесь с предпринимателями клуба\n\n" +
	"💳 Участие: 10 000 ₸\n" +
	"⚠️ Мест мало: формат камерный\n" +
	"📍 Адрес пришлём участникам накануне\n\n" +
	"Нажмите «Иду», чтобы забронировать место 👇"

// Broadcast hours (Almaty): from 10:00 to 20:00 unless forced.
const (
	bcFrom  = 10
	bcUntil = 20
)

func breakfastSeats() int {
	if n, err := strconv.Atoi(strings.TrimSpace(os.Getenv("EVENT_SEATS"))); err == nil && n > 0 {
		return n
	}
	return 15
}

// seedEvents: the breakfast and its draft broadcast, once, while it is ahead.
func (o *Outreach) seedEvents(ctx context.Context) {
	at := time.Date(2026, 10, 8, 10, 0, 0, 0, club.Almaty)
	if !o.now().Before(at) {
		return
	}
	added, err := o.repo.EventAddOnce(ctx, pg.ClubEvent{ID: BreakfastID, Title: "Бизнес-завтрак Business Surgery", StartsAt: at, City: "Алматы",
		AddressNote: "адрес сообщим участникам накануне", Price: 10000, Seats: breakfastSeats(),
		PayLink: strings.TrimSpace(os.Getenv("EVENTS_PAY_LINK")), About: BreakfastText, Status: "open"})
	if err != nil {
		log.Printf("outreach: seed event: %v", err)
		return
	}
	if added {
		log.Printf("outreach: event %s created", BreakfastID)
	}
	if ok, err := o.repo.CampaignAddOnce(ctx, pg.Campaign{ID: BreakfastCampaign, Title: "Анонс: Бизнес-завтрак 8 октября", EventID: BreakfastID,
		Text: BreakfastText, Audience: json.RawMessage(`{"all":true}`)}); err == nil && ok {
		log.Printf("outreach: draft broadcast %s prepared (not sent)", BreakfastCampaign)
	}
}

// SeedEventPayLinks (R55): an open paid event without a payment link gets
// the owner's Kaspi link (bot.KaspiLink) once; the marker in the server doc
// bs_paylink_seed keeps a link the team cleared afterwards cleared.
func (o *Outreach) SeedEventPayLinks(ctx context.Context) int {
	if o.repo == nil || o.docs == nil {
		return 0
	}
	evs, err := o.repo.Events(ctx)
	if err != nil {
		log.Printf("outreach: pay links: %v", err)
		return 0
	}
	marks := map[string]any{}
	base := 0
	if d, err := o.docs.GetDoc(ctx, "server", payLinkSeedDoc); err == nil && d != nil {
		base = d.Version
		if !d.Deleted {
			_ = json.Unmarshal([]byte(d.Value), &marks)
		}
	}
	if marks == nil {
		marks = map[string]any{}
	}
	n := 0
	now := o.now()
	for _, e := range evs {
		if e.Price <= 0 || e.Status != "open" || strings.TrimSpace(e.PayLink) != "" || !e.StartsAt.After(now) || marks[e.ID] != nil {
			continue
		}
		e.PayLink = bot.KaspiLink
		if err := o.repo.EventPut(ctx, e); err != nil {
			log.Printf("outreach: pay link %s: %v", e.ID, err)
			continue
		}
		marks[e.ID] = now.UTC().Format(time.RFC3339)
		n++
		log.Printf("outreach: event %s: Kaspi pay link set (it was empty)", e.ID)
	}
	if n > 0 {
		b, _ := json.Marshal(marks)
		if _, err := o.docs.PutDoc(ctx, "server", payLinkSeedDoc, base, string(b), false, "server:outreach"); err != nil {
			log.Printf("outreach: pay link marks: %v", err)
		}
	}
	return n
}

const payLinkSeedDoc = "bs_paylink_seed"

// ── the event in words ──

var ruWeekdays = []string{"Воскресенье", "Понедельник", "Вторник", "Среда", "Четверг", "Пятница", "Суббота"}

func evWhen(e pg.ClubEvent) string {
	a := e.StartsAt.In(club.Almaty)
	return fmt.Sprintf("%s, %d %s, %s", ruWeekdays[a.Weekday()], a.Day(), ruMonthsGen[a.Month()-1], a.Format("15:04"))
}

func evPay(e pg.ClubEvent) string {
	s := "💳 Участие: " + bot.Money(e.Price) + " ₸"
	if e.Price <= 0 {
		return "💳 Участие бесплатное"
	}
	if strings.HasPrefix(e.PayLink, "https://") {
		s += ", оплатить можно по ссылке: " + e.PayLink
		if isKaspiOpen(e.PayLink) { // R55: the link opens Kaspi without a sum
			s += " (в Kaspi введите сумму " + bot.Money(e.Price) + " ₸)"
		}
		return s
	}
	return s + ": оплата на месте или по ссылке, которую пришлём"
}

func evPlace(e pg.ClubEvent) string {
	if a := strings.TrimSpace(e.Address); a != "" {
		return "📍 " + a
	}
	n := strings.TrimSpace(e.AddressNote)
	if n == "" {
		n = "адрес сообщим участникам накануне"
	}
	return "📍 " + e.City + ", " + n
}

func evKB(e pg.ClubEvent) map[string]any {
	return map[string]any{"inline_keyboard": [][]map[string]any{{
		{"text": "Иду", "callback_data": "ev:go:" + e.ID},
		{"text": "Подробнее", "callback_data": "ev:more:" + e.ID},
	}}}
}

func evCancelKB(e pg.ClubEvent) map[string]any {
	return map[string]any{"inline_keyboard": [][]map[string]any{{{"text": "Не смогу прийти", "callback_data": "ev:no:" + e.ID}}}}
}

// waText: the announcement for WhatsApp: no buttons, so «ответьте «Иду»».
func waText(text string) string {
	lines := strings.Split(text, "\n")
	for i, l := range lines {
		if strings.Contains(l, "«Иду»") {
			lines[i] = "Чтобы забронировать место, ответьте «Иду» на это сообщение 👇"
		}
	}
	return strings.Join(lines, "\n")
}

// ── who pressed ──

func (o *Outreach) isAdmin(tg int64) bool {
	for _, a := range o.admins {
		if a == tg {
			return true
		}
	}
	return false
}

// identify: a resident, the team, a CRM lead or a guest; the phone when known.
func (o *Outreach) identify(ctx context.Context, tg int64, name string) (kind, who, phone string) {
	kind, who = "guest", name
	if list, err := o.club.LoadResidents(ctx); err == nil {
		for _, r := range list {
			if r.TgID == tg && r.Name != "" && !r.Archived {
				kind, who = "resident", r.Name
				if c, ok := o.channels(ctx)[club.NormName(r.Name)]; ok && c.Phone != "" {
					phone = "+" + c.Phone
				}
				break
			}
		}
	}
	if kind == "guest" && o.isAdmin(tg) {
		kind = "team"
	}
	if o.docs != nil {
		if d, err := o.docs.GetDoc(ctx, "club", "bs_crm"); err == nil && d != nil && !d.Deleted {
			var crm map[string]any
			_ = json.Unmarshal([]byte(d.Value), &crm)
			leads, _ := crm["leads"].([]any)
			if l := findLeadByTg(leads, tg); l != nil {
				if kind == "guest" {
					kind = "lead"
				}
				if p, _ := l["phone"].(string); phone == "" && len(phoneDigits(p)) >= 11 {
					phone = p
				}
			}
		}
	}
	return kind, who, phone
}

// mutateCRM: read bs_crm, change it, write it back (retrying on a conflict).
func (o *Outreach) mutateCRM(ctx context.Context, fn func(crm map[string]any) bool) error {
	if o.docs == nil {
		return nil
	}
	for try := 0; try < 5; try++ {
		crm := map[string]any{}
		base := 0
		if d, err := o.docs.GetDoc(ctx, "club", "bs_crm"); err == nil && d != nil {
			base = d.Version
			if !d.Deleted {
				_ = json.Unmarshal([]byte(d.Value), &crm)
			}
			if crm == nil {
				crm = map[string]any{}
			}
		}
		if !fn(crm) {
			return nil
		}
		val, _ := json.Marshal(crm)
		if _, err := o.docs.PutDoc(ctx, "club", "bs_crm", base, string(val), false, "server:events"); err == nil {
			return nil
		}
	}
	return pg.ErrPlatformConflict
}

// crmLead: someone outside the club pressed «Иду»: a lead in the CRM (or a
// line in the existing one's history), with the phone once known.
func (o *Outreach) crmLead(ctx context.Context, e pg.ClubEvent, cb bot.CallbackUpdate, phone, logText string) {
	now := o.now()
	err := o.mutateCRM(ctx, func(crm map[string]any) bool {
		leads, _ := crm["leads"].([]any)
		if l := findLeadByTg(leads, cb.FromID); l != nil {
			if phone != "" {
				if p, _ := l["phone"].(string); p == "" {
					l["phone"] = phone
				}
			}
			if logText != "" {
				addLog(l, now, logText)
			}
			return true
		}
		if logText == "" {
			return false
		}
		name := strings.TrimSpace(cb.FirstName + " " + cb.LastName)
		if name == "" {
			name = "Без имени"
		}
		tg := ""
		if cb.Username != "" {
			tg = "@" + cb.Username
		}
		l := map[string]any{"id": fmt.Sprintf("tg%d", cb.FromID), "col": "new", "name": name, "phone": phone, "tg": tg, "tgId": cb.FromID,
			"source": "Мероприятие: " + e.Title, "niche": "", "note": "", "sum": "", "date": now.In(club.Almaty).Format("02.01.2006"), "funnel": "event"}
		addLog(l, now, logText)
		crm["leads"] = append([]any{l}, leads...)
		return true
	})
	if err != nil {
		log.Printf("outreach: crm lead %d: %v", cb.FromID, err)
	}
}

func (o *Outreach) send(ctx context.Context, chat int64, text string, kb map[string]any) error {
	if o.Send == nil {
		return fmt.Errorf("бот не подключён")
	}
	return o.Send(ctx, chat, text, kb)
}

// toParticipant: a message to someone going: Telegram, or WhatsApp for
// those the team added by phone (or residents who chose WhatsApp).
func (o *Outreach) toParticipant(ctx context.Context, e pg.ClubEvent, x pg.Rsvp, kind, text string, kb map[string]any) error {
	if strings.HasPrefix(x.Who, "wa:") {
		return o.DeliverWA(ctx, bot.WAMessage{Resident: x.Name, Phone: strings.TrimPrefix(x.Who, "wa:"), Kind: "event", Key: "ev|" + e.ID + "|" + kind + "|" + x.Who, Text: text})
	}
	chat, err := strconv.ParseInt(strings.TrimPrefix(x.Who, "tg:"), 10, 64)
	if err != nil || chat == 0 {
		return fmt.Errorf("bad participant %q", x.Who)
	}
	if x.Kind == "resident" {
		if p := o.Route(ctx, x.Name); p != "" {
			return o.DeliverWA(ctx, bot.WAMessage{Resident: x.Name, Phone: p, Kind: "event", Key: "ev|" + e.ID + "|" + kind + "|" + x.Who, Text: text})
		}
	}
	return o.send(ctx, chat, text, kb)
}

func (o *Outreach) confirmText(e pg.ClubEvent) string {
	return "✅ Вы записаны: " + e.Title + "\n📅 " + evWhen(e) + "\n" + evPlace(e) + "\n\n" + evPay(e) +
		"\n\nЕсли планы изменятся, нажмите «Не смогу прийти»: место перейдёт следующему."
}

// EventButton: «Иду», «Подробнее», «Не смогу прийти» (callback ev:<op>:<id>).
func (o *Outreach) EventButton(ctx context.Context, cb bot.CallbackUpdate) (string, bool) {
	parts := strings.SplitN(cb.Data, ":", 3)
	if len(parts) != 3 {
		return "", false
	}
	e, err := o.repo.EventGet(ctx, parts[2])
	if err != nil {
		return "Не получилось, попробуйте через минуту", true
	}
	if e == nil {
		return "Мероприятие не найдено", true
	}
	who := "tg:" + strconv.FormatInt(cb.FromID, 10)
	switch parts[1] {
	case "more":
		_ = o.send(ctx, cb.ChatID, e.About+"\n\n"+evPlace(*e)+"\n"+evPay(*e), evKB(*e))
		return "", true
	case "no":
		cancelled, promoted, err := o.repo.RsvpCancel(ctx, e.ID, who, e.Seats)
		if err != nil {
			return "Не получилось, попробуйте через минуту", true
		}
		if !cancelled {
			return "Вы не были записаны", true
		}
		_ = o.send(ctx, cb.ChatID, "Запись отменена. Если планы изменятся, нажмите «Иду» в анонсе: место дадим, если оно будет свободно.", nil)
		o.crmLead(ctx, *e, cb, "", "Отменил запись: "+e.Title)
		if promoted != nil {
			o.promoted(ctx, *e, *promoted)
		}
		return "Запись отменена", true
	case "go":
	default:
		return "", false
	}
	if e.Status != "open" || !o.now().Before(e.StartsAt) {
		return "Запись на это мероприятие закрыта", true
	}
	name := strings.TrimSpace(cb.FirstName + " " + cb.LastName)
	kind, name, phone := o.identify(ctx, cb.FromID, name)
	// the team knows its residents: the phone is asked from the others only
	askPhone := phone == "" && kind != "resident" && kind != "team"
	x, fresh, err := o.repo.RsvpJoin(ctx, pg.Rsvp{EventID: e.ID, Who: who, Name: name, Username: cb.Username, Phone: phone, Kind: kind, WantPhone: askPhone}, e.Seats)
	if err != nil {
		log.Printf("outreach: rsvp %s %s: %v", e.ID, who, err)
		return "Не получилось записать, попробуйте через минуту", true
	}
	if !fresh {
		if x.Status == "waitlist" {
			return "Вы в листе ожидания: напишем, если место освободится", true
		}
		return "Вы уже записаны ✅", true
	}
	if x.Status == "waitlist" {
		_ = o.send(ctx, cb.ChatID, "Места на «"+e.Title+"» закончились 😔\n\nВы в листе ожидания: если место освободится, напишем сразу.", nil)
	} else {
		_ = o.send(ctx, cb.ChatID, o.confirmText(*e), evCancelKB(*e))
		if strings.TrimSpace(e.Address) != "" {
			_, _ = o.repo.NoteOnce(ctx, e.ID, who, "address:"+dedupKey(e.Address)[:8]) // the address was in the confirmation
		}
	}
	if askPhone && o.Contact != nil {
		_ = o.Contact(ctx, cb.ChatID, "📱 Оставьте, пожалуйста, номер телефона: по нему свяжемся и пришлём адрес. Нажмите кнопку ниже.")
	}
	if kind != "resident" && kind != "team" {
		st := map[string]string{"going": "Записался", "waitlist": "В листе ожидания"}[x.Status]
		o.crmLead(ctx, *e, cb, "", st+": "+e.Title+" ("+e.StartsAt.In(club.Almaty).Format("02.01")+")")
	}
	o.maybeFull(ctx, *e)
	if x.Status == "waitlist" {
		return "Вы в листе ожидания", true
	}
	return "Вы записаны ✅", true
}

// maybeFull: the owner hears once that the seats are gone.
func (o *Outreach) maybeFull(ctx context.Context, e pg.ClubEvent) {
	if e.FullNoted || e.Seats <= 0 {
		return
	}
	list, err := o.repo.Rsvps(ctx, e.ID)
	if err != nil {
		return
	}
	going := 0
	for _, x := range list {
		if x.Status == "going" {
			going++
		}
	}
	if going < e.Seats {
		return
	}
	e.FullNoted = true
	if err := o.repo.EventPut(ctx, e); err != nil {
		return
	}
	if o.owner != 0 && o.Send != nil {
		_ = o.Send(ctx, o.owner, fmt.Sprintf("🎟 Места на «%s» закончились: %d из %d. Новые записи идут в лист ожидания; места можно добавить в карточке мероприятия на платформе.", e.Title, going, e.Seats), nil)
	}
}

func (o *Outreach) promoted(ctx context.Context, e pg.ClubEvent, x pg.Rsvp) {
	if ok, _ := o.repo.NoteOnce(ctx, e.ID, x.Who, "promoted"); !ok {
		return
	}
	_ = o.toParticipant(ctx, e, x, "promoted", "🎉 Освободилось место, и оно ваше!\n\n"+o.confirmText(e), evCancelKB(e))
}

// EventContact: the phone a participant shared after «Иду».
func (o *Outreach) EventContact(ctx context.Context, cu bot.ContactUpdate) bool {
	evs, err := o.repo.RsvpPhone(ctx, "tg:"+strconv.FormatInt(cu.FromID, 10), cu.Phone)
	if err != nil || len(evs) == 0 {
		return false
	}
	_ = o.send(ctx, cu.ChatID, "✅ Спасибо, номер сохранён. Адрес пришлём накануне.", map[string]any{"remove_keyboard": true})
	for _, id := range evs {
		if e, _ := o.repo.EventGet(ctx, id); e != nil {
			o.crmLead(ctx, *e, bot.CallbackUpdate{FromID: cu.FromID, FirstName: cu.FirstName, LastName: cu.LastName, Username: cu.Username}, cu.Phone, "")
		}
	}
	return true
}

// ── reminders (every minute) ──

func sameDay(a, b time.Time) bool {
	a, b = a.In(club.Almaty), b.In(club.Almaty)
	return a.Year() == b.Year() && a.YearDay() == b.YearDay()
}

// eventTick: the address to everyone going once it is set (08:00–22:00), the
// reminder the day before at 18:00 (with the address), the morning one at 08:30.
func (o *Outreach) eventTick(ctx context.Context) {
	evs, err := o.repo.Events(ctx)
	if err != nil {
		return
	}
	now := o.now()
	h, m := almatyHour(now)
	for _, e := range evs {
		if !now.Before(e.StartsAt) || e.Status != "open" {
			continue
		}
		list, err := o.repo.Rsvps(ctx, e.ID)
		if err != nil {
			continue
		}
		eve := sameDay(now.Add(24*time.Hour), e.StartsAt)
		today := sameDay(now, e.StartsAt)
		addr := strings.TrimSpace(e.Address)
		for _, x := range list {
			if x.Status != "going" {
				continue
			}
			switch {
			case today && (h > 8 || (h == 8 && m >= 30)):
				if ok, _ := o.repo.NoteOnce(ctx, e.ID, x.Who, "morning"); ok {
					t := "☕ Сегодня в " + e.StartsAt.In(club.Almaty).Format("15:04") + ": " + e.Title + "\n"
					if addr != "" {
						t += "📍 " + addr
					} else {
						t += "📍 Адрес пришлём в течение часа"
					}
					_ = o.toParticipant(ctx, e, x, "morning", t+"\n\nДо встречи!", nil)
					if addr != "" {
						_, _ = o.repo.NoteOnce(ctx, e.ID, x.Who, "address:"+dedupKey(addr)[:8])
					}
				}
			case eve && h >= 18 && h < 22 && addr != "":
				if ok, _ := o.repo.NoteOnce(ctx, e.ID, x.Who, "eve"); ok {
					_ = o.toParticipant(ctx, e, x, "eve", "⏰ Напоминаем: завтра, "+evWhen(e)+"\n"+e.Title+"\n📍 "+addr+"\n\n"+evPay(e)+"\n\nДо встречи!", nil)
					_, _ = o.repo.NoteOnce(ctx, e.ID, x.Who, "address:"+dedupKey(addr)[:8])
				}
			case addr != "" && h >= 8 && h < 22 && !(today && h == 8):
				if ok, _ := o.repo.NoteOnce(ctx, e.ID, x.Who, "address:"+dedupKey(addr)[:8]); ok {
					_ = o.toParticipant(ctx, e, x, "address:"+dedupKey(addr)[:8], "📍 Адрес: "+e.Title+"\n\n"+addr+"\n📅 "+evWhen(e)+"\n\nДо встречи!", nil)
				}
			}
		}
		// the day before at 18:00 without an address: the owner hears it once
		if eve && h >= 18 && addr == "" && o.owner != 0 && o.Send != nil {
			if ok, _ := o.repo.NoteOnce(ctx, e.ID, "owner", "no-address"); ok {
				_ = o.Send(ctx, o.owner, "📍 Завтра «"+e.Title+"», а адрес не указан. Укажите его на платформе: BS → Мероприятия → карточка → «Указать адрес». Участники получат его сразу.", nil)
			}
		}
	}
}

// ── campaigns ──

type bcAudience struct {
	All         bool     `json:"all"`
	Residents   bool     `json:"residents"`
	Subscribers bool     `json:"subscribers"`
	CRM         []string `json:"crm"` // CRM stages: new, work, qual, meet, diag, won, lost
	// R47: a segment of the CRM (outreach_segments.go): exactly these leads (bs_crm ids)
	Leads []string `json:"leads,omitempty"`
}

func parseAudience(raw json.RawMessage) bcAudience {
	var a bcAudience
	_ = json.Unmarshal(raw, &a)
	return a
}

type bcCounts struct {
	Residents   int `json:"residents"`
	ResidentsWA int `json:"residentsWa"`
	ResidentsNo int `json:"residentsNoChannel"`
	CRM         int `json:"crm"`
	CRMNoTg     int `json:"crmNoTelegram"`
	Subscribers int `json:"subscribers"`
	Total       int `json:"total"`
	Telegram    int `json:"telegram"`
	WhatsApp    int `json:"whatsapp"`
	Team        int `json:"teamExcluded"`
}

// targets: who gets the broadcast, each chat once (a resident first, then a
// lead, then a subscriber); the team gets only the test.
func (o *Outreach) targets(ctx context.Context, a bcAudience) ([]pg.BcTarget, bcCounts, error) {
	var out []pg.BcTarget
	var n bcCounts
	seen := map[string]bool{}
	add := func(t pg.BcTarget) bool {
		if seen[t.Target] {
			return false
		}
		if strings.HasPrefix(t.Target, "tg:") {
			if id, _ := strconv.ParseInt(t.Target[3:], 10, 64); o.isAdmin(id) {
				n.Team++
				seen[t.Target] = true
				return false
			}
		}
		seen[t.Target] = true
		out = append(out, t)
		return true
	}
	if a.All || a.Residents {
		list, err := o.club.LoadResidents(ctx)
		if err != nil {
			return nil, n, err
		}
		names := map[string]bool{}
		for _, r := range list {
			k := club.NormName(r.Name)
			if r.Name == "" || r.Former || r.Archived || r.Admin || names[k] {
				continue
			}
			names[k] = true
			if p := o.Route(ctx, r.Name); p != "" {
				if add(pg.BcTarget{Target: "wa:" + p, Name: r.Name, Kind: "resident"}) {
					n.Residents++
					n.ResidentsWA++
				}
			} else if r.TgID != 0 {
				if add(pg.BcTarget{Target: "tg:" + strconv.FormatInt(r.TgID, 10), Name: r.Name, Kind: "resident"}) {
					n.Residents++
				}
			} else {
				n.ResidentsNo++
			}
		}
	}
	stages := map[string]bool{}
	for _, s := range a.CRM {
		stages[s] = true
	}
	var leads []any
	pick := map[string]bool{} // R47: the segment's leads
	for _, id := range a.Leads {
		pick[id] = true
	}
	if o.docs != nil && (a.All || len(stages) > 0 || a.Subscribers || len(pick) > 0) {
		if d, err := o.docs.GetDoc(ctx, "club", "bs_crm"); err == nil && d != nil && !d.Deleted {
			var crm map[string]any
			_ = json.Unmarshal([]byte(d.Value), &crm)
			leads, _ = crm["leads"].([]any)
		}
	}
	if a.All || len(stages) > 0 || len(pick) > 0 {
		for _, l := range leads {
			m, _ := l.(map[string]any)
			if m == nil || (!a.All && !stages[fmt.Sprint(m["col"])] && !pick[pStr(m, "id")]) {
				continue
			}
			name, _ := m["name"].(string)
			if tg := leadTg(m); tg > 0 {
				if add(pg.BcTarget{Target: "tg:" + strconv.FormatInt(tg, 10), Name: name, Kind: "lead"}) {
					n.CRM++
				}
			} else {
				n.CRMNoTg++
			}
		}
	}
	if a.All || a.Subscribers {
		chats, err := o.repo.BotChats(ctx)
		if err != nil {
			return nil, n, err
		}
		for _, l := range leads { // the bot's own leads, also older than the 30 days of updates
			if m, _ := l.(map[string]any); m != nil && m["funnel"] == "bot" {
				if tg := leadTg(m); tg > 0 {
					if _, ok := chats[tg]; !ok {
						name, _ := m["name"].(string)
						chats[tg] = name
					}
				}
			}
		}
		for id, name := range chats {
			if add(pg.BcTarget{Target: "tg:" + strconv.FormatInt(id, 10), Name: name, Kind: "subscriber"}) {
				n.Subscribers++
			}
		}
	}
	for _, t := range out {
		if strings.HasPrefix(t.Target, "wa:") {
			n.WhatsApp++
		} else {
			n.Telegram++
		}
	}
	n.Total = len(out)
	return out, n, nil
}

var tgRetryRe = regexp.MustCompile(`retry after (\d+)`)

// sendTG: one message of a broadcast, waiting out Telegram's 429.
func (o *Outreach) sendTG(ctx context.Context, chat int64, text string, kb map[string]any) error {
	var err error
	for try := 0; try < 4; try++ {
		err = o.send(ctx, chat, text, kb)
		if err == nil {
			return nil
		}
		m := tgRetryRe.FindStringSubmatch(err.Error())
		if m == nil {
			return err
		}
		n, _ := strconv.Atoi(m[1])
		if n < 1 {
			n = 1
		}
		if n > 60 {
			n = 60
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(time.Duration(n) * time.Second):
		}
	}
	return err
}

func tgErrShort(err error) string {
	s := err.Error()
	switch {
	case strings.Contains(s, "blocked by the user"):
		return "заблокировал бота"
	case strings.Contains(s, "chat not found"), strings.Contains(s, "user is deactivated"), strings.Contains(s, "have no rights"):
		return "чат недоступен"
	}
	if len(s) > 120 {
		s = s[:120]
	}
	return s
}

// runCampaign sends what is left of a campaign (one worker per campaign).
func (o *Outreach) runCampaign(ctx context.Context, id string) {
	o.mu.Lock()
	if o.running[id] {
		o.mu.Unlock()
		return
	}
	o.running[id] = true
	o.mu.Unlock()
	defer func() {
		o.mu.Lock()
		delete(o.running, id)
		o.mu.Unlock()
	}()
	c, err := o.repo.CampaignGet(ctx, id)
	if err != nil || c == nil || c.Status != "sending" {
		return
	}
	var ev *pg.ClubEvent
	var kb map[string]any
	if c.EventID != "" {
		if ev, _ = o.repo.EventGet(ctx, c.EventID); ev != nil {
			kb = evKB(*ev)
		}
	}
	for ctx.Err() == nil {
		t, err := o.repo.SendClaim(ctx, id)
		if err != nil {
			log.Printf("outreach: campaign %s: %v", id, err)
			return
		}
		if t == nil {
			break
		}
		status, why := "sent", ""
		if strings.HasPrefix(t.Target, "wa:") {
			status = "wa"
			if err := o.DeliverWA(ctx, bot.WAMessage{Resident: t.Name, Phone: t.Target[3:], Kind: "broadcast", Key: "bc|" + id + "|" + t.Target[3:], Text: waText(c.Text)}); err != nil {
				status, why = "failed", err.Error()
			}
		} else {
			chat, _ := strconv.ParseInt(t.Target[3:], 10, 64)
			if err := o.sendTG(ctx, chat, c.Text, kb); err != nil {
				status, why = "failed", tgErrShort(err)
			}
		}
		if err := o.repo.SendMark(context.WithoutCancel(ctx), id, t.Target, status, why); err != nil {
			log.Printf("outreach: mark %s %s: %v", id, t.Target, err)
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(o.Pace):
		}
	}
	if ok, _ := o.repo.CampaignDone(ctx, id); ok {
		st, _ := o.repo.SendStats(ctx, id)
		log.Printf("outreach: campaign %s done: %v", id, st)
		to := o.owner
		if v, err := strconv.ParseInt(strings.TrimPrefix(c.StartedBy, "tg:"), 10, 64); err == nil && v != 0 {
			to = v
		}
		if to != 0 && o.Send != nil {
			t := fmt.Sprintf("📣 Рассылка «%s» отправлена: Telegram %d", c.Title, st["sent"])
			if st["wa"] > 0 {
				t += fmt.Sprintf(", WhatsApp %d", st["wa"])
			}
			if f := st["failed"] + st["unknown"]; f > 0 {
				t += fmt.Sprintf(", не доставлено %d (заблокировали бота или чат недоступен)", f)
			}
			_ = o.Send(ctx, to, t+". Подробности и записи на платформе: BS → Мероприятия.", nil)
		}
	}
}

// ── loops ──

// Start: the seeds, the reminders, the broadcasts left by a restart.
func (o *Outreach) Start(ctx context.Context) {
	go func() {
		c, cancel := context.WithTimeout(ctx, 30*time.Second)
		o.seedEvents(c)
		o.SeedEventPayLinks(c)
		if err := o.repo.SendsOrphaned(c); err != nil {
			log.Printf("outreach: orphaned sends: %v", err)
		}
		cancel()
		elena := false
		tick := time.NewTicker(time.Minute)
		defer tick.Stop()
		n := 0
		for {
			c, cancel := context.WithTimeout(ctx, 50*time.Second)
			if !elena && n%60 == 0 { // the residents may come later than the start
				elena = o.SeedElena(c)
			}
			n++
			o.eventTick(c)
			o.notifyWA(c)
			cancel()
			if ids, err := o.repo.CampaignsSending(ctx); err == nil {
				for _, id := range ids {
					go o.runCampaign(context.WithoutCancel(ctx), id)
				}
			}
			select {
			case <-ctx.Done():
				return
			case <-tick.C:
			case <-o.kick:
			case <-o.notifyNow:
				time.Sleep(3 * time.Second) // a burst of messages makes one note
			}
		}
	}()
}

func (o *Outreach) Kick() {
	select {
	case o.kick <- struct{}{}:
	default:
	}
}

// ── HTTP ──

func (o *Outreach) eventView(ctx context.Context, e pg.ClubEvent, withList bool) gin.H {
	list, _ := o.repo.Rsvps(ctx, e.ID)
	going, wait, noPhone := 0, 0, 0
	people := []gin.H{}
	for _, x := range list {
		switch x.Status {
		case "going":
			going++
			if x.Phone == "" {
				noPhone++
			}
		case "waitlist":
			wait++
		}
		if withList {
			p := gin.H{"who": x.Who, "name": x.Name, "username": x.Username, "phone": x.Phone, "kind": x.Kind, "status": x.Status, "at": x.CreatedAt}
			if x.Phone != "" {
				p["phonePretty"] = prettyPhone(x.Phone)
			}
			people = append(people, p)
		}
	}
	a := e.StartsAt.In(club.Almaty)
	v := gin.H{"id": e.ID, "title": e.Title, "startsAt": e.StartsAt, "date": a.Format("2006-01-02"), "time": a.Format("15:04"), "when": evWhen(e),
		"city": e.City, "address": e.Address, "addressNote": e.AddressNote, "price": e.Price, "seats": e.Seats, "payLink": e.PayLink,
		"about": e.About, "status": e.Status, "going": going, "waitlist": wait, "noPhone": noPhone, "past": !o.now().Before(e.StartsAt)}
	if withList {
		v["participants"] = people
	}
	return v
}

// Events: GET /outreach/events
func (o *Outreach) Events(c *gin.Context) {
	if !teamOnly(c) {
		return
	}
	evs, err := o.repo.Events(c.Request.Context())
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "load_failed"})
		return
	}
	out := []gin.H{}
	for _, e := range evs {
		out = append(out, o.eventView(c.Request.Context(), e, false))
	}
	n, _ := o.repo.WAPending(c.Request.Context())
	c.Header("Cache-Control", "no-store")
	c.JSON(http.StatusOK, gin.H{"events": out, "waPending": n})
}

// Event: GET /outreach/events/:id
func (o *Outreach) Event(c *gin.Context) {
	if !teamOnly(c) {
		return
	}
	ctx := c.Request.Context()
	e, err := o.repo.EventGet(ctx, c.Param("id"))
	if err != nil || e == nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "not_found"})
		return
	}
	v := o.eventView(ctx, *e, true)
	cs, _ := o.repo.Campaigns(ctx)
	var camps []gin.H
	for _, x := range cs {
		if x.EventID == e.ID && !strings.HasPrefix(x.ID, segCampaignPrefix) { // R47: a segment's invitation lives in the CRM
			camps = append(camps, o.campaignView(ctx, x, false))
		}
	}
	if camps == nil {
		camps = []gin.H{}
	}
	v["campaigns"] = camps
	c.Header("Cache-Control", "no-store")
	c.JSON(http.StatusOK, v)
}

// PutEvent: PUT /outreach/events/:id
func (o *Outreach) PutEvent(c *gin.Context) {
	if !adminOnly(c) {
		return
	}
	ctx := c.Request.Context()
	e, err := o.repo.EventGet(ctx, c.Param("id"))
	if err != nil || e == nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "not_found"})
		return
	}
	var in struct {
		Title   *string `json:"title"`
		Date    *string `json:"date"`
		Time    *string `json:"time"`
		Price   *int64  `json:"price"`
		Seats   *int    `json:"seats"`
		PayLink *string `json:"payLink"`
		About   *string `json:"about"`
		Status  *string `json:"status"`
	}
	if err := c.ShouldBindJSON(&in); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "bad_json"})
		return
	}
	if in.Title != nil && strings.TrimSpace(*in.Title) != "" {
		e.Title = strings.TrimSpace(*in.Title)
	}
	if in.Date != nil || in.Time != nil {
		a := e.StartsAt.In(club.Almaty)
		d, t := a.Format("2006-01-02"), a.Format("15:04")
		if in.Date != nil {
			d = strings.TrimSpace(*in.Date)
		}
		if in.Time != nil {
			t = strings.TrimSpace(*in.Time)
		}
		at, err := time.ParseInLocation("2006-01-02 15:04", d+" "+t, club.Almaty)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "bad_date", "message": "Дата и время: 2026-10-08 и 10:00"})
			return
		}
		e.StartsAt = at
	}
	if in.Price != nil && *in.Price >= 0 {
		e.Price = *in.Price
	}
	if in.Seats != nil && *in.Seats >= 0 && *in.Seats <= 1000 {
		if *in.Seats > e.Seats {
			e.FullNoted = false
		}
		e.Seats = *in.Seats
	}
	if in.PayLink != nil {
		l := strings.TrimSpace(*in.PayLink)
		if l != "" && !strings.HasPrefix(l, "https://") {
			c.JSON(http.StatusBadRequest, gin.H{"error": "bad_link", "message": "Ссылка на оплату начинается с https://"})
			return
		}
		e.PayLink = l
	}
	if in.About != nil && strings.TrimSpace(*in.About) != "" {
		e.About = strings.TrimSpace(*in.About)
	}
	if in.Status != nil && (*in.Status == "open" || *in.Status == "closed") {
		e.Status = *in.Status
	}
	if err := o.repo.EventPut(ctx, *e); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "store_failed"})
		return
	}
	// more seats: the waitlist moves up and hears it
	if up, err := o.repo.RsvpPromote(ctx, e.ID, e.Seats); err == nil {
		for _, x := range up {
			o.promoted(ctx, *e, x)
		}
	}
	c.JSON(http.StatusOK, o.eventView(ctx, *e, true))
}

// SetAddress: POST /outreach/events/:id/address {address}: saved, sent to
// everyone going (now, or from 08:00 when it is night).
func (o *Outreach) SetAddress(c *gin.Context) {
	if !adminOnly(c) {
		return
	}
	ctx := c.Request.Context()
	e, err := o.repo.EventGet(ctx, c.Param("id"))
	if err != nil || e == nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "not_found"})
		return
	}
	var in struct {
		Address string `json:"address"`
	}
	if err := c.ShouldBindJSON(&in); err != nil || len([]rune(strings.TrimSpace(in.Address))) < 5 || len([]rune(in.Address)) > 300 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "bad_address", "message": "Укажите адрес: улица, дом, заведение"})
		return
	}
	e.Address = strings.Join(strings.Fields(in.Address), " ")
	if err := o.repo.EventPut(ctx, *e); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "store_failed"})
		return
	}
	log.Printf("outreach: %s address set by %s", e.ID, platformUser(c))
	o.eventTick(ctx) // the address goes out now (unless it is night)
	v := o.eventView(ctx, *e, true)
	h, _ := almatyHour(o.now())
	v["sentNow"] = h >= 8 && h < 22
	c.JSON(http.StatusOK, v)
}

// AddParticipant: POST /outreach/events/:id/participants {name, phone}:
// someone without Telegram (Елена): reminders go to WhatsApp.
func (o *Outreach) AddParticipant(c *gin.Context) {
	if !adminOnly(c) {
		return
	}
	ctx := c.Request.Context()
	e, err := o.repo.EventGet(ctx, c.Param("id"))
	if err != nil || e == nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "not_found"})
		return
	}
	var in struct {
		Name  string `json:"name"`
		Phone string `json:"phone"`
	}
	_ = c.ShouldBindJSON(&in)
	phone := waDigits(in.Phone)
	if strings.TrimSpace(in.Name) == "" || len(phone) < 11 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "bad_participant", "message": "Нужны имя и номер WhatsApp"})
		return
	}
	x, _, err := o.repo.RsvpJoin(ctx, pg.Rsvp{EventID: e.ID, Who: "wa:" + phone, Name: strings.TrimSpace(in.Name), Phone: "+" + phone, Kind: "guest"}, e.Seats)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "store_failed"})
		return
	}
	v := o.eventView(ctx, *e, true)
	v["added"] = x.Status
	c.JSON(http.StatusOK, v)
}

// CancelParticipant: POST /outreach/events/:id/participants/:who/cancel
func (o *Outreach) CancelParticipant(c *gin.Context) {
	if !adminOnly(c) {
		return
	}
	ctx := c.Request.Context()
	e, err := o.repo.EventGet(ctx, c.Param("id"))
	if err != nil || e == nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "not_found"})
		return
	}
	_, promoted, err := o.repo.RsvpCancel(ctx, e.ID, c.Param("who"), e.Seats)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "store_failed"})
		return
	}
	if promoted != nil {
		o.promoted(ctx, *e, *promoted)
	}
	c.JSON(http.StatusOK, o.eventView(ctx, *e, true))
}

func (o *Outreach) campaignView(ctx context.Context, x pg.Campaign, counts bool) gin.H {
	st, _ := o.repo.SendStats(ctx, x.ID)
	total := 0
	for _, n := range st {
		total += n
	}
	v := gin.H{"id": x.ID, "title": x.Title, "eventId": x.EventID, "text": x.Text, "audience": json.RawMessage(x.Audience), "status": x.Status,
		"forced": x.Forced, "updatedAt": x.UpdatedAt, "startedAt": x.StartedAt, "startedBy": x.StartedBy, "finishedAt": x.FinishedAt, "testedAt": x.TestedAt,
		"progress": gin.H{"total": total, "sent": st["sent"], "wa": st["wa"], "failed": st["failed"] + st["unknown"], "left": st["pending"] + st["claimed"]}}
	if counts && x.Status == "draft" {
		if _, n, err := o.targets(ctx, parseAudience(x.Audience)); err == nil {
			v["counts"] = n
		}
	}
	if x.Status != "draft" {
		if f, _ := o.repo.SendFailures(ctx, x.ID, 30); len(f) > 0 {
			v["failures"] = f
		}
	}
	h, _ := almatyHour(o.now())
	v["quiet"] = h < bcFrom || h >= bcUntil
	return v
}

// Campaign: GET /outreach/campaigns/:id
func (o *Outreach) Campaign(c *gin.Context) {
	if !teamOnly(c) {
		return
	}
	x, err := o.repo.CampaignGet(c.Request.Context(), c.Param("id"))
	if err != nil || x == nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "not_found"})
		return
	}
	c.Header("Cache-Control", "no-store")
	c.JSON(http.StatusOK, o.campaignView(c.Request.Context(), *x, true))
}

func cleanAudience(raw json.RawMessage) (json.RawMessage, bool) {
	a := parseAudience(raw)
	ok := map[string]bool{"new": true, "work": true, "qual": true, "meet": true, "diag": true, "decide": true, "later": true, "won": true, "lost": true}
	var crm []string
	for _, s := range a.CRM {
		if ok[s] {
			crm = append(crm, s)
		}
	}
	a.CRM = crm
	if len(a.Leads) > 20000 {
		a.Leads = a.Leads[:20000]
	}
	b, _ := json.Marshal(a)
	return b, a.All || a.Residents || a.Subscribers || len(a.CRM) > 0 || len(a.Leads) > 0
}

// PutCampaign: PUT /outreach/campaigns/:id {text, audience}: a draft only.
func (o *Outreach) PutCampaign(c *gin.Context) {
	if !adminOnly(c) {
		return
	}
	var in struct {
		Text     string          `json:"text"`
		Audience json.RawMessage `json:"audience"`
	}
	if err := c.ShouldBindJSON(&in); err != nil || strings.TrimSpace(in.Text) == "" || len([]rune(in.Text)) > 3800 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "bad_text", "message": "Текст рассылки: от 1 до 3 800 символов"})
		return
	}
	aud, _ := cleanAudience(in.Audience)
	// R47: a segment's broadcast keeps its leads even when an editor that does
	// not know them saves the text (the audience never widens by accident)
	if cur, _ := o.repo.CampaignGet(c.Request.Context(), c.Param("id")); cur != nil {
		if old := parseAudience(cur.Audience); len(old.Leads) > 0 && len(parseAudience(aud).Leads) == 0 {
			aud, _ = cleanAudience(cur.Audience)
		}
	}
	ok, err := o.repo.CampaignEdit(c.Request.Context(), c.Param("id"), strings.TrimRight(in.Text, " \n"), aud, platformUser(c))
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "store_failed"})
		return
	}
	if !ok {
		c.JSON(http.StatusConflict, gin.H{"error": "not_draft", "message": "Рассылка уже отправлена: текст не меняется"})
		return
	}
	x, _ := o.repo.CampaignGet(c.Request.Context(), c.Param("id"))
	c.JSON(http.StatusOK, o.campaignView(c.Request.Context(), *x, true))
}

// Preview: POST /outreach/campaigns/:id/preview {audience}: the counts only.
func (o *Outreach) Preview(c *gin.Context) {
	if !teamOnly(c) {
		return
	}
	var in struct {
		Audience json.RawMessage `json:"audience"`
	}
	_ = c.ShouldBindJSON(&in)
	aud, _ := cleanAudience(in.Audience)
	_, n, err := o.targets(c.Request.Context(), parseAudience(aud))
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "load_failed"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"counts": n})
}

// TestSend: POST /outreach/campaigns/:id/test: the message, with its
// buttons, to the one who pressed (once in 15 seconds).
func (o *Outreach) TestSend(c *gin.Context) {
	if !adminOnly(c) {
		return
	}
	ctx := c.Request.Context()
	x, err := o.repo.CampaignGet(ctx, c.Param("id"))
	if err != nil || x == nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "not_found"})
		return
	}
	me, err := strconv.ParseInt(strings.TrimPrefix(platformUser(c), "tg:"), 10, 64)
	if err != nil || me == 0 {
		me = o.owner
	}
	o.mu.Lock()
	if t, ok := o.tested[x.ID]; ok && o.now().Sub(t) < 15*time.Second {
		o.mu.Unlock()
		c.JSON(http.StatusTooManyRequests, gin.H{"error": "wait", "message": "Тест уже отправлен, подождите 15 секунд"})
		return
	}
	o.tested[x.ID] = o.now()
	o.mu.Unlock()
	var kb map[string]any
	if x.EventID != "" {
		if e, _ := o.repo.EventGet(ctx, x.EventID); e != nil {
			kb = evKB(*e)
		}
	}
	if err := o.send(ctx, me, x.Text, kb); err != nil {
		c.JSON(http.StatusOK, gin.H{"ok": false, "message": "Тест не ушёл: " + tgErrShort(err) + ". Откройте бота и нажмите «Старт»"})
		return
	}
	_ = o.repo.CampaignTested(ctx, x.ID)
	c.JSON(http.StatusOK, gin.H{"ok": true, "message": "Тест отправлен вам в Telegram: так рассылку увидят получатели"})
}

// SendCampaign: POST /outreach/campaigns/:id/send {force}: one click. From
// 10:00 to 20:00 Almaty unless forced; a second click changes nothing.
func (o *Outreach) SendCampaign(c *gin.Context) {
	if !adminOnly(c) {
		return
	}
	ctx := c.Request.Context()
	var in struct {
		Force bool `json:"force"`
	}
	_ = c.ShouldBindJSON(&in)
	x, err := o.repo.CampaignGet(ctx, c.Param("id"))
	if err != nil || x == nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "not_found"})
		return
	}
	if x.Status != "draft" {
		c.JSON(http.StatusConflict, gin.H{"error": "not_draft", "message": "Эта рассылка уже отправляется или отправлена: повторно она не уйдёт", "campaign": o.campaignView(ctx, *x, false)})
		return
	}
	if o.Send == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "no_bot", "message": "Бот не подключён"})
		return
	}
	if h, _ := almatyHour(o.now()); (h < bcFrom || h >= bcUntil) && !in.Force {
		c.JSON(http.StatusConflict, gin.H{"error": "quiet", "message": fmt.Sprintf("Сейчас тихие часы: рассылки уходят с %02d:00 до %02d:00 по Алматы. Отправьте позже или нажмите «Отправить сейчас», если это срочно", bcFrom, bcUntil)})
		return
	}
	list, n, err := o.targets(ctx, parseAudience(x.Audience))
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "load_failed"})
		return
	}
	if len(list) == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "empty", "message": "Получателей нет: выберите аудиторию"})
		return
	}
	ok, err := o.repo.CampaignStart(ctx, x.ID, platformUser(c), in.Force, list)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "store_failed"})
		return
	}
	if !ok {
		c.JSON(http.StatusConflict, gin.H{"error": "not_draft", "message": "Эта рассылка уже отправляется или отправлена: повторно она не уйдёт"})
		return
	}
	log.Printf("outreach: campaign %s started by %s: %d recipients (force=%v)", x.ID, platformUser(c), len(list), in.Force)
	go o.runCampaign(context.Background(), x.ID)
	x, _ = o.repo.CampaignGet(ctx, x.ID)
	v := o.campaignView(ctx, *x, false)
	v["counts"] = n
	c.JSON(http.StatusOK, v)
}

// Register: the routes (team group for the platform, the signed WhatsApp link public).
func (o *Outreach) Register(r *gin.Engine, g *gin.RouterGroup) {
	r.GET("/api/v1/wa/open/:id/:sig", o.WAOpen)
	g.GET("/outreach/wa", o.WAQueue)
	g.POST("/outreach/wa/:id/:op", o.WAMark)
	g.GET("/outreach/channels", o.Channels)
	g.PUT("/outreach/channels", o.PutChannel)
	g.GET("/outreach/events", o.Events)
	g.GET("/outreach/events/:id", o.Event)
	g.PUT("/outreach/events/:id", o.PutEvent)
	g.POST("/outreach/events/:id/address", o.SetAddress)
	g.POST("/outreach/events/:id/participants", o.AddParticipant)
	g.POST("/outreach/events/:id/participants/:who/cancel", o.CancelParticipant)
	g.GET("/outreach/campaigns/:id", o.Campaign)
	g.PUT("/outreach/campaigns/:id", o.PutCampaign)
	g.POST("/outreach/campaigns/:id/preview", o.Preview)
	g.POST("/outreach/campaigns/:id/test", o.TestSend)
	g.POST("/outreach/campaigns/:id/send", o.SendCampaign)
	g.POST("/outreach/segments/act", o.SegmentAct) // R47: outreach_segments.go
}

// OutreachModule mounts the routes behind the platform's login.
type OutreachModule struct {
	O      *Outreach
	Secret []byte
}

func (m *OutreachModule) Register(r *gin.Engine) {
	g := r.Group("/api/v1/platform")
	g.Use(middleware.AuthJWT(m.Secret))
	g.Use(middleware.RequireRole("admin", "moderator", "resident"))
	m.O.Register(r, g)
}
