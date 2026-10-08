package http

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/bnursik/business_surgery_backend/internal/club"
	pg "github.com/bnursik/business_surgery_backend/internal/repository/pg"
	"github.com/gin-gonic/gin"
)

// R32a: «Учёт» on the platform edits the cash journal (ДДС) as a spreadsheet
// and builds the P&L from it in the page, with the same rules as
// club.BuildPL. GET gives the journal with the P&L lines; POST takes a batch
// of cell edits, added and deleted rows.

func registerDDS(g *gin.RouterGroup, h *ClubHandler) {
	g.GET("/dds", h.DDS)
	g.POST("/dds", h.DDSSave)
	g.POST("/dds/link", h.DDSLink)
}

type ddsLinkReq struct {
	ID       int64  `json:"id"`
	Resident string `json:"resident"`
}

// DDSLink godoc
// @Summary  «Привязать платёж»: a resident's payment counted against their debt
// @Description  {id, resident}: the ДДС income row is linked to the resident and pays off the rest of the entry fee, then the renewal debt (once; the row becomes «учтено»). For a payment the server could not match to a resident by name.
// @Tags     club
// @Security BearerAuth
// @Router   /api/v1/club/dds/link [post]
func (h *ClubHandler) DDSLink(c *gin.Context) {
	var req ddsLinkReq
	if err := c.ShouldBindJSON(&req); err != nil || req.ID <= 0 || strings.TrimSpace(req.Resident) == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "bad_params", "detail": "нужны строка ДДС и резидент"})
		return
	}
	ctx := c.Request.Context()
	if _, editable := ddsMaster(ctx, h.repo); !editable {
		c.JSON(http.StatusConflict, gin.H{"error": "sheet_is_master", "detail": "ДДС пока только для просмотра"})
		return
	}
	res, err := h.repo.LinkPayment(ctx, req.ID, req.Resident)
	if err != nil {
		var in *pg.ErrLinkInput
		if errors.As(err, &in) {
			c.JSON(http.StatusBadRequest, gin.H{"error": "bad_params", "detail": in.Msg})
			return
		}
		log.Printf("dds link: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal server error"})
		return
	}
	who := platformUser(c)
	p := map[string]string{"id": strconv.FormatInt(req.ID, 10), "resident": res.Resident, "paid": strconv.FormatInt(res.Paid, 10)}
	if err := h.repo.LogOp(ctx, pg.ClubOp{Source: "platform", Who: who, Action: "linkPayment", Params: p, OK: true}); err != nil {
		log.Printf("dds link log: %v", err)
	}
	if err := RefreshPlatformSeed(ctx, h.repo, h.platform, h.StaticSeed); err != nil {
		log.Printf("platform seed: %v", err)
	}
	c.JSON(http.StatusOK, gin.H{"ok": true, "resident": res.Resident, "paid": res.Paid, "applied": res.Applied})
}

type ddsPLInfo struct {
	Year        int      `json:"year"`
	UpTo        int      `json:"upTo"`
	CashStart   int      `json:"cashStart"`
	IncomeRows  []string `json:"incomeRows"`
	ExpenseRows []string `json:"expenseRows"`
}

// DDS godoc
// @Summary  Cash journal (ДДС) for the spreadsheet in «Учёт»
// @Description  Every row with its id, the P&L lines (income and expense articles from the PL structure), residents' names, and whether the platform may edit (master = server).
// @Tags     club
// @Security BearerAuth
// @Router   /api/v1/club/dds [get]
func (h *ClubHandler) DDS(c *gin.Context) {
	ctx := c.Request.Context()
	rows, err := h.repo.DDSRows(ctx)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal server error"})
		return
	}
	s, ok := h.load(c)
	if !ok {
		return
	}
	master, editable := ddsMaster(ctx, h.repo)
	now := club.Today()
	info := ddsPLInfo{Year: now.Year(), UpTo: int(now.Month()), CashStart: club.CashStartMonth, IncomeRows: []string{}, ExpenseRows: []string{}}
	// The same year and months as the platform's P&L (club.LiveSeed)
	if s.PL != nil && s.PL.Year != 0 && s.PL.Year < info.Year {
		info.Year, info.UpTo = s.PL.Year, 12
	}
	if s.PL != nil {
		for _, r := range s.PL.Rows {
			switch r.Section {
			case "income":
				info.IncomeRows = append(info.IncomeRows, r.Name)
			case "expense":
				info.ExpenseRows = append(info.ExpenseRows, r.Name)
			}
		}
	}
	names := []string{}
	seen := map[string]bool{}
	for _, r := range s.Residents {
		n := strings.TrimSpace(r.Name)
		if n != "" && !r.Admin && !seen[n] {
			seen[n] = true
			names = append(names, n)
		}
	}
	sort.Strings(names)
	c.JSON(http.StatusOK, gin.H{"master": master, "editable": editable, "mode": club.SheetMode(), "pl": info, "residents": names, "rows": rows})
}

// ddsMaster: who keeps the journal and whether the platform edits it. After
// the cutover (R32d) the server owns the data and the grid is the place to
// edit ДДС; the edits land in club_payments, the same rows the sheet's
// read-only copy (POST /club/export) takes. Read-only only in the legacy
// rollback (SHEET_MODE=legacy: the sheet is the master again) and while the
// cutover still waits for the sheet's final import (that import replaces
// the tables, an edit made meanwhile would be lost).
func ddsMaster(ctx context.Context, repo interface {
	Master(ctx context.Context) (string, error)
}) (string, bool) {
	if club.SheetLegacy() {
		return "sheet", false
	}
	m, err := repo.Master(ctx)
	if err != nil || m == "" {
		m = "sheet"
	}
	return m, m == "server"
}

type ddsSaveReq struct {
	Ops []pg.DDSOp `json:"ops"`
}

// DDSSave godoc
// @Summary  Save edits of the cash journal (ДДС)
// @Description  {ops:[{op:"insert",cid,set:{date,income,expense,incomeCat,resident,expenseCat,account,comment}}, {op:"update",id,set:{…}}, {op:"delete",id}]}, all in one transaction. Answer {ok, ids:{cid:id}, rows, deleted}. 409 while the sheet keeps the club's data.
// @Tags     club
// @Security BearerAuth
// @Router   /api/v1/club/dds [post]
func (h *ClubHandler) DDSSave(c *gin.Context) {
	var req ddsSaveReq
	if err := json.NewDecoder(http.MaxBytesReader(c.Writer, c.Request.Body, 4<<20)).Decode(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "bad json"})
		return
	}
	ctx := c.Request.Context()
	if _, editable := ddsMaster(ctx, h.repo); !editable {
		detail := "ДДС пока только для просмотра: идёт перенос данных на сервер"
		if club.SheetLegacy() {
			detail = "ДДС сейчас ведётся в Google Таблице (резервный режим SHEET_MODE=legacy)"
		}
		c.JSON(http.StatusConflict, gin.H{"error": "sheet_is_master", "detail": detail})
		return
	}
	who := platformUser(c)
	res, err := h.repo.DDSApply(ctx, req.Ops, who, time.Now())
	if err != nil {
		var in *pg.ErrDDSInput
		if errors.As(err, &in) {
			c.JSON(http.StatusBadRequest, gin.H{"error": "bad_params", "detail": in.Msg})
			return
		}
		log.Printf("dds save: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal server error"})
		return
	}
	// History of changes («История изменений»): one line per batch
	var ins, upd, del int
	for _, op := range req.Ops {
		switch op.Op {
		case "insert":
			ins++
		case "update":
			upd++
		case "delete":
			del++
		}
	}
	p := map[string]string{"added": strconv.Itoa(ins), "changed": strconv.Itoa(upd), "deleted": strconv.Itoa(del)}
	if len(req.Ops) == 1 {
		b, _ := json.Marshal(req.Ops[0])
		p["op"] = string(b)
	}
	if err := h.repo.LogOp(ctx, pg.ClubOp{Source: "platform", Who: who, Action: "ddsEdit", Params: p, OK: true}); err != nil {
		log.Printf("dds log: %v", err)
	}
	if err := RefreshPlatformSeed(ctx, h.repo, h.platform, h.StaticSeed); err != nil {
		log.Printf("platform seed: %v", err)
	}
	c.JSON(http.StatusOK, gin.H{"ok": true, "ids": res.IDs, "rows": res.Rows, "deleted": res.Deleted})
}
