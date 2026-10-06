package app

import (
	"context"
	"os"
	"time"

	"github.com/bnursik/business_surgery_backend/internal/bot"
	httpapi "github.com/bnursik/business_surgery_backend/internal/handlers/http"
)

// BuildTrends wires «Тренды Threads» (R53): after BuildContent (the plan it
// adds to) and before WireSales (the Monday report's line).
func BuildTrends(d *Deps, pm *httpapi.PlatformModule, botSvc *bot.Service, jwtSecret string) httpapi.RoutesRegistrar {
	if d.PlatformRepo == nil {
		return httpapi.NewTrends(nil, []byte(jwtSecret))
	}
	t := httpapi.NewTrends(d.PlatformRepo, []byte(jwtSecret))
	t.Content = contentEngine
	if pm != nil && pm.AI != nil && pm.AI.AI != nil {
		c := pm.AI.AI
		t.AI = httpapi.ThreadsAI(c) // within the free budget, as the daily batch
		t.Search = c.Search         // Claude web search, Gemini Google Search grounding
	}
	if botSvc != nil && botSvc.Enabled() {
		botSvc.SetTrendHook(t.BotLinks)
	}
	httpapi.WeeklyExtra = append(httpapi.WeeklyExtra, t.WeeklyLine)
	if os.Getenv("TRENDS_SELFTEST") != "off" {
		go func() {
			time.Sleep(90 * time.Second)
			ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
			defer cancel()
			t.SelfTest(ctx)
		}()
	}
	return t
}
