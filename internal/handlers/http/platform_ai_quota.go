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
