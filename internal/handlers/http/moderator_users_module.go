package http

import (
	"github.com/bnursik/business_surgery_backend/internal/middleware"
	"github.com/gin-gonic/gin"
)

type ModeratorUsersModule struct {
	h      *ModeratorUsersHandler
	secret []byte
}

func NewModeratorUsersModule(h *ModeratorUsersHandler, secret []byte) *ModeratorUsersModule {
	return &ModeratorUsersModule{h: h, secret: secret}
}

func (m *ModeratorUsersModule) Register(r *gin.Engine) {
	api := r.Group("/api/v1")

	protected := api.Group("/moderator")
	protected.Use(middleware.AuthJWT(m.secret))

	protected.GET("/users/:id/progress",
		middleware.RequirePerm("participants:read"),
		m.h.Progress,
	)

	protected.GET("/users/:id/diseases",
		middleware.RequirePerm("participants:read"),
		m.h.Diseases,
	)

	protected.GET("/users/:id/activity",
		middleware.RequirePerm("participants:read"),
		m.h.Activity,
	)

	protected.GET("/users/:id/treatment",
		middleware.RequirePerm("participants:read"),
		m.h.TreatmentOverview,
	)

	protected.POST("/users/:id/feedback",
		middleware.RequirePerm("feedback:create"),
		m.h.CreateFeedback,
	)
}
 