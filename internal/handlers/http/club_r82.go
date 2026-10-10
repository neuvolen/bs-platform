package http

import (
	"errors"
	"log"
	"net/http"
	"strconv"
	"strings"

	"github.com/bnursik/business_surgery_backend/internal/club"
	pg "github.com/bnursik/business_surgery_backend/internal/repository/pg"
	"github.com/gin-gonic/gin"
)

// Former godoc
// @Summary  «Клуб → Резиденты → Бывшие»: former residents with their package, debt, fines, payments and meetings
// @Tags     club
// @Security BearerAuth
// @Router   /api/v1/club/former [get]
func (h *ClubHandler) Former(c *gin.Context) {
	ctx := c.Request.Context()
	list, err := h.repo.FormerResidents(ctx)
	if err != nil {
		log.Printf("former: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal server error"})
		return
	}
	_, editable := ddsMaster(ctx, h.repo)
	c.JSON(http.StatusOK, gin.H{"ok": true, "former": list, "editable": editable})
}

type returnReq struct {
	ID     int64  `json:"id"`
	Format string `json:"format"`
	Tariff any    `json:"tariff"`
}

// ReturnFormer godoc
// @Summary  «Вернуть в резиденты»: a former resident is a resident again
// @Description  {id, format?, tariff?}: the history stays (payments, fines, meetings, boards, entry date, debt); a new package starts today (months of the tariff × 3 meetings, «проведено» 0).
// @Tags     club
// @Security BearerAuth
// @Router   /api/v1/club/former/return [post]
func (h *ClubHandler) ReturnFormer(c *gin.Context) {
	var req returnReq
	if err := c.ShouldBindJSON(&req); err != nil || req.ID <= 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "bad_params", "detail": "нужен резидент"})
		return
	}
	var tariff int64
	switch v := req.Tariff.(type) {
	case float64:
		tariff = int64(v)
	case string:
		if strings.TrimSpace(v) != "" {
			n, ok := club.Money(v)
			if !ok {
				c.JSON(http.StatusBadRequest, gin.H{"error": "bad_params", "detail": "тариф: только цифры"})
				return
			}
			tariff = n
		}
	}
	ctx := c.Request.Context()
	if _, editable := ddsMaster(ctx, h.repo); !editable {
		c.JSON(http.StatusConflict, gin.H{"error": "sheet_is_master", "detail": "пока данные клуба ведёт таблица: уберите отметку «Бывший» в таблице"})
		return
	}
	who := platformUser(c)
	res, err := h.repo.ReturnResident(ctx, req.ID, req.Format, tariff, who)
	if err != nil {
		var in *pg.ErrLinkInput
		if errors.As(err, &in) {
			c.JSON(http.StatusBadRequest, gin.H{"error": "bad_params", "detail": in.Msg})
			return
		}
		log.Printf("former return: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal server error"})
		return
	}
	p := map[string]string{"id": strconv.FormatInt(res.ID, 10), "name": res.Name, "format": res.Format,
		"tariff": strconv.FormatInt(res.Tariff, 10), "granted": strconv.FormatInt(res.Granted, 10), "from": res.From}
	if err := h.repo.LogOp(ctx, pg.ClubOp{Source: "platform", Who: who, Action: "returnResident", Params: p, OK: true}); err != nil {
		log.Printf("former return log: %v", err)
	}
	if err := RefreshPlatformSeed(ctx, h.repo, h.platform, h.StaticSeed); err != nil {
		log.Printf("platform seed: %v", err)
	}
	c.JSON(http.StatusOK, gin.H{"ok": true, "result": res})
}
