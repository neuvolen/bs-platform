package http

import (
	"github.com/bnursik/business_surgery_backend/internal/middleware"
	"github.com/gin-gonic/gin"
)

type ModeratorDashboardUsersManageModule struct {
	h      *ModeratorDashboardUsersManageHandler
	secret []byte
}

func NewModeratorDashboardUsersManageModule(h *ModeratorDashboardUsersManageHandler, secret []byte) *ModeratorDashboardUsersManageModule {
	return &ModeratorDashboardUsersManageModule{h: h, secret: secret}
}

func (m *ModeratorDashboardUsersManageModule) Register(r *gin.Engine) {
	api := r.Group("/api/v1")

	protected := api.Group("/moderator")
	protected.Use(middleware.AuthJWT(m.secret))
	protected.Use(middleware.RequirePerm("participants:manage"))

	protected.PUT("/dashboard/users/:id", m.h.UpdateDashboardUser)
	protected.DELETE("/dashboard/users/:id", m.h.DeleteDashboardUser)
}
