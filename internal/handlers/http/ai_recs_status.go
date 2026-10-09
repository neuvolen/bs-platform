package http

import (
	"context"
	"errors"
	"fmt"
	"strings"
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

// aiRecsMaxTries: R51: while a provider answers, a failed day is tried
// again every aiRecsRetry until 21:00, at most this many runs a day.
const aiRecsMaxTries = 12

// recsNext: when the loop runs again after a run at now (failed: retry).
// R51: answering says a provider answers now: then the retry comes within
// the day even after aiRecsTries failures (they were spent while every
// provider rested: prod 06.10, attempt 6 went to the next morning).
func recsNext(now time.Time, failed bool, tries int, answering ...bool) time.Time {
	limit := aiRecsTries
	if len(answering) > 0 && answering[0] {
		limit = aiRecsMaxTries
	}
	if failed && tries < limit {
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
		// R51: the reason once, in words, and when it is tried again; a
		// retry is coming, so a warning, not ❌ (the AI line says what to fix)
		msg := "сегодня ещё не найдены: " + recsReason(recStr(run, "error"))
		if nx, e := time.Parse(time.RFC3339, recStr(run, "next")); e == nil && nx.After(now) {
			if nx.In(almaty).Format("2006-01-02") == today {
				msg += ", следующая попытка в " + nx.In(almaty).Format("15:04")
			} else {
				msg += ", следующая попытка завтра в " + nx.In(almaty).Format("15:04")
			}
		}
		it.State, it.Sig = "warn", "err"
		it.Text = msg + " · " + it.Text
		it.Note = "⚠️ Рекомендации ИИ не находятся: " + recStr(run, "error")
		if at, e := time.Parse(time.RFC3339, recStr(run, "at")); e == nil {
			it.Detail = "Последняя попытка " + at.In(almaty).Format("02.01 15:04") + ": " + recStr(run, "error")
		}
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

// search: «Поиск в интернете: работает / ошибка (причина)», a tiny live
// search. R51: Problem is the human reason once, Action what to do (only
// when the owner can do something: a refused key; a used-up free limit
// comes back by itself), Detail the API's answer for /status подробно.
func (s *SysCheck) search(ctx context.Context) CheckItem {
	it := CheckItem{Key: "search", Title: "Поиск в интернете"}
	if s.AI == nil || len(s.AI.SearchModels()) == 0 {
		it.State, it.Text, it.Sig = "off", "нет ключа Claude или Gemini", "nokey"
		return it
	}
	c, cancel := context.WithTimeout(ctx, 40*time.Second)
	defer cancel()
	si, err := s.AI.SearchPing(c)
	var ke *ai.KeyError
	if errors.As(err, &ke) {
		it.State, it.Sig = "fail", "badkey-"+ke.Service
		it.Problem, it.Action = ke.Problem(), ke.Action()
		it.Text = "не работает: " + ke.Problem()
		it.Note = "❌ Поиск в интернете не работает: " + ke.Problem()
		it.Detail = "Поиск: " + ke.Problem() + searchHTTP(err)
		return it
	}
	if err != nil && ai.SearchUnavailable(err) {
		// R42: events keep the feed, AI recs go without search
		it.State, it.Sig = "warn", "nosearch"
		it.Problem = "временно недоступен, " + searchWhy(err)
		it.Text = it.Problem + ": мероприятия не обновляются, рекомендации ИИ делаются без поиска"
		it.Note = "⚠️ Поиск в интернете недоступен: мероприятия не обновляются, рекомендации ИИ делаются без поиска"
		it.Detail = "Поиск: " + ai.SearchMessage(err) + searchHTTP(err)
		return it
	}
	if err != nil {
		msg := ai.SearchMessage(err)
		it.State, it.Text, it.Sig = "warn", "ошибка ("+msg+")", "err"
		it.Problem = "ошибка, повторю позже"
		it.Note = "❌ Поиск в интернете не работает: " + msg
		it.Detail = "Поиск: " + msg + searchHTTP(err)
		return it
	}
	it.State, it.Sig = "ok", "ok"
	it.Text = "работает (" + si.Model + ", " + si.Tool + ")"
	it.Note = "✅ Поиск в интернете работает: мероприятия и рекомендации ИИ снова находятся"
	it.Detail = "Поиск: " + si.Model + ", " + si.Tool
	return it
}

// searchWhy: the short reason a search is unavailable.
func searchWhy(err error) string {
	var qe *ai.QuotaError
	if errors.As(err, &qe) && strings.HasPrefix(qe.Service, "gemini") {
		return "бесплатный лимит поиска на сегодня исчерпан, восстановится сам"
	}
	if ai.IsQuota(err) {
		return "бесплатный поиск для этого ключа сейчас недоступен"
	}
	return "повторю позже"
}

// searchHTTP: the API's status and message (no keys inside) for the details.
func searchHTTP(err error) string {
	var he *ai.HTTPError
	if errors.As(err, &he) {
		return fmt.Sprintf(" (ответ API %d: %s)", he.Status, ai.ShortAPIMessage(he.Body))
	}
	return ""
}

func (s *SysCheck) recs(ctx context.Context) CheckItem {
	if s.Recs == nil {
		return CheckItem{Key: "airecs", Title: "Рекомендации ИИ", State: "off", Text: "нет данных"}
	}
	return s.Recs.RecsStatus(ctx, s.now())
}

// recsReason: the stored error in a few words (the AI line has the details
// and the action).
func recsReason(e string) string {
	low := strings.ToLower(e)
	switch {
	case strings.Contains(low, "ключ") && strings.Contains(low, "не принят"), strings.Contains(low, "api key not valid"):
		return "ключ ИИ не принят"
	case strings.Contains(low, "баланс") || strings.Contains(low, "лимит") || strings.Contains(low, "на паузе"):
		return "бесплатные лимиты ИИ были на паузе"
	case strings.Contains(low, "нет ключа"):
		return "нет ключа ИИ"
	case strings.Contains(low, "долго") || strings.Contains(low, "deadline") || strings.Contains(low, "timeout"):
		return "ИИ отвечал слишком долго"
	}
	if i := strings.Index(e, " (ответ "); i > 0 { // the API's own words: /status подробно
		e = e[:i]
	}
	if r := []rune(e); len(r) > 160 {
		return string(r[:160]) + "…"
	}
	return e
}
