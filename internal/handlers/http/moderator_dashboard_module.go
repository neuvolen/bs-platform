package http

import (
	"github.com/bnursik/business_surgery_backend/internal/middleware"
	"github.com/gin-gonic/gin"
)

type ModeratorDashboardModule struct {
	h      *ModeratorDashboardHandler
	secret []byte
}

func NewModeratorDashboardModule(h *ModeratorDashboardHandler, secret []byte) *ModeratorDashboardModule {
	return &ModeratorDashboardModule{h: h, secret: secret}
}

func (m *ModeratorDashboardModule) Register(r *gin.Engine) {
	api := r.Group("/api/v1")

	protected := api.Group("/moderator")
	protected.Use(middleware.AuthJWT(m.secret))

	// full dashboard (stats + users)
	protected.GET("/dashboard",
		middleware.RequirePerm("dashboard:read"),
		middleware.RequirePerm("participants:read"),
		m.h.Dashboard,
	)

	// stats
	protected.GET("/dashboard/stats",
		middleware.RequirePerm("dashboard:read"),
		m.h.Stats,
	)

	// users list
	protected.GET("/dashboard/users",
		middleware.RequirePerm("participants:read"),
		m.h.Users,
	)
}
