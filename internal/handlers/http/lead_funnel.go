package http

import (
	"context"
	"encoding/json"
	"fmt"
	"html"
	"io"
	"log"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/bnursik/business_surgery_backend/internal/bot"
	"github.com/bnursik/business_surgery_backend/internal/content"
	pg "github.com/bnursik/business_surgery_backend/internal/repository/pg"
	"github.com/gin-gonic/gin"
)

// Воронка лида на сервере.
//
//  1. Старт в боте: сервер сам здоровается и зовёт в приложение к 99
//     чек-листам, заводит лида в CRM платформы (club doc bs_crm) с источником
//     из ссылки (t.me/bsurgery_bot?start=threads) и сообщает команде.
//  2. Прогрев: через 1, 3, 7 и 14 дней бот пишет лиду, учитывая, что он уже
//     сделал в приложении (открыл ли чек-листы, прошёл ли хоть один).
//  3. Чек-листы: приложение присылает прогресс (POST /app/ckprogress). Всё
//     копится в club doc bs_ck_stats: трекер видит, что внедрил резидент.
//     Лид, прошедший чек-лист до конца, сразу получает приглашение на разбор.

type funnelDocs interface {
	GetDoc(ctx context.Context, scope, key string) (*pg.PlatformDoc, error)
	PutDoc(ctx context.Context, scope, key string, baseVersion int, value string, deleted bool, by string) (*pg.PlatformDoc, error)
}

type LeadFunnel struct {
	docs   funnelDocs
	send   func(ctx context.Context, chatID int64, text string, kb map[string]any) error
	admins []int64
	app    string // the Mini App, ?p=<page>
	now    func() time.Time
	mu     sync.Mutex

	// Photo and Doc send a picture or a file (the bot service); nil in tests means text only.
	Photo func(ctx context.Context, chatID int64, key string, photo []byte, caption string, kb map[string]any) error
	Doc   func(ctx context.Context, chatID int64, key, name string, data []byte, fileID, caption string, kb map[string]any) error
	// R40b (lead_autoreply.go, lead_quiz.go): Edit replaces a message's text
	// and buttons (the in-chat checklist); Meta keeps the autoreply health
	// for /status. Both nil in older tests: a new message, no health.
	Edit func(ctx context.Context, chatID, msgID int64, text string, kb map[string]any) error
	Meta autoMeta
	hmu  sync.Mutex
	// R47 (lead_dialog.go): the bot's messages to leads go to the dialog
	dlgOn bool
	// AfterRazbor: R51: a booked разбор is over (sales_razbor.go asks the team for the итоги).
	AfterRazbor func(ctx context.Context, tg int64, name, when string)
	// R55 (funnel_video.go): the video library and the bot's sendVideo; nil: no videos.
	Videos *FunnelVideos
	Video  VideoSender
}

func NewLeadFunnel(docs funnelDocs, send func(ctx context.Context, chatID int64, text string, kb map[string]any) error, admins []int64) *LeadFunnel {
	return &LeadFunnel{docs: docs, send: send, admins: admins, app: bot.WebAppBase, now: time.Now}
}

var almaty = time.FixedZone("Almaty", 5*3600)

func (f *LeadFunnel) appURL(page string) string { return f.app + "?p=" + page }

func (f *LeadFunnel) appBtn(text, page string) map[string]any {
	return map[string]any{"text": text, "web_app": map[string]string{"url": f.appURL(page)}}
}

func kb(rows ...[]map[string]any) map[string]any {
	return map[string]any{"inline_keyboard": rows}
}

func row(b ...map[string]any) []map[string]any { return b }

// startSource: the label of where the person came from.
func startSource(p string) string {
	m := map[string]string{
		"": "Telegram: бот", "ig": "Instagram профиль", "igbio": "Instagram шапка", "threads": "Threads",
		"tt": "TikTok", "site": "Сайт", "speech": "Выступление", "qr": "QR офлайн",
		"checklists": "Чек-листы", "99": "Чек-листы",
	}
	if l, ok := m[p]; ok {
		return l
	}
	if strings.HasPrefix(p, "pdf_") || strings.HasPrefix(p, "guide_") {
		id := p[strings.Index(p, "_")+1:]
		if t := content.GuideTitle(id); t != "" {
			if strings.HasPrefix(p, "pdf_") {
				return "PDF-гайд: " + t
			}
			return "Гайд: " + t
		}
	}
	if c := PartnerCode(strings.TrimPrefix(p, "pt_")); strings.HasPrefix(p, "pt_") && c != "" { // R38b: partners.go
		return PartnerSource("", c)
	}
	// The manual Threads mode: each post has its own code th_p2610051448 (content_threads_manual.go).
	if l := thPostSource(strings.TrimPrefix(p, "th_")); strings.HasPrefix(p, "th_") && l != "" {
		return l
	}
	// The content engine's links: th_g003, tg_case_isfandiyar, ig_g010 (content_engine.go).
	for pre, l := range map[string]string{"th_": "Threads", "tg_": "Telegram-канал", "ig_": "Instagram"} {
		if rest := strings.TrimPrefix(p, pre); rest != p && rest != "" {
			return l + ": " + content.LinkTitle(rest)
		}
	}
	for pre, l := range map[string]string{"threads_": "Threads", "car_": "Карусель", "ad_": "Реклама", "wa_": "WhatsApp"} {
		if strings.HasPrefix(p, pre) {
			return l + ": " + strings.ReplaceAll(strings.TrimPrefix(p, pre), "_", " ")
		}
	}
	return "Ссылка: " + p
}

// mutate reads a club doc, lets fn change it and writes it back (retrying on conflict).
func (f *LeadFunnel) mutate(ctx context.Context, key string, fn func(doc map[string]any) bool) error {
	for try := 0; try < 5; try++ {
		doc := map[string]any{}
		base := 0
		if d, err := f.docs.GetDoc(ctx, "club", key); err == nil && d != nil {
			base = d.Version
			if !d.Deleted {
				_ = json.Unmarshal([]byte(d.Value), &doc)
			}
			if doc == nil {
				doc = map[string]any{}
			}
		}
		if !fn(doc) {
			return nil
		}
		val, _ := json.Marshal(doc)
		if _, err := f.docs.PutDoc(ctx, "club", key, base, string(val), false, "server:funnel"); err == nil {
			return nil
		}
		time.Sleep(time.Duration(20*(try+1)) * time.Millisecond) // R40b: a short pause, the other writer finishes
	}
	return pg.ErrPlatformConflict
}

func leadTg(m map[string]any) int64 {
	switch v := m["tgId"].(type) {
	case float64:
		return int64(v)
	case string:
		n, _ := strconv.ParseInt(v, 10, 64)
		return n
	}
	return 0
}

func findLeadByTg(leads []any, tg int64) map[string]any {
	for _, l := range leads {
		if m, _ := l.(map[string]any); m != nil && leadTg(m) == tg {
			return m
		}
	}
	return nil
}

func addLog(lead map[string]any, at time.Time, text string) {
	lg, _ := lead["log"].([]any)
	lg = append(lg, map[string]any{"at": at.UTC().Format(time.RFC3339), "text": text})
	if len(lg) > 50 {
		lg = lg[len(lg)-50:]
	}
	lead["log"] = lg
}

// exampleTitles: three guide titles for the welcome, different every day.
func exampleTitles(day int) []string {
	items := content.Guides()
	if len(items) == 0 {
		return nil
	}
	var out []string
	for i := 0; i < 3; i++ {
		out = append(out, items[(day*7+i*33)%len(items)]["title"].(string))
	}
	return out
}

func welcomeText(first string) string {
	var b strings.Builder
	if first != "" {
		b.WriteString("Привет, " + html.EscapeString(first) + "! 👋\n\n")
	} else {
		b.WriteString("Привет! 👋\n\n")
	}
	b.WriteString("Это <b>Business Surgery</b>, клуб бизнес-трекинга в Алматы.\n\n")
	// R40b: сначала польза прямо в чате (проверка на 2 минуты), 99 гайдов остаются в приложении
	b.WriteString("Начнём с быстрой проверки прямо здесь: 6 вопросов да/нет, 2 минуты. В конце покажу, сколько денег бизнес теряет в месяц, и один шаг, который вернёт часть.\n\n")
	b.WriteString("А все <b>99 гайдов</b> с чек-листами лежат в приложении BS, кнопка ниже. Там же библиотека клуба: " + content.ScaleText() + ".")
	return b.String()
}

// ensureLead finds or creates the lead of a Telegram user in the CRM; a new
// lead is announced to the team.
func (f *LeadFunnel) ensureLead(ctx context.Context, chatID int64, first, last, username, src, logNew, logAgain string, quietRepeat bool) (isNew, repeat bool, err error) {
	return f.ensureLeadN(ctx, chatID, first, last, username, src, logNew, logAgain, quietRepeat, true)
}

// ensureLeadN: ensureLead; notify false keeps the team chat quiet (the card
// in the CRM is the only trace).
func (f *LeadFunnel) ensureLeadN(ctx context.Context, chatID int64, first, last, username, src, logNew, logAgain string, quietRepeat, notify bool) (isNew, repeat bool, err error) {
	now := f.now()
	name := strings.TrimSpace(first + " " + last)
	if name == "" {
		name = "Без имени"
	}
	err = f.mutate(ctx, "bs_crm", func(crm map[string]any) bool {
		leads, _ := crm["leads"].([]any)
		lead := findLeadByTg(leads, chatID)
		if lead != nil {
			if t, err := time.Parse(time.RFC3339, fmt.Sprint(lead["lastStart"])); quietRepeat && err == nil && now.Sub(t) < time.Minute {
				repeat = true
				return false
			}
			lead["lastStart"] = now.UTC().Format(time.RFC3339)
			if logAgain != "" {
				addLog(lead, now, logAgain)
			}
			return true
		}
		// A lead the team deleted on the platform is not brought back.
		if del, _ := crm["deleted"].([]any); len(del) > 0 {
			for _, x := range del {
				if fmt.Sprint(x) == fmt.Sprintf("tg%d", chatID) {
					return false
				}
			}
		}
		isNew = true
		tg := ""
		if username != "" {
			tg = "@" + username
		}
		lead = map[string]any{
			"id": fmt.Sprintf("tg%d", chatID), "col": "new", "name": name, "phone": "", "tg": tg,
			"tgId": chatID, "source": src, "niche": "", "note": "", "sum": "",
			"date": now.In(almaty).Format("02.01.2006"), "funnel": "bot",
			"startAt": now.UTC().Format(time.RFC3339), "lastStart": now.UTC().Format(time.RFC3339), "warm": 0,
		}
		addLog(lead, now, logNew)
		crm["leads"] = append([]any{lead}, leads...)
		return true
	})
	if isNew && notify {
		who := name
		if username != "" {
			who += " @" + username
		}
		for _, a := range f.admins {
			_ = f.send(ctx, a, fmt.Sprintf("Новый лид: %s\nИсточник: %s\n🆔 %d\n\nПозван в приложение к гайдам, карточка в CRM платформы.", who, src, chatID), nil)
		}
	}
	return
}

// sendWelcome: one message, a picture showing how to open the app and the app buttons.
func (f *LeadFunnel) sendWelcome(ctx context.Context, chatID int64, first, param string) error {
	top := row(f.appBtn("📘 Открыть 99 гайдов", "checklists"))
	rows := [][]map[string]any{}
	if i := strings.Index(param, "g"); (strings.HasPrefix(param, "guide_") || strings.HasPrefix(param, "pdf_")) && i > 0 {
		if t := content.GuideTitle(param[i:]); t != "" {
			rows = append(rows, row(f.appBtn("📘 "+t, "guide_"+param[i:])))
			top = row(f.appBtn("📚 Все 99 гайдов", "checklists"))
		}
	}
	rows = append(rows, top,
		row(f.appBtn("🔬 Диагностика бизнеса", "diagnostic")),
		row(map[string]any{"text": "✋ Я резидент BS", "callback_data": "i_am_resident"}))
	keys := map[string]any{"inline_keyboard": rows}
	text := welcomeText(first)
	if f.Photo != nil && len(content.HowToApp) > 0 {
		if err := f.Photo(ctx, chatID, "howto_app", content.HowToApp, text, keys); err == nil {
			return nil
		} else {
			log.Printf("funnel: welcome photo %d: %v", chatID, err)
		}
	}
	return f.send(ctx, chatID, strings.NewReplacer("<b>", "", "</b>", "").Replace(text), keys)
}

// HandleStart answers a new person's /start (the hook of the bot service).
func (f *LeadFunnel) HandleStart(ctx context.Context, st bot.StartUpdate) bool {
	if bot.IsRetry(ctx) && f.handledSince(ctx, st.ChatID, st.Date) {
		return true // R40b: the same /start taken again after a restart
	}
	src := startSource(st.Param)
	isNew, repeat, err := f.ensureLead(ctx, st.ChatID, st.FirstName, st.LastName, st.Username, src,
		"Нажал Старт в боте ("+src+")", "Снова нажал Старт в боте ("+src+")", true)
	if err != nil {
		log.Printf("funnel: crm: %v", err)
		f.replyFail(ctx, st.ChatID, "CRM недоступна: "+err.Error())
		return false // the script answers instead
	}
	if repeat {
		f.markHandled(ctx, st.ChatID)
		return true // the second /start within a minute: quiet, like the script
	}
	if err := f.sendWelcome(ctx, st.ChatID, st.FirstName, st.Param); err != nil {
		log.Printf("funnel: welcome %d: %v", st.ChatID, err)
		f.replyFail(ctx, st.ChatID, err.Error())
		return false
	}
	what := "приветствие"
	if isNew { // R32e: чек-лист по боли (lead_pain.go); R40b: дальше проверка прямо в чате (lead_quiz.go)
		if err := f.sendPainAsk(ctx, st.ChatID); err != nil {
			log.Printf("funnel: pain ask %d: %v", st.ChatID, err)
		} else {
			what = "приветствие и выбор чек-листа"
		}
	}
	f.replyOK(ctx, st.ChatID, what)
	return true
}

// Old script buttons (lead magnets) map to the guides.
var lmGuide = map[string]string{"sales": "g015", "unit": "g078", "delegate": "g057", "hire": "g043", "marketing": "g029",
	"cashflow": "g001", "scripts": "g016", "team_culture": "g044", "metrics": "g069", "crisis": "g079"}

// HandleCallback answers the buttons of the script's old lead-magnet menu.
func (f *LeadFunnel) HandleCallback(ctx context.Context, cb bot.CallbackUpdate) bool {
	f.noteCallback(ctx, cb)                         // R47: the press goes to the lead's dialog
	if strings.HasPrefix(cb.Data, leadPainPrefix) { // R32e: lead_pain.go
		return f.painPick(ctx, cb)
	}
	if strings.HasPrefix(cb.Data, quizPrefix) || strings.HasPrefix(cb.Data, quizBookPrefix) { // R40b: lead_quiz.go
		return f.quizCallback(ctx, cb)
	}
	if cb.Data == "sub_leadmagnets" || cb.Data == "sub_menu" {
		_, _, _ = f.ensureLead(ctx, cb.ChatID, cb.FirstName, "", cb.Username, "Telegram: старое меню бота", "Открыл меню материалов в боте", "", false)
		return f.sendWelcome(ctx, cb.ChatID, cb.FirstName, "") == nil
	}
	id := lmGuide[strings.TrimPrefix(cb.Data, "lm_")]
	title := content.GuideTitle(id)
	if title == "" {
		_, _, _ = f.ensureLead(ctx, cb.ChatID, cb.FirstName, "", cb.Username, "Telegram: старое меню бота", "Открыл меню материалов в боте", "", false)
		return f.sendWelcome(ctx, cb.ChatID, cb.FirstName, "") == nil
	}
	_, _, _ = f.ensureLead(ctx, cb.ChatID, cb.FirstName, "", cb.Username, "Гайд: "+title, "Взял PDF «"+title+"» из меню бота", "", false)
	keys := kb(row(f.appBtn("📘 Читать в приложении", "guide_"+id)), row(f.appBtn("📚 Ещё 98 гайдов", "checklists")))
	caption := "📘 " + title + "\n\nВ приложении BS этот гайд удобно читать с телефона и отмечать пункты чек-листа."
	if f.Doc != nil {
		fileID := ""
		if key, ok := content.LeadMagnetKey[id]; ok {
			fileID = leadMagnets[key].FileID
		}
		pdf := content.GuidePDF(id)
		if fileID != "" || pdf != nil {
			if err := f.Doc(ctx, cb.ChatID, "guide_"+id, "BS — "+title+".pdf", pdf, fileID, caption, keys); err == nil {
				_ = f.Downloaded(ctx, cb.ChatID, id, title)
				return true
			} else {
				log.Printf("funnel: lm %s → %d: %v", id, cb.ChatID, err)
			}
		}
	}
	return f.send(ctx, cb.ChatID, caption, keys) == nil
}

// ── Прогресс по чек-листам ──

type ckReport struct {
	ID    string `json:"id"`
	Title string `json:"title"`
	Organ string `json:"organ"`
	Done  int    `json:"done"`
	Total int    `json:"total"`
}

// Progress stores a user's checklist progress; a lead who finished one gets
// an invitation to разбор. role: "resident", "lead" or "team".
func (f *LeadFunnel) Progress(ctx context.Context, tg int64, name, username, role, resident string, r ckReport) error {
	now := f.now()
	finished := r.Total > 0 && r.Done >= r.Total
	nudge := false
	err := f.mutate(ctx, "bs_ck_stats", func(doc map[string]any) bool {
		users, _ := doc["users"].(map[string]any)
		if users == nil {
			users = map[string]any{}
		}
		key := strconv.FormatInt(tg, 10)
		u, _ := users[key].(map[string]any)
		if u == nil {
			u = map[string]any{}
		}
		u["name"], u["role"] = name, role
		if username != "" {
			u["username"] = username
		}
		if resident != "" {
			u["resident"] = resident
		}
		items, _ := u["items"].(map[string]any)
		if items == nil {
			items = map[string]any{}
		}
		it, _ := items[r.ID].(map[string]any)
		if it == nil {
			it = map[string]any{"started": now.UTC().Format(time.RFC3339)}
		}
		it["t"], it["o"], it["d"], it["n"], it["at"] = r.Title, r.Organ, r.Done, r.Total, now.UTC().Format(time.RFC3339)
		if finished && it["finished"] == nil {
			it["finished"] = now.UTC().Format(time.RFC3339)
			if role == "lead" && it["nudged"] == nil {
				last, _ := time.Parse(time.RFC3339, fmt.Sprint(u["nudgedAt"]))
				if now.Sub(last) > 20*time.Hour {
					it["nudged"] = now.UTC().Format(time.RFC3339)
					u["nudgedAt"] = now.UTC().Format(time.RFC3339)
					nudge = true
				}
			}
		}
		items[r.ID] = it
		u["items"] = items
		u["at"] = now.UTC().Format(time.RFC3339)
		users[key] = u
		doc["users"] = users
		return true
	})
	if err != nil {
		return err
	}
	if role != "lead" {
		return nil
	}
	// the lead's card in the CRM: what they do with the checklists
	_ = f.mutate(ctx, "bs_crm", func(crm map[string]any) bool {
		leads, _ := crm["leads"].([]any)
		lead := findLeadByTg(leads, tg)
		if lead == nil {
			return false
		}
		ck, _ := lead["ck"].(map[string]any)
		if ck == nil {
			ck = map[string]any{}
		}
		started, _ := ck["started"].([]any)
		seen := false
		for _, s := range started {
			if s == r.ID {
				seen = true
			}
		}
		if !seen {
			started = append(started, r.ID)
			addLog(lead, now, "Открыл гайд «"+r.Title+"»")
		}
		ck["started"] = started
		ck["last"] = r.Title
		ck["at"] = now.UTC().Format(time.RFC3339)
		if finished {
			done, _ := ck["done"].([]any)
			has := false
			for _, s := range done {
				if s == r.ID {
					has = true
				}
			}
			if !has {
				ck["done"] = append(done, r.ID)
				lead["hot"] = true
				addLog(lead, now, "Прошёл гайд «"+r.Title+"» до конца")
			}
		}
		lead["ck"] = ck
		return true
	})
	if nudge {
		text := fmt.Sprintf("Гайд «%s» пройден ✅\n\nСамое ценное сейчас: понять, какие пункты дадут деньги именно вашему бизнесу и в каком порядке их внедрять.\n\nДля этого есть разбор: час с основателями BS, ваши цифры и план на 10 дней. 50 000 ₸.", r.Title)
		keys := kb(
			row(f.appBtn("📅 Записаться на разбор", "razbor")),
			row(f.appBtn("📘 Следующий гайд", "checklists")),
		)
		if err := f.send(ctx, tg, text, keys); err != nil {
			log.Printf("funnel: nudge %d: %v", tg, err)
		}
		who := name
		if username != "" {
			who += " @" + username
		}
		for _, a := range f.admins {
			_ = f.send(ctx, a, fmt.Sprintf("🔥 Лид %s прошёл гайд «%s» до конца. Бот позвал на разбор, самое время позвонить.", who, r.Title), nil)
		}
	}
	return nil
}

// CkProgress: POST /api/v1/app/ckprogress?_tg=  {id,title,organ,done,total}
func (g *AppGateway) CkProgress(c *gin.Context) {
	u, ok := g.identify(c, c.Query("_tg"))
	if !ok {
		return
	}
	if g.Funnel == nil {
		c.JSON(http.StatusOK, gin.H{"ok": false})
		return
	}
	body, _ := io.ReadAll(http.MaxBytesReader(c.Writer, c.Request.Body, 16<<10))
	var r ckReport
	if json.Unmarshal(body, &r) != nil || r.ID == "" || len(r.ID) > 16 || r.Total <= 0 || r.Total > 100 || r.Done < 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "bad_body"})
		return
	}
	if r.Done > r.Total {
		r.Done = r.Total
	}
	if rr := []rune(r.Title); len(rr) > 200 {
		r.Title = string(rr[:200])
	}
	role, resident := "lead", ""
	if _, admin := g.Admins[u.ID]; admin {
		role = "team"
	} else if g.Boards != nil {
		if n, _, err := g.Boards.ResidentByTg(c.Request.Context(), u.ID); err == nil && n != "" {
			role, resident = "resident", n
		}
	}
	name := strings.TrimSpace(u.FirstName + " " + u.LastName)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if err := g.Funnel.Progress(ctx, u.ID, name, u.Username, role, resident, r); err != nil {
		c.JSON(http.StatusOK, gin.H{"ok": false})
		return
	}
	c.JSON(http.StatusOK, gin.H{"ok": true})
}

// ── Прогрев ──

type warmStep struct {
	day  float64
	text func(first, last string, opened, done int) string
	keys func(f *LeadFunnel) map[string]any
}

func warmSteps() []warmStep {
	open := func(f *LeadFunnel) map[string]any { // R55: the express-разбор is one tap away from the first touch
		return kb(row(f.appBtn("📘 Открыть гайды", "checklists")), row(f.appBtn("🔬 Диагностика бизнеса", "diagnostic")),
			row(f.appBtn("📅 Экспресс-разбор", "razbor")))
	}
	razbor := func(f *LeadFunnel) map[string]any {
		return kb(row(f.appBtn("📅 Записаться на разбор", "razbor")), row(f.appBtn("📘 Гайды", "checklists")))
	}
	hi := func(first string) string {
		if first == "" {
			return ""
		}
		return first + ", "
	}
	// R32b: в каждом касании короткий кейс резидента с цифрами (план маркетинга, действие a2)
	cs := func(t string) string { return "\n\nКейс: " + t }
	return []warmStep{
		{1, func(first, last string, opened, done int) string {
			if opened == 0 {
				return hi(first) + "вы ещё не открыли гайды.\n\nНачните с одного, это 15 минут чтения. Большинство уже с первого гайда находят место, где бизнес теряет деньги каждый месяц." +
					cs("Исфандияр начинал с 200 000 ₸ чистой прибыли, сейчас 2 млн ₸. Первым шагом сменили аудиторию и подняли средний чек.")
			}
			return hi(first) + "вижу, вы открыли «" + last + "». Дошли до конца?\n\nОбычно после первого гайда видно 2-3 места, где теряются деньги. Отметьте пункты чек-листа в приложении, прогресс сохраняется." +
				cs("Исфандияр начинал с 200 000 ₸ чистой прибыли, сейчас 2 млн ₸. Первым шагом сменили аудиторию и подняли средний чек.")
		}, open},
		{3, func(first, last string, opened, done int) string {
			return hi(first) + "одна идея.\n\nЧек-лист показывает, что делать. Диагностика показывает, с чего начать именно вам: за 5 минут видно, какой орган бизнеса болит сильнее остальных.\n\nРезультат сразу в приложении." +
				cs("у Артёма чистая прибыль выросла в 3 раза, а на новую нишу (магазин на Kaspi) он получил грант от государства.")
		}, open},
		{7, func(first, last string, opened, done int) string {
			s := hi(first) + "вопрос по делу.\n\nЧто из гайдов вы уже внедрили?\n\n"
			if done > 0 {
				s = hi(first) + "вы прошли гайдов до конца: " + strconv.Itoa(done) + ". Это больше, чем делает большинство.\n\n"
			}
			return s + "Между «понял» и «сделал» обычно стоит операционка, которая съедает неделю за неделей. Разбор нужен ровно для этого: час, ваши цифры, план на 10 дней. Проводим вдвоём с Береке, 50 000 ₸." +
				cs("у Елены три филиала языкового центра. Операционку забрал операционный директор, собственник вернулся к развитию.")
		}, razbor},
		{10, func(first, last string, opened, done int) string {
			return hi(first) + "ещё один пример из клуба." +
				cs("Даулет делает сайты: плотный рынок, фрилансеры давят цены. За время в клубе доход вырос в 3 раза, появилась своя команда.") +
				"\n\nПотолок чаще задаёт не рынок, а то, как бизнес устроен внутри. На разборе за час видно, что держит именно вас. 50 000 ₸."
		}, razbor},
		{14, func(first, last string, opened, done int) string {
			return hi(first) + "последнее сообщение от меня.\n\nЕсли тема сейчас не актуальна, просто игнорируйте. Если актуальна, но что-то останавливает, напишите одним словом что именно. Отвечу лично.\n\nГайды остаются вашими, они всегда в приложении." +
				cs("у Казбека К9 15-20 млн ₸ чистой прибыли в месяц. Рост на таком масштабе упёрся в оргструктуру и финансы, их и наладили.")
		}, razbor},
	}
}

var warmCols = map[string]bool{"new": true, "work": true, "qual": true}

// WarmOnce sends the due warm-up messages (at most 25 per run).
func (f *LeadFunnel) WarmOnce(ctx context.Context) int {
	now := f.now()
	if h := now.In(almaty).Hour(); h < 10 || h >= 20 {
		return 0
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	stats := map[int64][2]int{} // opened, done
	lastTitle := map[int64]string{}
	if d, err := f.docs.GetDoc(ctx, "club", "bs_ck_stats"); err == nil && d != nil {
		var doc struct {
			Users map[string]struct {
				Items map[string]struct {
					T        string `json:"t"`
					At       string `json:"at"`
					Finished string `json:"finished"`
				} `json:"items"`
			} `json:"users"`
		}
		_ = json.Unmarshal([]byte(d.Value), &doc)
		for k, u := range doc.Users {
			id, _ := strconv.ParseInt(k, 10, 64)
			o, dn, best := 0, 0, ""
			for _, it := range u.Items {
				o++
				if it.Finished != "" {
					dn++
				}
				if it.At > best {
					best, lastTitle[id] = it.At, it.T
				}
			}
			stats[id] = [2]int{o, dn}
		}
	}
	type job struct {
		tg          int64
		stage       int
		first, last string
		claim       bool // the «Я резидент» sequence (resident_claim.go)
		qz          map[string]any
		pain        string
	}
	var jobs []job
	steps := warmSteps()
	csteps := claimSteps()
	vsteps := map[string]bool{}
	if f.Video != nil {
		vsteps = f.Videos.Steps(ctx) // R55: the steps that have a video
	}
	type vjob struct {
		tg    int64
		first string
	}
	var startVids []vjob
	_ = f.mutate(ctx, "bs_crm", func(crm map[string]any) bool {
		leads, _ := crm["leads"].([]any)
		changed := false
		startVids = nil
		for _, l := range leads {
			m, _ := l.(map[string]any)
			if m == nil || !warmCols[fmt.Sprint(m["col"])] || m["warmStop"] == true {
				continue
			}
			if vsteps["start"] && len(startVids) < 25 && startVideoDue(m, now) {
				startVids = append(startVids, vjob{leadTg(m), leadFirst(m)})
			}
			// Wanted to be a resident: its own short sequence goes first.
			if cst, cat, ok := claimWarmState(m); ok {
				if tg := leadTg(m); tg != 0 && cst < len(csteps) && now.Sub(cat).Hours()/24 >= csteps[cst].day {
					jobs = append(jobs, job{tg: tg, stage: cst, first: leadFirst(m), claim: true})
					m["claimWarm"] = cst + 1
					m["warmAt"] = now.UTC().Format(time.RFC3339)
					addLog(m, now, fmt.Sprintf("Прогрев после «Я резидент»: касание %d из %d", cst+1, len(csteps)))
					changed = true
					if len(jobs) >= 25 {
						break
					}
				}
				if cst < len(csteps) {
					continue
				}
			}
			if m["funnel"] != "bot" {
				continue
			}
			// one touch a day at most, whichever sequence sent it
			if t, err := time.Parse(time.RFC3339, fmt.Sprint(m["warmAt"])); err == nil && now.Sub(t) < 20*time.Hour {
				continue
			}
			tg := leadTg(m)
			start, err := time.Parse(time.RFC3339, fmt.Sprint(m["startAt"]))
			if tg == 0 || err != nil {
				continue
			}
			stage := 0
			if v, ok := m["warm"].(float64); ok {
				stage = int(v)
			}
			// R32b: касаний стало 5 (добавлен 10-й день). Кто получил прежние 4, уже прошёл всё
			if m["warmV"] == nil && stage >= 4 {
				stage = len(steps)
			}
			if stage >= len(steps) || now.Sub(start).Hours()/24 < steps[stage].day {
				continue
			}
			qz, _ := m["qz"].(map[string]any)
			jobs = append(jobs, job{tg: tg, stage: stage, first: leadFirst(m), last: lastTitle[tg], qz: qz, pain: fmt.Sprint(m["pain"])})
			m["warm"] = stage + 1
			m["warmV"] = 2
			m["warmAt"] = now.UTC().Format(time.RFC3339)
			addLog(m, now, fmt.Sprintf("Прогрев: касание %d из %d", stage+1, len(steps)))
			changed = true
			if len(jobs) >= 25 {
				break
			}
		}
		return changed
	})
	sent := 0
	var slotLine string
	for _, j := range jobs {
		if j.claim && slotLine == "" {
			slotLine = f.nearestSlotLine(ctx, now)
		}
	}
	for _, j := range jobs {
		st := stats[j.tg]
		var text string
		var keys map[string]any
		if j.claim {
			text, keys = csteps[j.stage].text(j.first, slotLine), csteps[j.stage].keys(f)
		} else if t, k, ok := f.quizNudge(ctx, j.stage, j.first, j.qz, j.pain); ok {
			text, keys = t, k // R40b: день 1 и 3: одна проверка прямо в чате (lead_quiz.go)
		} else {
			s := steps[j.stage]
			text, keys = s.text(j.first, j.last, st[0], st[1]), s.keys(f)
		}
		if !j.claim { // R55: the day's video first, then the touch
			if vs := fvWarmStep(j.stage); vsteps[vs] && f.sendStepVideo(ctx, j.tg, vs, j.first) {
				sent++
				time.Sleep(300 * time.Millisecond)
			}
		}
		if err := f.send(ctx, j.tg, text, keys); err != nil {
			log.Printf("funnel: warm %d: %v", j.tg, err)
			if strings.Contains(err.Error(), "blocked") || strings.Contains(err.Error(), "deactivated") {
				_ = f.mutate(ctx, "bs_crm", func(crm map[string]any) bool {
					leads, _ := crm["leads"].([]any)
					if m := findLeadByTg(leads, j.tg); m != nil {
						m["warmStop"] = true
						addLog(m, now, "Заблокировал бота, прогрев остановлен")
						return true
					}
					return false
				})
			}
			continue
		}
		sent++
		time.Sleep(300 * time.Millisecond)
	}
	// R55: the start video, 20 minutes after /start (the checklists are given by then)
	for _, v := range startVids {
		if f.sendStepVideo(ctx, v.tg, "start", v.first) {
			sent++
			time.Sleep(300 * time.Millisecond)
		}
	}
	return sent
}

// WarmLoop checks every 10 minutes (R55: the start video comes 20-30
// minutes after /start; a touch still goes once a day at most).
func (f *LeadFunnel) WarmLoop(ctx context.Context) {
	t := time.NewTicker(10 * time.Minute)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		c, cancel := context.WithTimeout(ctx, 5*time.Minute)
		if n := f.WarmOnce(c); n > 0 {
			log.Printf("funnel: warm-up sent %d", n)
		}
		cancel()
	}
}

// leadFirst: the first name of a CRM card for a greeting ("" for «Без имени»).
func leadFirst(m map[string]any) string {
	first := strings.Fields(fmt.Sprint(m["name"]))
	if len(first) > 0 && first[0] != "Без" && first[0] != "<nil>" {
		return first[0]
	}
	return ""
}

// PlatformLogin: a lead signed in to the platform with Telegram. A new card
// gets source "platform_login"; an existing one keeps its source and only
// notes the visit (at most twice a day). Nobody is told in the team chat.
func (f *LeadFunnel) PlatformLogin(ctx context.Context, u *platformTgUser) error {
	now := f.now()
	return f.mutate(ctx, "bs_crm", func(crm map[string]any) bool {
		leads, _ := crm["leads"].([]any)
		lead := findLeadByTg(leads, u.ID)
		if lead == nil {
			// A lead the team deleted on the platform is not brought back.
			if del, _ := crm["deleted"].([]any); len(del) > 0 {
				for _, x := range del {
					if fmt.Sprint(x) == fmt.Sprintf("tg%d", u.ID) {
						return false
					}
				}
			}
			name := fullName(u)
			if name == "" {
				name = "Без имени"
			}
			tg := ""
			if u.Username != "" {
				tg = "@" + u.Username
			}
			lead = map[string]any{
				"id": fmt.Sprintf("tg%d", u.ID), "col": "new", "name": name, "phone": "", "tg": tg,
				"tgId": u.ID, "source": "platform_login", "niche": "", "note": "", "sum": "",
				"date": now.In(almaty).Format("02.01.2006"), "funnel": "platform",
				"platformAt": now.UTC().Format(time.RFC3339),
			}
			addLog(lead, now, "Вошёл на платформу app.bxclub.kz через Telegram (лид)")
			crm["leads"] = append([]any{lead}, leads...)
			return true
		}
		prev, perr := time.Parse(time.RFC3339, fmt.Sprint(lead["platformAt"]))
		lead["platformAt"] = now.UTC().Format(time.RFC3339)
		if perr != nil {
			addLog(lead, now, "Вошёл на платформу app.bxclub.kz через Telegram (лид)")
		} else if now.Sub(prev) > 12*time.Hour {
			addLog(lead, now, "Снова зашёл на платформу")
		}
		if s, _ := lead["tg"].(string); s == "" && u.Username != "" {
			lead["tg"] = "@" + u.Username
		}
		return true
	})
}

// Downloaded notes in the lead's card that they took the PDF.
func (f *LeadFunnel) Downloaded(ctx context.Context, tg int64, id, title string) error {
	now := f.now()
	return f.mutate(ctx, "bs_crm", func(crm map[string]any) bool {
		leads, _ := crm["leads"].([]any)
		lead := findLeadByTg(leads, tg)
		if lead == nil {
			return false
		}
		ck, _ := lead["ck"].(map[string]any)
		if ck == nil {
			ck = map[string]any{}
		}
		pdfs, _ := ck["pdf"].([]any)
		for _, x := range pdfs {
			if x == id {
				return false
			}
		}
		ck["pdf"] = append(pdfs, id)
		lead["ck"] = ck
		addLog(lead, now, "Скачал PDF «"+title+"»")
		return true
	})
}
