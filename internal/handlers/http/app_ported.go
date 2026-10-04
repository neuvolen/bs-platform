package http

import (
	"context"
	"encoding/json"
	"fmt"
	"html"
	"log"
	"math"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/bnursik/business_surgery_backend/internal/ai"
	"github.com/bnursik/business_surgery_backend/internal/club"
	"github.com/gin-gonic/gin"
)

// R32d: the app's actions only the Apps Script answered until now, on the
// server, so that nothing depends on the script: the wheel's analysis and
// the SMM draft (the server's AI, rules or an honest refusal without it),
// the channel (ban, publish a post of the SMM plan), the lead magnets (send
// one to the admin to check, swap two files), the team's events and to-do
// list (kept on the server; the app gets a Google Calendar link instead of
// the script's calendar event) and the NPS survey. Only the Threads carousel
// stays with the platform's content factory.

// AppPorted is what these actions need.
type AppPorted struct {
	// TG calls the Bot API (the server's bot).
	TG func(ctx context.Context, method string, params map[string]any) (json.RawMessage, error)
	// AI writes texts (nil or failing: rules, templates).
	AI interface {
		Text(ctx context.Context, system, prompt string) (string, error)
	}
	Meta MetaStore
	// Channel is the club's Telegram channel (@bsurgery_kz).
	Channel string
	// Team: who hears about a new event and the NPS survey.
	Team []int64
}

const (
	metaNPSLast = "nps_last_sent"
	npsEvery    = 30 * 24 * time.Hour
)

// portedOnly: actions the app calls by these names.
var portedActions = map[string]bool{"analyzeWheel": true, "generateSmm": true, "banFromChannel": true, "testLeadmagnet": true,
	"swapLeadmagnetFiles": true, "publishSmm": true, "createEvent": true, "addTodo": true, "runNPS": true, "publishCarousel": true}

// portedCall answers one of those actions; false: not one of them.
func (g *AppGateway) portedCall(c *gin.Context, u *platformTgUser, action string, in url.Values) bool {
	_, team := g.Admins[u.ID]
	if club.PortedTeamOnly[action] && !team {
		c.JSON(http.StatusOK, gin.H{"error": "Только для команды"})
		return true
	}
	if !portedActions[action] {
		return false
	}
	ctx := c.Request.Context()
	p := g.Ported
	if p == nil {
		p = &AppPorted{}
	}
	reply := func(v map[string]any) {
		if _, bad := v["error"]; !bad {
			if _, has := v["ok"]; !has {
				v["ok"] = true
			}
			g.note(true, u.ID)
		}
		c.JSON(http.StatusOK, v)
	}
	if action != "analyzeWheel" && !team {
		reply(map[string]any{"error": "Только для команды"})
		return true
	}
	who := fullName(u)
	if n := g.Admins[u.ID]; n != "" {
		who = n
	}
	write := func(act string, params map[string]string) error {
		if g.Writes == nil {
			return fmt.Errorf("нет данных клуба")
		}
		_, err := g.Writes.Local(ctx, "app", u.ID, who, act, params)
		if err == nil {
			g.dropBundles()
		}
		return err
	}
	tg := func(method string, params map[string]any) error {
		if p.TG == nil {
			return fmt.Errorf("бот недоступен")
		}
		_, err := p.TG(ctx, method, params)
		return err
	}
	switch action {
	case "analyzeWheel":
		reply(p.analyzeWheel(ctx, in))
	case "generateSmm":
		reply(p.generateSmm(ctx, in, len(g.activeResidents(ctx))))
	case "banFromChannel":
		cid, err := strconv.ParseInt(strings.TrimSpace(in.Get("chatId")), 10, 64)
		if err != nil || cid <= 0 {
			reply(map[string]any{"error": "Нет chatId"})
			return true
		}
		if err := tg("banChatMember", map[string]any{"chat_id": p.channel(), "user_id": cid}); err != nil {
			reply(map[string]any{"error": "TG API: " + err.Error()})
			return true
		}
		_ = write("subscriberRemoved", map[string]string{"chatId": strconv.FormatInt(cid, 10)}) // the old list, if it has them
		reply(map[string]any{})
	case "testLeadmagnet":
		key := strings.TrimSpace(in.Get("key"))
		if key == "" {
			reply(map[string]any{"error": "Не определены параметры"})
			return true
		}
		title, file := g.leadmagnet(ctx, key)
		if title == "" && file == "" {
			reply(map[string]any{"error": "Не найден"})
			return true
		}
		if file == "" {
			reply(map[string]any{"error": "fileId пуст"})
			return true
		}
		caption := "🔍 <b>ТЕСТ:</b> " + html.EscapeString(title) + "\n\nПроверь что содержимое PDF соответствует названию. Если нет. используй кнопку 🔄 Поменять местами в Mini App."
		if err := tg("sendDocument", map[string]any{"chat_id": u.ID, "document": file, "caption": caption, "parse_mode": "HTML"}); err != nil {
			reply(map[string]any{"error": "Не удалось отправить"})
			return true
		}
		reply(map[string]any{})
	case "swapLeadmagnetFiles":
		if err := write("swapLeadmagnetFiles", map[string]string{"keyA": in.Get("keyA"), "keyB": in.Get("keyB")}); err != nil {
			reply(map[string]any{"error": err.Error()})
			return true
		}
		reply(map[string]any{})
	case "publishSmm":
		row, err := strconv.Atoi(strings.TrimSpace(in.Get("row")))
		if err != nil || row < 2 {
			reply(map[string]any{"error": "Bad row"})
			return true
		}
		r := g.rawSheet(ctx, club.SheetSmm)
		if row > len(r) {
			reply(map[string]any{"error": "Bad row"})
			return true
		}
		title, text := strings.TrimSpace(cell(r[row-1], 3)), strings.TrimSpace(cell(r[row-1], 4))
		if title == "" && text == "" {
			reply(map[string]any{"error": "Пустой пост"})
			return true
		}
		msg := html.EscapeString(text)
		if title != "" {
			msg = "<b>" + html.EscapeString(title) + "</b>\n\n" + msg
		}
		if err := tg("sendMessage", map[string]any{"chat_id": p.channel(), "text": msg, "parse_mode": "HTML", "disable_web_page_preview": true}); err != nil {
			reply(map[string]any{"error": err.Error()})
			return true
		}
		if err := write("smmPublished", map[string]string{"row": strconv.Itoa(row)}); err != nil {
			log.Printf("publishSmm: mark row %d: %v", row, err)
		}
		reply(map[string]any{})
	case "createEvent":
		name := strings.TrimSpace(in.Get("name"))
		if name == "" {
			name = "Мероприятие"
		}
		typ := strings.TrimSpace(in.Get("type"))
		if typ == "" {
			typ = "Другое"
		}
		date, tm := strings.TrimSpace(in.Get("date")), strings.TrimSpace(in.Get("time"))
		start, ok := appDateTime(date, tm)
		if !ok {
			reply(map[string]any{"error": "Нет даты или времени"})
			return true
		}
		if err := write("addSchedule", map[string]string{"res": "🎬 " + name, "date": date, "time": start.Format("15:04")}); err != nil {
			reply(map[string]any{"error": err.Error()})
			return true
		}
		link := calendarLink("🎬 "+name, "Тип: "+typ+"\nСоздано через BS Mini App", start, start.Add(90*time.Minute))
		note := "🎬 Мероприятие создано\n\n📌 " + name + "\n📅 " + date + " в " + start.Format("15:04") +
			"\n⏱ Длительность: 1.5ч\n\nЕсть в Расписании в Mini App. В календарь: " + link
		for _, id := range p.Team {
			if id != u.ID {
				_ = tg("sendMessage", map[string]any{"chat_id": id, "text": note, "disable_web_page_preview": true})
			}
		}
		reply(map[string]any{"calendarUrl": link})
	case "addTodo":
		text := strings.TrimSpace(in.Get("text"))
		id := fmt.Sprintf("t%d", time.Now().UnixMilli())
		if err := write("addTodo", map[string]string{"text": text, "deadline": in.Get("deadline"), "id": id}); err != nil {
			reply(map[string]any{"error": err.Error()})
			return true
		}
		out := map[string]any{"id": id, "eventId": ""}
		if d, err := time.ParseInLocation("2006-01-02", strings.TrimSpace(in.Get("deadline")), club.Almaty); err == nil {
			s := d.Add(9 * time.Hour)
			out["calendarUrl"] = calendarLink("BS - "+text, "Задача из BS", s, s.Add(30*time.Minute))
		}
		reply(out)
	case "runNPS":
		reply(g.runNPS(ctx, p))
	case "publishCarousel":
		reply(map[string]any{"ok": false, "error": "Карусели в Threads публикует Контент-завод на платформе"})
	}
	return true
}

func (p *AppPorted) channel() string {
	if p.Channel != "" {
		return p.Channel
	}
	return "@bsurgery_kz"
}

func (g *AppGateway) rawSheet(ctx context.Context, name string) [][]string {
	if rs, ok := g.Club.(rawSheets); ok && g.Club != nil {
		if s, err := rs.Sheets(ctx); err == nil {
			return s[name]
		}
	}
	return nil
}

// leadmagnet: title and Telegram file id of a lead magnet by its key.
func (g *AppGateway) leadmagnet(ctx context.Context, key string) (string, string) {
	for i, r := range g.rawSheet(ctx, club.SheetLeadmagnets) {
		if i > 0 && strings.TrimSpace(cell(r, 0)) == key {
			return strings.TrimSpace(cell(r, 1)), strings.TrimSpace(cell(r, 2))
		}
	}
	return "", ""
}

func (g *AppGateway) activeResidents(ctx context.Context) []club.Resident {
	var out []club.Resident
	if g.Writes == nil {
		return nil
	}
	for _, r := range g.Writes.residentsList(ctx) {
		if r.Name != "" && !r.Former && !r.Archived && !r.Admin {
			out = append(out, r)
		}
	}
	return out
}

// appDateTime reads the app's dd.mm.yyyy and HH:MM in Almaty.
func appDateTime(date, tm string) (time.Time, bool) {
	d, ok := club.Date(date)
	if !ok {
		return time.Time{}, false
	}
	hm := strings.Split(tm, ":")
	if len(hm) < 2 {
		return time.Time{}, false
	}
	h, err1 := strconv.Atoi(strings.TrimSpace(hm[0]))
	m, err2 := strconv.Atoi(strings.TrimSpace(hm[1]))
	if err1 != nil || err2 != nil || h < 0 || h > 23 || m < 0 || m > 59 {
		return time.Time{}, false
	}
	return time.Date(d.Year(), d.Month(), d.Day(), h, m, 0, 0, club.Almaty), true
}

// calendarLink: Google Calendar's «add event» page (the script created the
// event itself with its Google access; the server has none).
func calendarLink(title, details string, start, end time.Time) string {
	f := func(t time.Time) string { return t.In(club.Almaty).Format("20060102T150405") }
	q := url.Values{}
	q.Set("action", "TEMPLATE")
	q.Set("text", title)
	q.Set("dates", f(start)+"/"+f(end))
	q.Set("details", details)
	q.Set("ctz", "Asia/Almaty")
	return "https://calendar.google.com/calendar/render?" + q.Encode()
}

// ── the wheel's analysis ──

type wheelChange struct {
	axis     string
	was, now float64
	diff     float64
}

func round1(v float64) float64 { return math.Round(v*10) / 10 }

func numList(s string) []float64 {
	var out []float64
	for _, x := range strings.Split(s, ",") {
		v, _ := strconv.ParseFloat(strings.TrimSpace(x), 64)
		out = append(out, v)
	}
	return out
}

func fmtNum(v float64) string { return strconv.FormatFloat(v, 'f', -1, 64) }

// WheelRules is the analysis without AI, as the script's.
func WheelRules(changes []wheelChange, days int) string {
	var grew, fell, weak []wheelChange
	for _, c := range changes {
		if c.diff > 0 {
			grew = append(grew, c)
		}
		if c.diff < 0 {
			fell = append(fell, c)
		}
		if c.now <= 5 {
			weak = append(weak, c)
		}
	}
	sort.SliceStable(grew, func(i, j int) bool { return grew[i].diff > grew[j].diff })
	sort.SliceStable(fell, func(i, j int) bool { return fell[i].diff < fell[j].diff })
	sort.SliceStable(weak, func(i, j int) bool { return weak[i].now < weak[j].now })
	var out []string
	if len(grew) > 0 {
		var parts []string
		for i, c := range grew {
			if i == 2 {
				break
			}
			parts = append(parts, strings.ToLower(c.axis)+" с "+fmtNum(c.was)+" до "+fmtNum(c.now))
		}
		out = append(out, "РОСТ: "+strings.Join(parts, ", ")+". Это то, во что вложились за "+strconv.Itoa(days)+" дней.")
	} else {
		out = append(out, "РОСТ: за "+strconv.Itoa(days)+" дней ни одна зона не выросла. Значит работа шла вширь, а не вглубь.")
	}
	switch {
	case len(fell) > 0:
		var parts []string
		for i, c := range fell {
			if i == 2 {
				break
			}
			parts = append(parts, strings.ToLower(c.axis)+" упало на "+fmtNum(math.Abs(c.diff)))
		}
		out = append(out, "ПРОСЕДАНИЕ: "+strings.Join(parts, ", ")+". Обычно это цена за рост в другом месте. Проверь, осознанный ли это размен.")
	case len(weak) > 0:
		out = append(out, "ПРОСЕДАНИЕ: "+strings.ToLower(weak[0].axis)+" держится на "+fmtNum(weak[0].now)+" и не двигается. Стоячая зона тормозит остальные.")
	default:
		out = append(out, "ПРОСЕДАНИЕ: явных провалов нет, все зоны выше пяти.")
	}
	var focus wheelChange
	switch {
	case len(weak) > 0:
		focus = weak[0]
	case len(fell) > 0:
		focus = fell[0]
	default:
		all := append([]wheelChange(nil), changes...)
		sort.SliceStable(all, func(i, j int) bool { return all[i].now < all[j].now })
		focus = all[0]
	}
	out = append(out, "ФОКУС: "+strings.ToLower(focus.axis)+" (сейчас "+fmtNum(focus.now)+"). Возьми одну задачу по этой зоне на ближайшие 10 дней и вынеси её на трекинг.")
	return strings.Join(out, "\n")
}

func (p *AppPorted) analyzeWheel(ctx context.Context, in url.Values) map[string]any {
	axes := strings.Split(in.Get("axes"), "|")
	was, now := numList(in.Get("was")), numList(in.Get("now"))
	days, _ := strconv.Atoi(in.Get("days"))
	if strings.TrimSpace(in.Get("axes")) == "" || len(axes) != len(now) || len(was) != len(now) {
		return map[string]any{"error": "Нет данных"}
	}
	var changes []wheelChange
	var sw, sn float64
	for i, a := range axes {
		changes = append(changes, wheelChange{axis: a, was: was[i], now: now[i], diff: round1(now[i] - was[i])})
		sw += was[i]
		sn += now[i]
	}
	kind := "личного баланса"
	if in.Get("kind") == "" || in.Get("kind") == "dna" {
		kind = "ДНК бизнеса"
	}
	if p.AI != nil {
		var lines []string
		for _, c := range changes {
			sign := ""
			if c.diff > 0 {
				sign = "+"
			}
			lines = append(lines, c.axis+": "+fmtNum(c.was)+" → "+fmtNum(c.now)+" ("+sign+fmtNum(c.diff)+")")
		}
		n := float64(len(changes))
		prompt := "Ты бизнес-трекер Business Surgery. Резидент дважды заполнил колесо " + kind + ". Прошло " + strconv.Itoa(days) + " дней.\n\n" +
			"Динамика по осям (было. стало):\n" + strings.Join(lines, "\n") +
			"\n\nСредний балл: " + fmtNum(round1(sw/n)) + " → " + fmtNum(round1(sn/n)) + "\n\n" +
			"Дай краткий разбор на русском, три блока по 1-2 предложения:\nРОСТ: где выросло и что это значит\n" +
			"ПРОСЕДАНИЕ: где упало или стоит и почему это опасно\nФОКУС: одна зона на следующие 10 дней и первый шаг\n\n" +
			"Пиши прямо, без воды, без длинных тире, обращайся на ты. Формат строго: РОСТ: текст, затем ПРОСЕДАНИЕ: текст, затем ФОКУС: текст."
		c, cancel := context.WithTimeout(ctx, 40*time.Second)
		txt, err := p.AI.Text(c, "", prompt)
		cancel()
		if txt = strings.TrimSpace(strings.ReplaceAll(txt, "—", ",")); err == nil && strings.Contains(txt, "ФОКУС") {
			return map[string]any{"text": txt, "source": "ai"}
		}
	}
	return map[string]any{"text": WheelRules(changes, days), "source": "rules"}
}

// ── the SMM draft ──

func (p *AppPorted) generateSmm(ctx context.Context, in url.Values, residents int) map[string]any {
	platform := portedOr(in.Get("platform"), "Instagram")
	format := portedOr(in.Get("format"), "Пост")
	rubric := portedOr(in.Get("rubric"), "Кейс резидента")
	if p.AI == nil {
		return map[string]any{"error": "ИИ на сервере не подключён. Посты готовит Контент-завод на платформе"}
	}
	formatBrief := map[string]string{
		"Пост":     "Один связный пост на 120-180 слов.",
		"Карусель": "Карусель из 7 слайдов. Для каждого слайда короткий заголовок до 6 слов и 1-2 предложения текста. Первый слайд цепляет, последний ведёт на разбор. Отдельно текст под публикацией на 60-90 слов.",
		"Сторис":   "Три коротких экрана сторис, каждый по 1-2 предложения.",
		"Reels":    "Сценарий Reels на 40 секунд: реплики и что показывать в кадре. Первые 3 секунды должны останавливать.",
	}[format]
	if formatBrief == "" {
		formatBrief = "Один пост."
	}
	platformBrief := map[string]string{
		"Instagram": "Instagram: живой разговорный тон, абзацы по 1-2 предложения, без хэштегов.",
		"Threads":   "Threads: коротко и остро, максимум 400 знаков.",
		"Telegram":  "Telegram-канал: спокойный экспертный тон, можно длиннее.",
		"TikTok":    "TikTok: динамично, простые слова, короткие фразы.",
	}[platform]
	shape := `{"title":"короткий заголовок","text":"полный текст"}`
	if format == "Карусель" {
		shape = `{"title":"заголовок","slides":[{"h":"заголовок слайда","t":"текст"}],"caption":"текст под публикацией"}`
	}
	prompt := "Ты пишешь контент для Business Surgery. Это клуб системного трекинга бизнеса в Алматы: разбор бизнеса каждые 10 дней с двумя основателями, Рустамом и Береке. Не курс, не инфобизнес. " +
		"Сейчас " + strconv.Itoa(residents) + " резидентов, проведено больше 700 разборов. Вход через разбор бизнеса за 50 000 тенге: 60 минут, диагностика, план на 10 дней.\n\n" +
		"Рубрика: " + rubric + "\nПлощадка: " + platformBrief + "\nФормат: " + formatBrief + "\n\n" +
		"ТРЕБОВАНИЯ:\nПиши как живой предприниматель, не как маркетолог. Запрещено: прокачай, секреты успеха, гарантированно, хочешь так же, лови подборку. Не используй длинное тире, только точки и запятые. " +
		"Не используй конструкцию не X а Y. Не начинай с вопроса-заманухи, начинай с конкретной ситуации. Конкретика вместо абстракций: цифры, ситуации, реплики. Максимум одна метафора. " +
		"Заканчивай спокойным приглашением на разбор, без давления.\n\nВерни СТРОГО JSON без markdown:\n" + shape
	c, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	txt, err := p.AI.Text(c, "", prompt)
	if err == nil {
		var out map[string]any
		if json.Unmarshal([]byte(ai.JSONFrom(txt)), &out) == nil && (out["text"] != nil || out["slides"] != nil) {
			out["source"] = "ai"
			return out
		}
	}
	return map[string]any{"error": "ИИ не ответил, попробуйте ещё раз"}
}

// ── NPS ──

func (g *AppGateway) runNPS(ctx context.Context, p *AppPorted) map[string]any {
	if p.Meta == nil || p.TG == nil {
		return map[string]any{"error": "Бот недоступен"}
	}
	if v, _ := p.Meta.GetMeta(ctx, metaNPSLast); v != "" {
		if t, err := time.Parse(time.RFC3339, v); err == nil && time.Since(t) < npsEvery {
			return map[string]any{"skipped": true, "message": fmt.Sprintf("Опрос уже отправлялся %d дн. назад, повтор не раньше чем через 30 дней", int(time.Since(t).Hours()/24))}
		}
	}
	text := func(k, def string) string {
		if s, ok := g.Writes.repo.(interface {
			Setting(ctx context.Context, key string) (string, error)
		}); ok {
			if v, _ := s.Setting(ctx, k); strings.TrimSpace(v) != "" {
				return v
			}
		}
		return def
	}
	msg := "📋 Быстрый опрос BS (3 вопроса)\n\n1️⃣ " + text("nps_q1", "Что в Business Surgery самое ценное для вас?") +
		"\n\n2️⃣ " + text("nps_q2", "Что бы вы хотели улучшить или чего вам не хватает?") +
		"\n\n3️⃣ " + text("nps_q3", "Если бы вы рекомендовали BS другу. чем бы его зацепили?") +
		"\n\nОтветьте прямо здесь. нам очень важно ваше мнение!"
	// Mark first: a second tap while sending does not send twice.
	if err := p.Meta.SetMeta(ctx, metaNPSLast, time.Now().UTC().Format(time.RFC3339)); err != nil {
		return map[string]any{"error": "Не удалось сохранить"}
	}
	sent := 0
	for _, r := range g.activeResidents(ctx) {
		if r.TgID == 0 || r.Exception {
			continue
		}
		if _, err := p.TG(ctx, "sendMessage", map[string]any{"chat_id": r.TgID, "text": msg}); err == nil {
			sent++
		}
	}
	for _, id := range p.Team {
		_, _ = p.TG(ctx, "sendMessage", map[string]any{"chat_id": id, "text": fmt.Sprintf("📋 NPS опрос отправлен %d резидентам.\n\nОтветы собираются в «Ответы резидентов» в приложении.", sent)})
	}
	return map[string]any{"sent": sent}
}

func portedOr(v, def string) string {
	if v = strings.TrimSpace(v); v != "" {
		return v
	}
	return def
}
