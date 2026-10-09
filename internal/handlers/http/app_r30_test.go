package http

import (
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	pg "github.com/bnursik/business_surgery_backend/internal/repository/pg"
)

// R30: the app's «Цели и задачи» is the platform's bs_kanban (team only), and
// the app's menu follows the platform's sections.
func TestAppTeamDocAndNav(t *testing.T) {
	g, _, r, now := newGateway(t)
	fs := &fakeSync{docs: map[string]*pg.PlatformDoc{
		"club/bs_kanban": {Scope: "club", Key: "bs_kanban", Value: `{"cards":[{"id":"k1","t":"Запустить рекламу","col":"work"}]}`, Version: 4},
	}}
	g.Sync = fs
	req := func(method, path, body string, id int64) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		q := url.Values{"_tg": {makeInitData(testBotToken, id, "X", *now)}}
		r.ServeHTTP(w, httptest.NewRequest(method, path+"&"+q.Encode(), strings.NewReader(body)))
		return w
	}
	if w := req("GET", "/api/v1/app/team/doc?key=bs_kanban", "", 453800951); w.Code != 200 || !strings.Contains(w.Body.String(), "Запустить рекламу") || !strings.Contains(w.Body.String(), `"version":4`) {
		t.Fatalf("team read: %d %s", w.Code, w.Body.String())
	}
	if w := req("GET", "/api/v1/app/team/doc?key=bs_kanban", "", 111); w.Code != 403 {
		t.Fatalf("resident must not read the team board: %d", w.Code)
	}
	if w := req("GET", "/api/v1/app/team/doc?key=bs_crm", "", 453800951); w.Code != 400 {
		t.Fatalf("only listed keys: %d", w.Code)
	}
	if w := req("PUT", "/api/v1/app/team/doc?key=bs_kanban", `{"value":"{\"nope\":1}","version":4}`, 453800951); w.Code != 400 {
		t.Fatalf("a board without cards is refused: %d", w.Code)
	}
	if w := req("PUT", "/api/v1/app/team/doc?key=bs_kanban", `{"value":"{\"cards\":[]}","version":3}`, 453800951); w.Code != 409 || !strings.Contains(w.Body.String(), "Запустить рекламу") {
		t.Fatalf("stale write: %d %s", w.Code, w.Body.String())
	}
	if w := req("PUT", "/api/v1/app/team/doc?key=bs_kanban", `{"value":"{\"cards\":[{\"id\":\"k2\",\"t\":\"Из приложения\"}]}","version":4}`, 453800951); w.Code != 200 {
		t.Fatalf("write: %d %s", w.Code, w.Body.String())
	}
	if d := fs.docs["club/bs_kanban"]; d.Version != 5 || !strings.Contains(d.Value, "Из приложения") {
		t.Fatalf("stored: %+v", d)
	}
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest("GET", "/api/v1/app/nav", nil))
	body := w.Body.String()
	if w.Code != 200 || !strings.Contains(body, `"id":"pl"`) || strings.Contains(body, `"id":"fines"`) /* R69: штрафы в «Учёте» */ || strings.Contains(body, `"gdoc"`) || strings.Contains(body, `"gsheet"`) {
		t.Fatalf("nav: %d %.300s", w.Code, body)
	}
}

func TestClubActionParityParams(t *testing.T) {
	for _, c := range []struct {
		action string
		p      map[string]string
		bad    bool
	}{
		{"deleteFine", map[string]string{"name": "Асет", "date": "01.09.2026", "type": "Не сдан отчёт", "amount": "10000"}, false},
		{"deleteFine", map[string]string{"name": "Асет", "amount": "10000"}, true},
		{"updateFine", map[string]string{"name": "Асет", "date": "01.09.2026", "amount": "10000"}, false},
		{"deleteSchedule", map[string]string{"res": "Асет", "date": "02.10.2026", "time": "12:00"}, false},
		{"updateMeeting", map[string]string{"oldRes": "Асет", "oldDate": "02.10.2026", "newDate": "2026-10-03"}, true},
		{"updateMeeting", map[string]string{"oldRes": "Асет", "oldDate": "02.10.2026", "newTime": "15:00"}, false},
		{"markAttendance", map[string]string{"names": "Асет", "date": "02.10.2026"}, false},
		{"addOfflineGroup", map[string]string{"date": "05.10.2026"}, true},
		{"setMeetings", map[string]string{"name": "Асет", "done": "2", "granted": "6"}, false},
		{"setMeetings", map[string]string{"name": "Асет", "done": "-1", "granted": "6"}, true},
		{"renewMeetings", map[string]string{}, true},
		{"addResident", map[string]string{"name": "Новый", "tariff": "100000", "visits": "3", "format": "Онлайн"}, false},
		{"addResident", map[string]string{"tariff": "100000"}, true},
	} {
		if _, ok := clubActions[c.action]; !ok {
			t.Fatalf("%s is not allowed from the platform", c.action)
		}
		msg := validateClubAction(c.action, c.p)
		if (msg != "") != c.bad {
			t.Errorf("%s %v: %q", c.action, c.p, msg)
		}
	}
	p := map[string]string{"name": "Асет", "date": "01.09.2026", "type": "Опоздание", "amount": "5000"}
	_ = validateClubAction("deleteFine", p)
	if p["kind"] != "Опоздание" {
		t.Fatalf("kind must follow type (the script finds the fine by kind): %v", p)
	}
}
