package http

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"math/rand"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/bnursik/business_surgery_backend/internal/middleware"
	pg "github.com/bnursik/business_surgery_backend/internal/repository/pg"
	"github.com/bnursik/business_surgery_backend/web"
	"github.com/gin-gonic/gin"
	"github.com/golang-jwt/jwt/v5"
)

// R83: «Нужен вход отдельный для менеджера, чтобы он работал внутри CRM
// качественно».
//
// The sales manager is a role of its own ("sales"). The owner invites him
// from Продажи → CRM → «Менеджеры» with a one-time link (72 hours); the
// manager opens it, signs in with his Telegram and from then on logs in
// with Telegram (or a personal 30-day link for a phone without Telegram).
//
// He sees one page, /crm: his leads with the next step and its date, the day
// plan, new leads nobody has taken (without phone numbers until taken), call
// and WhatsApp quick actions, scripts, booking of express-разборы and his own
// numbers. Nothing else of the platform: the server lets a "sales" token into
// /api/v1/manager/* only (Guard), finance, residents and boards are never
// sent; of the club he sees only the name of the resident who recommended a
// lead. Every action of his goes to a journal the owner reads.
//
// Stages have required fields: «Отказ» needs the reason, «Резидент» the sum,
// «Отложено» the date to come back, every working stage the next step with
// its date. The bot reminds the manager: the morning plan, a step with a
// time 10 minutes before, a lead the owner assigned to him.
//
// Storage: platform docs, no new tables. server/bs_managers (who, tokens as
// SHA-256 only), server/bs_mgrlog (the journal), the lead's own fields in
// club/bs_crm: mgr (id), mgrName, nextTime, mgrNotified, mgrRemind.

const (
	mgrDocKey    = "bs_managers"
	mgrLogKey    = "bs_mgrlog"
	mgrLogMax    = 3000
	mgrInviteTTL = 72 * time.Hour
	mgrLinkTTL   = 30 * 24 * time.Hour
	mgrPoolMax   = 60
	// RoleSales: the manager's role in the token.
	RoleSales = "sales"
)

// SalesMgr is one manager.
type SalesMgr struct {
	ID         int64  `json:"id"`
	Name       string `json:"name"`
	Tg         int64  `json:"tg,omitempty"`
	TgName     string `json:"tgName,omitempty"`
	InviteHash string `json:"inviteHash,omitempty"`
	InviteExp  string `json:"inviteExp,omitempty"`
	LinkHash   string `json:"linkHash,omitempty"`
	LinkExp    string `json:"linkExp,omitempty"`
	CreatedBy  string `json:"createdBy,omitempty"`
	CreatedAt  string `json:"createdAt,omitempty"`
	AcceptedAt string `json:"acceptedAt,omitempty"`
	RevokedAt  string `json:"revokedAt,omitempty"`
	LastSeen   string `json:"lastSeen,omitempty"`
	DigestDay  string `json:"digestDay,omitempty"`
}

func (m *SalesMgr) Active() bool { return m != nil && m.RevokedAt == "" }
func (m *SalesMgr) Key() string  { return "mgr:" + strconv.FormatInt(m.ID, 10) }

type mgrDoc struct {
	Seq      int64      `json:"seq"`
	Managers []SalesMgr `json:"managers"`
}

func (d *mgrDoc) byID(id int64) *SalesMgr {
	for i := range d.Managers {
		if d.Managers[i].ID == id {
			return &d.Managers[i]
		}
	}
	return nil
}

type mgrLogEntry struct {
	At       string `json:"at"`
	Mid      int64  `json:"mid"`
	Who      string `json:"who"`
	Action   string `json:"action"`
	Lead     string `json:"lead,omitempty"`
	LeadName string `json:"leadName,omitempty"`
	Detail   string `json:"detail,omitempty"`
}

type mgrLogDoc struct {
	Log []mgrLogEntry `json:"log"`
}

// SalesManagers: the role, its pages and its API.
type SalesManagers struct {
	Docs   funnelDocs
	Repo   *pg.PlatformRepo // sessions (nil: tokens without a session row)
	Auth   *PlatformAuthHandler
	AI     *PlatformAI // WhatsApp chat in the lead's card (nil: wa.me only)
	Secret []byte
	Send   func(ctx context.Context, chat int64, text string, kb map[string]any) error
	Admins []int64
	Now    func() time.Time

	rl      *rateLimiter
	mu      sync.Mutex
	cache   *mgrDoc
	cacheAt time.Time
	seen    map[int64]time.Time
}

func NewSalesManagers(docs funnelDocs, auth *PlatformAuthHandler, secret []byte) *SalesManagers {
	return &SalesManagers{Docs: docs, Auth: auth, Secret: secret, rl: newRateLimiter(), seen: map[int64]time.Time{}}
}

func (s *SalesManagers) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

// ── storage ──

// r83Mutate: read a JSON doc into T, change it, write it back (retries on a
// version conflict). fn false: nothing to write.
func r83Mutate[T any](ctx context.Context, docs funnelDocs, scope, key, by string, fn func(v *T) bool) error {
	for try := 0; try < 12; try++ {
		var v T
		base := 0
		d, err := docs.GetDoc(ctx, scope, key)
		if err != nil {
			return err
		}
		if d != nil {
			base = d.Version
			if !d.Deleted && d.Value != "" {
				_ = json.Unmarshal([]byte(d.Value), &v)
			}
		}
		if !fn(&v) {
			return nil
		}
		val, _ := json.Marshal(&v)
		if _, err = docs.PutDoc(ctx, scope, key, base, string(val), false, by); err == nil {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(time.Duration(5+try*7) * time.Millisecond):
		}
	}
	return pg.ErrPlatformConflict
}

func r83Read[T any](ctx context.Context, docs funnelDocs, scope, key string) (T, error) {
	var v T
	d, err := docs.GetDoc(ctx, scope, key)
	if err != nil || d == nil || d.Deleted || d.Value == "" {
		return v, err
	}
	err = json.Unmarshal([]byte(d.Value), &v)
	return v, err
}

func (s *SalesManagers) load(ctx context.Context, fresh bool) (*mgrDoc, error) {
	s.mu.Lock()
	if !fresh && s.cache != nil && s.now().Sub(s.cacheAt) < 10*time.Second {
		d := s.cache
		s.mu.Unlock()
		return d, nil
	}
	s.mu.Unlock()
	d, err := r83Read[mgrDoc](ctx, s.Docs, "server", mgrDocKey)
	if err != nil {
		return nil, err
	}
	s.mu.Lock()
	s.cache, s.cacheAt = &d, s.now()
	s.mu.Unlock()
	return &d, nil
}

func (s *SalesManagers) drop() {
	s.mu.Lock()
	s.cache = nil
	s.mu.Unlock()
}

func (s *SalesManagers) mutate(ctx context.Context, by string, fn func(d *mgrDoc) bool) error {
	err := r83Mutate(ctx, s.Docs, "server", mgrDocKey, by, fn)
	s.drop()
	return err
}

// ByTg: the active manager with this Telegram (nil: none).
func (s *SalesManagers) ByTg(ctx context.Context, tg int64) *SalesMgr {
	if tg == 0 {
		return nil
	}
	d, err := s.load(ctx, false)
	if err != nil {
		return nil
	}
	for i := range d.Managers {
		m := d.Managers[i]
		if m.Tg == tg && m.Active() {
			return &m
		}
	}
	return nil
}

// Allowed: the session guard's check of a manager's token (revoked at once,
// within the 10 s of the cache on another instance).
func (s *SalesManagers) Allowed(ctx context.Context, id int64, sub string) bool {
	d, err := s.load(ctx, false)
	if err != nil {
		return false
	}
	m := d.byID(id)
	if !m.Active() || m.Key() != sub {
		return false
	}
	s.mu.Lock()
	last := s.seen[id]
	busy := s.now().Sub(last) < 5*time.Minute
	if !busy {
		s.seen[id] = s.now()
	}
	s.mu.Unlock()
	if !busy {
		go func() {
			c, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			_ = s.mutate(c, "server:managers", func(d *mgrDoc) bool {
				if x := d.byID(id); x != nil {
					x.LastSeen = s.now().UTC().Format(time.RFC3339)
					return true
				}
				return false
			})
		}()
	}
	return true
}

func (s *SalesManagers) logAct(ctx context.Context, m *SalesMgr, action string, lead map[string]any, detail string) {
	e := mgrLogEntry{At: s.now().UTC().Format(time.RFC3339), Action: action, Detail: clip(detail, 300)}
	if m != nil {
		e.Mid, e.Who = m.ID, m.Name
	}
	if lead != nil {
		e.Lead, e.LeadName = sStr(lead, "id"), clip(firstNonBlank(sStr(lead, "name"), sStr(lead, "phone")), 80)
	}
	err := r83Mutate(context.WithoutCancel(ctx), s.Docs, "server", mgrLogKey, "server:managers", func(d *mgrLogDoc) bool {
		d.Log = append([]mgrLogEntry{e}, d.Log...)
		if len(d.Log) > mgrLogMax {
			d.Log = d.Log[:mgrLogMax]
		}
		return true
	})
	if err != nil {
		log.Printf("managers: journal: %v", err)
	}
}

// ── who asks ──

func mgrIDOf(c *gin.Context) int64 {
	u := platformUser(c)
	if !strings.HasPrefix(u, "mgr:") {
		return 0
	}
	id, _ := strconv.ParseInt(strings.TrimPrefix(u, "mgr:"), 10, 64)
	return id
}

func (s *SalesManagers) me(c *gin.Context) *SalesMgr {
	id := mgrIDOf(c)
	if id == 0 || platformRole(c) != RoleSales {
		c.JSON(http.StatusForbidden, gin.H{"error": "forbidden", "reason": "sales_only"})
		return nil
	}
	d, err := s.load(c.Request.Context(), false)
	if err != nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "try_again"})
		return nil
	}
	m := d.byID(id)
	if !m.Active() {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "manager_revoked", "detail": "Доступ менеджера закрыт. Напишите владельцу клуба"})
		return nil
	}
	cp := *m
	return &cp
}

func (s *SalesManagers) team(c *gin.Context) bool {
	r := platformRole(c)
	if (r != "admin" && r != "moderator") || assistOf(c) != nil {
		forbidden(c, "team_only")
		return false
	}
	return true
}

func (s *SalesManagers) teamName(c *gin.Context) string {
	tg := platformTgID(c)
	if s.Auth != nil {
		if n := s.Auth.team[tg]; n != "" {
			return n
		}
	}
	return platformUser(c)
}

// ── the CRM doc ──

var mgrCols = map[string]string{"new": "Новый", "work": "В работе", "qual": "Квалифицирован", "prepay": "Предоплата за разбор",
	"meet": "Записан на разбор", "diag": "Разбор проведён", "decide": "Решение", "won": "Резидент", "later": "Отложено", "lost": "Отказ"}

var mgrColOrder = []string{"new", "work", "qual", "prepay", "meet", "diag", "decide", "won", "later", "lost"}

var mgrLostReasons = []string{"Дорого", "Нет времени", "Не ЦА", "Ушёл к конкурентам", "Не отвечает", "Не сейчас", "Другое"}

var mgrSources = []string{"WhatsApp", "Telegram", "Instagram", "Сайт", "Рекомендация"}

func mgrOf(l map[string]any) int64 { return int64(claimInt(l["mgr"])) }

func leadClosed(col string) bool { return col == "won" || col == "lost" }

// leadActive: a stage where somebody works the lead (needs a next step).
func leadActive(col string) bool { return !leadClosed(col) && col != "later" }

func (s *SalesManagers) crm(ctx context.Context) (map[string]any, []any, error) {
	crm := map[string]any{}
	d, err := s.Docs.GetDoc(ctx, "club", "bs_crm")
	if err != nil {
		return nil, nil, err
	}
	if d != nil && !d.Deleted {
		_ = json.Unmarshal([]byte(d.Value), &crm)
	}
	if crm == nil {
		crm = map[string]any{}
	}
	leads, _ := crm["leads"].([]any)
	return crm, leads, nil
}

func findLeadID(leads []any, id string) map[string]any {
	for _, v := range leads {
		if m, ok := v.(map[string]any); ok && fmt.Sprint(m["id"]) == id {
			return m
		}
	}
	return nil
}

func (s *SalesManagers) today() string { return s.now().In(almaty).Format("2006-01-02") }

// leadDay: the lead's nextAt as YYYY-MM-DD (it may be dd.mm.yyyy in old cards).
func mgrDay(v string) string {
	v = strings.TrimSpace(v)
	if len(v) >= 10 && v[4] == '-' {
		return v[:10]
	}
	if t, err := time.Parse("02.01.2006", v); err == nil {
		return t.Format("2006-01-02")
	}
	return ""
}

func (s *SalesManagers) overdue(l map[string]any) bool {
	d := mgrDay(sStr(l, "nextAt"))
	return sStr(l, "next") != "" && d != "" && !leadClosed(sStr(l, "col")) && d < s.today()
}

var mgrLeadFields = []string{"id", "col", "name", "phone", "tg", "source", "niche", "goal", "sum", "note", "next", "nextAt", "nextTime",
	"lostReason", "hot", "date", "created", "waUnread", "waLast", "waAt", "razborAt", "razborSlot", "refName", "mgrName", "email", "wonAt"}

// leadView: what the manager sees of his lead. Nothing about money of the
// club or residents beyond the name of the one who recommended.
func (s *SalesManagers) leadView(l map[string]any, full bool) gin.H {
	out := gin.H{}
	for _, k := range mgrLeadFields {
		if v, ok := l[k]; ok && v != nil && v != "" {
			out[k] = v
		}
	}
	out["mgr"] = mgrOf(l)
	out["overdue"] = s.overdue(l)
	out["day"] = mgrDay(sStr(l, "nextAt"))
	if full {
		var logv []gin.H
		if lg, ok := l["log"].([]any); ok {
			for i := len(lg) - 1; i >= 0 && len(logv) < 60; i-- {
				e, _ := lg[i].(map[string]any)
				if e == nil {
					continue
				}
				logv = append(logv, gin.H{"at": e["at"], "text": e["text"], "by": e["byName"]})
			}
		}
		out["log"] = logv
	}
	return out
}

// poolView: a lead nobody has taken: enough to choose, no phone or Telegram.
func (s *SalesManagers) poolView(l map[string]any) gin.H {
	out := gin.H{"id": sStr(l, "id"), "col": sStr(l, "col"), "name": firstNonBlank(sStr(l, "name"), "Без имени"), "source": sStr(l, "source"),
		"niche": sStr(l, "niche"), "date": sStr(l, "date"), "created": sStr(l, "created"), "pool": true}
	if r := sStr(l, "refName"); r != "" {
		out["refName"] = r
	}
	if sStr(l, "phone") != "" {
		out["hasPhone"] = true
	}
	if sStr(l, "tg") != "" || leadTg(l) != 0 {
		out["hasTg"] = true
	}
	if l["hot"] == true {
		out["hot"] = true
	}
	return out
}

func inPool(l map[string]any) bool {
	return mgrOf(l) == 0 && (sStr(l, "col") == "new" || sStr(l, "col") == "work" || sStr(l, "col") == "")
}

func mgrAddLog(l map[string]any, m *SalesMgr, k, text, to string, at time.Time) {
	lg, _ := l["log"].([]any)
	e := map[string]any{"at": at.UTC().Format(time.RFC3339), "text": text, "k": k}
	if m != nil {
		e["by"], e["byName"] = m.Key(), m.Name
	}
	if to != "" {
		e["to"] = to
	}
	l["log"] = append(lg, e)
}

// mgrCheck: the required fields of the stage, in words ("" when fine).
func mgrCheck(l map[string]any) string {
	col := sStr(l, "col")
	if _, ok := mgrCols[col]; !ok {
		return "Нет такого этапа"
	}
	switch col {
	case "lost":
		if sStr(l, "lostReason") == "" {
			return "Отказ: укажите причину"
		}
	case "won":
		if crmSum(l["sum"]) <= 0 {
			return "Резидент: укажите сумму сделки"
		}
	case "later":
		if mgrDay(sStr(l, "nextAt")) == "" {
			return "Отложено: укажите дату, когда вернуться к лиду"
		}
	}
	if leadActive(col) && col != "new" && (sStr(l, "next") == "" || mgrDay(sStr(l, "nextAt")) == "") {
		return "Укажите следующий шаг и дату: без них лид теряется"
	}
	return ""
}

func crmSum(v any) int64 {
	d := digitsRe.ReplaceAllString(fmt.Sprint(v), "")
	if v == nil || d == "" {
		return 0
	}
	n, _ := strconv.ParseInt(d, 10, 64)
	return n
}

var hhmmRe = func(s string) bool {
	if len(s) != 5 || s[2] != ':' {
		return false
	}
	h, e1 := strconv.Atoi(s[:2])
	m, e2 := strconv.Atoi(s[3:])
	return e1 == nil && e2 == nil && h >= 0 && h < 24 && m >= 0 && m < 60
}

// ── the manager's API ──

func (s *SalesManagers) Me(c *gin.Context) {
	m := s.me(c)
	if m == nil {
		return
	}
	cols := []gin.H{}
	for _, k := range mgrColOrder {
		cols = append(cols, gin.H{"id": k, "n": mgrCols[k]})
	}
	wa := greenFromEnv() != nil
	c.JSON(http.StatusOK, gin.H{"manager": gin.H{"id": m.ID, "name": m.Name, "tg": m.Tg != 0}, "cols": cols,
		"lostReasons": mgrLostReasons, "sources": mgrSources, "wa": wa, "today": s.today()})
}

func (s *SalesManagers) Leads(c *gin.Context) {
	m := s.me(c)
	if m == nil {
		return
	}
	_, leads, err := s.crm(c.Request.Context())
	if err != nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "try_again"})
		return
	}
	mine, pool := []gin.H{}, []gin.H{}
	for _, v := range leads {
		l, ok := v.(map[string]any)
		if !ok {
			continue
		}
		if mgrOf(l) == m.ID {
			mine = append(mine, s.leadView(l, false))
		} else if inPool(l) && len(pool) < mgrPoolMax {
			pool = append(pool, s.poolView(l))
		}
	}
	c.JSON(http.StatusOK, gin.H{"mine": mine, "pool": pool, "today": s.today()})
}

func (s *SalesManagers) Lead(c *gin.Context) {
	m := s.me(c)
	if m == nil {
		return
	}
	_, leads, err := s.crm(c.Request.Context())
	if err != nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "try_again"})
		return
	}
	l := findLeadID(leads, c.Param("id"))
	switch {
	case l == nil:
		c.JSON(http.StatusNotFound, gin.H{"error": "not_found"})
	case mgrOf(l) == m.ID:
		c.JSON(http.StatusOK, gin.H{"lead": s.leadView(l, true)})
	case inPool(l):
		c.JSON(http.StatusOK, gin.H{"lead": s.poolView(l)})
	default:
		c.JSON(http.StatusForbidden, gin.H{"error": "not_yours", "detail": "Этот лид ведёт другой менеджер или команда"})
	}
}

// editLead: one change of the manager's own lead (or a lead of the pool
// when take is true), checked and journaled.
func (s *SalesManagers) editLead(c *gin.Context, m *SalesMgr, id string, take bool, fn func(l map[string]any) (string, string)) (map[string]any, string, bool) {
	ctx := c.Request.Context()
	var got map[string]any
	var action, detail, problem string
	code := http.StatusOK
	err := r83Mutate(ctx, s.Docs, "club", "bs_crm", m.Key(), func(crm *map[string]any) bool {
		got, problem, code = nil, "", http.StatusOK
		if *crm == nil {
			*crm = map[string]any{}
		}
		leads, _ := (*crm)["leads"].([]any)
		l := findLeadID(leads, id)
		switch {
		case l == nil:
			code, problem = http.StatusNotFound, "Лид не найден"
			return false
		case take && !inPool(l):
			if mgrOf(l) == m.ID {
				got = l
				return false
			}
			code, problem = http.StatusConflict, "Этого лида уже взяли в работу"
			return false
		case !take && mgrOf(l) != m.ID:
			code, problem = http.StatusForbidden, "Этот лид ведёт другой менеджер или команда"
			return false
		}
		before, _ := json.Marshal(l)
		action, detail = fn(l)
		if action == "" {
			got = l
			return false
		}
		if p := mgrCheck(l); p != "" {
			var back map[string]any
			_ = json.Unmarshal(before, &back)
			for k := range l {
				delete(l, k)
			}
			for k, v := range back {
				l[k] = v
			}
			code, problem = http.StatusUnprocessableEntity, p
			return false
		}
		l["updatedAt"] = s.now().UTC().Format(time.RFC3339)
		got = l
		return true
	})
	if err != nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "try_again", "detail": "Не получилось сохранить, попробуйте ещё раз"})
		return nil, "", false
	}
	if problem != "" {
		c.JSON(code, gin.H{"error": "check", "detail": problem})
		return nil, "", false
	}
	if action != "" {
		s.logAct(ctx, m, action, got, detail)
	}
	return got, action, true
}

func (s *SalesManagers) Take(c *gin.Context) {
	m := s.me(c)
	if m == nil {
		return
	}
	l, _, ok := s.editLead(c, m, c.Param("id"), true, func(l map[string]any) (string, string) {
		l["mgr"], l["mgrName"], l["mgrNotified"] = m.ID, m.Name, m.ID
		if sStr(l, "col") == "" {
			l["col"] = "new"
		}
		mgrAddLog(l, m, "take", "Взял в работу: "+m.Name, "", s.now())
		return "Взял лида в работу", ""
	})
	if ok {
		c.JSON(http.StatusOK, gin.H{"lead": s.leadView(l, true)})
	}
}

type mgrPatch struct {
	Col        *string `json:"col"`
	Next       *string `json:"next"`
	NextAt     *string `json:"nextAt"`
	NextTime   *string `json:"nextTime"`
	LostReason *string `json:"lostReason"`
	Sum        *string `json:"sum"`
	Niche      *string `json:"niche"`
	Name       *string `json:"name"`
	Phone      *string `json:"phone"`
	Tg         *string `json:"tg"`
	Source     *string `json:"source"`
	Note       *string `json:"note"`
}

func str255(p *string, n int) string { return clip(strings.TrimSpace(noLongDash(*p)), n) }

func (s *SalesManagers) Update(c *gin.Context) {
	m := s.me(c)
	if m == nil {
		return
	}
	var p mgrPatch
	if err := c.ShouldBindJSON(&p); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "bad_request"})
		return
	}
	if p.NextAt != nil && *p.NextAt != "" && mgrDay(*p.NextAt) == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "check", "detail": "Дата шага: ГГГГ-ММ-ДД"})
		return
	}
	if p.NextTime != nil && *p.NextTime != "" && !hhmmRe(*p.NextTime) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "check", "detail": "Время шага: ЧЧ:ММ"})
		return
	}
	if p.Col != nil {
		if _, ok := mgrCols[*p.Col]; !ok {
			c.JSON(http.StatusBadRequest, gin.H{"error": "check", "detail": "Нет такого этапа"})
			return
		}
	}
	l, _, ok := s.editLead(c, m, c.Param("id"), false, func(l map[string]any) (string, string) {
		now := s.now()
		var acts []string
		set := func(k, v, label string) {
			if sStr(l, k) == v {
				return
			}
			l[k] = v
			if label != "" {
				acts = append(acts, label)
			}
		}
		if p.Name != nil {
			set("name", str255(p.Name, 120), "Имя")
		}
		if p.Phone != nil {
			set("phone", str255(p.Phone, 40), "Телефон")
		}
		if p.Tg != nil {
			set("tg", str255(p.Tg, 60), "Telegram")
		}
		if p.Source != nil {
			set("source", str255(p.Source, 60), "Источник")
		}
		if p.Niche != nil {
			set("niche", str255(p.Niche, 200), "Ниша")
		}
		if p.Sum != nil {
			v := ""
			if n := crmSum(*p.Sum); n > 0 {
				v = strconv.FormatInt(n, 10)
			}
			if sStr(l, "sum") != v {
				l["sum"] = v
				mgrAddLog(l, m, "sum", "Сумма: "+firstNonBlank(tenge(crmSum(v)), "не указана"), "", now)
				acts = append(acts, "Сумма")
			}
		}
		if p.LostReason != nil {
			v := str255(p.LostReason, 80)
			if v != sStr(l, "lostReason") {
				l["lostReason"] = v
				if v != "" {
					mgrAddLog(l, m, "lost", "Причина отказа: "+v, "", now)
				}
				acts = append(acts, "Причина отказа")
			}
		}
		stepChanged := false
		if p.Next != nil && str255(p.Next, 200) != sStr(l, "next") {
			l["next"], stepChanged = str255(p.Next, 200), true
		}
		if p.NextAt != nil && mgrDay(*p.NextAt) != mgrDay(sStr(l, "nextAt")) {
			l["nextAt"], stepChanged = mgrDay(*p.NextAt), true
		}
		if p.NextTime != nil && strings.TrimSpace(*p.NextTime) != sStr(l, "nextTime") {
			l["nextTime"], stepChanged = strings.TrimSpace(*p.NextTime), true
		}
		if stepChanged {
			delete(l, "mgrRemind")
			if sStr(l, "next") != "" {
				when := sStr(l, "nextAt")
				if t := sStr(l, "nextTime"); t != "" {
					when += " " + t
				}
				mgrAddLog(l, m, "step", "Следующий шаг: "+sStr(l, "next")+" · "+when, "", now)
			}
			acts = append(acts, "Следующий шаг")
		}
		if p.Col != nil && *p.Col != sStr(l, "col") {
			from := sStr(l, "col")
			l["col"] = *p.Col
			mgrAddLog(l, m, "col", "Этап: "+mgrCols[from]+" → "+mgrCols[*p.Col], *p.Col, now)
			if *p.Col == "won" {
				l["wonAt"] = now.UTC().Format(time.RFC3339)
			} else {
				delete(l, "wonAt")
			}
			if leadClosed(*p.Col) {
				delete(l, "next")
				delete(l, "nextAt")
				delete(l, "nextTime")
			}
			if *p.Col != "lost" {
				delete(l, "lostReason")
			}
			acts = append(acts, "Этап: "+mgrCols[*p.Col])
		}
		if p.Note != nil && str255(p.Note, 2000) != "" {
			mgrAddLog(l, m, "note", "Заметка: "+str255(p.Note, 2000), "", now)
			acts = append(acts, "Заметка")
		}
		if len(acts) == 0 {
			return "", ""
		}
		return "Изменил лида", strings.Join(acts, ", ")
	})
	if ok {
		c.JSON(http.StatusOK, gin.H{"lead": s.leadView(l, true)})
	}
}

// Create: a lead the manager found himself (a call, a meeting).
func (s *SalesManagers) Create(c *gin.Context) {
	m := s.me(c)
	if m == nil {
		return
	}
	var p mgrPatch
	if err := c.ShouldBindJSON(&p); err != nil || p.Name == nil || (str255(p.Name, 120) == "" && (p.Phone == nil || str255(p.Phone, 40) == "")) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "check", "detail": "Укажите имя или телефон"})
		return
	}
	now := s.now()
	l := map[string]any{"id": "m" + strconv.FormatInt(now.UnixNano()%1e12, 36) + strconv.Itoa(rand.Intn(1000)), "col": "work",
		"name": str255(p.Name, 120), "tg": "", "niche": "", "note": "", "sum": "", "date": now.In(almaty).Format("02.01.2006"),
		"created": now.UTC().Format(time.RFC3339), "mgr": m.ID, "mgrName": m.Name, "mgrNotified": m.ID, "source": "Менеджер"}
	if p.Phone != nil {
		l["phone"] = str255(p.Phone, 40)
	}
	if p.Source != nil && str255(p.Source, 60) != "" {
		l["source"] = str255(p.Source, 60)
	}
	if p.Niche != nil {
		l["niche"] = str255(p.Niche, 200)
	}
	if p.Next != nil {
		l["next"] = str255(p.Next, 200)
	}
	if p.NextAt != nil {
		l["nextAt"] = mgrDay(*p.NextAt)
	}
	if p.NextTime != nil && hhmmRe(*p.NextTime) {
		l["nextTime"] = *p.NextTime
	}
	if sStr(l, "next") == "" {
		l["next"], l["nextAt"] = "Первый контакт", s.today()
	}
	mgrAddLog(l, m, "new", "Лид создан менеджером "+m.Name, "", now)
	err := r83Mutate(c.Request.Context(), s.Docs, "club", "bs_crm", m.Key(), func(crm *map[string]any) bool {
		if *crm == nil {
			*crm = map[string]any{}
		}
		leads, _ := (*crm)["leads"].([]any)
		(*crm)["leads"] = append([]any{l}, leads...)
		return true
	})
	if err != nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "try_again"})
		return
	}
	s.logAct(c.Request.Context(), m, "Создал лида", l, sStr(l, "source"))
	c.JSON(http.StatusOK, gin.H{"lead": s.leadView(l, true)})
}

var touchNames = map[string]string{"call": "📞 Звонок", "wa": "💬 WhatsApp", "tg": "✈️ Telegram", "nocall": "📵 Не дозвонился"}

// Touch: the quick actions (call, WhatsApp, Telegram) are counted and kept.
func (s *SalesManagers) Touch(c *gin.Context) {
	m := s.me(c)
	if m == nil {
		return
	}
	var r struct {
		Kind string `json:"kind"`
	}
	if err := c.ShouldBindJSON(&r); err != nil || touchNames[r.Kind] == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "bad_request"})
		return
	}
	l, _, ok := s.editLead(c, m, c.Param("id"), false, func(l map[string]any) (string, string) {
		mgrAddLog(l, m, r.Kind, touchNames[r.Kind], "", s.now())
		if sStr(l, "col") == "new" {
			if sStr(l, "next") == "" {
				l["next"], l["nextAt"] = "Связаться повторно", s.today()
			}
			l["col"] = "work"
			mgrAddLog(l, m, "col", "Этап: Новый → В работе", "work", s.now())
		}
		return touchNames[r.Kind], ""
	})
	if ok {
		c.JSON(http.StatusOK, gin.H{"lead": s.leadView(l, true)})
	}
}

// ── scripts, slots, WhatsApp ──

func (s *SalesManagers) Scripts(c *gin.Context) {
	if s.me(c) == nil {
		return
	}
	var list []any
	if d, err := s.Docs.GetDoc(c.Request.Context(), "club", "bs_scripts"); err == nil && d != nil && !d.Deleted {
		_ = json.Unmarshal([]byte(d.Value), &list)
	}
	c.JSON(http.StatusOK, gin.H{"scripts": list})
}

// ScriptsDefault: GET /crm/scripts.js: the platform's own default scripts
// (used when the team never edited them).
func ScriptsDefault(c *gin.Context) {
	c.Header("Cache-Control", "private, max-age=3600")
	c.Data(http.StatusOK, "text/javascript; charset=utf-8", []byte("window.SCRIPTS_DEF = "+web.ScriptsDefJS()+";\n"))
}

func (s *SalesManagers) Slots(c *gin.Context) {
	if s.me(c) == nil {
		return
	}
	d, err := s.Docs.GetDoc(c.Request.Context(), "club", slotsDoc)
	doc := map[string]any{}
	if err == nil && d != nil && !d.Deleted {
		_ = json.Unmarshal([]byte(d.Value), &doc)
	}
	price, _ := slotsPrice(doc)
	list, _ := doc["slots"].([]any)
	now := s.now()
	out := []gin.H{}
	for _, v := range list {
		sl, ok := readSlot(v)
		if !ok || !sl.free() || !sl.Start.After(now.Add(30*time.Minute)) || sl.Start.After(now.Add(21*24*time.Hour)) {
			continue
		}
		out = append(out, gin.H{"id": sl.ID, "start": sl.Start.UTC().Format(time.RFC3339), "when": whenRu(sl.Start), "dur": sl.Dur, "format": sl.str("format")})
	}
	sort.Slice(out, func(i, j int) bool { return out[i]["start"].(string) < out[j]["start"].(string) })
	c.JSON(http.StatusOK, gin.H{"slots": out, "price": price})
}

// Book: the manager books an express-разбор for his lead: the slot, the
// lead in «Записан на разбор», a note to the team.
func (s *SalesManagers) Book(c *gin.Context) {
	m := s.me(c)
	if m == nil {
		return
	}
	var r struct {
		SlotID   string `json:"slotId"`
		Question string `json:"question"`
	}
	if err := c.ShouldBindJSON(&r); err != nil || r.SlotID == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "bad_request"})
		return
	}
	ctx := c.Request.Context()
	_, leads, err := s.crm(ctx)
	if err != nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "try_again"})
		return
	}
	lead := findLeadID(leads, c.Param("id"))
	if lead == nil || mgrOf(lead) != m.ID {
		c.JSON(http.StatusForbidden, gin.H{"error": "not_yours", "detail": "Записать можно только своего лида"})
		return
	}
	if sStr(lead, "phone") == "" && sStr(lead, "tg") == "" && leadTg(lead) == 0 {
		c.JSON(http.StatusUnprocessableEntity, gin.H{"error": "check", "detail": "Укажите телефон лида: по нему с ним свяжутся перед разбором"})
		return
	}
	now := s.now()
	var got slot
	problem := ""
	err = r83Mutate(ctx, s.Docs, "club", slotsDoc, m.Key(), func(doc *map[string]any) bool {
		problem = ""
		if *doc == nil {
			*doc = map[string]any{}
		}
		list, _ := (*doc)["slots"].([]any)
		for _, v := range list {
			sl, ok := readSlot(v)
			if !ok || sl.ID != r.SlotID {
				continue
			}
			if !sl.Start.After(now) {
				problem = "Это время уже прошло"
				return false
			}
			if !sl.free() {
				problem = "Это время уже заняли, выберите другое"
				return false
			}
			sl.m["status"] = "booked"
			sl.m["booking"] = map[string]any{"tgId": leadTg(lead), "name": firstNonBlank(sStr(lead, "name"), "Без имени"), "username": strings.TrimPrefix(sStr(lead, "tg"), "@"),
				"phone": sStr(lead, "phone"), "niche": sStr(lead, "niche"), "question": clip(strings.TrimSpace(r.Question), 300),
				"at": now.UTC().Format(time.RFC3339), "leadId": sStr(lead, "id"), "paid": false, "reminded": []any{}, "by": m.Key(), "byName": m.Name}
			got = sl
			return true
		}
		problem = "Слот не найден, обновите список"
		return false
	})
	if err != nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "try_again"})
		return
	}
	if problem != "" {
		c.JSON(http.StatusConflict, gin.H{"error": "check", "detail": problem})
		return
	}
	when := whenRu(got.Start)
	l, _, ok := s.editLead(c, m, sStr(lead, "id"), false, func(l map[string]any) (string, string) {
		if sStr(l, "col") != "won" {
			mgrAddLog(l, m, "col", "Этап: "+mgrCols[sStr(l, "col")]+" → "+mgrCols["meet"], "meet", now)
			l["col"] = "meet"
		}
		l["next"] = "Разбор " + got.Start.In(almaty).Format("15:04")
		l["nextAt"] = got.Start.In(almaty).Format("2006-01-02")
		l["nextTime"] = got.Start.In(almaty).Format("15:04")
		l["razborSlot"], l["razborAt"], l["warmStop"] = got.ID, got.Start.UTC().Format(time.RFC3339), true
		mgrAddLog(l, m, "book", "Записан на экспресс-разбор: "+when+" ("+got.str("format")+")", "", now)
		return "Записал на экспресс-разбор", when
	})
	if !ok {
		return
	}
	if s.Send != nil {
		txt := "📅 " + m.Name + " записал на экспресс-разбор\n\n" + firstNonBlank(sStr(l, "name"), "Без имени") + " · " + crmPhonePretty(sStr(l, "phone")) +
			"\n" + when + " · " + got.str("format")
		if n := sStr(l, "niche"); n != "" {
			txt += "\nНиша: " + n
		}
		for _, id := range s.Admins {
			if err := s.Send(context.WithoutCancel(ctx), id, txt, nil); err != nil {
				log.Printf("managers: booking note → %d: %v", id, err)
			}
		}
	}
	c.JSON(http.StatusOK, gin.H{"lead": s.leadView(l, true), "when": when})
}

func crmPhonePretty(p string) string {
	d := phoneDigits(p)
	if len(d) == 11 && d[0] == '7' {
		return "+7 " + d[1:4] + " " + d[4:7] + " " + d[7:9] + " " + d[9:]
	}
	if d == "" {
		return "без телефона"
	}
	return "+" + d
}

func (s *SalesManagers) ownLeadPhone(c *gin.Context, m *SalesMgr) (map[string]any, string) {
	_, leads, err := s.crm(c.Request.Context())
	if err != nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "try_again"})
		return nil, ""
	}
	l := findLeadID(leads, c.Param("id"))
	if l == nil || mgrOf(l) != m.ID {
		c.JSON(http.StatusForbidden, gin.H{"error": "not_yours"})
		return nil, ""
	}
	p := phoneDigits(sStr(l, "phone"))
	if len(p) < 11 {
		c.JSON(http.StatusOK, gin.H{"messages": []any{}, "detail": "У лида нет телефона"})
		return nil, ""
	}
	return l, p
}

func (s *SalesManagers) WAChat(c *gin.Context) {
	m := s.me(c)
	if m == nil {
		return
	}
	_, phone := s.ownLeadPhone(c, m)
	if phone == "" {
		return
	}
	if s.AI == nil || s.AI.repo == nil {
		c.JSON(http.StatusOK, gin.H{"messages": []any{}, "connected": false})
		return
	}
	list, err := s.AI.repo.CrmMessages(c.Request.Context(), phone)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "load_failed"})
		return
	}
	_ = s.AI.repo.CrmMarkRead(c.Request.Context(), phone)
	c.JSON(http.StatusOK, gin.H{"messages": list, "connected": greenFromEnv() != nil})
}

func (s *SalesManagers) WASend(c *gin.Context) {
	m := s.me(c)
	if m == nil {
		return
	}
	var r struct {
		Text string `json:"text"`
	}
	if err := c.ShouldBindJSON(&r); err != nil || strings.TrimSpace(r.Text) == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "text_required"})
		return
	}
	l, phone := s.ownLeadPhone(c, m)
	if phone == "" {
		return
	}
	g := greenFromEnv()
	if g == nil || s.AI == nil {
		c.JSON(http.StatusOK, gin.H{"error": "wa_off", "detail": "WhatsApp клуба не подключён к CRM: отправьте через кнопку «WhatsApp» (откроется приложение)"})
		return
	}
	ctx, cancel := context.WithTimeout(c.Request.Context(), 20*time.Second)
	defer cancel()
	out, err := s.AI.greenCall(ctx, g, "sendMessage", "POST", map[string]any{"chatId": phone + "@c.us", "message": r.Text})
	if err != nil {
		reason, hint := waHuman("", err)
		c.JSON(http.StatusOK, gin.H{"error": "wa_failed", "detail": reason + ". " + hint})
		return
	}
	id, _ := out["idMessage"].(string)
	_, _ = s.AI.repo.AddCrmMessage(ctx, pg.CrmMessage{Phone: phone, Dir: "out", Text: r.Text, At: s.now()}, id)
	_, _, _ = s.editLead(c, m, sStr(l, "id"), false, func(l map[string]any) (string, string) {
		mgrAddLog(l, m, "wa", "💬 WhatsApp: "+clip(r.Text, 120), "", s.now())
		return "Написал в WhatsApp", ""
	})
	c.JSON(http.StatusOK, gin.H{"ok": true})
}

// ── numbers ──

type mgrStats struct {
	Active   int     `json:"active"`
	Today    int     `json:"today"`
	Overdue  int     `json:"overdue"`
	NoStep   int     `json:"noStep"`
	Calls7   int     `json:"calls7"`
	WA7      int     `json:"wa7"`
	Touch30  int     `json:"touch30"`
	Taken30  int     `json:"taken30"`
	Booked30 int     `json:"booked30"`
	Won30    int     `json:"won30"`
	Lost30   int     `json:"lost30"`
	WonSum30 int64   `json:"wonSum30"`
	Conv     float64 `json:"conv"`
}

func (s *SalesManagers) statsOf(leads []any, id int64) mgrStats {
	var st mgrStats
	key := "mgr:" + strconv.FormatInt(id, 10)
	now := s.now()
	today := s.today()
	for _, v := range leads {
		l, ok := v.(map[string]any)
		if !ok {
			continue
		}
		own := mgrOf(l) == id
		if own && !leadClosed(sStr(l, "col")) {
			if leadActive(sStr(l, "col")) {
				st.Active++
			}
			d := mgrDay(sStr(l, "nextAt"))
			switch {
			case sStr(l, "next") == "" || d == "":
				if leadActive(sStr(l, "col")) {
					st.NoStep++
				}
			case d < today:
				st.Overdue++
			case d == today:
				st.Today++
			}
		}
		if own && sStr(l, "col") == "won" {
			if t := sTime(l, "wonAt"); !t.IsZero() && now.Sub(t) < 30*24*time.Hour {
				st.Won30++
				st.WonSum30 += crmSum(l["sum"])
			}
		}
		lg, _ := l["log"].([]any)
		for _, x := range lg {
			e, _ := x.(map[string]any)
			if e == nil || sStr(e, "by") != key {
				continue
			}
			at := sTime(e, "at")
			if at.IsZero() || now.Sub(at) > 30*24*time.Hour {
				continue
			}
			k := sStr(e, "k")
			switch k {
			case "call", "nocall":
				if now.Sub(at) < 7*24*time.Hour {
					st.Calls7++
				}
				st.Touch30++
			case "wa", "tg":
				if now.Sub(at) < 7*24*time.Hour && k == "wa" {
					st.WA7++
				}
				st.Touch30++
			case "take":
				st.Taken30++
			case "book":
				st.Booked30++
			case "col":
				if sStr(e, "to") == "lost" {
					st.Lost30++
				}
			}
		}
	}
	if st.Won30+st.Lost30 > 0 {
		st.Conv = float64(st.Won30) / float64(st.Won30+st.Lost30)
	}
	return st
}

func (s *SalesManagers) Stats(c *gin.Context) {
	m := s.me(c)
	if m == nil {
		return
	}
	_, leads, err := s.crm(c.Request.Context())
	if err != nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "try_again"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"stats": s.statsOf(leads, m.ID)})
}

// ── the owner: managers, invitations, performance, journal ──

func (s *SalesManagers) mgrView(m SalesMgr, leads []any) gin.H {
	st := "active"
	switch {
	case m.RevokedAt != "":
		st = "revoked"
	case m.AcceptedAt == "" && m.LinkHash == "":
		st = "invited"
	}
	return gin.H{"id": m.ID, "name": m.Name, "tgName": m.TgName, "hasTg": m.Tg != 0, "state": st, "createdAt": m.CreatedAt, "acceptedAt": m.AcceptedAt,
		"lastSeen": m.LastSeen, "inviteExp": m.InviteExp, "linkExp": m.LinkExp, "revokedAt": m.RevokedAt, "stats": s.statsOf(leads, m.ID)}
}

func (s *SalesManagers) List(c *gin.Context) {
	if !s.team(c) {
		return
	}
	ctx := c.Request.Context()
	d, err := s.load(ctx, true)
	if err != nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "try_again"})
		return
	}
	_, leads, _ := s.crm(ctx)
	out := []gin.H{}
	for _, m := range d.Managers {
		out = append(out, s.mgrView(m, leads))
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i]["state"] == "active" && out[j]["state"] != "active" })
	pool := 0
	for _, v := range leads {
		if l, ok := v.(map[string]any); ok && inPool(l) {
			pool++
		}
	}
	lg, _ := r83Read[mgrLogDoc](ctx, s.Docs, "server", mgrLogKey)
	if len(lg.Log) > 80 {
		lg.Log = lg.Log[:80]
	}
	c.JSON(http.StatusOK, gin.H{"managers": out, "pool": pool, "log": lg.Log})
}

func (s *SalesManagers) Log(c *gin.Context) {
	if !s.team(c) {
		return
	}
	id, _ := strconv.ParseInt(c.Query("id"), 10, 64)
	lg, _ := r83Read[mgrLogDoc](c.Request.Context(), s.Docs, "server", mgrLogKey)
	out := []mgrLogEntry{}
	for _, e := range lg.Log {
		if id == 0 || e.Mid == id {
			out = append(out, e)
			if len(out) >= 300 {
				break
			}
		}
	}
	c.JSON(http.StatusOK, gin.H{"log": out})
}

func (s *SalesManagers) Invite(c *gin.Context) {
	if !s.team(c) {
		return
	}
	var r struct {
		Name string `json:"name"`
		ID   int64  `json:"id"`
	}
	if err := c.ShouldBindJSON(&r); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "bad_request"})
		return
	}
	name := clip(strings.TrimSpace(noLongDash(r.Name)), 80)
	if r.ID == 0 && name == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "check", "detail": "Как зовут менеджера?"})
		return
	}
	tok := newSecretToken()
	now := s.now()
	exp := now.Add(mgrInviteTTL)
	by := s.teamName(c)
	var made SalesMgr
	problem := ""
	err := s.mutate(c.Request.Context(), platformUser(c), func(d *mgrDoc) bool {
		problem = ""
		if r.ID != 0 {
			m := d.byID(r.ID)
			if m == nil || !m.Active() {
				problem = "Менеджер не найден или отозван"
				return false
			}
			m.InviteHash, m.InviteExp = hashSecretToken(tok), exp.UTC().Format(time.RFC3339)
			made = *m
			return true
		}
		active := 0
		for _, m := range d.Managers {
			if m.Active() {
				active++
			}
		}
		if active >= 10 {
			problem = "Активных менеджеров уже 10: отзовите лишних"
			return false
		}
		d.Seq++
		made = SalesMgr{ID: d.Seq, Name: name, InviteHash: hashSecretToken(tok), InviteExp: exp.UTC().Format(time.RFC3339),
			CreatedBy: by, CreatedAt: now.UTC().Format(time.RFC3339)}
		d.Managers = append(d.Managers, made)
		return true
	})
	if err != nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "try_again"})
		return
	}
	if problem != "" {
		c.JSON(http.StatusConflict, gin.H{"error": "check", "detail": problem})
		return
	}
	s.logAct(c.Request.Context(), nil, "Приглашение менеджера: "+made.Name, nil, "выдал "+by)
	link := linkBase(c) + "/crm/join/" + tok
	c.JSON(http.StatusOK, gin.H{"id": made.ID, "link": link, "expiresAt": exp.UTC(),
		"text": "Приглашение в CRM Business Surgery (менеджер по продажам). Откройте ссылку и войдите своим Telegram: " + link + "\nСсылка одноразовая, действует 72 часа."})
}

func (s *SalesManagers) PersonalLink(c *gin.Context) {
	if !s.team(c) {
		return
	}
	var r idReq
	if err := c.ShouldBindJSON(&r); err != nil || r.ID == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "bad_request"})
		return
	}
	tok := newSecretToken()
	exp := s.now().Add(mgrLinkTTL)
	var name string
	ok := false
	err := s.mutate(c.Request.Context(), platformUser(c), func(d *mgrDoc) bool {
		m := d.byID(r.ID)
		if !m.Active() {
			ok = false
			return false
		}
		m.LinkHash, m.LinkExp, name, ok = hashSecretToken(tok), exp.UTC().Format(time.RFC3339), m.Name, true
		return true
	})
	if err != nil || !ok {
		c.JSON(http.StatusNotFound, gin.H{"error": "not_found", "detail": "Менеджер не найден или отозван"})
		return
	}
	s.logAct(c.Request.Context(), nil, "Личная ссылка входа: "+name, nil, "выдал "+s.teamName(c))
	link := linkBase(c) + "/crm/in/" + tok
	c.JSON(http.StatusOK, gin.H{"link": link, "expiresAt": exp.UTC(),
		"text": "Ваш вход в CRM Business Surgery: " + link + "\nЛичная ссылка на 30 дней, не пересылайте её никому."})
}

func (s *SalesManagers) Revoke(c *gin.Context) {
	if !s.team(c) {
		return
	}
	var r idReq
	if err := c.ShouldBindJSON(&r); err != nil || r.ID == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "bad_request"})
		return
	}
	var name string
	err := s.mutate(c.Request.Context(), platformUser(c), func(d *mgrDoc) bool {
		m := d.byID(r.ID)
		if m == nil || m.RevokedAt != "" {
			return false
		}
		m.RevokedAt, m.InviteHash, m.LinkHash, name = s.now().UTC().Format(time.RFC3339), "", "", m.Name
		return true
	})
	if err != nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "try_again"})
		return
	}
	if name != "" {
		s.logAct(c.Request.Context(), nil, "Доступ менеджера закрыт: "+name, nil, s.teamName(c))
	}
	c.JSON(http.StatusOK, gin.H{"ok": true})
}

// Assign: the owner gives a lead to a manager (0: back to the team).
func (s *SalesManagers) Assign(c *gin.Context) {
	if !s.team(c) {
		return
	}
	var r struct {
		Lead string `json:"lead"`
		ID   int64  `json:"id"`
	}
	if err := c.ShouldBindJSON(&r); err != nil || r.Lead == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "bad_request"})
		return
	}
	ctx := c.Request.Context()
	var m *SalesMgr
	if r.ID != 0 {
		d, err := s.load(ctx, true)
		if err != nil || !d.byID(r.ID).Active() {
			c.JSON(http.StatusNotFound, gin.H{"error": "not_found", "detail": "Менеджер не найден или отозван"})
			return
		}
		cp := *d.byID(r.ID)
		m = &cp
	}
	var got map[string]any
	err := r83Mutate(ctx, s.Docs, "club", "bs_crm", platformUser(c), func(crm *map[string]any) bool {
		got = nil
		if *crm == nil {
			return false
		}
		leads, _ := (*crm)["leads"].([]any)
		l := findLeadID(leads, r.Lead)
		if l == nil {
			return false
		}
		got = l
		if m == nil {
			delete(l, "mgr")
			delete(l, "mgrName")
			mgrAddLog(l, nil, "assign", "Снят с менеджера: лид ведёт команда", "", s.now())
			return true
		}
		l["mgr"], l["mgrName"] = m.ID, m.Name
		mgrAddLog(l, nil, "assign", "Передан менеджеру: "+m.Name+" ("+s.teamName(c)+")", "", s.now())
		return true
	})
	if err != nil || got == nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "not_found"})
		return
	}
	who := "команда"
	if m != nil {
		who = m.Name
	}
	s.logAct(ctx, nil, "Лид передан: "+who, got, s.teamName(c))
	c.JSON(http.StatusOK, gin.H{"ok": true})
	go s.Tick(context.Background()) // the manager hears about it at once
}

// ── entering: invitation, personal link, Telegram ──

func (s *SalesManagers) issue(c *gin.Context, m *SalesMgr, actor, actorName, how string) (gin.H, error) {
	tok, err := s.Auth.issueSession(c, pg.PlatformSession{Sub: m.Key(), Actor: actor, ActorName: firstNonBlank(actorName, m.Name), Kind: "manager"},
		RoleSales, jwt.MapClaims{"mg": m.ID})
	if err != nil {
		return nil, err
	}
	setSessionCookie(c, tok, int(platformSessionTTL.Seconds()))
	s.logAct(c.Request.Context(), m, "Вход в CRM", nil, how)
	return gin.H{"token": tok, "expiresAt": time.Now().Add(platformSessionTTL).UTC(),
		"user": gin.H{"id": m.Key(), "name": m.Name, "role": RoleSales}, "team": gin.H{}}, nil
}

// LoginTg: the main login page and /crm: an active manager's Telegram.
func (s *SalesManagers) LoginTg(c *gin.Context, u *platformTgUser) (gin.H, bool) {
	m := s.ByTg(c.Request.Context(), u.ID)
	if m == nil {
		return nil, false
	}
	out, err := s.issue(c, m, "tg:"+strconv.FormatInt(u.ID, 10), tgDisplayName(u), "Telegram")
	if err != nil {
		log.Printf("managers: login %d: %v", m.ID, err)
		return nil, false
	}
	return out, true
}

func (s *SalesManagers) tgProof(c *gin.Context, req acceptReq) (*platformTgUser, bool) {
	if s.Auth == nil || s.Auth.botToken == "" {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "telegram_not_configured"})
		return nil, false
	}
	var u *platformTgUser
	var err error
	switch {
	case req.InitData != "":
		u, err = verifyTelegramInitData(req.InitData, s.Auth.botToken, time.Now())
	case req.Widget != nil:
		u, err = verifyTelegramWidget(req.Widget, s.Auth.botToken, time.Now())
	default:
		err = errors.New("no telegram proof")
	}
	if err != nil {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "telegram_check_failed", "detail": "Telegram не подтвердил вход, попробуйте ещё раз"})
		return nil, false
	}
	return u, true
}

// Login: POST /api/v1/manager/login {widget|initData}: /crm's own sign-in.
func (s *SalesManagers) Login(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	if !s.rl.allow("mlogin:"+c.ClientIP(), 20, 10*time.Minute) {
		tooMany(c)
		return
	}
	var req acceptReq
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "bad request"})
		return
	}
	u, ok := s.tgProof(c, req)
	if !ok {
		return
	}
	out, ok := s.LoginTg(c, u)
	if !ok {
		c.JSON(http.StatusForbidden, gin.H{"error": "not_manager", "detail": "Этот Telegram не подключён как менеджер. Попросите у владельца приглашение"})
		return
	}
	c.JSON(http.StatusOK, out)
}

func (s *SalesManagers) InviteInfo(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	if !s.rl.allow("mpeek:"+c.ClientIP(), 30, 10*time.Minute) {
		tooMany(c)
		return
	}
	t := c.Query("t")
	if !validSecretToken(t) {
		c.JSON(http.StatusNotFound, gin.H{"error": "invalid"})
		return
	}
	d, err := s.load(c.Request.Context(), true)
	if err != nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "try_again"})
		return
	}
	h := hashSecretToken(t)
	for _, m := range d.Managers {
		if m.InviteHash == h && m.Active() && sTimeStr(m.InviteExp).After(s.now()) {
			c.JSON(http.StatusOK, gin.H{"name": m.Name, "expiresAt": m.InviteExp})
			return
		}
	}
	c.JSON(http.StatusNotFound, gin.H{"error": "invalid"})
}

func mgrFirst(n string) string {
	if f := strings.Fields(n); len(f) > 0 {
		return f[0]
	}
	return "менеджер"
}

func sTimeStr(v string) time.Time {
	t, _ := time.Parse(time.RFC3339, v)
	return t
}

// Accept: POST /api/v1/manager/accept {token, widget|initData}.
func (s *SalesManagers) Accept(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	if !s.rl.allow("maccept:"+c.ClientIP(), 10, 10*time.Minute) {
		tooMany(c)
		return
	}
	var req acceptReq
	if err := c.ShouldBindJSON(&req); err != nil || !validSecretToken(req.Token) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "bad request"})
		return
	}
	u, ok := s.tgProof(c, req)
	if !ok {
		return
	}
	if s.Auth != nil && s.Auth.team[u.ID] != "" {
		c.JSON(http.StatusConflict, gin.H{"error": "team", "detail": "Вы в команде клуба: CRM у вас уже есть на платформе. Приглашение открывает менеджер в своём Telegram"})
		return
	}
	h := hashSecretToken(req.Token)
	var got SalesMgr
	problem := ""
	err := s.mutate(c.Request.Context(), "server:managers", func(d *mgrDoc) bool {
		problem = ""
		var m *SalesMgr
		for i := range d.Managers {
			if d.Managers[i].InviteHash == h {
				m = &d.Managers[i]
			}
		}
		if m == nil || !m.Active() || !sTimeStr(m.InviteExp).After(s.now()) {
			problem = "Приглашение недействительно: истекло, уже принято или отозвано. Попросите новое"
			return false
		}
		for i := range d.Managers {
			if x := &d.Managers[i]; x.ID != m.ID && x.Tg == u.ID && x.Active() {
				problem = "Этот Telegram уже подключён как менеджер «" + x.Name + "»"
				return false
			}
		}
		m.Tg, m.TgName, m.InviteHash, m.InviteExp = u.ID, clip(tgDisplayName(u), 80), "", ""
		if m.AcceptedAt == "" {
			m.AcceptedAt = s.now().UTC().Format(time.RFC3339)
		}
		got = *m
		return true
	})
	if err != nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "try_again"})
		return
	}
	if problem != "" {
		c.JSON(http.StatusGone, gin.H{"error": "invite_invalid", "detail": problem})
		return
	}
	out, err := s.issue(c, &got, "tg:"+strconv.FormatInt(u.ID, 10), tgDisplayName(u), "приглашение")
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal server error"})
		return
	}
	if s.Send != nil {
		for _, id := range s.Admins {
			_ = s.Send(context.WithoutCancel(c.Request.Context()), id, "🧑‍💼 Менеджер «"+got.Name+"» принял приглашение и вошёл в CRM ("+firstNonBlank(got.TgName, "Telegram")+").", nil)
		}
	}
	c.JSON(http.StatusOK, out)
}

// LinkLogin: POST /api/v1/manager/link {token}: the personal link.
func (s *SalesManagers) LinkLogin(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	if !s.rl.allow("mlink:"+c.ClientIP(), 20, 10*time.Minute) {
		tooMany(c)
		return
	}
	var req struct {
		Token string `json:"token"`
	}
	if err := c.ShouldBindJSON(&req); err != nil || !validSecretToken(req.Token) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "bad request"})
		return
	}
	d, err := s.load(c.Request.Context(), true)
	if err != nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "try_again"})
		return
	}
	h := hashSecretToken(req.Token)
	for _, m := range d.Managers {
		if m.LinkHash == h && m.Active() && sTimeStr(m.LinkExp).After(s.now()) {
			out, err := s.issue(c, &m, "mlink:"+strconv.FormatInt(m.ID, 10), m.Name, "личная ссылка")
			if err != nil {
				c.JSON(http.StatusInternalServerError, gin.H{"error": "internal server error"})
				return
			}
			c.JSON(http.StatusOK, out)
			return
		}
	}
	c.JSON(http.StatusGone, gin.H{"error": "link_invalid", "detail": "Ссылка недействительна: истекла или отозвана. Попросите у владельца новую"})
}

// ── the bot: the morning plan, a step's time, a lead given to him ──

func (s *SalesManagers) crmURL() string {
	b := publicBase()
	if b == "" {
		b = "https://app.bxclub.kz"
	}
	return b + "/crm"
}

func (s *SalesManagers) openKB(text, query string) map[string]any {
	return map[string]any{"inline_keyboard": [][]map[string]any{{{"text": text, "web_app": map[string]string{"url": s.crmURL() + query}}}}}
}

// Loop: every 2 minutes.
func (s *SalesManagers) Loop(ctx context.Context) {
	t := time.NewTicker(2 * time.Minute)
	defer t.Stop()
	for {
		s.Tick(ctx)
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// Tick sends what is due now. Each message is marked in the lead (or the
// manager) before it goes: never twice.
func (s *SalesManagers) Tick(ctx context.Context) {
	if s.Send == nil {
		return
	}
	ctx, cancel := context.WithTimeout(ctx, time.Minute)
	defer cancel()
	d, err := s.load(ctx, true)
	if err != nil || len(d.Managers) == 0 {
		return
	}
	mgrs := map[int64]SalesMgr{}
	for _, m := range d.Managers {
		if m.Active() && m.Tg != 0 {
			mgrs[m.ID] = m
		}
	}
	if len(mgrs) == 0 {
		return
	}
	now := s.now()
	a := now.In(almaty)
	today := a.Format("2006-01-02")
	type note struct {
		to   int64
		text string
		kb   map[string]any
	}
	var out []note
	// leads: given to a manager, a step with a time
	err = r83Mutate(ctx, s.Docs, "club", "bs_crm", "server:managers", func(crm *map[string]any) bool {
		out = out[:0]
		if *crm == nil {
			return false
		}
		leads, _ := (*crm)["leads"].([]any)
		ch := false
		for _, v := range leads {
			l, ok := v.(map[string]any)
			if !ok {
				continue
			}
			id := mgrOf(l)
			m, ok := mgrs[id]
			if !ok {
				continue
			}
			if int64(claimInt(l["mgrNotified"])) != id {
				l["mgrNotified"] = id
				ch = true
				txt := "🆕 Вам передан лид: " + firstNonBlank(sStr(l, "name"), "Без имени") + "\n" + crmPhonePretty(sStr(l, "phone"))
				if src := sStr(l, "source"); src != "" {
					txt += " · " + src
				}
				if n := sStr(l, "niche"); n != "" {
					txt += "\nНиша: " + n
				}
				txt += "\n\nНазначьте следующий шаг в CRM."
				out = append(out, note{m.Tg, txt, s.openKB("Открыть лида", "?lead="+url.QueryEscape(sStr(l, "id")))})
			}
			if leadClosed(sStr(l, "col")) || sStr(l, "next") == "" || mgrDay(sStr(l, "nextAt")) != today || !hhmmRe(sStr(l, "nextTime")) {
				continue
			}
			key := today + " " + sStr(l, "nextTime")
			if sStr(l, "mgrRemind") == key {
				continue
			}
			at, _ := time.ParseInLocation("2006-01-02 15:04", key, almaty)
			if now.Before(at.Add(-10*time.Minute)) || now.After(at.Add(time.Hour)) {
				continue
			}
			l["mgrRemind"] = key
			ch = true
			txt := "⏰ " + sStr(l, "nextTime") + ": " + sStr(l, "next") + "\n" + firstNonBlank(sStr(l, "name"), "Без имени") + " · " + crmPhonePretty(sStr(l, "phone"))
			out = append(out, note{m.Tg, txt, s.openKB("Открыть лида", "?lead="+url.QueryEscape(sStr(l, "id")))})
		}
		return ch
	})
	if err != nil {
		log.Printf("managers: tick: %v", err)
		return
	}
	// the morning plan, from 09:00, once a day, only when there is something
	if a.Hour() >= 9 && a.Hour() < 20 {
		_, leads, err := s.crm(ctx)
		if err == nil {
			for id, m := range mgrs {
				if m.DigestDay == today {
					continue
				}
				st := s.statsOf(leads, id)
				claimed := false
				_ = s.mutate(ctx, "server:managers", func(d *mgrDoc) bool {
					x := d.byID(id)
					if x == nil || x.DigestDay == today {
						claimed = false
						return false
					}
					x.DigestDay, claimed = today, true
					return true
				})
				if !claimed || st.Today+st.Overdue+st.NoStep == 0 {
					continue
				}
				txt := fmt.Sprintf("☀️ План на сегодня, %s\n\nШагов на сегодня: %d", mgrFirst(m.Name), st.Today)
				if st.Overdue > 0 {
					txt += fmt.Sprintf("\nПросрочено: %d", st.Overdue)
				}
				if st.NoStep > 0 {
					txt += fmt.Sprintf("\nБез следующего шага: %d", st.NoStep)
				}
				out = append(out, note{m.Tg, txt, s.openKB("📋 План дня", "?v=plan")})
			}
		}
	}
	for _, n := range out {
		if err := s.Send(ctx, n.to, n.text, n.kb); err != nil {
			log.Printf("managers: bot → %d: %v", n.to, err)
		}
	}
}

// ── routes ──

// Register: the manager's API (role sales only), the team's management on
// the platform group g, the pages.
func (s *SalesManagers) Register(r *gin.Engine, pub, g *gin.RouterGroup) {
	mp := r.Group("/api/v1/manager")
	mp.POST("/login", s.Login)
	mp.GET("/invite-info", s.InviteInfo)
	mp.POST("/accept", s.Accept)
	mp.POST("/link", s.LinkLogin)
	a := r.Group("/api/v1/manager")
	a.Use(middleware.AuthJWT(s.Secret))
	a.Use(middleware.RequireRole(RoleSales))
	a.GET("/me", s.Me)
	a.GET("/leads", s.Leads)
	a.POST("/leads", s.Create)
	a.GET("/leads/:id", s.Lead)
	a.POST("/leads/:id", s.Update)
	a.POST("/leads/:id/take", s.Take)
	a.POST("/leads/:id/touch", s.Touch)
	a.POST("/leads/:id/book", s.Book)
	a.GET("/leads/:id/wa", s.WAChat)
	a.POST("/leads/:id/wa", s.WASend)
	a.GET("/scripts", s.Scripts)
	a.GET("/slots", s.Slots)
	a.GET("/stats", s.Stats)
	g.GET("/managers", s.List)
	g.GET("/managers/log", s.Log)
	g.POST("/managers/invite", s.Invite)
	g.POST("/managers/link", s.PersonalLink)
	g.POST("/managers/revoke", s.Revoke)
	g.POST("/managers/assign", s.Assign)
	r.GET("/crm", serveManagerApp)
	r.GET("/crm/scripts.js", ScriptsDefault)
	r.GET("/crm/join/:token", serveManagerJoin)
	r.GET("/crm/in/:token", serveManagerLink)
}
