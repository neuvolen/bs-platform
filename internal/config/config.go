package config

import "os"

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
	}
}
