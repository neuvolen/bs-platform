package http

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"strconv"
	"strings"
	"time"

	"github.com/bnursik/business_surgery_backend/internal/bot"
)

// R40b: автоответ лидам и страховка.
//
//   - Каждый ответ бота лиду отмечается в карточке CRM: botReplyAt (когда),
//     botReply (что ушло), handledAt (последнее обработанное сообщение) и
//     строка в истории «Бот ответил: …». Платформа показывает это на карточке,
//     колонка «Новый» больше не выглядит как «не ответили».
//   - Свободный текст лида (раньше бот его молча пропускал) попадает в
//     карточку, лид получает короткий ответ, команда видит сообщение.
//   - Входящие (bs_bot_inbox) пишутся при получении, до ответа. Если через
//     2 минуты ответа нет, бот один раз шлёт стандартное приветствие и
//     отмечает это в карточке («страховка»).
//   - Здоровье автоответа (последний ответ, сбои за день и причина) лежит в
//     bot_meta autoreply:health и выводится строкой в /status.

const (
	inboxDoc       = "bs_bot_inbox"
	metaAutoReply  = "autoreply:health"
	safetyAfter    = 2 * time.Minute
	safetyMaxAge   = 24 * time.Hour
	leadAckEvery   = 30 * time.Minute
	safetyCooldown = 7 * 24 * time.Hour
)

type autoMeta interface {
	GetMeta(ctx context.Context, key string) (string, error)
	SetMeta(ctx context.Context, key, value string) error
}

// AutoHealth: the autoreply's day (Almaty).
type AutoHealth struct {
	Last     string `json:"last,omitempty"` // RFC3339 of the last reply
	LastWhat string `json:"lastWhat,omitempty"`
	Day      string `json:"day,omitempty"` // 2006-01-02 (Almaty) of the counters
	Fails    int    `json:"fails"`
	Reason   string `json:"reason,omitempty"`
	Net      int    `json:"net"` // the safety net's welcomes today
	Replies  int    `json:"replies"`
}

func (f *LeadFunnel) health(ctx context.Context, fn func(h *AutoHealth)) {
	if f.Meta == nil {
		return
	}
	f.hmu.Lock()
	defer f.hmu.Unlock()
	var h AutoHealth
	if v, err := f.Meta.GetMeta(ctx, metaAutoReply); err == nil && v != "" {
		_ = json.Unmarshal([]byte(v), &h)
	}
	day := f.now().In(almaty).Format("2006-01-02")
	if h.Day != day {
		h.Day, h.Fails, h.Reason, h.Net, h.Replies = day, 0, "", 0, 0
	}
	fn(&h)
	b, _ := json.Marshal(h)
	if err := f.Meta.SetMeta(ctx, metaAutoReply, string(b)); err != nil {
		log.Printf("funnel: autoreply health: %v", err)
	}
}

// ReadAutoHealth: the stored health (zero when none).
func ReadAutoHealth(ctx context.Context, m autoMeta) AutoHealth {
	var h AutoHealth
	if m == nil {
		return h
	}
	if v, err := m.GetMeta(ctx, metaAutoReply); err == nil && v != "" {
		_ = json.Unmarshal([]byte(v), &h)
	}
	return h
}

// replyReason: a Telegram error in a few words.
func replyReason(err string) string {
	low := strings.ToLower(err)
	switch {
	case strings.Contains(low, "blocked"), strings.Contains(low, "deactivated"):
		return "лид заблокировал бота"
	case strings.Contains(low, "parse entities"), strings.Contains(low, "can't parse"):
		return "ошибка разметки текста"
	case strings.Contains(low, "button"), strings.Contains(low, "reply markup"), strings.Contains(low, "web_app"):
		return "ошибка кнопок"
	case strings.Contains(low, "chat not found"):
		return "чат не найден"
	case strings.Contains(low, "too many requests"):
		return "Telegram ограничил частоту"
	case strings.Contains(low, "request failed"), strings.Contains(low, "timeout"), strings.Contains(low, "deadline"):
		return "нет связи с Telegram"
	case strings.Contains(low, "crm"):
		return "CRM недоступна"
	}
	if r := []rune(err); len(r) > 80 {
		err = string(r[:80])
	}
	return err
}

// replyOK marks the reply in the lead's card and in the health.
func (f *LeadFunnel) replyOK(ctx context.Context, chatID int64, what string) {
	now := f.now()
	_ = f.mutate(ctx, "bs_crm", func(crm map[string]any) bool {
		leads, _ := crm["leads"].([]any)
		lead := findLeadByTg(leads, chatID)
		if lead == nil {
			return false
		}
		ts := now.UTC().Format(time.RFC3339)
		lead["botReplyAt"], lead["botReply"], lead["handledAt"] = ts, what, ts
		addLog(lead, now, "Бот ответил: "+what)
		return true
	})
	f.health(ctx, func(h *AutoHealth) {
		h.Last, h.LastWhat = now.UTC().Format(time.RFC3339), what
		h.Replies++
	})
}

// replyFail counts a failed reply (the reason of the last one is kept).
func (f *LeadFunnel) replyFail(ctx context.Context, chatID int64, err string) {
	reason := replyReason(err)
	log.Printf("funnel: autoreply to %d failed: %s", chatID, reason)
	f.health(ctx, func(h *AutoHealth) {
		h.Fails++
		h.Reason = reason
	})
}

// markHandled: the lead's message was taken care of without a new reply.
func (f *LeadFunnel) markHandled(ctx context.Context, chatID int64) {
	now := f.now()
	_ = f.mutate(ctx, "bs_crm", func(crm map[string]any) bool {
		lead := findLeadByTg(asList(crm["leads"]), chatID)
		if lead == nil {
			return false
		}
		if t, err := time.Parse(time.RFC3339, fmt.Sprint(lead["handledAt"])); err == nil && now.Sub(t) < time.Minute {
			return false // the reply just marked it: no second write
		}
		lead["handledAt"] = now.UTC().Format(time.RFC3339)
		return true
	})
}

func asList(v any) []any { l, _ := v.([]any); return l }

func leadUnix(m map[string]any, k string) int64 {
	t, err := time.Parse(time.RFC3339, fmt.Sprint(m[k]))
	if err != nil {
		return 0
	}
	return t.Unix()
}

// handledSince: the lead's message of date was already answered.
func (f *LeadFunnel) handledSince(ctx context.Context, chatID, date int64) bool {
	if date <= 0 {
		return false
	}
	d, err := f.docs.GetDoc(ctx, "club", "bs_crm")
	if err != nil || d == nil || d.Deleted {
		return false
	}
	var crm map[string]any
	_ = json.Unmarshal([]byte(d.Value), &crm)
	lead := findLeadByTg(asList(crm["leads"]), chatID)
	return lead != nil && leadUnix(lead, "handledAt") >= date
}

// StartHook: the bot's /start hook (referrals and partners included); any
// answered /start counts as handled for the safety net.
func (f *LeadFunnel) StartHook(residents refInviter, team map[int64]string) bot.StartHook {
	inner := f.WithReferrals(residents, team)
	return func(ctx context.Context, st bot.StartUpdate) bool {
		if bot.IsRetry(ctx) && f.handledSince(ctx, st.ChatID, st.Date) {
			return true
		}
		ok := inner(ctx, st)
		if ok {
			f.markHandled(ctx, st.ChatID)
		}
		return ok
	}
}

// ── Свободный текст лида ──

func inboundText(in bot.Inbound) string {
	t := in.Text
	if in.Media != "" {
		if t != "" {
			t = "[" + in.Media + "] " + t
		} else {
			t = "[" + in.Media + "]"
		}
	}
	if r := []rune(t); len(r) > 300 {
		t = string(r[:299]) + "…"
	}
	return t
}

func workHours(t time.Time) bool { h := t.In(almaty).Hour(); return h >= 10 && h < 20 }

// HandleLeadText answers a lead's message (not a command).
func (f *LeadFunnel) HandleLeadText(ctx context.Context, in bot.Inbound) bool {
	if bot.IsRetry(ctx) && f.handledSince(ctx, in.ChatID, in.Date) {
		return true
	}
	now := f.now()
	txt := inboundText(in)
	isNew, _, err := f.ensureLead(ctx, in.ChatID, in.FirstName, in.LastName, in.Username, "Telegram: бот",
		"Написал в бот без старта", "", false)
	if err != nil {
		f.replyFail(ctx, in.ChatID, "CRM недоступна: "+err.Error())
		return false
	}
	if isNew { // пришёл не по ссылке: то же, что Старт
		if err := f.sendWelcome(ctx, in.ChatID, in.FirstName, ""); err != nil {
			f.replyFail(ctx, in.ChatID, err.Error())
			return false
		}
		_ = f.sendPainAsk(ctx, in.ChatID)
	}
	ack, qzDone := false, false
	_ = f.mutate(ctx, "bs_crm", func(crm map[string]any) bool {
		lead := findLeadByTg(asList(crm["leads"]), in.ChatID)
		if lead == nil {
			return false
		}
		lead["tgIn"] = txt
		lead["tgInAt"] = now.UTC().Format(time.RFC3339)
		n, _ := lead["tgUnread"].(float64)
		lead["tgUnread"] = n + 1
		lead["handledAt"] = now.UTC().Format(time.RFC3339)
		addLog(lead, now, "Написал в бот: «"+txt+"»")
		if t, err := time.Parse(time.RFC3339, fmt.Sprint(lead["autoAckAt"])); err != nil || now.Sub(t) >= leadAckEvery {
			lead["autoAckAt"] = now.UTC().Format(time.RFC3339)
			ack = true
		}
		if qz, _ := lead["qz"].(map[string]any); qz != nil {
			for _, v := range qz {
				if m, _ := v.(map[string]any); m != nil && m["fin"] != nil {
					qzDone = true
				}
			}
		}
		return true
	})
	if !ack {
		return true
	}
	if !isNew {
		text := "Спасибо, сообщение получил и передал команде. Ответим лично в течение часа."
		if !workHours(now) {
			text = "Спасибо, сообщение получил и передал команде. Ответим лично завтра после 10:00 по Алматы."
		}
		var rows [][]map[string]any
		if !qzDone {
			text += "\n\nПока ждёте: проверка бизнеса прямо здесь, 6 вопросов, 2 минуты."
			rows = append(rows, row(map[string]any{"text": "✅ Пройти проверку", "callback_data": quizPrefix + "menu"}))
		}
		rows = append(rows, row(f.appBtn("📅 Записаться на экспресс-разбор", "razbor")))
		if err := f.send(ctx, in.ChatID, text, kb(rows...)); err != nil {
			f.replyFail(ctx, in.ChatID, err.Error())
		} else {
			f.replyOK(ctx, in.ChatID, "ответ на сообщение")
		}
	} else {
		f.replyOK(ctx, in.ChatID, "приветствие на первое сообщение")
	}
	who := strings.TrimSpace(in.FirstName + " " + in.LastName)
	if who == "" {
		who = "Без имени"
	}
	if in.Username != "" {
		who += " @" + in.Username
	}
	for _, a := range f.admins {
		_ = f.send(ctx, a, fmt.Sprintf("💬 Лид %s написал в бот:\n\n%s\n\nОтветьте ему лично в Telegram. Карточка в CRM платформы.\n🆔 %d", who, txt, in.ChatID), nil)
	}
	return true
}

// ── Входящие и страховка ──

// NoteInbound keeps the message in bs_bot_inbox (the latest per chat).
func (f *LeadFunnel) NoteInbound(ctx context.Context, in bot.Inbound) {
	now := f.now()
	err := f.mutate(ctx, inboxDoc, func(doc map[string]any) bool {
		items, _ := doc["items"].(map[string]any)
		if items == nil {
			items = map[string]any{}
		}
		for k, v := range items { // older than a day: gone
			if m, _ := v.(map[string]any); m == nil || now.Unix()-int64(anyInt(m["at"])) > int64(safetyMaxAge/time.Second) {
				delete(items, k)
			}
		}
		items[strconv.FormatInt(in.ChatID, 10)] = map[string]any{
			"at": in.Date, "first": in.FirstName, "last": in.LastName, "username": in.Username,
			"start": in.Start, "param": in.Param, "text": inboundText(in),
		}
		doc["items"] = items
		return true
	})
	if err != nil {
		log.Printf("funnel: inbox %d: %v", in.ChatID, err)
	}
}

type inboxItem struct {
	chat                   int64
	at                     int64
	first, last, user, prm string
}

// SafetyOnce: a message without an answer for 2 minutes gets the standard
// welcome once; the card says so. Returns how many were sent.
func (f *LeadFunnel) SafetyOnce(ctx context.Context) int {
	now := f.now()
	d, err := f.docs.GetDoc(ctx, "club", inboxDoc)
	if err != nil || d == nil || d.Deleted {
		return 0
	}
	var doc struct {
		Items map[string]map[string]any `json:"items"`
	}
	_ = json.Unmarshal([]byte(d.Value), &doc)
	if len(doc.Items) == 0 {
		return 0
	}
	var due []inboxItem
	for k, m := range doc.Items {
		at := int64(anyInt(m["at"]))
		age := now.Sub(time.Unix(at, 0))
		if age < safetyAfter || age > safetyMaxAge {
			continue
		}
		id, _ := strconv.ParseInt(k, 10, 64)
		due = append(due, inboxItem{chat: id, at: at, first: fmt.Sprint(m["first"]), last: fmt.Sprint(m["last"]), user: fmt.Sprint(m["username"]), prm: fmt.Sprint(m["param"])})
	}
	if len(due) == 0 {
		return 0
	}
	var crm map[string]any
	if cd, err := f.docs.GetDoc(ctx, "club", "bs_crm"); err == nil && cd != nil && !cd.Deleted {
		_ = json.Unmarshal([]byte(cd.Value), &crm)
	}
	var done []string
	sent := 0
	for _, it := range due {
		clean := func(s string) string {
			if s == "<nil>" {
				return ""
			}
			return s
		}
		it.first, it.last, it.user, it.prm = clean(it.first), clean(it.last), clean(it.user), clean(it.prm)
		lead := findLeadByTg(asList(crm["leads"]), it.chat)
		if lead != nil && leadUnix(lead, "handledAt") >= it.at {
			done = append(done, strconv.FormatInt(it.chat, 10))
			continue
		}
		if lead != nil {
			if t, err := time.Parse(time.RFC3339, fmt.Sprint(lead["safetyAt"])); err == nil && now.Sub(t) < safetyCooldown {
				done = append(done, strconv.FormatInt(it.chat, 10))
				continue
			}
		}
		src := startSource(it.prm)
		_, _, _ = f.ensureLead(ctx, it.chat, it.first, it.last, it.user, src, "Написал в бот, ответ не ушёл вовремя ("+src+")", "", false)
		err := f.sendWelcome(ctx, it.chat, it.first, it.prm)
		if err == nil {
			_ = f.sendPainAsk(ctx, it.chat)
		}
		_ = f.mutate(ctx, "bs_crm", func(c map[string]any) bool {
			l := findLeadByTg(asList(c["leads"]), it.chat)
			if l == nil {
				return false
			}
			ts := now.UTC().Format(time.RFC3339)
			l["safetyAt"], l["handledAt"], l["autoFix"] = ts, ts, true
			if err == nil {
				l["botReplyAt"], l["botReply"] = ts, "приветствие (страховка)"
				addLog(l, now, "⚠️ Автоответ не ушёл за 2 минуты: бот отправил приветствие (страховка)")
			} else {
				addLog(l, now, "⚠️ Автоответ не ушёл, страховка тоже не смогла: "+replyReason(err.Error()))
			}
			return true
		})
		if err != nil {
			f.replyFail(ctx, it.chat, err.Error())
		} else {
			sent++
			f.health(ctx, func(h *AutoHealth) {
				h.Net++
				h.Last, h.LastWhat = now.UTC().Format(time.RFC3339), "приветствие (страховка)"
				h.Replies++
			})
			log.Printf("funnel: safety net welcomed %d (no answer for %s)", it.chat, now.Sub(time.Unix(it.at, 0)).Round(time.Second))
		}
		done = append(done, strconv.FormatInt(it.chat, 10))
	}
	if len(done) > 0 {
		_ = f.mutate(ctx, inboxDoc, func(doc map[string]any) bool {
			items, _ := doc["items"].(map[string]any)
			if items == nil {
				return false
			}
			for _, k := range done {
				if m, _ := items[k].(map[string]any); m != nil {
					// a newer message of the same person stays for the next round
					for _, it := range due {
						if strconv.FormatInt(it.chat, 10) == k && int64(anyInt(m["at"])) == it.at {
							delete(items, k)
						}
					}
				}
			}
			doc["items"] = items
			return true
		})
	}
	return sent
}

// SafetyLoop checks the inbox every 30 seconds.
func (f *LeadFunnel) SafetyLoop(ctx context.Context) {
	t := time.NewTicker(30 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		c, cancel := context.WithTimeout(ctx, 2*time.Minute)
		f.SafetyOnce(c)
		cancel()
	}
}

// AutoReplyLine: the /status line «Автоответ: последний HH:MM, сбоев сегодня N (причина)».
func AutoReplyLine(h AutoHealth, now time.Time) (state, text string) {
	state, last := "ok", "ещё не было"
	if t, err := time.Parse(time.RFC3339, h.Last); err == nil {
		l := t.In(almaty)
		if l.Format("2006-01-02") == now.In(almaty).Format("2006-01-02") {
			last = l.Format("15:04")
		} else {
			last = l.Format("02.01 15:04")
		}
	}
	today := now.In(almaty).Format("2006-01-02")
	fails, net, reason := 0, 0, ""
	if h.Day == today {
		fails, net, reason = h.Fails, h.Net, h.Reason
	}
	text = "последний " + last + ", сбоев сегодня " + strconv.Itoa(fails)
	if fails > 0 && reason != "" {
		text += " (" + reason + ")"
	}
	if net > 0 {
		text += ", страховка сработала " + strconv.Itoa(net)
	}
	if fails > 0 || net > 0 {
		state = "warn"
	}
	return state, text
}

// autoreply: the /status line of the bot's autoreply to leads.
func (s *SysCheck) autoreply(ctx context.Context) CheckItem {
	it := CheckItem{Key: "autoreply", Title: "Автоответ"}
	if s.Meta == nil {
		it.State, it.Text = "off", "нет данных"
		return it
	}
	it.State, it.Text = AutoReplyLine(ReadAutoHealth(ctx, s.Meta), s.now())
	return it
}
