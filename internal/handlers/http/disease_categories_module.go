package http

import "github.com/gin-gonic/gin"

type DiseaseCategoriesModule struct {
	h *DiseaseCategoriesHandler
}

func NewDiseaseCategoriesModule(h *DiseaseCategoriesHandler) *DiseaseCategoriesModule {
	return &DiseaseCategoriesModule{h: h}
}

func (m *DiseaseCategoriesModule) Register(r *gin.Engine) {
	api := r.Group("/api/v1")
	api.GET("/diseases/categories", m.h.List)
}
