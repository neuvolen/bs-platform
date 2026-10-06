package bot

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/bnursik/business_surgery_backend/internal/club"
	pg "github.com/bnursik/business_surgery_backend/internal/repository/pg"
)

// The bot after the cutover (club.SheetMode off or mirror): the server answers
// everything the Apps Script answered in private chats. Group reports are
// judged and logged on arrival (shadow_run.go); the lead funnel, the claims
// and the team's buttons keep their hooks (start_hook.go). What is left lands
// here: the residents' and the team's commands, the offer acceptance, a
// resident's free text (CustDev), a payment note, and the old buttons of the
// script's menus, which now open the app.

// FineSink keeps the daily fines in the club tables; it returns the names
// whose fine was added (a fine already there for that day is not added).
type FineSink func(ctx context.Context, fines []FineRow) ([]string, error)

// NoteSink keeps a private event as a club write (the offer acceptance, a
// resident's message): action and its parameters, as the app's section
// writes (club.SectionWriteActions).
type NoteSink func(ctx context.Context, tg int64, who, action string, params map[string]string) error

// CustdevSink is the NoteSink the private chat uses.
type CustdevSink = NoteSink

func (s *Service) SetFineSink(f FineSink) { s.mu.Lock(); s.fineSink = f; s.mu.Unlock() }
func (s *Service) SetNoteSink(f NoteSink) { s.mu.Lock(); s.custdev = f; s.mu.Unlock() }
func (s *Service) SetPlatformURL(u string) {
	s.mu.Lock()
	s.platform = strings.TrimRight(u, "/")
	s.mu.Unlock()
}
func (s *Service) noteSink() NoteSink { s.mu.RLock(); defer s.mu.RUnlock(); return s.custdev }
func (s *Service) PublicURL() string  { return s.publicURL }
func (s *Service) platformURL(ctx context.Context) string {
	if v, _ := s.repo.Setting(ctx, "platform_url"); strings.HasPrefix(strings.TrimSpace(v), "https://") {
		return strings.TrimRight(strings.TrimSpace(v), "/")
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.platform != "" {
		return s.platform
	}
	return "https://app.bxclub.kz"
}

// Offer texts, as the script's POLICY_TEXT and versions.
const (
	PolicyText = "📋 Перед использованием сервиса необходимо ознакомиться с документами:\n\n" +
		"📄 Публичная оферта. https://bxclub.kz/oferta\n🔒 Политика конфиденциальности. https://bxclub.kz/privacy\n\n" +
		"Нажимая кнопку «✅ Принять и продолжить», вы:\n• подтверждаете ознакомление с условиями Оферты\n• принимаете условия Договора\n" +
		"• даете согласие на обработку персональных данных\n• соглашаетесь с электронным способом заключения договора\n" +
		"• подтверждаете, что ваши действия являются аналогом собственноручной подписи"
	OfferVersion   = "1.0"
	PrivacyVersion = "1.0"
	// SheetAccepts: who accepted the offer (the script's «Акцепты»).
	SheetAccepts = "Акцепты"
)

// ensureWebhook points Telegram at the server once it is the bot. Idempotent:
// nothing is changed when the webhook is already there; updates waiting in
// Telegram's queue are kept.
func (s *Service) ensureWebhook(ctx context.Context) {
	if s.publicURL == "" || !s.Enabled() {
		log.Printf("bot: webhook not checked (PUBLIC_URL is empty)")
		return
	}
	want := s.publicURL + "/api/v1/bot/webhook"
	for try := 0; try < 3 && ctx.Err() == nil; try++ {
		c, cancel := context.WithTimeout(ctx, 20*time.Second)
		info, err := s.GetWebhookInfo(c)
		if err == nil && info.URL == want {
			cancel()
			return
		}
		if err == nil {
			err = s.SetWebhook(c, want)
		}
		cancel()
		if err == nil {
			log.Printf("bot: webhook set to the server (%s, was %q)", want, info.URL)
			return
		}
		log.Printf("bot: webhook: %v", err)
		select {
		case <-ctx.Done():
			return
		case <-time.After(15 * time.Second):
		}
	}
}

// handleOwn answers one update on the server. At most once: an update taken
// again after a restart is not answered twice.
func (s *Service) handleOwn(ctx context.Context, u *pg.BotUpdate) {
	defer func() {
		if r := recover(); r != nil {
			log.Printf("bot: update %d: panic %v", u.UpdateID, r)
		}
		if e := s.repo.MarkRelayed(context.WithoutCancel(ctx), u.UpdateID, 204, 0); e != nil {
			log.Printf("bot: mark %d: %v", u.UpdateID, e)
		}
	}()
	if time.Since(u.Received) > time.Hour {
		return // a command older than an hour is not run (as the script did)
	}
	if u.Tries > 1 && !s.leadRetry(ctx, u) {
		return // R40b: a lead's message after a restart is still answered (lead_inbound.go)
	}
	c, cancel := context.WithTimeout(ctx, 90*time.Second)
	defer cancel()
	if u.Tries > 1 {
		c = WithRetry(c)
	}
	switch u.Kind {
	case "message":
		if !s.takeStart(c, u.Body) {
			s.private(c, u.Body)
		}
	case "callback_query":
		if !s.takeCallback(c, u.Body) {
			s.privateCallback(c, u.Body)
		}
	}
}

type privMsg struct {
	ChatID    int64
	ChatType  string
	FromID    int64
	FirstName string
	LastName  string
	Username  string
	Text      string
	Thread    int64
}

func (m privMsg) name() string { return strings.TrimSpace(m.FirstName + " " + m.LastName) }

func readPrivMsg(body []byte) (privMsg, bool) {
	var u struct {
		Message *struct {
			Text   string `json:"text"`
			Thread int64  `json:"message_thread_id"`
			Chat   struct {
				ID   int64  `json:"id"`
				Type string `json:"type"`
			} `json:"chat"`
			From *struct {
				ID        int64  `json:"id"`
				IsBot     bool   `json:"is_bot"`
				FirstName string `json:"first_name"`
				LastName  string `json:"last_name"`
				Username  string `json:"username"`
			} `json:"from"`
		} `json:"message"`
	}
	if json.Unmarshal(body, &u) != nil || u.Message == nil || u.Message.From == nil || u.Message.From.IsBot {
		return privMsg{}, false
	}
	m := u.Message
	return privMsg{ChatID: m.Chat.ID, ChatType: m.Chat.Type, FromID: m.From.ID, FirstName: m.From.FirstName,
		LastName: m.From.LastName, Username: m.From.Username, Text: strings.TrimSpace(m.Text), Thread: m.Thread}, true
}

// command: "/start@bsurgery_bot x" → "/start".
func command(t string) string {
	if !strings.HasPrefix(t, "/") {
		return ""
	}
	c := strings.Fields(t)[0]
	if i := strings.Index(c, "@"); i > 0 {
		c = c[:i]
	}
	return strings.ToLower(c)
}

// resident is the club's row for a Telegram id: an active one wins.
func (s *Service) resident(ctx context.Context, tg int64) (club.Resident, bool) {
	list, err := s.repo.Club().LoadResidents(ctx)
	if err != nil {
		return club.Resident{}, false
	}
	var found *club.Resident
	for i := range list {
		r := &list[i]
		if r.TgID != tg || r.Name == "" || r.Archived {
			continue
		}
		if found == nil || (found.Former && !r.Former) {
			found = r
		}
	}
	if found == nil {
		return club.Resident{}, false
	}
	return *found, true
}

func firstName(n string) string {
	if f := strings.Fields(n); len(f) > 0 {
		return f[0]
	}
	return n
}

// Money is "10 000".
func Money(n int64) string {
	neg := n < 0
	if neg {
		n = -n
	}
	s := strconv.FormatInt(n, 10)
	var b strings.Builder
	for i, c := range s {
		if i > 0 && (len(s)-i)%3 == 0 {
			b.WriteByte(' ')
		}
		b.WriteRune(c)
	}
	if neg {
		return "-" + b.String()
	}
	return b.String()
}

func kb(rows ...[]map[string]any) map[string]any {
	return map[string]any{"inline_keyboard": rows}
}

func appBtn(text, page string) []map[string]any {
	u := WebAppBase
	if page != "" {
		u = webApp(page)
	}
	return []map[string]any{{"text": text, "web_app": map[string]string{"url": u}}}
}

func (s *Service) platformBtn(ctx context.Context) []map[string]any {
	return []map[string]any{{"text": "💻 Платформа BS", "web_app": map[string]string{"url": s.platformURL(ctx)}}}
}

// accepted: the resident accepted the offer (on the server, or in the
// script's «Акцепты» that came over with the last import).
func (s *Service) accepted(ctx context.Context, tg int64) bool {
	if v, _ := s.repo.GetMeta(ctx, "accept:"+strconv.FormatInt(tg, 10)); v != "" {
		return true
	}
	rows, ok, err := s.repo.Club().RawSheet(ctx, SheetAccepts)
	if err != nil || !ok {
		return false
	}
	id := strconv.FormatInt(tg, 10)
	for _, r := range rows {
		if len(r) > 1 && strings.TrimSpace(r[1]) == id {
			return true
		}
	}
	return false
}

var payWords = []string{"оплатил", "оплатила", "оплатили", "оплата", "заплатил", "перевел", "перевёл", "перевела", "оплачено"}

// private answers a message in a private chat (and /chatid anywhere).
func (s *Service) private(ctx context.Context, body []byte) {
	m, ok := readPrivMsg(body)
	if !ok {
		return
	}
	cmd := command(m.Text)
	switch cmd {
	case "/chatid":
		t := fmt.Sprintf("🆔 Chat ID: %d", m.ChatID)
		if m.Thread != 0 {
			t += fmt.Sprintf(" | Topic ID: %d", m.Thread)
		}
		_ = s.SendMessage(ctx, m.ChatID, t)
		return
	case "/topicid":
		_ = s.SendMessage(ctx, m.ChatID, fmt.Sprintf("📌 Topic ID: %d\nGroup: %d", m.Thread, m.ChatID))
		return
	}
	if m.ChatType != "private" {
		return
	}
	if phone := readContact(body); phone != "" {
		if s.takeContact(ctx, body) { // R38c: a phone for an event's RSVP
			return
		}
		s.contactNote(ctx, m, phone)
		return
	}
	admin := s.isAdmin(m.FromID)
	res, isRes := s.resident(ctx, m.FromID)
	active := isRes && !res.Former
	switch cmd {
	case "/start", "/menu":
		switch {
		case admin:
			s.adminMenu(ctx, m.ChatID)
		case active && !s.accepted(ctx, m.FromID):
			_ = s.SendMessageKB(ctx, m.ChatID, strings.Replace(PolicyText, "{имя}", firstName(res.Name), 1),
				kb([]map[string]any{{"text": "✅ Принять и продолжить", "callback_data": "accept_terms"}}))
		case active:
			_ = s.SendMessageKB(ctx, m.ChatID, "🧬 "+firstName(res.Name)+", с возвращением в Business Surgery!\n\n"+
				"Отчёты, встречи, штрафы и прогресс: в приложении BS (кнопка ниже).\n"+
				"Шаблон отчёта: /help · Ваш баланс: /status", kb(appBtn("📱 Открыть в BS", ""), s.platformBtn(ctx)))
		default:
			_ = s.SendMessageKB(ctx, m.ChatID, "🧬 Добро пожаловать в Business Surgery!\n\n"+
				"В приложении: 99 чек-листов для владельца бизнеса и запись на разбор.",
				kb(appBtn("📱 Открыть приложение", "")))
		}
		return
	case "/help":
		today := time.Now().In(club.Almaty).Format("02.01")
		_ = s.SendMessage(ctx, m.ChatID, "📋 Шаблон отчёта Business Surgery:\n\n"+today+"\n"+firstName(m.name())+
			"\n\nТочка А:\nТочка Б:\n\nЦель: _____ тг\n\nИз старых задач осталось:\n\nЗадачи на 10 дней:\n\n"+
			"Ежедневные задачи:\n\nОтчёт за день:\n\nПлан на завтра:\n\n❌ Штраф за пропуск: 10 000 тг")
		return
	case "/status":
		if admin && s.systemStatus(ctx, m.ChatID, statusFull(m.Text)) { // R36: the team gets the system check (R51: «/status подробно»)
			return
		}
		_ = s.SendMessage(ctx, m.ChatID, s.statusText(ctx, m.FromID))
		return
	case "/platform", "/платформа":
		if !admin && !isRes {
			_ = s.SendMessage(ctx, m.ChatID, "Платформа открыта для резидентов клуба. Если вы резидент, напишите куратору: он проверит ваш Telegram в списке резидентов.")
			return
		}
		_ = s.SendMessageKB(ctx, m.ChatID, "📱 Платформа Business Surgery\n\nВаш разбор, задачи и замеры. Открывается прямо здесь.\n\nС компьютера: "+
			s.platformURL(ctx), kb([]map[string]any{{"text": "Открыть платформу", "web_app": map[string]string{"url": s.platformURL(ctx)}}}))
		return
	}
	if admin {
		if s.takeTrend(ctx, m, body) { // R53: a Threads link → «Тренды Threads» (trends_hook.go)
			return
		}
		switch cmd {
		case "/version":
			_ = s.SendMessage(ctx, m.ChatID, "🤖 Бот работает на сервере платформы.\nТаблица: "+sheetModeName(ctx)+
				"\nВерсия скрипта таблицы (выключен): "+LatestScript)
		case "/check":
			_ = s.SendMessageKB(ctx, m.ChatID, s.todayText(ctx), kb(appBtn("📋 Отчёты в BS", "reports")))
		case "/fines":
			_ = s.SendMessageKB(ctx, m.ChatID, s.finesText(ctx), kb(appBtn("⚠️ Штрафы в BS", "fines")))
		case "/residents":
			_ = s.SendMessageKB(ctx, m.ChatID, s.residentsText(ctx), kb(appBtn("👥 Резиденты в BS", "")))
		case "/help_admin", "/помощь":
			_ = s.SendMessage(ctx, m.ChatID, "Команды команды:\n/menu: панель\n/check: кто сдал отчёт сегодня\n"+
				"/fines: неоплаченные штрафы\n/residents: резиденты и долги\n/status: проверка системы\n/version: где работает бот\n"+
				"/trend ссылка: чужой пост Threads в «Тренды Threads» (или просто пришлите ссылку)\n\n"+
				"Штрафы, оплаты, расписание и резиденты: в приложении BS и на платформе.")
		}
		return
	}
	if !isRes && cmd == "" {
		// R40b: a lead's message used to end here without an answer; now the
		// funnel answers it, keeps it in the CRM card and tells the team.
		s.leadText(ctx, body)
		return
	}
	if cmd != "" || !isRes {
		return
	}
	low := strings.ToLower(m.Text)
	for _, w := range payWords {
		if strings.Contains(low, w) {
			s.paymentNote(ctx, m, res)
			return
		}
	}
	if len([]rune(m.Text)) >= 30 {
		// CustDev: a resident's message to the bot is kept for the team (the
		// script's «NPS ответы»), without an answer, as before.
		if sink := s.noteSink(); sink != nil {
			if err := sink(ctx, m.FromID, res.Name, "addCustdevResponse", map[string]string{
				"name": res.Name, "tg": strconv.FormatInt(m.FromID, 10), "text": m.Text}); err != nil {
				log.Printf("bot custdev %d: %v", m.FromID, err)
			}
		}
	}
}

func sheetModeName(ctx context.Context) string {
	switch club.ScriptMode(ctx) {
	case club.SheetModeMirror:
		return "копия раз в час, только для просмотра"
	case club.SheetModeLegacy:
		return "главная (аварийный режим)"
	}
	return "отключена"
}

// paymentNote: a resident says he paid. The script closed a fine on the
// word alone; the server does not touch money without a check: the team is
// told once, the resident gets an answer.
func (s *Service) paymentNote(ctx context.Context, m privMsg, res club.Resident) {
	if !s.once(ctx, "paynote:"+strconv.FormatInt(m.FromID, 10), 10*time.Minute) {
		return
	}
	_ = s.SendMessage(ctx, m.ChatID, "Спасибо! Команда проверит оплату и отметит её в приложении.")
	txt := "💰 " + res.Name + " пишет об оплате:\n\n" + m.Text + "\n\nПроверьте поступление и отметьте оплату в приложении."
	for _, id := range s.admins {
		_ = s.SendMessageKB(ctx, id, txt, kb(appBtn("💰 Открыть в BS", "fines")))
	}
}

func (s *Service) adminMenu(ctx context.Context, chat int64) {
	_ = s.SendMessageKB(ctx, chat, "🏥 BS. Панель управления\n\nШтрафы, оплаты, расписание и резиденты ведутся в приложении BS и на платформе.\n"+
		"Быстро: /check отчёты сегодня · /fines штрафы · /residents долги · /status проверка системы",
		kb(appBtn("📱 Открыть в BS", ""), appBtn("📋 Отчёты", "reports"), appBtn("⚠️ Штрафы", "fines"), s.platformBtn(ctx)))
}

func (s *Service) statusText(ctx context.Context, tg int64) string {
	snap, err := s.repo.Club().Load(ctx)
	if err != nil {
		return "Не получилось открыть данные, попробуйте через минуту."
	}
	for _, d := range club.Debet(snap.Residents, snap.Fines) {
		if d.TgID != tg || d.Archived {
			continue
		}
		t := "📊 " + d.Name + "\n💰 Общий долг: " + Money(d.TotalDebt) + " тг\n⚠️ Штрафы: " + Money(d.FinesUnpaid) + " тг\n"
		if d.MeetingsLeft > 0 {
			t += fmt.Sprintf("🗓 Осталось встреч: %d\n", d.MeetingsLeft)
		}
		if d.TotalDebt > 0 {
			return t + "❗ Есть задолженность"
		}
		return t + "✅ Всё в порядке!"
	}
	return "Вы не найдены в системе. Обратитесь к куратору."
}

func (s *Service) todayText(ctx context.Context) string {
	a := time.Now().In(club.Almaty)
	day := time.Date(a.Year(), a.Month(), a.Day(), 0, 0, 0, 0, club.Almaty)
	snap, err := s.repo.Club().Load(ctx)
	if err != nil {
		return "Не получилось открыть данные."
	}
	rows, _ := s.repo.ServerReports(ctx, day)
	var rep []DayReport
	for _, x := range rows {
		rep = append(rep, DayReport{TgID: x.TgID, Name: x.Name})
	}
	var want []club.Resident
	for _, r := range snap.Residents {
		if r.Name != "" && !r.Former && !r.Exception && !r.Admin && !r.Archived {
			want = append(want, r)
		}
	}
	missing := EveningTargets(want, rep)
	var names []string
	for _, r := range missing {
		names = append(names, "  • "+r.Name)
	}
	t := fmt.Sprintf("📋 Отчёты за %s\n\nСдали: %d из %d", day.Format("02.01.2006"), len(want)-len(missing), len(want))
	if len(names) > 0 {
		t += "\n\nЕщё не сдали:\n" + strings.Join(names, "\n")
	}
	return t
}

func (s *Service) finesText(ctx context.Context) string {
	snap, err := s.repo.Club().Load(ctx)
	if err != nil {
		return "Не получилось открыть данные."
	}
	sum := map[string]int64{}
	var total int64
	for _, f := range snap.Fines {
		if !f.Paid {
			sum[f.Name] += f.Amount
			total += f.Amount
		}
	}
	if len(sum) == 0 {
		return "✅ Неоплаченных штрафов нет"
	}
	var names []string
	for n := range sum {
		names = append(names, n)
	}
	sort.Strings(names)
	t := "⚠️ Неоплаченные штрафы: " + Money(total) + " тг\n"
	for _, n := range names {
		t += "\n  • " + n + ": " + Money(sum[n]) + " тг"
	}
	return t
}

func (s *Service) residentsText(ctx context.Context) string {
	snap, err := s.repo.Club().Load(ctx)
	if err != nil {
		return "Не получилось открыть данные."
	}
	var lines []string
	var debt int64
	n := 0
	for _, d := range club.Debet(snap.Residents, snap.Fines) {
		if d.Archived || d.Admin || d.Name == "" {
			continue
		}
		n++
		debt += d.TotalDebt
		l := "  • " + d.Name
		if d.TotalDebt > 0 {
			l += ": долг " + Money(d.TotalDebt) + " тг"
		}
		lines = append(lines, l)
	}
	sort.Strings(lines)
	return fmt.Sprintf("👥 Резиденты: %d\n💰 Общий долг: %s тг\n\n%s", n, Money(debt), strings.Join(lines, "\n"))
}

// privateCallback answers a button the hooks did not take: the offer, and
// the old buttons of the script's menus (they open the app now).
func (s *Service) privateCallback(ctx context.Context, body []byte) {
	var u struct {
		Callback *struct {
			ID   string `json:"id"`
			Data string `json:"data"`
			From struct {
				ID        int64  `json:"id"`
				FirstName string `json:"first_name"`
				LastName  string `json:"last_name"`
				Username  string `json:"username"`
			} `json:"from"`
			Message *struct {
				Chat struct {
					ID   int64  `json:"id"`
					Type string `json:"type"`
				} `json:"chat"`
			} `json:"message"`
		} `json:"callback_query"`
	}
	if json.Unmarshal(body, &u) != nil || u.Callback == nil {
		return
	}
	cb := u.Callback
	answer := func(text string) {
		p := map[string]any{"callback_query_id": cb.ID}
		if text != "" {
			p["text"] = text
		}
		_, _ = s.call(ctx, "answerCallbackQuery", p)
	}
	if cb.Message == nil || cb.Message.Chat.Type != "private" {
		answer("")
		return
	}
	chat := cb.Message.Chat.ID
	if cb.Data == "accept_terms" {
		answer("")
		s.acceptTerms(ctx, chat, cb.From.ID, strings.TrimSpace(cb.From.FirstName+" "+cb.From.LastName), cb.From.Username)
		return
	}
	answer("Это теперь в приложении BS")
	if s.isAdmin(cb.From.ID) {
		s.adminMenu(ctx, chat)
		return
	}
	if !s.once(ctx, "oldbtn:"+strconv.FormatInt(cb.From.ID, 10), time.Minute) {
		return
	}
	_ = s.SendMessageKB(ctx, chat, "Всё нужное теперь в приложении BS: откройте его кнопкой ниже.", kb(appBtn("📱 Открыть в BS", "")))
}

// acceptTerms records the offer acceptance once and starts the onboarding.
func (s *Service) acceptTerms(ctx context.Context, chat, tg int64, name, username string) {
	if !s.once(ctx, "accept_guard:"+strconv.FormatInt(tg, 10), time.Minute) {
		return
	}
	res, isRes := s.resident(ctx, tg)
	when := time.Now().In(club.Almaty)
	if !s.accepted(ctx, tg) {
		_ = s.repo.SetMeta(ctx, "accept:"+strconv.FormatInt(tg, 10), when.UTC().Format(time.RFC3339))
		if sink := s.noteSink(); sink != nil {
			if err := sink(ctx, tg, name, "acceptTerms", map[string]string{"tg": strconv.FormatInt(tg, 10),
				"username": username, "name": name, "offer": OfferVersion, "privacy": PrivacyVersion}); err != nil {
				log.Printf("bot accept %d: %v", tg, err)
			}
		}
	}
	date := when.Format("02.01.2006 15:04")
	if isRes && !res.Former {
		_ = s.SendMessageKB(ctx, chat, "✅ Условия приняты\n\nДата: "+date+"\nВерсия: "+OfferVersion+"\n\nДобро пожаловать в Business Surgery 🧬\n\n"+
			"💻 Платформа BS: "+strings.TrimPrefix(s.platformURL(ctx), "https://")+"\nВаши разборы, задачи, отчёты и прогресс. Удобнее открывать с ноутбука, вход через Telegram.",
			kb(appBtn("📱 Открыть в BS", ""), s.platformBtn(ctx)))
		s.StartOnboarding(ctx, tg, res.Name)
		return
	}
	_ = s.SendMessageKB(ctx, chat, "✅ Условия приняты\n\nДата: "+date+"\nВерсия оферты: "+OfferVersion+"\nВерсия политики: "+PrivacyVersion+
		"\n\n🧬 Добро пожаловать в Business Surgery", kb(appBtn("📱 Открыть приложение", "")))
}

// SendTopic posts text into a topic of a group (message_thread_id).
func (s *Service) SendTopic(ctx context.Context, chat, thread int64, text string) error {
	p := map[string]any{"chat_id": chat, "text": text, "disable_web_page_preview": true}
	if thread != 0 {
		p["message_thread_id"] = thread
	}
	_, err := s.call(ctx, "sendMessage", p)
	return err
}

// RequestContact asks a person for the phone number with Telegram's own button.
func (s *Service) RequestContact(ctx context.Context, chat int64, text string) error {
	_, err := s.call(ctx, "sendMessage", map[string]any{"chat_id": chat, "text": text,
		"reply_markup": map[string]any{"keyboard": [][]map[string]any{{{"text": "📱 Отправить мой номер", "request_contact": true}}},
			"resize_keyboard": true, "one_time_keyboard": true}})
	return err
}

// readContact: the phone a person shared with Telegram's button.
func readContact(body []byte) string {
	var u struct {
		Message *struct {
			Contact *struct {
				Phone string `json:"phone_number"`
			} `json:"contact"`
		} `json:"message"`
	}
	if json.Unmarshal(body, &u) != nil || u.Message == nil || u.Message.Contact == nil {
		return ""
	}
	return strings.TrimSpace(u.Message.Contact.Phone)
}

// contactNote: the lead shared the phone (the app's «Отправить номер»): the
// team gets it once, the person gets an answer and the keyboard goes away.
func (s *Service) contactNote(ctx context.Context, m privMsg, phone string) {
	if !strings.HasPrefix(phone, "+") {
		phone = "+" + phone
	}
	_, _ = s.call(ctx, "sendMessage", map[string]any{"chat_id": m.ChatID, "text": "✅ Спасибо, номер получен. Мы свяжемся с вами.",
		"reply_markup": map[string]any{"remove_keyboard": true}})
	if !s.once(ctx, "contact:"+strconv.FormatInt(m.FromID, 10), 24*time.Hour) {
		return
	}
	who := m.name()
	if m.Username != "" {
		who += " @" + m.Username
	}
	for _, id := range s.admins {
		_ = s.SendMessage(ctx, id, fmt.Sprintf("📞 %s поделился номером: %s\n🆔 %d", who, phone, m.FromID))
	}
}
