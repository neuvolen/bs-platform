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

	"github.com/bnursik/business_surgery_backend/internal/club"
	"github.com/bnursik/business_surgery_backend/internal/middleware"
	"github.com/gin-gonic/gin"
)

// The resident card's settings, edited by the team on the platform:
// Исключение для отчётов, Формат (онлайн/офлайн), Дата входа, Партнёр and
// Telegram Chat ID. A change goes the write-first way like every club write:
// kept and applied on the server at once (the bot's report reminders and
// fines read club_residents, so an exception counts immediately), then
// written to «BS - резиденты дебет» by the script (setResidentField v37,
// setPartner for the partner, both sides). The club journal (club_ops) keeps
// who changed what, with the value before.

// ResidentCard is what the editor shows for one resident.
type ResidentCard struct {
	Name      string `json:"name"`
	Exception bool   `json:"exception"`
	Format    string `json:"format"`
	JoinedAt  string `json:"joinedAt"` // 30.09.2026
	Partner   string `json:"partner"`
	ChatID    string `json:"chatId"`
	Former    bool   `json:"former,omitempty"`
	Admin     bool   `json:"admin,omitempty"`
}

func residentCard(r club.Resident) ResidentCard {
	c := ResidentCard{Name: r.Name, Exception: r.Exception, Format: r.Format, Partner: r.Partner, Former: r.Former, Admin: r.Admin}
	if r.JoinedAt != nil {
		c.JoinedAt = r.JoinedAt.In(club.Almaty).Format("02.01.2006")
	}
	if r.TgID > 0 {
		c.ChatID = strconv.FormatInt(r.TgID, 10)
	}
	return c
}

// cardValue: the field's current value as the editor sends it.
func cardValue(c ResidentCard, field string) string {
	switch field {
	case "exception":
		if c.Exception {
			return "Да"
		}
		return "Нет"
	case "format":
		return c.Format
	case "joinedAt":
		return c.JoinedAt
	case "partner":
		return c.Partner
	case "chatId":
		return c.ChatID
	}
	return ""
}

// ResidentEditParams checks one change and turns it into the club write
// that makes it: setPartner for the partner, setResidentField otherwise.
// list is the debet sheet's residents (for the partner and duplicates).
func ResidentEditParams(list []club.Resident, name, field, value string) (string, map[string]string, *club.Resident, error) {
	var res *club.Resident
	for i := range list {
		if !list[i].Archived && club.NormName(list[i].Name) == club.NormName(name) {
			res = &list[i]
			break
		}
	}
	if res == nil {
		return "", nil, nil, fmt.Errorf("резидент «%s» не найден", name)
	}
	value = strings.TrimSpace(value)
	p := map[string]string{"name": res.Name}
	switch field {
	case "partner":
		if value != "" {
			var o *club.Resident
			for i := range list {
				if !list[i].Archived && club.NormName(list[i].Name) == club.NormName(value) {
					o = &list[i]
					break
				}
			}
			if o == nil {
				return "", nil, nil, fmt.Errorf("партнёр «%s» не найден среди резидентов", value)
			}
			if o.Name == res.Name {
				return "", nil, nil, fmt.Errorf("резидент не может быть партнёром сам себе")
			}
			value = o.Name
		}
		p["partner"] = value
		return "setPartner", p, res, nil
	case "exception":
		v, err := club.EditExceptionValue(value)
		if err != nil {
			return "", nil, nil, err
		}
		value = v
	case "format":
		v, err := club.EditFormatValue(value)
		if err != nil {
			return "", nil, nil, err
		}
		value = v
	case "joinedAt":
		d, err := club.EditJoinDate(value, club.Today())
		if err != nil {
			return "", nil, nil, err
		}
		value = d.Format("02.01.2006")
	case "chatId":
		v, err := club.EditChatID(value)
		if err != nil {
			return "", nil, nil, err
		}
		if v != "" {
			id, _ := strconv.ParseInt(v, 10, 64)
			for _, o := range list {
				if !o.Archived && !o.Former && o.TgID == id && o.Name != res.Name {
					return "", nil, nil, fmt.Errorf("Chat ID %s уже у резидента «%s»", v, o.Name)
				}
			}
		}
		value = v
	default:
		return "", nil, nil, fmt.Errorf("поле «%s» не меняется", field)
	}
	p["field"], p["value"] = field, value
	return "setResidentField", p, res, nil
}

func (h *ClubActionHandler) debet(ctx context.Context) ([]club.Resident, error) {
	snap, err := h.club.Load(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]club.Resident, 0, len(snap.Residents))
	for _, r := range snap.Residents {
		if !r.Archived {
			out = append(out, r)
		}
	}
	return out, nil
}

// Residents godoc
// @Summary  The residents' card settings (exception, format, entry date, partner, Telegram chat id) for the team's editor
// @Tags     club
// @Security BearerAuth
// @Router   /api/v1/club/residents [get]
func (h *ClubActionHandler) Residents(c *gin.Context) {
	list, err := h.debet(c.Request.Context())
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	out := make([]ResidentCard, 0, len(list))
	for _, r := range list {
		out = append(out, residentCard(r))
	}
	c.JSON(http.StatusOK, gin.H{"residents": out, "fields": club.ResidentEditFields})
}

// EditResident godoc
// @Summary  Change one setting of a resident: {name, field, value}
// @Description  field: exception (Да/Нет: excluded from report reminders and fines), format (Онлайн/Офлайн), joinedAt (30.09.2026), partner (a resident's name or empty), chatId (digits or empty). Saved on the server at once and written to the sheet by the script (queued while it does not answer).
// @Tags     club
// @Security BearerAuth
// @Router   /api/v1/club/resident [post]
func (h *ClubActionHandler) EditResident(c *gin.Context) {
	var req struct {
		Name  string `json:"name"`
		Field string `json:"field"`
		Value any    `json:"value"`
	}
	if err := c.ShouldBindJSON(&req); err != nil || strings.TrimSpace(req.Name) == "" || req.Field == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "bad_params", "detail": "нужны name, field и value"})
		return
	}
	value := ""
	switch v := req.Value.(type) {
	case string:
		value = v
	case bool:
		value = map[bool]string{true: "Да", false: "Нет"}[v]
	case float64:
		value = strconv.FormatFloat(v, 'f', -1, 64)
	case nil:
	default:
		value = fmt.Sprint(v)
	}
	tg, err := strconv.ParseInt(strings.TrimPrefix(platformUser(c), "tg:"), 10, 64)
	if err != nil || tg <= 0 {
		c.JSON(http.StatusForbidden, gin.H{"error": "not_telegram_user"})
		return
	}
	if h.gw == nil || h.gw.Writes == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "writes_off", "detail": "запись в клуб на сервере не настроена"})
		return
	}
	ctx := c.Request.Context()
	list, err := h.debet(ctx)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	action, params, res, err := ResidentEditParams(list, req.Name, req.Field, value)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "bad_params", "detail": err.Error()})
		return
	}
	before := residentCard(*res)
	newValue := params["value"]
	if action == "setPartner" {
		newValue = params["partner"]
	}
	if cardValue(before, req.Field) == newValue {
		c.JSON(http.StatusOK, gin.H{"ok": true, "unchanged": true, "resident": before})
		return
	}
	in := url.Values{}
	for k, v := range params {
		in.Set(k, v)
	}
	u := &platformTgUser{ID: tg}
	if n, ok := h.gw.Admins[tg]; ok {
		u.FirstName = n
	}
	q := h.gw.params(in, action, u)
	body := h.gw.Writes.Do(ctx, "platform", u, action, q, true)
	// The journal: what changed, from what, by whom.
	lq := url.Values{}
	for k, v := range q {
		lq[k] = v
	}
	lq.Set("edit", req.Field)
	lq.Set("prev", cardValue(before, req.Field))
	h.gw.logOp(ctx, "platform", u, action, lq, body)
	var out map[string]any
	_ = json.Unmarshal(body, &out)
	if e, _ := out["error"].(string); e != "" {
		c.JSON(http.StatusUnprocessableEntity, gin.H{"error": "script", "detail": e})
		return
	}
	log.Printf("club resident %q: %s %q → %q by %d", res.Name, req.Field, cardValue(before, req.Field), newValue, tg)
	if err := RefreshPlatformSeed(ctx, h.club, h.platform, h.seed); err != nil {
		log.Printf("platform seed: %v", err)
	}
	after := before
	if l, err := h.debet(ctx); err == nil {
		for _, r := range l {
			if r.Name == res.Name {
				after = residentCard(r)
			}
		}
	}
	queued, _ := out["queued"].(bool)
	c.JSON(http.StatusOK, gin.H{"ok": true, "queued": queued, "resident": after, "action": action})
}

// ClubResidentModule: the team's resident editor.
type ClubResidentModule struct {
	h      *ClubActionHandler
	secret []byte
}

func NewClubResidentModule(h *ClubActionHandler, secret []byte) *ClubResidentModule {
	return &ClubResidentModule{h: h, secret: secret}
}

func (m *ClubResidentModule) Register(r *gin.Engine) {
	g := r.Group("/api/v1/club")
	g.Use(middleware.AuthJWT(m.secret))
	g.Use(middleware.RequireRole("admin", "moderator"))
	g.GET("/residents", m.h.Residents)
	g.POST("/resident", m.h.EditResident)
}
