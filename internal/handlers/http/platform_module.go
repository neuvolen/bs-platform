package http

import (
	"github.com/bnursik/business_surgery_backend/internal/middleware"
	"github.com/gin-gonic/gin"
)

type PlatformModule struct {
	h      *PlatformHandler
	auth   *PlatformAuthHandler
	secret []byte
}

func NewPlatformModule(h *PlatformHandler, a *PlatformAuthHandler, secret []byte) *PlatformModule {
	return &PlatformModule{h: h, auth: a, secret: secret}
}

func (m *PlatformModule) Register(r *gin.Engine) {
	// Public: what the login screen needs, and the login itself.
	pub := r.Group("/api/v1/platform")
	pub.GET("/config", m.auth.Config)
	pub.POST("/auth/telegram", m.auth.Login)

	g := r.Group("/api/v1/platform")
	g.Use(middleware.AuthJWT(m.secret))
	// For now the platform is for the team only. Residents get their own,
	// narrower access when Telegram login lands.
	g.Use(middleware.RequireRole("admin", "moderator"))

	g.GET("/sync", m.h.Sync)
	g.PUT("/boards/:id", m.h.PutBoard)
	g.DELETE("/boards/:id", m.h.DeleteBoard)
	g.GET("/boards/:id/versions", m.h.BoardVersions)
	g.GET("/boards/:id/versions/:version", m.h.BoardVersion)
	g.PUT("/docs/:key", m.h.PutDoc)
	g.POST("/import", m.h.Import)
}
