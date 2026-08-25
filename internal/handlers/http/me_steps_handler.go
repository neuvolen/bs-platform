package http

import (
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/bnursik/business_surgery_backend/internal/domain/tracking"
	"github.com/gin-gonic/gin"
)

// swagger:model UserStepItem
type UserStepItem struct {
	ID            string     `json:"id" example:"11111111-2222-3333-4444-555555555555"`
	UserDiseaseID string     `json:"userDiseaseId" example:"c3e1c9a2-4f3a-4f2d-8b1a-1a2b3c4d5e6a"`
	StepID        string     `json:"stepId" example:"99999999-8888-7777-6666-555555555555"`
	State         string     `json:"state" example:"pending"`
	CompletedAt   *time.Time `json:"completedAt" example:"2026-01-09T10:12:00Z"`
	CreatedAt     time.Time  `json:"createdAt" example:"2026-01-09T10:00:00Z"`
	UpdatedAt     time.Time  `json:"updatedAt" example:"2026-01-09T10:10:00Z"`
}

// swagger:model UserStepsPage
type UserStepsPage struct {
	Total  int            `json:"total" example:"12"`
	Limit  int            `json:"limit" example:"20"`
	Offset int            `json:"offset" example:"0"`
	Items  []UserStepItem `json:"items"`
}

type MeStepsHandler struct {
	svc tracking.Service
}

func NewMeStepsHandler(svc tracking.Service) *MeStepsHandler {
	return &MeStepsHandler{svc: svc}
}

type updateStepStateRequest struct {
	// 0 = pending, 1 = active, 2 = completed
	State int `json:"state"`
}

// ListMyDiseaseSteps godoc
//
// @Summary      List steps for a user disease
// @Description  Returns user_steps for given userDiseaseId (ordered by created_at asc)
// @Tags         me-steps
// @Produce      json
// @Security     BearerAuth
// @Param        userDiseaseId  path      string  true   "User disease ID (uuid)"
// @Param        limit          query     int     false  "Page size"  minimum(1)  maximum(200)  default(20)
// @Param        offset         query     int     false  "Offset"     minimum(0)  default(0)
// @Success      200            {object}  UserStepsPage
// @Failure      401  {object}  map[string]any  "unauthorized"
// @Failure      403  {object}  map[string]any  "forbidden"
// @Failure      500  {object}  map[string]any  "internal_error"
// @Router       /api/v1/me/diseases/{userDiseaseId}/steps [get]
func (h *MeStepsHandler) ListMyDiseaseSteps(c *gin.Context) {
	userDiseaseID := strings.TrimSpace(c.Param("userDiseaseId"))
	if userDiseaseID == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "userDiseaseId required"})
		return
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

	page, err := h.svc.ListUserDiseaseSteps(c.Request.Context(), userDiseaseID, limit, offset)
	if err != nil {
		writeError(c, err)
		return
	}

	c.JSON(http.StatusOK, page)
}

// CompleteMyStep godoc
//
// @Summary      Complete a step
// @Description  Marks user_step as completed (sets completed_at) and writes activity log (step_completed). If all steps completed, auto-resolves the disease (status_change).
// @Tags         me-steps
// @Security     BearerAuth
// @Param        userStepId  path  string  true  "User step ID (uuid)"
// @Success      204
// @Failure      401  {object}  map[string]any  "unauthorized"
// @Failure      403  {object}  map[string]any  "forbidden"
// @Failure      500  {object}  map[string]any  "internal_error"
// @Router       /api/v1/me/steps/{userStepId}/complete [post]
func (h *MeStepsHandler) CompleteMyStep(c *gin.Context) {
	userIDAny, ok := c.Get("userID")
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
		return
	}
	userID, _ := userIDAny.(string)

	userStepID := strings.TrimSpace(c.Param("userStepId"))
	if userStepID == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "userStepId required"})
		return
	}

	if err := h.svc.CompleteUserStep(c.Request.Context(), userID, userStepID); err != nil {
		writeError(c, err)
		return
	}

	c.Status(http.StatusNoContent)
}

// UpdateMyStepState godoc
//
// @Summary      Update step state
// @Description  Updates user_step state. Body: {state: 0|1|2} where 0=pending, 1=active, 2=completed. For completed it uses /complete logic (activity + auto-resolve).
// @Tags         me-steps
// @Security     BearerAuth
// @Accept       json
// @Param        userStepId  path  string                 true  "User step ID (uuid)"
// @Param        body        body  updateStepStateRequest  true  "State"
// @Success      204
// @Failure      400  {object}  map[string]any  "bad_request"
// @Failure      401  {object}  map[string]any  "unauthorized"
// @Failure      403  {object}  map[string]any  "forbidden"
// @Failure      404  {object}  map[string]any  "not_found"
// @Failure      500  {object}  map[string]any  "internal_error"
// @Router       /api/v1/me/steps/{userStepId}/state [post]
func (h *MeStepsHandler) UpdateMyStepState(c *gin.Context) {
	userIDAny, ok := c.Get("userID")
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
		return
	}
	userID, _ := userIDAny.(string)

	userStepID := strings.TrimSpace(c.Param("userStepId"))
	if userStepID == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "userStepId required"})
		return
	}

	var req updateStepStateRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid json"})
		return
	}

	var state string
	switch req.State {
	case 0:
		state = "pending"
	case 1:
		state = "active"
	case 2:
		// keep behaviour consistent with existing complete endpoint
		if err := h.svc.CompleteUserStep(c.Request.Context(), userID, userStepID); err != nil {
			writeError(c, err)
			return
		}
		c.Status(http.StatusNoContent)
		return
	default:
		c.JSON(http.StatusBadRequest, gin.H{"error": "state must be 0,1,2"})
		return
	}

	if err := h.svc.UpdateUserStepState(c.Request.Context(), userID, userStepID, state); err != nil {
		writeError(c, err)
		return
	}

	c.Status(http.StatusNoContent)
}

// ResolveMyDisease godoc
//
// @Summary      Resolve a user disease
// @Description  Marks user_disease as resolved (sets resolved_at) and writes activity log (status_change).
// @Tags         me-steps
// @Security     BearerAuth
// @Param        userDiseaseId  path  string  true  "User disease ID (uuid)"
// @Success      204
// @Failure      401  {object}  map[string]any  "unauthorized"
// @Failure      403  {object}  map[string]any  "forbidden"
// @Failure      500  {object}  map[string]any  "internal_error"
// @Router       /api/v1/me/diseases/{userDiseaseId}/resolve [post]
func (h *MeStepsHandler) ResolveMyDisease(c *gin.Context) {
	userIDAny, ok := c.Get("userID")
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
		return
	}
	userID, _ := userIDAny.(string)

	userDiseaseID := strings.TrimSpace(c.Param("userDiseaseId"))
	if userDiseaseID == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "userDiseaseId required"})
		return
	}

	// actorID = сам пользователь
	if err := h.svc.ResolveUserDisease(c.Request.Context(), userID, userDiseaseID, userID); err != nil {
		writeError(c, err)
		return
	}

	c.Status(http.StatusNoContent)
}
