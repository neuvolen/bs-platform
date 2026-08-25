package http

import (
	"github.com/bnursik/business_surgery_backend/internal/middleware"
	"github.com/gin-gonic/gin"
)

type AuthModule struct {
	h      *AuthHandler
	secret []byte
}

func NewAuthModule(h *AuthHandler, secret []byte) *AuthModule {
	return &AuthModule{h: h, secret: secret}
}

func (m *AuthModule) Register(r *gin.Engine) {
	api := r.Group("/api/v1/auth")
	api.POST("/register", m.h.Register)
	api.POST("/login", m.h.Login)
	api.POST("/oauth", m.h.OAuthLogin)
	api.GET("/oauth/google/login", m.h.GoogleLogin)
	api.GET("/oauth/google/callback", m.h.GoogleCallback)
	api.POST("/refresh", m.h.Refresh)
	api.POST("/logout", m.h.Logout)

	protected := api.Group("/")
	protected.Use(middleware.AuthJWT(m.secret))
	protected.GET("/ping", m.h.Ping)
}
