package bot

import (
	"context"
	"fmt"
	"log"
	"net/url"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/bnursik/business_surgery_backend/internal/club"
)

// R67: «Я в приложении отметил встречу, а бот спустя время спросил
// "встреча прошла?"». The question came from the sheet's old script (v32,
// its self-update fails): it reads «Расписание» and «Лог встреч» in the
// sheet, which after the cutover never learn about a mark made in the app.
// The server asks itself now, from its own data (the app's and the
// platform's marks), and only about a meeting nobody marked; the button opens
// the app right on that meeting with «Встреча прошла».

// MeetDeepLink: the app's address of one meeting (the app opens its card).
func MeetDeepLink(day time.Time, hhmm, res string) string {
	p := "meet_" + day.In(club.Almaty).Format("20060102")
	if c := club.ClockTime(hhmm); c != "" {
		p += "_" + strings.Replace(c, ":", "", 1)
	}
	q := url.Values{"p": {p}}
	if strings.TrimSpace(res) != "" {
		q.Set("r", strings.TrimSpace(res))
	}
	return WebAppBase + "?" + q.Encode()
}

// AppLink: the app opened on a page, optionally for one resident (?p=pay&r=Имя).
func AppLink(page, res string) string {
	q := url.Values{"p": {page}}
	if strings.TrimSpace(res) != "" {
		q.Set("r", strings.TrimSpace(res))
	}
	return WebAppBase + "?" + q.Encode()
}

// MeetButton: the inline button that opens one meeting in the app.
func MeetButton(text string, day time.Time, hhmm, res string) map[string]any {
	return map[string]any{"inline_keyboard": [][]map[string]any{{{"text": text, "web_app": map[string]string{"url": MeetDeepLink(day, hhmm, res)}}}}}
}

// meetAskAfter: the question comes 90 minutes after the start (as the
// script's), and not later than 6 hours after (a forgotten old meeting is
// not asked about at night).
const (
	meetAskAfter = 90 * time.Minute
	meetAskUntil = 6 * time.Hour
)

// MeetAsk is one question to the team.
type MeetAsk struct {
	At    time.Time
	Names []string
	Key   string
	Text  string
	Link  string
}

// MeetDoneAsks: the meetings started 90 minutes to 6 hours ago that nobody
// marked as held. Residents at the same time (the offline day) are one question.
func MeetDoneAsks(meetings []club.Meeting, logged map[string]bool, now time.Time) []MeetAsk {
	a := now.In(club.Almaty)
	slots := map[string]*MeetAsk{}
	var order []string
	for _, m := range meetings {
		name := strings.TrimSpace(m.Resident)
		if m.Done || name == "" || club.ClockTime(m.Time) == "" {
			continue
		}
		d := m.Date.In(club.Almaty)
		if logged[club.NormName(name)+"|"+d.Format("2006-01-02")] {
			continue
		}
		at := meetingStart(m)
		if since := a.Sub(at); since < meetAskAfter || since > meetAskUntil {
			continue
		}
		k := at.Format("2006-01-02T15:04")
		sl := slots[k]
		if sl == nil {
			sl = &MeetAsk{At: at, Key: "meetask|" + k}
			slots[k] = sl
			order = append(order, k)
		}
		sl.Names = append(sl.Names, name)
	}
	sort.Strings(order)
	var out []MeetAsk
	for _, k := range order {
		sl := slots[k]
		day, hm := sl.At.Format("02.01"), sl.At.Format("15:04")
		if len(sl.Names) > 1 {
			sl.Text = fmt.Sprintf("📋 Офлайн день %s в %s прошёл?\n\n%d резидентов: %s\n\nОтметьте присутствие кнопкой ниже.", day, hm, len(sl.Names), strings.Join(sl.Names, ", "))
			sl.Link = MeetDeepLink(sl.At, hm, "")
		} else {
			sl.Text = fmt.Sprintf("📋 Встреча %s в %s с %s прошла?\n\nКнопка ниже откроет эту встречу, там «Встреча прошла».", day, hm, sl.Names[0])
			sl.Link = MeetDeepLink(sl.At, hm, sl.Names[0])
		}
		out = append(out, *sl)
	}
	return out
}

// maybeMeetDoneAsks sends the questions, once per slot.
func (s *Service) maybeMeetDoneAsks(ctx context.Context, now time.Time) ([]MeetAsk, error) {
	if len(s.admins) == 0 || club.SheetLegacy() {
		return nil, nil // in legacy mode the sheet's script asks
	}
	snap, err := s.repo.Club().Load(ctx)
	if err != nil {
		return nil, err
	}
	logged, err := s.repo.Club().MeetingLogSince(ctx, now.Add(-48*time.Hour))
	if err != nil {
		return nil, err
	}
	var sent []MeetAsk
	for _, q := range MeetDoneAsks(snap.Meetings, logged, now) {
		if !s.once(ctx, q.Key, 30*24*time.Hour) {
			continue
		}
		kb := map[string]any{"inline_keyboard": [][]map[string]any{{{"text": "✅ Открыть встречу", "web_app": map[string]string{"url": q.Link}}}}}
		for _, id := range s.admins {
			if err := s.SendMessageKB(ctx, id, q.Text, kb); err != nil {
				log.Printf("bot meet ask %s → %d: %v", q.Key, id, err)
			}
		}
		sent = append(sent, q)
	}
	return sent, nil
}

// ── /calendar ──

// CalendarHook answers the team's /calendar (the Google Calendar's state and
// a private link to connect it).
type CalendarHook func(ctx context.Context) string

var calendarHooks sync.Map // *Service → CalendarHook

func (s *Service) SetCalendarHook(h CalendarHook) { calendarHooks.Store(s, h) }

func (s *Service) calendarHook() CalendarHook {
	if v, ok := calendarHooks.Load(s); ok {
		return v.(CalendarHook)
	}
	return nil
}
