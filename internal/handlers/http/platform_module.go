package http

import (
	"context"
	"time"

	"github.com/bnursik/business_surgery_backend/internal/bot"
	"github.com/bnursik/business_surgery_backend/internal/middleware"
	"github.com/gin-gonic/gin"
)

type PlatformModule struct {
	AI     *PlatformAI
	h      *PlatformHandler
	auth   *PlatformAuthHandler
	lead   *LeadHome // lead_home.go
	secret []byte
}

func NewPlatformModule(h *PlatformHandler, a *PlatformAuthHandler, secret []byte) *PlatformModule {
	a.repo, a.names = h.repo, h.names
	if h.repo != nil {
		a.leads = &LeadFunnel{docs: h.repo, now: time.Now}
	}
	m := &PlatformModule{h: h, auth: a, secret: secret, AI: NewPlatformAI(h.repo, nil)}
	if a.leads != nil {
		m.lead = &LeadHome{f: a.leads, bot: func() string { n, _ := a.username(); return n }}
	}
	if h.repo != nil {
		go m.AI.EventsLoop(context.Background())
		go m.AI.LoadEmbedded(context.Background(), a.botToken)
		go m.AI.SeedGuides(context.Background())
		go m.AI.MigrateRazborPrice(context.Background()) // price_migrate.go
		go m.AI.LibExtLoop(context.Background())         // library_ext.go
		go m.AI.RecsLoop(context.Background())           // ai_recs.go
		go m.AI.SeedMarketingAll(context.Background())   // mkt_competitors.go
		go m.AI.ThreadsLoop(context.Background())
		go m.AI.SetupWhatsApp(context.Background())
	}
	return m
}

// WireLeadBot lets the lead home send through the bot: the booking's
// confirmation to the lead and its note to the team.
func (m *PlatformModule) WireLeadBot(send func(ctx context.Context, chatID int64, text string, kb map[string]any) error, admins []int64) {
	if m.lead == nil {
		return
	}
	m.lead.f.send, m.lead.f.admins = send, admins
	if m.lead.f.app == "" {
		m.lead.f.app = bot.WebAppBase
	}
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
	// R25: the branded template of a library tool by an open link (library_rich.go)
	r.GET("/t/:file", PublicTemplate)
	r.HEAD("/t/:file", PublicTemplate)
	// R32d: the tour's voice as immutable files (platform_tts_static.go)
	pub.GET("/tts/a/:file", m.AI.TTSFile)

	// R27: the lead's home. The only routes a lead's token opens; the team
	// sees the same in the «Лид» preview (lead_home.go).
	if m.lead != nil {
		l := r.Group("/api/v1/platform/lead")
		l.Use(middleware.AuthJWTAllowLead(m.secret))
		l.Use(middleware.RequireRole("lead", "admin", "moderator"))
		l.GET("/home", m.lead.Home)
		l.POST("/diag", m.lead.Diag)
		l.POST("/book", m.lead.Book)
		l.POST("/request", m.lead.Request)
	}

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
	g.POST("/ai/jobs/:id/retry", m.AI.RetryCall) // platform_calls.go: записи разборов
	g.GET("/ai/calls", m.AI.Calls)
	g.GET("/ai/calls/:id/summary.pdf", m.AI.CallSummaryPDF) // platform_calls_summary.go (R32d)
	// R32e: саммари разбора: черновик → правка → публикация резиденту (callsum_flow.go)
	g.GET("/ai/calls/:id/summary", m.AI.CallSummary)
	g.PUT("/ai/calls/:id/summary", m.AI.PutCallSummary)
	g.POST("/ai/calls/:id/summary/regenerate", m.AI.RegenCallSummary)
	g.POST("/ai/calls/:id/publish", m.AI.PublishCall)
	g.GET("/ai/callsum/settings", m.AI.CallSumSettings)
	g.PUT("/ai/callsum/settings", m.AI.PutCallSumSettings)
	g.POST("/ai/events", m.AI.RefreshEvents)
	g.GET("/ai/events", m.AI.EventsStatus)
	g.POST("/ai/recs", m.AI.RecsNow)
	g.POST("/ai/recs/:id", m.AI.RecAction)
	g.POST("/ai/marketing", m.AI.Marketing)
	g.GET("/insights", m.AI.Insights)        // r32_insights.go: «Идеи и заметки» → «Аналитика»
	g.POST("/ai/gallup", m.AI.Gallup)        // platform_gallup.go: 34 talents from a Gallup report
	g.POST("/gallup/pdf", GallupPDF)         // platform_gallup_pdf.go: the analysis as a PDF (R29)
	g.POST("/ai/health", m.AI.Health)        // platform_health.go: organ scores for «Здоровье бизнеса»
	g.POST("/tts", m.AI.TTS)                 // platform_tts.go: voice guide (onboarding)
	g.POST("/tts/warm", m.AI.TTSWarm)        // platform_tts_warm.go: the tour phrases made ahead of time
	g.GET("/tts/manifest", m.AI.TTSManifest) // platform_tts_static.go: phrase → file, state
	g.POST("/tts/voice", m.AI.TTSSetVoice)
	g.GET("/guide/:id", m.AI.GuideForPlatform)
	g.GET("/library/rich", LibraryRich)               // library_rich.go
	g.GET("/library/template/:file", LibraryTemplate) // <id>.pdf
	g.GET("/library/templates.zip", LibraryTemplatesZip)
	g.GET("/ops", m.AI.OpsList)
	g.POST("/threads/publish", m.AI.ThreadsNow)
	g.GET("/crm/wa/status", m.AI.WAStatus)
	g.GET("/crm/chats", m.AI.WAChats)
	g.GET("/crm/chats/:phone", m.AI.WAMessages)
	g.POST("/crm/chats/:phone/send", m.AI.WASend)
}
