package http

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/bnursik/business_surgery_backend/internal/bot"
	"github.com/bnursik/business_surgery_backend/internal/content"
	"github.com/bnursik/business_surgery_backend/internal/tplpdf"
	"github.com/gin-gonic/gin"
)

// R51 (1): «Сценарий после экспресс-разбора: саммари PDF + предложение клуба
// + напоминания».
//
// Итоги разбора лида команда заполняет короткой формой в карточке CRM
// (3 диагноза, 3 шага, цифры): PUT /sales/razbor/:lead. Для онлайн-разбора
// с записью то же делает публикация саммари звонка (callsum_flow.go →
// OnCallPublished). С этого момента сценарий лида (lead.pz):
//
//	offer  сразу (в окне 10:00-20:00): PDF итогов + предложение клуба под его
//	       диагнозы, кнопки «Хочу в клуб», «Есть вопрос», «Не сейчас»
//	d2     через 2 дня: «как первые шаги?» и один инструмент из итогов
//	d5     через 5 дней: короткий кейс похожего бизнеса (sales_cases.go)
//	d10    через 10 дней: последнее напоминание
//
// Сценарий останавливается, когда лид ответил (написал в бот), оплатил
// (этап «Резидент»), нажал «Хочу в клуб» (этап «Решение», команде
// уведомление), «Не сейчас» (этап «Отложено», бот спрашивает, когда
// напомнить) или команда остановила его в карточке. Лид без Telegram, но
// с телефоном получает то же в очереди WhatsApp (ссылка на PDF вместо файла).
//
// club doc bs_razbor_sums = {items: {<leadId>: {leadId, tgId, name, phone,
// date, form, at, by, source: form|call}}}; состояние сценария в карточке
// лида bs_crm: lead.pz = {st, start, offerAt, steps{offer,d2,d5,d10},
// next, nextAt, why, stopAt, via, remindAt}.

const (
	razborSumsDoc = "bs_razbor_sums"
	pzPrefix      = "pz:"
)

var pzSteps = []struct {
	Key  string
	Days int
	Name string
}{{"offer", 0, "PDF и предложение"}, {"d2", 2, "день 2: первые шаги"}, {"d5", 5, "день 5: кейс"}, {"d10", 10, "день 10: последнее напоминание"}}

// RazborForm: what the team writes after an offline разбор.
type RazborForm struct {
	Date      string `json:"date"` // 02.10.2026
	Situation string `json:"situation"`
	Diag      []struct {
		Title string `json:"title"`
		Why   string `json:"why"`
	} `json:"diag"`
	Steps   []string `json:"steps"`
	Numbers []struct {
		Label string `json:"label"`
		Value string `json:"value"`
	} `json:"numbers"`
	PointA string `json:"pointA"`
	PointB string `json:"pointB"`
}

func (f *RazborForm) clean() {
	cl := func(s string, n int) string { return clip(noLongDash(s), n) }
	f.Date, f.Situation, f.PointA, f.PointB = cl(f.Date, 20), cl(f.Situation, 2000), cl(f.PointA, 300), cl(f.PointB, 300)
	ds := f.Diag[:0]
	for _, d := range f.Diag {
		d.Title, d.Why = cl(d.Title, 160), cl(d.Why, 400)
		if d.Title != "" && len(ds) < 5 {
			ds = append(ds, d)
		}
	}
	f.Diag = ds
	ss := []string{}
	for _, s := range f.Steps {
		if s = cl(s, 400); s != "" && len(ss) < 6 {
			ss = append(ss, s)
		}
	}
	f.Steps = ss
	ns := f.Numbers[:0]
	for _, n := range f.Numbers {
		n.Label, n.Value = cl(n.Label, 120), cl(n.Value, 60)
		if n.Value != "" && len(ns) < 9 {
			ns = append(ns, n)
		}
	}
	f.Numbers = ns
}

// ── библиотека: диагнозы и инструменты по точному названию ──

type salesLibDiag struct {
	ID, Title, Organ string
	Cure, Steps      []string
	Case             map[string]any
}

type salesLibIdx struct {
	etag        string
	diag        map[string]salesLibDiag // csNorm(title)
	diagTitles  []string
	tools       map[string]csRef // csNorm(title)
	toolByOrgan map[string][]csRef
}

var (
	salesLibMu sync.Mutex
	salesLibC  *salesLibIdx
)

func salesLib() *salesLibIdx {
	plain, _, etag, err := content.LibRich()
	salesLibMu.Lock()
	defer salesLibMu.Unlock()
	if salesLibC != nil && salesLibC.etag == etag {
		return salesLibC
	}
	x := &salesLibIdx{etag: etag, diag: map[string]salesLibDiag{}, tools: map[string]csRef{}, toolByOrgan: map[string][]csRef{}}
	if err == nil {
		var raw struct {
			Tools []map[string]any `json:"tools"`
			Diag  []map[string]any `json:"diag"`
		}
		_ = json.Unmarshal(plain, &raw)
		for _, t := range raw.Tools {
			r := csRef{ID: csS(t["id"]), Title: csS(t["title"])}
			if k := csNorm(r.Title); k != "" {
				if _, dup := x.tools[k]; !dup {
					x.tools[k] = r
					o := csS(t["organ"])
					x.toolByOrgan[o] = append(x.toolByOrgan[o], r)
				}
			}
		}
		for _, d := range raw.Diag {
			v := salesLibDiag{ID: csS(d["id"]), Title: csS(d["title"]), Organ: csS(d["organ"]), Cure: sStrs(d["cure"]), Steps: sStrs(d["first_steps"])}
			v.Case, _ = d["case"].(map[string]any)
			if k := csNorm(v.Title); k != "" {
				if _, dup := x.diag[k]; !dup {
					x.diag[k] = v
					x.diagTitles = append(x.diagTitles, v.Title)
				}
			}
		}
	}
	salesLibC = x
	return x
}

// organ words: a diagnosis outside the library still finds its organ.
var organWords = []struct {
	organ string
	words []string
}{
	{"Финансы", []string{"деньг", "касс", "прибыл", "финанс", "маржин", "долг", "кредит", "себестоим", "налог", "дебитор", "p&l", "учёт", "учет"}},
	{"Продажи", []string{"продаж", "конверс", "чек", "клиент", "менеджер", "сделк", "скрипт", "воронк", "повторн"}},
	{"Маркетинг", []string{"маркет", "заявк", "реклам", "трафик", "лид", "бренд", "позиционир", "контент", "smm", "таргет"}},
	{"Команда", []string{"команд", "сотрудн", "найм", "персонал", "делегир", "руковод", "текучк", "мотивац", "люд", "операционк", "собственник"}},
	{"Стратегия", []string{"стратег", "цел", "рост", "масштаб", "ниш", "фокус", "направлен"}},
	{"Процессы", []string{"процесс", "регламент", "операц", "хаос", "срок", "качеств", "автомат", "систем"}},
	{"Аналитика", []string{"аналит", "цифр", "метрик", "отчёт", "отчет", "показател", "данн"}},
}

func guessOrgan(s string) string {
	low := strings.ToLower(s)
	for _, o := range organWords {
		for _, w := range o.words {
			if strings.Contains(low, w) {
				return o.organ
			}
		}
	}
	return ""
}

var organPlan = map[string]string{
	"Финансы":   "за первые 2 цикла (20 дней) деньги под контролем: видно, куда уходят и сколько остаётся",
	"Продажи":   "за 3 цикла: воронка в цифрах, скрипты, конверсия под еженедельным контролем",
	"Маркетинг": "за 3 цикла: понятный поток заявок и цена заявки по каждому каналу",
	"Команда":   "за 3-4 цикла: роли, зоны ответственности и планёрки, вы выходите из операционки",
	"Стратегия": "на первом же цикле: цель на 3 месяца в цифрах и план по неделям",
	"Процессы":  "за 2-3 цикла: регламенты ключевых процессов, бизнес меньше зависит от вас",
	"Аналитика": "с первого цикла: 5-7 ключевых цифр бизнеса каждую неделю на одном экране",
}

// diagPlan: one diagnosis → the library entry (if any), its organ and the club's tools for it.
type diagPlan struct {
	Title, Why, Organ string
	Lib               bool
	Tools             []csRef
	Steps             []string
	Case              map[string]any
}

func planForDiag(title, why string) diagPlan {
	lib := salesLib()
	p := diagPlan{Title: title, Why: why}
	if d, ok := lib.diag[csNorm(title)]; ok {
		p.Title, p.Lib, p.Organ, p.Steps, p.Case = d.Title, true, d.Organ, d.Steps, d.Case
		for _, c := range d.Cure {
			if t, ok := lib.tools[csNorm(c)]; ok {
				p.Tools = append(p.Tools, t)
			}
		}
	}
	if p.Organ == "" {
		p.Organ = guessOrgan(title + " " + why)
	}
	if len(p.Tools) == 0 && p.Organ != "" {
		ts := lib.toolByOrgan[p.Organ]
		if len(ts) > 2 {
			ts = ts[:2]
		}
		p.Tools = append(p.Tools, ts...)
	}
	return p
}

// clubForDiagnoses: «что даст клуб» line by line, under the lead's diagnoses.
func clubForDiagnoses(plans []diagPlan) string {
	var b strings.Builder
	for _, p := range plans {
		b.WriteString("• " + p.Title)
		var tn []string
		for i, t := range p.Tools {
			if i >= 2 {
				break
			}
			tn = append(tn, "«"+t.Title+"»")
		}
		if len(tn) > 0 {
			b.WriteString(": внедряем " + strings.Join(tn, " и "))
		}
		if pl := organPlan[p.Organ]; pl != "" {
			if len(tn) > 0 {
				b.WriteString(", " + pl)
			} else {
				b.WriteString(": " + pl)
			}
		} else if len(tn) == 0 {
			b.WriteString(": разбираем на ближайших циклах и закрываем по плану")
		}
		b.WriteString("\n")
	}
	return strings.TrimSpace(b.String())
}

// ── итоги разбора ──

type razborSum struct {
	LeadID string     `json:"leadId"`
	TgID   int64      `json:"tgId"`
	Name   string     `json:"name"`
	Phone  string     `json:"phone"`
	Form   RazborForm `json:"form"`
	Source string     `json:"source"` // form | call
	CallID string     `json:"callId,omitempty"`
	At     string     `json:"at"`
	By     string     `json:"by"`
}

func (s *ClubSales) razborSum(ctx context.Context, leadID string) *razborSum {
	doc := s.read(ctx, "club", razborSumsDoc)
	items := sMap(doc, "items")
	raw, ok := items[leadID]
	if !ok {
		return nil
	}
	b, _ := json.Marshal(raw)
	var r razborSum
	if json.Unmarshal(b, &r) != nil {
		return nil
	}
	return &r
}

// razborDoc: the PDF content of a lead's express разбор.
func razborDoc(r *razborSum) *tplpdf.CallDoc {
	f := r.Form
	d := &tplpdf.CallDoc{Resident: r.Name, Date: f.Date, Title: "Итоги экспресс-разбора", Pill: "ЭКСПРЕСС-РАЗБОР",
		Footer:       "Business Surgery · bxclub.kz · итоги экспресс-разбора",
		Participants: []string{"Рустам и Береке, трекеры BS", firstName(r.Name)},
		Situation:    f.Situation, PointA: f.PointA, PointB: f.PointB}
	for _, n := range f.Numbers {
		d.Numbers = append(d.Numbers, tplpdf.CallNumber{Label: n.Label, Value: n.Value})
	}
	var plans []diagPlan
	for _, g := range f.Diag {
		p := planForDiag(g.Title, g.Why)
		plans = append(plans, p)
		d.DiagList = append(d.DiagList, tplpdf.CallDiag{Title: p.Title, Why: g.Why, Lib: p.Lib})
	}
	start, ok := parseRuDate(f.Date)
	if !ok {
		start = time.Now()
	}
	for i, st := range f.Steps {
		sol := tplpdf.CallSolution{Text: st}
		if i < len(plans) && len(plans[i].Tools) > 0 {
			sol.Tool, sol.ToolURL = plans[i].Tools[0].Title, toolURL(plans[i].Tools[0].ID)
		}
		d.Solutions = append(d.Solutions, sol)
		d.Tasks = append(d.Tasks, tplpdf.CallTask{Text: st, Owner: firstName(r.Name), Due: start.AddDate(0, 0, 3*(i+1)).In(almaty).Format("02.01")})
	}
	d.NextAgenda = []string{"Как прошли первые шаги", "Что даст клуб под ваши диагнозы"}
	return d
}

func parseRuDate(s string) (time.Time, bool) {
	t, err := time.ParseInLocation("02.01.2006", strings.TrimSpace(s), almaty)
	return t, err == nil
}

func razborPDFName(name, date string) string {
	n := strings.TrimSpace(name)
	if n == "" {
		n = "BS"
	}
	if date != "" {
		return "Итоги разбора · " + n + " · " + date + ".pdf"
	}
	return "Итоги разбора · " + n + ".pdf"
}

// leadByID finds a lead card in bs_crm.
func (s *ClubSales) leadByID(ctx context.Context, id string) map[string]any {
	crm := s.read(ctx, "club", "bs_crm")
	for _, l := range asList(crm["leads"]) {
		if m, _ := l.(map[string]any); m != nil && sStr(m, "id") == id {
			return m
		}
	}
	return nil
}

// mutateLead changes one lead card (false from fn: nothing to write).
func (s *ClubSales) mutateLead(ctx context.Context, match func(l map[string]any) bool, fn func(l map[string]any) bool) error {
	return s.mutate(ctx, "club", "bs_crm", func(crm map[string]any) bool {
		for _, l := range asList(crm["leads"]) {
			if m, _ := l.(map[string]any); m != nil && match(m) {
				return fn(m)
			}
		}
		return false
	})
}

func byLeadID(id string) func(map[string]any) bool {
	return func(l map[string]any) bool { return sStr(l, "id") == id }
}

func byLeadTg(tg int64) func(map[string]any) bool {
	return func(l map[string]any) bool { return leadTg(l) == tg }
}

// GetRazbor: GET /sales/razbor/:lead → the form, the sequence, the library titles.
func (s *ClubSales) GetRazbor(c *gin.Context) {
	ctx := c.Request.Context()
	id := c.Param("lead")
	l := s.leadByID(ctx, id)
	if l == nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "not_found"})
		return
	}
	out := gin.H{"lead": gin.H{"id": id, "name": sStr(l, "name"), "tgId": leadTg(l), "phone": sStr(l, "phone"), "col": sStr(l, "col")},
		"pz": l["pz"], "status": pzStatus(sMap(l, "pz"), s.now()), "diagTitles": salesLib().diagTitles}
	if r := s.razborSum(ctx, id); r != nil {
		out["sum"] = r
		out["pdf"] = "/api/v1/platform/sales/razbor/" + id + "/pdf"
	}
	c.JSON(http.StatusOK, out)
}

type putRazborReq struct {
	Form RazborForm `json:"form"`
	Send bool       `json:"send"`
}

// PutRazbor: PUT /sales/razbor/:lead {form, send}: keeps the итоги; send starts the sequence.
func (s *ClubSales) PutRazbor(c *gin.Context) {
	var req putRazborReq
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "bad_json"})
		return
	}
	req.Form.clean()
	if len(req.Form.Diag) == 0 || len(req.Form.Steps) == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "need_diag", "message": "Нужны хотя бы один диагноз и один шаг"})
		return
	}
	if req.Form.Date == "" {
		req.Form.Date = s.now().In(almaty).Format("02.01.2006")
	}
	ctx := c.Request.Context()
	id := c.Param("lead")
	l := s.leadByID(ctx, id)
	if l == nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "not_found"})
		return
	}
	if req.Send && leadTg(l) == 0 && len(waDigits(sStr(l, "phone"))) < 11 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "no_contact", "message": "У лида нет Telegram и телефона: отправить некуда"})
		return
	}
	r := &razborSum{LeadID: id, TgID: leadTg(l), Name: sStr(l, "name"), Phone: sStr(l, "phone"), Form: req.Form, Source: "form",
		At: rfc(s.now()), By: platformUser(c)}
	if _, err := tplpdf.RenderCallSummary(razborDoc(r)); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "pdf", "message": "PDF не собрался: " + err.Error()})
		return
	}
	if err := s.saveRazborSum(ctx, r); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "storage"})
		return
	}
	if req.Send {
		if err := s.StartSequence(ctx, id, "Итоги разбора заполнены"); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "storage", "message": err.Error()})
			return
		}
		go s.RazborTick(context.WithoutCancel(ctx)) // the offer goes now when it is daytime
	}
	s.GetRazbor(c)
}

func (s *ClubSales) saveRazborSum(ctx context.Context, r *razborSum) error {
	return s.mutate(ctx, "club", razborSumsDoc, func(doc map[string]any) bool {
		items := sMap(doc, "items")
		if items == nil {
			items = map[string]any{}
		}
		b, _ := json.Marshal(r)
		var m map[string]any
		_ = json.Unmarshal(b, &m)
		items[r.LeadID] = m
		doc["items"] = items
		return true
	})
}

// RazborPDF: GET /sales/razbor/:lead/pdf (the team's preview).
func (s *ClubSales) RazborPDF(c *gin.Context) {
	r := s.razborSum(c.Request.Context(), c.Param("lead"))
	if r == nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "not_found"})
		return
	}
	pdf, err := tplpdf.RenderCallSummary(razborDoc(r))
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "pdf"})
		return
	}
	servePDF(c, razborPDFName(r.Name, r.Form.Date), pdf)
}

func servePDF(c *gin.Context, name string, pdf []byte) {
	c.Header("Content-Disposition", "inline; filename*=UTF-8''"+urlPathEscape(name))
	c.Header("Cache-Control", "private, no-store")
	c.Data(http.StatusOK, "application/pdf", pdf)
}

func urlPathEscape(s string) string {
	var b strings.Builder
	for _, c := range []byte(s) {
		if (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') || c == '.' || c == '-' || c == '_' {
			b.WriteByte(c)
		} else {
			fmt.Fprintf(&b, "%%%02X", c)
		}
	}
	return b.String()
}

// StopRazbor: POST /sales/razbor/:lead/stop (the team stops the sequence).
func (s *ClubSales) StopRazbor(c *gin.Context) {
	ctx := c.Request.Context()
	id := c.Param("lead")
	now := s.now()
	_ = s.mutateLead(ctx, byLeadID(id), func(l map[string]any) bool {
		pz := sMap(l, "pz")
		if pz == nil || (pz["st"] != "active" && pz["st"] != "wait") {
			return false
		}
		pzStop(l, pz, now, "остановлено командой")
		return true
	})
	s.GetRazbor(c)
}

// StartSequence starts (or restarts) the lead's sequence: the offer goes at
// the next tick inside the window.
func (s *ClubSales) StartSequence(ctx context.Context, leadID, why string) error {
	now := s.now()
	return s.mutateLead(ctx, byLeadID(leadID), func(l map[string]any) bool {
		via := "tg"
		if leadTg(l) == 0 {
			via = "wa"
		}
		l["pz"] = map[string]any{"st": "wait", "start": rfc(now), "steps": map[string]any{}, "next": "offer", "nextAt": rfc(salesNextWindow(now)), "via": via}
		l["warmStop"] = true
		if c := sStr(l, "col"); c == "meet" || c == "" || c == "new" || c == "work" || c == "qual" {
			l["col"] = "diag"
		}
		addLog(l, now, why+": сценарий после разбора запущен (PDF и предложение клуба, напоминания на 2, 5, 10 день)")
		return true
	})
}

func pzStop(l, pz map[string]any, now time.Time, why string) {
	pz["st"], pz["why"], pz["stopAt"] = "stopped", why, rfc(now)
	delete(pz, "next")
	delete(pz, "nextAt")
	addLog(l, now, "Сценарий после разбора остановлен: "+why)
}

// pzStatus: one line for the CRM card.
func pzStatus(pz map[string]any, now time.Time) string {
	if pz == nil {
		return ""
	}
	steps := sMap(pz, "steps")
	sent := 0
	last := ""
	for _, st := range pzSteps {
		if steps[st.Key] != nil {
			sent++
			last = st.Name
		}
	}
	switch sStr(pz, "st") {
	case "wait":
		return "Сценарий: PDF и предложение уйдут " + pzWhen(sTime(pz, "nextAt"), now)
	case "active":
		nx := ""
		for _, st := range pzSteps {
			if st.Key == sStr(pz, "next") {
				nx = st.Name
			}
		}
		return fmt.Sprintf("Сценарий: отправлено %d из 4 (%s), дальше %s %s", sent, last, nx, pzWhen(sTime(pz, "nextAt"), now))
	case "stopped":
		t := "Сценарий остановлен: " + sStr(pz, "why")
		if r := sTime(pz, "remindAt"); !r.IsZero() {
			t += ", напомнить " + r.In(almaty).Format("02.01")
		}
		return t
	case "done":
		return "Сценарий завершён: отправлено 4 из 4"
	}
	return ""
}

func pzWhen(t, now time.Time) string {
	if t.IsZero() {
		return ""
	}
	if !t.After(now) {
		return "сейчас"
	}
	return dayWord(now, t)
}

// SeqList: GET /sales/seq → the sequence line of every lead that has one.
func (s *ClubSales) SeqList(c *gin.Context) {
	crm := s.read(c.Request.Context(), "club", "bs_crm")
	now := s.now()
	out := map[string]string{}
	for _, l := range asList(crm["leads"]) {
		if m, _ := l.(map[string]any); m != nil && m["pz"] != nil {
			out[sStr(m, "id")] = pzStatus(sMap(m, "pz"), now)
		}
	}
	c.JSON(http.StatusOK, gin.H{"seq": out})
}

// ── фон: шаги сценария ──

type pzJob struct {
	lead map[string]any
	step string
}

// RazborTick sends the due steps (inside the window) and stops what must stop.
func (s *ClubSales) RazborTick(ctx context.Context) int {
	now := s.now()
	cfg := s.Settings(ctx)
	var jobs []pzJob
	err := s.mutate(ctx, "club", "bs_crm", func(crm map[string]any) bool {
		jobs = nil
		changed := false
		for _, x := range asList(crm["leads"]) {
			l, _ := x.(map[string]any)
			pz := sMap(l, "pz")
			if pz == nil {
				continue
			}
			st := sStr(pz, "st")
			// «Не сейчас»: напомнить в выбранный день
			if st == "stopped" {
				if r := sTime(pz, "remindAt"); !r.IsZero() && !now.Before(r) && salesWindow(now) && !cfg.SeqOff {
					delete(pz, "remindAt")
					pz["remindedAt"] = rfc(now)
					jobs = append(jobs, pzJob{copyMap(l), "later"})
					changed = true
				}
				continue
			}
			if st != "active" && st != "wait" {
				continue
			}
			col := sStr(l, "col")
			start := sTime(pz, "offerAt")
			if start.IsZero() {
				start = sTime(pz, "start")
			}
			switch {
			case col == "won":
				pzStop(l, pz, now, "оплатил, теперь резидент")
				changed = true
				continue
			case col == "lost":
				pzStop(l, pz, now, "отказ")
				changed = true
				continue
			case st == "active" && sTime(l, "tgInAt").After(start):
				pzStop(l, pz, now, "лид ответил")
				changed = true
				continue
			}
			if cfg.SeqOff || !salesWindow(now) {
				continue
			}
			nextAt := sTime(pz, "nextAt")
			if nextAt.IsZero() || now.Before(nextAt) {
				continue
			}
			// the latest step that is due (a server that slept does not send three at once)
			offer := sTime(pz, "offerAt")
			steps := sMap(pz, "steps")
			if steps == nil {
				steps = map[string]any{}
				pz["steps"] = steps
			}
			step := "offer"
			if !offer.IsZero() {
				step = ""
				var due []string
				for _, ps := range pzSteps[1:] {
					if steps[ps.Key] == nil && !now.Before(offer.AddDate(0, 0, ps.Days)) {
						due = append(due, ps.Key)
					}
				}
				if len(due) == 0 {
					continue
				}
				step = due[len(due)-1]
				for _, k := range due[:len(due)-1] {
					steps[k] = "skipped " + rfc(now)
				}
			}
			jobs = append(jobs, pzJob{copyMap(l), step})
			// marked before the send: a failed send is not repeated every 5 minutes
			steps[step] = rfc(now)
			if step == "offer" {
				pz["offerAt"], pz["st"] = rfc(now), "active"
				offer = now
			}
			nx := ""
			for i, ps := range pzSteps {
				if ps.Key == step && i+1 < len(pzSteps) {
					nx = pzSteps[i+1].Key
					pz["nextAt"] = rfc(offer.AddDate(0, 0, pzSteps[i+1].Days))
				}
			}
			if nx == "" {
				pz["st"] = "done"
				delete(pz, "next")
				delete(pz, "nextAt")
			} else {
				pz["next"] = nx
			}
			changed = true
		}
		return changed
	})
	if err != nil {
		log.Printf("sales: razbor tick: %v", err)
		return 0
	}
	sent := 0
	for _, j := range jobs {
		if err := s.sendStep(ctx, cfg, j.lead, j.step, now); err != nil {
			log.Printf("sales: %s %s: %v", j.step, sStr(j.lead, "id"), err)
			_ = s.mutateLead(ctx, byLeadID(sStr(j.lead, "id")), func(l map[string]any) bool {
				addLog(l, now, "Сценарий: не отправилось ("+j.step+"): "+err.Error())
				return true
			})
			continue
		}
		sent++
		_ = s.mutateLead(ctx, byLeadID(sStr(j.lead, "id")), func(l map[string]any) bool {
			for _, ps := range pzSteps {
				if ps.Key == j.step {
					addLog(l, now, "Сценарий после разбора: отправлено «"+ps.Name+"»")
				}
			}
			if j.step == "later" {
				addLog(l, now, "Отложено: напомнили о клубе, как просил")
			}
			return true
		})
	}
	return sent
}

func copyMap(m map[string]any) map[string]any {
	b, _ := json.Marshal(m)
	out := map[string]any{}
	_ = json.Unmarshal(b, &out)
	return out
}

func pzKB(leadID string) map[string]any {
	return kb(row(map[string]any{"text": "✅ Хочу в клуб", "callback_data": pzPrefix + "join"}),
		row(map[string]any{"text": "💬 Есть вопрос", "callback_data": pzPrefix + "ask"}, map[string]any{"text": "⏸ Не сейчас", "callback_data": pzPrefix + "no"}))
}

// stepText: the text of one step for the lead.
func (s *ClubSales) stepText(ctx context.Context, cfg SalesCfg, l map[string]any, step string, now time.Time) string {
	r := s.razborSum(ctx, sStr(l, "id"))
	var plans []diagPlan
	var steps []string
	if r != nil {
		for _, g := range r.Form.Diag {
			plans = append(plans, planForDiag(g.Title, g.Why))
		}
		steps = r.Form.Steps
	}
	offerAt := sTime(sMap(l, "pz"), "offerAt")
	if offerAt.IsZero() {
		offerAt = now
	}
	vals := map[string]string{"{имя}": firstName(sStr(l, "name")), "{цены}": pricesLine(cfg), "{бонус}": bonusLine(cfg, offerAt)}
	var dn []string
	for _, p := range plans {
		dn = append(dn, p.Title)
	}
	vals["{диагнозы}"] = strings.Join(dn, ", ")
	vals["{что_даст_клуб}"] = clubForDiagnoses(plans)
	if len(steps) > 0 {
		vals["{шаг}"] = "«" + strings.TrimRight(steps[0], ".") + "»"
	}
	for _, p := range plans {
		if len(p.Tools) > 0 {
			vals["{инструмент}"] = "Инструмент в помощь: «" + p.Tools[0].Title + "», шаблон: " + toolURL(p.Tools[0].ID) + "\n\n"
			break
		}
	}
	if step == "d5" {
		vals["{кейс}"] = s.caseFor(ctx, plans, sStr(l, "niche"))
	}
	key := step
	if step == "later" {
		key = "later"
	}
	return salesFill(cfg.Text(key), vals)
}

// sendStep sends one step (the offer with the PDF first).
func (s *ClubSales) sendStep(ctx context.Context, cfg SalesCfg, l map[string]any, step string, now time.Time) error {
	tg := leadTg(l)
	text := s.stepText(ctx, cfg, l, step, now)
	id := sStr(l, "id")
	if tg == 0 {
		phone := waDigits(sStr(l, "phone"))
		if s.WA == nil || len(phone) < 11 {
			return fmt.Errorf("нет Telegram и WhatsApp")
		}
		if step == "offer" {
			text = "Итоги разбора (PDF): " + s.openURL("razbor", id) + "\n\n" + text
		}
		text += "\n\nОтветьте на это сообщение: «Хочу в клуб», «Есть вопрос» или «Не сейчас»."
		return s.WA(ctx, bot.WAMessage{Resident: sStr(l, "name"), Phone: phone, Kind: "lead", Key: "pz|" + id + "|" + step + "|" + sStr(sMap(l, "pz"), "start"), Text: text})
	}
	if step == "offer" {
		if r := s.razborSum(ctx, id); r != nil {
			pdf, err := tplpdf.RenderCallSummary(razborDoc(r))
			if err != nil {
				return err
			}
			key := "razbor:" + id + ":" + strconv.FormatInt(now.UnixNano(), 36)
			if err := s.docLead(ctx, tg, key, razborPDFName(r.Name, r.Form.Date), pdf, "Итоги вашего разбора: диагнозы, первые шаги и цифры", nil); err != nil {
				return err
			}
		}
	}
	if err := s.sendLead(ctx, tg, text, pzKB(id)); err != nil {
		return err
	}
	if step == "offer" && s.F != nil { // R55: the step «offer» of the funnel's video library
		s.F.sendStepVideo(ctx, tg, "offer", leadFirst(l))
	}
	return nil
}

// ── кнопки лида ──

// HandleCallback: the lead's «Хочу в клуб», «Есть вопрос», «Не сейчас» and «когда напомнить».
func (s *ClubSales) HandleCallback(ctx context.Context, cb bot.CallbackUpdate) (string, bool) {
	if !strings.HasPrefix(cb.Data, pzPrefix) {
		return "", false
	}
	if s.F != nil {
		s.F.noteCallback(ctx, cb)
	}
	act := strings.TrimPrefix(cb.Data, pzPrefix)
	now := s.now()
	tg := cb.ChatID
	var lead map[string]any
	var col0 string
	err := s.mutateLead(ctx, byLeadTg(tg), func(l map[string]any) bool {
		pz := sMap(l, "pz")
		if pz == nil {
			pz = map[string]any{"st": "stopped", "start": rfc(now)}
			l["pz"] = pz
		}
		col0 = sStr(l, "col")
		switch {
		case act == "join":
			if pz["st"] == "active" || pz["st"] == "wait" {
				pzStop(l, pz, now, "хочет в клуб")
			}
			delete(pz, "remindAt")
			if col0 != "won" {
				l["col"] = "decide"
			}
			l["next"], l["nextAt"] = "Связаться: хочет в клуб", now.In(almaty).Format("2006-01-02")
			l["hot"] = true
			addLog(l, now, "Нажал «Хочу в клуб»: этап «Решение»")
		case act == "ask":
			if pz["st"] == "active" || pz["st"] == "wait" {
				pzStop(l, pz, now, "задал вопрос")
			}
			l["next"], l["nextAt"] = "Ответить на вопрос о клубе", now.In(almaty).Format("2006-01-02")
			addLog(l, now, "Нажал «Есть вопрос»")
		case act == "no":
			if pz["st"] == "active" || pz["st"] == "wait" {
				pzStop(l, pz, now, "не сейчас")
			}
			if col0 != "won" {
				l["col"] = "later"
			}
			addLog(l, now, "Нажал «Не сейчас»: этап «Отложено»")
		case strings.HasPrefix(act, "rem:"):
			days, _ := strconv.Atoi(strings.TrimPrefix(act, "rem:"))
			if days > 0 {
				d := now.In(almaty).AddDate(0, 0, days)
				at := time.Date(d.Year(), d.Month(), d.Day(), 11, 0, 0, 0, almaty)
				pz["remindAt"] = rfc(at)
				l["next"], l["nextAt"] = "Напомнить о клубе", at.Format("2006-01-02")
				addLog(l, now, "Просит напомнить о клубе "+at.Format("02.01.2006"))
			} else {
				delete(pz, "remindAt")
				addLog(l, now, "Просит не напоминать о клубе")
			}
		default:
			return false
		}
		lead = copyMap(l)
		return true
	})
	if err != nil {
		return "Не получилось, попробуйте ещё раз", true
	}
	if lead == nil {
		return "", true
	}
	name := sStr(lead, "name")
	who := name
	if u := sStr(lead, "tg"); u != "" {
		who += " " + u
	}
	switch {
	case act == "join":
		_ = s.sendLead(ctx, tg, "Отлично! Команда напишет вам лично"+workWhen(now)+": расскажем, как начать, и пришлём ссылку на оплату.", nil)
		s.team(ctx, fmt.Sprintf("🔥 %s нажал «Хочу в клуб» после разбора. Этап «Решение».\nСвяжитесь сегодня: договор и оплата.\n🆔 %d", who, tg), nil)
		return "Передали команде", true
	case act == "ask":
		_ = s.sendLead(ctx, tg, "Напишите вопрос одним сообщением прямо сюда. Его увидят Рустам и Береке и ответят лично"+workWhen(now)+".", nil)
		s.team(ctx, fmt.Sprintf("💬 %s хочет задать вопрос о клубе после разбора. Вопрос придёт отдельным сообщением, ответьте ему лично.\n🆔 %d", who, tg), nil)
		return "", true
	case act == "no":
		_ = s.sendLead(ctx, tg, "Понял, без спешки. Когда напомнить о клубе?", kb(
			row(map[string]any{"text": "Через 2 недели", "callback_data": pzPrefix + "rem:14"}, map[string]any{"text": "Через месяц", "callback_data": pzPrefix + "rem:30"}),
			row(map[string]any{"text": "Через 3 месяца", "callback_data": pzPrefix + "rem:90"}, map[string]any{"text": "Не напоминать", "callback_data": pzPrefix + "rem:0"})))
		if col0 != "later" {
			s.team(ctx, fmt.Sprintf("⏸ %s после разбора: «Не сейчас». Этап «Отложено», бот спросил, когда напомнить.\n🆔 %d", who, tg), nil)
		}
		return "", true
	case strings.HasPrefix(act, "rem:"):
		if r := sTime(sMap(lead, "pz"), "remindAt"); !r.IsZero() {
			_ = s.sendLead(ctx, tg, "Хорошо, напомню "+ruDate(r)+". Если вопрос появится раньше, просто напишите сюда.", nil)
		} else {
			_ = s.sendLead(ctx, tg, "Хорошо, не буду напоминать. Если решите вернуться к клубу, просто напишите сюда.", nil)
		}
		return "", true
	}
	return "", true
}

func workWhen(now time.Time) string {
	if workHours(now) {
		return " в течение часа"
	}
	return " завтра после 10:00"
}

// OnCallPublished: a published call summary of a lead (its board's resident
// is a lead in «Записан на разбор» or «Разбор проведён», not a resident yet)
// starts the lead's sequence with that summary's diagnoses and plan.
func (s *ClubSales) OnCallPublished(ctx context.Context, callID, resident string, sum map[string]any) {
	if strings.TrimSpace(resident) == "" {
		return
	}
	if s.Boards != nil {
		if tg, _, err := s.Boards.ResidentTgByName(ctx, resident); err == nil && tg != 0 {
			return // a resident: the summary is already theirs
		}
	}
	crm := s.read(ctx, "club", "bs_crm")
	var lead map[string]any
	for _, x := range asList(crm["leads"]) {
		if m, _ := x.(map[string]any); m != nil && normName(sStr(m, "name")) == normName(resident) {
			if c := sStr(m, "col"); c == "meet" || c == "diag" {
				lead = m
				break
			}
		}
	}
	if lead == nil || (lead["pz"] != nil && sStr(sMap(lead, "pz"), "st") != "stopped") {
		return
	}
	f := RazborForm{Date: csS(sum["date"]), Situation: csS(sum["situation"]), PointA: csS(sum["pointA"]), PointB: csS(sum["pointB"])}
	if f.Date == "" {
		f.Date = s.now().In(almaty).Format("02.01.2006")
	}
	for _, d := range csMaps(sum["diagnoses"], "title") {
		f.Diag = append(f.Diag, struct {
			Title string `json:"title"`
			Why   string `json:"why"`
		}{csS(d["title"]), csS(d["why"])})
	}
	for _, p := range csMaps(sum["plan"], "what") {
		f.Steps = append(f.Steps, csFirst(p, "what", "text"))
	}
	if len(f.Steps) == 0 {
		for _, p := range csMaps(sum["solutions"], "text") {
			f.Steps = append(f.Steps, csS(p["text"]))
		}
	}
	for _, n := range csMaps(sum["numbers"], "value") {
		f.Numbers = append(f.Numbers, struct {
			Label string `json:"label"`
			Value string `json:"value"`
		}{csS(n["label"]), csS(n["value"])})
	}
	f.clean()
	if len(f.Diag) == 0 {
		return
	}
	if len(f.Diag) > 3 {
		f.Diag = f.Diag[:3]
	}
	if len(f.Steps) > 3 {
		f.Steps = f.Steps[:3]
	}
	id := sStr(lead, "id")
	r := &razborSum{LeadID: id, TgID: leadTg(lead), Name: sStr(lead, "name"), Phone: sStr(lead, "phone"), Form: f, Source: "call", CallID: callID, At: rfc(s.now()), By: "server:callsum"}
	if err := s.saveRazborSum(ctx, r); err != nil {
		log.Printf("sales: call %s → lead %s: %v", callID, id, err)
		return
	}
	if err := s.StartSequence(ctx, id, "Саммари онлайн-разбора опубликовано"); err != nil {
		log.Printf("sales: call %s → lead %s: %v", callID, id, err)
	}
}

// AfterRazbor: the разбор of a booked lead is over (booking.go): the team
// is asked for the итоги, the form opens from the note.
func (s *ClubSales) AfterRazbor(ctx context.Context, tg int64, name, when string) {
	crm := s.read(ctx, "club", "bs_crm")
	l := findLeadByTg(asList(crm["leads"]), tg)
	if l == nil || l["pz"] != nil {
		return
	}
	link := ContentPlatformURL() + "#leads"
	s.team(ctx, fmt.Sprintf("📝 Разбор с %s прошёл (%s). Заполните итоги в карточке лида: 3 диагноза, 3 шага, цифры. Лид получит PDF и предложение клуба, дальше напоминания на 2, 5 и 10 день.\n%s", name, when, link), nil)
}

// PublicPDF: GET /api/v1/public/sales/:kind/:id/:sig.pdf, the open link in WhatsApp.
func (s *ClubSales) PublicPDF(c *gin.Context) {
	kind, id := c.Param("kind"), c.Param("id")
	sig := strings.TrimSuffix(c.Param("sig"), ".pdf")
	if len(s.Secret) == 0 || !hmacEq(sig, s.sign(kind, id)) {
		c.JSON(http.StatusNotFound, gin.H{"error": "not_found"})
		return
	}
	ctx := c.Request.Context()
	switch kind {
	case "razbor":
		r := s.razborSum(ctx, id)
		if r == nil {
			c.JSON(http.StatusNotFound, gin.H{"error": "not_found"})
			return
		}
		pdf, err := tplpdf.RenderCallSummary(razborDoc(r))
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "pdf"})
			return
		}
		servePDF(c, razborPDFName(r.Name, r.Form.Date), pdf)
	case "report":
		rep := s.reportByID(ctx, id)
		if rep == nil || rep.Status != "sent" {
			c.JSON(http.StatusNotFound, gin.H{"error": "not_found"})
			return
		}
		pdf, err := tplpdf.RenderReport(s.reportDoc(ctx, rep, false))
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "pdf"})
			return
		}
		servePDF(c, reportPDFName(rep), pdf)
	default:
		c.JSON(http.StatusNotFound, gin.H{"error": "not_found"})
	}
}

func hmacEq(a, b string) bool {
	if len(a) != len(b) {
		return false
	}
	var v byte
	for i := 0; i < len(a); i++ {
		v |= a[i] ^ b[i]
	}
	return v == 0
}
