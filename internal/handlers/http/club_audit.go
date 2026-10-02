package http

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/bnursik/business_surgery_backend/internal/club"
	"github.com/bnursik/business_surgery_backend/internal/middleware"
	"github.com/gin-gonic/gin"
)

// The data audit after the script v31 slips (club.Audit): which residents'
// values look damaged and what was likely right. Kept in the club doc
// bs_data_audit for the team (residents cannot read it: it is not in
// residentReadableKeys), recomputed after every import and every club
// action, and on demand. A fix goes the write-first way like any club
// write, so the sheet gets it too.

const DataAuditKey = "bs_data_audit"

// AuditSource reads what the audit needs.
type AuditSource interface {
	AuditInput(ctx context.Context, now time.Time) (club.AuditInput, error)
	AuditMarks(ctx context.Context) (string, error)
}

type ClubAudit struct {
	src  AuditSource
	docs interface {
		PutServerDoc(ctx context.Context, key, value string) error
	}
	gw  *AppGateway
	now func() time.Time

	mu    sync.Mutex
	marks string
	last  *club.AuditReport
}

func NewClubAudit(src AuditSource, docs interface {
	PutServerDoc(ctx context.Context, key, value string) error
}, gw *AppGateway) *ClubAudit {
	return &ClubAudit{src: src, docs: docs, gw: gw, now: time.Now}
}

// Run recomputes the audit and stores it.
func (a *ClubAudit) Run(ctx context.Context) (*club.AuditReport, error) {
	marks, _ := a.src.AuditMarks(ctx)
	in, err := a.src.AuditInput(ctx, a.now())
	if err != nil {
		return nil, err
	}
	rep := club.Audit(in)
	if a.docs != nil {
		b, _ := json.Marshal(rep)
		if err := a.docs.PutServerDoc(ctx, DataAuditKey, string(b)); err != nil {
			return &rep, err
		}
	}
	a.mu.Lock()
	a.marks, a.last = marks, &rep
	a.mu.Unlock()
	return &rep, nil
}

// Loop recomputes the audit after each import or club action.
func (a *ClubAudit) Loop(ctx context.Context) {
	t := time.NewTicker(time.Minute)
	defer t.Stop()
	for {
		if marks, err := a.src.AuditMarks(ctx); err == nil {
			a.mu.Lock()
			due := marks != a.marks
			a.mu.Unlock()
			if due {
				if _, err := a.Run(ctx); err != nil {
					log.Printf("data audit: %v", err)
				}
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// FixParams turns {name, field, value} into the club write that makes it:
// the meeting counters both at once (setMeetings, v32 order), the partner
// with setPartner (both sides), any other field with setResidentField.
func FixParams(r club.Resident, field, value string) (string, map[string]string, error) {
	known := false
	for _, f := range club.AuditFields {
		known = known || f == field
	}
	if !known {
		return "", nil, fmt.Errorf("поле «%s» не исправляется", field)
	}
	value = strings.TrimSpace(value)
	action := club.FixAction(field)
	p := map[string]string{"name": r.Name, "audit": "1"}
	switch action {
	case "setMeetings":
		n, err := strconv.ParseInt(value, 10, 64)
		if err != nil || n < 0 {
			return "", nil, fmt.Errorf("«%s» не число", value)
		}
		g, d := r.Granted, r.Done
		if field == "meetingsGranted" {
			g = n
		} else {
			d = n
		}
		p["granted"], p["done"] = strconv.FormatInt(g, 10), strconv.FormatInt(d, 10)
	case "setPartner":
		p["partner"] = value
	default:
		switch field {
		case "chatId":
			if n, err := strconv.ParseInt(value, 10, 64); value != "" && (err != nil || n <= 0) {
				return "", nil, fmt.Errorf("Chat ID «%s» не число", value)
			}
		default:
			if n, err := strconv.ParseInt(strings.NewReplacer(" ", "", ",", "").Replace(value), 10, 64); value != "" && (err != nil || n < 0) {
				return "", nil, fmt.Errorf("«%s» не число", value)
			}
		}
		p["field"], p["value"] = field, value
	}
	return action, p, nil
}

// Fix applies one correction as the team member tg.
func (a *ClubAudit) Fix(ctx context.Context, tg int64, name, field, value string) (map[string]any, error) {
	if a.gw == nil || a.gw.Writes == nil || a.gw.Club == nil {
		return nil, fmt.Errorf("club writes are not configured")
	}
	snap, err := a.gw.Club.LoadBundle(ctx, a.now())
	if err != nil {
		return nil, err
	}
	var res *club.Resident
	for i := range snap.Residents {
		if !snap.Residents[i].Archived && club.NormName(snap.Residents[i].Name) == club.NormName(name) {
			res = &snap.Residents[i]
			break
		}
	}
	if res == nil {
		return nil, fmt.Errorf("резидент «%s» не найден", name)
	}
	action, params, err := FixParams(*res, field, value)
	if err != nil {
		return nil, err
	}
	in := url.Values{}
	for k, v := range params {
		in.Set(k, v)
	}
	u := &platformTgUser{ID: tg}
	if n, ok := a.gw.Admins[tg]; ok {
		u.FirstName = n
	}
	q := a.gw.params(in, action, u)
	body := a.gw.Writes.Do(ctx, "platform", u, action, q, true)
	a.gw.logOp(ctx, "platform", u, action, q, body)
	var out map[string]any
	_ = json.Unmarshal(body, &out)
	if out == nil {
		out = map[string]any{}
	}
	out["action"], out["params"] = action, params
	return out, nil
}

// ── endpoints (team) ──

type ClubAuditModule struct {
	a      *ClubAudit
	secret []byte
}

func NewClubAuditModule(a *ClubAudit, secret []byte) *ClubAuditModule {
	return &ClubAuditModule{a: a, secret: secret}
}

func (m *ClubAuditModule) Register(r *gin.Engine) {
	g := r.Group("/api/v1/club")
	g.Use(middleware.AuthJWT(m.secret))
	g.Use(middleware.RequireRole("admin", "moderator"))
	g.GET("/audit", m.get)
	g.POST("/audit", m.run)
	g.POST("/audit/fix", m.fix)
}

// get godoc
// @Summary  The data audit: residents' values that look damaged by the script v31, with the likely right value
// @Tags     club
// @Security BearerAuth
// @Router   /api/v1/club/audit [get]
func (m *ClubAuditModule) get(c *gin.Context) {
	m.a.mu.Lock()
	rep := m.a.last
	m.a.mu.Unlock()
	if rep == nil {
		m.run(c)
		return
	}
	c.JSON(http.StatusOK, rep)
}

// run godoc
// @Summary  Recompute the data audit now (also stored in the club doc bs_data_audit)
// @Tags     club
// @Security BearerAuth
// @Router   /api/v1/club/audit [post]
func (m *ClubAuditModule) run(c *gin.Context) {
	rep, err := m.a.Run(c.Request.Context())
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, rep)
}

// fix godoc
// @Summary  Correct one value the audit found: {name, field, value}; kept on the server first, then written to the sheet
// @Description  field: tariff, meetingsGranted, meetingsDone, paidEntry, restEntry, renewDebt, months, chatId, partner. The counters go as setMeetings, the partner as setPartner, the rest as setResidentField (script v33).
// @Tags     club
// @Security BearerAuth
// @Router   /api/v1/club/audit/fix [post]
func (m *ClubAuditModule) fix(c *gin.Context) {
	var req struct {
		Name  string `json:"name"`
		Field string `json:"field"`
		Value any    `json:"value"`
	}
	if err := c.ShouldBindJSON(&req); err != nil || strings.TrimSpace(req.Name) == "" || req.Field == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "body must be {name, field, value}"})
		return
	}
	tg, err := strconv.ParseInt(strings.TrimPrefix(platformUser(c), "tg:"), 10, 64)
	if err != nil || tg <= 0 {
		c.JSON(http.StatusForbidden, gin.H{"error": "not_telegram_user"})
		return
	}
	value := ""
	switch v := req.Value.(type) {
	case string:
		value = v
	case float64:
		value = strconv.FormatFloat(v, 'f', -1, 64)
	case nil:
	default:
		value = fmt.Sprint(v)
	}
	ctx := c.Request.Context()
	out, err := m.a.Fix(ctx, tg, req.Name, req.Field, value)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	if e, _ := out["error"].(string); e != "" {
		c.JSON(http.StatusUnprocessableEntity, gin.H{"error": "script", "detail": e, "result": out})
		return
	}
	rep, err := m.a.Run(ctx)
	if err != nil {
		log.Printf("data audit: %v", err)
	}
	c.JSON(http.StatusOK, gin.H{"ok": true, "result": out, "audit": rep})
}
