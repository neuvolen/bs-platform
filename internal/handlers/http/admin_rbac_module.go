package http

import (
	"github.com/bnursik/business_surgery_backend/internal/middleware"
	"github.com/gin-gonic/gin"
)

type AdminRBACModule struct {
	h      *AdminRBACHandler
	secret []byte
}

func NewAdminRBACModule(h *AdminRBACHandler, secret []byte) *AdminRBACModule {
	return &AdminRBACModule{h: h, secret: secret}
}

func (m *AdminRBACModule) Register(r *gin.Engine) {
	api := r.Group("/api/v1")

	protected := api.Group("/admin")
	protected.Use(middleware.AuthJWT(m.secret))
	protected.Use(middleware.RequirePerm("rbac:manage"))

	protected.GET("/users/:id/roles", m.h.GetUserRoles)
	protected.POST("/users/:id/roles", m.h.AddRole)
	protected.DELETE("/users/:id/roles/:roleCode", m.h.RemoveRole)
}
