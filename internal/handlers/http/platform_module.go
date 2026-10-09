package http

import (
	"context"
	"errors"
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
	// R38b: Маркетинг → «Партнёрства и UGC», воронки и база CRM (partners.go, crm_base.go)
	Partners *Partners
	// R52: кто сейчас на доске: курсоры, выделение, аватары (platform_presence.go)
	Presence *PlatformPresence
	// Доступ для ассистента, личные ссылки входа, сессии (assist_access.go).
	// Его Guard ставится в middleware.SessionGuard при сборке сервера (app.go).
	Access *AssistAccess
}

func NewPlatformModule(h *PlatformHandler, a *PlatformAuthHandler, secret []byte) *PlatformModule {
	a.repo, a.names = h.repo, h.names
	if h.repo != nil {
		a.leads = &LeadFunnel{docs: h.repo, now: time.Now}
	}
	m := &PlatformModule{h: h, auth: a, secret: secret, AI: NewPlatformAI(h.repo, nil)}
	m.Presence = NewPlatformPresence(func(tg int64) string { return a.team[tg] }, h.residentOf, func(ctx context.Context, id string) (string, error) {
		if h.repo == nil {
			return "", errors.New("no storage")
		}
		b, err := h.repo.GetBoard(ctx, id)
		if err != nil || b == nil {
			return "", errors.New("no board")
		}
		return b.Resident, nil
	})
	m.Presence.Start(context.Background())
	if h.repo != nil {
		m.Access = NewAssistAccess(h.repo, a, h.names)
		a.access = m.Access
	}
	m.AI.KeySecret = secret // R34a: seals the Claude key saved in the settings
	// R36: the tour's premium voice (ElevenLabs), picked in the settings
	m.AI.Premium = NewPremiumVoice(h.repo, m.AI.aiKeySecret, m.AI.tourTexts)
	m.AI.Premium.OnReady = m.AI.premiumReadyNote
	if a.leads != nil {
		m.lead = &LeadHome{f: a.leads, bot: func() string { n, _ := a.username(); return n }}
	}
	if h.repo != nil {
		lctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		m.AI.LoadAIKey(lctx) // platform_ai_key.go
		m.AI.Premium.Load(lctx)
		cancel()
		m.AI.Premium.Start(context.Background())
		go m.AI.EventsLoop(context.Background())
		go m.AI.TGEventsLoop(context.Background()) // platform_tgevents.go (R70)
		go m.AI.LoadEmbedded(context.Background(), a.botToken)
		go m.AI.SeedGuides(context.Background())
		go m.AI.MigrateRazborPrice(context.Background()) // price_migrate.go
		go m.AI.LibExtLoop(context.Background())         // library_ext.go
		go m.AI.RecsLoop(context.Background())           // ai_recs.go
		go m.AI.SeedMarketingAll(context.Background())   // mkt_competitors.go
		go m.AI.ThreadsLoop(context.Background())
		go m.AI.SetupWhatsApp(context.Background())
		m.Partners = NewPartners(h.repo)
		m.Partners.Bot = func() string { n, _ := a.username(); return n }
		m.Partners.Base = h.repo
		m.Partners.Start(context.Background())
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
	// R63: the summary PDF by the signed link of «Отправить в WhatsApp» (callsum_r63.go)
	r.GET("/sum/:key", m.AI.PublicSummary)
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
		l.POST("/idea", m.lead.Idea) // R46: ideas.go
	}

	// R46: бизнес-идеи (ideas.go): каталог открыт и лиду (верх воронки), и команде, и резидентам
	ig := r.Group("/api/v1/platform")
	ig.Use(middleware.AuthJWTAllowLead(m.secret))
	ig.Use(middleware.RequireRole("lead", "admin", "moderator", "resident"))
	ig.GET("/ideas", Ideas)
	go WarmIdeas(false)

	g := r.Group("/api/v1/platform")
	g.Use(middleware.AuthJWT(m.secret))
	// Residents get in too; what each of them may see is decided per request
	// (platform_resident.go).
	g.Use(middleware.RequireRole("admin", "moderator", "resident"))

	g.GET("/sync", m.h.Sync)
	g.POST("/presence", m.Presence.Post)          // R52: platform_presence.go
	g.GET("/presence/stream", m.Presence.Stream)  // R52
	g.POST("/call/room", m.Presence.CallRoom)     // R65: онлайн-разбор на доске (platform_callroom.go)
	g.POST("/call/signal", m.Presence.CallSignal) // R65
	g.GET("/call/stream", m.Presence.CallStream)  // R65
	g.PUT("/boards/:id", m.h.PutBoard)
	g.DELETE("/boards/:id", m.h.DeleteBoard)
	g.GET("/boards/:id/versions", m.h.BoardVersions)
	g.GET("/boards/:id/versions/:version", m.h.BoardVersion)
	g.PUT("/docs/:key", m.h.PutDoc)
	g.GET("/mycal", m.h.ResidentCal) // R59: календарь резидента (mycal_scope.go)
	g.PUT("/mycal", m.h.ResidentCal)
	g.GET("/docs/:key/versions", m.h.DocVersions)
	g.GET("/docs/:key/versions/:version", m.h.DocVersion)
	g.POST("/import", m.h.Import)
	g.POST("/files", m.AI.UploadFile)
	g.GET("/files/:id", m.AI.GetFile)
	g.GET("/ai/status", m.AI.Status)
	g.GET("/ai/key", m.AI.AIKey) // platform_ai_key.go: «Ключ Claude» (admin)
	g.PUT("/ai/key", m.AI.PutAIKey)
	g.DELETE("/ai/key", m.AI.DeleteAIKey)
	g.POST("/ai/key/test", m.AI.TestAIKey)
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
	g.DELETE("/ai/calls/:id", m.AI.DeleteCall)    // R63: callsum_r63.go
	g.POST("/ai/calls/:id/share", m.AI.ShareCall) // R63: WhatsApp
	g.GET("/ai/callsum/settings", m.AI.CallSumSettings)
	g.PUT("/ai/callsum/settings", m.AI.PutCallSumSettings)
	g.POST("/ai/events", m.AI.RefreshEvents)
	g.GET("/ai/events", m.AI.EventsStatus)
	g.POST("/ai/recs", m.AI.RecsNow)
	g.POST("/ai/recs/:id", m.AI.RecAction)
	g.POST("/ai/marketing", m.AI.Marketing)
	g.POST("/ai/forecast", m.AI.Forecast)            // forecast_ai.go: «Учёт» → «Прогноз» → «Что если…» с ИИ (R48)
	g.GET("/insights", m.AI.Insights)                // r32_insights.go: «Идеи и заметки» → «Аналитика»
	g.POST("/ai/gallup", m.AI.Gallup)                // platform_gallup.go: 34 talents from a Gallup report
	g.POST("/gallup/pdf", GallupPDF)                 // platform_gallup_pdf.go: the analysis as a PDF (R29)
	g.POST("/razbor/pdf", RazborPrintPDF)            // platform_razbor_print.go: «Печать разбора», A4 checklist (R69)
	g.POST("/gallup/fix/send", m.AI.GallupFixSend)   // platform_gallup_fix.go: the corrected analysis to the resident (R68)
	g.POST("/gallup/fix/audit", m.AI.GallupFixAudit) // R68: who got an analysis not from their own report
	g.POST("/ai/health", m.AI.Health)                // platform_health.go: organ scores for «Здоровье бизнеса»
	g.POST("/tts", m.AI.TTS)                         // platform_tts.go: voice guide (onboarding)
	g.POST("/tts/warm", m.AI.TTSWarm)                // platform_tts_warm.go: the tour phrases made ahead of time
	g.GET("/tts/manifest", m.AI.TTSManifest)         // platform_tts_static.go: phrase → file, state
	g.POST("/tts/voice", m.AI.TTSSetVoice)
	m.AI.Premium.Register(pub, g) // R36: «Голос ElevenLabs» (platform_voice_premium.go)
	// R57: the video voiceovers of GitHub Actions (OIDC), with the same key (voicepipe.go)
	pub.POST("/voicepipe/eleven", NewVoicePipe(m.AI.Premium.key).Eleven)
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
	if m.Partners != nil {
		m.Partners.Register(r, g) // R38b: /p/<code>, /r?ref=, /partners/links, /crm/base-import
	}
	RegisterVideo(r, g, m) // R54: Маркетинг → SMM → «Видео» (platform_video.go)
	if m.Access != nil {
		m.Access.Register(r, pub, g) // доступ для ассистента, личные ссылки, сессии
	}
}
