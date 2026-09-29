package http

import (
	"context"
	"encoding/json"
	"regexp"
	"strings"
	"time"

	"github.com/bnursik/business_surgery_backend/internal/repository/pg"
)

// Meetings that already happened do not stay in the app's schedule.
//
// The script leaves a finished meeting in «Расписание» as history ("✅
// Проведена") and still hands it to the app until the end of the day. The
// server hides it: at once when the team marks it in the app (the action goes
// through here), and from the hourly import for anything marked elsewhere
// (the bot, the sheet): the «Проведена» mark and «Лог встреч».

// AppDoneSource gives what the import knows about finished meetings:
// marked rows (Time set) and logged meetings (Time "*").
type AppDoneSource interface {
	DoneMeetings(ctx context.Context) ([]pg.DoneMeeting, error)
}

var ddmmRe = regexp.MustCompile(`^(\d{1,2})\.(\d{1,2})`)

func ddmm(s string) string {
	m := ddmmRe.FindStringSubmatch(strings.TrimSpace(s))
	if m == nil {
		return ""
	}
	pad := func(x string) string {
		if len(x) == 1 {
			return "0" + x
		}
		return x
	}
	return pad(m[1]) + "." + pad(m[2])
}

func doneKey(res, date, tm string) string {
	return normName(res) + "|" + ddmm(date) + "|" + strings.TrimSpace(tm)
}

// noteDone remembers a meeting the team marked in the app.
func (g *AppGateway) noteDone(action string, p map[string]string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.done == nil {
		g.done = map[string]time.Time{}
	}
	now := g.now()
	switch action {
	case "confirmMeeting":
		if p["res"] != "" && ddmm(p["date"]) != "" {
			g.done[doneKey(p["res"], p["date"], p["time"])] = now
			if strings.TrimSpace(p["time"]) == "" {
				g.done[doneKey(p["res"], p["date"], "*")] = now
			}
		}
	case "markAttendance":
		for _, n := range strings.Split(p["names"], ",") {
			if strings.TrimSpace(n) != "" && ddmm(p["date"]) != "" {
				g.done[doneKey(n, p["date"], "*")] = now
			}
		}
	}
	for k, at := range g.done { // a week is plenty: past days drop out of the schedule anyway
		if now.Sub(at) > 7*24*time.Hour {
			delete(g.done, k)
		}
	}
	g.bundles = map[string]*cachedBundle{}
}

// doneSet merges the marks made here with the import, the import at most once a minute.
func (g *AppGateway) doneSet(ctx context.Context) map[string]bool {
	g.mu.Lock()
	stale := g.Done != nil && g.now().Sub(g.doneAt) > time.Minute
	g.mu.Unlock()
	if stale {
		c, cancel := context.WithTimeout(ctx, 3*time.Second)
		rows, err := g.Done.DoneMeetings(c)
		cancel()
		m := map[string]bool{}
		for _, r := range rows {
			m[doneKey(r.Resident, r.Date, r.Time)] = true
		}
		g.mu.Lock()
		g.doneAt = g.now()
		if err == nil {
			g.doneImp = m
		}
		g.mu.Unlock()
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	out := map[string]bool{}
	for k := range g.doneImp {
		out[k] = true
	}
	for k := range g.done {
		out[k] = true
	}
	return out
}

// hideDone drops finished meetings from a bundle. The bundle is returned as is
// when nothing is to be dropped or it cannot be read.
func hideDone(body []byte, done map[string]bool, now time.Time) []byte {
	if len(done) == 0 {
		return body
	}
	var top map[string]json.RawMessage
	if json.Unmarshal(body, &top) != nil || top["schedule"] == nil {
		return body
	}
	var list []map[string]any
	if json.Unmarshal(top["schedule"], &list) != nil {
		return body
	}
	loc := time.FixedZone("Almaty", 5*3600)
	keep := list[:0]
	dropped := 0
	for _, s := range list {
		res, _ := s["res"].(string)
		date, _ := s["date"].(string)
		tm, _ := s["time"].(string)
		if res == "" {
			keep = append(keep, s)
			continue
		}
		hit := done[doneKey(res, date, tm)]
		if !hit && done[doneKey(res, date, "*")] {
			// a logged meeting that day hides only a meeting that has started
			if st, ok := meetingStart(date, tm, now, loc); ok && !st.After(now) {
				hit = true
			}
		}
		if hit {
			dropped++
			continue
		}
		keep = append(keep, s)
	}
	if dropped == 0 {
		return body
	}
	raw, err := json.Marshal(keep)
	if err != nil {
		return body
	}
	top["schedule"] = raw
	out, err := json.Marshal(top)
	if err != nil {
		return body
	}
	return out
}

func meetingStart(date, tm string, now time.Time, loc *time.Location) (time.Time, bool) {
	d := strings.TrimSpace(date)
	var t time.Time
	var err error
	if len(d) >= 10 {
		t, err = time.ParseInLocation("02.01.2006", d[:10], loc)
	} else {
		t, err = time.ParseInLocation("02.01.2006", ddmm(d)+"."+now.In(loc).Format("2006"), loc)
	}
	if err != nil {
		return time.Time{}, false
	}
	if hm, err := time.Parse("15:04", strings.TrimSpace(tm)); err == nil {
		t = t.Add(time.Duration(hm.Hour())*time.Hour + time.Duration(hm.Minute())*time.Minute)
	}
	return t, true
}
