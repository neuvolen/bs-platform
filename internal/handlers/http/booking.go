package http

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/bnursik/business_surgery_backend/internal/bot"
	"github.com/gin-gonic/gin"
)

// Запись на разбор: экспресс-диагностика, 60 минут с обоими основателями, 50 000 ₸.
//
// Club doc bs_slots = {price, kaspiLink, slots:[{id, start, dur, format,
// place, link, status: free|booked|blocked, booking:{tgId, name, username,
// phone, niche, question, at, leadId, paid, reminded:[]}}]}. Слоты заводит и
// правит команда на платформе; сервер только записывает, отменяет и напоминает.

const (
	slotsDoc    = "bs_slots"
	razborPrice = 50000
	// legacyRazborPrice: цена до октября 2026. Если она осталась в bs_slots,
	// считаем её устаревшей и берём razborPrice.
	legacyRazborPrice = 30000
	slotsAhead        = 21 * 24 * time.Hour
	bookRemindTick    = 5 * time.Minute
)

func parseSlotTime(s string) (time.Time, bool) {
	s = strings.TrimSpace(s)
	for _, l := range []string{time.RFC3339, "2006-01-02T15:04Z07:00"} {
		if t, err := time.Parse(l, s); err == nil {
			return t, true
		}
	}
	for _, l := range []string{"2006-01-02T15:04:05", "2006-01-02T15:04", "2006-01-02 15:04"} {
		if t, err := time.ParseInLocation(l, s, almaty); err == nil {
			return t, true
		}
	}
	return time.Time{}, false
}

var ruMonths = []string{"января", "февраля", "марта", "апреля", "мая", "июня", "июля", "августа", "сентября", "октября", "ноября", "декабря"}
var ruDays = []string{"вс", "пн", "вт", "ср", "чт", "пт", "сб"}

// whenRu: «пн, 5 октября, 11:00» in Almaty time.
func whenRu(t time.Time) string {
	t = t.In(almaty)
	return fmt.Sprintf("%s, %d %s, %s", ruDays[t.Weekday()], t.Day(), ruMonths[t.Month()-1], t.Format("15:04"))
}

func tenge(n int64) string {
	s := strconv.FormatInt(n, 10)
	var b strings.Builder
	for i, r := range s {
		if i > 0 && (len(s)-i)%3 == 0 {
			b.WriteRune(' ')
		}
		b.WriteRune(r)
	}
	return b.String() + " ₸"
}

// slot is a read view of one slot of the doc.
type slot struct {
	m     map[string]any
	ID    string
	Start time.Time
	Dur   int
}

func readSlot(v any) (slot, bool) {
	m, _ := v.(map[string]any)
	if m == nil {
		return slot{}, false
	}
	id, _ := m["id"].(string)
	if id == "" {
		id = fmt.Sprint(m["id"])
		if id == "<nil>" {
			return slot{}, false
		}
	}
	st, _ := m["start"].(string)
	t, ok := parseSlotTime(st)
	if !ok {
		return slot{}, false
	}
	dur := int(anyInt(m["dur"]))
	if dur <= 0 {
		dur = 60
	}
	return slot{m: m, ID: id, Start: t, Dur: dur}, true
}

func (s slot) End() time.Time { return s.Start.Add(time.Duration(s.Dur) * time.Minute) }

func (s slot) booking() map[string]any {
	b, _ := s.m["booking"].(map[string]any)
	return b
}

func (s slot) str(k string) string {
	v, _ := s.m[k].(string)
	return v
}

func (s slot) free() bool {
	st := s.str("status")
	return (st == "free" || st == "") && s.booking() == nil
}

func (s slot) bookedBy(tg int64) bool {
	b := s.booking()
	return b != nil && s.str("status") == "booked" && anyInt(b["tgId"]) == tg
}

// public: what anyone may see of a slot.
func (s slot) public(withLink bool) gin.H {
	out := gin.H{"id": s.ID, "start": s.str("start"), "dur": s.Dur, "format": s.str("format"), "place": s.str("place")}
	if withLink {
		out["link"] = s.str("link")
	}
	return out
}

func slotsPrice(doc map[string]any) (int64, string) {
	p := anyInt(doc["price"])
	if p <= 0 || p == legacyRazborPrice {
		p = razborPrice
	}
	k, _ := doc["kaspiLink"].(string)
	if k == "" {
		k = bot.KaspiLink
	}
	return p, k
}

func (f *LeadFunnel) readSlots(ctx context.Context) (map[string]any, error) {
	doc := map[string]any{}
	d, err := f.docs.GetDoc(ctx, "club", slotsDoc)
	if err != nil {
		return nil, err
	}
	if d != nil && !d.Deleted {
		_ = json.Unmarshal([]byte(d.Value), &doc)
	}
	if doc == nil {
		doc = map[string]any{}
	}
	return doc, nil
}

func mineView(s slot, price int64, kaspi string) gin.H {
	b := s.booking()
	str := func(k string) string { v, _ := b[k].(string); return v }
	return gin.H{"slot": s.public(true), "price": bookingPrice(b, price), "kaspiLink": kaspi, "paid": b["paid"] == true,
		"phone": str("phone"), "niche": str("niche"), "question": str("question"), "at": str("at")}
}

// Slots godoc
// @Summary  Free slots for разбор and the caller's booking
// @Description  Query _tg = initData. Answer {price, kaspiLink, slots:[{id,start,dur,format,place}] (free, next 21 days, sorted), mine: {slot:{id,start,dur,format,place,link}, price, kaspiLink, paid, phone, niche, question, at} | null}.
// @Tags     app
// @Router   /api/v1/app/slots [get]
func (g *AppGateway) Slots(c *gin.Context) {
	u, ok := g.identify(c, c.Query("_tg"))
	if !ok {
		return
	}
	if g.Funnel == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "unavailable"})
		return
	}
	doc, err := g.Funnel.readSlots(c.Request.Context())
	if err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": "storage"})
		return
	}
	now := g.Funnel.now()
	price, kaspi := slotsPrice(doc)
	var free []slot
	var mine any
	list, _ := doc["slots"].([]any)
	for _, v := range list {
		s, ok := readSlot(v)
		if !ok {
			continue
		}
		if s.bookedBy(u.ID) && s.End().After(now) {
			mine = mineView(s, price, kaspi)
		}
		if s.free() && s.Start.After(now) && s.Start.Before(now.Add(slotsAhead)) {
			free = append(free, s)
		}
	}
	sort.Slice(free, func(i, j int) bool { return free[i].Start.Before(free[j].Start) })
	out := make([]gin.H, 0, len(free))
	for _, s := range free {
		out = append(out, s.public(false))
	}
	c.JSON(http.StatusOK, gin.H{"price": price, "kaspiLink": kaspi, "slots": out, "mine": mine})
}

type bookReq struct {
	SlotID   string `json:"slotId"`
	Phone    string `json:"phone"`
	Niche    string `json:"niche"`
	Question string `json:"question"`
	TG       string `json:"_tg"`
}

func clip(s string, n int) string {
	s = strings.TrimSpace(s)
	if r := []rune(s); len(r) > n {
		return string(r[:n])
	}
	return s
}

func readBookReq(c *gin.Context) (bookReq, bool) {
	var r bookReq
	body, _ := io.ReadAll(http.MaxBytesReader(c.Writer, c.Request.Body, 32<<10))
	if json.Unmarshal(body, &r) != nil || strings.TrimSpace(r.SlotID) == "" {
		return r, false
	}
	r.SlotID = clip(r.SlotID, 64)
	r.Phone, r.Niche, r.Question = clip(r.Phone, 40), clip(r.Niche, 200), clip(r.Question, 1000)
	return r, true
}

func tgFrom(c *gin.Context, body string) string {
	if t := c.Query("_tg"); t != "" {
		return t
	}
	return body
}

func bookErr(c *gin.Context, code string) {
	st := http.StatusConflict
	if code == "not_found" {
		st = http.StatusNotFound
	}
	c.JSON(st, gin.H{"error": code})
}

func (f *LeadFunnel) placeLine(s slot) string {
	if strings.Contains(strings.ToLower(s.str("format")), "офлайн") {
		p := s.str("place")
		if p == "" {
			p = "адрес пришлём перед встречей"
		}
		return "📍 Офлайн: " + p
	}
	if l := s.str("link"); l != "" {
		return "💻 Онлайн, ссылка: " + l
	}
	return "💻 Онлайн, ссылку пришлём перед встречей"
}

// isKaspiOpen (R55): a pay.kaspi.kz/pay/… link of the owner opens Kaspi
// without a sum (the same link takes fines of any size): the message says
// which sum to type.
func isKaspiOpen(link string) bool {
	return strings.Contains(link, "pay.kaspi.kz/pay/")
}

// kaspiSum: « (сумму 50 000 ₸ введите в Kaspi)» for an open-amount link.
func kaspiSum(link string, amount int64) string {
	if !isKaspiOpen(link) || amount <= 0 {
		return ""
	}
	return " (сумму " + tenge(amount) + " введите в Kaspi сами)"
}

func (f *LeadFunnel) payKB(s slot, price int64, kaspi string, paid bool) map[string]any {
	var rows [][]map[string]any
	if !paid && kaspi != "" {
		rows = append(rows, row(map[string]any{"text": "💳 Оплатить " + tenge(price) + " (Kaspi)", "url": kaspi}))
	}
	rows = append(rows, row(f.appBtn("🔁 Перенести/отменить", "razbor")))
	return map[string]any{"inline_keyboard": rows}
}

// Book godoc
// @Summary  Book a разбор slot
// @Description  Query _tg = initData. Body {slotId, phone, niche, question}. Answer {ok:true, booking:{slot:{id,start,dur,format,place,link}, price, kaspiLink}} or {error: taken|past|already|not_found} (409; not_found 404).
// @Tags     app
// @Router   /api/v1/app/book [post]
func (g *AppGateway) Book(c *gin.Context) {
	r, okBody := readBookReq(c)
	u, ok := g.identify(c, tgFrom(c, r.TG))
	if !ok {
		return
	}
	if !okBody {
		c.JSON(http.StatusBadRequest, gin.H{"error": "bad_body"})
		return
	}
	f := g.Funnel
	if f == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "unavailable"})
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	code, got, price, kaspi, err := f.BookSlot(ctx, u, r, "приложение")
	if err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": "storage"})
		return
	}
	if code != "" {
		bookErr(c, code)
		return
	}
	c.JSON(http.StatusOK, gin.H{"ok": true, "booking": gin.H{"slot": got.public(true), "price": price, "kaspiLink": kaspi}})
}

// BookSlot books a разбор slot for u: the slot, the CRM card («Записан на
// разбор»), the confirmation with the Kaspi button in the bot and a note to
// the team. via names where it was booked («приложение», «платформа»).
// code is taken|past|already|not_found when nothing was booked.
func (f *LeadFunnel) BookSlot(ctx context.Context, u *platformTgUser, r bookReq, via string) (code string, got slot, price int64, kaspi string, err error) {
	now := f.now()
	name := fullName(u)
	if name == "" {
		name = "Без имени"
	}
	leadID := fmt.Sprintf("tg%d", u.ID)
	if d, err := f.docs.GetDoc(ctx, "club", "bs_crm"); err == nil && d != nil {
		var crm map[string]any
		_ = json.Unmarshal([]byte(d.Value), &crm)
		leads, _ := crm["leads"].([]any)
		if l := findLeadByTg(leads, u.ID); l != nil {
			if id, _ := l["id"].(string); id != "" {
				leadID = id
			}
		}
	}
	err = f.mutateIn(ctx, "club", slotsDoc, "server:booking", func(doc map[string]any) bool {
		code, got = "", slot{}
		price, kaspi = slotsPrice(doc)
		list, _ := doc["slots"].([]any)
		var target slot
		found := false
		for _, v := range list {
			s, ok := readSlot(v)
			if !ok {
				continue
			}
			if s.bookedBy(u.ID) && s.End().After(now) {
				code = "already"
				return false
			}
			if s.ID == r.SlotID {
				target, found = s, true
			}
		}
		switch {
		case !found:
			code = "not_found"
		case !target.Start.After(now):
			code = "past"
		case !target.free():
			code = "taken"
		}
		if code != "" {
			return false
		}
		target.m["status"] = "booked"
		target.m["booking"] = map[string]any{
			"tgId": u.ID, "name": name, "username": u.Username, "phone": r.Phone, "niche": r.Niche,
			"question": r.Question, "at": now.UTC().Format(time.RFC3339), "leadId": leadID, "paid": false, "reminded": []any{},
		}
		got = target
		return true
	})
	if err != nil || code != "" {
		return
	}
	when := whenRu(got.Start)

	// the CRM: the lead goes to «Встреча»
	src, logNew := "Приложение: запись на разбор", "Записался на разбор через приложение"
	if via == "платформа" {
		src, logNew = "Платформа: запись на разбор", "Записался на разбор на платформе"
	}
	_, _, _ = f.ensureLead(ctx, u.ID, u.FirstName, u.LastName, u.Username, src, logNew, "", false)
	_ = f.mutate(ctx, "bs_crm", func(crm map[string]any) bool {
		leads, _ := crm["leads"].([]any)
		lead := findLeadByTg(leads, u.ID)
		if lead == nil {
			return false
		}
		if fmt.Sprint(lead["col"]) != "won" {
			lead["col"] = "meet"
		}
		lead["next"] = "Разбор " + got.Start.In(almaty).Format("15:04")
		lead["nextAt"] = got.Start.In(almaty).Format("2006-01-02")
		lead["razborSlot"] = got.ID
		lead["razborAt"] = got.Start.UTC().Format(time.RFC3339)
		lead["warmStop"] = true // записан: прогрев больше не нужен
		if s, _ := lead["phone"].(string); s == "" && r.Phone != "" {
			lead["phone"] = r.Phone
		}
		if s, _ := lead["niche"].(string); s == "" && r.Niche != "" {
			lead["niche"] = r.Niche
		}
		txt := "Записался на разбор: " + when + " (" + got.str("format") + ")"
		if r.Question != "" {
			txt += ". Вопрос: " + r.Question
		}
		addLog(lead, now, txt)
		return true
	})

	text := "✅ Вы записаны на разбор\n\n📅 " + when + " (время Алматы)\n⏱ " + strconv.Itoa(got.Dur) + " минут с основателями BS\n" + f.placeLine(got) +
		"\n\nСтоимость " + tenge(price) + ". Оплатите через Kaspi по кнопке ниже" + kaspiSum(kaspi, price) + ", чтобы закрепить время.\n\nПеренести или отменить запись можно в приложении."
	if f.send != nil {
		if err := f.send(ctx, u.ID, text, f.payKB(got, price, kaspi, false)); err != nil {
			log.Printf("booking: confirm %d: %v", u.ID, err)
		} else if f.Video != nil {
			// R55: what happens at the разбор, on video (the step «booked»)
			go func(tg int64, first string) {
				vctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
				defer cancel()
				f.sendStepVideo(vctx, tg, "booked", first)
			}(u.ID, u.FirstName)
		}
	}
	who := name
	if u.Username != "" {
		who += " @" + u.Username
	}
	note := fmt.Sprintf("📅 Запись на разбор (%s): %s\n%s, %s\nТелефон: %s\nНиша: %s\nВопрос: %s\n🆔 %d", via, who, when, got.str("format"),
		dash(r.Phone), dash(r.Niche), dash(r.Question), u.ID)
	if f.send != nil {
		for _, a := range f.admins {
			_ = f.send(ctx, a, note, bookPaidKB(got.ID))
		}
	}
	return
}

// BookPaidPrefix: R70: the owner's «Оплата получена» under a booking note:
// the разбор is marked paid as the platform's toggle does, without the platform.
const BookPaidPrefix = "bkp:"

func bookPaidKB(slotID string) map[string]any {
	if slotID == "" || len(BookPaidPrefix+slotID) > 64 {
		return nil
	}
	return kb(row(map[string]any{"text": "💳 Оплата получена", "callback_data": BookPaidPrefix + slotID}))
}

// BookPaidCallback marks the slot's booking paid, logs it on the lead's card
// and tells the lead the time is held.
func (f *LeadFunnel) BookPaidCallback(ctx context.Context, cb bot.CallbackUpdate) (string, bool) {
	if !strings.HasPrefix(cb.Data, BookPaidPrefix) {
		return "", false
	}
	id := strings.TrimPrefix(cb.Data, BookPaidPrefix)
	now := f.now()
	var tg int64
	var when, name string
	already, found := false, false
	err := f.mutateIn(ctx, "club", slotsDoc, "server:booking", func(doc map[string]any) bool {
		list, _ := doc["slots"].([]any)
		for _, v := range list {
			s, ok := readSlot(v)
			if !ok || s.ID != id {
				continue
			}
			b := s.booking()
			if b == nil {
				return false
			}
			found = true
			tg, when, name = anyInt(b["tgId"]), whenRu(s.Start), fmt.Sprint(b["name"])
			if b["paid"] == true {
				already = true
				return false
			}
			b["paid"], b["paidAt"] = true, now.UTC().Format(time.RFC3339)
			return true
		}
		return false
	})
	switch {
	case err != nil:
		return clip("Не получилось: "+err.Error(), 190), true
	case !found:
		return "Запись не найдена (отменена или перенесена)", true
	case already:
		return "Уже отмечено: оплачено", true
	}
	if tg != 0 {
		_ = f.mutate(ctx, "bs_crm", func(crm map[string]any) bool {
			leads, _ := crm["leads"].([]any)
			if l := findLeadByTg(leads, tg); l != nil {
				addLog(l, now, "Разбор оплачен (владелец в боте)")
				return true
			}
			return false
		})
		if f.send != nil {
			_ = f.send(ctx, tg, "✅ Оплата получена, время разбора закреплено: "+when+" (Алматы).\n\nДо встречи! Если планы изменятся, перенести запись можно в приложении.", nil)
		}
	}
	return "Оплачено: " + name, true
}

func dash(s string) string {
	if s == "" {
		return "не указано"
	}
	return s
}

// BookCancel godoc
// @Summary  Cancel the caller's разбор booking
// @Description  Query _tg = initData. Body {slotId}. Answer {ok:true} or {error: not_found|past}.
// @Tags     app
// @Router   /api/v1/app/book/cancel [post]
func (g *AppGateway) BookCancel(c *gin.Context) {
	r, okBody := readBookReq(c)
	u, ok := g.identify(c, tgFrom(c, r.TG))
	if !ok {
		return
	}
	if !okBody {
		c.JSON(http.StatusBadRequest, gin.H{"error": "bad_body"})
		return
	}
	f := g.Funnel
	if f == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "unavailable"})
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	now := f.now()
	var code string
	var got slot
	var bk map[string]any
	err := f.mutateIn(ctx, "club", slotsDoc, "server:booking", func(doc map[string]any) bool {
		code, got, bk = "not_found", slot{}, nil
		list, _ := doc["slots"].([]any)
		for _, v := range list {
			s, ok := readSlot(v)
			if !ok || s.ID != r.SlotID || !s.bookedBy(u.ID) {
				continue
			}
			if !s.Start.After(now) {
				code = "past"
				return false
			}
			code, got, bk = "", s, s.booking()
			s.m["status"] = "free"
			delete(s.m, "booking")
			return true
		}
		return false
	})
	if err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": "storage"})
		return
	}
	if code != "" {
		bookErr(c, code)
		return
	}
	when := whenRu(got.Start)
	_ = f.mutate(ctx, "bs_crm", func(crm map[string]any) bool {
		leads, _ := crm["leads"].([]any)
		lead := findLeadByTg(leads, u.ID)
		if lead == nil {
			return false
		}
		if lead["razborSlot"] == got.ID {
			delete(lead, "razborSlot")
			delete(lead, "razborAt")
			if n, _ := lead["next"].(string); strings.HasPrefix(n, "Разбор") {
				lead["next"], lead["nextAt"] = "", ""
			}
		}
		addLog(lead, now, "Отменил запись на разбор "+when)
		return true
	})
	who := fullName(u)
	if u.Username != "" {
		who += " @" + u.Username
	}
	paid := ""
	if bk != nil && bk["paid"] == true {
		paid = "\nОплата уже была: верните или перенесите."
	}
	for _, a := range f.admins {
		_ = f.send(ctx, a, fmt.Sprintf("❌ Отмена разбора: %s\n%s, слот снова свободен.%s\n🆔 %d", who, when, paid, u.ID), nil)
	}
	c.JSON(http.StatusOK, gin.H{"ok": true})
}

// ── Напоминания ──

func hasMark(b map[string]any, k string) bool {
	l, _ := b["reminded"].([]any)
	for _, x := range l {
		if x == k {
			return true
		}
	}
	return false
}

func addMark(b map[string]any, k string) {
	l, _ := b["reminded"].([]any)
	b["reminded"] = append(l, k)
}

// due24 is when the day-before reminder goes: 24 h before, never at night
// (22:00-09:00 moves to 10:00).
func due24(start time.Time) time.Time {
	d := start.Add(-24 * time.Hour).In(almaty)
	switch h := d.Hour(); {
	case h >= 22:
		n := d.AddDate(0, 0, 1)
		return time.Date(n.Year(), n.Month(), n.Day(), 10, 0, 0, 0, almaty)
	case h < 9:
		return time.Date(d.Year(), d.Month(), d.Day(), 10, 0, 0, 0, almaty)
	}
	return d
}

func dayWord(now, t time.Time) string {
	a, b := now.In(almaty), t.In(almaty)
	da := time.Date(a.Year(), a.Month(), a.Day(), 0, 0, 0, 0, almaty)
	db := time.Date(b.Year(), b.Month(), b.Day(), 0, 0, 0, 0, almaty)
	switch int(db.Sub(da).Hours() / 24) {
	case 0:
		return "сегодня в " + b.Format("15:04")
	case 1:
		return "завтра в " + b.Format("15:04")
	}
	return whenRu(t)
}

type bookJob struct {
	kind  string // 24h, 1h, admin1h, after
	s     slot
	b     map[string]any
	price int64
	kaspi string
}

// RemindOnce sends the due reminders and moves past разборы meet → diag.
func (f *LeadFunnel) RemindOnce(ctx context.Context) int {
	now := f.now()
	var jobs []bookJob
	err := f.mutateIn(ctx, "club", slotsDoc, "server:booking", func(doc map[string]any) bool {
		jobs = nil
		price, kaspi := slotsPrice(doc)
		list, _ := doc["slots"].([]any)
		changed := false
		for _, v := range list {
			s, ok := readSlot(v)
			if !ok || s.str("status") != "booked" || s.booking() == nil {
				continue
			}
			b := s.booking()
			at, perr := time.Parse(time.RFC3339, fmt.Sprint(b["at"]))
			if perr != nil {
				at = time.Time{} // unknown: as if booked long ago
			}
			cp := func() map[string]any { // a copy for sending after the write
				out := map[string]any{}
				for k, x := range b {
					out[k] = x
				}
				return out
			}
			mark := func(kind string, send bool) {
				addMark(b, kind)
				changed = true
				if send {
					jobs = append(jobs, bookJob{kind, s, cp(), price, kaspi})
				}
			}
			d24, h1 := due24(s.Start), s.Start.Add(-time.Hour)
			if !hasMark(b, "24h") && !now.Before(d24) {
				mark("24h", now.Before(h1) && at.Before(d24))
			}
			if !hasMark(b, "1h") && !now.Before(h1) {
				mark("1h", now.Before(s.Start) && at.Before(h1))
			}
			if !hasMark(b, "admin1h") && !now.Before(h1) {
				mark("admin1h", now.Before(s.Start))
			}
			if b["after"] == nil && !now.Before(s.End()) {
				b["after"] = now.UTC().Format(time.RFC3339)
				changed = true
				jobs = append(jobs, bookJob{"after", s, cp(), price, kaspi})
			}
		}
		return changed
	})
	if err != nil {
		log.Printf("booking: reminders: %v", err)
		return 0
	}
	sent := 0
	for _, j := range jobs {
		tg := anyInt(j.b["tgId"])
		paid := j.b["paid"] == true
		name, _ := j.b["name"].(string)
		switch j.kind {
		case "24h", "1h":
			var text string
			if j.kind == "24h" {
				text = "Напоминаю: " + dayWord(now, j.s.Start) + " разбор с основателями BS, " + strconv.Itoa(j.s.Dur) + " минут.\n" + f.placeLine(j.s)
			} else {
				text = "Через час разбор, в " + j.s.Start.In(almaty).Format("15:04") + ".\n" + f.placeLine(j.s)
			}
			if !paid {
				text += "\n\nОплата " + tenge(j.price) + " через Kaspi по кнопке ниже" + kaspiSum(j.kaspi, j.price) + "."
			}
			text += "\n\nЕсли планы изменились, перенесите запись в приложении."
			if err := f.send(ctx, tg, text, f.payKB(j.s, j.price, j.kaspi, paid)); err != nil {
				log.Printf("booking: remind %s %d: %v", j.kind, tg, err)
				continue
			}
			sent++
		case "admin1h":
			pay := "не оплачен"
			if paid {
				pay = "оплачен"
			}
			phone, _ := j.b["phone"].(string)
			q, _ := j.b["question"].(string)
			note := fmt.Sprintf("⏰ Через час разбор: %s, %s\n%s\nТелефон: %s\nВопрос: %s\nОплата: %s", name, j.s.Start.In(almaty).Format("15:04"),
				f.placeLine(j.s), dash(phone), dash(q), pay)
			var keys map[string]any
			if !paid {
				keys = bookPaidKB(j.s.ID)
			}
			for _, a := range f.admins {
				_ = f.send(ctx, a, note, keys)
			}
			sent++
		case "after":
			_ = f.mutate(ctx, "bs_crm", func(crm map[string]any) bool {
				leads, _ := crm["leads"].([]any)
				lead := findLeadByTg(leads, tg)
				if lead == nil || fmt.Sprint(lead["col"]) != "meet" || lead["razborSlot"] != j.s.ID {
					return false // the team moved the card: leave it
				}
				lead["col"] = "diag"
				if n, _ := lead["next"].(string); strings.HasPrefix(n, "Разбор") {
					lead["next"], lead["nextAt"] = "", ""
				}
				addLog(lead, now, "Разбор прошёл ("+whenRu(j.s.Start)+"), карточка переведена в «Диагностика»")
				return true
			})
			if f.AfterRazbor != nil { // R51: the team fills the итоги, the lead's sequence starts
				f.AfterRazbor(ctx, tg, name, whenRu(j.s.Start))
			}
		}
	}
	return sent
}

// RemindLoop checks every 5 minutes.
func (f *LeadFunnel) RemindLoop(ctx context.Context) {
	t := time.NewTicker(bookRemindTick)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		c, cancel := context.WithTimeout(ctx, 2*time.Minute)
		if n := f.RemindOnce(c); n > 0 {
			log.Printf("booking: %d reminders", n)
		}
		cancel()
	}
}
