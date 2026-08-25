package http

import (
	"github.com/bnursik/business_surgery_backend/internal/middleware"
	"github.com/gin-gonic/gin"
)

type MeStepsModule struct {
	h      *MeStepsHandler
	secret []byte
}

func NewMeStepsModule(h *MeStepsHandler, secret []byte) *MeStepsModule {
	return &MeStepsModule{h: h, secret: secret}
}

func (m *MeStepsModule) Register(r *gin.Engine) {
	api := r.Group("/api/v1")

	protected := api.Group("/me")
	protected.Use(middleware.AuthJWT(m.secret))
	protected.Use(middleware.RequirePerm("profile:read"))

	protected.GET("/diseases/:userDiseaseId/steps", m.h.ListMyDiseaseSteps)
	protected.POST("/steps/:userStepId/complete", m.h.CompleteMyStep)
	protected.POST("/steps/:userStepId/state", m.h.UpdateMyStepState)

	protected.POST("/diseases/:userDiseaseId/resolve", m.h.ResolveMyDisease)
}
