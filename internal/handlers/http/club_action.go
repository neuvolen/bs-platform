package http

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/bnursik/business_surgery_backend/internal/club"
	"github.com/bnursik/business_surgery_backend/internal/middleware"
	pg "github.com/bnursik/business_surgery_backend/internal/repository/pg"
	"github.com/gin-gonic/gin"
)

// Entering the club's money and meetings from the platform.
//
// While the Google Sheet keeps the club's data, a platform button does exactly
// what the same button in the Telegram app does: the script writes the row
// into the sheet (with its checks against double entries and its
// recalculations). The server puts the same row into its own tables at once,
// so the platform shows it immediately; the hourly import from the sheet then
// replaces it with the sheet's own row.

// ClubActions lists what the platform may do, with the parameters the script
// expects.
var clubActions = map[string][]string{
	"addPayment":  {"type", "src", "amount", "isCash", "resident"},
	"addFine":     {"name", "type", "amount"},
	"updateFine":  {"name", "date", "type", "amount", "status"},
	"addSchedule": {"res", "date", "time"},
}

type ClubActionHandler struct {
	gw       *AppGateway
	club     *pg.ClubRepo
	platform *pg.PlatformRepo
	seed     string
}

func NewClubActionHandler(gw *AppGateway, clubRepo *pg.ClubRepo, platform *pg.PlatformRepo, staticSeed string) *ClubActionHandler {
	return &ClubActionHandler{gw: gw, club: clubRepo, platform: platform, seed: staticSeed}
}

type clubActionReq struct {
	Action string            `json:"action"`
	Params map[string]string `json:"params"`
}

// Action godoc
// @Summary  Enter a payment, fine, paid fine or meeting from the platform
// @Description  Team only. {action: addPayment|addFine|updateFine|addSchedule, params:{…}} with the Telegram app's parameters. Written into the sheet by the script while the sheet keeps the club's data.
// @Tags     club
// @Security BearerAuth
// @Router   /api/v1/club/action [post]
func (h *ClubActionHandler) Action(c *gin.Context) {
	var req clubActionReq
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "bad json"})
		return
	}
	keys, ok := clubActions[req.Action]
	if !ok {
		c.JSON(http.StatusBadRequest, gin.H{"error": "unknown action"})
		return
	}
	params := map[string]string{}
	for _, k := range keys {
		if v, ok := req.Params[k]; ok {
			params[k] = strings.TrimSpace(v)
		}
	}
	if msg := validateClubAction(req.Action, params); msg != "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "bad_params", "detail": msg})
		return
	}
	ctx := c.Request.Context()
	if m, err := h.club.Master(ctx); err == nil && m == "server" {
		c.JSON(http.StatusConflict, gin.H{"error": "server_is_master", "detail": "ввод на сервере включается вместе с переводом данных клуба"})
		return
	}
	tg, err := strconv.ParseInt(strings.TrimPrefix(platformUser(c), "tg:"), 10, 64)
	if err != nil || tg <= 0 {
		c.JSON(http.StatusForbidden, gin.H{"error": "not_telegram_user"})
		return
	}
	res, err := h.gw.CallAs(ctx, tg, "", req.Action, params)
	if err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": "script", "detail": err.Error()})
		return
	}
	if e, _ := res["error"].(string); e != "" {
		c.JSON(http.StatusUnprocessableEntity, gin.H{"error": "script", "detail": e})
		return
	}
	dup, _ := res["deduplicated"].(bool)
	if !dup {
		if err := h.mirror(ctx, req.Action, params); err != nil {
			log.Printf("club action mirror: %v", err)
		}
	}
	if err := RefreshPlatformSeed(ctx, h.club, h.platform, h.seed); err != nil {
		log.Printf("platform seed: %v", err)
	}
	c.JSON(http.StatusOK, gin.H{"ok": true, "duplicate": dup, "script": res})
}

func validateClubAction(action string, p map[string]string) string {
	num := func(k string) bool { n, err := strconv.ParseInt(p[k], 10, 64); return err == nil && n > 0 }
	switch action {
	case "addPayment":
		if p["type"] != "income" && p["type"] != "expense" {
			return "type: income или expense"
		}
		if !num("amount") || p["src"] == "" {
			return "нужны сумма и статья"
		}
		if p["isCash"] != "true" {
			p["isCash"] = "false"
		}
	case "addFine":
		if p["name"] == "" || !num("amount") {
			return "нужны резидент и сумма"
		}
		if p["type"] == "" {
			p["type"] = "Штраф"
		}
	case "updateFine":
		if p["name"] == "" || p["date"] == "" || !num("amount") {
			return "нужны резидент, дата и сумма штрафа"
		}
		if p["status"] == "" {
			p["status"] = "Оплатил"
		}
	case "addSchedule":
		if p["res"] == "" {
			return "нужен резидент"
		}
		if _, err := time.Parse("02.01.2006", p["date"]); err != nil {
			return "дата в виде 30.09.2026"
		}
		if _, err := time.Parse("15:04", p["time"]); err != nil {
			return "время в виде 15:00"
		}
	}
	return ""
}

// mirror puts the row the script just wrote into the server's tables.
func (h *ClubActionHandler) mirror(ctx context.Context, action string, p map[string]string) error {
	today := club.Today().Format("2006-01-02")
	amount, _ := strconv.ParseInt(p["amount"], 10, 64)
	db := h.club.DB()
	switch action {
	case "addPayment":
		if p["type"] == "income" {
			// As the script writes it: a bank payment carries 4% commission and tax
			// as an expense on the same row.
			var fee int64
			feeCat := ""
			if p["isCash"] != "true" {
				fee, feeCat = (amount*4+50)/100, "Комиссия+налог"
			}
			_, err := db.Exec(ctx, `INSERT INTO club_payments (date, income, expense, income_cat, resident, expense_cat, source, created_by)
				VALUES ($1,$2,$3,$4,$5,$6,'platform','platform')`, today, amount, fee, p["src"], p["resident"], feeCat)
			return err
		}
		_, err := db.Exec(ctx, `INSERT INTO club_payments (date, expense, expense_cat, source, created_by) VALUES ($1,$2,$3,'platform','platform')`,
			today, amount, p["src"])
		return err
	case "addFine":
		_, err := db.Exec(ctx, `INSERT INTO club_fines (resident, type, amount, date, created_by) VALUES ($1,$2,$3,$4,'platform')`,
			p["name"], p["type"], amount, today)
		return err
	case "updateFine":
		d, err := time.Parse("02.01.2006", p["date"])
		if err != nil {
			return err
		}
		_, err = db.Exec(ctx, `UPDATE club_fines SET paid = $5, paid_at = CASE WHEN $5 THEN now() END
			WHERE id = (SELECT id FROM club_fines WHERE resident = $1 AND type = $2 AND amount = $3 AND date = $4 ORDER BY id LIMIT 1)`,
			p["name"], p["type"], amount, d.Format("2006-01-02"), p["status"] == "Оплатил")
		return err
	case "addSchedule":
		d, _ := time.Parse("02.01.2006", p["date"])
		_, err := db.Exec(ctx, `INSERT INTO club_meetings (resident, date, time) VALUES ($1,$2,$3)`, p["res"], d.Format("2006-01-02"), p["time"])
		return err
	}
	return fmt.Errorf("unknown action %s", action)
}

type ClubActionModule struct {
	h      *ClubActionHandler
	secret []byte
}

func NewClubActionModule(h *ClubActionHandler, secret []byte) *ClubActionModule {
	return &ClubActionModule{h: h, secret: secret}
}

func (m *ClubActionModule) Register(r *gin.Engine) {
	g := r.Group("/api/v1/club")
	g.Use(middleware.AuthJWT(m.secret))
	g.Use(middleware.RequireRole("admin", "moderator"))
	g.POST("/action", m.h.Action)
}
