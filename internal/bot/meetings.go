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
	ids := map[string]int64{}
	names := map[string]string{} // the resident's name as the debts sheet has it
	for _, r := range residents {
		if r.TgID != 0 && !r.Former && r.Name != "" {
			ids[club.NormName(r.Name)] = r.TgID
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
	for _, r := range DueReminders(snap.Meetings, snap.Residents, texts, now) {
		if !s.once(ctx, "meet:"+r.Key, 30*24*time.Hour) {
			continue
		}
		var kb map[string]any
		if r.Wheel {
			kb = map[string]any{"inline_keyboard": [][]map[string]any{{{"text": "🧬 Обновить колесо", "web_app": map[string]string{"url": webApp("wheel")}}}}}
		}
		if err := s.SendMessageKB(ctx, r.TgID, r.Text, kb); err != nil {
			log.Printf("bot meeting reminder %s: %v", r.Key, err)
			continue
		}
		sent = append(sent, r)
	}
	return sent, nil
}
