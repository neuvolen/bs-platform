package http

import (
	"context"
	"fmt"
	"time"

	"github.com/bnursik/business_surgery_backend/internal/ai"
)

// R39: «Рекомендации ИИ: почему ничего не делается». The daily run leaves a
// trace in bs_ai_recs.run (the platform shows it, /status reads it):
//
//	run = {at, ok, error, tries, day, next}
//
// A failed run is tried again later the same day (aiRecsRetry, at most
// aiRecsTries runs a day, not after 21:00 Almaty), so one bad hour of the
// API no longer costs the whole day.

const (
	aiRecsTries     = 4
	aiRecsLastHour  = 21
	aiRecsRetryWait = time.Hour
)

// aiRecsRetry: the wait before the next attempt after a failure (tests shorten it).
var aiRecsRetry = aiRecsRetryWait

// noteRecsRun writes the run's result into bs_ai_recs.run.
func (h *PlatformAI) noteRecsRun(ctx context.Context, now time.Time, err error, next time.Time) int {
	day := now.In(almaty).Format("2006-01-02")
	tries := 0
	_ = h.updateRecsDoc(ctx, func(doc map[string]any, items []any) bool {
		prev, _ := doc["run"].(map[string]any)
		if prev != nil && recStr(prev, "day") == day {
			if n, ok := prev["tries"].(float64); ok {
				tries = int(n)
			}
		}
		tries++
		run := map[string]any{"at": now.UTC().Format(time.RFC3339), "ok": err == nil, "day": day, "tries": tries}
		if err != nil {
			run["error"] = ai.UserMessage(err)
		}
		if !next.IsZero() {
			run["next"] = next.UTC().Format(time.RFC3339)
		}
		doc["run"] = run
		return true
	})
	return tries
}

// recsNext: when the loop runs again after a run at now (failed: retry).
func recsNext(now time.Time, failed bool, tries int) time.Time {
	if failed && tries < aiRecsTries {
		next := now.Add(aiRecsRetry)
		if next.In(almaty).Hour() < aiRecsLastHour && next.In(almaty).Day() == now.In(almaty).Day() {
			return next
		}
	}
	return now.Add(untilAlmatyHour(now, aiRecsHour))
}

// RecsStatus: the /status line «Рекомендации ИИ».
func (h *PlatformAI) RecsStatus(ctx context.Context, now time.Time) CheckItem {
	it := CheckItem{Key: "airecs", Title: "Рекомендации ИИ"}
	if h == nil || h.repo == nil {
		it.State, it.Text = "off", "нет данных"
		return it
	}
	var doc map[string]any
	var items []any
	if d, err := h.repo.GetDoc(ctx, "club", aiRecsKey); err == nil && d != nil && !d.Deleted {
		doc, items = readRecs(d.Value)
	}
	last := ""
	added, waiting := 0, 0
	week := now.Add(-7 * 24 * time.Hour)
	for _, x := range items {
		if last == "" {
			last = recStr(x, "date")
		}
		switch recStr(x, "status") {
		case "added":
			if t, err := time.Parse(time.RFC3339, recStr(x, "addedAt")); err == nil && t.After(week) {
				added++
			}
		case "ask", "new":
			waiting++
		}
	}
	lastTxt := "ещё не было"
	if t, err := time.ParseInLocation("2006-01-02", last, almaty); err == nil {
		lastTxt = "последняя " + t.Format("02.01")
	}
	it.Text = fmt.Sprintf("%s, добавлено %d за неделю, ждёт решения %d", lastTxt, added, waiting)
	it.State, it.Sig = "ok", "ok"
	today := now.In(almaty).Format("2006-01-02")
	run, _ := doc["run"].(map[string]any)
	if run != nil && run["ok"] == false && recStr(run, "error") != "" && last != today {
		at, _ := time.Parse(time.RFC3339, recStr(run, "at"))
		msg := "ошибка: " + recStr(run, "error")
		if !at.IsZero() {
			msg += " (" + at.In(almaty).Format("02.01 15:04")
			if nx, e := time.Parse(time.RFC3339, recStr(run, "next")); e == nil {
				msg += ", следующая попытка " + nx.In(almaty).Format("02.01 15:04")
			}
			msg += ")"
		}
		it.State, it.Sig = "fail", "err"
		it.Text = msg + " · " + it.Text
		it.Note = "❌ Рекомендации ИИ не находятся: " + recStr(run, "error")
		return it
	}
	if last != today && now.In(almaty).Hour() >= aiRecsHour+1 {
		it.State, it.Sig = "warn", "missing"
		it.Text = "сегодня ещё нет · " + it.Text
	}
	if waiting > 0 && it.State == "ok" {
		it.Note = fmt.Sprintf("ℹ️ Рекомендации ИИ: ждёт решения %d", waiting)
	}
	return it
}

// noteRecsNext: the time of the next attempt, after the failure was counted.
func (h *PlatformAI) noteRecsNext(ctx context.Context, next time.Time) {
	_ = h.updateRecsDoc(ctx, func(doc map[string]any, items []any) bool {
		run, _ := doc["run"].(map[string]any)
		if run == nil {
			return false
		}
		run["next"] = next.UTC().Format(time.RFC3339)
		return true
	})
}

// search: «Поиск в интернете: работает / ошибка (причина)», a tiny live search.
func (s *SysCheck) search(ctx context.Context) CheckItem {
	it := CheckItem{Key: "search", Title: "Поиск в интернете"}
	if s.AI == nil || !s.AI.HasClaude() {
		it.State, it.Text, it.Sig = "off", "нет ключа Claude", "nokey"
		return it
	}
	c, cancel := context.WithTimeout(ctx, 40*time.Second)
	defer cancel()
	si, err := s.AI.SearchPing(c)
	if err != nil {
		msg := ai.SearchMessage(err)
		it.State, it.Text, it.Sig = "fail", "ошибка ("+msg+")", "err"
		it.Note = "❌ Поиск в интернете не работает: " + msg
		return it
	}
	it.State, it.Sig = "ok", "ok"
	it.Text = "работает (" + si.Model + ", " + si.Tool + ")"
	it.Note = "✅ Поиск в интернете работает: мероприятия и рекомендации ИИ снова находятся"
	return it
}

func (s *SysCheck) recs(ctx context.Context) CheckItem {
	if s.Recs == nil {
		return CheckItem{Key: "airecs", Title: "Рекомендации ИИ", State: "off", Text: "нет данных"}
	}
	return s.Recs.RecsStatus(ctx, s.now())
}
