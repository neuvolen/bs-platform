package http

import (
	"github.com/bnursik/business_surgery_backend/internal/middleware"
	"github.com/gin-gonic/gin"
)

type PlatformModule struct {
	h      *PlatformHandler
	secret []byte
}

func NewPlatformModule(h *PlatformHandler, secret []byte) *PlatformModule {
	return &PlatformModule{h: h, secret: secret}
}

func (m *PlatformModule) Register(r *gin.Engine) {
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
