package http

import (
	"github.com/bnursik/business_surgery_backend/internal/middleware"
	"github.com/gin-gonic/gin"
)

type DiseasesModule struct {
	h      *DiseasesHandler
	secret []byte
}

func NewDiseasesModule(h *DiseasesHandler, secret []byte) *DiseasesModule {
	return &DiseasesModule{h: h, secret: secret}
}

func (m *DiseasesModule) Register(r *gin.Engine) {
	api := r.Group("/api/v1")

	// Public read
	api.GET("/diseases", m.h.ListDetails)
	api.GET("/diseases/:id", m.h.GetDetails)

	// Protected write
	protected := api.Group("/")
	protected.Use(middleware.AuthJWT(m.secret))

	// Diseases CRUD
	diseasesManage := protected.Group("/")
	diseasesManage.Use(middleware.RequirePerm("diseases:manage"))
	diseasesManage.POST("/diseases", m.h.Create)
	diseasesManage.PUT("/diseases/:id", m.h.Update)
	diseasesManage.DELETE("/diseases/:id", m.h.Delete)

	// Plan upsert for disease
	plansManage := protected.Group("/")
	plansManage.Use(middleware.RequirePerm("plans:manage"))
	plansManage.PUT("/diseases/:id/plan", m.h.UpsertPlanForDisease)
}
