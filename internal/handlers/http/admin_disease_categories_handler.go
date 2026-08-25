package http

import (
	"net/http"
	"strings"

	"github.com/bnursik/business_surgery_backend/internal/domain/diseasecategories"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

// swagger:model AdminDiseaseCategoryUpsertRequest
type AdminDiseaseCategoryUpsertRequest struct {
	Title string `json:"title" binding:"required" example:"Cashflow"`
}

type AdminDiseaseCategoriesHandler struct {
	svc diseasecategories.Service
}

func NewAdminDiseaseCategoriesHandler(svc diseasecategories.Service) *AdminDiseaseCategoriesHandler {
	return &AdminDiseaseCategoriesHandler{svc: svc}
}

// ListCategories godoc
// @Summary      List disease categories (admin)
// @Tags         admin-categories
// @Produce      json
// @Security     BearerAuth
// @Param        Authorization header string true "Bearer {accessToken}"
// @Success      200 {array} DiseaseCategoryResponse
// @Failure      401 {object} map[string]string
// @Failure      403 {object} map[string]string
// @Failure      500 {object} map[string]string
// @Router       /api/v1/admin/disease-categories [get]
func (h *AdminDiseaseCategoriesHandler) List(c *gin.Context) {
	items, err := h.svc.List(c.Request.Context())
	if err != nil {
		writeError(c, err)
		return
	}

	out := make([]DiseaseCategoryResponse, 0, len(items))
	for _, it := range items {
		out = append(out, DiseaseCategoryResponse{
			ID:    it.ID.String(),
			Code:  it.Code,
			Title: it.Title,
		})
	}
	c.JSON(http.StatusOK, out)
}

// CreateCategory godoc
// @Summary      Create disease category
// @Tags         admin-categories
// @Accept       json
// @Produce      json
// @Security     BearerAuth
// @Param        Authorization header string true "Bearer {accessToken}"
// @Param        request body AdminDiseaseCategoryUpsertRequest true "Category payload"
// @Success      201 {object} DiseaseCategoryResponse
// @Failure      400 {object} map[string]string
// @Failure      401 {object} map[string]string
// @Failure      403 {object} map[string]string
// @Failure      409 {object} map[string]string
// @Failure      500 {object} map[string]string
// @Router       /api/v1/admin/disease-categories [post]
func (h *AdminDiseaseCategoriesHandler) Create(c *gin.Context) {
	var req AdminDiseaseCategoryUpsertRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	created, err := h.svc.Create(c.Request.Context(), diseasecategories.Category{
		Title: strings.TrimSpace(req.Title),
	})
	if err != nil {
		writeError(c, err)
		return
	}

	c.JSON(http.StatusCreated, DiseaseCategoryResponse{
		ID:    created.ID.String(),
		Code:  created.Code,
		Title: created.Title,
	})
}

// UpdateCategory godoc
// @Summary      Update disease category
// @Tags         admin-categories
// @Accept       json
// @Produce      json
// @Security     BearerAuth
// @Param        Authorization header string true "Bearer {accessToken}"
// @Param        id path string true "Category ID (uuid)"
// @Param        request body AdminDiseaseCategoryUpsertRequest true "Category payload"
// @Success      200 {object} DiseaseCategoryResponse
// @Failure      400 {object} map[string]string
// @Failure      401 {object} map[string]string
// @Failure      403 {object} map[string]string
// @Failure      404 {object} map[string]string
// @Failure      409 {object} map[string]string
// @Failure      500 {object} map[string]string
// @Router       /api/v1/admin/disease-categories/{id} [put]
func (h *AdminDiseaseCategoriesHandler) Update(c *gin.Context) {
	id := strings.TrimSpace(c.Param("id"))
	if _, err := uuid.Parse(id); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_category_id"})
		return
	}

	var req AdminDiseaseCategoryUpsertRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	updated, err := h.svc.Update(c.Request.Context(), id, diseasecategories.Category{
		Title: strings.TrimSpace(req.Title),
	})
	if err != nil {
		writeError(c, err)
		return
	}

	c.JSON(http.StatusOK, DiseaseCategoryResponse{
		ID:    updated.ID.String(),
		Code:  updated.Code,
		Title: updated.Title,
	})
}

// DeleteCategory godoc
// @Summary      Delete disease category
// @Description  Deletes category if it is not referenced by any disease.
// @Tags         admin-categories
// @Produce      json
// @Security     BearerAuth
// @Param        Authorization header string true "Bearer {accessToken}"
// @Param        id path string true "Category ID (uuid)"
// @Success      204
// @Failure      400 {object} map[string]string
// @Failure      401 {object} map[string]string
// @Failure      403 {object} map[string]string
// @Failure      404 {object} map[string]string
// @Failure      409 {object} map[string]string
// @Failure      500 {object} map[string]string
// @Router       /api/v1/admin/disease-categories/{id} [delete]
func (h *AdminDiseaseCategoriesHandler) Delete(c *gin.Context) {
	id := strings.TrimSpace(c.Param("id"))
	if _, err := uuid.Parse(id); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_category_id"})
		return
	}

	if err := h.svc.Delete(c.Request.Context(), id); err != nil {
		writeError(c, err)
		return
	}

	c.Status(http.StatusNoContent)
}
