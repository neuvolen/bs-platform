package http

import (
	"github.com/bnursik/business_surgery_backend/internal/middleware"
	"github.com/gin-gonic/gin"
)

type OrgansModule struct {
	h      *OrgansHandler
	secret []byte
}

func NewOrgansModule(h *OrgansHandler, secret []byte) *OrgansModule {
	return &OrgansModule{h: h, secret: secret}
}

func (m *OrgansModule) Register(r *gin.Engine) {
	api := r.Group("/api/v1")

	// Public read
	api.GET("/organs", m.h.List)
	api.GET("/organs/:id", m.h.Get)

	// Protected write
	protected := api.Group("/")
	protected.Use(middleware.AuthJWT(m.secret))
	protected.Use(middleware.RequirePerm("organs:manage"))

	protected.POST("/organs", m.h.Create)
	protected.PUT("/organs/:id", m.h.Update)
	protected.DELETE("/organs/:id", m.h.Delete)
}
