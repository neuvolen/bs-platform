package http

import (
	"context"
	"errors"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/bnursik/business_surgery_backend/internal/club"
	pg "github.com/bnursik/business_surgery_backend/internal/repository/pg"
	"github.com/gin-gonic/gin"
)

// R70: «Установить долг» in «Учёт → Долги и штрафы», and at start: the
// owner's adjustment for Альтаир (once), the debt changes since the last
// start with their reasons, the audit of every resident's debt.

type debtSetReq struct {
	ID     int64  `json:"id"`
	Debt   any    `json:"debt"`
	Reason string `json:"reason"`
}

// DebtSet godoc
// @Summary  «Установить долг»: the resident's membership debt from now on, with the reason
// @Description  {id, debt, reason}: the debt (rest of the entry fee first, then the renewal) is set and kept as an adjustment; payments made before it never bring the old debt back. Fines are not touched.
// @Tags     club
// @Security BearerAuth
// @Router   /api/v1/club/debts/set [post]
func (h *ClubHandler) DebtSet(c *gin.Context) {
	var req debtSetReq
	if err := c.ShouldBindJSON(&req); err != nil || req.ID <= 0 || strings.TrimSpace(req.Reason) == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "bad_params", "detail": "нужны резидент, сумма и причина"})
		return
	}
	var debt int64
	switch v := req.Debt.(type) {
	case float64:
		debt = int64(v)
	case string:
		n, ok := club.Money(v)
		if !ok && strings.TrimSpace(v) != "" {
			c.JSON(http.StatusBadRequest, gin.H{"error": "bad_params", "detail": "сумма: только цифры"})
			return
		}
		debt = n
	default:
		c.JSON(http.StatusBadRequest, gin.H{"error": "bad_params", "detail": "нужна сумма долга"})
		return
	}
	if debt < 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "bad_params", "detail": "долг не может быть меньше нуля"})
		return
	}
	ctx := c.Request.Context()
	if _, editable := ddsMaster(ctx, h.repo); !editable {
		c.JSON(http.StatusConflict, gin.H{"error": "sheet_is_master", "detail": "долги пока только для просмотра"})
		return
	}
	reason := strings.TrimSpace(req.Reason)
	if len([]rune(reason)) > 300 {
		reason = string([]rune(reason)[:300])
	}
	who := platformUser(c)
	by := ""
	if who != "" {
		by = "платформа, " + who
	}
	res, err := h.repo.SetDebt(ctx, req.ID, "", debt, reason, by, "")
	if err != nil {
		var in *pg.ErrLinkInput
		if errors.As(err, &in) {
			c.JSON(http.StatusBadRequest, gin.H{"error": "bad_params", "detail": in.Msg})
			return
		}
		log.Printf("debt set: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal server error"})
		return
	}
	p := map[string]string{"id": strconv.FormatInt(req.ID, 10), "resident": res.Resident, "debt": strconv.FormatInt(debt, 10), "reason": reason}
	if err := h.repo.LogOp(ctx, pg.ClubOp{Source: "platform", Who: who, Action: "setDebt", Params: p, OK: true}); err != nil {
		log.Printf("debt set log: %v", err)
	}
	if err := RefreshPlatformSeed(ctx, h.repo, h.platform, h.StaticSeed); err != nil {
		log.Printf("platform seed: %v", err)
	}
	c.JSON(http.StatusOK, gin.H{"ok": true, "result": res})
}

type r70Repo interface {
	Master(ctx context.Context) (string, error)
	SetDebt(ctx context.Context, id int64, name string, debt int64, reason, by, key string) (*pg.DebtSet, error)
	DebtState(ctx context.Context, name string) (*pg.DebtState, error)
	DebtAudit(ctx context.Context) ([]pg.DebtFlag, error)
	BalanceLogSince(ctx context.Context, id int64, limit int) ([]pg.BalanceEvent, error)
	Meta(ctx context.Context, key string) string
	MetaSet(ctx context.Context, key, value string) error
}

// R70Adjust: the owner's decisions on debts, applied once each (by key).
type R70Adjust struct {
	Key, Resident string
	Debt          int64
	Reason        string
	Fine          int64 // the open fines the owner expects (checked, not changed)
}

var R70Adjusts = []R70Adjust{
	{Key: "r70-altair-2026-10-10", Resident: "Альтаир", Debt: 0, Fine: 100000,
		Reason: "владелец 10.10.2026: «Он должен 100к штрафа, и всё». Продление оплачено 07.10 (2 × 50 000), встреча 09.10 на закрытом пакете 3/3 начислила тариф второй раз"},
}

// ClubR70AtStart runs after R69's start (the meetings recount).
func ClubR70AtStart(ctx context.Context, repo r70Repo, refresh func()) {
	if m, err := repo.Master(ctx); err != nil || m != "server" {
		return
	}
	changed := false
	for _, x := range R70Adjusts {
		res, err := repo.SetDebt(ctx, 0, x.Resident, x.Debt, x.Reason, "r70", x.Key)
		if err != nil {
			log.Printf("r70 adjust %s: %v", x.Resident, err)
			continue
		}
		if !res.Already {
			changed = true
			log.Printf("r70 adjust: %s: долг %s → %s ₸ (%s)", res.Resident, club.FmtMoney(res.RestBefore+res.RenewBefore),
				club.FmtMoney(res.RestAfter+res.RenewAfter), x.Reason)
		}
	}
	// every change of a debt since the last start, with its reason
	last, _ := strconv.ParseInt(repo.Meta(ctx, "r70_balance_logged"), 10, 64)
	for {
		ev, err := repo.BalanceLogSince(ctx, last, 500)
		if err != nil {
			log.Printf("r70 balance log: %v", err)
			break
		}
		for _, e := range ev {
			at, _ := time.Parse(time.RFC3339, e.At)
			log.Printf("r70 balance: %s: долг %s → %s ₸, %s: %s", e.Resident, club.FmtMoney(e.Before), club.FmtMoney(e.After),
				at.In(club.Almaty).Format("02.01 15:04"), e.Reason)
			last = e.ID
		}
		if len(ev) < 500 {
			break
		}
	}
	if err := repo.MetaSet(ctx, "r70_balance_logged", strconv.FormatInt(last, 10)); err != nil {
		log.Printf("r70 balance log mark: %v", err)
	}
	// the audit: debts that do not fit the payments
	if flags, err := repo.DebtAudit(ctx); err != nil {
		log.Printf("r70 audit: %v", err)
	} else {
		for _, f := range flags {
			log.Printf("r70 audit: проверить %s (долг %s ₸): %s", f.Resident, club.FmtMoney(f.Debt), strings.Join(f.Why, "; "))
		}
		log.Printf("r70 audit: %d residents to check", len(flags))
	}
	for _, x := range R70Adjusts {
		st, err := repo.DebtState(ctx, x.Resident)
		if err != nil {
			log.Printf("r70 %s: %v", x.Resident, err)
			continue
		}
		mark := ""
		if st.Debt != x.Debt || st.FinesOpen != x.Fine {
			mark = " (ожидалось: долг " + club.FmtMoney(x.Debt) + " ₸, штраф " + club.FmtMoney(x.Fine) + " ₸)"
		}
		log.Printf("r70 %s: долг %s ₸, штраф к оплате %s ₸%s", st.Resident, club.FmtMoney(st.Debt), club.FmtMoney(st.FinesOpen), mark)
	}
	if changed && refresh != nil {
		refresh()
	}
}
