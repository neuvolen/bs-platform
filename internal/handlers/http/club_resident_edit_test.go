package http

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/bnursik/business_surgery_backend/internal/bot"
	"github.com/bnursik/business_surgery_backend/pkg/auth"
	"github.com/gin-gonic/gin"
)

func TestResidentEditParams(t *testing.T) {
	list := sheetSnap().Residents
	cases := []struct {
		field, value, action, want string
		bad                        bool
	}{
		{"exception", "true", "setResidentField", "Да", false},
		{"exception", "нет", "setResidentField", "Нет", false},
		{"exception", "может", "", "", true},
		{"format", "online", "setResidentField", "Онлайн", false},
		{"format", "гибрид", "", "", true},
		{"joinedAt", "2026-09-01", "setResidentField", "01.09.2026", false},
		{"joinedAt", "31.02.2026", "", "", true},
		{"joinedAt", "01.01.2019", "", "", true},
		{"chatId", " 4906856050 ", "setResidentField", "4906856050", false},
		{"chatId", "", "setResidentField", "", false},
		{"chatId", "@altair", "", "", true},
		{"chatId", "-100", "", "", true},
		{"chatId", "478757502", "", "", true}, // Асет's
		{"partner", "асет", "setPartner", "Асет", false},
		{"partner", "Альтаир", "", "", true},
		{"partner", "Никто", "", "", true},
		{"tariff", "1", "", "", true},
	}
	for _, c := range cases {
		a, p, _, err := ResidentEditParams(list, "Альтаир", c.field, c.value)
		if c.bad != (err != nil) {
			t.Fatalf("%s=%q: err %v", c.field, c.value, err)
		}
		if c.bad {
			continue
		}
		got := p["value"]
		if a == "setPartner" {
			got = p["partner"]
		}
		if a != c.action || got != c.want || p["name"] != "Альтаир" {
			t.Fatalf("%s=%q: %s %v", c.field, c.value, a, p)
		}
	}
	if _, _, _, err := ResidentEditParams(list, "Нет такого", "format", "Онлайн"); err == nil {
		t.Fatal("unknown resident")
	}
}

func TestResidentEditWritePath(t *testing.T) {
	e := newClubEnv(t)
	ctx := context.Background()
	h := NewClubActionHandler(e.g, e.repo, nil, "")
	gin.SetMode(gin.TestMode)
	r := gin.New()
	NewClubResidentModule(h, []byte("res-edit-secret")).Register(r)
	tok := func(sub, role string) string {
		acc, _, _ := auth.NewManager("res-edit-secret", time.Hour, time.Hour).GenerateTokens(sub, role, nil)
		return acc
	}
	admin := tok("tg:453800951", "admin")
	do := func(tk, method, path, body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, path, bytes.NewBufferString(body))
		req.Header.Set("Authorization", "Bearer "+tk)
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		return w
	}
	edit := func(body string) *httptest.ResponseRecorder {
		return do(admin, http.MethodPost, "/api/v1/club/resident", body)
	}
	reminded := func() bool {
		s, err := e.repo.Load(ctx)
		if err != nil {
			t.Fatal(err)
		}
		for _, x := range bot.EveningTargets(s.Residents, nil) {
			if x.Name == "Альтаир" {
				return true
			}
		}
		return false
	}

	// Team only.
	if w := do(tok("tg:490685605", "resident"), http.MethodGet, "/api/v1/club/residents", ``); w.Code != http.StatusForbidden {
		t.Fatalf("resident reads: %d", w.Code)
	}
	if w := do(tok("tg:490685605", "resident"), http.MethodPost, "/api/v1/club/resident", `{"name":"Альтаир","field":"exception","value":true}`); w.Code != http.StatusForbidden {
		t.Fatalf("resident writes: %d", w.Code)
	}
	w := do(admin, http.MethodGet, "/api/v1/club/residents", ``)
	var list struct {
		Residents []ResidentCard `json:"residents"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &list)
	if w.Code != 200 || len(list.Residents) != 3 || list.Residents[2].Name != "Альтаир" || list.Residents[2].ChatID != "490685605" ||
		list.Residents[2].JoinedAt != "01.06.2026" || list.Residents[2].Exception {
		t.Fatalf("list: %d %s", w.Code, w.Body.String())
	}

	// Exception on: saved on the server at once, the bot stops reminding, the sheet gets it, the journal has it.
	if !reminded() {
		t.Fatal("reminded before")
	}
	w = edit(`{"name":"Альтаир","field":"exception","value":true}`)
	if w.Code != 200 || e.count(t, `SELECT count(*) FROM club_residents WHERE name = 'Альтаир' AND exception`) != 1 {
		t.Fatalf("exception: %d %s", w.Code, w.Body.String())
	}
	if reminded() {
		t.Fatal("an exception is not reminded")
	}
	q := e.f.calls[len(e.f.calls)-1]
	if q.Get("action") != "setResidentField" || q.Get("field") != "exception" || q.Get("value") != "Да" || q.Get("name") != "Альтаир" {
		t.Fatalf("sent %v", q)
	}
	if e.count(t, `SELECT count(*) FROM club_ops WHERE action = 'setResidentField' AND params->>'edit' = 'exception'
		AND params->>'prev' = 'Нет' AND params->>'value' = 'Да' AND ok`) != 1 {
		t.Fatal("journal")
	}
	// The same value again: nothing sent.
	n := len(e.f.actions())
	if w := edit(`{"name":"Альтаир","field":"exception","value":"Да"}`); w.Code != 200 || len(e.f.actions()) != n {
		t.Fatalf("unchanged: %d %s", w.Code, w.Body.String())
	}

	// Format, entry date, chat id; bad values are refused before anything is kept.
	for _, b := range []string{`{"name":"Альтаир","field":"format","value":"Гибрид"}`, `{"name":"Альтаир","field":"joinedAt","value":"32.01.2026"}`,
		`{"name":"Альтаир","field":"chatId","value":"12ab"}`, `{"name":"Альтаир","field":"tariff","value":"1"}`} {
		if w := edit(b); w.Code != 400 {
			t.Fatalf("%s: %d", b, w.Code)
		}
	}
	if w := edit(`{"name":"Альтаир","field":"format","value":"Онлайн"}`); w.Code != 200 {
		t.Fatalf("format: %d %s", w.Code, w.Body.String())
	}
	if w := edit(`{"name":"Альтаир","field":"joinedAt","value":"15.08.2026"}`); w.Code != 200 {
		t.Fatalf("date: %d %s", w.Code, w.Body.String())
	}
	if e.f.calls[len(e.f.calls)-1].Get("value") != "15.08.2026" {
		t.Fatalf("date sent %v", e.f.calls[len(e.f.calls)-1])
	}
	w = edit(`{"name":"Альтаир","field":"chatId","value":"4906856050"}`)
	var out struct {
		Resident ResidentCard `json:"resident"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &out)
	if w.Code != 200 || out.Resident.ChatID != "4906856050" || out.Resident.Format != "Онлайн" || out.Resident.JoinedAt != "15.08.2026" {
		t.Fatalf("chat id: %d %s", w.Code, w.Body.String())
	}
	if e.count(t, `SELECT count(*) FROM club_residents WHERE name = 'Альтаир' AND format = 'Онлайн' AND joined_at = '2026-08-15' AND tg_id = 4906856050`) != 1 {
		t.Fatal("server tables")
	}

	// Partner: both sides, as setPartner; then none.
	if w := edit(`{"name":"Альтаир","field":"partner","value":"Асет"}`); w.Code != 200 ||
		e.count(t, `SELECT count(*) FROM club_residents WHERE (name = 'Альтаир' AND partner = 'Асет') OR (name = 'Асет' AND partner = 'Альтаир')`) != 2 {
		t.Fatalf("partner: %d %s", w.Code, w.Body.String())
	}
	if q := e.f.calls[len(e.f.calls)-1]; q.Get("action") != "setPartner" || q.Get("partner") != "Асет" {
		t.Fatalf("sent %v", q)
	}
	if w := edit(`{"name":"Альтаир","field":"partner","value":""}`); w.Code != 200 ||
		e.count(t, `SELECT count(*) FROM club_residents WHERE partner <> ''`) != 0 {
		t.Fatalf("no partner: %d %s", w.Code, w.Body.String())
	}

	// The script is down: kept on the server, queued, and an import of the old sheet does not lose it.
	e.g.scriptURL = scriptDown
	w = edit(`{"name":"Альтаир","field":"exception","value":false}`)
	if w.Code != 200 || !bytes.Contains(w.Body.Bytes(), []byte(`"queued":true`)) || !reminded() {
		t.Fatalf("queued: %d %s", w.Code, w.Body.String())
	}
	old := sheetSnap()
	old.Residents[2].Exception = true
	importSheet(t, e.repo, old, e.now.Add(-time.Minute))
	if !reminded() {
		t.Fatal("the import undid a waiting change")
	}
	e.g.scriptURL = e.srvURL
	*e.now = e.now.Add(time.Minute)
	if n, err := e.writes.Flush(ctx); err != nil || n != 1 {
		t.Fatalf("flush %d %v", n, err)
	}
	if q := e.f.calls[len(e.f.calls)-1]; q.Get("field") != "exception" || q.Get("value") != "Нет" {
		t.Fatalf("flushed %v", q)
	}

	// The script refuses: the server's change is undone, the editor gets the reason.
	e.f.set("setResidentField", "error")
	if w := edit(`{"name":"Альтаир","field":"format","value":"Офлайн"}`); w.Code != 422 ||
		e.count(t, `SELECT count(*) FROM club_residents WHERE name = 'Альтаир' AND format = 'Офлайн'`) != 0 {
		t.Fatalf("refused: %d %s", w.Code, w.Body.String())
	}
}
