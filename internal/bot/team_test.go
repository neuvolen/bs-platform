package bot

import (
	"strings"
	"testing"
	"time"

	"github.com/bnursik/business_surgery_backend/internal/club"
)

func TestTeamReminders(t *testing.T) {
	d := time.Date(2026, 9, 29, 0, 0, 0, 0, club.Almaty)
	ms := []club.Meeting{
		{Resident: "Мади Актобе", Date: d, Time: "10:00", Link: "https://meet.google.com/ouc", Online: true},
		{Resident: "Альтаир", Date: d, Time: "15:00", Link: "г.Алматы, Достык 44"},
		{Resident: "Асет", Date: d, Time: "15:00", Link: "г.Алматы, Достык 44"},
		{Resident: "Азат", Date: d, Time: "15:00", Link: "г.Алматы, Достык 44", Done: true},
		{Resident: "Бакытжан", Date: d.AddDate(0, 0, 1), Time: "14:30", Link: "https://meet.google.com/x"},
	}
	at := func(h, m int) time.Time { return time.Date(2026, 9, 29, h, m, 0, 0, club.Almaty) }

	if r := TeamReminders(ms, at(8, 0)); len(r) != 0 {
		t.Fatalf("2 hours before: nothing yet, got %v", r)
	}
	r := TeamReminders(ms, at(9, 0))
	if len(r) != 1 || !strings.Contains(r[0].Text, "Мади Актобе · онлайн") || !strings.Contains(r[0].Text, "/call/") { // R75 call
		t.Fatalf("an hour before the online meeting: %+v", r)
	}
	if r := TeamReminders(ms, at(10, 0)); len(r) != 0 {
		t.Fatalf("a started meeting is not reminded: %v", r)
	}
	r = TeamReminders(ms, at(14, 0))
	if len(r) != 1 || !strings.Contains(r[0].Text, "Офлайн день · 2 резидентов") || strings.Contains(r[0].Text, "Азат") || !strings.Contains(r[0].Text, "Достык 44") {
		t.Fatalf("offline day is one message without the held meeting: %+v", r)
	}
	t.Log("\n" + r[0].Text)
}
