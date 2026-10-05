package http

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/bnursik/business_surgery_backend/internal/ai"
	"github.com/bnursik/business_surgery_backend/internal/club"
)

// R42: «ИИ: кто отвечает»: every provider of the text chain (Claude, Gemini,
// Groq, OpenRouter): работает / нет ключа / лимит до HH:MM / нет баланса,
// requests used today against the budget, and the one answering now. The
// free providers with a key get a tiny live call (kept 10 minutes).

// ProviderLine: one provider in words, e.g. «Gemini: работает, 12 из 1000 сегодня».
func ProviderLine(p ai.ProviderState) string {
	t := p.Label + ": "
	switch p.State {
	case "ok":
		t += "работает"
	case "none":
		return t + "нет ключа (" + p.Env + ")"
	case "off":
		return t + "выключен (GEMINI_ENABLED=0)"
	case "billing":
		t += "нет баланса"
		if p.Until != "" {
			t += ", проверю снова в " + untilHM(p.Until)
		}
	case "quota", "budget":
		t += "лимит до " + untilHM(p.Until)
	default:
		t += p.State
	}
	if p.Budget > 0 {
		t += fmt.Sprintf(", %d из %d сегодня", p.Used, p.Budget)
	} else if p.Used > 0 {
		t += fmt.Sprintf(", %d запросов сегодня", p.Used)
	}
	if p.Active {
		t += " (отвечает сейчас)"
	}
	return t
}

// untilHM: an RFC3339 time as «HH:MM» in Almaty (with the date when not today).
func untilHM(s string) string {
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return "?"
	}
	a := t.In(club.Almaty)
	if a.Format("2006-01-02") != time.Now().In(club.Almaty).Format("2006-01-02") {
		return a.Format("02.01 15:04")
	}
	return a.Format("15:04")
}

func (s *SysCheck) chain(ctx context.Context) CheckItem {
	it := CheckItem{Key: "aichain", Title: "ИИ: кто отвечает"}
	if s.AI == nil {
		it.State, it.Text = "off", "нет данных"
		return it
	}
	c, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	// a live call to every free provider with a key (the budget allows it)
	for _, p := range s.AI.ProviderStates() {
		if p.Free && p.State == "ok" {
			_, _ = s.AI.PingProvider(c, p.Name)
		}
	}
	states := s.AI.ProviderStates()
	var lines []string
	active := ""
	for _, p := range states {
		lines = append(lines, ProviderLine(p))
		if p.Active {
			active = p.Name
		}
	}
	it.Text = strings.Join(lines, " · ")
	switch {
	case active == "":
		it.State, it.Sig = "fail", "none"
		it.Text = "никто не отвечает: " + it.Text
		it.Note = "❌ ИИ не отвечает: нет ключей или все бесплатные лимиты исчерпаны. Бесплатный ключ: GEMINI_API_KEY из aistudio.google.com"
	case active == "claude":
		it.State, it.Sig = "ok", "claude"
	default:
		it.State, it.Sig = "ok", active
		it.Note = "🤖 ИИ отвечает через " + ai.ProviderLabel(active) + " (бесплатно)"
	}
	return it
}
