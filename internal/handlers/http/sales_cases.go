package http

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"math"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/bnursik/business_surgery_backend/internal/bot"
	"github.com/bnursik/business_surgery_backend/internal/tplpdf"
	"github.com/gin-gonic/gin"
)

// R51 (2): «Кейсы «было → стало» автоматически из замеров резидентов (с их
// согласия)».
//
// Согласие (club doc bs_case_consent = {people: {<normName>: {name, mode,
// at, by, reqAt, reqSent}}}): mode anon | name | no, по умолчанию «нельзя».
// Резидент меняет его в профиле (платформа и приложение), команда может
// попросить его через бота: кнопки «Анонимно», «С именем», «Нельзя» (cs:).
//
// Кейсы (club doc bs_cases = {items: [...]}): «Собрать черновики» берёт
// каждого резидента с согласием и данными (начало в клубе → сейчас:
// выручка, прибыль, часы собственника, индекс здоровья, закрытые
// диагнозы), пишет короткую историю по шаблону (ИИ по кнопке, если
// подключён) и кладёт черновик. Команда утверждает и публикует. Анонимный
// кейс показывает только нишу, город и цифры. Опубликованный кейс с
// действующим согласием идёт в напоминание дня 5 (sales_razbor.go) и на
// открытую страницу /about. Экспорт: пост Threads, текст карусели, блок сайта.

const (
	consentDoc = "bs_case_consent"
	casesDoc   = "bs_cases"
	csPrefix   = "cs:"
)

var consentModes = map[string]string{"anon": "анонимно", "name": "с именем", "no": "нельзя"}

// ── согласие ──

func (s *ClubSales) consents(ctx context.Context) map[string]map[string]any {
	out := map[string]map[string]any{}
	for k, v := range sMap(s.read(ctx, "club", consentDoc), "people") {
		if m, _ := v.(map[string]any); m != nil {
			out[k] = m
		}
	}
	return out
}

// ConsentOf: the resident's mode ("no" by default).
func (s *ClubSales) ConsentOf(ctx context.Context, name string) string {
	if m := s.consents(ctx)[normName(name)]; m != nil {
		if md := sStr(m, "mode"); consentModes[md] != "" {
			return md
		}
	}
	return "no"
}

func (s *ClubSales) setConsent(ctx context.Context, name, mode, by string) error {
	if consentModes[mode] == "" || strings.TrimSpace(name) == "" {
		return fmt.Errorf("bad mode")
	}
	now := s.now()
	err := s.mutate(ctx, "club", consentDoc, func(doc map[string]any) bool {
		people := sMap(doc, "people")
		if people == nil {
			people = map[string]any{}
		}
		k := normName(name)
		m, _ := people[k].(map[string]any)
		if m == nil {
			m = map[string]any{}
		}
		hist, _ := m["hist"].([]any)
		hist = append(hist, map[string]any{"mode": mode, "at": rfc(now), "by": by})
		if len(hist) > 20 {
			hist = hist[len(hist)-20:]
		}
		m["name"], m["mode"], m["at"], m["by"], m["hist"] = strings.TrimSpace(name), mode, rfc(now), by, hist
		delete(m, "reqAt")
		delete(m, "reqSent")
		people[k] = m
		doc["people"] = people
		return true
	})
	if err != nil {
		return err
	}
	if mode == "no" || mode == "anon" {
		s.syncPublic(ctx) // a withdrawn or anonymised consent changes /about at once
	}
	return nil
}

// ConsentList: GET /sales/consent → every current resident with their mode.
func (s *ClubSales) ConsentList(c *gin.Context) {
	ctx := c.Request.Context()
	all := s.consents(ctx)
	out := []gin.H{}
	seen := map[string]bool{}
	if s.Club != nil {
		if list, err := s.Club.LoadResidents(ctx); err == nil {
			for _, r := range activeResidents(list) {
				k := normName(r.Name)
				seen[k] = true
				m := all[k]
				md := sStr(m, "mode")
				if consentModes[md] == "" {
					md = "no"
				}
				out = append(out, gin.H{"name": r.Name, "mode": md, "at": sStr(m, "at"), "by": sStr(m, "by"), "reqAt": sStr(m, "reqAt"), "reqSent": m["reqSent"] == true, "tg": r.TgID != 0})
			}
		}
	}
	for k, m := range all {
		if !seen[k] {
			out = append(out, gin.H{"name": sStr(m, "name"), "mode": sStr(m, "mode"), "at": sStr(m, "at"), "by": sStr(m, "by"), "former": true})
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return fmt.Sprint(out[i]["name"]) < fmt.Sprint(out[j]["name"]) })
	c.JSON(http.StatusOK, gin.H{"people": out})
}

type consentReq struct {
	Name string `json:"name"`
	Mode string `json:"mode"`
}

// PutConsent: PUT /sales/consent {name, mode} (the team writes down the resident's word).
func (s *ClubSales) PutConsent(c *gin.Context) {
	var r consentReq
	if c.ShouldBindJSON(&r) != nil || consentModes[r.Mode] == "" || strings.TrimSpace(r.Name) == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "bad_request"})
		return
	}
	if err := s.setConsent(c.Request.Context(), r.Name, r.Mode, "команда "+platformUser(c)); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "storage"})
		return
	}
	s.ConsentList(c)
}

// RequestConsent: POST /sales/consent/request {name}: the bot asks (in the window).
func (s *ClubSales) RequestConsent(c *gin.Context) {
	var r consentReq
	if c.ShouldBindJSON(&r) != nil || strings.TrimSpace(r.Name) == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "bad_request"})
		return
	}
	ctx := c.Request.Context()
	now := s.now()
	_ = s.mutate(ctx, "club", consentDoc, func(doc map[string]any) bool {
		people := sMap(doc, "people")
		if people == nil {
			people = map[string]any{}
		}
		k := normName(r.Name)
		m, _ := people[k].(map[string]any)
		if m == nil {
			m = map[string]any{"name": strings.TrimSpace(r.Name), "mode": "no"}
		}
		m["reqAt"], m["reqSent"] = rfc(now), false
		people[k] = m
		doc["people"] = people
		return true
	})
	go s.ConsentTick(context.WithoutCancel(ctx))
	when := "сейчас"
	if !salesWindow(now) {
		when = "в " + fmt.Sprint(salesFrom) + ":00 по Алматы"
	}
	c.JSON(http.StatusOK, gin.H{"ok": true, "when": when})
}

func consentKB() map[string]any {
	return kb(row(map[string]any{"text": "Анонимно", "callback_data": csPrefix + "anon"}, map[string]any{"text": "С именем", "callback_data": csPrefix + "name"}),
		row(map[string]any{"text": "Нельзя", "callback_data": csPrefix + "no"}))
}

// ConsentTick sends the asked consent requests (in the window).
func (s *ClubSales) ConsentTick(ctx context.Context) {
	now := s.now()
	if !salesWindow(now) || s.Boards == nil {
		return
	}
	cfg := s.Settings(ctx)
	var todo []string
	_ = s.mutate(ctx, "club", consentDoc, func(doc map[string]any) bool {
		todo = nil
		for _, v := range sMap(doc, "people") {
			m, _ := v.(map[string]any)
			if m != nil && sStr(m, "reqAt") != "" && m["reqSent"] == false {
				m["reqSent"] = true
				todo = append(todo, sStr(m, "name"))
			}
		}
		return len(todo) > 0
	})
	for _, name := range todo {
		tg, _, err := s.Boards.ResidentTgByName(ctx, name)
		if err != nil || tg == 0 {
			s.team(ctx, "Согласие на кейс: у резидента «"+name+"» нет Telegram в списке клуба, спросите лично", nil)
			continue
		}
		text := salesFill(cfg.Text("consent"), map[string]string{"{имя}": firstName(name)})
		if err := s.toResident(ctx, "notice", "consent|"+rfc(now), name, tg, text, consentKB()); err != nil {
			log.Printf("sales: consent %s: %v", name, err)
		}
	}
}

// ConsentCallback: the resident's answer (cs:anon|name|no).
func (s *ClubSales) ConsentCallback(ctx context.Context, cb bot.CallbackUpdate) (string, bool) {
	if !strings.HasPrefix(cb.Data, csPrefix) || s.Boards == nil {
		return "", false
	}
	mode := strings.TrimPrefix(cb.Data, csPrefix)
	name, active, err := s.Boards.ResidentByTg(ctx, cb.FromID)
	if err != nil || name == "" || !active {
		return "Это для резидентов клуба", true
	}
	if err := s.setConsent(ctx, name, mode, "резидент в боте"); err != nil {
		return "Не получилось, попробуйте ещё раз", true
	}
	ans := map[string]string{"anon": "Спасибо! Ваши результаты можно показать в кейсах клуба анонимно: только ниша, город и цифры.",
		"name": "Спасибо! Ваши результаты можно показать в кейсах клуба с именем.",
		"no":   "Понял: ваши результаты в кейсах клуба не используем."}[mode]
	if s.Send != nil {
		_ = s.Send(ctx, cb.ChatID, ans+" Поменять решение можно в профиле на платформе или в приложении.", nil)
	}
	s.team(ctx, "Согласие на кейс: "+name+" выбрал(а) «"+consentModes[mode]+"»", nil)
	return "Сохранено", true
}

// ── кейсы ──

type salesCase struct {
	ID          string                `json:"id"`
	Resident    string                `json:"resident"`
	Niche       string                `json:"niche"`
	City        string                `json:"city"`
	Organ       string                `json:"organ"`
	Status      string                `json:"status"` // draft | approved | published | hidden
	Months      int                   `json:"months"`
	From        string                `json:"from"`
	To          string                `json:"to"`
	Metrics     []tplpdf.ReportMetric `json:"metrics"`
	Closed      []string              `json:"closed"`
	Title       string                `json:"title"`
	Story       string                `json:"story"`
	Edited      bool                  `json:"edited,omitempty"`
	AI          bool                  `json:"ai,omitempty"`
	CreatedAt   string                `json:"createdAt"`
	UpdatedAt   string                `json:"updatedAt"`
	By          string                `json:"by,omitempty"`
	ApprovedAt  string                `json:"approvedAt,omitempty"`
	PublishedAt string                `json:"publishedAt,omitempty"`
}

func (s *ClubSales) cases(ctx context.Context) []salesCase {
	doc := s.read(ctx, "club", casesDoc)
	b, _ := json.Marshal(doc["items"])
	var out []salesCase
	_ = json.Unmarshal(b, &out)
	return out
}

func (s *ClubSales) saveCases(ctx context.Context, fn func(items []salesCase) ([]salesCase, bool)) error {
	return s.mutate(ctx, "club", casesDoc, func(doc map[string]any) bool {
		b, _ := json.Marshal(doc["items"])
		var items []salesCase
		_ = json.Unmarshal(b, &items)
		out, ok := fn(items)
		if !ok {
			return false
		}
		var raw []any
		b, _ = json.Marshal(out)
		_ = json.Unmarshal(b, &raw)
		doc["items"] = raw
		return true
	})
}

// who: what an anonymous case may say about the person.
func (c *salesCase) who(mode string) string {
	niche := strings.TrimSpace(c.Niche)
	if niche == "" {
		niche = "Бизнес резидента"
	}
	parts := []string{}
	if mode == "name" {
		parts = append(parts, c.Resident)
		niche = lowFirst(niche)
	}
	parts = append(parts, niche)
	if c.City != "" {
		parts = append(parts, c.City)
	}
	return strings.Join(parts, ", ")
}

func monthsWord(n int) string {
	switch {
	case n%10 == 1 && n%100 != 11:
		return fmt.Sprintf("%d месяц", n)
	case n%10 >= 2 && n%10 <= 4 && (n%100 < 12 || n%100 > 14):
		return fmt.Sprintf("%d месяца", n)
	}
	return fmt.Sprintf("%d месяцев", n)
}

func metricLine(m tplpdf.ReportMetric) string {
	return m.Label + ": " + tplpdf.MetricValue(m.Before, m.Unit) + " → " + tplpdf.MetricValue(m.After, m.Unit) + " (" + tplpdf.MetricDelta(m) + ")"
}

// caseTemplate: the title and the short story (no AI).
func caseTemplate(c *salesCase, mode string) (string, string) {
	var best *tplpdf.ReportMetric
	for i := range c.Metrics {
		m := &c.Metrics[i]
		if m.After == m.Before {
			continue
		}
		if best == nil || (m.Unit == "₸" && best.Unit != "₸") {
			best = m
		}
	}
	title := "Было → стало за " + monthsWord(c.Months)
	if best != nil {
		verb := "выросла"
		if best.After < best.Before {
			verb = "снизилась"
		}
		lbl := strings.ToLower(best.Label)
		switch {
		case strings.HasPrefix(lbl, "часы"):
			verb = map[bool]string{true: "стало меньше", false: "стало больше"}[best.After < best.Before]
			title = fmt.Sprintf("Часов собственника %s: %s → %s за %s", verb, tplpdf.MetricValue(best.Before, best.Unit), tplpdf.MetricValue(best.After, best.Unit), monthsWord(c.Months))
		case strings.HasPrefix(lbl, "чистая прибыль"):
			title = fmt.Sprintf("Прибыль %s на %s за %s", map[bool]string{true: "выросла", false: "снизилась"}[best.After > best.Before], strings.TrimLeft(tplpdf.MetricDelta(*best), "+-"), monthsWord(c.Months))
		case strings.HasPrefix(lbl, "индекс"):
			title = fmt.Sprintf("Индекс здоровья бизнеса %s → %s за %s", tplpdf.MetricValue(best.Before, ""), tplpdf.MetricValue(best.After, ""), monthsWord(c.Months))
		default:
			title = fmt.Sprintf("Выручка %s на %s за %s", verb, strings.TrimLeft(tplpdf.MetricDelta(*best), "+-"), monthsWord(c.Months))
		}
	}
	var b strings.Builder
	b.WriteString(c.who(mode) + ". ")
	b.WriteString(fmt.Sprintf("Пришёл в клуб %s, сейчас %s в клубе.", c.From, monthsWord(c.Months)))
	if c.Organ != "" {
		b.WriteString(" Главная зона работы: " + strings.ToLower(c.Organ) + ".")
	}
	b.WriteString("\n\nЧто изменилось в цифрах:")
	for _, m := range c.Metrics {
		b.WriteString("\n" + metricLine(m))
	}
	if len(c.Closed) > 0 {
		cl := c.Closed
		if len(cl) > 4 {
			cl = cl[:4]
		}
		b.WriteString("\n\nЗакрытые диагнозы: " + strings.Join(cl, ", ") + ".")
	}
	b.WriteString("\n\nКак: разбор каждые 10 дней с трекерами, план задач на цикл и ежедневные отчёты.")
	return noLongDash(title), noLongDash(b.String())
}

// caseFromPeriod: a draft from the period data; nil when the data is too thin.
func caseFromPeriod(p *ResPeriod, now time.Time, mode string) *salesCase {
	changed := 0
	for _, m := range p.Metrics {
		if m.After != m.Before {
			changed++
		}
	}
	if changed == 0 || (changed < 2 && len(p.Closed()) == 0) {
		return nil
	}
	months := int(math.Round(now.Sub(p.First).Hours() / 24 / 30.4))
	if months < 1 {
		months = 1
	}
	c := &salesCase{Resident: p.Name, Niche: p.Niche, City: p.City, Organ: p.MainOrgan(), Months: months,
		From: p.First.In(almaty).Format("01.2006"), To: now.In(almaty).Format("01.2006"), Metrics: p.Metrics, Closed: p.Closed()}
	if c.City == "" {
		c.City = "Алматы"
	}
	c.Title, c.Story = caseTemplate(c, mode)
	return c
}

// caseView: what the team (and the exports) see; the consent applies now.
func (s *ClubSales) caseView(c salesCase, mode string) gin.H {
	ex := caseExports(&c, mode)
	return gin.H{"case": c, "consent": mode, "exports": ex, "who": c.who(mode)}
}

// caseExports: Threads, carousel, site block.
func caseExports(c *salesCase, mode string) gin.H {
	who := c.who(mode)
	var nums []string
	for _, m := range c.Metrics {
		nums = append(nums, metricLine(m))
	}
	th := c.Title + "\n\n" + who + ".\n" + strings.Join(nums, "\n")
	if len(c.Closed) > 0 {
		th += "\n\nЗакрыли: " + strings.Join(c.Closed[:minInt2(len(c.Closed), 3)], ", ")
	}
	th += "\n\nРазбор бизнеса каждые 10 дней в Business Surgery. Экспресс-разбор: t.me/bsurgery_bot?start=th_case"
	if r := []rune(th); len(r) > 500 {
		th = string(r[:497]) + "..."
	}
	slides := []string{c.Title, who}
	for _, m := range c.Metrics {
		slides = append(slides, m.Label+"\nбыло "+tplpdf.MetricValue(m.Before, m.Unit)+"\nстало "+tplpdf.MetricValue(m.After, m.Unit)+"\n"+tplpdf.MetricDelta(m))
	}
	if len(c.Closed) > 0 {
		slides = append(slides, "Закрытые диагнозы\n"+strings.Join(c.Closed[:minInt2(len(c.Closed), 4)], "\n"))
	}
	slides = append(slides, "Как: разбор каждые 10 дней, план на цикл, ежедневные отчёты\nЭкспресс-разбор: @bsurgery_bot")
	var site strings.Builder
	site.WriteString(`<article class="bs-case"><h3>` + htmlEsc(c.Title) + `</h3><p class="who">` + htmlEsc(who) + `</p><ul>`)
	for _, m := range c.Metrics {
		site.WriteString(`<li><b>` + htmlEsc(m.Label) + `</b> ` + htmlEsc(tplpdf.MetricValue(m.Before, m.Unit)) + ` → ` + htmlEsc(tplpdf.MetricValue(m.After, m.Unit)) + ` <i>` + htmlEsc(tplpdf.MetricDelta(m)) + `</i></li>`)
	}
	site.WriteString(`</ul></article>`)
	return gin.H{"threads": noLongDash(th), "carousel": slides, "site": site.String()}
}

func minInt2(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func htmlEsc(s string) string {
	r := strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;", `"`, "&quot;")
	return r.Replace(s)
}

// Cases: GET /sales/cases.
func (s *ClubSales) Cases(c *gin.Context) {
	ctx := c.Request.Context()
	cons := s.consents(ctx)
	out := []gin.H{}
	for _, x := range s.cases(ctx) {
		md := sStr(cons[normName(x.Resident)], "mode")
		if consentModes[md] == "" {
			md = "no"
		}
		out = append(out, s.caseView(x, md))
	}
	c.JSON(http.StatusOK, gin.H{"items": out, "ai": s.AI != nil})
}

// GenerateCases: POST /sales/cases/generate: drafts for everyone with consent and data.
func (s *ClubSales) GenerateCases(c *gin.Context) {
	ctx := c.Request.Context()
	n, skipped, err := s.Generate(ctx, platformUser(c))
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "storage", "message": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"made": n, "skipped": skipped})
}

// Generate makes or refreshes the drafts. skipped: why someone has none.
func (s *ClubSales) Generate(ctx context.Context, by string) (int, []gin.H, error) {
	if s.Club == nil {
		return 0, nil, fmt.Errorf("нет данных клуба")
	}
	list, err := s.Club.LoadResidents(ctx)
	if err != nil {
		return 0, nil, err
	}
	now := s.now()
	cons := s.consents(ctx)
	var drafts []*salesCase
	skipped := []gin.H{}
	for _, r := range activeResidents(list) {
		md := sStr(cons[normName(r.Name)], "mode")
		if md != "anon" && md != "name" {
			skipped = append(skipped, gin.H{"name": r.Name, "why": "нет согласия"})
			continue
		}
		from := now.AddDate(-3, 0, 0)
		if r.JoinedAt != nil {
			from = r.JoinedAt.AddDate(0, 0, -7)
		}
		p := s.period(ctx, r.Name, from, now)
		if r.JoinedAt != nil && (p.First.IsZero() || r.JoinedAt.Before(p.First)) {
			p.First = *r.JoinedAt
		}
		cs := caseFromPeriod(p, now, md)
		if cs == nil {
			skipped = append(skipped, gin.H{"name": r.Name, "why": "мало данных: нужны замеры в начале и сейчас"})
			continue
		}
		drafts = append(drafts, cs)
	}
	made := 0
	err = s.saveCases(ctx, func(items []salesCase) ([]salesCase, bool) {
		for _, d := range drafts {
			found := false
			for i := range items {
				x := &items[i]
				if normName(x.Resident) != normName(d.Resident) || x.Status == "hidden" {
					continue
				}
				found = true
				if x.Status == "draft" {
					x.Metrics, x.Closed, x.Months, x.To, x.Niche, x.City, x.Organ = d.Metrics, d.Closed, d.Months, d.To, d.Niche, d.City, d.Organ
					if !x.Edited {
						x.Title, x.Story = d.Title, d.Story
					}
					x.UpdatedAt = rfc(now)
					made++
				}
			}
			if !found {
				d.ID = "case_" + strconv.FormatInt(now.UnixNano(), 36) + strconv.Itoa(made)
				d.Status, d.CreatedAt, d.UpdatedAt, d.By = "draft", rfc(now), rfc(now), by
				items = append(items, *d)
				made++
			}
		}
		return items, true
	})
	return made, skipped, err
}

type putCaseReq struct {
	Title  *string `json:"title"`
	Story  *string `json:"story"`
	Status string  `json:"status"`
}

// PutCase: PUT /sales/cases/:id {title?, story?, status?}.
func (s *ClubSales) PutCase(c *gin.Context) {
	var r putCaseReq
	if c.ShouldBindJSON(&r) != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "bad_json"})
		return
	}
	if r.Status != "" && r.Status != "draft" && r.Status != "approved" && r.Status != "published" && r.Status != "hidden" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "bad_status"})
		return
	}
	ctx := c.Request.Context()
	id := c.Param("id")
	now := s.now()
	code := ""
	err := s.saveCases(ctx, func(items []salesCase) ([]salesCase, bool) {
		for i := range items {
			x := &items[i]
			if x.ID != id {
				continue
			}
			if r.Status == "published" && s.ConsentOf(ctx, x.Resident) == "no" {
				code = "no_consent"
				return nil, false
			}
			if r.Title != nil {
				x.Title, x.Edited = clip(noLongDash(*r.Title), 200), true
			}
			if r.Story != nil {
				x.Story, x.Edited = clip(noLongDash(*r.Story), 4000), true
			}
			if r.Status != "" && r.Status != x.Status {
				x.Status = r.Status
				switch r.Status {
				case "approved":
					x.ApprovedAt = rfc(now)
				case "published":
					x.PublishedAt = rfc(now)
					if x.ApprovedAt == "" {
						x.ApprovedAt = rfc(now)
					}
				}
			}
			x.UpdatedAt = rfc(now)
			return items, true
		}
		code = "not_found"
		return nil, false
	})
	switch {
	case code == "no_consent":
		c.JSON(http.StatusConflict, gin.H{"error": "no_consent", "message": "Резидент не дал согласия: публиковать нельзя"})
		return
	case code == "not_found":
		c.JSON(http.StatusNotFound, gin.H{"error": "not_found"})
		return
	case err != nil:
		c.JSON(http.StatusInternalServerError, gin.H{"error": "storage"})
		return
	}
	s.syncPublic(ctx)
	s.Cases(c)
}

// DeleteCase: DELETE /sales/cases/:id.
func (s *ClubSales) DeleteCase(c *gin.Context) {
	ctx := c.Request.Context()
	id := c.Param("id")
	_ = s.saveCases(ctx, func(items []salesCase) ([]salesCase, bool) {
		out := items[:0]
		for _, x := range items {
			if x.ID != id {
				out = append(out, x)
			}
		}
		return out, len(out) != len(items)
	})
	s.syncPublic(ctx)
	s.Cases(c)
}

const caseAISystem = `Ты редактор клуба Business Surgery. По цифрам резидента напиши короткий кейс «было → стало» для соцсетей:
заголовок до 90 знаков и историю 3-5 предложений. Только факты из данных, ничего не придумывай, без длинного тире,
тысячи через пробел: 10 000. Если сказано «анонимно», не называй имя, только нишу и город.
Ответ строго JSON: {"title":"...","story":"..."}`

// CaseAI: POST /sales/cases/:id/ai: the story rewritten by the model (the template stays if it fails).
func (s *ClubSales) CaseAI(c *gin.Context) {
	if s.AI == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "no_ai", "message": "ИИ не подключён, история собрана по шаблону"})
		return
	}
	ctx := c.Request.Context()
	id := c.Param("id")
	var cur *salesCase
	for _, x := range s.cases(ctx) {
		if x.ID == id {
			y := x
			cur = &y
		}
	}
	if cur == nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "not_found"})
		return
	}
	mode := s.ConsentOf(ctx, cur.Resident)
	var b strings.Builder
	if mode == "name" {
		b.WriteString("Резидент: " + cur.Resident + " (можно с именем)\n")
	} else {
		b.WriteString("Анонимно\n")
	}
	b.WriteString("Ниша: " + cur.Niche + "\nГород: " + cur.City + "\nВ клубе: " + monthsWord(cur.Months) + "\nЦифры:\n")
	for _, m := range cur.Metrics {
		b.WriteString(metricLine(m) + "\n")
	}
	if len(cur.Closed) > 0 {
		b.WriteString("Закрытые диагнозы: " + strings.Join(cur.Closed, ", ") + "\n")
	}
	actx, cancel := context.WithTimeout(ctx, 90*time.Second)
	defer cancel()
	ans, err := s.AI(actx, caseAISystem, b.String())
	if err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": "ai", "message": "ИИ не ответил: " + err.Error()})
		return
	}
	var out struct {
		Title string `json:"title"`
		Story string `json:"story"`
	}
	if i, j := strings.Index(ans, "{"), strings.LastIndex(ans, "}"); i >= 0 && j > i {
		_ = json.Unmarshal([]byte(ans[i:j+1]), &out)
	}
	if strings.TrimSpace(out.Story) == "" {
		c.JSON(http.StatusBadGateway, gin.H{"error": "ai", "message": "ИИ ответил не по формату, история осталась прежней"})
		return
	}
	if mode != "name" && strings.Contains(strings.ToLower(out.Title+out.Story), strings.ToLower(firstName(cur.Resident))) {
		c.JSON(http.StatusBadGateway, gin.H{"error": "ai", "message": "ИИ назвал имя в анонимном кейсе, история осталась прежней"})
		return
	}
	title, story := noLongDash(out.Title), noLongDash(out.Story)
	_ = s.saveCases(ctx, func(items []salesCase) ([]salesCase, bool) {
		for i := range items {
			if items[i].ID == id {
				if title != "" {
					items[i].Title = clip(title, 200)
				}
				items[i].Story, items[i].AI, items[i].Edited, items[i].UpdatedAt = clip(story, 4000), true, true, rfc(s.now())
				return items, true
			}
		}
		return nil, false
	})
	s.Cases(c)
}

// published: the cases that may be shown now (published, consent still given).
func (s *ClubSales) published(ctx context.Context) []gin.H {
	cons := s.consents(ctx)
	var out []gin.H
	for _, x := range s.cases(ctx) {
		md := sStr(cons[normName(x.Resident)], "mode")
		if x.Status != "published" || (md != "anon" && md != "name") {
			continue
		}
		out = append(out, gin.H{"case": x, "mode": md})
	}
	return out
}

// PublicCaseView: a published case as the open page shows it.
type PublicCaseView struct {
	Title   string
	Who     string
	Metrics []string
	Story   string
}

func (s *ClubSales) syncPublic(ctx context.Context) {
	if s.OnCases == nil {
		return
	}
	var list []PublicCaseView
	for _, p := range s.published(ctx) {
		x := p["case"].(salesCase)
		md := p["mode"].(string)
		v := PublicCaseView{Title: x.Title, Who: x.who(md), Story: x.Story}
		if md != "name" {
			v.Story = "" // the team's text may name the person: anonymous cases show only the figures
		}
		for _, m := range x.Metrics {
			v.Metrics = append(v.Metrics, metricLine(m))
		}
		list = append(list, v)
	}
	s.OnCases(list)
}

// caseFor: the day-5 case: a published one of the same organ (or niche),
// else the library's story for the lead's first diagnosis.
func (s *ClubSales) caseFor(ctx context.Context, plans []diagPlan, niche string) string {
	organs := map[string]bool{}
	for _, p := range plans {
		if p.Organ != "" {
			organs[p.Organ] = true
		}
	}
	var best *salesCase
	bestMode := ""
	score := -1
	for _, p := range s.published(ctx) {
		x := p["case"].(salesCase)
		sc := 0
		if organs[x.Organ] {
			sc += 2
		}
		if nicheNear(niche, x.Niche) {
			sc += 3
		}
		if sc > score {
			y := x
			best, bestMode, score = &y, p["mode"].(string), sc
		}
	}
	if best != nil && score > 0 {
		var nums []string
		for _, m := range best.Metrics {
			nums = append(nums, metricLine(m))
		}
		return best.who(bestMode) + ".\n" + strings.Join(nums, "\n")
	}
	for _, p := range plans {
		if p.Case != nil {
			bz, res := csS(p.Case["business"]), csS(p.Case["result"])
			if res != "" {
				if bz != "" {
					return upFirst(bz) + ". " + res
				}
				return res
			}
		}
	}
	if best != nil {
		var nums []string
		for _, m := range best.Metrics {
			nums = append(nums, metricLine(m))
		}
		return best.who(bestMode) + ".\n" + strings.Join(nums, "\n")
	}
	return "Собственник оптовой компании, Алматы: за 3 цикла по 10 дней навёл порядок в деньгах, кассовые разрывы ушли, прибыль стала видна каждую неделю."
}

func lowFirst(s string) string {
	r := []rune(s)
	if len(r) == 0 {
		return s
	}
	return strings.ToLower(string(r[:1])) + string(r[1:])
}

// SyncPublic: the published cases to the open page (at start).
func (s *ClubSales) SyncPublic(ctx context.Context) { s.syncPublic(ctx) }

// nicheNear: two niches share a word stem («кофейня» and «сеть кофеен»).
func nicheNear(a, b string) bool {
	stems := func(s string) map[string]bool {
		out := map[string]bool{}
		for _, w := range strings.FieldsFunc(strings.ToLower(s), func(r rune) bool { return !unicode.IsLetter(r) }) {
			if r := []rune(w); len(r) >= 4 {
				out[string(r[:4])] = true
			}
		}
		return out
	}
	sa, sb := stems(a), stems(b)
	for k := range sa {
		if sb[k] {
			return true
		}
	}
	return false
}
