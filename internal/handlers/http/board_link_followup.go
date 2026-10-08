package http

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/bnursik/business_surgery_backend/internal/bot"
	"github.com/bnursik/business_surgery_backend/internal/repository/pg"
)

// R58: «Отправить в WhatsApp» и прогрев после ссылки для клиента.
//
// Кнопка в панели ссылки открывает wa.me с телефоном клиента (из карточки лида
// в CRM, иначе трекер вписывает его сам) и готовым сообщением, а сервер
// запоминает отправку и ставит владельцу напоминания в бот (туда же, куда идут
// уведомления продаж):
//
//	+1 день: клиент не открыл доску → «напомните», готовый текст для WhatsApp;
//	+2 дня: открыл, но не нажал «Хочу в клуб» → «спросите, что думает»;
//	+5 дней: последнее напоминание, дальше тишина.
//
// В каждом напоминании цифры доски (открытия, время, разделы, нажатия) и
// кнопка «Написать в WhatsApp» с готовым текстом (одно касание), кнопка «Не
// напоминать». Напоминания останавливаются, когда клиент нажал «Хочу в клуб»,
// лид стал резидентом (этап CRM «won» или есть в списке резидентов), владелец
// нажал «Не напоминать» (в боте или в панели), ссылка отключена или истекла.
// Напоминания не приходят ночью: время сдвигается в окно 10:00-20:00 Алматы.

const (
	fuCallbackPrefix = "blr:"
	fuBatch          = 50
)

// fuOffsets: when each reminder is checked, counted from the send.
var fuOffsets = [...]time.Duration{24 * time.Hour, 48 * time.Hour, 5 * 24 * time.Hour}

// fuWindow moves t into 10:00-20:00 Almaty (the owner is not woken up).
func fuWindow(t time.Time) time.Time {
	a := t.In(almaty)
	switch {
	case a.Hour() < 10:
		return time.Date(a.Year(), a.Month(), a.Day(), 10, 0, 0, 0, almaty)
	case a.Hour() >= 20:
		n := a.AddDate(0, 0, 1)
		return time.Date(n.Year(), n.Month(), n.Day(), 10, 0, 0, 0, almaty)
	}
	return t
}

func (h *BoardLinks) notifyKB(ctx context.Context, text string, kb map[string]any) {
	if h.NotifyKB != nil {
		h.NotifyKB(ctx, text, kb)
		return
	}
	if h.Notify != nil {
		h.Notify(ctx, text)
		return
	}
	if h.S != nil {
		h.S.team(ctx, text, kb)
	}
}

// leadOf: the CRM lead named as the board's client (a copy; nil: none).
func (h *BoardLinks) leadOf(ctx context.Context, b *pg.PlatformBoard) map[string]any {
	if h.S == nil || h.S.docs == nil || b == nil {
		return nil
	}
	names := map[string]bool{}
	for _, n := range []string{b.Resident, clientBoardOf(b).Name} {
		if k := normName(n); k != "" {
			names[k] = true
		}
	}
	var d struct {
		LeadID string `json:"leadId"`
	}
	_ = json.Unmarshal(b.Data, &d)
	crm := h.S.read(ctx, "club", "bs_crm")
	for _, l := range asList(crm["leads"]) {
		m, _ := l.(map[string]any)
		if m == nil {
			continue
		}
		if (d.LeadID != "" && sStr(m, "id") == d.LeadID) || names[normName(sStr(m, "name"))] {
			return m
		}
	}
	return nil
}

// isResident: the board's client already is a resident (the lead is won, or the
// full name is in the club's list and not a former one).
func (h *BoardLinks) isResident(ctx context.Context, b *pg.PlatformBoard, lead map[string]any) bool {
	if lead != nil && sStr(lead, "col") == "won" {
		return true
	}
	if h.S == nil || h.S.Club == nil {
		return false
	}
	name := clientBoardOf(b).Name
	if name == "" {
		name = b.Resident
	}
	if !strings.Contains(strings.TrimSpace(name), " ") {
		return false // one word: «Азат» the lead is not «Азат» the resident
	}
	r, err := h.S.residentByName(ctx, name)
	return err == nil && r != nil && !r.Former
}

func (h *BoardLinks) clientName(b *pg.PlatformBoard) string {
	if n := clientBoardOf(b).Name; n != "" {
		return n
	}
	return b.Name
}

// waText: the first message with the link.
func waFirstText(name, u string) string {
	msg := "Ваш разбор в Business Surgery: доска с диагнозами и планом на 10 дней"
	if fn := firstName(name); fn != "" {
		msg = fn + ", ваш разбор в Business Surgery: доска с диагнозами и планом на 10 дней"
	}
	return msg + "\n" + u
}

// fuText: the follow-up the owner sends the client (stage 1..3).
func fuText(stage int, name, u, intent string, opened bool) string {
	hi := "Добрый день!"
	if fn := firstName(name); fn != "" {
		hi = fn + ", добрый день!"
	}
	switch {
	case stage == 1:
		return hi + " Вчера отправлял ссылку на вашу доску после разбора: там диагнозы и план на 10 дней. Откройте, когда будет минутка, это 5 минут чтения:\n" + u
	case stage == 2 && intent == "ask":
		return hi + " Вы отметили на доске, что есть вопрос. Напишите его сюда или давайте созвонимся на 15 минут: когда удобно, сегодня или завтра?"
	case stage == 2:
		return hi + " Видел, что вы посмотрели доску. Какие мысли? Если что-то в диагнозах или плане непонятно, давайте созвонимся на 15 минут: когда удобно, сегодня или завтра?"
	case !opened:
		return hi + " Последний раз напоминаю о вашей доске после разбора:\n" + u + "\nЕсли сейчас не время, просто напишите, и я не буду беспокоить."
	}
	return hi + " Возвращаюсь к вашему разбору. Решили, как двигаться дальше? Если готовы, начнём с первого шага из плана на этой неделе. Если сейчас не время, просто напишите, и я не буду беспокоить."
}

// fuStats: the board's numbers for the reminder.
func fuStats(l *pg.BoardLink) string {
	var b strings.Builder
	if l.Opens == 0 {
		b.WriteString("Доску не открывал")
	} else {
		fmt.Fprintf(&b, "Открыл %d раз", l.Opens)
		if l.LastOpen != nil {
			b.WriteString(", последний " + l.LastOpen.In(almaty).Format("02.01 15:04"))
		}
		if l.Seconds > 0 {
			m := (l.Seconds + 30) / 60
			if m < 1 {
				fmt.Fprintf(&b, ", на странице %d с", l.Seconds)
			} else {
				fmt.Fprintf(&b, ", на странице %d мин", m)
			}
		}
	}
	secNames := map[string]string{"diag": "диагнозы", "cause": "причины", "strat": "стратегия", "plan": "план", "tools": "инструменты", "club": "клуб", "cases": "кейсы", "price": "цены", "pay": "оплата"}
	var sec []string
	for _, k := range []string{"diag", "cause", "strat", "plan", "tools", "club", "cases", "price", "pay"} {
		if l.Sections[k] > 0 {
			sec = append(sec, secNames[k])
		}
	}
	if len(sec) > 0 {
		b.WriteString("\nСмотрел: " + strings.Join(sec, ", "))
	}
	switch l.Intent {
	case "think":
		b.WriteString("\nНажал «Пока подумаю»")
	case "ask":
		b.WriteString("\nНажал «Есть вопрос»")
	}
	return b.String()
}

// ── команда: отправка и «Не напоминать» ──

// AdminSent: POST /platform/clientlink/:board/sent {phone} → the link (reminders on).
func (h *BoardLinks) AdminSent(c *gin.Context) {
	b, ok := h.board(c)
	if !ok {
		return
	}
	var in struct {
		Phone string `json:"phone"`
	}
	_ = json.NewDecoder(http.MaxBytesReader(c.Writer, c.Request.Body, 2048)).Decode(&in)
	phone := waDigits(in.Phone)
	if phone != "" && (len(phone) < 10 || len(phone) > 15) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "bad_phone", "message": "Проверьте номер: нужен номер WhatsApp с кодом страны"})
		return
	}
	ctx := c.Request.Context()
	now := h.now()
	l, err := h.Store.Live(ctx, b.ID, now)
	if err != nil || l == nil {
		c.JSON(http.StatusConflict, gin.H{"error": "no_link", "message": "Ссылка не действует: создайте новую"})
		return
	}
	lead := h.leadOf(ctx, b)
	if phone == "" && lead != nil {
		phone = waDigits(sStr(lead, "phone"))
	}
	if h.isResident(ctx, b, lead) {
		// резиденту ссылка и прогрев не нужны: отправка без напоминаний
		_ = h.Store.Sent(ctx, l.ID, phone, now, now)
		_, _ = h.Store.StopFollowups(ctx, l.ID, "", "resident", now)
	} else if err := h.Store.Sent(ctx, l.ID, phone, now, fuWindow(now.Add(fuOffsets[0]))); err != nil {
		log.Printf("clientlink: sent %s: %v", l.ID, err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "sent_failed"})
		return
	}
	if in.Phone != "" {
		h.mutateLeadOf(ctx, b, func(ld map[string]any) bool {
			if sStr(ld, "phone") == "" {
				ld["phone"] = "+" + phone
			}
			addLog(ld, now, "Ссылка на доску отправлена в WhatsApp")
			return true
		})
	} else {
		h.mutateLeadOf(ctx, b, func(ld map[string]any) bool {
			addLog(ld, now, "Ссылка на доску отправлена в WhatsApp")
			return true
		})
	}
	l, _ = h.Store.Get(ctx, l.ID)
	c.JSON(http.StatusOK, gin.H{"link": h.viewFor(ctx, b, l, lead)})
}

// AdminStopRemind: DELETE /platform/clientlink/:board/remind → «Не напоминать».
func (h *BoardLinks) AdminStopRemind(c *gin.Context) {
	b, ok := h.board(c)
	if !ok {
		return
	}
	ctx := c.Request.Context()
	if _, err := h.Store.StopFollowups(ctx, "", b.ID, "owner", h.now()); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "stop_failed"})
		return
	}
	l, _ := h.Store.Latest(ctx, b.ID)
	c.JSON(http.StatusOK, gin.H{"link": h.viewFor(ctx, b, l, nil)})
}

// RemindCallback: the bot's «Не напоминать» (callback blr:<link id>, team only).
func (h *BoardLinks) RemindCallback(ctx context.Context, cb bot.CallbackUpdate) (string, bool) {
	if !strings.HasPrefix(cb.Data, fuCallbackPrefix) {
		return "", false
	}
	id := strings.TrimPrefix(cb.Data, fuCallbackPrefix)
	if !boardLinkIDRe.MatchString(id) {
		return "Ссылка не найдена", true
	}
	n, err := h.Store.StopFollowups(ctx, id, "", "owner", h.now())
	if err != nil {
		return "Не получилось, попробуйте ещё раз", true
	}
	if n == 0 {
		return "Напоминаний по этой доске уже нет", true
	}
	return "Больше не напомню об этой доске", true
}

// ── цикл напоминаний ──

// Loop checks due reminders every few minutes.
func (h *BoardLinks) Loop(ctx context.Context) {
	t := time.NewTicker(3 * time.Minute)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		c, cancel := context.WithTimeout(ctx, 2*time.Minute)
		h.FollowupTick(c)
		cancel()
	}
}

// FollowupTick sends the reminders that are due (tests call it with a fixed clock).
func (h *BoardLinks) FollowupTick(ctx context.Context) int {
	now := h.now()
	due, err := h.Store.Due(ctx, now, fuBatch)
	if err != nil {
		log.Printf("clientlink: followups: %v", err)
		return 0
	}
	sent := 0
	for _, l := range due {
		if h.followup(ctx, l, now) {
			sent++
		}
	}
	return sent
}

func (h *BoardLinks) followup(ctx context.Context, l *pg.BoardLink, now time.Time) bool {
	stop := func(why string) { _, _ = h.Store.StopFollowups(ctx, l.ID, "", why, now) }
	switch {
	case l.Revoked:
		stop("revoked")
		return false
	case !l.ExpiresAt.After(now):
		stop("expired")
		return false
	case l.Intent == "join":
		stop("join")
		return false
	case l.SentAt == nil || l.FuStage >= len(fuOffsets):
		stop("done")
		return false
	}
	b, err := h.Boards.GetBoard(ctx, l.BoardID)
	if err != nil {
		return false // try again on the next tick
	}
	if b == nil || b.Deleted {
		stop("revoked")
		return false
	}
	lead := h.leadOf(ctx, b)
	if h.isResident(ctx, b, lead) {
		stop("resident")
		return false
	}
	stage := l.FuStage + 1 // 1..3
	var next *time.Time
	if stage < len(fuOffsets) {
		t := fuWindow(l.SentAt.Add(fuOffsets[stage]))
		if !t.After(now) {
			t = fuWindow(now.Add(time.Hour))
		}
		next = &t
	}
	opened := l.Opens > 0
	send := true
	switch stage {
	case 1:
		send = !opened // открыл в первый день: «не открыл» не нужно
	case 2:
		send = opened // не открыл: напоминание уже было на первый день
	}
	// сначала ход вперёд: при двух серверах сообщение уходит один раз
	moved, err := h.Store.Advance(ctx, l.ID, l.FuStage, stage, next)
	if err != nil || !moved {
		return false
	}
	if next == nil {
		_, _ = h.Store.StopFollowups(ctx, l.ID, "", "done", now)
	}
	if !send {
		return false
	}
	name := h.clientName(b)
	u := boardLinkURL(l.ID)
	phone := l.WAPhone
	if phone == "" && lead != nil {
		phone = waDigits(sStr(lead, "phone"))
	}
	text := fuText(stage, name, u, l.Intent, opened)
	head := map[int]string{1: "⏰ Прогрев: " + name + " не открыл доску (1 день)", 2: "⏰ Прогрев: " + name + " посмотрел доску, решения нет (2 дня)", 3: "⏰ Последнее напоминание: " + name + " (5 дней)"}[stage]
	msg := head + "\n" + fuStats(l) + "\n\nГотовый текст:\n" + text
	if phone == "" {
		msg += "\n\nТелефона нет: скопируйте текст или добавьте номер в карточку лида"
	}
	if next == nil {
		msg += "\n\nБольше напоминаний по этой доске не будет"
	}
	var btns [][]map[string]any
	if phone != "" {
		btns = append(btns, row(map[string]any{"text": "💬 Написать в WhatsApp", "url": WALink(phone, text)}))
	}
	btns = append(btns, row(map[string]any{"text": "Открыть доску клиента", "url": u}),
		row(map[string]any{"text": "🔕 Не напоминать", "callback_data": fuCallbackPrefix + l.ID}))
	h.notifyKB(ctx, msg, kb(btns...))
	h.mutateLeadOf(ctx, b, func(ld map[string]any) bool {
		addLog(ld, now, fmt.Sprintf("Напоминание владельцу о доске (%d из 3)", stage))
		return true
	})
	return true
}

// ── вид для панели ──

type boardLinkFollowup struct {
	Stage  int        `json:"stage"`
	NextAt *time.Time `json:"nextAt,omitempty"`
	Stop   string     `json:"stop"`
	StopAt *time.Time `json:"stopAt,omitempty"`
	SentAt *time.Time `json:"sentAt,omitempty"`
}

// viewFor: the link with the WhatsApp phone and the reminders (lead may be nil: read here).
func (h *BoardLinks) viewFor(ctx context.Context, b *pg.PlatformBoard, l *pg.BoardLink, lead map[string]any) *boardLinkView {
	v := h.view(l, clientBoardOf(b).Name)
	if v == nil {
		return nil
	}
	if lead == nil {
		lead = h.leadOf(ctx, b)
	}
	phone := l.WAPhone
	if phone == "" && lead != nil {
		phone = waDigits(sStr(lead, "phone"))
	}
	v.WAText = waFirstText(h.clientName(b), v.URL)
	if phone != "" {
		v.Phone = "+" + phone
		v.WA = WALink(phone, v.WAText)
	}
	v.Resident = h.isResident(ctx, b, lead)
	if lead != nil {
		v.LeadID = sStr(lead, "id")
	}
	if l.SentAt != nil {
		v.Followup = &boardLinkFollowup{Stage: l.FuStage, NextAt: l.FuNextAt, Stop: l.FuStop, StopAt: l.FuStopAt, SentAt: l.SentAt}
	}
	return v
}
