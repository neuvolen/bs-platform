package http

import (
	"github.com/bnursik/business_surgery_backend/internal/middleware"
	"github.com/gin-gonic/gin"
)

type AdminTrackingModule struct {
	h      *AdminTrackingHandler
	secret []byte
}

func NewAdminTrackingModule(h *AdminTrackingHandler, secret []byte) *AdminTrackingModule {
	return &AdminTrackingModule{h: h, secret: secret}
}

func (m *AdminTrackingModule) Register(r *gin.Engine) {
	api := r.Group("/api/v1")

	protected := api.Group("/admin")
	protected.Use(middleware.AuthJWT(m.secret))
	protected.Use(middleware.RequirePerm("assignments:manage"))

	protected.POST("/assign-disease", m.h.AssignDiseaseToUser)
}
