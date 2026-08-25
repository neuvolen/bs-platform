package http

import (
	"github.com/bnursik/business_surgery_backend/internal/middleware"
	"github.com/gin-gonic/gin"
)

type MeTrackingModule struct {
	h      *MeTrackingHandler
	secret []byte
}

func NewMeTrackingModule(h *MeTrackingHandler, secret []byte) *MeTrackingModule {
	return &MeTrackingModule{h: h, secret: secret}
}

func (m *MeTrackingModule) Register(r *gin.Engine) {
	api := r.Group("/api/v1")

	protected := api.Group("/")
	protected.Use(middleware.AuthJWT(m.secret))
	protected.Use(middleware.RequirePerm("profile:read"))

	protected.GET("/me/progress", m.h.MeProgress)
	protected.GET("/me/diseases", m.h.MeDiseases)
	protected.GET("/me/activity", m.h.MeActivity)
}
