package bot

import (
	"context"
	"fmt"
	"log"
	"strconv"
	"strings"
	"time"

	"github.com/bnursik/business_surgery_backend/internal/club"
)

// Meeting reminders, as the script's sendMeetingReminders meant them:
//   - 3 days before (calendar days), from 09:00;
//   - the day before, from 09:00, with a button to update the balance wheel;
//   - an hour before (sent within the last 65 minutes).
//
// The script read the Chat ID from the «Бывший» column, so its reminders
// never reached anyone; here the resident's Telegram id is used.
const (
	remindFromHour = 9
	hourBeforeMin  = 65
)

// MeetingReminder is one reminder due now.
type MeetingReminder struct {
	Kind     string // 3d, 1d, 1h
	Resident string
	TgID     int64
	When     time.Time
	Text     string
	Wheel    bool
	Key      string
}

func meetingStart(m club.Meeting) time.Time {
	d := m.Date.In(club.Almaty)
	h, mi := 9, 0
	if p := strings.Split(strings.TrimSpace(m.Time), ":"); len(p) >= 2 {
		if x, err := strconv.Atoi(strings.TrimSpace(p[0])); err == nil {
			h = x
		}
		if x, err := strconv.Atoi(strings.TrimSpace(p[1])); err == nil {
			mi = x
		}
	}
	return time.Date(d.Year(), d.Month(), d.Day(), h, mi, 0, 0, club.Almaty)
}

// DueReminders lists the reminders to send at now. texts holds the
// «Настройки» templates schedule_3days and schedule_1day.
func DueReminders(meetings []club.Meeting, residents []club.Resident, texts map[string]string, now time.Time) []MeetingReminder {
	return DueRemindersWA(meetings, residents, texts, now, nil)
}

// DueRemindersWA: as DueReminders, also for the residents in wa (normalized
// names) who read WhatsApp and may have no Telegram (their TgID is then -1).
func DueRemindersWA(meetings []club.Meeting, residents []club.Resident, texts map[string]string, now time.Time, wa map[string]bool) []MeetingReminder {
	ids := map[string]int64{}
	names := map[string]string{} // the resident's name as the debts sheet has it
	for _, r := range residents {
		if r.Former || r.Name == "" {
			continue
		}
		if r.TgID != 0 {
			ids[club.NormName(r.Name)] = r.TgID
			names[club.NormName(r.Name)] = r.Name
		} else if wa[club.NormName(r.Name)] && !r.Archived {
			ids[club.NormName(r.Name)] = -1
			names[club.NormName(r.Name)] = r.Name
		}
	}
	a := now.In(club.Almaty)
	today := time.Date(a.Year(), a.Month(), a.Day(), 0, 0, 0, 0, club.Almaty)
	var out []MeetingReminder
	for _, m := range meetings {
		if m.Done || strings.TrimSpace(m.Resident) == "" {
			continue
		}
		id := ids[club.NormName(m.Resident)]
		if id == 0 {
			continue
		}
		at := meetingStart(m)
		if !at.After(a) {
			continue
		}
		day := time.Date(at.Year(), at.Month(), at.Day(), 0, 0, 0, 0, club.Almaty)
		daysDiff := int(day.Sub(today).Hours()/24 + 0.5)
		first := strings.Fields(names[club.NormName(m.Resident)])[0]
		when := at.Format("02.01.2006 15:04")
		addr := strings.TrimSpace(m.Link)
		if addr == "" {
			addr = strings.TrimSpace(m.Place)
		}
		if addr == "" {
			addr = "Онлайн (Google Meet)"
		}
		vars := map[string]string{"имя": first, "дата": when, "адрес": addr}
		key := club.NormName(m.Resident) + "|" + at.Format("2006-01-02T15:04")
		switch {
		case daysDiff == 3 && a.Hour() >= remindFromHour && !m.Sent3d:
			t := texts["schedule_3days"]
			if t == "" {
				t = "🗓 {имя}, через 3 дня встреча!\n\n📅 {дата}\n📍 {адрес}"
			}
			out = append(out, MeetingReminder{Kind: "3d", Resident: m.Resident, TgID: id, When: at, Text: fill(t, vars), Key: "3d|" + key})
		case daysDiff == 1 && a.Hour() >= remindFromHour && !m.Sent1d:
			t := texts["schedule_1day"]
			if t == "" {
				t = "⏰ {имя}, встреча ЗАВТРА!\n\n📅 {дата}\n📍 {адрес}\n\nДо встречи! 💪"
			}
			out = append(out, MeetingReminder{Kind: "1d", Resident: m.Resident, TgID: id, When: at, Wheel: true, Key: "1d|" + key,
				Text: fill(t, vars) + "\n\n🎯 Перед встречей обнови Колесо баланса. это займёт 2 минуты и даст нам точную повестку."})
		case daysDiff == 0 && at.Sub(a) <= hourBeforeMin*time.Minute && !m.Sent1h:
			t := fmt.Sprintf("⏰ %s, встреча через час!\n\n📅 %s\n", first, when)
			if strings.Contains(m.Link, "http") {
				t += "🔗 " + m.Link
			} else {
				t += "📍 " + addr
			}
			out = append(out, MeetingReminder{Kind: "1h", Resident: m.Resident, TgID: id, When: at, Text: t, Key: "1h|" + key})
		}
	}
	return out
}

func (s *Service) maybeMeetingReminders(ctx context.Context, now time.Time) ([]MeetingReminder, error) {
	if !s.Has(FeatureMeetingReminders) {
		return nil, nil
	}
	snap, err := s.repo.Club().Load(ctx)
	if err != nil {
		return nil, err
	}
	texts := map[string]string{}
	for _, k := range []string{"schedule_3days", "schedule_1day"} {
		texts[k], _ = s.repo.Setting(ctx, k)
	}
	var sent []MeetingReminder
	for _, r := range DueRemindersWA(snap.Meetings, snap.Residents, texts, now, s.waNames(ctx, snap.Residents)) {
		if !s.once(ctx, "meet:"+r.Key, 30*24*time.Hour) {
			continue
		}
		var kb map[string]any
		if r.Wheel {
			kb = map[string]any{"inline_keyboard": [][]map[string]any{{{"text": "🧬 Обновить колесо", "web_app": map[string]string{"url": webApp("wheel")}}}}}
		}
		tg := r.TgID
		if tg < 0 {
			tg = 0
		}
		// R38c: a resident who reads WhatsApp gets it there (outreach.go)
		if err := s.SendResident(ctx, "meeting", r.Key, r.Resident, tg, r.Text, kb); err != nil {
			log.Printf("bot meeting reminder %s: %v", r.Key, err)
			continue
		}
		sent = append(sent, r)
	}
	return sent, nil
}

// Team reminders: an hour before every meeting the bot tells the team
// (Рустам, Береке). They replace the Google Calendar's own notifications.
// Residents meeting at the same time (the offline day) come as one message.

// TeamReminder is one message to the team.
type TeamReminder struct {
	When      time.Time `json:"when"`
	Residents []string  `json:"residents"`
	Text      string    `json:"text"`
	Key       string    `json:"key"`
}

// TeamReminders lists what to tell the team at now: meetings starting within
// the next hour (65 minutes), not yet marked as held.
func TeamReminders(meetings []club.Meeting, now time.Time) []TeamReminder {
	a := now.In(club.Almaty)
	type slot struct {
		at     time.Time
		names  []string
		online []club.Meeting
		addr   string
	}
	slots := map[string]*slot{}
	var order []string
	for _, m := range meetings {
		if m.Done || strings.TrimSpace(m.Resident) == "" {
			continue
		}
		at := meetingStart(m)
		if !at.After(a) || at.Sub(a) > hourBeforeMin*time.Minute {
			continue
		}
		k := at.Format("2006-01-02T15:04")
		sl := slots[k]
		if sl == nil {
			sl = &slot{at: at}
			slots[k] = sl
			order = append(order, k)
		}
		sl.names = append(sl.names, strings.TrimSpace(m.Resident))
		if strings.Contains(m.Link, "http") {
			sl.online = append(sl.online, m)
		} else if sl.addr == "" {
			sl.addr = strings.TrimSpace(m.Link)
			if sl.addr == "" {
				sl.addr = strings.TrimSpace(m.Place)
			}
		}
	}
	var out []TeamReminder
	for _, k := range order {
		sl := slots[k]
		var b strings.Builder
		fmt.Fprintf(&b, "⏰ Через час встреча, %s\n\n", sl.at.Format("15:04"))
		if len(sl.names) > 1 && len(sl.online) == 0 {
			fmt.Fprintf(&b, "Офлайн день · %d резидентов\n• %s\n", len(sl.names), strings.Join(sl.names, "\n• "))
			if sl.addr != "" {
				fmt.Fprintf(&b, "\n📍 %s", sl.addr)
			}
		} else {
			for _, m := range sl.online {
				fmt.Fprintf(&b, "%s · онлайн\n🔗 %s\n", strings.TrimSpace(m.Resident), strings.TrimSpace(m.Link))
			}
			for _, n := range sl.names {
				isOnline := false
				for _, m := range sl.online {
					if strings.TrimSpace(m.Resident) == n {
						isOnline = true
					}
				}
				if !isOnline {
					fmt.Fprintf(&b, "%s · офлайн", n)
					if sl.addr != "" {
						fmt.Fprintf(&b, "\n📍 %s", sl.addr)
					}
					b.WriteString("\n")
				}
			}
		}
		out = append(out, TeamReminder{When: sl.at, Residents: sl.names, Text: strings.TrimSpace(b.String()), Key: "team1h|" + k})
	}
	return out
}

// maybeTeamReminders sends them. Runs whatever the rollout says: the script
// never reminded the team, so there is nothing to double. Skipped when the
// imported schedule is older than 3 hours (it could be out of date).
func (s *Service) maybeTeamReminders(ctx context.Context, now time.Time) ([]TeamReminder, error) {
	if len(s.admins) == 0 {
		return nil, nil
	}
	if imp, err := s.repo.LastImportAt(ctx); err != nil || imp == nil || now.Sub(*imp) > 3*time.Hour {
		return nil, err
	}
	snap, err := s.repo.Club().Load(ctx)
	if err != nil {
		return nil, err
	}
	var sent []TeamReminder
	for _, r := range TeamReminders(snap.Meetings, now) {
		if !s.once(ctx, "meet:"+r.Key, 30*24*time.Hour) {
			continue
		}
		for _, id := range s.admins {
			if err := s.SendMessage(ctx, id, r.Text); err != nil {
				log.Printf("bot team reminder %s → %d: %v", r.Key, id, err)
			}
		}
		sent = append(sent, r)
	}
	return sent, nil
}
