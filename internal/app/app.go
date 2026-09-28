package app

import (
	"time"

	"github.com/bnursik/business_surgery_backend/internal/domain/diseasecategories"
	"github.com/bnursik/business_surgery_backend/internal/domain/diseases"
	"github.com/bnursik/business_surgery_backend/internal/domain/organs"
	"github.com/bnursik/business_surgery_backend/internal/domain/plans"
	"github.com/bnursik/business_surgery_backend/internal/domain/rbac"
	"github.com/bnursik/business_surgery_backend/internal/domain/tracking"
	"github.com/bnursik/business_surgery_backend/internal/domain/users"
	"github.com/bnursik/business_surgery_backend/internal/domain/dashboardusers"

	httpapi "github.com/bnursik/business_surgery_backend/internal/handlers/http"
	"github.com/bnursik/business_surgery_backend/internal/repository/pg"

	authsvc "github.com/bnursik/business_surgery_backend/internal/services/auth"
	diseasecategoriesvc "github.com/bnursik/business_surgery_backend/internal/services/diseasecategories"
	diseasessvc "github.com/bnursik/business_surgery_backend/internal/services/diseases"
	organsvc "github.com/bnursik/business_surgery_backend/internal/services/organs"
	plansvc "github.com/bnursik/business_surgery_backend/internal/services/plans"
	rbacsvc "github.com/bnursik/business_surgery_backend/internal/services/rbac"
	trackingsvc "github.com/bnursik/business_surgery_backend/internal/services/tracking"
	usersvc "github.com/bnursik/business_surgery_backend/internal/services/users"
	dashboardusersvc "github.com/bnursik/business_surgery_backend/internal/services/dashboardusers"

	"github.com/bnursik/business_surgery_backend/pkg/auth"
	"golang.org/x/oauth2"
)

type Deps struct {
	DB *pg.DB

	AuthSvc  users.AuthService
	UsersSvc users.UsersService
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

	// platform storage
	platformHandler := httpapi.NewPlatformHandler(d.PlatformRepo)

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

		httpapi.NewPlatformModule(platformHandler, []byte(jwtSecret)),
	}
}
