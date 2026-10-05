package ai

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"regexp"
	"strings"
	"time"
)

// The events feed asks a model that searches the web. Its answers are less
// tidy than a JSON-mode answer: text around the JSON, ``` fences, a trailing
// comma, a list cut off at the output limit, "events" instead of "items",
// dates as 05.10.2026, prices as numbers. ParseEvents takes all of that.
// Search retries what is worth retrying (overload, rate limit, timeouts) and
// FriendlyError says in plain Russian what went wrong.

// SearchBackoff: the waits between search attempts (tests shorten them).
var SearchBackoff = []time.Duration{4 * time.Second, 12 * time.Second}

// ErrNoEvents: the model answered, but without a list of events.
type ErrNoEvents struct{ Answer string }

func (e *ErrNoEvents) Error() string {
	a := strings.TrimSpace(e.Answer)
	if len([]rune(a)) > 300 {
		a = string([]rune(a)[:300]) + "…"
	}
	if a == "" {
		return "ИИ не вернул список мероприятий: пустой ответ"
	}
	return "ИИ не вернул список мероприятий: " + a
}

// ErrEmptyAnswer: the model gave no text (blocked, cut off before any text).
type ErrEmptyAnswer struct{ Reason string }

func (e *ErrEmptyAnswer) Error() string {
	if e.Reason == "" {
		return "ИИ вернул пустой ответ"
	}
	return "ИИ вернул пустой ответ (" + e.Reason + ")"
}

// transient: worth another try in a few seconds.
func transient(err error) bool {
	if err == nil {
		return false
	}
	// A used-up quota does not come back in seconds: asking again burns calls
	if IsQuota(err) {
		return false
	}
	var se *SearchError
	if errors.As(err, &se) {
		return se.Kind == "results" // too_many_requests, unavailable: the search itself was busy
	}
	var he *HTTPError
	if errors.As(err, &he) {
		switch he.Status {
		case 408, 429, 500, 502, 503, 504:
			return true
		}
		return false
	}
	var ee *ErrEmptyAnswer
	if errors.As(err, &ee) {
		return true // a blocked or empty grounded answer often works on a second try
	}
	var ne net.Error
	if errors.As(err, &ne) && ne.Timeout() {
		return true
	}
	return strings.Contains(err.Error(), "connection reset") || strings.Contains(err.Error(), "EOF")
}

func sleepCtx(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

// FriendlyError: what the team sees when the feed could not be refreshed.
func FriendlyError(err error) string {
	if err == nil {
		return ""
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return "ИИ искал мероприятия слишком долго и не успел ответить. Попробуйте ещё раз"
	}
	if errors.Is(err, context.Canceled) {
		return "Поиск мероприятий прерван. Попробуйте ещё раз"
	}
	if errors.Is(err, ErrNoKey) {
		return err.Error()
	}
	// R32c: one sentence for a used-up quota, the same on every page
	if IsQuota(err) {
		return quotaText(err)
	}
	// R39: a web search refusal in words, with Anthropic's own message
	var se *SearchError
	if errors.As(err, &se) {
		return se.Msg
	}
	var he *HTTPError
	if errors.As(err, &he) {
		low := strings.ToLower(he.Body)
		switch {
		case he.Status == 429:
			return "ИИ ограничил частоту запросов. Попробуйте ещё раз через минуту"
		case he.Status == 401 || he.Status == 403:
			return keyRejected(he)
		case he.Status >= 500:
			return "Сервис ИИ сейчас перегружен (" + fmt.Sprint(he.Status) + "). Попробуйте ещё раз через пару минут"
		case strings.Contains(low, "google_search") || strings.Contains(low, "grounding"):
			return "Gemini отклонил поиск в интернете: " + apiMessage(he.Body)
		case strings.Contains(low, "web_search"):
			return "Поиск в интернете отклонён Anthropic: " + apiMessage(he.Body)
		case he.Status == 404 || he.Status == 400 && strings.Contains(low, "model"):
			return "Модель ИИ недоступна. Укажите рабочую модель в AI_MODEL"
		}
		if m := apiMessage(he.Body); m != "" {
			return "ИИ отклонил запрос: " + m
		}
		return fmt.Sprintf("ИИ ответил ошибкой %d", he.Status)
	}
	var ne *ErrNoEvents
	if errors.As(err, &ne) {
		return "ИИ ответил, но без списка мероприятий. Попробуйте ещё раз"
	}
	var ee *ErrEmptyAnswer
	if errors.As(err, &ee) {
		return "ИИ не дал ответа. Попробуйте ещё раз"
	}
	var nerr net.Error
	if errors.As(err, &nerr) && nerr.Timeout() {
		return "ИИ не ответил вовремя. Попробуйте ещё раз"
	}
	return "Не получилось обновить ленту: " + err.Error()
}

// apiMessage: error.message of a Google/Anthropic error body.
func apiMessage(body string) string {
	var e struct {
		Error struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if json.Unmarshal([]byte(body), &e) == nil {
		m := strings.TrimSpace(e.Error.Message)
		if len([]rune(m)) > 200 {
			m = string([]rune(m)[:200]) + "…"
		}
		return m
	}
	return ""
}

var (
	fenceRe    = regexp.MustCompile("```[a-zA-Z]*")
	trailComma = regexp.MustCompile(`,\s*([}\]])`)
	dotDate    = regexp.MustCompile(`^(\d{1,2})\.(\d{1,2})\.(\d{4})`)
	isoDate    = regexp.MustCompile(`^(\d{4})-(\d{2})-(\d{2})`)
	hhmm       = regexp.MustCompile(`(\d{1,2})[:.](\d{2})`)
)

// jsonBlocks: the balanced {…} and […] blocks of s, outermost first, and a
// cut-off block at the end (from its start to the end of s).
func jsonBlocks(s string) (blocks []string, open string) {
	depth, start := 0, -1
	inStr, esc := false, false
	for i := 0; i < len(s); i++ {
		ch := s[i]
		if inStr {
			switch {
			case esc:
				esc = false
			case ch == '\\':
				esc = true
			case ch == '"':
				inStr = false
			}
			continue
		}
		switch ch {
		case '"':
			if depth > 0 {
				inStr = true
			}
		case '{', '[':
			if depth == 0 {
				start = i
			}
			depth++
		case '}', ']':
			if depth > 0 {
				depth--
				if depth == 0 && start >= 0 {
					blocks = append(blocks, s[start:i+1])
					start = -1
				}
			}
		}
	}
	if depth > 0 && start >= 0 {
		open = s[start:]
	}
	return blocks, open
}

// rawItems: the list of events in one JSON value.
func rawItems(js string) ([]map[string]any, bool) {
	var v any
	if json.Unmarshal([]byte(js), &v) != nil {
		if json.Unmarshal([]byte(trailComma.ReplaceAllString(js, "$1")), &v) != nil {
			return nil, false
		}
	}
	return itemsOf(v)
}

func itemsOf(v any) ([]map[string]any, bool) {
	switch x := v.(type) {
	case []any:
		var out []map[string]any
		for _, it := range x {
			if m, ok := it.(map[string]any); ok {
				out = append(out, m)
			}
		}
		return out, true
	case map[string]any:
		for _, k := range []string{"items", "events", "мероприятия", "data", "results"} {
			if l, ok := x[k]; ok {
				return itemsOf(l)
			}
		}
		if _, ok := x["title"]; ok {
			return []map[string]any{x}, true
		}
	}
	return nil, false
}

// salvage: the complete event objects of a list cut off at the output limit.
func salvage(open string) []map[string]any {
	i := strings.Index(open, "[")
	if i < 0 {
		return nil
	}
	inner := open[i+1:]
	blocks, _ := jsonBlocks(inner)
	var out []map[string]any
	for _, b := range blocks {
		var m map[string]any
		if json.Unmarshal([]byte(b), &m) == nil {
			out = append(out, m)
		}
	}
	return out
}

func str(v any) string {
	switch x := v.(type) {
	case nil:
		return ""
	case string:
		return strings.TrimSpace(x)
	case float64:
		if x == float64(int64(x)) {
			return fmt.Sprint(int64(x))
		}
		return fmt.Sprint(x)
	case []any:
		var p []string
		for _, e := range x {
			if s := str(e); s != "" {
				p = append(p, s)
			}
		}
		return strings.Join(p, ", ")
	}
	return strings.TrimSpace(fmt.Sprint(v))
}

// normDate: YYYY-MM-DD from 2026-10-05, 2026-10-05T18:00, 5.10.2026; the
// time part if the date carried one.
func normDate(s string) (string, string) {
	s = strings.TrimSpace(s)
	tm := ""
	if i := strings.IndexAny(s, "T "); i > 0 {
		if m := hhmm.FindStringSubmatch(s[i:]); m != nil {
			tm = pad2(m[1]) + ":" + m[2]
		}
	}
	if m := isoDate.FindStringSubmatch(s); m != nil {
		return m[1] + "-" + m[2] + "-" + m[3], tm
	}
	if m := dotDate.FindStringSubmatch(s); m != nil {
		return m[3] + "-" + pad2(m[2]) + "-" + pad2(m[1]), tm
	}
	return "", tm
}

func toEvent(m map[string]any) Event {
	e := Event{Title: str(m["title"]), URL: str(m["url"]), Place: str(m["place"]), Price: str(m["price"]), Source: str(m["source"])}
	if e.Title == "" {
		e.Title = str(m["name"])
	}
	if e.URL == "" {
		e.URL = str(m["link"])
	}
	if strings.HasPrefix(e.URL, "www.") {
		e.URL = "https://" + e.URL
	}
	var dt string
	e.Date, dt = normDate(str(m["date"]))
	e.Time = str(m["time"])
	if mm := hhmm.FindStringSubmatch(e.Time); mm != nil {
		e.Time = pad2(mm[1]) + ":" + mm[2]
	} else {
		e.Time = dt
	}
	switch t := m["tags"].(type) {
	case []any:
		for _, x := range t {
			if s := str(x); s != "" {
				e.Tags = append(e.Tags, s)
			}
		}
	case string:
		for _, s := range strings.FieldsFunc(t, func(r rune) bool { return r == ',' || r == '|' }) {
			if s = strings.TrimSpace(s); s != "" {
				e.Tags = append(e.Tags, s)
			}
		}
	}
	return e
}

// ParseEvents reads the model's answer: the upcoming events with a date and
// a link, without doubles.
func ParseEvents(ans string, from time.Time) ([]Event, error) {
	s := fenceRe.ReplaceAllString(ans, "")
	blocks, open := jsonBlocks(s)
	var raw []map[string]any
	found := false
	for _, b := range blocks {
		if items, ok := rawItems(b); ok && (len(items) > len(raw) || !found) {
			raw, found = items, true
		}
	}
	if !found && open != "" {
		if items := salvage(open); len(items) > 0 {
			raw, found = items, true
		}
	}
	if !found {
		return nil, &ErrNoEvents{Answer: ans}
	}
	today := from.Format("2006-01-02")
	keep := []Event{}
	seen := map[string]bool{}
	for _, m := range raw {
		e := toEvent(m)
		if e.Title == "" || len(e.Date) != 10 || e.Date < today || !strings.HasPrefix(e.URL, "http") {
			continue
		}
		k := strings.ToLower(e.Title) + e.Date
		if seen[k] {
			continue
		}
		seen[k] = true
		keep = append(keep, e)
	}
	return keep, nil
}

func pad2(s string) string {
	if len(s) == 1 {
		return "0" + s
	}
	return s
}
