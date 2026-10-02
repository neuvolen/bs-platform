package http

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/bnursik/business_surgery_backend/internal/club"
	"github.com/bnursik/business_surgery_backend/internal/middleware"
	pg "github.com/bnursik/business_surgery_backend/internal/repository/pg"
	"github.com/gin-gonic/gin"
	"github.com/golang-jwt/jwt/v5"
	"github.com/jackc/pgx/v5"
)

// ClubHandler serves the club data moved from the Google Sheet.
type ClubHandler struct {
	// StaticSeed is the page's own snapshot of the club data, for the parts
	// the server does not hold (NPS, leads, resident tasks).
	StaticSeed string

	repo      *pg.ClubRepo
	platform  *pg.PlatformRepo
	botToken  string
	jwtSecret []byte
}

func NewClubHandler(repo *pg.ClubRepo, platform *pg.PlatformRepo, botToken, jwtSecret string) *ClubHandler {
	return &ClubHandler{repo: repo, platform: platform, botToken: strings.TrimSpace(botToken), jwtSecret: []byte(jwtSecret)}
}

type ClubModule struct{ h *ClubHandler }

func NewClubModule(h *ClubHandler) *ClubModule { return &ClubModule{h: h} }

func (m *ClubModule) Register(r *gin.Engine) {
	// Import: signed by the Apps Script with the bot token, or sent by an admin.
	r.POST("/api/v1/club/import", m.h.Import)
	// Copy of ДДС and PL for the sheet, once the server keeps the club's data.
	r.POST("/api/v1/club/export", m.h.Export)

	g := r.Group("/api/v1/club")
	g.Use(middleware.AuthJWT(m.h.jwtSecret))
	g.Use(middleware.RequireRole("admin", "moderator"))
	g.GET("/status", m.h.Status)
	g.GET("/debet", m.h.Debet)
	g.GET("/payments", m.h.Payments)
	g.GET("/fines", m.h.Fines)
	g.GET("/meetings", m.h.Meetings)
	g.GET("/pl", m.h.PL)
}

// VerifyBotSignature checks X-BS-Signature = hex(HMAC-SHA256(body, bot token)).
func VerifyBotSignature(body []byte, header, botToken string) bool {
	if botToken == "" {
		return false
	}
	m := hmac.New(sha256.New, []byte(botToken))
	m.Write(body)
	got, err := hex.DecodeString(strings.ToLower(strings.TrimSpace(header)))
	return err == nil && hmac.Equal(m.Sum(nil), got)
}

// adminFromBearer accepts a platform JWT of an admin.
func (h *ClubHandler) adminFromBearer(c *gin.Context) (string, bool) {
	a := c.GetHeader("Authorization")
	if !strings.HasPrefix(strings.ToLower(a), "bearer ") {
		return "", false
	}
	tkn, err := jwt.Parse(strings.TrimSpace(a[7:]), func(t *jwt.Token) (any, error) {
		if _, ok := t.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, jwt.ErrSignatureInvalid
		}
		return h.jwtSecret, nil
	})
	if err != nil || !tkn.Valid {
		return "", false
	}
	cl, _ := tkn.Claims.(jwt.MapClaims)
	role, _ := cl["role"].(string)
	typ, _ := cl["typ"].(string)
	sub, _ := cl["sub"].(string)
	return sub, typ == "access" && (role == "admin" || role == "moderator")
}

type clubImportReq struct {
	TS     int64       `json:"ts"`
	Sheets club.Sheets `json:"sheets"`
}

type clubImportReport struct {
	DryRun        bool                `json:"dryRun"`
	Saved         bool                `json:"saved"`
	Counts        map[string]int      `json:"counts"`
	Warnings      []string            `json:"warnings"`
	Checked       int                 `json:"checked"`
	DebetMismatch []club.Mismatch     `json:"debetMismatch"`
	PLMismatch    []club.Mismatch     `json:"plMismatch"`
	Unplaced      []club.UnknownEntry `json:"unplaced"`
	PlatformUsers int                 `json:"platformResidents"`
	Reapplied     int                 `json:"reapplied"` // server writes put back on top of the sheet's copy
}

// Import godoc
// @Summary      Import the club data from the Google Sheet
// @Description  Body {ts, sheets:{name:[[cells…]]}} with display values. Signed by the Apps Script (X-BS-Signature = HMAC-SHA256 of the body with the bot token) or sent with an admin token. ?dry=1 only checks. The answer compares the server's debts and P&L with the sheet's.
// @Tags         club
// @Accept       json
// @Produce      json
// @Router       /api/v1/club/import [post]
func (h *ClubHandler) Import(c *gin.Context) {
	body, err := io.ReadAll(http.MaxBytesReader(c.Writer, c.Request.Body, 16<<20))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "body too large or unreadable"})
		return
	}
	by := "sheet"
	if sig := c.GetHeader("X-BS-Signature"); sig != "" {
		if !VerifyBotSignature(body, sig, h.botToken) {
			c.JSON(http.StatusUnauthorized, gin.H{"error": "bad_signature"})
			return
		}
	} else if sub, ok := h.adminFromBearer(c); ok {
		by = sub
	} else {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
		return
	}
	var req clubImportReq
	if err := json.Unmarshal(body, &req); err != nil || req.Sheets == nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "body must be {ts, sheets}"})
		return
	}
	if by == "sheet" {
		if d := time.Since(time.Unix(req.TS, 0)); d > time.Hour || d < -5*time.Minute {
			c.JSON(http.StatusUnauthorized, gin.H{"error": "stale_request"})
			return
		}
	}
	dry := c.Query("dry") == "1"
	ctx := c.Request.Context()
	// Once the server keeps the club's data, the sheet is a copy: its data
	// must not come back and overwrite the server's, whatever it holds.
	if m, err := h.repo.Master(ctx); err == nil && m == "server" {
		c.JSON(http.StatusConflict, gin.H{"error": "server_is_master", "detail": "Данные уже ведутся на сервере, таблица их не перезаписывает"})
		return
	}

	snap, warn, perr := club.Parse(req.Sheets)
	if perr != nil {
		c.JSON(http.StatusUnprocessableEntity, gin.H{"error": "parse", "detail": perr.Error(), "warnings": warn})
		return
	}
	upTo := int(club.Today().Month())
	if snap.PL != nil && snap.PL.Year != 0 && snap.PL.Year < club.Today().Year() {
		upTo = 12
	}
	debetMiss, plMiss, checked := club.Check(snap, upTo)
	rep := clubImportReport{
		DryRun: dry, Warnings: nz(warn), Checked: checked,
		DebetMismatch: nzm(debetMiss), PLMismatch: nzm(plMiss), Unplaced: []club.UnknownEntry{},
		Counts: map[string]int{"payments": len(snap.Payments), "fines": len(snap.Fines), "meetings": len(snap.Meetings),
			"reports": len(snap.Reports), "meetingLog": len(snap.MeetingLog), "settings": len(snap.Settings)},
	}
	for _, p := range snap.Residents {
		if p.Former {
			rep.Counts["former"]++
		} else {
			rep.Counts["residents"]++
		}
	}
	if snap.PL != nil && snap.PL.Year != 0 {
		rep.Unplaced = append(rep.Unplaced, club.BuildPL(snap.Payments, snap.PL, snap.PL.Year, upTo).Unknown...)
	}

	if !dry {
		// Club writes the sheet did not have when it sent this copy are put back on top.
		sheetAt := time.Now()
		if req.TS > 0 {
			sheetAt = time.Unix(req.TS, 0)
		}
		reapply := func(ctx context.Context, tx pgx.Tx) error {
			n, err := h.repo.ReapplyAfterImport(ctx, tx, sheetAt)
			rep.Reapplied = n
			return err
		}
		if err := h.repo.ReplaceAllThen(ctx, snap, by, reapply); err != nil {
			if errors.Is(err, pg.ErrServerIsMaster) {
				c.JSON(http.StatusConflict, gin.H{"error": "server_is_master", "detail": "Данные уже ведутся на сервере, таблица их не перезаписывает"})
				return
			}
			c.JSON(http.StatusInternalServerError, gin.H{"error": "internal server error"})
			return
		}
		rep.Saved = true
		// The platform's club sections show the new numbers.
		if err := RefreshPlatformSeed(ctx, h.repo, h.platform, h.StaticSeed); err != nil {
			log.Printf("platform seed: %v", err)
		}
		// Who may log into the platform as a resident follows the same list.
		var list []pg.PlatformResident
		for _, p := range snap.Residents {
			if p.TgID > 0 && !p.Admin {
				list = append(list, pg.PlatformResident{TgID: p.TgID, Name: p.Name, Active: !p.Former})
			}
		}
		if len(list) > 0 && h.platform != nil {
			if n, err := h.platform.ReplaceResidents(ctx, list); err == nil {
				rep.PlatformUsers = n
			}
		}
	}
	_ = h.repo.LogImport(ctx, by, dry, body, rep)
	c.JSON(http.StatusOK, rep)
}

func nz(v []string) []string {
	if v == nil {
		return []string{}
	}
	return v
}

func nzm(v []club.Mismatch) []club.Mismatch {
	if v == nil {
		return []club.Mismatch{}
	}
	return v
}

// Status godoc
// @Summary  What the server holds and who is the source of truth
// @Tags     club
// @Security BearerAuth
// @Router   /api/v1/club/status [get]
func (h *ClubHandler) Status(c *gin.Context) {
	master, err := h.repo.Master(c.Request.Context())
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal server error"})
		return
	}
	counts, err := h.repo.Counts(c.Request.Context())
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal server error"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"master": master, "counts": counts})
}

func (h *ClubHandler) load(c *gin.Context) (*club.Snapshot, bool) {
	s, err := h.repo.Load(c.Request.Context())
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal server error"})
		return nil, false
	}
	return s, true
}

// Debet godoc
// @Summary  Residents with meetings left, unpaid fines and total debt
// @Tags     club
// @Security BearerAuth
// @Router   /api/v1/club/debet [get]
func (h *ClubHandler) Debet(c *gin.Context) {
	s, ok := h.load(c)
	if !ok {
		return
	}
	rows := club.Debet(s.Residents, s.Fines)
	var total, fines int64
	for _, r := range rows {
		total += r.TotalDebt
		fines += r.FinesUnpaid
	}
	c.JSON(http.StatusOK, gin.H{"residents": rows, "totalDebt": total, "unpaidFines": fines})
}

// Payments godoc
// @Summary  Cash journal (ДДС)
// @Tags     club
// @Security BearerAuth
// @Param    year query int false "year, default all"
// @Router   /api/v1/club/payments [get]
func (h *ClubHandler) Payments(c *gin.Context) {
	s, ok := h.load(c)
	if !ok {
		return
	}
	year, _ := strconv.Atoi(c.Query("year"))
	out := []club.Payment{}
	for _, p := range s.Payments {
		if year == 0 || p.Date.Year() == year {
			out = append(out, p)
		}
	}
	c.JSON(http.StatusOK, gin.H{"payments": out})
}

// Fines godoc
// @Summary  Fines
// @Tags     club
// @Security BearerAuth
// @Router   /api/v1/club/fines [get]
func (h *ClubHandler) Fines(c *gin.Context) {
	s, ok := h.load(c)
	if !ok {
		return
	}
	if s.Fines == nil {
		s.Fines = []club.Fine{}
	}
	c.JSON(http.StatusOK, gin.H{"fines": s.Fines})
}

// Meetings godoc
// @Summary  Scheduled meetings
// @Tags     club
// @Security BearerAuth
// @Router   /api/v1/club/meetings [get]
func (h *ClubHandler) Meetings(c *gin.Context) {
	s, ok := h.load(c)
	if !ok {
		return
	}
	if s.Meetings == nil {
		s.Meetings = []club.Meeting{}
	}
	c.JSON(http.StatusOK, gin.H{"meetings": s.Meetings})
}

// PL godoc
// @Summary  P&L built from the cash journal
// @Tags     club
// @Security BearerAuth
// @Param    year query int false "default: the P&L year"
// @Param    upto query int false "last month to include, default: current month"
// @Router   /api/v1/club/pl [get]
func (h *ClubHandler) PL(c *gin.Context) {
	s, ok := h.load(c)
	if !ok {
		return
	}
	year, _ := strconv.Atoi(c.Query("year"))
	if year == 0 && s.PL != nil {
		year = s.PL.Year
	}
	if year == 0 {
		year = club.Today().Year()
	}
	upTo, _ := strconv.Atoi(c.Query("upto"))
	if upTo < 1 || upTo > 12 {
		upTo = 12
		if year == club.Today().Year() {
			upTo = int(club.Today().Month())
		}
	}
	c.JSON(http.StatusOK, club.BuildPL(s.Payments, s.PL, year, upTo))
}

// ExportPayment is one ДДС row as the sheet writes it.
type ExportPayment struct {
	Date       string `json:"date"`
	Income     int64  `json:"income"`
	Expense    int64  `json:"expense"`
	IncomeCat  string `json:"incomeCat"`
	Resident   string `json:"resident"`
	ExpenseCat string `json:"expenseCat"`
	Applied    bool   `json:"applied"`
}

// ExportPLLine is one line of the PL sheet: its name and 12 months (null: empty cell).
type ExportPLLine struct {
	Name   string   `json:"name"`
	Values []*int64 `json:"values"`
}

// ExportPL turns the computed P&L into the sheet's lines, by line name.
func ExportPL(sheet *club.PLSheet, pl *club.PL) []ExportPLLine {
	if sheet == nil || pl == nil {
		return nil
	}
	var out []ExportPLLine
	for _, row := range sheet.Rows {
		line := ExportPLLine{Name: row.Name, Values: make([]*int64, 12)}
		for m := 0; m < 12; m++ {
			if m+1 > pl.UpTo {
				continue
			}
			mo := pl.Months[m]
			var v int64
			ok := true
			switch row.Section {
			case "income":
				v = mo.Income[row.Name]
			case "expense":
				v = mo.Expense[row.Name]
			case "total_income":
				v = mo.IncomeSum
			case "total_expense":
				v = mo.Expenses
			case "profit":
				v = mo.Profit
			case "dividends":
				v = mo.Dividends
			case "cash":
				v, ok = mo.Cash, mo.HasCash
			default:
				ok = false
			}
			if ok {
				x := v
				line.Values[m] = &x
			}
		}
		out = append(out, line)
	}
	return out
}

// Export godoc
// @Summary  ДДС and PL for the sheet's copy
// @Description  Signed by the sheet (X-BS-Signature). Only answers once the server keeps the club's data (master = server).
// @Tags     club
// @Router   /api/v1/club/export [post]
func (h *ClubHandler) Export(c *gin.Context) {
	body, err := io.ReadAll(http.MaxBytesReader(c.Writer, c.Request.Body, 1<<20))
	if err != nil || !VerifyBotSignature(body, c.GetHeader("X-BS-Signature"), h.botToken) {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "bad_signature"})
		return
	}
	var req struct {
		TS int64 `json:"ts"`
	}
	if json.Unmarshal(body, &req) != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "bad json"})
		return
	}
	if d := time.Since(time.Unix(req.TS, 0)); d > time.Hour || d < -5*time.Minute {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "stale_request"})
		return
	}
	ctx := c.Request.Context()
	if m, err := h.repo.Master(ctx); err != nil || m != "server" {
		c.JSON(http.StatusConflict, gin.H{"error": "sheet_is_master"})
		return
	}
	s, ok := h.load(c)
	if !ok {
		return
	}
	pays := make([]ExportPayment, 0, len(s.Payments))
	for _, p := range s.Payments {
		pays = append(pays, ExportPayment{Date: p.Date.In(club.Almaty).Format("02.01.2006"), Income: p.Income, Expense: p.Expense,
			IncomeCat: p.IncomeCat, Resident: p.Resident, ExpenseCat: p.ExpenseCat, Applied: p.Applied})
	}
	year := club.Today().Year()
	if s.PL != nil && s.PL.Year != 0 {
		year = s.PL.Year
	}
	upTo := 12
	if year == club.Today().Year() {
		upTo = int(club.Today().Month())
	}
	c.JSON(http.StatusOK, gin.H{"payments": pays, "pl": ExportPL(s.PL, club.BuildPL(s.Payments, s.PL, year, upTo))})
}

// RefreshPlatformSeed puts the club data into the platform's bs_seed: live
// numbers once the server holds the club's data, the page's snapshot before.
func RefreshPlatformSeed(ctx context.Context, clubRepo *pg.ClubRepo, platform *pg.PlatformRepo, static string) error {
	if platform == nil {
		return nil
	}
	snap, err := clubRepo.Load(ctx)
	if err != nil {
		return err
	}
	if len(snap.Residents) == 0 {
		return platform.PutServerDoc(ctx, platformSeedKey, static)
	}
	seed, err := club.LiveSeed(snap, static, time.Now())
	if err != nil {
		return err
	}
	return platform.PutServerDoc(ctx, platformSeedKey, seed)
}
