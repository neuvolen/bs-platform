package bot

import (
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/bnursik/business_surgery_backend/internal/club"
)

// R67: the bot asks «встреча прошла?» only about meetings nobody marked on
// the server, and its button opens that meeting in the app.
func TestR67MeetDoneAsks(t *testing.T) {
	d := time.Date(2026, 10, 9, 0, 0, 0, 0, club.Almaty)
	ms := []club.Meeting{
		{Resident: "Альтаир", Date: d, Time: "15:00", Done: true}, // «Встреча прошла» in the app
		{Resident: "Асет", Date: d, Time: "15:00"},
		{Resident: "Дана", Date: d, Time: "11:00"},  // in «Лог встреч» (markAttendance)
		{Resident: "Ержан", Date: d, Time: "11:00"}, // the same offline slot, not marked
		{Resident: "Мадина", Date: d, Time: "17:00"},
		{Resident: "Тимур", Date: d.AddDate(0, 0, -1), Time: "10:00"}, // yesterday: too old
	}
	logged := map[string]bool{club.NormName("Дана") + "|2026-10-09": true}
	now := time.Date(2026, 10, 9, 16, 45, 0, 0, club.Almaty)
	got := MeetDoneAsks(ms, logged, now)
	if len(got) != 2 {
		t.Fatalf("asks %+v", got)
	}
	if got[0].Key != "meetask|2026-10-09T11:00" || strings.Join(got[0].Names, ",") != "Ержан" {
		t.Fatalf("first %+v", got[0])
	}
	if got[1].Key != "meetask|2026-10-09T15:00" || strings.Join(got[1].Names, ",") != "Асет" || !strings.Contains(got[1].Text, "с Асет прошла?") {
		t.Fatalf("second %+v", got[1])
	}
	u, err := url.Parse(got[1].Link)
	if err != nil || !strings.HasPrefix(got[1].Link, WebAppBase+"?") || u.Query().Get("p") != "meet_20261009_1500" || u.Query().Get("r") != "Асет" {
		t.Fatalf("link %s", got[1].Link)
	}
	// 17:00 is not asked yet (90 minutes), nothing at all once all are marked
	for i := range ms {
		ms[i].Done = true
	}
	if a := MeetDoneAsks(ms, logged, now); len(a) != 0 {
		t.Fatalf("asked about marked meetings %+v", a)
	}
	if l := AppLink("pay", "Альтаир"); !strings.Contains(l, "p=pay") || !strings.Contains(l, "r=%D0%90") {
		t.Fatalf("app link %s", l)
	}
}
