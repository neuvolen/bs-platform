package http

import (
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/bnursik/business_surgery_backend/internal/domain/tracking"
	"github.com/gin-gonic/gin"
)

type ModeratorDashboardHandler struct {
	svc tracking.Service
}

func NewModeratorDashboardHandler(svc tracking.Service) *ModeratorDashboardHandler {
	return &ModeratorDashboardHandler{svc: svc}
}

// Stats godoc
// @Summary      Moderator dashboard stats
// @Description  Возвращает агрегированную статистику для дашборда модератора: всего участников, средний прогресс и активные проблемы.
// @Tags         Moderator, Dashboard
// @Security     BearerAuth
// @Produce      json
// @Success      200  {object}  map[string]any  "OK"
// @Failure      401  {object}  map[string]any  "unauthorized"
// @Failure      403  {object}  map[string]any  "forbidden"
// @Failure      500  {object}  map[string]any  "internal_error"
// @Router       /api/v1/moderator/dashboard/stats [get]
func (h *ModeratorDashboardHandler) Stats(c *gin.Context) {
	st, err := h.svc.GetDashboardStats(c.Request.Context())
	if err != nil {
		writeError(c, err)
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"totalParticipants":  st.TotalParticipants,
		"avgProgressPercent": st.AvgProgressPercent,
		"activeProblems":     st.ActiveProblems,
	})
}

// Users godoc
// @Summary      Moderator dashboard users list
// @Description  Возвращает список участников для таблицы дашборда (поиск + пагинация) с прогрессом и последней активностью.
// @Tags         Moderator, Dashboard
// @Security     BearerAuth
// @Param        q       query     string  false  "Search (email/name/surname)"
// @Param        limit   query     int     false  "Limit (default 20, max 200)"
// @Param        offset  query     int     false  "Offset (default 0)"
// @Produce      json
// @Success      200  {object}  map[string]any  "OK"
// @Failure      401  {object}  map[string]any  "unauthorized"
// @Failure      403  {object}  map[string]any  "forbidden"
// @Failure      500  {object}  map[string]any  "internal_error"
// @Router       /api/v1/moderator/dashboard/users [get]
func (h *ModeratorDashboardHandler) Users(c *gin.Context) {
	q := strings.TrimSpace(c.Query("q"))

	limit := 20
	offset := 0

	if v := strings.TrimSpace(c.Query("limit")); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			limit = n
		}
	}
	if v := strings.TrimSpace(c.Query("offset")); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			offset = n
		}
	}

	page, err := h.svc.ListDashboardUsers(c.Request.Context(), q, limit, offset)
	if err != nil {
		writeError(c, err)
		return
	}

	items := make([]gin.H, 0, len(page.Items))
	for _, it := range page.Items {
		var last string
		if it.LastActivityAt != nil {
			last = it.LastActivityAt.UTC().Format(time.RFC3339)
		} else {
			last = ""
		}

		items = append(items, gin.H{
			"id":                 it.ID,
			"email":              it.Email,
			"name":               it.Name,
			"surname":            it.Surname,
			"lastActivityAt":     last,
			"activeDiseases":     it.ActiveDiseases,
			"completedSteps":     it.CompletedSteps,
			"totalSteps":         it.TotalSteps,
			"overallProgressPct": it.OverallProgressPct,
		})
	}

	c.JSON(http.StatusOK, gin.H{
		"total":  page.Total,
		"limit":  page.Limit,
		"offset": page.Offset,
		"items":  items,
	})
}

// Dashboard godoc
// @Summary      Moderator dashboard (stats + users)
// @Description  Один эндпойнт для страницы дашборда модератора: возвращает статистику карточек и список участников (поиск + пагинация).
// @Tags         Moderator, Dashboard
// @Security     BearerAuth
// @Param        q       query     string  false  "Search (email/name/surname)"
// @Param        limit   query     int     false  "Limit (default 20, max 200)"
// @Param        offset  query     int     false  "Offset (default 0)"
// @Produce      json
// @Success      200  {object}  map[string]any  "OK"
// @Failure      401  {object}  map[string]any  "unauthorized"
// @Failure      403  {object}  map[string]any  "forbidden"
// @Failure      500  {object}  map[string]any  "internal_error"
// @Router       /api/v1/moderator/dashboard [get]
func (h *ModeratorDashboardHandler) Dashboard(c *gin.Context) {
	q := strings.TrimSpace(c.Query("q"))

	limit := 20
	offset := 0

	if v := strings.TrimSpace(c.Query("limit")); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			limit = n
		}
	}
	if v := strings.TrimSpace(c.Query("offset")); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			offset = n
		}
	}

	st, err := h.svc.GetDashboardStats(c.Request.Context())
	if err != nil {
		writeError(c, err)
		return
	}

	page, err := h.svc.ListDashboardUsers(c.Request.Context(), q, limit, offset)
	if err != nil {
		writeError(c, err)
		return
	}

	items := make([]gin.H, 0, len(page.Items))
	for _, it := range page.Items {
		var last string
		if it.LastActivityAt != nil {
			last = it.LastActivityAt.UTC().Format(time.RFC3339)
		} else {
			last = ""
		}

		items = append(items, gin.H{
			"id":                 it.ID,
			"email":              it.Email,
			"name":               it.Name,
			"surname":            it.Surname,
			"lastActivityAt":     last,
			"activeDiseases":     it.ActiveDiseases,
			"completedSteps":     it.CompletedSteps,
			"totalSteps":         it.TotalSteps,
			"overallProgressPct": it.OverallProgressPct,
		})
	}

	c.JSON(http.StatusOK, gin.H{
		"stats": gin.H{
			"totalParticipants":  st.TotalParticipants,
			"avgProgressPercent": st.AvgProgressPercent,
			"activeProblems":     st.ActiveProblems,
		},
		"users": gin.H{
			"total":  page.Total,
			"limit":  page.Limit,
			"offset": page.Offset,
			"items":  items,
		},
	})
}
