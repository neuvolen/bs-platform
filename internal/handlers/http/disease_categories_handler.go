package http

import (
	"net/http"

	"github.com/bnursik/business_surgery_backend/internal/domain/diseasecategories"
	"github.com/gin-gonic/gin"
)

type DiseaseCategoriesHandler struct {
	svc diseasecategories.Service
}

func NewDiseaseCategoriesHandler(svc diseasecategories.Service) *DiseaseCategoriesHandler {
	return &DiseaseCategoriesHandler{svc: svc}
}

// ListDiseaseCategories godoc
// @Summary      List disease categories
// @Description  Returns categories for selector
// @Tags         diseases
// @Produce      json
// @Success      200 {array} DiseaseCategoryResponse
// @Router       /api/v1/diseases/categories [get]
func (h *DiseaseCategoriesHandler) List(c *gin.Context) {
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
