package http

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/bnursik/business_surgery_backend/internal/bot"
	"github.com/bnursik/business_surgery_backend/internal/club"
	"github.com/bnursik/business_surgery_backend/internal/tplpdf"
	"github.com/gin-gonic/gin"
)

// R51 (3): «Итог года резидента (PDF) и продление в 1 клик: нужно и года, и
// 3-х месяцев».
//
// Итог периода (3 или 12 месяцев) собирается из досок резидента и данных
// клуба (sales_data.go): что диагностировали, что изменилось (метрики
// было/стало с графиками в PDF), закрытые задачи и инструменты, встречи,
// дисциплина отчётов, Gallup, план на следующий период. Черновик видит
// команда (предпросмотр PDF, правка плана и комментария), «Отправить»
// уходит резиденту файлом в Telegram (в окне 10:00-20:00) и появляется в
// его профиле на платформе и в приложении. Квартальный черновик сервер
// собирает сам за 7 дней до конца каждых 3 месяцев в клубе, годовой за 7
// дней до годовщины, команде приходит уведомление.
//
// Продление в 1 клик: под итогом, в профиле и в напоминаниях за 14 и 3 дня
// до конца пакета кнопки «Продлить на 3 месяца» и «Продлить на год» (цены
// из настроек). Нажатие: запрос на оплату (ссылка Kaspi клуба из настроек,
// не ссылка штрафов), статус «Ожидает оплату продления», уведомление
// команде. Когда команда вносит оплату резидента (ДДС, доход с резидентом)
// или нажимает «Оплата получена», пакет продлевается сам: конец пакета
// сдвигается на период, встречи считаются как «Продлить пакет» (новый
// пакет по 3 встречи в месяц плюс неиспользованные).
//
// club doc bs_res_reports = {items: [...], renew: {<normName>: {name,
// packEnd, pending: {months, amount, at, via}, reminded: [...], hist: [...]}}}

const (
	resReportsDoc = "bs_res_reports"
	rnPrefix      = "rn:"
)

type resReport struct {
	ID        string           `json:"id"`
	Resident  string           `json:"resident"`
	Months    int              `json:"months"`
	From      string           `json:"from"`
	To        string           `json:"to"`
	Status    string           `json:"status"` // draft | queued | sent
	Auto      string           `json:"auto,omitempty"`
	Doc       tplpdf.ReportDoc `json:"doc"`
	CreatedAt string           `json:"createdAt"`
	By        string           `json:"by,omitempty"`
	QueuedAt  string           `json:"queuedAt,omitempty"`
	SentAt    string           `json:"sentAt,omitempty"`
	SentVia   string           `json:"sentVia,omitempty"`
	Err       string           `json:"err,omitempty"`
}

func reportPDFName(r *resReport) string {
	p := "Итог 3 месяцев"
	if r.Months == 12 {
		p = "Итог года"
	}
	return p + " · " + r.Resident + " · " + r.To + ".pdf"
}

func periodWord(m int) string {
	if m == 12 {
		return "год в клубе"
	}
	return "3 месяца в клубе"
}

func (s *ClubSales) reportsDoc(ctx context.Context) ([]resReport, map[string]map[string]any) {
	doc := s.read(ctx, "club", resReportsDoc)
	b, _ := json.Marshal(doc["items"])
	var items []resReport
	_ = json.Unmarshal(b, &items)
	ren := map[string]map[string]any{}
	for k, v := range sMap(doc, "renew") {
		if m, _ := v.(map[string]any); m != nil {
			ren[k] = m
		}
	}
	return items, ren
}

func (s *ClubSales) reportByID(ctx context.Context, id string) *resReport {
	items, _ := s.reportsDoc(ctx)
	for i := range items {
		if items[i].ID == id {
			return &items[i]
		}
	}
	return nil
}

// saveReports changes the items and the renewals together.
func (s *ClubSales) saveReports(ctx context.Context, fn func(items []resReport, ren map[string]any) ([]resReport, bool)) error {
	return s.mutate(ctx, "club", resReportsDoc, func(doc map[string]any) bool {
		b, _ := json.Marshal(doc["items"])
		var items []resReport
		_ = json.Unmarshal(b, &items)
		ren := sMap(doc, "renew")
		if ren == nil {
			ren = map[string]any{}
		}
		out, ok := fn(items, ren)
		if !ok {
			return false
		}
		var raw []any
		b, _ = json.Marshal(out)
		_ = json.Unmarshal(b, &raw)
		doc["items"], doc["renew"] = raw, ren
		return true
	})
}

// reportDoc: the PDF content with today's prices.
func (s *ClubSales) reportDoc(ctx context.Context, r *resReport, draft bool) *tplpdf.ReportDoc {
	d := r.Doc
	cfg := s.Settings(ctx)
	d.Draft, d.YearPrice, d.Q3Price = draft, cfg.YearPrice, cfg.Q3Price
	return &d
}

func salesTariffMonths(sum int64) int {
	switch {
	case sum >= 1000000:
		return 12
	case sum >= 400000:
		return 3
	case sum > 0:
		return 1
	}
	return 3
}

func dayOf(t time.Time) time.Time {
	a := t.In(almaty)
	return time.Date(a.Year(), a.Month(), a.Day(), 0, 0, 0, 0, almaty)
}

// packEnd: when the resident's current package ends (the renewal's date, or
// counted from the date they joined by the tariff's length).
func packEnd(r club.Resident, st map[string]any, now time.Time) (time.Time, bool) {
	if t, err := time.ParseInLocation("2006-01-02", sStr(st, "packEnd"), almaty); err == nil {
		return t, true
	}
	if r.JoinedAt == nil {
		return time.Time{}, false
	}
	n := salesTariffMonths(r.Tariff)
	today := dayOf(now)
	end := dayOf(*r.JoinedAt).AddDate(0, n, 0)
	for i := 0; i < 400 && end.Before(today); i++ {
		end = end.AddDate(0, n, 0)
	}
	return end, true
}

// buildReport collects the data of one period.
func (s *ClubSales) buildReport(ctx context.Context, res club.Resident, months int, to time.Time) (*resReport, error) {
	if months != 12 {
		months = 3
	}
	to = dayOf(to)
	from := to.AddDate(0, -months, 0)
	if res.JoinedAt != nil && dayOf(*res.JoinedAt).After(from) {
		from = dayOf(*res.JoinedAt)
	}
	end := to.Add(24*time.Hour - time.Second)
	p := s.period(ctx, res.Name, from, end)
	d := tplpdf.ReportDoc{Resident: res.Name, Niche: p.Niche, City: p.City, Months: months,
		From: from.Format("02.01.2006"), To: to.Format("02.01.2006"), Metrics: p.Metrics, Organs: p.Organs,
		Diagnoses: p.Diagnoses, TasksDone: p.TasksDone, TasksTotal: p.TasksTotal, TasksList: p.TasksList, Tools: p.Tools,
		Cycles: p.Cycles, Gallup: p.Gallup}
	if s.Club != nil {
		if snap, err := s.Club.Load(ctx); err == nil && snap != nil {
			for _, m := range snap.Meetings {
				if normName(m.Resident) != normName(res.Name) || m.Date.Before(from) || m.Date.After(end) {
					continue
				}
				if m.Done {
					d.Meetings++
				}
				if !m.Date.After(to) {
					d.MeetPlan++
				}
			}
			for _, f := range snap.Fines {
				if normName(f.Name) == normName(res.Name) && !f.Date.Before(from) && !f.Date.After(end) {
					d.Fines++
				}
			}
		}
		if days, err := s.Club.ReportDays(ctx, from, end); err == nil {
			for _, x := range days {
				if (res.TgID != 0 && x.TgID == res.TgID) || (x.TgID == 0 && normName(x.Name) == normName(res.Name)) {
					d.ReportDays += x.Days
				}
			}
		}
	}
	if !res.Exception {
		d.PlanDays = int(end.Sub(from).Hours()/24 + 0.5)
		if d.ReportDays > d.PlanDays {
			d.PlanDays = d.ReportDays
		}
	}
	// the next period's plan: what is open, the goal, the diagnoses still in work
	var plan []string
	if p.PointB != "" {
		plan = append(plan, "Цель: "+p.PointB)
	}
	for _, g := range p.Diagnoses {
		if !g.Closed && len(plan) < 4 {
			plan = append(plan, "Закрыть диагноз «"+g.Title+"»")
		}
	}
	for _, t := range p.OpenTasks {
		if len(plan) >= 7 {
			break
		}
		plan = append(plan, t)
	}
	d.Plan = plan
	return &resReport{Resident: res.Name, Months: months, From: d.From, To: d.To, Status: "draft", Doc: d}, nil
}

func (s *ClubSales) residentByName(ctx context.Context, name string) (*club.Resident, error) {
	if s.Club == nil {
		return nil, fmt.Errorf("нет данных клуба")
	}
	list, err := s.Club.LoadResidents(ctx)
	if err != nil {
		return nil, err
	}
	for i := range list {
		if normName(list[i].Name) == normName(name) {
			return &list[i], nil
		}
	}
	return nil, fmt.Errorf("резидент не найден")
}

// addReport keeps a new draft.
func (s *ClubSales) addReport(ctx context.Context, r *resReport) error {
	now := s.now()
	r.ID = "rep_" + strconv.FormatInt(now.UnixNano(), 36)
	r.CreatedAt = rfc(now)
	return s.saveReports(ctx, func(items []resReport, ren map[string]any) ([]resReport, bool) {
		return append(items, *r), true
	})
}

// ── команда ──

// Reports: GET /sales/reports?name= → reports (all or one resident's).
func (s *ClubSales) Reports(c *gin.Context) {
	ctx := c.Request.Context()
	items, _ := s.reportsDoc(ctx)
	name := strings.TrimSpace(c.Query("name"))
	out := []gin.H{}
	for i := len(items) - 1; i >= 0; i-- {
		r := items[i]
		if name != "" && normName(r.Resident) != normName(name) {
			continue
		}
		out = append(out, reportView(&r))
	}
	c.JSON(http.StatusOK, gin.H{"items": out})
}

func reportView(r *resReport) gin.H {
	return gin.H{"id": r.ID, "resident": r.Resident, "months": r.Months, "from": r.From, "to": r.To, "status": r.Status,
		"auto": r.Auto, "createdAt": r.CreatedAt, "queuedAt": r.QueuedAt, "sentAt": r.SentAt, "sentVia": r.SentVia, "err": r.Err,
		"plan": r.Doc.Plan, "note": r.Doc.Note, "metrics": len(r.Doc.Metrics), "tasks": r.Doc.TasksDone, "meetings": r.Doc.Meetings,
		"pdf": "/api/v1/platform/sales/reports/" + r.ID + "/pdf"}
}

type newReportReq struct {
	Name   string `json:"name"`
	Months int    `json:"months"`
}

// NewReport: POST /sales/reports {name, months}: a draft for the team.
func (s *ClubSales) NewReport(c *gin.Context) {
	var req newReportReq
	if c.ShouldBindJSON(&req) != nil || strings.TrimSpace(req.Name) == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "bad_request"})
		return
	}
	ctx := c.Request.Context()
	res, err := s.residentByName(ctx, req.Name)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "not_found", "message": err.Error()})
		return
	}
	r, err := s.buildReport(ctx, *res, req.Months, s.now())
	if err == nil {
		r.By = platformUser(c)
		err = s.addReport(ctx, r)
	}
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "storage"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"report": reportView(r)})
}

// ReportPDF: GET /sales/reports/:id/pdf (a draft carries «ЧЕРНОВИК»).
func (s *ClubSales) ReportPDF(c *gin.Context) {
	r := s.reportByID(c.Request.Context(), c.Param("id"))
	if r == nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "not_found"})
		return
	}
	pdf, err := tplpdf.RenderReport(s.reportDoc(c.Request.Context(), r, r.Status == "draft"))
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "pdf", "message": err.Error()})
		return
	}
	servePDF(c, reportPDFName(r), pdf)
}

type putReportReq struct {
	Plan *[]string `json:"plan"`
	Note *string   `json:"note"`
}

// PutReport: PUT /sales/reports/:id {plan?, note?} (the team's edit before sending).
func (s *ClubSales) PutReport(c *gin.Context) {
	var req putReportReq
	if c.ShouldBindJSON(&req) != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "bad_json"})
		return
	}
	id := c.Param("id")
	var got *resReport
	err := s.saveReports(c.Request.Context(), func(items []resReport, ren map[string]any) ([]resReport, bool) {
		for i := range items {
			if items[i].ID != id {
				continue
			}
			if req.Plan != nil {
				var pl []string
				for _, x := range *req.Plan {
					if x = clip(noLongDash(x), 300); x != "" && len(pl) < 12 {
						pl = append(pl, x)
					}
				}
				items[i].Doc.Plan = pl
			}
			if req.Note != nil {
				items[i].Doc.Note = clip(noLongDash(*req.Note), 1500)
			}
			r := items[i]
			got = &r
			return items, true
		}
		return nil, false
	})
	if err != nil || got == nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "not_found"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"report": reportView(got)})
}

// SendReport: POST /sales/reports/:id/send: to the resident (now, or at 10:00).
func (s *ClubSales) SendReport(c *gin.Context) {
	id := c.Param("id")
	now := s.now()
	var got *resReport
	_ = s.saveReports(c.Request.Context(), func(items []resReport, ren map[string]any) ([]resReport, bool) {
		for i := range items {
			if items[i].ID == id && items[i].Status != "sent" {
				items[i].Status, items[i].QueuedAt, items[i].Err = "queued", rfc(now), ""
				r := items[i]
				got = &r
				return items, true
			}
		}
		return nil, false
	})
	if got == nil {
		c.JSON(http.StatusConflict, gin.H{"error": "not_draft"})
		return
	}
	go s.ReportTick(context.WithoutCancel(c.Request.Context()))
	when := "сейчас"
	if !salesWindow(now) {
		when = "в 10:00 по Алматы"
	}
	c.JSON(http.StatusOK, gin.H{"report": reportView(got), "when": when})
}

// DeleteReport: DELETE /sales/reports/:id (a draft only).
func (s *ClubSales) DeleteReport(c *gin.Context) {
	id := c.Param("id")
	_ = s.saveReports(c.Request.Context(), func(items []resReport, ren map[string]any) ([]resReport, bool) {
		out := items[:0]
		for _, r := range items {
			if r.ID != id || r.Status == "sent" {
				out = append(out, r)
			}
		}
		return out, len(out) != len(items)
	})
	c.JSON(http.StatusOK, gin.H{"ok": true})
}

// Renewals: GET /sales/renewals → every resident's package end and renewal state.
func (s *ClubSales) Renewals(c *gin.Context) {
	ctx := c.Request.Context()
	_, ren := s.reportsDoc(ctx)
	out := []gin.H{}
	if s.Club != nil {
		if list, err := s.Club.LoadResidents(ctx); err == nil {
			now := s.now()
			for _, r := range activeResidents(list) {
				st := ren[normName(r.Name)]
				row := gin.H{"name": r.Name, "pending": st["pending"]}
				if end, ok := packEnd(r, st, now); ok {
					row["packEnd"] = end.Format("2006-01-02")
					row["daysLeft"] = int(end.Sub(dayOf(now)).Hours() / 24)
				}
				if h := st["hist"]; h != nil {
					row["hist"] = h
				}
				out = append(out, row)
			}
		}
	}
	cfg := s.Settings(ctx)
	c.JSON(http.StatusOK, gin.H{"items": out, "kaspi": cfg.KaspiClub != "", "q3Price": cfg.Q3Price, "yearPrice": cfg.YearPrice})
}

// ── резидент ──

// meName: the resident behind the request (the team may look as ?name=).
func (s *ClubSales) meName(c *gin.Context) (string, bool) {
	if !isResident(c) {
		if n := strings.TrimSpace(c.Query("name")); n != "" {
			return n, true
		}
		c.JSON(http.StatusBadRequest, gin.H{"error": "name_required"})
		return "", false
	}
	if s.Boards == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "unavailable"})
		return "", false
	}
	n, active, err := s.Boards.ResidentByTg(c.Request.Context(), platformTgID(c))
	if err != nil || !active || n == "" {
		forbidden(c, "not_resident")
		return "", false
	}
	return n, true
}

// meView: the resident's consent, sent reports and renewal state.
func (s *ClubSales) meView(ctx context.Context, name string) gin.H {
	cfg := s.Settings(ctx)
	items, ren := s.reportsDoc(ctx)
	reps := []gin.H{}
	for i := len(items) - 1; i >= 0; i-- {
		r := items[i]
		if normName(r.Resident) == normName(name) && r.Status == "sent" {
			reps = append(reps, gin.H{"id": r.ID, "months": r.Months, "from": r.From, "to": r.To, "sentAt": r.SentAt})
		}
	}
	st := ren[normName(name)]
	renew := gin.H{"q3Price": cfg.Q3Price, "yearPrice": cfg.YearPrice, "kaspi": cfg.KaspiClub, "pending": st["pending"]}
	if res, err := s.residentByName(ctx, name); err == nil {
		if end, ok := packEnd(*res, st, s.now()); ok {
			renew["packEnd"] = end.Format("02.01.2006")
			renew["daysLeft"] = int(end.Sub(dayOf(s.now())).Hours() / 24)
		}
	}
	md, chosen := consentMode(s.consents(ctx)[normName(name)])
	return gin.H{"name": name, "consent": md, "consentChosen": chosen, "reports": reps, "renew": renew}
}

// Me: GET /sales/me.
func (s *ClubSales) Me(c *gin.Context) {
	name, ok := s.meName(c)
	if !ok {
		return
	}
	out := s.meView(c.Request.Context(), name)
	if isResident(c) { // R52: the resident sees the line about the default once (the team's view-as does not count)
		if n := s.consentNotice(c.Request.Context(), name); n != "" {
			out["consentNote"] = n
		}
	}
	c.JSON(http.StatusOK, out)
}

// PutMyConsent: PUT /sales/me/consent {mode}.
func (s *ClubSales) PutMyConsent(c *gin.Context) {
	name, ok := s.meName(c)
	if !ok {
		return
	}
	var r consentReq
	if c.ShouldBindJSON(&r) != nil || consentModes[r.Mode] == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "bad_mode"})
		return
	}
	by := "резидент на платформе"
	if !isResident(c) {
		by = "команда " + platformUser(c)
	}
	if err := s.setConsent(c.Request.Context(), name, r.Mode, by); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "storage"})
		return
	}
	c.JSON(http.StatusOK, s.meView(c.Request.Context(), name))
}

type renewReq struct {
	Months int `json:"months"`
}

// MyRenew: POST /sales/me/renew {months: 3|12}.
func (s *ClubSales) MyRenew(c *gin.Context) {
	name, ok := s.meName(c)
	if !ok {
		return
	}
	var r renewReq
	_ = c.ShouldBindJSON(&r)
	via := "платформа"
	if !isResident(c) {
		via = "команда на платформе"
	}
	res, err := s.RequestRenewal(c.Request.Context(), name, r.Months, via)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "renew", "message": err.Error()})
		return
	}
	out := s.meView(c.Request.Context(), name)
	out["request"] = res
	c.JSON(http.StatusOK, out)
}

// MyReportPDF: GET /sales/me/reports/:id/pdf (sent ones only).
func (s *ClubSales) MyReportPDF(c *gin.Context) {
	name, ok := s.meName(c)
	if !ok {
		return
	}
	r := s.reportByID(c.Request.Context(), c.Param("id"))
	if r == nil || normName(r.Resident) != normName(name) || (r.Status != "sent" && isResident(c)) {
		c.JSON(http.StatusNotFound, gin.H{"error": "not_found"})
		return
	}
	pdf, err := tplpdf.RenderReport(s.reportDoc(c.Request.Context(), r, r.Status != "sent"))
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "pdf"})
		return
	}
	servePDF(c, reportPDFName(r), pdf)
}

// ── приложение (initData) ──

func (s *ClubSales) appWho(c *gin.Context, g *AppGateway, tg string) (string, *platformTgUser, bool) {
	u, ok := g.identify(c, tg)
	if !ok {
		return "", nil, false
	}
	if _, admin := g.Admins[u.ID]; admin && strings.TrimSpace(c.Query("name")) != "" {
		return strings.TrimSpace(c.Query("name")), u, true
	}
	if s.Boards == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "unavailable"})
		return "", nil, false
	}
	n, active, err := s.Boards.ResidentByTg(c.Request.Context(), u.ID)
	if err != nil || !active || n == "" {
		c.JSON(http.StatusForbidden, gin.H{"error": "residents_only"})
		return "", nil, false
	}
	return n, u, true
}

// AppMe: GET /app/sales/me?_tg=.
func (s *ClubSales) AppMe(c *gin.Context, g *AppGateway) {
	name, _, ok := s.appWho(c, g, c.Query("_tg"))
	if !ok {
		return
	}
	out := s.meView(c.Request.Context(), name)
	if n := s.consentNotice(c.Request.Context(), name); n != "" {
		out["consentNote"] = n // R52
	}
	c.JSON(http.StatusOK, out)
}

type appSalesReq struct {
	TG     string `json:"_tg"`
	Mode   string `json:"mode"`
	Months int    `json:"months"`
	ID     string `json:"id"`
}

func readAppSales(c *gin.Context) appSalesReq {
	var r appSalesReq
	_ = c.ShouldBindJSON(&r)
	if r.TG == "" {
		r.TG = c.Query("_tg")
	}
	return r
}

// AppConsent: POST /app/sales/consent {mode}.
func (s *ClubSales) AppConsent(c *gin.Context, g *AppGateway) {
	r := readAppSales(c)
	name, _, ok := s.appWho(c, g, r.TG)
	if !ok {
		return
	}
	if consentModes[r.Mode] == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "bad_mode"})
		return
	}
	if err := s.setConsent(c.Request.Context(), name, r.Mode, "резидент в приложении"); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "storage"})
		return
	}
	c.JSON(http.StatusOK, s.meView(c.Request.Context(), name))
}

// AppRenew: POST /app/sales/renew {months}.
func (s *ClubSales) AppRenew(c *gin.Context, g *AppGateway) {
	r := readAppSales(c)
	name, _, ok := s.appWho(c, g, r.TG)
	if !ok {
		return
	}
	res, err := s.RequestRenewal(c.Request.Context(), name, r.Months, "приложение")
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "renew", "message": err.Error()})
		return
	}
	out := s.meView(c.Request.Context(), name)
	out["request"] = res
	c.JSON(http.StatusOK, out)
}

// AppReportSend: POST /app/sales/report/send {id}: the bot sends the PDF into the chat.
func (s *ClubSales) AppReportSend(c *gin.Context, g *AppGateway) {
	r := readAppSales(c)
	name, u, ok := s.appWho(c, g, r.TG)
	if !ok {
		return
	}
	rep := s.reportByID(c.Request.Context(), r.ID)
	if rep == nil || normName(rep.Resident) != normName(name) || rep.Status != "sent" {
		c.JSON(http.StatusNotFound, gin.H{"error": "not_found"})
		return
	}
	pdf, err := tplpdf.RenderReport(s.reportDoc(c.Request.Context(), rep, false))
	if err == nil && s.Doc != nil {
		key := "report:" + rep.ID + ":" + strconv.FormatInt(s.now().UnixNano(), 36)
		err = s.Doc(c.Request.Context(), u.ID, key, reportPDFName(rep), pdf, "", "Ваш итог: "+periodWord(rep.Months), nil)
	}
	if err != nil || s.Doc == nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": "send"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"ok": true})
}

// ── продление ──

func renewKBFor(cfg SalesCfg) map[string]any {
	var rows [][]map[string]any
	if cfg.Q3Price > 0 {
		rows = append(rows, row(map[string]any{"text": "🔁 Продлить на 3 месяца · " + tenge(cfg.Q3Price), "callback_data": rnPrefix + "3"}))
	}
	rows = append(rows, row(map[string]any{"text": "🔁 Продлить на год · " + tenge(cfg.YearPrice), "callback_data": rnPrefix + "12"}))
	return kb(rows...)
}

// RequestRenewal: the resident asked to renew: the payment request, the
// status «Ожидает оплату продления», the team's note. Returns what to show.
func (s *ClubSales) RequestRenewal(ctx context.Context, name string, months int, via string) (gin.H, error) {
	cfg := s.Settings(ctx)
	if months != 12 && months != 3 {
		return nil, fmt.Errorf("период: 3 или 12 месяцев")
	}
	amount := cfg.YearPrice
	if months == 3 {
		if cfg.Q3Price <= 0 {
			return nil, fmt.Errorf("продление на 3 месяца сейчас не предлагается")
		}
		amount = cfg.Q3Price
	}
	now := s.now()
	again := false
	err := s.saveReports(ctx, func(items []resReport, ren map[string]any) ([]resReport, bool) {
		k := normName(name)
		st, _ := ren[k].(map[string]any)
		if st == nil {
			st = map[string]any{"name": strings.TrimSpace(name)}
		}
		if p := sMap(st, "pending"); p != nil && int(anyInt(p["months"])) == months {
			again = true
		}
		st["pending"] = map[string]any{"months": months, "amount": amount, "at": rfc(now), "via": via}
		ren[k] = st
		return items, true
	})
	if err != nil {
		return nil, err
	}
	period := map[int]string{3: "3 месяца", 12: "год"}[months]
	if !again {
		// R70: «Оплата получена» right under the note, no trip to the platform
		var keys map[string]any
		if s.Boards != nil {
			if tg, _, err := s.Boards.ResidentTgByName(ctx, name); err == nil && tg != 0 {
				keys = kb(row(map[string]any{"text": "✅ Оплата получена", "callback_data": RenewPaidPrefix + strconv.FormatInt(tg, 10)}))
			}
		}
		s.team(ctx, fmt.Sprintf("🔁 %s хочет продлить резидентство на %s (%s), %s. Статус «Ожидает оплату продления».\n%s\nКогда оплата придёт, нажмите «Оплата получена» ниже (или внесите её в ДДС, доход, резидент %s): пакет продлится сам.",
			name, period, tenge(amount), via, map[bool]string{true: "Ссылка Kaspi ушла резиденту.", false: "⚠️ Ссылка Kaspi клуба не задана в настройках: пришлите реквизиты сами."}[cfg.KaspiClub != ""], name), keys)
		if s.Boards != nil {
			if tg, _, err := s.Boards.ResidentTgByName(ctx, name); err == nil && tg != 0 {
				_ = s.mutateLead(ctx, byLeadTg(tg), func(l map[string]any) bool {
					l["next"], l["nextAt"] = "Ожидает оплату продления ("+period+")", now.In(almaty).Format("2006-01-02")
					addLog(l, now, "Запросил продление на "+period+": ожидает оплату")
					return true
				})
			}
		}
	}
	out := gin.H{"months": months, "amount": amount, "kaspi": cfg.KaspiClub, "status": "Ожидает оплату продления"}
	return out, nil
}

// RenewCallback: «Продлить на 3 месяца / на год» under the report or a reminder.
func (s *ClubSales) RenewCallback(ctx context.Context, cb bot.CallbackUpdate) (string, bool) {
	if !strings.HasPrefix(cb.Data, rnPrefix) || s.Boards == nil {
		return "", false
	}
	months, _ := strconv.Atoi(strings.TrimPrefix(cb.Data, rnPrefix))
	name, active, err := s.Boards.ResidentByTg(ctx, cb.FromID)
	if err != nil || name == "" || !active {
		return "Это для резидентов клуба", true
	}
	res, err := s.RequestRenewal(ctx, name, months, "бот")
	if err != nil {
		return err.Error(), true
	}
	text := fmt.Sprintf("Продление на %s: %s.", map[int]string{3: "3 месяца", 12: "год"}[months], tenge(anyInt(res["amount"])))
	var keys map[string]any
	if k := fmt.Sprint(res["kaspi"]); k != "" && k != "<nil>" {
		text += "\n\nОплатите через Kaspi по кнопке ниже" + kaspiSum(k, anyInt(res["amount"])) + ". Как только команда увидит оплату, пакет продлится сам и придёт подтверждение."
		keys = kb(row(map[string]any{"text": "💳 Оплатить " + tenge(anyInt(res["amount"])) + " (Kaspi)", "url": k}))
	} else {
		text += "\n\nКоманда пришлёт ссылку на оплату" + workWhen(s.now()) + ". Как только оплата придёт, пакет продлится сам."
	}
	if s.Send != nil {
		_ = s.Send(ctx, cb.ChatID, text, keys)
	}
	return "Запрос на продление отправлен", true
}

// RenewPaidPrefix: R70: the owner's «Оплата получена» under a renewal request.
const RenewPaidPrefix = "rnp:"

// RenewPaidCallback: «Оплата получена» in the bot, as the card's button.
func (s *ClubSales) RenewPaidCallback(ctx context.Context, cb bot.CallbackUpdate) (string, bool) {
	if !strings.HasPrefix(cb.Data, RenewPaidPrefix) || s.Boards == nil {
		return "", false
	}
	tg, _ := strconv.ParseInt(strings.TrimPrefix(cb.Data, RenewPaidPrefix), 10, 64)
	name, _, err := s.Boards.ResidentByTg(ctx, tg)
	if err != nil || name == "" {
		return "Резидент не найден", true
	}
	done, err := s.applyRenewal(ctx, name, 0, "команда: оплата получена (бот)")
	if err != nil {
		return clip("Не получилось: "+err.Error(), 190), true
	}
	if !done {
		return "Продление " + name + " уже оплачено", true
	}
	return "Продление " + name + " оплачено", true
}

// RenewPaidByTeam: POST /sales/renewals/paid {name}: the team saw the money.
func (s *ClubSales) RenewPaidByTeam(c *gin.Context) {
	var r consentReq
	if c.ShouldBindJSON(&r) != nil || strings.TrimSpace(r.Name) == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "bad_request"})
		return
	}
	done, err := s.applyRenewal(c.Request.Context(), r.Name, 0, "команда: оплата получена")
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "renew", "message": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"ok": true, "renewed": done})
}

// OnClubWrite: a club write went through (app_writes.go, sheet_off.go): a
// resident's payment closes their pending renewal.
func (s *ClubSales) OnClubWrite(ctx context.Context, action string, p map[string]string) {
	if action != "addPayment" || p["type"] != "income" || strings.TrimSpace(p["resident"]) == "" || strings.Contains(p["src"], "Штраф") {
		return
	}
	amount, _ := strconv.ParseInt(strings.TrimSpace(p["amount"]), 10, 64)
	if _, err := s.applyRenewal(ctx, p["resident"], amount, "оплата в ДДС"); err != nil {
		log.Printf("sales: renewal %s: %v", p["resident"], err)
	}
}

// applyRenewal extends a pending renewal's package. amount: the payment just
// entered (0: the team's button). done false: nothing was pending.
func (s *ClubSales) applyRenewal(ctx context.Context, name string, amount int64, why string) (bool, error) {
	_, ren := s.reportsDoc(ctx)
	st := ren[normName(name)]
	pend := sMap(st, "pending")
	if pend == nil {
		return false, nil
	}
	months := int(anyInt(pend["months"]))
	if months <= 0 {
		months = 3
	}
	res, err := s.residentByName(ctx, name)
	if err != nil {
		return false, err
	}
	now := s.now()
	end, ok := packEnd(*res, st, now)
	if !ok || end.Before(dayOf(now)) {
		end = dayOf(now)
	}
	newEnd := end.AddDate(0, months, 0)
	// meetings as «Продлить пакет»: a new package (3 a month) plus the unused ones.
	// A payment the server already applied (addPayment ≥ tariff) renewed the
	// package by the tariff: its leftover is counted back out first.
	left := res.Granted - res.Done
	if amount > 0 && res.Tariff > 0 && amount/res.Tariff >= 1 && res.Done == 0 {
		left = res.Granted - int64(salesTariffMonths(res.Tariff))*3*(amount/res.Tariff)
	}
	total := int64(months)*3 + left
	if total < 0 {
		total = 0
	}
	if s.Write != nil {
		if err := s.Write(ctx, "setMeetings", map[string]string{"name": res.Name, "done": "0", "granted": strconv.FormatInt(total, 10)}); err != nil {
			log.Printf("sales: renewal %s: meetings: %v", res.Name, err)
		}
	}
	err = s.saveReports(ctx, func(items []resReport, ren map[string]any) ([]resReport, bool) {
		k := normName(name)
		st, _ := ren[k].(map[string]any)
		if st == nil || st["pending"] == nil {
			return nil, false
		}
		hist, _ := st["hist"].([]any)
		hist = append(hist, map[string]any{"months": months, "amount": pend["amount"], "paid": amount, "at": rfc(now), "why": why, "from": end.Format("2006-01-02"), "to": newEnd.Format("2006-01-02")})
		st["hist"], st["packEnd"] = hist, newEnd.Format("2006-01-02")
		delete(st, "pending")
		ren[k] = st
		return items, true
	})
	if err != nil {
		return false, err
	}
	period := map[int]string{3: "3 месяца", 12: "год"}[months]
	s.team(ctx, fmt.Sprintf("✅ Продление %s на %s оплачено (%s). Пакет до %s, встреч в пакете: %d.", res.Name, period, why, newEnd.Format("02.01.2006"), total), nil)
	tg := res.TgID
	if tg == 0 && s.Boards != nil {
		tg, _, _ = s.Boards.ResidentTgByName(ctx, res.Name)
	}
	text := fmt.Sprintf("%s, оплата получена, спасибо! Резидентство продлено на %s, до %s. Встреч в пакете: %d.", firstName(res.Name), period, ruDate(newEnd)+" "+strconv.Itoa(newEnd.Year()), total)
	if err := s.toResident(ctx, "notice", "renew|"+newEnd.Format("2006-01-02"), res.Name, tg, text, nil); err != nil {
		log.Printf("sales: renewal note %s: %v", res.Name, err)
	}
	return true, nil
}

// ── фон: черновики итогов, отправка, напоминания о продлении ──

// ReportTick: auto drafts, queued sends, the 14- and 3-day reminders.
func (s *ClubSales) ReportTick(ctx context.Context) {
	if s.Club == nil {
		return
	}
	cfg := s.Settings(ctx)
	now := s.now()
	list, err := s.Club.LoadResidents(ctx)
	if err != nil {
		return
	}
	items, ren := s.reportsDoc(ctx)
	has := map[string]bool{}
	for _, r := range items {
		if r.Auto != "" {
			has[r.Auto] = true
		}
	}
	if !cfg.ReportOff {
		for _, res := range activeResidents(list) {
			if res.JoinedAt == nil {
				continue
			}
			j := dayOf(*res.JoinedAt)
			// the next 3-month boundary
			b := j
			for k := 0; k < 200 && !b.After(dayOf(now)); k++ {
				b = b.AddDate(0, 3, 0)
			}
			if b.Sub(dayOf(now)) > 7*24*time.Hour {
				continue
			}
			months := 3
			if monthsBetween(j, b)%12 == 0 {
				months = 12
			}
			key := fmt.Sprintf("%s|%d|%s", normName(res.Name), months, b.Format("2006-01-02"))
			if has[key] {
				continue
			}
			r, err := s.buildReport(ctx, res, months, now)
			if err != nil {
				continue
			}
			r.Auto, r.By = key, "server"
			if err := s.addReport(ctx, r); err != nil {
				continue
			}
			has[key] = true
			s.team(ctx, fmt.Sprintf("📄 Готов черновик: %s, %s (%s - %s). Проверьте на платформе (Клуб → резидент → «Итоги и продление») и отправьте: резидент получит PDF и кнопки продления.\n%s",
				res.Name, map[int]string{3: "итог 3 месяцев", 12: "итог года"}[months], r.From, r.To, ContentPlatformURL()+"#sRes"), nil)
		}
	}
	if !salesWindow(now) {
		return
	}
	// queued reports
	items, ren = s.reportsDoc(ctx)
	for _, r := range items {
		if r.Status != "queued" {
			continue
		}
		via, err := s.deliverReport(ctx, cfg, &r)
		_ = s.saveReports(ctx, func(all []resReport, rn map[string]any) ([]resReport, bool) {
			for i := range all {
				if all[i].ID == r.ID && all[i].Status == "queued" {
					if err != nil {
						all[i].Status, all[i].Err = "draft", err.Error()
					} else {
						all[i].Status, all[i].SentAt, all[i].SentVia = "sent", rfc(now), via
					}
					return all, true
				}
			}
			return nil, false
		})
		if err != nil {
			s.team(ctx, "Итог «"+r.Resident+"» не отправился: "+err.Error(), nil)
		}
	}
	// reminders 14 and 3 days before the package ends
	for _, res := range activeResidents(list) {
		st := ren[normName(res.Name)]
		if st["pending"] != nil {
			continue
		}
		end, ok := packEnd(res, st, now)
		if !ok {
			continue
		}
		days := int(end.Sub(dayOf(now)).Hours() / 24)
		var which string
		switch {
		case days <= 14 && days > 7:
			which = "14"
		case days <= 3 && days >= 0:
			which = "3"
		default:
			continue
		}
		mark := which + "|" + end.Format("2006-01-02")
		if hasStr(st["reminded"], mark) {
			continue
		}
		_ = s.saveReports(ctx, func(all []resReport, rn map[string]any) ([]resReport, bool) {
			k := normName(res.Name)
			m, _ := rn[k].(map[string]any)
			if m == nil {
				m = map[string]any{"name": res.Name}
			}
			l, _ := m["reminded"].([]any)
			m["reminded"] = append(l, mark)
			rn[k] = m
			return all, true
		})
		tg := res.TgID
		if tg == 0 && s.Boards != nil {
			tg, _, _ = s.Boards.ResidentTgByName(ctx, res.Name)
		}
		text := salesFill(cfg.Text("renew"+which), map[string]string{"{имя}": firstName(res.Name), "{до}": ruDate(end), "{цены}": pricesLine(cfg)})
		if err := s.toResident(ctx, "notice", "renew"+mark, res.Name, tg, text, renewKBFor(cfg)); err != nil {
			log.Printf("sales: renew reminder %s: %v", res.Name, err)
		}
	}
}

func monthsBetween(a, b time.Time) int {
	return (b.Year()-a.Year())*12 + int(b.Month()) - int(a.Month())
}

func hasStr(v any, s string) bool {
	for _, x := range sStrs(v) {
		if x == s {
			return true
		}
	}
	return false
}

// deliverReport: the PDF and the renewal buttons to the resident.
func (s *ClubSales) deliverReport(ctx context.Context, cfg SalesCfg, r *resReport) (string, error) {
	tg := int64(0)
	if s.Boards != nil {
		tg, _, _ = s.Boards.ResidentTgByName(ctx, r.Resident)
	}
	if tg == 0 {
		if res, err := s.residentByName(ctx, r.Resident); err == nil {
			tg = res.TgID
		}
	}
	text := salesFill(cfg.Text("report"), map[string]string{"{имя}": firstName(r.Resident), "{период}": periodWord(r.Months), "{цены}": pricesLine(cfg)})
	if s.WARoute != nil && s.WARoute(ctx, r.Resident) != "" {
		t := text + "\n\nPDF: " + s.openURL("report", r.ID)
		if err := s.toResident(ctx, "notice", "report|"+r.ID, r.Resident, tg, t, renewKBFor(cfg)); err != nil {
			return "", err
		}
		return "whatsapp", nil
	}
	if tg == 0 {
		return "", fmt.Errorf("у резидента нет Telegram в списке клуба")
	}
	pdf, err := tplpdf.RenderReport(s.reportDoc(ctx, r, false))
	if err != nil {
		return "", err
	}
	if s.Doc == nil {
		return "", fmt.Errorf("нет бота")
	}
	key := "report:" + r.ID + ":" + strconv.FormatInt(s.now().UnixNano(), 36)
	if err := s.Doc(ctx, tg, key, reportPDFName(r), pdf, "", "Итог: "+periodWord(r.Months), nil); err != nil {
		return "", err
	}
	if err := s.toResident(ctx, "notice", "report|"+r.ID, r.Resident, tg, text, renewKBFor(cfg)); err != nil {
		return "", err
	}
	return "telegram", nil
}
