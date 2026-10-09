package app

import (
	"context"
	"github.com/bnursik/business_surgery_backend/internal/bot"
	"github.com/bnursik/business_surgery_backend/internal/club"
	"log"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/bnursik/business_surgery_backend/internal/domain/dashboardusers"
	"github.com/bnursik/business_surgery_backend/internal/domain/diseasecategories"
	"github.com/bnursik/business_surgery_backend/internal/domain/diseases"
	"github.com/bnursik/business_surgery_backend/internal/domain/organs"
	"github.com/bnursik/business_surgery_backend/internal/domain/plans"
	"github.com/bnursik/business_surgery_backend/internal/domain/rbac"
	"github.com/bnursik/business_surgery_backend/internal/domain/tracking"
	"github.com/bnursik/business_surgery_backend/internal/domain/users"

	httpapi "github.com/bnursik/business_surgery_backend/internal/handlers/http"
	"github.com/bnursik/business_surgery_backend/internal/repository/pg"

	authsvc "github.com/bnursik/business_surgery_backend/internal/services/auth"
	dashboardusersvc "github.com/bnursik/business_surgery_backend/internal/services/dashboardusers"
	diseasecategoriesvc "github.com/bnursik/business_surgery_backend/internal/services/diseasecategories"
	diseasessvc "github.com/bnursik/business_surgery_backend/internal/services/diseases"
	organsvc "github.com/bnursik/business_surgery_backend/internal/services/organs"
	plansvc "github.com/bnursik/business_surgery_backend/internal/services/plans"
	rbacsvc "github.com/bnursik/business_surgery_backend/internal/services/rbac"
	trackingsvc "github.com/bnursik/business_surgery_backend/internal/services/tracking"
	usersvc "github.com/bnursik/business_surgery_backend/internal/services/users"

	"github.com/bnursik/business_surgery_backend/pkg/auth"
	"github.com/bnursik/business_surgery_backend/web"
	"golang.org/x/oauth2"
)

type Deps struct {
	DB *pg.DB

	AuthSvc   users.AuthService
	UsersSvc  users.UsersService
	UsersRepo users.UserRepository

	RBACSvc rbac.Service

	OrgansSvc     organs.Service
	DiseasesSvc   diseases.Service
	PlansSvc      plans.Service
	CategoriesSvc diseasecategories.Service
	TrackingSvc   tracking.Service

	// NEW: dashboard users (moderator manage)
	DashboardUsersSvc dashboardusers.Service

	// Platform storage (boards and sections moved from the browser)
	PlatformRepo *pg.PlatformRepo

	// Club: the club data handler (the sheet's final import ends the cutover)
	Club *httpapi.ClubHandler
}

func BuildDeps(db *pg.DB, jwtSecret, accessTTL, refreshTTL string) *Deps {
	// repositories
	userRepo := pg.NewUsersRepo(db)
	rbacRepo := pg.NewRBACRepo(db)

	orgRepo := pg.NewOrgansRepo(db)
	disRepo := pg.NewDiseasesRepo(db)
	plRepo := pg.NewPlansRepo(db)

	catRepo := pg.NewDiseaseCategoriesRepo(db)

	trackingRepo := pg.NewTrackingRepo(db)

	// NEW: dashboard users repo
	dashUsersRepo := pg.NewDashboardUsersRepo(db)

	// services
	catsSvc := diseasecategoriesvc.New(catRepo)
	trackingSvc := trackingsvc.New(trackingRepo)

	usersSvc := usersvc.New(userRepo)
	rbacSvc := rbacsvc.New(rbacRepo)

	orgSvc := organsvc.New(orgRepo)
	disSvc := diseasessvc.New(disRepo)
	plSvc := plansvc.New(plRepo)

	// NEW: dashboard users service
	dashUsersSvc := dashboardusersvc.New(dashUsersRepo)

	// auth
	accessDur, err := time.ParseDuration(accessTTL)
	if err != nil {
		accessDur = 15 * time.Minute
	}
	refreshDur, err := time.ParseDuration(refreshTTL)
	if err != nil {
		refreshDur = 7 * 24 * time.Hour
	}

	jwtManager := auth.NewManager(jwtSecret, accessDur, refreshDur)
	authSvc := authsvc.New(userRepo, rbacRepo, jwtManager)

	return &Deps{
		DB: db,

		UsersRepo: userRepo,

		AuthSvc:  authSvc,
		UsersSvc: usersSvc,
		RBACSvc:  rbacSvc,

		OrgansSvc:     orgSvc,
		DiseasesSvc:   disSvc,
		PlansSvc:      plSvc,
		CategoriesSvc: catsSvc,
		TrackingSvc:   trackingSvc,

		DashboardUsersSvc: dashUsersSvc,

		PlatformRepo: pg.NewPlatformRepo(db),
	}
}

func BuildHTTPModules(
	d *Deps,
	jwtSecret string,
	googleOAuthConfig *oauth2.Config,
	frontendURL string,
) []httpapi.RoutesRegistrar {

	// core
	authHandler := httpapi.NewAuthHandler(d.AuthSvc, googleOAuthConfig, frontendURL)
	organsHandler := httpapi.NewOrgansHandler(d.OrgansSvc)
	diseasesHandler := httpapi.NewDiseasesHandler(d.DiseasesSvc, d.PlansSvc)
	stepsHandler := httpapi.NewStepsHandler(d.PlansSvc)
	plansHandler := httpapi.NewPlansHandler(d.PlansSvc)

	// admin
	adminRBACHandler := httpapi.NewAdminRBACHandler(d.RBACSvc)
	adminUsersHandler := httpapi.NewAdminUsersHandler(d.UsersRepo, d.RBACSvc)

	// users
	userHandler := httpapi.NewUsersHandler(d.UsersSvc)

	// categories
	catsHandler := httpapi.NewDiseaseCategoriesHandler(d.CategoriesSvc)
	adminCatsHandler := httpapi.NewAdminDiseaseCategoriesHandler(d.CategoriesSvc)

	// moderator dashboard (READ)
	modDashHandler := httpapi.NewModeratorDashboardHandler(d.TrackingSvc)
	modUsersHandler := httpapi.NewModeratorUsersHandler(d.TrackingSvc)

	// moderator dashboard (MANAGE users)  ← NEW
	modDashUsersManageHandler :=
		httpapi.NewModeratorDashboardUsersManageHandler(d.DashboardUsersSvc)

	// tracking
	meTrackingHandler := httpapi.NewMeTrackingHandler(d.TrackingSvc)
	adminTrackingH := httpapi.NewAdminTrackingHandler(d.TrackingSvc)

	// steps
	meStepsH := httpapi.NewMeStepsHandler(d.TrackingSvc)

	// diary
	meDiaryHandler := httpapi.NewMeDiaryHandler(d.TrackingSvc)

	return []httpapi.RoutesRegistrar{
		httpapi.NewAuthModule(authHandler, []byte(jwtSecret)),

		httpapi.NewOrgansModule(organsHandler, []byte(jwtSecret)),
		httpapi.NewDiseasesModule(diseasesHandler, []byte(jwtSecret)),
		httpapi.NewStepsModule(stepsHandler, []byte(jwtSecret)),

		httpapi.NewAdminRBACModule(adminRBACHandler, []byte(jwtSecret)),
		httpapi.NewAdminUsersModule(adminUsersHandler, []byte(jwtSecret)),

		httpapi.NewUsersModule(userHandler, []byte(jwtSecret)),

		// public categories
		httpapi.NewDiseaseCategoriesModule(catsHandler),

		// admin categories CRUD
		httpapi.NewAdminDiseaseCategoriesModule(adminCatsHandler, []byte(jwtSecret)),

		httpapi.NewPlansModule(plansHandler),

		// moderator dashboard READ
		httpapi.NewModeratorDashboardModule(modDashHandler, []byte(jwtSecret)),
		httpapi.NewModeratorUsersModule(modUsersHandler, []byte(jwtSecret)),

		// moderator dashboard MANAGE users  ← NEW
		httpapi.NewModeratorDashboardUsersManageModule(
			modDashUsersManageHandler,
			[]byte(jwtSecret),
		),

		httpapi.NewMeTrackingModule(meTrackingHandler, []byte(jwtSecret)),
		httpapi.NewAdminTrackingModule(adminTrackingH, []byte(jwtSecret)),
		httpapi.NewMeStepsModule(meStepsH, []byte(jwtSecret)),

		httpapi.NewMeDiaryModule(meDiaryHandler, []byte(jwtSecret)),
	}
}

// BuildClubModule wires the club data moved from the Google Sheet.
func BuildClubModule(d *Deps, jwtSecret, telegramBotToken, staticSeed string) httpapi.RoutesRegistrar {
	h := httpapi.NewClubHandler(pg.NewClubRepo(d.DB), d.PlatformRepo, telegramBotToken, jwtSecret)
	h.StaticSeed = staticSeed
	d.Club = h
	return httpapi.NewClubModule(h)
}

// BuildPlatformModule wires the BS platform: its storage API and Telegram login.
func BuildPlatformModule(d *Deps, jwtSecret, telegramBotToken, team string) *httpapi.PlatformModule {
	m := httpapi.NewPlatformModule(
		httpapi.NewPlatformHandler(d.PlatformRepo),
		httpapi.NewPlatformAuthHandler(telegramBotToken, team, jwtSecret),
		[]byte(jwtSecret),
	)
	m.AI.Ops = pg.NewClubRepo(d.DB)
	if d.PlatformRepo != nil {
		// The voice guide: every tour phrase made and kept before anyone opens the tour.
		m.AI.TourTexts = web.TourTexts
		m.AI.StaticVoice = web.StaticVoice
		// R32c: recorded phrases ship with the binary (web/voice); the server
		// synthesises only a phrase without a file, so the quota is not spent.
		go m.AI.TourVoiceLoop(context.Background(), web.TourTextsUnvoiced)
	}
	if m.AI.Premium != nil {
		// R36: the owner's ElevenLabs voice replaces the built-in files once every phrase is read
		web.VoiceOverlay = m.AI.Premium.Overlay
		// R40d: the login demo is read with the same voice, a bit faster
		web.LoginVoiceOverlay = m.AI.Premium.LoginOverlay
		m.AI.Premium.SetLoginTexts(web.LoginLines)
		// R52: the tour's phrases are known only now: the map of a voice that
		// was ready before this start is looked for again (Load ran without them)
		m.AI.Premium.Kick()
	}
	return m
}

// WireCalls: записи разборов. Готовые итоги уходят владельцу (первый id
// PLATFORM_TEAM) и резиденту через бота, прерванные перезапуском задачи
// сервер продолжает сам.
func WireCalls(pm *httpapi.PlatformModule, botSvc *bot.Service, team string) {
	if pm == nil || pm.AI == nil {
		return
	}
	pm.AI.Owner = httpapi.FirstTeamID(team)
	if botSvc != nil && botSvc.Enabled() {
		pm.AI.Notify = botSvc.SendMessage
		// R32e: the published «Саммари разбора» PDF goes to the resident as a file
		pm.AI.SendDoc = func(ctx context.Context, chatID int64, name string, data []byte, caption string) error {
			key := "callsum:" + strconv.FormatInt(chatID, 10) + ":" + strconv.FormatInt(time.Now().UnixNano(), 36) // never a cached file_id of another PDF
			return botSvc.SendDocumentKB(ctx, chatID, key, name, data, "", caption, nil)
		}
		// Рекомендации ИИ: кардинальное решает владелец кнопками в боте (ai_recs_auto.go)
		pm.AI.RecsBot = httpapi.RecsBot{Send: botSvc.SendMessageID, Edit: botSvc.EditMessageKB, Platform: httpapi.ContentPlatformURL()}
		botSvc.SetTeamCallbackHook("airec_", pm.AI.HandleRecCallback)
		// R27: the lead home books разбор: the confirmation goes to the lead, a note to the team
		var admins []int64
		for id := range httpapi.ParsePlatformTeam(team) {
			admins = append(admins, id)
		}
		pm.WireLeadBot(botSvc.SendMessageKB, admins)
	}
	go pm.AI.ResumeCalls(context.Background(), 45*time.Second, 2*time.Minute, 3*time.Minute)
	go pm.AI.CallSumLoop(context.Background(), 5*time.Minute)                  // R32e: quiet-hours sends, auto-publish
	go pm.AI.CallRecLoop(context.Background(), 90*time.Second, 6*time.Hour)    // R65: записи разборов не храним после саммари
	go pm.AI.GallupAuditLoop(context.Background(), 2*time.Minute, 6*time.Hour) // R68: чей Gallup собран не по своему отчёту
}

// contentEngine: the wired engine, for the /status line (syscheck_wiring.go).
var contentEngine *httpapi.ContentEngine

// BuildContent wires the content engine (bs_content): planning, the owner's
// morning preview in the bot, publishing to Threads and the Telegram channel.
func BuildContent(d *Deps, pm *httpapi.PlatformModule, botSvc *bot.Service, jwtSecret, team string) httpapi.RoutesRegistrar {
	if d.PlatformRepo == nil {
		return httpapi.NewContentModule(httpapi.NewContentEngine(nil), []byte(jwtSecret))
	}
	e := httpapi.NewContentEngine(d.PlatformRepo)
	e.Owner = httpapi.FirstTeamID(team)
	contentEngine = e
	// R43: no THREADS_TOKEN: the bot sends the owner each post to publish by hand
	e.ThreadsManual = func(context.Context) bool { return true }
	if pm != nil && pm.AI != nil {
		e.ThreadsManual = func(ctx context.Context) bool { return !pm.AI.HasThreadsToken(ctx) }
		e.Threads = pm.AI.PublishThreadsText
		e.ThreadsReply = pm.AI.PublishThreadsReply
		if pm.AI.AI != nil {
			e.AI = httpapi.ThreadsAI(pm.AI.AI) // the daily Threads batch (content_threads.go), R42: within the free budget
		}
		pm.AI.SetQueueOwnsThreads(e.OwnsThreadsDay)
	}
	if botSvc != nil && botSvc.Enabled() {
		e.Channel, e.Send, e.Edit = botSvc.SendChannel, botSvc.SendMessageID, botSvc.EditMessageKB
		botSvc.SetTeamCallbackHook("cnt_", e.HandleCallback)
	}
	if os.Getenv("CONTENT_ENGINE") != "off" {
		go e.Loop(context.Background())
	}
	return httpapi.NewContentModule(e, []byte(jwtSecret))
}

// BuildBot wires the Telegram webhook on the server and its relay to the
// Apps Script bot. Start the returned service with a context that lives as
// long as the server.
func BuildBot(d *Deps, token, team, apiBase, publicURL, notify string) (*bot.Service, httpapi.RoutesRegistrar) {
	var admins []int64
	for id := range httpapi.ParsePlatformTeam(team) {
		admins = append(admins, id)
	}
	// The daily comparison goes to the owner only, unless BOT_SHADOW_NOTIFY says otherwise.
	if strings.TrimSpace(notify) == "" {
		notify = "453800951"
	}
	var who []int64
	for id := range httpapi.ParsePlatformTeam(notify) {
		who = append(who, id)
	}
	if strings.TrimSpace(publicURL) == "" {
		if dom := strings.TrimSpace(os.Getenv("RAILWAY_PUBLIC_DOMAIN")); dom != "" {
			publicURL = "https://" + dom // the webhook the server keeps after the cutover
		}
	}
	svc := bot.New(pg.NewBotRepo(d.DB), bot.Options{Token: token, APIBase: apiBase, Admins: admins, Notify: who,
		TestClock: os.Getenv("BOT_TEST_CLOCK") == "1", PublicURL: publicURL})
	svc.SetPlatformURL(httpapi.ContentPlatformURL())
	log.Printf("sheet mode: %s", club.SheetMode())
	return svc, httpapi.NewBotModule(httpapi.NewBotHandler(svc, publicURL))
}

// BuildAppGateway: the Telegram app's calls go through the server.
func BuildAppGateway(d *Deps, token, jwtSecret, staticSeed string, botSvc *bot.Service) []httpapi.RoutesRegistrar {
	g := httpapi.NewAppGateway(token, os.Getenv("APP_SCRIPT_URL"))
	appGW = g // R51: sales_wiring.go
	g.Admins = httpapi.ParsePlatformTeam(os.Getenv("PLATFORM_TEAM"))
	if botSvc != nil {
		// A club write the app's deployment of the script does not know yet
		// goes to the bot's deployment, which the self-update moves first.
		g.Fallback = botSvc.RelayURL
	}
	if d.PlatformRepo != nil {
		g.Boards = d.PlatformRepo
		g.Library = d.PlatformRepo
		g.Sync = d.PlatformRepo
	}
	g.Done = pg.NewClubRepo(d.DB)
	g.Ops = pg.NewClubRepo(d.DB)
	if d.PlatformRepo != nil && botSvc != nil && botSvc.Enabled() {
		var admins []int64
		for id := range g.Admins {
			admins = append(admins, id)
		}
		f := httpapi.NewLeadFunnel(d.PlatformRepo, botSvc.SendMessageKB, admins)
		g.Funnel = f
		f.Photo, f.Doc = botSvc.SendPhotoKB, botSvc.SendDocumentKB
		f.Edit, f.Meta = botSvc.EditMessageKB, pg.NewBotRepo(d.DB) // R40b: the in-chat checklist, the autoreply health
		f.RecordDialog()                                           // R47: every bot message to a lead goes to its dialog (CRM card «Бот»)
		if os.Getenv("LEAD_FUNNEL") != "off" {
			botSvc.SetStartHook(f.StartHook(d.PlatformRepo, g.Admins)) // R40b: WithReferrals + the safety net's «handled»
			botSvc.SetCallbackHook(f.HandleCallback)
			botSvc.SetLeadTextHook(f.HandleLeadText) // R40b: a lead's message is answered, not dropped
			botSvc.SetInboundHook(f.NoteInbound)     // R40b: every lead message on record before the answer
			go f.SafetyLoop(context.Background())    // R40b: no answer in 2 minutes: the standard welcome once
			go func() {
				ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
				defer cancel()
				botSvc.EnsureMenuButton(ctx)
			}()
			go f.WarmLoop(context.Background())
		}
		go f.RefWonLoop(context.Background()) // referral.go: a referral became a resident
		go f.RemindLoop(context.Background()) // booking.go: разбор reminders, meet → diag
	}
	g.Avatars = pg.NewClubRepo(d.DB)
	repo := pg.NewBotRepo(d.DB)
	var once sync.Once
	g.OnOK = func() {
		once.Do(func() {
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			if v, _ := repo.GetMeta(ctx, bot.MetaAppFirstOK); v == "" {
				_ = repo.SetMeta(ctx, bot.MetaAppFirstOK, time.Now().UTC().Format(time.RFC3339))
			}
		})
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = repo.SetMeta(ctx, bot.MetaAppLastOK, time.Now().UTC().Format(time.RFC3339))
	}
	// Moving the club off the sheet, step 2: club writes go to the server
	// first, and the app's bundle moves to the server once it matches.
	clubRepo := pg.NewClubRepo(d.DB)
	g.Club = clubRepo
	writes := httpapi.NewClubWrites(clubRepo, g)
	g.Writes = writes
	// R32: the server is the only source of truth (SHEET_MODE off|mirror):
	// the one-time switch, and the bot doing what the script did for a write.
	cut := httpapi.NewSheetCutover(clubRepo, repo)
	writes.Cutover = cut
	if d.Club != nil {
		d.Club.Cutover = cut
	}
	if botSvc != nil && botSvc.Enabled() {
		var admins []int64
		for id := range g.Admins {
			admins = append(admins, id)
		}
		writes.Notify = &httpapi.WriteNotify{Send: botSvc.SendMessageKB, Topic: botSvc.SendTopic, Admins: admins,
			Resident: botSvc.SendResident} // R38c: WhatsApp for the residents who chose it
		g.Contact, g.Photo = botSvc.RequestContact, botSvc.SendPhotoKB
		botSvc.SetFineSink(func(ctx context.Context, fines []bot.FineRow) ([]string, error) {
			var added []string
			for _, f := range fines {
				day, ok := club.Date(f.Date)
				if !ok {
					continue
				}
				if have, err := clubRepo.FineExists(ctx, f.Name, f.Type, day); err != nil {
					return added, err
				} else if have {
					continue
				}
				p := map[string]string{"name": f.Name, "type": f.Type, "amount": strconv.FormatInt(f.Amount, 10), "date": f.Date}
				if _, err := writes.Local(ctx, "bot", 0, "Бот: проверка отчётов", "addFine", p); err != nil {
					return added, err
				}
				added = append(added, f.Name)
			}
			return added, nil
		})
		botSvc.SetNoteSink(func(ctx context.Context, tg int64, who, action string, params map[string]string) error {
			_, err := writes.Local(ctx, "bot", tg, who, action, params)
			return err
		})
	}
	// R27: «Я резидент BS»: the server checks the residents list itself, links
	// a found resident, warms a lead, and asks the team only in doubtful
	// cases and only in the daytime (resident_claim.go).
	if g.Funnel != nil && botSvc != nil && botSvc.Enabled() {
		claims := httpapi.NewResidentClaims(g.Funnel, clubRepo)
		claims.Names = g.Admins
		claims.Edit = botSvc.EditMessageKB
		owner := httpapi.FirstTeamID(os.Getenv("PLATFORM_TEAM"))
		claims.Link = func(ctx context.Context, name string, tg int64) error {
			return g.LinkResidentTg(ctx, clubRepo, owner, name, tg)
		}
		g.Claims = claims
		if os.Getenv("LEAD_FUNNEL") != "off" {
			botSvc.SetClaimHook(claims.LeadCallback)
			botSvc.SetTeamCallbackHook("rcl_", claims.TeamCallback)
			go claims.Loop(context.Background())
		}
	}
	var seedMu sync.Mutex
	writes.Tables = func() { // the platform's club sections show the change at once
		seedMu.Lock()
		defer seedMu.Unlock()
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		if err := httpapi.RefreshPlatformSeed(ctx, clubRepo, d.PlatformRepo, staticSeed); err != nil {
			log.Printf("platform seed: %v", err)
		}
	}
	// R63: payments entered before R59 pay off the debt by themselves (Альтаир 07.10 and the others)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancel()
		httpapi.DebtFixAtStart(ctx, clubRepo, repo, writes.Tables)
	}()
	var docs interface {
		PutServerDoc(ctx context.Context, key, value string) error
	}
	if d.PlatformRepo != nil {
		docs = d.PlatformRepo
	}
	mig := httpapi.NewBundleMigration(g, clubRepo, repo, docs, writes)
	mig.Sample = parseBundleSample(os.Getenv("BUNDLE_SAMPLE"))
	if botSvc != nil && botSvc.Enabled() {
		if ids := botSvc.NotifyIDs(); len(ids) > 0 {
			mig.Owner = ids[0]
		}
		mig.Notify = func(ctx context.Context, text string) {
			for _, id := range botSvc.NotifyIDs() {
				if err := botSvc.SendMessage(ctx, id, text); err != nil {
					log.Printf("migration notify %d: %v", id, err)
				}
			}
		}
	}
	cut.OnDone = func() {
		g.Reset()
		if writes.Tables != nil {
			writes.Tables()
		}
	}
	go cut.Loop(context.Background())
	go writes.Loop(context.Background())
	go mig.Loop(context.Background())
	// The data audit after the script v31 slips (bs_data_audit), after every import.
	audit := httpapi.NewClubAudit(clubRepo, docs, g)
	go audit.Loop(context.Background())

	calMod := wireCalendar(g, writes, clubRepo, repo, botSvc)                               // R67 (calendar_wiring.go)
	sheetMod := wireSheetOwner(d, g, writes, clubRepo, repo, cut, token, jwtSecret, botSvc) // R32d (sheet_wiring.go)
	action := httpapi.NewClubActionHandler(g, clubRepo, d.PlatformRepo, staticSeed)
	// Заявки с сайта (Tilda) прямо на сервер: /api/v1/public/tilda/<ключ> (tilda.go)
	tilda := httpapi.NewTildaLeads(writes, nil, repo, token)
	if d.PlatformRepo != nil {
		tilda.Docs = d.PlatformRepo
	}
	g.OnLead = tilda.ScriptLead
	return []httpapi.RoutesRegistrar{httpapi.NewAppGatewayModule(g), httpapi.NewTildaModule(tilda, []byte(jwtSecret)),
		httpapi.NewClubActionModule(action, []byte(jwtSecret)),
		httpapi.NewMigrationModule(mig, []byte(jwtSecret)), httpapi.NewClubAuditModule(audit, []byte(jwtSecret)),
		httpapi.NewClubResidentModule(action, []byte(jwtSecret)), sheetMod, calMod}
}

// parseBundleSample reads "admin:453800951,resident:490685605,lead:999".
func parseBundleSample(v string) []httpapi.SampleUser {
	var out []httpapi.SampleUser
	for _, part := range strings.Split(v, ",") {
		role, id, ok := strings.Cut(strings.TrimSpace(part), ":")
		if !ok {
			continue
		}
		if n, err := strconv.ParseInt(strings.TrimSpace(id), 10, 64); err == nil && n > 0 {
			out = append(out, httpapi.SampleUser{Role: strings.TrimSpace(role), TgID: n})
		}
	}
	return out
}
