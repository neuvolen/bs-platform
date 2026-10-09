package gcal

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/bnursik/business_surgery_backend/internal/club"
)

type memStore struct {
	mu sync.Mutex
	m  map[string]string
}

func (s *memStore) GetMeta(_ context.Context, k string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.m[k], nil
}
func (s *memStore) SetMeta(_ context.Context, k, v string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.m[k] = v
	return nil
}

// fakeGoogle: the token endpoint and the Calendar API, enough for the sync.
type fakeGoogle struct {
	mu       sync.Mutex
	events   map[string]*Event
	patches  []string // "id query body"
	inserts  []string
	listQ    []string
	calPatch []string
	revoked  bool
}

func (f *fakeGoogle) handler(t *testing.T) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		body, _ := io.ReadAll(r.Body)
		switch {
		case r.URL.Path == "/token":
			v, _ := url.ParseQuery(string(body))
			if f.revoked {
				w.WriteHeader(400)
				_, _ = w.Write([]byte(`{"error":"invalid_grant","error_description":"Token has been expired or revoked."}`))
				return
			}
			if v.Get("grant_type") == "authorization_code" {
				_, _ = w.Write([]byte(`{"access_token":"at1","refresh_token":"rt1","expires_in":3600,"scope":"https://www.googleapis.com/auth/calendar"}`))
				return
			}
			if v.Get("refresh_token") != "rt1" {
				t.Errorf("refresh with %q", v.Get("refresh_token"))
			}
			_, _ = w.Write([]byte(`{"access_token":"at2","expires_in":3600}`))
		case r.URL.Path == "/api/users/me/calendarList" && r.Method == "GET":
			_, _ = w.Write([]byte(`{"items":[{"id":"primary@x","summary":"Me","accessRole":"owner","primary":true},{"id":"bs@group.calendar.google.com","summary":"Business Surgery Meetings","accessRole":"owner"}]}`))
		case strings.HasPrefix(r.URL.Path, "/api/users/me/calendarList/") && r.Method == "PATCH":
			f.calPatch = append(f.calPatch, string(body))
			_, _ = w.Write([]byte(`{}`))
		case r.URL.Path == "/api/calendars/bs@group.calendar.google.com/events" && r.Method == "GET":
			f.listQ = append(f.listQ, r.URL.RawQuery)
			from, _ := time.Parse(time.RFC3339, r.URL.Query().Get("timeMin"))
			to, _ := time.Parse(time.RFC3339, r.URL.Query().Get("timeMax"))
			var items []Event
			for _, e := range f.events {
				if st := e.StartAt(); !st.Before(from) && st.Before(to) {
					items = append(items, *e)
				}
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"items": items})
		case strings.HasPrefix(r.URL.Path, "/api/calendars/bs@group.calendar.google.com/events/") && r.Method == "PATCH":
			if r.URL.Query().Get("sendUpdates") != "none" {
				t.Errorf("patch without sendUpdates=none: %s", r.URL.RawQuery)
			}
			id := strings.TrimPrefix(r.URL.Path, "/api/calendars/bs@group.calendar.google.com/events/")
			f.patches = append(f.patches, id+" "+string(body))
			e := f.events[id]
			var p map[string]json.RawMessage
			_ = json.Unmarshal(body, &p)
			if v, ok := p["summary"]; ok {
				_ = json.Unmarshal(v, &e.Summary)
			}
			if v, ok := p["colorId"]; ok {
				_ = json.Unmarshal(v, &e.ColorID)
			}
			if v, ok := p["reminders"]; ok {
				e.Reminders = &Reminders{}
				_ = json.Unmarshal(v, e.Reminders)
			}
			if v, ok := p["start"]; ok {
				_ = json.Unmarshal(v, &e.Start)
			}
			_ = json.NewEncoder(w).Encode(e)
		case r.URL.Path == "/api/calendars/bs@group.calendar.google.com/events" && r.Method == "POST":
			if r.URL.Query().Get("sendUpdates") != "none" {
				t.Errorf("insert without sendUpdates=none: %s", r.URL.RawQuery)
			}
			f.inserts = append(f.inserts, r.URL.RawQuery+" "+string(body))
			var e Event
			_ = json.Unmarshal(body, &e)
			e.ID = "new1"
			if r.URL.Query().Get("conferenceDataVersion") == "1" {
				e.HangoutLink = "https://meet.google.com/abc-defg-hij"
			}
			f.events[e.ID] = &e
			_ = json.NewEncoder(w).Encode(e)
		default:
			t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
			w.WriteHeader(404)
		}
	})
}

func ev(id, title, start, color string, rem *Reminders) *Event {
	return &Event{ID: id, Summary: title, ColorID: color, Start: EventTime{DateTime: start}, End: EventTime{DateTime: start}, Reminders: rem,
		Attendees: []Attendee{{Email: "owner@example.com", Self: true, Organizer: true}, {Email: "team@example.com", ResponseStatus: "accepted"}}}
}

func setup(t *testing.T) (*Sync, *fakeGoogle, *memStore) {
	f := &fakeGoogle{events: map[string]*Event{}}
	srv := httptest.NewServer(f.handler(t))
	t.Cleanup(srv.Close)
	st := &memStore{m: map[string]string{}}
	c := New(st)
	c.TokenURL, c.AuthURL, c.API = srv.URL+"/token", srv.URL+"/auth", srv.URL+"/api"
	c.Now = func() time.Time { return time.Date(2026, 10, 9, 18, 0, 0, 0, club.Almaty) }
	t.Setenv("GOOGLE_CAL_CLIENT_ID", "")
	t.Setenv("GOOGLE_CAL_CLIENT_SECRET", "")
	t.Setenv("GCAL_CALENDAR_ID", "")
	return &Sync{C: c}, f, st
}

func TestConsentAndMarkDone(t *testing.T) {
	s, f, st := setup(t)
	ctx := context.Background()
	if s.C.Connected(ctx) {
		t.Fatal("connected without consent")
	}
	s.OnWrite(ctx, "confirmMeeting", map[string]string{"res": "Альтаир", "date": "09.10.2026", "time": "15:00"}) // no-op, no calls
	if err := s.C.SaveCreds(ctx, Creds{ID: "x", Secret: "y"}); err == nil {
		t.Fatal("a wrong client id was kept")
	}
	if err := s.C.SaveCreds(ctx, Creds{ID: " 123-abc.apps.googleusercontent.com ", Secret: "GOCSPX-secret123"}); err != nil {
		t.Fatal(err)
	}
	u, err := s.C.ConsentURL(ctx, "https://app.bxclub.kz/api/v1/gcal/callback", "k1")
	if err != nil || !strings.Contains(u, "access_type=offline") || !strings.Contains(u, "prompt=consent") || !strings.Contains(u, url.QueryEscape(Scope)) {
		t.Fatalf("consent url %s %v", u, err)
	}
	if err := s.C.Exchange(ctx, "code1", "https://app.bxclub.kz/api/v1/gcal/callback"); err != nil {
		t.Fatal(err)
	}
	if !s.C.Connected(ctx) || !strings.Contains(st.m[MetaToken], "rt1") {
		t.Fatal("token not kept")
	}

	loud := &Reminders{UseDefault: false, Overrides: []Reminder{{"popup", 60}, {"email", 60}}}
	f.events["a"] = ev("a", "Встреча BS. Альтаир", "2026-10-09T15:00:00+05:00", "11", loud)
	f.events["b"] = ev("b", "Встреча BS. Асет Нурланов", "2026-10-09T16:00:00+05:00", "11", loud)
	f.events["g"] = ev("g", "Офлайн день BS. 3 резидентов", "2026-10-08T11:00:00+05:00", "11", loud)
	f.events["g"].Description = "👥 Резиденты (3):\n• Дана\n• Ержан\n• Мадина"
	f.events["x"] = ev("x", "Стоматолог", "2026-10-09T15:00:00+05:00", "", nil)

	// «Встреча прошла» in the app: Альтаир's event only, green, ✅, no reminders
	r, err := s.MarkDone(ctx, []Mark{{Res: "Альтаир", Day: time.Date(2026, 10, 9, 0, 0, 0, 0, club.Almaty), Time: "15:00"}})
	if err != nil || len(r.Painted) != 1 {
		t.Fatalf("mark %+v %v", r, err)
	}
	if a := f.events["a"]; a.Summary != "✅ Встреча BS. Альтаир" || a.ColorID != DoneColor || !a.IsQuiet() {
		t.Fatalf("event a %+v %+v", a, a.Reminders)
	}
	if f.events["b"].ColorID != "11" || f.events["x"].ColorID != "" {
		t.Fatal("another event was painted")
	}
	// again: idempotent, nothing patched
	n := len(f.patches)
	r, _ = s.MarkDone(ctx, []Mark{{Res: "Альтаир", Day: time.Date(2026, 10, 9, 0, 0, 0, 0, club.Almaty), Time: "15:00"}})
	if len(f.patches) != n || r.Already != 1 || strings.Count(f.events["a"].Summary, "✅") != 1 {
		t.Fatalf("second mark patched again: %+v", r)
	}
	// offline day: markAttendance paints the day's group event once
	s.OnWrite(ctx, "markAttendance", map[string]string{"names": "Дана|Ержан", "date": "08.10.2026", "time": "11:00"})
	if g := f.events["g"]; g.Summary != "✅ Офлайн день BS. 3 резидентов" || g.ColorID != DoneColor {
		t.Fatalf("offline day %+v", g)
	}
	if st.m[MetaCalendar] != "bs@group.calendar.google.com" {
		t.Fatalf("calendar %q", st.m[MetaCalendar])
	}
	// first name only (the script matched it too), another day's event is not taken
	f.events["c"] = ev("c", "Встреча BS. Мадина", "2026-10-07T12:00:00+05:00", "11", nil)
	r, _ = s.MarkDone(ctx, []Mark{{Res: "Мадина Сапарова", Day: time.Date(2026, 10, 7, 0, 0, 0, 0, club.Almaty), Time: "12:00"}})
	if len(r.Painted) != 1 || f.events["c"].ColorID != DoneColor {
		t.Fatalf("first name %+v", r)
	}
	r, _ = s.MarkDone(ctx, []Mark{{Res: "Асет Нурланов", Day: time.Date(2026, 10, 10, 0, 0, 0, 0, club.Almaty), Time: "16:00"}})
	if len(r.NotFound) != 1 || f.events["b"].ColorID != "11" {
		t.Fatalf("another day's event was painted %+v", r)
	}
}

func TestBackfillQuietAndCreate(t *testing.T) {
	s, f, st := setup(t)
	ctx := context.Background()
	_ = s.C.SaveCreds(ctx, Creds{ID: "1-a.apps.googleusercontent.com", Secret: "GOCSPX-secret123"})
	_ = st.SetMeta(ctx, MetaToken, `{"refresh":"rt1"}`)
	day := func(d int) time.Time { return time.Date(2026, 10, d, 0, 0, 0, 0, club.Almaty) }
	snap := &club.Snapshot{
		Residents: []club.Resident{{Name: "Альтаир", Format: "Онлайн"}, {Name: "Дана", Format: "Офлайн"}},
		Meetings: []club.Meeting{
			{Resident: "Альтаир", Date: day(6), Time: "15:00", Done: true},
			{Resident: "Альтаир", Date: day(1), Time: "15:00", Done: true},  // 8 days ago: painted too (14 days)
			{Resident: "Альтаир", Date: day(12), Time: "15:00", Done: false}, // future
		},
		MeetingLog: []club.MeetingLogEntry{{Resident: "Дана", Date: day(8)}, {Resident: "Дана", Date: time.Date(2026, 9, 1, 0, 0, 0, 0, club.Almaty)}},
	}
	s.Load = func(context.Context) (*club.Snapshot, error) { return snap, nil }
	loud := &Reminders{UseDefault: false, Overrides: []Reminder{{"popup", 60}, {"email", 60}}}
	f.events["a6"] = ev("a6", "Встреча BS. Альтаир", "2026-10-06T15:00:00+05:00", "11", loud)
	f.events["a1"] = ev("a1", "Встреча BS. Альтаир", "2026-10-01T15:00:00+05:00", "11", loud)
	f.events["d8"] = ev("d8", "Встреча BS. Дана", "2026-10-08T11:00:00+05:00", "11", loud)
	f.events["s1"] = ev("s1", "Встреча BS. Дана", "2026-09-01T11:00:00+05:00", "11", loud)
	f.events["a12"] = ev("a12", "Встреча BS. Альтаир", "2026-10-12T15:00:00+05:00", "11", loud)
	f.events["d"] = ev("d", "Встреча BS. Дана", "2026-10-20T11:00:00+05:00", "11", &Reminders{UseDefault: true})
	f.events["p"] = ev("p", "Личное", "2026-10-20T11:00:00+05:00", "", &Reminders{UseDefault: true})
	var told string
	msg, err := s.Catchup(ctx)
	if err != nil {
		t.Fatal(err)
	}
	told = msg
	for _, id := range []string{"a6", "a1", "d8"} {
		if !f.events[id].IsDone() {
			t.Fatalf("%s not painted", id)
		}
	}
	if f.events["s1"].IsDone() || f.events["a12"].IsDone() {
		t.Fatal("an old or a future meeting was painted")
	}
	if !f.events["a12"].IsQuiet() || !f.events["d"].IsQuiet() || f.events["p"].IsQuiet() {
		t.Fatal("future club events not quiet, or a private event touched")
	}
	if len(f.calPatch) != 1 || !strings.Contains(f.calPatch[0], `"defaultReminders":[]`) {
		t.Fatalf("calendar defaults %v", f.calPatch)
	}
	if !strings.Contains(told, "Окрашено прошедших встреч за 14 дней: 3") || !strings.Contains(told, "убраны у будущих встреч: 2") {
		t.Fatalf("message %q", told)
	}
	if again, _ := s.Catchup(ctx); again != "" {
		t.Fatal("catch-up ran twice")
	}

	// a new online meeting: an event with Meet, quiet, the team as guests, the link kept
	var gotLink, gotID string
	s.SetLink = func(_ context.Context, res string, d time.Time, link, id string) error {
		gotLink, gotID = link, id
		return nil
	}
	snap.Meetings = append(snap.Meetings, club.Meeting{Resident: "Альтаир", Date: day(15), Time: "10:00"})
	s.OnWrite(ctx, "addSchedule", map[string]string{"res": "Альтаир", "date": "15.10.2026", "time": "10:00"})
	if len(f.inserts) != 1 || gotLink != "https://meet.google.com/abc-defg-hij" || gotID != "new1" {
		t.Fatalf("insert %v link %q id %q", f.inserts, gotLink, gotID)
	}
	in := f.inserts[0]
	if !strings.Contains(in, "conferenceDataVersion=1") || !strings.Contains(in, `"reminders":{"useDefault":false,"overrides":[]}`) ||
		!strings.Contains(in, "team@example.com") || strings.Contains(in, "owner@example.com") || !strings.Contains(in, `"dateTime":"2026-10-15T10:00:00+05:00"`) {
		t.Fatalf("insert body %s", in)
	}
	// the same meeting again: no second event
	s.OnWrite(ctx, "addSchedule", map[string]string{"res": "Альтаир", "date": "15.10.2026", "time": "10:00"})
	if len(f.inserts) != 1 {
		t.Fatal("a duplicate event")
	}
	// moved: the event follows, quietly
	s.OnWrite(ctx, "updateMeeting", map[string]string{"oldRes": "Альтаир", "oldDate": "15.10.2026", "oldTime": "10:00", "newDate": "16.10.2026", "newTime": "11:30"})
	if e := f.events["new1"]; e.Start.DateTime != "2026-10-16T11:30:00+05:00" {
		t.Fatalf("moved %+v", e.Start)
	}
}

func TestRevokedConsent(t *testing.T) {
	s, f, st := setup(t)
	ctx := context.Background()
	_ = s.C.SaveCreds(ctx, Creds{ID: "1-a.apps.googleusercontent.com", Secret: "GOCSPX-secret123"})
	_ = st.SetMeta(ctx, MetaToken, `{"refresh":"rt1"}`)
	f.revoked = true
	_, err := s.MarkDone(ctx, []Mark{{Res: "Альтаир", Day: time.Date(2026, 10, 9, 0, 0, 0, 0, club.Almaty)}})
	if !NeedsConsent(err) || !strings.Contains(st.m[MetaError], "открыть ссылку подключения") {
		t.Fatalf("err %v meta %q", err, st.m[MetaError])
	}
}
