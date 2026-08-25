package http

import (
	"github.com/bnursik/business_surgery_backend/internal/middleware"
	"github.com/gin-gonic/gin"
)

type AdminUsersModule struct {
	h      *AdminUsersHandler
	secret []byte
}

func NewAdminUsersModule(h *AdminUsersHandler, secret []byte) *AdminUsersModule {
	return &AdminUsersModule{h: h, secret: secret}
}

func (m *AdminUsersModule) Register(r *gin.Engine) {
	api := r.Group("/api/v1")

	protected := api.Group("/admin")
	protected.Use(middleware.AuthJWT(m.secret))
	protected.Use(middleware.RequirePerm("rbac:manage"))

	protected.GET("/users", m.h.ListUsers)
	protected.GET("/users/:id/permissions", m.h.GetUserPermissions)
}
