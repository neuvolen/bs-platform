// @title Business Surgery Backend API
// @version 1.0
// @description API for Business Surgery service.
//
// @accept json
// @produce json
//
// @securityDefinitions.apikey BearerAuth
// @in header
// @name Authorization
package main

import (
	"context"
	"log"
	"net/http"
	"os"
	"os/signal"
	"regexp"
	"syscall"
	"time"

	_ "github.com/bnursik/business_surgery_backend/docs"
	"github.com/joho/godotenv"
	swaggerFiles "github.com/swaggo/files"
	ginSwagger "github.com/swaggo/gin-swagger"

	"github.com/bnursik/business_surgery_backend/internal/app"
	"github.com/bnursik/business_surgery_backend/internal/bot"
	"github.com/bnursik/business_surgery_backend/internal/config"
	httpapi "github.com/bnursik/business_surgery_backend/internal/handlers/http"
	pg "github.com/bnursik/business_surgery_backend/internal/repository/pg"
	"github.com/bnursik/business_surgery_backend/internal/server"
	"github.com/bnursik/business_surgery_backend/migrations"
	"github.com/bnursik/business_surgery_backend/web"
	"golang.org/x/oauth2"
	"golang.org/x/oauth2/google"
)

func main() {
	_ = godotenv.Load()

	cfg := config.Load()
	if cfg.Port == "" || cfg.DSN == "" || cfg.JWTSecret == "" || cfg.AccessTTL == "" || cfg.RefreshTTL == "" {
		log.Fatal("PORT, DATABASE_DSN, JWT_SECRET, JWT_ACCESS_TTL, JWT_REFRESH_TTL must be set")
	}

	var googleOAuthConfig *oauth2.Config
	if cfg.GoogleClientID != "" && cfg.GoogleClientSecret != "" && cfg.GoogleRedirectURL != "" {
		googleOAuthConfig = &oauth2.Config{
			ClientID:     cfg.GoogleClientID,
			ClientSecret: cfg.GoogleClientSecret,
			RedirectURL:  cfg.GoogleRedirectURL,
			Scopes:       []string{"openid", "email", "profile"},
			Endpoint:     google.Endpoint,
		}
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	db, err := pg.NewDB(ctx, cfg.DSN)
	if err != nil {
		log.Fatalf("db init failed: %v", err)
	}
	defer db.Pool.Close()

	// Bring the schema up to date before serving. A failed migration must not
	// take the whole API down: existing endpoints keep working on the schema
	// they already have, and the error is in the logs.
	migCtx, migCancel := context.WithTimeout(context.Background(), 2*time.Minute)
	if err := pg.Migrate(migCtx, db, migrations.FS); err != nil {
		log.Printf("MIGRATIONS FAILED, serving with the current schema: %v", err)
	}
	migCancel()

	deps := app.BuildDeps(db, cfg.JWTSecret, cfg.AccessTTL, cfg.RefreshTTL)
	modules := app.BuildHTTPModules(deps, cfg.JWTSecret, googleOAuthConfig, cfg.FrontendURL)
	platformMod := app.BuildPlatformModule(deps, cfg.JWTSecret, cfg.TelegramBotToken, cfg.PlatformTeam)
	modules = append(modules, platformMod)
	modules = append(modules, app.BuildClubModule(deps, cfg.JWTSecret, cfg.TelegramBotToken, web.Seed()))
	if cfg.BotRelayPattern != "" {
		bot.RelayURLPattern = regexp.MustCompile(cfg.BotRelayPattern)
	}
	botSvc, botModule := app.BuildBot(deps, cfg.TelegramBotToken, cfg.PlatformTeam, cfg.TelegramAPIBase, cfg.PublicURL, cfg.BotShadowNotify)
	modules = append(modules, botModule)
	app.WireCalls(platformMod, botSvc, cfg.PlatformTeam)
	modules = append(modules, app.BuildContent(deps, platformMod, botSvc, cfg.JWTSecret, cfg.PlatformTeam))
	modules = append(modules, app.BuildAppGateway(deps, cfg.TelegramBotToken, cfg.JWTSecret, web.Seed(), botSvc)...)
	botCtx, botStop := context.WithCancel(context.Background())
	defer botStop()
	if botSvc.Enabled() {
		botSvc.Start(botCtx)
	}
	router := server.SetupRouter(modules...)
	web.Register(router, cfg.JWTSecret, httpapi.PlatformSessionCookie)

	// Business data cut out of the page goes to storage, visible after login only.
	seedCtx, seedCancel := context.WithTimeout(context.Background(), 20*time.Second)
	if err := httpapi.RefreshPlatformSeed(seedCtx, pg.NewClubRepo(db), deps.PlatformRepo, web.Seed()); err != nil {
		log.Printf("platform seed not saved: %v", err)
	}
	seedCancel()
	router.GET("/swagger/*any", ginSwagger.WrapHandler(swaggerFiles.Handler))

	srv := &http.Server{
		Addr:         ":" + cfg.Port,
		Handler:      router,
		ReadTimeout:  60 * time.Second,
		WriteTimeout: 60 * time.Second,
		IdleTimeout:  60 * time.Second,
	}

	go func() {
		log.Printf("HTTP listening on :%s", cfg.Port)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("listen failed: %v", err)
		}
	}()

	// wait for termination signal
	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGINT, syscall.SIGTERM)
	<-stop

	shCtx, cancel2 := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel2()
	if err := srv.Shutdown(shCtx); err != nil {
		log.Printf("server shutdown error: %v", err)
	}
}
