package http

import (
	"github.com/bnursik/business_surgery_backend/internal/middleware"
	"github.com/gin-gonic/gin"
)

type AdminDiseaseCategoriesModule struct {
	h      *AdminDiseaseCategoriesHandler
	secret []byte
}

func NewAdminDiseaseCategoriesModule(h *AdminDiseaseCategoriesHandler, secret []byte) *AdminDiseaseCategoriesModule {
	return &AdminDiseaseCategoriesModule{h: h, secret: secret}
}

func (m *AdminDiseaseCategoriesModule) Register(r *gin.Engine) {
	api := r.Group("/api/v1")

	protected := api.Group("/admin")
	protected.Use(middleware.AuthJWT(m.secret))
	protected.Use(middleware.RequirePerm("categories:manage"))

	protected.GET("/disease-categories", m.h.List)
	protected.POST("/disease-categories", m.h.Create)
	protected.PUT("/disease-categories/:id", m.h.Update)
	protected.DELETE("/disease-categories/:id", m.h.Delete)
}
