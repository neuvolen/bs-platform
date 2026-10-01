package http

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/bnursik/business_surgery_backend/internal/repository/pg"
)

type fakeDone struct{ rows []pg.DoneMeeting }

func (f *fakeDone) DoneMeetings(context.Context) ([]pg.DoneMeeting, error) { return f.rows, nil }

const doneBundle = `{"residents":[],"schedule":[
 {"res":"Мади Актобе","date":"29.09.2026","time":"10:00","link":"https://meet.google.com/a"},
 {"res":"Даулет Сайты","date":"29.09.2026","time":"11:00","link":"https://meet.google.com/b"},
 {"res":"Альтаир","date":"29.09.2026","time":"15:00","link":"г.Алматы, Достык 44"},
 {"res":"Асет","date":"29.09.2026","time":"15:00","link":"г.Алматы, Достык 44"},
 {"res":"Бакытжан","date":"30.09.2026","time":"12:00","link":"https://meet.google.com/c"}]}`

func scheduleOf(t *testing.T, r *gin.Engine, init string) []string {
	t.Helper()
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest("GET", "/api/v1/app/call?"+url.Values{"action": {"getBotCache"}, "_tg": {init}, "fresh": {"1"}}.Encode(), nil))
	var b struct {
		Schedule []struct{ Res, Date, Time string } `json:"schedule"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &b); err != nil {
		t.Fatalf("bundle: %v %s", err, w.Body.String())
	}
	out := []string{}
	for _, s := range b.Schedule {
		out = append(out, s.Res+" "+s.Time)
	}
	return out
}

func TestAppHidesFinishedMeetings(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("action") == "getBotCache" {
			_, _ = w.Write([]byte(doneBundle))
			return
		}
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer srv.Close()
	g := NewAppGateway(testBotToken, srv.URL+"/exec")
	g.Admins = map[int64]string{453800951: "Рустам"}
	// 29.09 13:00 in Almaty
	now := time.Date(2026, 9, 29, 8, 0, 0, 0, time.UTC)
	g.now = func() time.Time { return now }
	imp := &fakeDone{}
	g.Done = imp
	gin.SetMode(gin.TestMode)
	r := gin.New()
	NewAppGatewayModule(g).Register(r)
	init := makeInitData(testBotToken, 453800951, "Рустам", now)

	if got := scheduleOf(t, r, init); len(got) != 5 {
		t.Fatalf("nothing is done yet, all 5 stay: %v", got)
	}

	// Мади marked in the app: gone at once
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest("GET", "/api/v1/app/call?"+url.Values{"action": {"confirmMeeting"}, "_tg": {init},
		"res": {"Мади Актобе"}, "date": {"29.09"}, "time": {"10:00"}}.Encode(), nil))
	got := scheduleOf(t, r, init)
	if len(got) != 4 || got[0] != "Даулет Сайты 11:00" {
		t.Fatalf("after the app mark Мади must be gone: %v", got)
	}

	// Даулет marked in the bot, known from the import («Проведена»)
	imp.rows = []pg.DoneMeeting{{Resident: "Даулет Сайты", Date: "29.09", Time: "11:00"}}
	now = now.Add(2 * time.Minute) // the import is read at most once a minute
	got = scheduleOf(t, r, init)
	if len(got) != 3 || got[0] != "Альтаир 15:00" {
		t.Fatalf("after the import Даулет must be gone: %v", got)
	}

	// Offline day: attendance logged today, but the meeting has not started yet (13:00 < 15:00): stays
	imp.rows = append(imp.rows, pg.DoneMeeting{Resident: "Альтаир", Date: "29.09", Time: "*"})
	now = now.Add(2 * time.Minute)
	if got = scheduleOf(t, r, init); len(got) != 3 {
		t.Fatalf("a log line must not hide a meeting that has not started: %v", got)
	}
	// after 15:00 it goes, Асет without a log line stays, tomorrow's Бакытжан stays
	now = time.Date(2026, 9, 29, 10, 30, 0, 0, time.UTC)
	got = scheduleOf(t, r, init)
	if len(got) != 2 || got[0] != "Асет 15:00" || got[1] != "Бакытжан 12:00" {
		t.Fatalf("after the start the logged offline meeting goes: %v", got)
	}

	// markAttendance from the app hides Асет at once
	r.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("GET", "/api/v1/app/call?"+url.Values{"action": {"markAttendance"}, "_tg": {init},
		"names": {"Асет|Бакытжан"}, "date": {"29.09.2026"}}.Encode(), nil))
	if got = scheduleOf(t, r, init); len(got) != 1 || got[0] != "Бакытжан 12:00" {
		t.Fatalf("after attendance only tomorrow stays: %v", got)
	}
}

func TestHideDoneKeepsBundleWhenNothingDone(t *testing.T) {
	b := []byte(`{"schedule":[{"res":"A","date":"01.10.2026","time":"10:00"}],"x":1}`)
	if string(hideDone(b, map[string]bool{"b|01.10|10:00": true}, time.Now())) != string(b) {
		t.Fatal("untouched bundle must come back byte for byte")
	}
}
