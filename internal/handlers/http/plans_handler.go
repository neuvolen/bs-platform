package http

import (
	"net/http"
	"strings"
	"time"

	"github.com/bnursik/business_surgery_backend/internal/domain/plans"
	"github.com/gin-gonic/gin"
)

// swagger:model PlanWithStepsResponse
type PlanWithStepsResponse struct {
	Plan  PlanResponse   `json:"plan"`
	Steps []StepResponse `json:"steps"`
}

type PlansHandler struct {
	svc plans.Service
}

func NewPlansHandler(svc plans.Service) *PlansHandler {
	return &PlansHandler{svc: svc}
}

// GetPlanByDisease godoc
// @Summary      Get plan by disease id (with steps)
// @Tags         plans
// @Produce      json
// @Param        id path string true "Disease ID (uuid)"
// @Success      200 {object} PlanWithStepsResponse
// @Failure      400 {object} map[string]string
// @Failure      404 {object} map[string]string
// @Failure      500 {object} map[string]string
// @Router       /api/v1/diseases/{id}/plan [get]
func (h *PlansHandler) GetByDisease(c *gin.Context) {
	diseaseID := strings.TrimSpace(c.Param("id"))
	if diseaseID == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_disease_id"})
		return
	}

	p, steps, err := h.svc.GetPlanWithStepsByDiseaseID(c.Request.Context(), diseaseID)
	if err != nil {
		writeError(c, err)
		return
	}

	outSteps := make([]StepResponse, 0, len(steps))
	for _, st := range steps {
		outSteps = append(outSteps, StepResponse{
			ID:          st.ID.String(),
			PlanID:      st.PlanID.String(),
			OrderNo:     st.OrderNo,
			Title:       st.Title,
			Description: st.Description,
			CreatedAt:   st.CreatedAt.UTC().Format(time.RFC3339),
			UpdatedAt:   st.UpdatedAt.UTC().Format(time.RFC3339),
		})
	}

	c.JSON(http.StatusOK, PlanWithStepsResponse{
		Plan: PlanResponse{
			ID:          p.ID.String(),
			DiseaseID:   p.DiseaseID.String(),
			Title:       p.Title,
			Description: p.Description,
			CreatedAt:   p.CreatedAt.UTC().Format(time.RFC3339),
			UpdatedAt:   p.UpdatedAt.UTC().Format(time.RFC3339),
		},
		Steps: outSteps,
	})
}

// GetPlan godoc
// @Summary      Get plan by id
// @Tags         plans
// @Produce      json
// @Param        planId path string true "Plan ID (uuid)"
// @Success      200 {object} PlanResponse
// @Failure      400 {object} map[string]string
// @Failure      404 {object} map[string]string
// @Failure      500 {object} map[string]string
// @Router       /api/v1/plans/{planId} [get]
func (h *PlansHandler) GetByID(c *gin.Context) {
	planID := strings.TrimSpace(c.Param("planId"))
	if planID == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_plan_id"})
		return
	}

	p, err := h.svc.GetPlanByID(c.Request.Context(), planID)
	if err != nil {
		writeError(c, err)
		return
	}

	c.JSON(http.StatusOK, PlanResponse{
		ID:          p.ID.String(),
		DiseaseID:   p.DiseaseID.String(),
		Title:       p.Title,
		Description: p.Description,
		CreatedAt:   p.CreatedAt.UTC().Format(time.RFC3339),
		UpdatedAt:   p.UpdatedAt.UTC().Format(time.RFC3339),
	})
}
