package http

import (
	"context"
	"encoding/json"
	"log"
	"strings"
	"time"
)

// R32e: «Маркетинг: Инсайты и план, внедрение того, где приоритет высокий».
// R32b (r32_mkt_actions.go) already marked the high-priority actions; this
// round adds what was still missing and says so in the plan:
//   a1 Threads: the 5 rubrics of the plan are formats of the content engine
//      (opinion «Мнение» and backstage «Закулисье» added, content_threads.go);
//   a2 the bot asks a new lead what hurts and gives 3 checklists of that
//      organ with a case (lead_pain.go);
//   a4, a5 the owner's tasks get a suggested deadline on the board.
// One-time merge (marker in the server doc). A note is replaced only while it
// is the one the platform wrote (the team did not edit it); a card's
// deadline only when the card has none; an action the team marked or
// reopened itself is left alone.

const mktActionsR32eKey = "mkt_actions_r32e" // scope server: done marker

// mktR32eNotes: action id → the note now (only over the R32b note or none).
var mktR32eNotes = map[string]string{
	"a1": "Контент-завод сам публикует в Threads каждый день (до 16 постов) по 5 рубрикам плана: диагностика (симптом, мини-чек-лист, вопрос собственнику), мнение, кейсы (мини-кейс, цифры в ₸), ошибки (миф и факт), закулисье разбора. У каждого 4-го поста ссылка в @bsurgery_bot с меткой источника, лиды видны в CRM. Настройки: Маркетинг → Контент-завод.",
	"a2": "Новый лид после /start в боте выбирает, что болит: операционка, команда, финансы или продажи, и сразу получает 3 чек-листа этого органа и кейс резидента с цифрами. Выбор записан в карточку лида в CRM. Дальше бот ведёт 5 касаниями (1, 3, 7, 10 и 14 день), в каждом кейс с цифрами; прогрев учитывает, какой чек-лист лид открыл и прошёл.",
}

// mktR32eDue: owner tasks and their suggested deadline in days.
var mktR32eDue = map[string]int{"a4": 14, "a5": 10}

// mergeMktR32e updates the plan's notes; due: action id → the task's
// deadline (ДД.ММ) for the note. Returns how many actions changed.
func mergeMktR32e(doc map[string]any, due map[string]string) int {
	n := 0
	ids := mktDefaultIDs()
	for _, x := range csList(doc["actions"]) {
		a := csM(x)
		if a == nil {
			continue
		}
		id := csS(a["id"])
		if id == "" {
			id = ids[mktKey(csS(a["text"]))]
		}
		if id == "" {
			continue
		}
		old, has := mktActionPlans[id]
		note := csS(a["note"])
		mine := note == "" || (has && note == old.Note)
		if text, ok := mktR32eNotes[id]; ok && mine && note != text && (csS(a["status"]) == "" || csS(a["status"]) == "done") {
			a["note"] = text
			if csS(a["status"]) == "" {
				a["status"], a["done"] = "done", true
				a["doneAt"] = time.Now().UTC().Format(time.RFC3339)
			}
			a["statusAt"] = time.Now().UTC().Format(time.RFC3339)
			n++
		}
		if d, ok := due[id]; ok && mine && csS(a["status"]) == "work" {
			text := old.Note
			if text == "" {
				text = "Задача у владельца на доске «Цели и задачи»."
			}
			text += " Шаги внутри задачи, предложенный срок " + d + "."
			if note != text {
				a["note"] = text
				n++
			}
		}
	}
	return n
}

func csList(v any) []any { l, _ := v.([]any); return l }

// MigrateMktR32e runs once, after MigrateMktActions (mkt_competitors.go).
func (h *PlatformAI) MigrateMktR32e(ctx context.Context) {
	if h.repo == nil {
		return
	}
	if d, err := h.repo.GetDoc(ctx, "server", mktActionsR32eKey); err == nil && d != nil && !d.Deleted {
		return
	}
	if d, err := h.repo.GetDoc(ctx, "server", mktActionsKey); err != nil || d == nil || d.Deleted {
		return // R32b has not run yet (no plan doc): next start
	}
	now := time.Now().In(csAlmaty)
	due := map[string]string{}
	// 1. Board: a deadline on the owner's tasks that have none
	for try := 0; try < 4; try++ {
		d, err := h.repo.GetDoc(ctx, "club", "bs_kanban")
		if err != nil || d == nil || d.Deleted {
			break
		}
		kb := map[string]any{}
		if json.Unmarshal([]byte(d.Value), &kb) != nil {
			break
		}
		changed := false
		for _, x := range csList(kb["cards"]) {
			c := csM(x)
			src := csS(c["src"])
			if c == nil || !strings.HasPrefix(src, "mkt:") {
				continue
			}
			id := strings.TrimPrefix(src, "mkt:")
			days, ok := mktR32eDue[id]
			if !ok {
				continue
			}
			if dd := csS(c["due"]); dd != "" {
				if t, err := time.Parse("2006-01-02", dd); err == nil {
					due[id] = t.Format("02.01")
				}
				continue
			}
			t := now.AddDate(0, 0, days)
			c["due"] = t.Format("2006-01-02")
			due[id] = t.Format("02.01")
			lg := csList(c["log"])
			c["log"] = append(lg, map[string]any{"at": time.Now().UTC().Format(time.RFC3339), "by": "Платформа", "k": "e", "t": "Предложен срок " + t.Format("02.01")})
			changed = true
		}
		if !changed {
			break
		}
		val, _ := json.Marshal(kb)
		if _, err := h.repo.PutDoc(ctx, "club", "bs_kanban", d.Version, string(val), false, "server:mkt"); err == nil {
			break
		}
		due = map[string]string{}
	}
	// 2. The plan's notes
	ok := false
	n := 0
	for try := 0; try < 4; try++ {
		d, err := h.repo.GetDoc(ctx, "club", "bs_mkt_analysis")
		if err != nil || d == nil || d.Deleted {
			return
		}
		doc := map[string]any{}
		if json.Unmarshal([]byte(d.Value), &doc) != nil {
			return
		}
		if n = mergeMktR32e(doc, due); n == 0 {
			ok = true
			break
		}
		val, _ := json.Marshal(doc)
		if _, err := h.repo.PutDoc(ctx, "club", "bs_mkt_analysis", d.Version, string(val), false, "server:mkt"); err == nil {
			ok = true
			break
		}
	}
	if !ok {
		return
	}
	if _, err := h.repo.PutDoc(ctx, "server", mktActionsR32eKey, 0, `{"v":1}`, false, "server:mkt"); err == nil {
		log.Printf("marketing r32e: %d plan actions updated", n)
	}
}
