package http

import (
	"github.com/gin-gonic/gin"
)

type PlansModule struct {
	h *PlansHandler
}

func NewPlansModule(h *PlansHandler) *PlansModule {
	return &PlansModule{h: h}
}

func (m *PlansModule) Register(r *gin.Engine) {
	api := r.Group("/api/v1")

	// Public read
	api.GET("/diseases/:id/plan", m.h.GetByDisease)
	api.GET("/plans/:planId", m.h.GetByID)
}
