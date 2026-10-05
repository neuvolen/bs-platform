package http

import (
	"context"
	"fmt"
	"strings"

	"github.com/bnursik/business_surgery_backend/internal/bot"
	"github.com/bnursik/business_surgery_backend/internal/content"
)

// R32e, план маркетинга a2: «выдача чек-листа по боли (операционка, команда,
// финансы, продажи)». После приветствия новый лид выбирает, что болит, и
// сразу получает три чек-листа этого органа и кейс резидента с цифрами.
// Выбор пишется в карточку лида в CRM (поле pain и строка в истории), дальше
// его ведёт прогрев из 5 касаний (lead_funnel.go). Кнопки идут с префиксом
// lm_, его бот уже отдаёт воронке (bot.LeadCallbacks).

type leadPain struct {
	Key, Label, Organ, Case string
}

var leadPains = []leadPain{
	{"ops", "⚙️ Операционка", "Процессы", "у Елены три филиала языкового центра. Операционку забрал операционный директор, собственник вернулся к развитию."},
	{"team", "👥 Команда", "Команда", "у Казбека К9 15-20 млн ₸ чистой прибыли в месяц. Рост упёрся в оргструктуру, её и пересобрали."},
	{"fin", "💰 Финансы", "Финансы", "Исфандияр начинал с 200 000 ₸ чистой прибыли, сейчас 2 млн ₸. Начали с цифр: куда уходят деньги."},
	{"sales", "📈 Продажи", "Продажи", "у Артёма чистая прибыль выросла в 3 раза: сменили аудиторию и подняли средний чек."},
}

const leadPainPrefix = "lm_pain_"

func leadPainBy(key string) *leadPain {
	for i := range leadPains {
		if leadPains[i].Key == key {
			return &leadPains[i]
		}
	}
	return nil
}

// painGuides: the first n guides of an organ (by id, the library's order).
func painGuides(organ string, n int) []map[string]any {
	var out []map[string]any
	for _, g := range content.Guides() {
		if o, _ := g["organ"].(string); o == organ {
			out = append(out, g)
			if len(out) == n {
				break
			}
		}
	}
	return out
}

// sendPainAsk: «что болит сильнее?» with four buttons, after the welcome.
func (f *LeadFunnel) sendPainAsk(ctx context.Context, chatID int64) error {
	var rows [][]map[string]any
	for i := 0; i < len(leadPains); i += 2 {
		r := []map[string]any{}
		for _, p := range leadPains[i:minI(i+2, len(leadPains))] {
			r = append(r, map[string]any{"text": p.Label, "callback_data": leadPainPrefix + p.Key})
		}
		rows = append(rows, r)
	}
	// R40b: кнопка запускает проверку прямо в чате (lead_quiz.go)
	return f.send(ctx, chatID, "Что в бизнесе сейчас болит сильнее всего?\n\nВыберите, и начнём проверку по этому органу бизнеса.",
		map[string]any{"inline_keyboard": rows})
}

func minI(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// painPick answers a pain button: three checklists, a case, the CRM note.
func (f *LeadFunnel) painPick(ctx context.Context, cb bot.CallbackUpdate) bool {
	p := leadPainBy(strings.TrimPrefix(cb.Data, leadPainPrefix))
	if p == nil {
		return f.sendWelcome(ctx, cb.ChatID, cb.FirstName, "") == nil
	}
	if q := quizByKey(p.Key); q != nil { // R40b: польза сразу в чате, а не ссылка в приложение
		return f.quizPainStart(ctx, cb, p.Organ, q)
	}
	_, _, _ = f.ensureLead(ctx, cb.ChatID, cb.FirstName, "", cb.Username, "Telegram: бот", "Выбрал боль в боте: "+p.Organ, "", false)
	gs := painGuides(p.Organ, 3)
	now := f.now()
	_ = f.mutate(ctx, "bs_crm", func(crm map[string]any) bool {
		leads, _ := crm["leads"].([]any)
		lead := findLeadByTg(leads, cb.ChatID)
		if lead == nil {
			return false
		}
		lead["pain"] = p.Organ
		var ts []string
		for _, g := range gs {
			ts = append(ts, fmt.Sprint(g["title"]))
		}
		addLog(lead, now, "Болит: "+p.Organ+". Получил чек-листы: "+strings.Join(ts, ", "))
		return true
	})
	var b strings.Builder
	b.WriteString(strings.TrimSpace(p.Label[strings.Index(p.Label, " ")+1:]) + ": начните с этих трёх чек-листов.\n")
	var rows [][]map[string]any
	for i, g := range gs {
		t, id := fmt.Sprint(g["title"]), fmt.Sprint(g["id"])
		b.WriteString(fmt.Sprintf("\n%d. %s", i+1, t))
		rows = append(rows, row(f.appBtn("📘 "+t, "guide_"+id)))
	}
	b.WriteString("\n\nОтмечайте пункты прямо в приложении, прогресс сохраняется.\n\nКейс: " + p.Case)
	rows = append(rows, row(f.appBtn("🔬 Диагностика: где болит ещё", "diagnostic")))
	return f.send(ctx, cb.ChatID, b.String(), map[string]any{"inline_keyboard": rows}) == nil
}
