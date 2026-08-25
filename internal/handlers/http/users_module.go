package http

import (
	"github.com/bnursik/business_surgery_backend/internal/middleware"
	"github.com/gin-gonic/gin"
)

type UsersModule struct {
	h      *UsersHandler
	secret []byte
}

func NewUsersModule(h *UsersHandler, secret []byte) *UsersModule {
	return &UsersModule{h: h, secret: secret}
}

func (m *UsersModule) Register(r *gin.Engine) {
	api := r.Group("/api/v1")

	protected := api.Group("/")
	protected.Use(middleware.AuthJWT(m.secret))
	protected.Use(middleware.RequirePerm("profile:read"))

	protected.GET("/me", m.h.Me)
	protected.PATCH("/me", m.h.UpdateMe)
}
