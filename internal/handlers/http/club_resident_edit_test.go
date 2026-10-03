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
	// The editor's answer comes at once; the sheet gets the change from the queue.
	edit := func(body string) *httptest.ResponseRecorder {
		w := do(admin, http.MethodPost, "/api/v1/club/resident", body)
		if _, err := e.writes.flush(ctx, true); err != nil { // what the loop does when woken
			t.Fatal(err)
		}
		return w
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

	// The script refuses (the sheet has no such resident): the server's change is undone.
	e.f.set("setResidentField", "error")
	if w := edit(`{"name":"Альтаир","field":"format","value":"Офлайн"}`); w.Code != 200 ||
		e.count(t, `SELECT count(*) FROM club_residents WHERE name = 'Альтаир' AND format = 'Офлайн'`) != 0 {
		t.Fatalf("refused: %d %s", w.Code, w.Body.String())
	}
}

// The owner's case: the app's deployment of the script is an old version
// that answers "Unknown action" (or does not know the field). The editor
// answers at once with the change saved, never shows the script's error; the
// change waits on the server without holding other club writes, goes through
// the bot's deployment when that one is new, and reaches the sheet once the
// script has updated itself.
func TestResidentEditOldScript(t *testing.T) {
	e := newClubEnv(t)
	ctx := context.Background()
	h := NewClubActionHandler(e.g, e.repo, nil, "")
	gin.SetMode(gin.TestMode)
	r := gin.New()
	NewClubResidentModule(h, []byte("res-edit-secret")).Register(r)
	acc, _, _ := auth.NewManager("res-edit-secret", time.Hour, time.Hour).GenerateTokens("tg:453800951", "admin", nil)
	edit := func(body string) (*httptest.ResponseRecorder, time.Duration) {
		req := httptest.NewRequest(http.MethodPost, "/api/v1/club/resident", bytes.NewBufferString(body))
		req.Header.Set("Authorization", "Bearer "+acc)
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		start := time.Now()
		r.ServeHTTP(w, req)
		return w, time.Since(start)
	}
	sent := func(field string) int {
		n := 0
		for _, q := range e.f.calls {
			if q.Get("action") == "setResidentField" && q.Get("field") == field {
				n++
			}
		}
		return n
	}

	// A slow script does not slow the editor: it does not wait for it.
	e.f.set("setResidentField", "unknown")
	w, took := edit(`{"name":"Альтаир","field":"joinedAt","value":"15.08.2026"}`)
	if w.Code != 200 || !bytes.Contains(w.Body.Bytes(), []byte(`"queued":true`)) || bytes.Contains(w.Body.Bytes(), []byte("nknown")) ||
		!bytes.Contains(w.Body.Bytes(), []byte(`"joinedAt":"15.08.2026"`)) {
		t.Fatalf("edit: %d %s", w.Code, w.Body.String())
	}
	if took > time.Second {
		t.Fatalf("the editor waited %v", took)
	}
	if e.count(t, `SELECT count(*) FROM club_residents WHERE name = 'Альтаир' AND joined_at = '2026-08-15'`) != 1 {
		t.Fatal("not saved on the server")
	}
	// The queue sends it: the script does not know it, the change stays and waits.
	if n, err := e.writes.flush(ctx, true); err != nil || n != 0 || sent("joinedAt") != 1 {
		t.Fatalf("flush %d %v, sent %d", n, err, sent("joinedAt"))
	}
	if e.count(t, `SELECT count(*) FROM club_writes WHERE action = 'setResidentField' AND status = 'pending' AND last_error LIKE 'скрипт ещё не знает%'`) != 1 ||
		e.count(t, `SELECT count(*) FROM club_residents WHERE name = 'Альтаир' AND joined_at = '2026-08-15'`) != 1 {
		t.Fatal("parked write lost the change")
	}
	// Another field with the v36 answer ("Поле … не меняется"): parked as well, saved.
	e.f.set("setResidentField", "oldfield")
	if w, _ := edit(`{"name":"Альтаир","field":"exception","value":"Да"}`); w.Code != 200 {
		t.Fatalf("exception: %d %s", w.Code, w.Body.String())
	}
	if _, err := e.writes.flush(ctx, true); err != nil {
		t.Fatal(err)
	}
	if e.count(t, `SELECT count(*) FROM club_residents WHERE name = 'Альтаир' AND exception`) != 1 ||
		e.count(t, `SELECT count(*) FROM club_writes WHERE status = 'rejected'`) != 0 {
		t.Fatal("the old script's answer undid the change")
	}
	// Other club writes are not held by the parked ones.
	before := len(e.f.actions())
	res := e.call(453800951, "addFine", "name", "Асет", "type", "Штраф", "amount", "10000")
	if res["ok"] != true || res["queued"] == true || len(e.f.actions()) != before+1 {
		t.Fatalf("a fine after a parked write: %v", res)
	}
	// An import of the old sheet meanwhile does not undo the waiting changes.
	importSheet(t, e.repo, sheetSnap(), e.now.Add(-time.Minute))
	if e.count(t, `SELECT count(*) FROM club_residents WHERE name = 'Альтаир' AND exception AND joined_at = '2026-08-15'`) != 1 {
		t.Fatal("the import undid a parked change")
	}
	// Not tried again before its time, even when a new write wakes the queue.
	n0 := sent("joinedAt")
	if _, err := e.writes.flush(ctx, true); err != nil || sent("joinedAt") != n0 {
		t.Fatalf("retried too early: %d", sent("joinedAt"))
	}

	// The bot's deployment is already new: the change goes there.
	nf := &fakeClubScript{mode: map[string]string{}}
	fb := httptest.NewServer(http.HandlerFunc(nf.handler))
	defer fb.Close()
	e.g.Fallback = func() string { return fb.URL + "/exec" }
	*e.now = e.now.Add(11 * time.Minute)
	if n, err := e.writes.Flush(ctx); err != nil || n != 2 {
		t.Fatalf("through the bot's deployment: %d %v", n, err)
	}
	if len(nf.calls) != 2 || nf.calls[0].Get("field") != "joinedAt" || nf.calls[1].Get("field") != "exception" {
		t.Fatalf("fallback got %v", nf.calls)
	}
	if e.count(t, `SELECT count(*) FROM club_writes WHERE action = 'setResidentField' AND status = 'sent'`) != 2 {
		t.Fatal("not sent")
	}

	// Without a newer deployment: parked until the script has updated itself.
	e.g.Fallback = nil
	e.f.set("setResidentField", "unknown")
	if w, _ := edit(`{"name":"Альтаир","field":"format","value":"Онлайн"}`); w.Code != 200 {
		t.Fatalf("format: %d", w.Code)
	}
	if _, err := e.writes.flush(ctx, true); err != nil {
		t.Fatal(err)
	}
	e.f.set("setResidentField", "ok") // v38 installed
	*e.now = e.now.Add(11 * time.Minute)
	if n, err := e.writes.Flush(ctx); err != nil || n != 1 {
		t.Fatalf("after the update: %d %v", n, err)
	}
	if q := e.f.calls[len(e.f.calls)-1]; q.Get("field") != "format" || q.Get("value") != "Онлайн" {
		t.Fatalf("sent %v", q)
	}
	if e.count(t, `SELECT count(*) FROM club_residents WHERE name = 'Альтаир' AND format = 'Онлайн'`) != 1 {
		t.Fatal("format lost")
	}
}
