package http

import (
	"net/http"
	"strings"

	"github.com/bnursik/business_surgery_backend/internal/domain/plans"
	"github.com/gin-gonic/gin"
)

// swagger:model StepUpsertRequest
type StepUpsertRequest struct {
	OrderNo     int    `json:"orderNo" binding:"required" example:"1"`
	Title       string `json:"title" binding:"required" example:"Cut non-essential expenses"`
	Description string `json:"description" example:"Pause subscriptions and renegotiate vendor costs"`
}

type StepsHandler struct {
	plansSvc plans.Service
}

func NewStepsHandler(plansSvc plans.Service) *StepsHandler {
	return &StepsHandler{plansSvc: plansSvc}
}

// AddStep godoc
// @Summary      Add step to plan
// @Tags         plans
// @Accept       json
// @Produce      json
// @Security     BearerAuth
// @Param        Authorization header string true "Bearer {accessToken}"
// @Param        planId path string true "Plan ID (uuid)"
// @Param        request body StepUpsertRequest true "Step payload"
// @Success      201 {object} StepResponse
// @Failure      400 {object} map[string]string
// @Failure      401 {object} map[string]string
// @Failure      403 {object} map[string]string
// @Failure      409 {object} map[string]string
// @Failure      500 {object} map[string]string
// @Router       /api/v1/plans/{planId}/steps [post]
func (h *StepsHandler) Add(c *gin.Context) {
	planID := strings.TrimSpace(c.Param("planId"))

	var req StepUpsertRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	st, err := h.plansSvc.AddStep(c.Request.Context(), planID, plans.Step{
		OrderNo:     req.OrderNo,
		Title:       req.Title,
		Description: req.Description,
	})
	if err != nil {
		writeError(c, err)
		return
	}

	c.JSON(http.StatusCreated, toStepResponse(st))
}

// UpdateStep godoc
// @Summary      Update step
// @Tags         plans
// @Accept       json
// @Produce      json
// @Security     BearerAuth
// @Param        Authorization header string true "Bearer {accessToken}"
// @Param        planId path string true "Plan ID (uuid)"
// @Param        stepId path string true "Step ID (uuid)"
// @Param        request body StepUpsertRequest true "Step payload"
// @Success      200 {object} StepResponse
// @Failure      400 {object} map[string]string
// @Failure      401 {object} map[string]string
// @Failure      403 {object} map[string]string
// @Failure      404 {object} map[string]string
// @Failure      409 {object} map[string]string
// @Failure      500 {object} map[string]string
// @Router       /api/v1/plans/{planId}/steps/{stepId} [put]
func (h *StepsHandler) Update(c *gin.Context) {
	// planId в URL нужен фронту, но в БД update идёт по stepId
	_ = strings.TrimSpace(c.Param("planId"))
	stepID := strings.TrimSpace(c.Param("stepId"))

	var req StepUpsertRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	st, err := h.plansSvc.UpdateStep(c.Request.Context(), stepID, plans.Step{
		OrderNo:     req.OrderNo,
		Title:       req.Title,
		Description: req.Description,
	})
	if err != nil {
		writeError(c, err)
		return
	}

	c.JSON(http.StatusOK, toStepResponse(st))
}

// DeleteStep godoc
// @Summary      Delete step
// @Tags         plans
// @Produce      json
// @Security     BearerAuth
// @Param        Authorization header string true "Bearer {accessToken}"
// @Param        planId path string true "Plan ID (uuid)"
// @Param        stepId path string true "Step ID (uuid)"
// @Success      204
// @Failure      401 {object} map[string]string
// @Failure      403 {object} map[string]string
// @Failure      404 {object} map[string]string
// @Failure      500 {object} map[string]string
// @Router       /api/v1/plans/{planId}/steps/{stepId} [delete]
func (h *StepsHandler) Delete(c *gin.Context) {
	_ = strings.TrimSpace(c.Param("planId"))
	stepID := strings.TrimSpace(c.Param("stepId"))

	if err := h.plansSvc.DeleteStep(c.Request.Context(), stepID); err != nil {
		writeError(c, err)
		return
	}
	c.Status(http.StatusNoContent)
}

// ListSteps godoc
// @Summary      List steps by plan id
// @Tags         steps
// @Produce      json
// @Param        planId path string true "Plan ID (uuid)"
// @Success      200 {array} StepResponse
// @Failure      400 {object} map[string]string
// @Failure      404 {object} map[string]string
// @Failure      500 {object} map[string]string
// @Router       /api/v1/plans/{planId}/steps [get]
func (h *StepsHandler) List(c *gin.Context) {
	planID := strings.TrimSpace(c.Param("planId"))
	if planID == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_plan_id"})
		return
	}

	steps, err := h.plansSvc.ListStepsByPlanID(c.Request.Context(), planID)
	if err != nil {
		writeError(c, err)
		return
	}

	out := make([]StepResponse, 0, len(steps))
	for _, st := range steps {
		out = append(out, toStepResponse(st))
	}
	c.JSON(http.StatusOK, out)
}
