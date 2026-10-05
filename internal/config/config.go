package config

import (
	"os"

	"github.com/bnursik/business_surgery_backend/internal/middleware"
)

type Config struct {
	Port       string
	DSN        string
	JWTSecret  string
	AccessTTL  string
	RefreshTTL string

	GoogleClientID     string
	GoogleClientSecret string
	GoogleRedirectURL  string
	FrontendURL        string

	// Platform login with Telegram
	TelegramBotToken string
	PlatformTeam     string // "id:Name,id:Name" of team members allowed in

	PublicURL       string // https://host the Telegram webhook is set to; empty: the host the sheet called
	TelegramAPIBase string // tests only
	BotRelayPattern string // tests only: which relay addresses are allowed
	BotShadowNotify string // Telegram ids for the daily server-bot comparison
}

func Load() Config {
	return Config{
		Port:               os.Getenv("PORT"),
		DSN:                os.Getenv("DB_DSN"),
		JWTSecret:          os.Getenv("JWT_SECRET"),
		AccessTTL:          os.Getenv("JWT_ACCESS_TTL"),
		RefreshTTL:         os.Getenv("JWT_REFRESH_TTL"),
		GoogleClientID:     os.Getenv("GOOGLE_CLIENT_ID"),
		GoogleClientSecret: os.Getenv("GOOGLE_CLIENT_SECRET"),
		GoogleRedirectURL:  os.Getenv("GOOGLE_REDIRECT_URL"),
		FrontendURL:        os.Getenv("FRONTEND_URL"),

		TelegramBotToken: os.Getenv("TELEGRAM_BOT_TOKEN"),
		PlatformTeam:     os.Getenv("PLATFORM_TEAM"),
		PublicURL:        middleware.HTTPSURL(os.Getenv("PUBLIC_URL")), // R38a: links and the webhook always on https
		TelegramAPIBase:  os.Getenv("TELEGRAM_API_BASE"),
		BotRelayPattern:  os.Getenv("BOT_RELAY_PATTERN"),
		BotShadowNotify:  os.Getenv("BOT_SHADOW_NOTIFY"),
	}
}
