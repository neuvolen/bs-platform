package http

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
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
	case "badkey":
		t += "ключ не принят"
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

// claudeShort: why Claude is not used, in two or three words.
func claudeShort(err error) string {
	var he *ai.HTTPError
	switch {
	case ai.IsQuota(err):
		return "без баланса"
	case ai.IsKeyRejected(err), errors.As(err, &he) && (he.Status == 401 || he.Status == 403):
		return "ключ не принят"
	}
	return "не отвечает"
}

// aiBlock: R51: the three AI lines (who answers, Claude, the web search) as
// one calm block. «✅ ИИ: отвечает Gemini (бесплатно) · Claude без баланса:
// не используется · поиск в интернете работает». A provider that is not
// needed is an info part, not ⚠️; a problem has its one action; the
// provider lines and the APIs' own answers go to /status подробно.
func (s *SysCheck) aiBlock(ctx context.Context) CheckItem {
	it := CheckItem{Key: "ai", Title: "ИИ"}
	// the free providers' live pings first: they decide who answers
	ch := s.chain(ctx)
	var cl, sr CheckItem
	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); cl = s.claude(ctx) }()
	go func() { defer wg.Done(); sr = s.search(ctx) }()
	wg.Wait()
	active := ""
	var bad []ai.ProviderState
	if s.AI != nil {
		for _, p := range s.AI.ProviderStates() {
			if p.Active {
				active = p.Name
			}
			if p.State == "badkey" {
				bad = append(bad, p)
			}
		}
	}
	var parts []string
	switch {
	case active == "":
		it.State = "fail"
		parts = append(parts, "никто не отвечает")
		it.Problem, it.Action = cl.Problem, cl.Action
		if it.Problem == "" {
			it.Problem = "не отвечает: нет ключей или все бесплатные лимиты исчерпаны"
			it.Action = "Добавьте бесплатный GEMINI_API_KEY (aistudio.google.com/api-keys) в Railway"
		}
		for _, p := range bad { // a refused key is the likeliest fix
			it.Problem, it.Action = "не отвечает: "+p.Problem, p.Action
			break
		}
	case active == "claude":
		it.State = "ok"
		parts = append(parts, "отвечает Claude")
	default:
		it.State = "ok"
		parts = append(parts, "отвечает "+ai.ProviderLabel(active)+" (бесплатно)")
		if cl.State == "off" && strings.HasPrefix(cl.Text, "Claude ") {
			parts = append(parts, strings.SplitN(cl.Text, ", отвечает", 2)[0])
		}
	}
	for _, p := range bad {
		if active != "" && sr.Sig != "badkey-"+p.Name { // the search part says it already
			parts = append(parts, p.Label+": ключ не принят")
		}
	}
	switch sr.State {
	case "ok":
		parts = append(parts, "поиск в интернете работает")
	case "off":
		parts = append(parts, "поиска в интернете нет (нужен ключ Claude или Gemini)")
	default:
		parts = append(parts, "поиск в интернете: "+sr.Problem)
		if it.State == "ok" {
			it.State = sr.State
			it.Problem, it.Action = "поиск в интернете: "+sr.Problem, sr.Action
		}
	}
	if it.State == "ok" && len(bad) > 0 {
		// a key that is set but refused: the owner can fix it (the free fallback is lost)
		it.State, it.Problem, it.Action = "warn", bad[0].Label+": "+bad[0].Problem, bad[0].Action
	}
	it.Text = strings.Join(parts, " · ")
	var det []string
	if cl.Detail != "" {
		det = append(det, cl.Detail)
	}
	if ch.Text != "" {
		det = append(det, "Кто отвечает: "+ch.Text)
	}
	if sr.Detail != "" {
		det = append(det, sr.Detail)
	}
	it.Detail = strings.Join(det, "\n")
	it.Sig = ch.Sig + "|" + cl.Sig + "|" + sr.Sig
	for _, p := range bad {
		it.Sig += "|bad-" + p.Name
	}
	return it
}
