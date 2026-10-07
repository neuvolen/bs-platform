package http

import (
	"context"
	"log"
	"strings"
	"sync"
	"time"

	"github.com/bnursik/business_surgery_backend/internal/ai"
)

// R32c: when the AI's quota runs out for long (R34a: Claude's balance; with
// GEMINI_ENABLED=1 also Gemini's daily limit), the owner hears it from the
// bot, at most once a day. The day of
// the last message is kept in the club doc bs_ai_quota_alert, so a restart
// does not repeat it.

const aiQuotaAlertDoc = "bs_ai_quota_alert"

var aiQuotaAlert struct {
	sync.Mutex
	day string
}

// quotaAlertNow is replaced in tests.
var quotaAlertNow = time.Now

func (h *PlatformAI) quotaAlert(q *ai.QuotaError) {
	if h == nil || h.Notify == nil || h.Owner == 0 || q == nil {
		return
	}
	// R42: another provider answers: the owner hears the switch (switchAlert), not a pause
	if q.Service != "tts" && h.AI != nil && otherProvider(h.AI, q.Service) {
		return
	}
	day := quotaAlertNow().In(almaty).Format("2006-01-02")
	aiQuotaAlert.Lock()
	defer aiQuotaAlert.Unlock()
	if aiQuotaAlert.day == day {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if h.repo != nil {
		if d, err := h.repo.GetDoc(ctx, "club", aiQuotaAlertDoc); err == nil && d != nil && strings.Trim(d.Value, `"`) == day {
			aiQuotaAlert.day = day
			return
		}
	}
	if err := h.Notify(ctx, h.Owner, quotaAlertText(h.AI, q)); err != nil {
		log.Printf("ai quota alert: %v", err)
		return
	}
	aiQuotaAlert.day = day
	if h.repo != nil {
		_ = h.repo.PutServerDoc(ctx, aiQuotaAlertDoc, `"`+day+`"`)
	}
}

func quotaAlertText(c *ai.Client, q *ai.QuotaError) string {
	svc := "ИИ"
	if q.Service == "tts" {
		svc = "озвучки"
	}
	if q.Service == "gemini" && q.Billing {
		// R56: Google named no per-minute or per-day limit (or a limit of 0)
		return "⚠️ " + ai.GeminiNoFreeMessage + ".\nGemini снова попробует " + strings.TrimPrefix(q.When(), "до ")
	}
	if q.Service == "gemini" {
		if c != nil && c.HasClaude() {
			return "⚠️ Закончилась квота Gemini (" + q.When() + "). Тексты и поиск идут через Claude, как обычно"
		}
		return "⚠️ " + ai.GeminiQuotaMessage + ".\nGemini снова попробует " + strings.TrimPrefix(q.When(), "до ")
	}
	if q.Service == "tts" {
		return "⚠️ Закончилась квота Gemini для " + svc + " (" + q.When() + "). Голос обучения не пострадает: его записи лежат в приложении"
	}
	return "⚠️ " + ai.QuotaMessage + ".\nClaude снова попробует " + strings.TrimPrefix(q.When(), "до ") + " или сразу после того, как новый ключ сохранён в Настройках платформы"
}

// otherProvider: some provider other than svc has a key for text tasks.
func otherProvider(c *ai.Client, svc string) bool {
	for _, m := range c.TextModels() {
		if m != svc && !(svc == "gemini-lite" && m == "gemini") {
			return true
		}
	}
	return false
}

// R42: the owner hears once when another provider starts answering text
// tasks (Claude ran out of balance → Gemini; Gemini's day is over → Groq;
// Claude is back). The last provider told is kept in the club doc
// bs_ai_active, so a restart or a deploy does not repeat it.

const aiActiveDoc = "bs_ai_active"

var aiActive struct {
	sync.Mutex
	name string
}

func (h *PlatformAI) switchAlert(from, to string) {
	if h == nil || to == "" {
		return
	}
	aiActive.Lock()
	defer aiActive.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if aiActive.name == "" && h.repo != nil {
		if d, err := h.repo.GetDoc(ctx, "club", aiActiveDoc); err == nil && d != nil && !d.Deleted {
			aiActive.name = strings.Trim(d.Value, `"`)
		}
	}
	if aiActive.name == to {
		return
	}
	prev := aiActive.name
	if prev == "" && to == "claude" {
		// the usual state at the first start: nothing to tell
		aiActive.name = to
		if h.repo != nil {
			_ = h.repo.PutServerDoc(ctx, aiActiveDoc, `"`+to+`"`)
		}
		return
	}
	if h.Notify != nil && h.Owner != 0 {
		if err := h.Notify(ctx, h.Owner, switchAlertText(h.AI, prev, to)); err != nil {
			log.Printf("ai switch alert: %v", err)
			return
		}
	}
	log.Printf("ai: text tasks now answered by %s (before: %s)", to, orDash(prev))
	aiActive.name = to
	if h.repo != nil {
		_ = h.repo.PutServerDoc(ctx, aiActiveDoc, `"`+to+`"`)
	}
}

func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

func switchAlertText(c *ai.Client, from, to string) string {
	name := ai.ProviderLabel(to)
	if to == "claude" {
		return "✅ ИИ снова работает через Claude"
	}
	t := "🤖 ИИ теперь отвечает через " + name + " (бесплатный лимит)"
	if c != nil {
		if why := c.SwitchReason(to); why != "" {
			t += ".\nПричина: " + why
		}
	}
	return t + ".\nВсё работает как обычно. Состояние: Настройки платформы → «Состояние ИИ» или /status"
}
