package http

import (
	"context"
	"github.com/bnursik/business_surgery_backend/internal/middleware"
	"github.com/gin-gonic/gin"
)

type PlatformModule struct {
	AI     *PlatformAI
	h      *PlatformHandler
	auth   *PlatformAuthHandler
	secret []byte
}

func NewPlatformModule(h *PlatformHandler, a *PlatformAuthHandler, secret []byte) *PlatformModule {
	a.repo, a.names = h.repo, h.names
	m := &PlatformModule{h: h, auth: a, secret: secret, AI: NewPlatformAI(h.repo, nil)}
	if h.repo != nil {
		go m.AI.EventsLoop(context.Background())
		go m.AI.LoadEmbedded(context.Background(), a.botToken)
		go m.AI.SeedGuides(context.Background())
		go m.AI.SeedMarketing(context.Background())
		go m.AI.ThreadsLoop(context.Background())
		go m.AI.SetupWhatsApp(context.Background())
	}
	return m
}

func (m *PlatformModule) Register(r *gin.Engine) {
	// Public: what the login screen needs, and the login itself.
	pub := r.Group("/api/v1/platform")
	pub.GET("/config", m.auth.Config)
	pub.POST("/auth/telegram", m.auth.Login)
	pub.POST("/auth/logout", m.auth.Logout)
	// Resident list from the Google Sheet, signed with the bot token.
	pub.POST("/residents/sync", m.auth.SyncResidents)
	pub.POST("/ingest", m.AI.Ingest)
	r.POST("/api/v1/wa/webhook/:secret", m.AI.WAWebhook)

	g := r.Group("/api/v1/platform")
	g.Use(middleware.AuthJWT(m.secret))
	// Residents get in too; what each of them may see is decided per request
	// (platform_resident.go).
	g.Use(middleware.RequireRole("admin", "moderator", "resident"))

	g.GET("/sync", m.h.Sync)
	g.PUT("/boards/:id", m.h.PutBoard)
	g.DELETE("/boards/:id", m.h.DeleteBoard)
	g.GET("/boards/:id/versions", m.h.BoardVersions)
	g.GET("/boards/:id/versions/:version", m.h.BoardVersion)
	g.PUT("/docs/:key", m.h.PutDoc)
	g.GET("/docs/:key/versions", m.h.DocVersions)
	g.GET("/docs/:key/versions/:version", m.h.DocVersion)
	g.POST("/import", m.h.Import)
	g.POST("/files", m.AI.UploadFile)
	g.GET("/files/:id", m.AI.GetFile)
	g.GET("/ai/status", m.AI.Status)
	g.POST("/ai/command", m.AI.Command)
	g.POST("/ai/call", m.AI.Call)
	g.GET("/ai/jobs/:id", m.AI.Job)
	g.GET("/ai/jobs", m.AI.Jobs)
	g.POST("/ai/events", m.AI.RefreshEvents)
	g.POST("/ai/marketing", m.AI.Marketing)
	g.GET("/guide/:id", m.AI.GuideForPlatform)
	g.GET("/ops", m.AI.OpsList)
	g.POST("/threads/publish", m.AI.ThreadsNow)
	g.GET("/crm/wa/status", m.AI.WAStatus)
	g.GET("/crm/chats", m.AI.WAChats)
	g.GET("/crm/chats/:phone", m.AI.WAMessages)
	g.POST("/crm/chats/:phone/send", m.AI.WASend)
}
