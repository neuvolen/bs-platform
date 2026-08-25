package http

import (
	"net/http"
	"strings"
	"time"

	"github.com/bnursik/business_surgery_backend/internal/domain/organs"
	"github.com/gin-gonic/gin"
)

// swagger:model OrganResponse
type OrganResponse struct {
	ID          string `json:"id"`
	Slug        string `json:"slug"`
	Title       string `json:"title"`
	Description string `json:"description"`
	CreatedAt   string `json:"createdAt"`
	UpdatedAt   string `json:"updatedAt"`
}

type OrgansHandler struct {
	svc organs.Service
}

func NewOrgansHandler(svc organs.Service) *OrgansHandler {
	return &OrgansHandler{svc: svc}
}

func toOrganResponse(o organs.Organ) OrganResponse {
	return OrganResponse{
		ID:          o.ID.String(),
		Slug:        o.Slug,
		Title:       o.Title,
		Description: o.Description,
		CreatedAt:   o.CreatedAt.UTC().Format(time.RFC3339),
		UpdatedAt:   o.UpdatedAt.UTC().Format(time.RFC3339),
	}
}

// ListOrgans godoc
// @Summary      List organs
// @Description  Returns list of business organs.
// @Tags         organs
// @Produce      json
// @Success      200 {array} OrganResponse
// @Failure      500 {object} map[string]string
// @Router       /api/v1/organs [get]
func (h *OrgansHandler) List(c *gin.Context) {
	items, err := h.svc.List(c.Request.Context())
	if err != nil {
		writeError(c, err)
		return
	}

	out := make([]OrganResponse, 0, len(items))
	for _, it := range items {
		out = append(out, toOrganResponse(it))
	}
	c.JSON(http.StatusOK, out)
}

// GetOrgan godoc
// @Summary      Get organ by id
// @Tags         organs
// @Produce      json
// @Param        id path string true "Organ ID (uuid)"
// @Success      200 {object} OrganResponse
// @Failure      400 {object} map[string]string
// @Failure      404 {object} map[string]string
// @Failure      500 {object} map[string]string
// @Router       /api/v1/organs/{id} [get]
func (h *OrgansHandler) Get(c *gin.Context) {
	id := strings.TrimSpace(c.Param("id"))
	o, err := h.svc.Get(c.Request.Context(), id)
	if err != nil {
		writeError(c, err)
		return
	}
	c.JSON(http.StatusOK, toOrganResponse(o))
}

// swagger:model OrganUpsertRequest
type OrganUpsertRequest struct {
	Slug        string `json:"slug" binding:"required" example:"finance"`
	Title       string `json:"title" binding:"required" example:"Finance"`
	Description string `json:"description" example:"Everything related to money, cashflow, budgeting"`
}

// CreateOrgan godoc
// @Summary      Create organ
// @Tags         organs
// @Accept       json
// @Produce      json
// @Security     BearerAuth
// @Param        Authorization header string true "Bearer {accessToken}"
// @Param        request body OrganUpsertRequest true "Organ payload"
// @Success      201 {object} OrganResponse
// @Failure      400 {object} map[string]string
// @Failure      401 {object} map[string]string
// @Failure      403 {object} map[string]string
// @Failure      409 {object} map[string]string
// @Failure      500 {object} map[string]string
// @Router       /api/v1/organs [post]
func (h *OrgansHandler) Create(c *gin.Context) {
	var req OrganUpsertRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	created, err := h.svc.Create(c.Request.Context(), organs.Organ{
		Slug:        req.Slug,
		Title:       req.Title,
		Description: req.Description,
	})
	if err != nil {
		writeError(c, err)
		return
	}
	c.JSON(http.StatusCreated, toOrganResponse(created))
}

// UpdateOrgan godoc
// @Summary      Update organ
// @Tags         organs
// @Accept       json
// @Produce      json
// @Security     BearerAuth
// @Param        Authorization header string true "Bearer {accessToken}"
// @Param        id path string true "Organ ID (uuid)"
// @Param        request body OrganUpsertRequest true "Organ payload"
// @Success      200 {object} OrganResponse
// @Failure      400 {object} map[string]string
// @Failure      401 {object} map[string]string
// @Failure      403 {object} map[string]string
// @Failure      404 {object} map[string]string
// @Failure      409 {object} map[string]string
// @Failure      500 {object} map[string]string
// @Router       /api/v1/organs/{id} [put]
func (h *OrgansHandler) Update(c *gin.Context) {
	id := strings.TrimSpace(c.Param("id"))

	var req OrganUpsertRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	updated, err := h.svc.Update(c.Request.Context(), id, organs.Organ{
		Slug:        req.Slug,
		Title:       req.Title,
		Description: req.Description,
	})
	if err != nil {
		writeError(c, err)
		return
	}
	c.JSON(http.StatusOK, toOrganResponse(updated))
}

// DeleteOrgan godoc
// @Summary      Delete organ
// @Tags         organs
// @Produce      json
// @Security     BearerAuth
// @Param        Authorization header string true "Bearer {accessToken}"
// @Param        id path string true "Organ ID (uuid)"
// @Success      204
// @Failure      401 {object} map[string]string
// @Failure      403 {object} map[string]string
// @Failure      404 {object} map[string]string
// @Failure      500 {object} map[string]string
// @Router       /api/v1/organs/{id} [delete]
func (h *OrgansHandler) Delete(c *gin.Context) {
	id := strings.TrimSpace(c.Param("id"))
	if err := h.svc.Delete(c.Request.Context(), id); err != nil {
		writeError(c, err)
		return
	}
	c.Status(http.StatusNoContent)
}
