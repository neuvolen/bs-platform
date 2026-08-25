package http

import (
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/bnursik/business_surgery_backend/internal/domain/tracking"
	"github.com/gin-gonic/gin"
)

type MeTrackingHandler struct {
	svc tracking.Service
}

func NewMeTrackingHandler(svc tracking.Service) *MeTrackingHandler {
	return &MeTrackingHandler{svc: svc}
}

// MeProgress godoc
// @Summary      My progress stats
// @Description  Возвращает агрегированный прогресс текущего пользователя: активные проблемы, выполненные шаги, общий прогресс, последняя активность.
// @Tags         users, tracking
// @Security     BearerAuth
// @Produce      json
// @Success      200  {object}  map[string]any
// @Failure      401  {object}  map[string]any
// @Failure      403  {object}  map[string]any
// @Failure      500  {object}  map[string]any
// @Router       /api/v1/me/progress [get]
func (h *MeTrackingHandler) MeProgress(c *gin.Context) {
	userIDAny, ok := c.Get("userID")
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
		return
	}
	userID, _ := userIDAny.(string)

	st, err := h.svc.GetUserProgress(c.Request.Context(), userID)
	if err != nil {
		writeError(c, err)
		return
	}

	var last string
	if st.LastActivityAt != nil {
		last = st.LastActivityAt.UTC().Format(time.RFC3339)
	} else {
		last = ""
	}

	c.JSON(http.StatusOK, gin.H{
		"userId":             st.UserID,
		"activeDiseases":     st.ActiveDiseases,
		"completedSteps":     st.CompletedSteps,
		"totalSteps":         st.TotalSteps,
		"overallProgressPct": st.OverallProgressPct,
		"lastActivityAt":     last,
	})
}

// MeDiseases godoc
// @Summary      My diseases list with progress
// @Description  Возвращает список проблем текущего пользователя с прогрессом по шагам. Поддерживает status + пагинацию.
// @Tags         users, tracking
// @Security     BearerAuth
// @Param        status  query     string  false  "Status (active|resolved|...) default active"
// @Param        limit   query     int     false  "Limit (default 20, max 200)"
// @Param        offset  query     int     false  "Offset (default 0)"
// @Produce      json
// @Success      200  {object}  map[string]any
// @Failure      401  {object}  map[string]any
// @Failure      403  {object}  map[string]any
// @Failure      500  {object}  map[string]any
// @Router       /api/v1/me/diseases [get]
func (h *MeTrackingHandler) MeDiseases(c *gin.Context) {
	userIDAny, ok := c.Get("userID")
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
		return
	}
	userID, _ := userIDAny.(string)

	status := strings.TrimSpace(strings.ToLower(c.Query("status")))
	if status == "" {
		status = "active"
	}

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

	page, err := h.svc.ListUserDiseases(c.Request.Context(), userID, status, limit, offset)
	if err != nil {
		writeError(c, err)
		return
	}

	items := make([]gin.H, 0, len(page.Items))
	for _, it := range page.Items {
		items = append(items, gin.H{
			"userDiseaseId":   it.UserDiseaseID,
			"diseaseId":       it.DiseaseID,
			"diseaseName":     it.DiseaseName,
			"organName":       it.OrganName,
			"categoryName":    it.CategoryName,
			"status":          it.Status,
			"startedAt":       it.StartedAt.UTC().Format(time.RFC3339),
			"updatedAt":       it.UpdatedAt.UTC().Format(time.RFC3339),
			"completedSteps":  it.CompletedSteps,
			"totalSteps":      it.TotalSteps,
			"progressPercent": it.ProgressPercent,
		})
	}

	c.JSON(http.StatusOK, gin.H{
		"total":  page.Total,
		"limit":  page.Limit,
		"offset": page.Offset,
		"items":  items,
	})
}

// MeActivity godoc
// @Summary      My activity logs
// @Description  Возвращает активность текущего пользователя (лента). Поддерживает пагинацию.
// @Tags         users, tracking
// @Security     BearerAuth
// @Param        limit   query     int     false  "Limit (default 20, max 200)"
// @Param        offset  query     int     false  "Offset (default 0)"
// @Produce      json
// @Success      200  {object}  map[string]any
// @Failure      401  {object}  map[string]any
// @Failure      403  {object}  map[string]any
// @Failure      500  {object}  map[string]any
// @Router       /api/v1/me/activity [get]
func (h *MeTrackingHandler) MeActivity(c *gin.Context) {
	userIDAny, ok := c.Get("userID")
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
		return
	}
	userID, _ := userIDAny.(string)

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

	page, err := h.svc.ListUserActivity(c.Request.Context(), userID, limit, offset)
	if err != nil {
		writeError(c, err)
		return
	}

	items := make([]gin.H, 0, len(page.Items))
	for _, it := range page.Items {
		items = append(items, gin.H{
			"id":        it.ID,
			"type":      it.Type,
			"payload":   it.Payload,
			"createdAt": it.CreatedAt.UTC().Format(time.RFC3339),
		})
	}

	c.JSON(http.StatusOK, gin.H{
		"total":  page.Total,
		"limit":  page.Limit,
		"offset": page.Offset,
		"items":  items,
	})
}
