package http

import (
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/bnursik/business_surgery_backend/internal/domain/diseases"
	"github.com/bnursik/business_surgery_backend/internal/domain/plans"
	"github.com/bnursik/business_surgery_backend/internal/domain/users"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

// swagger:model DiseaseCategoryResponse
type DiseaseCategoryResponse struct {
	ID    string `json:"id"`
	Code  string `json:"code"`
	Title string `json:"title"`
}

// swagger:model DiseaseResponse
type DiseaseResponse struct {
	ID          string                  `json:"id"`
	OrganID     string                  `json:"organId"`
	Category    DiseaseCategoryResponse `json:"category"`
	Title       string                  `json:"title"`
	Description string                  `json:"description"`
	CreatedAt   string                  `json:"createdAt"`
	UpdatedAt   string                  `json:"updatedAt"`
}
type DiseasesHandler struct {
	svc      diseases.Service
	plansSvc plans.Service
}

// swagger:model PlanResponse
type PlanResponse struct {
	ID          string `json:"id"`
	DiseaseID   string `json:"diseaseId"`
	Title       string `json:"title"`
	Description string `json:"description"`
	CreatedAt   string `json:"createdAt"`
	UpdatedAt   string `json:"updatedAt"`
}

// swagger:model StepResponse
type StepResponse struct {
	ID          string `json:"id"`
	PlanID      string `json:"planId"`
	OrderNo     int    `json:"orderNo"`
	Title       string `json:"title"`
	Description string `json:"description"`
	CreatedAt   string `json:"createdAt"`
	UpdatedAt   string `json:"updatedAt"`
}

// swagger:model DiseaseDetailsResponse
type DiseaseDetailsResponse struct {
	Disease DiseaseResponse `json:"disease"`
	Plan    *PlanResponse   `json:"plan"`  // null если нет плана
	Steps   []StepResponse  `json:"steps"` // [] если нет шагов/плана
}

// swagger:model DiseaseDetailsWithAssignedResponse
type DiseaseDetailsWithAssignedResponse struct {
	Disease    DiseaseResponse `json:"disease"`
	Plan       *PlanResponse   `json:"plan"`
	Steps      []StepResponse  `json:"steps"`
	IsAssigned bool            `json:"isAssigned"`
}

func NewDiseasesHandler(svc diseases.Service, plansSvc plans.Service) *DiseasesHandler {
	return &DiseasesHandler{svc: svc, plansSvc: plansSvc}
}

func toDiseaseResponse(d diseases.Disease) DiseaseResponse {
	return DiseaseResponse{
		ID:      d.ID.String(),
		OrganID: d.OrganID.String(),
		Category: DiseaseCategoryResponse{
			ID:    d.Category.ID.String(),
			Code:  d.Category.Code,
			Title: d.Category.Title,
		},
		Title:       d.Title,
		Description: d.Description,
		CreatedAt:   d.CreatedAt.UTC().Format(time.RFC3339),
		UpdatedAt:   d.UpdatedAt.UTC().Format(time.RFC3339),
	}
}

func toPlanResponse(p plans.Plan) PlanResponse {
	return PlanResponse{
		ID:          p.ID.String(),
		DiseaseID:   p.DiseaseID.String(),
		Title:       p.Title,
		Description: p.Description,
		CreatedAt:   p.CreatedAt.UTC().Format(time.RFC3339),
		UpdatedAt:   p.UpdatedAt.UTC().Format(time.RFC3339),
	}
}

func toStepResponse(s plans.Step) StepResponse {
	return StepResponse{
		ID:          s.ID.String(),
		PlanID:      s.PlanID.String(),
		OrderNo:     s.OrderNo,
		Title:       s.Title,
		Description: s.Description,
		CreatedAt:   s.CreatedAt.UTC().Format(time.RFC3339),
		UpdatedAt:   s.UpdatedAt.UTC().Format(time.RFC3339),
	}
}

// ListDiseases godoc
// @Summary      List diseases with plan and steps
// @Tags         diseases
// @Produce      json
// @Param        organId    query string false "Filter by organ id (uuid)"
// @Param        categoryId query string false "Filter by category id (uuid)"
// @Param        userId     query string false "If provided, response includes isAssigned for this user (uuid)"
// @Success      200 {array} DiseaseDetailsResponse
// @Failure      500 {object} map[string]string
// @Router       /api/v1/diseases/ [get]
func (h *DiseasesHandler) ListDetails(c *gin.Context) {
	organID := strings.TrimSpace(c.Query("organId"))
	categoryID := strings.TrimSpace(c.Query("categoryId"))
	userID := strings.TrimSpace(c.Query("userId"))

	items, err := h.svc.List(c.Request.Context(), organID, categoryID)
	if err != nil {
		writeError(c, err)
		return
	}

	// если userId НЕ передали → старое поведение (не ломаем фронт)
	if userID == "" {
		out := make([]DiseaseDetailsResponse, 0, len(items))

		for _, d := range items {
			var planPtr *PlanResponse
			stepsOut := make([]StepResponse, 0)

			p, steps, pErr := h.plansSvc.GetPlanWithStepsByDiseaseID(c.Request.Context(), d.ID.String())
			if pErr == nil {
				tmp := toPlanResponse(p)
				planPtr = &tmp
				for _, st := range steps {
					stepsOut = append(stepsOut, toStepResponse(st))
				}
			} else if !errors.Is(pErr, users.ErrNotFound) {
				writeError(c, pErr)
				return
			}

			out = append(out, DiseaseDetailsResponse{
				Disease: toDiseaseResponse(d),
				Plan:    planPtr,
				Steps:   stepsOut,
			})
		}

		c.JSON(http.StatusOK, out)
		return
	}

	// если userId передали → добавляем isAssigned
	assigned, err := h.svc.ListAssignedDiseaseIDs(c.Request.Context(), userID)
	if err != nil {
		writeError(c, err)
		return
	}

	out := make([]DiseaseDetailsWithAssignedResponse, 0, len(items))

	for _, d := range items {
		var planPtr *PlanResponse
		stepsOut := make([]StepResponse, 0)

		p, steps, pErr := h.plansSvc.GetPlanWithStepsByDiseaseID(c.Request.Context(), d.ID.String())
		if pErr == nil {
			tmp := toPlanResponse(p)
			planPtr = &tmp
			for _, st := range steps {
				stepsOut = append(stepsOut, toStepResponse(st))
			}
		} else if !errors.Is(pErr, users.ErrNotFound) {
			writeError(c, pErr)
			return
		}

		_, ok := assigned[d.ID.String()]

		out = append(out, DiseaseDetailsWithAssignedResponse{
			Disease:    toDiseaseResponse(d),
			Plan:       planPtr,
			Steps:      stepsOut,
			IsAssigned: ok,
		})
	}

	c.JSON(http.StatusOK, out)
}

// GetDiseaseDetails godoc
// @Summary      Get disease details (includes plan and steps)
// @Tags         diseases
// @Produce      json
// @Param        id path string true "Disease ID (uuid)"
// @Success      200 {object} DiseaseDetailsResponse
// @Failure      400 {object} map[string]string
// @Failure      404 {object} map[string]string
// @Failure      500 {object} map[string]string
// @Router       /api/v1/diseases/{id} [get]
func (h *DiseasesHandler) GetDetails(c *gin.Context) {
	id := strings.TrimSpace(c.Param("id"))

	d, err := h.svc.Get(c.Request.Context(), id)
	if err != nil {
		writeError(c, err)
		return
	}

	var planPtr *PlanResponse
	stepsOut := make([]StepResponse, 0)

	p, steps, pErr := h.plansSvc.GetPlanWithStepsByDiseaseID(c.Request.Context(), id)
	if pErr == nil {
		tmp := toPlanResponse(p)
		planPtr = &tmp
		for _, st := range steps {
			stepsOut = append(stepsOut, toStepResponse(st))
		}
	} else if !errors.Is(pErr, users.ErrNotFound) {
		writeError(c, pErr)
		return
	}

	c.JSON(http.StatusOK, DiseaseDetailsResponse{
		Disease: toDiseaseResponse(d),
		Plan:    planPtr,
		Steps:   stepsOut,
	})
}

// swagger:model DiseaseUpsertRequest
type DiseaseUpsertRequest struct {
	OrganID     string `json:"organId" binding:"required" example:"b3e1c9a2-4f3a-4f2d-8b1a-1a2b3c4d5e6f"`
	CategoryID  string `json:"categoryId" binding:"required" example:"b3e1c9a2-4f3a-4f2d-8b1a-1a2b3c4d5e6f"`
	Title       string `json:"title" binding:"required" example:"Negative cash flow"`
	Description string `json:"description" example:"Expenses exceed income for last 3 months"`
}

// CreateDisease godoc
// @Summary      Create disease
// @Tags         diseases
// @Accept       json
// @Produce      json
// @Security     BearerAuth
// @Param        Authorization header string true "Bearer {accessToken}"
// @Param        request body DiseaseUpsertRequest true "Disease payload"
// @Success      201 {object} DiseaseResponse
// @Failure      400 {object} map[string]string
// @Failure      401 {object} map[string]string
// @Failure      403 {object} map[string]string
// @Failure      500 {object} map[string]string
// @Router       /api/v1/diseases [post]
func (h *DiseasesHandler) Create(c *gin.Context) {
	var req DiseaseUpsertRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	oid, err := uuid.Parse(strings.TrimSpace(req.OrganID))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid organId"})
		return
	}

	cid, err := uuid.Parse(strings.TrimSpace(req.CategoryID))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid categoryId"})
		return
	}

	created, err := h.svc.Create(c.Request.Context(), diseases.Disease{
		OrganID:     oid,
		CategoryID:  cid,
		Title:       req.Title,
		Description: req.Description,
	})
	if err != nil {
		writeError(c, err)
		return
	}

	c.JSON(http.StatusCreated, toDiseaseResponse(created))
}

// UpdateDisease godoc
// @Summary      Update disease
// @Tags         diseases
// @Accept       json
// @Produce      json
// @Security     BearerAuth
// @Param        Authorization header string true "Bearer {accessToken}"
// @Param        id path string true "Disease ID (uuid)"
// @Param        request body DiseaseUpsertRequest true "Disease payload"
// @Success      200 {object} DiseaseResponse
// @Failure      400 {object} map[string]string
// @Failure      401 {object} map[string]string
// @Failure      403 {object} map[string]string
// @Failure      404 {object} map[string]string
// @Failure      500 {object} map[string]string
// @Router       /api/v1/diseases/{id} [put]
func (h *DiseasesHandler) Update(c *gin.Context) {
	id := strings.TrimSpace(c.Param("id"))

	var req DiseaseUpsertRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	oid, err := uuid.Parse(strings.TrimSpace(req.OrganID))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid organId"})
		return
	}

	cid, err := uuid.Parse(strings.TrimSpace(req.CategoryID))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid categoryId"})
		return
	}

	updated, err := h.svc.Update(c.Request.Context(), id, diseases.Disease{
		OrganID:     oid,
		CategoryID:  cid,
		Title:       req.Title,
		Description: req.Description,
	})
	if err != nil {
		writeError(c, err)
		return
	}

	c.JSON(http.StatusOK, toDiseaseResponse(updated))
}

// DeleteDisease godoc
// @Summary      Delete disease
// @Tags         diseases
// @Produce      json
// @Security     BearerAuth
// @Param        Authorization header string true "Bearer {accessToken}"
// @Param        id path string true "Disease ID (uuid)"
// @Success      204
// @Failure      401 {object} map[string]string
// @Failure      403 {object} map[string]string
// @Failure      404 {object} map[string]string
// @Failure      500 {object} map[string]string
// @Router       /api/v1/diseases/{id} [delete]
func (h *DiseasesHandler) Delete(c *gin.Context) {
	id := strings.TrimSpace(c.Param("id"))
	if err := h.svc.Delete(c.Request.Context(), id); err != nil {
		writeError(c, err)
		return
	}
	c.Status(http.StatusNoContent)
}

// swagger:model PlanUpsertRequest
type PlanUpsertRequest struct {
	Title       string `json:"title" binding:"required" example:"Cashflow recovery plan"`
	Description string `json:"description" example:"7-day actions to stabilize cashflow"`
}

// UpsertPlanForDisease godoc
// @Summary      Create/update plan for disease
// @Tags         plans
// @Accept       json
// @Produce      json
// @Security     BearerAuth
// @Param        Authorization header string true "Bearer {accessToken}"
// @Param        id path string true "Disease ID (uuid)"
// @Param        request body PlanUpsertRequest true "Plan payload"
// @Success      200 {object} PlanResponse
// @Failure      400 {object} map[string]string
// @Failure      401 {object} map[string]string
// @Failure      403 {object} map[string]string
// @Failure      404 {object} map[string]string
// @Failure      500 {object} map[string]string
// @Router       /api/v1/diseases/{id}/plan [put]
func (h *DiseasesHandler) UpsertPlanForDisease(c *gin.Context) {
	diseaseID := strings.TrimSpace(c.Param("id"))

	var req PlanUpsertRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	p, err := h.plansSvc.UpsertForDisease(c.Request.Context(), diseaseID, plans.Plan{
		Title:       req.Title,
		Description: req.Description,
	})
	if err != nil {
		writeError(c, err)
		return
	}

	c.JSON(http.StatusOK, toPlanResponse(p))
}
