package http

import (
	"github.com/bnursik/business_surgery_backend/internal/middleware"
	"github.com/gin-gonic/gin"
)

type MeDiaryModule struct {
	h      *MeDiaryHandler
	secret []byte
}

func NewMeDiaryModule(h *MeDiaryHandler, secret []byte) *MeDiaryModule {
	return &MeDiaryModule{h: h, secret: secret}
}

func (m *MeDiaryModule) Register(r *gin.Engine) {
	api := r.Group("/api/v1")

	protected := api.Group("/")
	protected.Use(middleware.AuthJWT(m.secret))
	protected.Use(middleware.RequirePerm("profile:read"))

	protected.POST("/me/diary", m.h.CreateDiary)
	protected.GET("/me/diary", m.h.ListDiary)
}
