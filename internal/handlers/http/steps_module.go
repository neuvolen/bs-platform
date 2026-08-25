package http

import (
	"github.com/bnursik/business_surgery_backend/internal/middleware"
	"github.com/gin-gonic/gin"
)

type StepsModule struct {
	h      *StepsHandler
	secret []byte
}

func NewStepsModule(h *StepsHandler, secret []byte) *StepsModule {
	return &StepsModule{h: h, secret: secret}
}

func (m *StepsModule) Register(r *gin.Engine) {
	api := r.Group("/api/v1")

	// Public read
	api.GET("/plans/:planId/steps", m.h.List)

	protected := api.Group("/")
	protected.Use(middleware.AuthJWT(m.secret))
	protected.Use(middleware.RequirePerm("plans:manage"))

	protected.POST("/plans/:planId/steps", m.h.Add)
	protected.PUT("/plans/:planId/steps/:stepId", m.h.Update)
	protected.DELETE("/plans/:planId/steps/:stepId", m.h.Delete)
}
