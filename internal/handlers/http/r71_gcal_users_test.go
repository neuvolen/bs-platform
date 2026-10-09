package http

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/bnursik/business_surgery_backend/internal/gcal"
	pg "github.com/bnursik/business_surgery_backend/internal/repository/pg"
	"github.com/bnursik/business_surgery_backend/migrations"
)

// R71: «Подключить Google Календарь» in the Mini App's work calendar: the
// button without the owner's OAuth client, the consent through the owner's
// redirect URI, the token kept encrypted, the calendar pushed quietly, the
// person's Google events in the calendar's GET, disconnect revokes.
func TestR71GoogleCalendarPerPerson(t *testing.T) {
	dsn := os.Getenv("BS_TEST_DSN")
	if dsn == "" {
		t.Skip("BS_TEST_DSN not set")
	}
	ctx := context.Background()
	db, err := pg.NewDB(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Pool.Close()
	if err := pg.Migrate(ctx, db, migrations.FS); err != nil {
		t.Fatal(err)
	}
	const scope = "user:tg:69111"
	for _, q := range []string{`DELETE FROM gcal_users WHERE scope = $1`, `DELETE FROM gcal_user_log WHERE scope = $1`,
		`DELETE FROM platform_docs WHERE scope = $1`, `DELETE FROM platform_doc_versions WHERE scope = $1`} {
		if _, err := db.Pool.Exec(ctx, q, scope); err != nil {
			t.Fatal(err)
		}
	}

	// a fake Google
	var mu sync.Mutex
	var inserts, revoked []string
	google := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		body, _ := io.ReadAll(r.Body)
		p := r.URL.Path
		switch {
		case p == "/token":
			v, _ := url.ParseQuery(string(body))
			if v.Get("grant_type") == "authorization_code" {
				fmt.Fprintf(w, `{"access_token":"a1","refresh_token":"secret-refresh-69","expires_in":3600,"scope":%q}`, gcal.UserScopes)
				return
			}
			fmt.Fprint(w, `{"access_token":"a2","expires_in":3600}`)
		case p == "/revoke":
			v, _ := url.ParseQuery(string(body))
			revoked = append(revoked, v.Get("token"))
		case p == "/api/users/me/calendarList/primary":
			fmt.Fprint(w, `{"id":"altair@example.com"}`)
		case p == "/api/users/me/calendarList":
			fmt.Fprint(w, `{"items":[{"id":"primary","summary":"altair@example.com","accessRole":"owner","primary":true}]}`)
		case p == "/api/calendars" && r.Method == "POST":
			fmt.Fprint(w, `{"id":"bswork@group.calendar.google.com"}`)
		case p == "/api/calendars/primary/events" && r.Method == "GET":
			fmt.Fprint(w, `{"items":[{"id":"x1","status":"confirmed","summary":"Встреча с банком","updated":"2026-10-09T05:00:00Z",
				"start":{"dateTime":"`+time.Now().Add(24*time.Hour).Format("2006-01-02")+`T15:00:00+05:00"},"end":{"dateTime":"`+time.Now().Add(24*time.Hour).Format("2006-01-02")+`T16:00:00+05:00"}}],"nextSyncToken":"p1"}`)
		case strings.HasPrefix(p, "/api/calendars/bswork@group.calendar.google.com/events"):
			if r.Method == "GET" {
				fmt.Fprint(w, `{"items":[],"nextSyncToken":"w1"}`)
				return
			}
			if r.URL.Query().Get("sendUpdates") != "none" {
				t.Errorf("write without sendUpdates=none")
			}
			var in map[string]any
			_ = json.Unmarshal(body, &in)
			inserts = append(inserts, fmt.Sprint(in["summary"]))
			fmt.Fprintf(w, `{"id":"e%d","updated":"2026-10-09T06:00:0%dZ"}`, len(inserts), len(inserts))
		default:
			t.Errorf("unexpected %s %s", r.Method, p)
			w.WriteHeader(404)
		}
	}))
	defer google.Close()

	g, _, r, now := newGateway(t)
	g.Boards = &fakeBoards{byTg: map[int64]string{69111: "Альтаир"}}
	g.Sync = pg.NewPlatformRepo(db)
	owner := gcal.New(pg.NewBotRepo(db))
	owner.TokenURL, owner.AuthURL, owner.API = google.URL+"/token", google.URL+"/auth", google.URL+"/api"
	_, _ = db.Pool.Exec(ctx, `DELETE FROM bot_meta WHERE key = $1`, gcal.MetaClient)
	t.Setenv("GOOGLE_CAL_CLIENT_ID", "")
	t.Setenv("GOOGLE_CAL_CLIENT_SECRET", "")
	repo := pg.NewGcalUserRepo(db, pg.NewPlatformRepo(db))
	u := &gcal.UserSync{Owner: owner, Store: repo, Docs: repo, Secret: []byte("r69-test-secret")}
	SetGcalUsers(u)
	t.Cleanup(func() { SetGcalUsers(nil) })
	NewGcalUsersModule(g, []byte("jwt")).Register(r)
	NewGcalSetup(&gcal.Sync{C: owner}).Register(r)

	req := func(method, path, body string, id int64) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		sep := "?"
		if strings.Contains(path, "?") {
			sep = "&"
		}
		r.ServeHTTP(w, httptest.NewRequest(method, path+sep+"_tg="+url.QueryEscape(makeInitData(testBotToken, id, "X", *now)), strings.NewReader(body)))
		return w
	}
	// 1. the owner has not set up Google: the button says so
	w := req("GET", "/api/v1/app/mycal", "", 69111)
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"available":false`) || !strings.Contains(w.Body.String(), "после настройки Google в /calendar") {
		t.Fatalf("mycal without the client: %d %s", w.Code, w.Body.String())
	}
	if w := req("POST", "/api/v1/app/gcal/connect", "", 69111); w.Code != 409 {
		t.Fatalf("connect without the client: %d %s", w.Code, w.Body.String())
	}
	// 2. the owner did /calendar
	if err := owner.SaveCreds(ctx, gcal.Creds{ID: "69-abc.apps.googleusercontent.com", Secret: "GOCSPX-r69-secret"}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = db.Pool.Exec(context.Background(), `DELETE FROM bot_meta WHERE key = $1`, gcal.MetaClient)
	})
	w = req("POST", "/api/v1/app/gcal/connect", "", 69111)
	var cj struct {
		URL string `json:"url"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &cj)
	cu, _ := url.Parse(cj.URL)
	if w.Code != 200 || cu == nil || cu.Query().Get("redirect_uri") != GcalRedirect() || !gcal.IsUserState(cu.Query().Get("state")) {
		t.Fatalf("connect: %d %s", w.Code, w.Body.String())
	}
	// the admin's preview may not connect a resident's Google
	if w := req("POST", "/api/v1/app/gcal/connect?name="+url.QueryEscape("Альтаир"), "", 453800951); w.Code != 403 {
		t.Fatalf("preview connect: %d", w.Code)
	}
	// 3. Google returns to the owner's redirect URI
	cw := httptest.NewRecorder()
	r.ServeHTTP(cw, httptest.NewRequest("GET", "/api/v1/gcal/callback?code=c1&state="+url.QueryEscape(cu.Query().Get("state")), nil))
	if cw.Code != 200 || !strings.Contains(cw.Body.String(), "Google Календарь подключён") {
		t.Fatalf("callback: %d %s", cw.Code, cw.Body.String())
	}
	var enc, email, write string
	if err := db.Pool.QueryRow(ctx, `SELECT refresh_enc, email, write_cal FROM gcal_users WHERE scope = $1`, scope).Scan(&enc, &email, &write); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(enc, "secret-refresh-69") || email != "altair@example.com" || write != "bswork@group.calendar.google.com" {
		t.Fatalf("kept: %q %q %q", enc, email, write)
	}
	// 4. the resident marks hours; the sync pushes them
	day := time.Now().Add(48 * time.Hour).Format("2006-01-02")
	if w := req("PUT", "/api/v1/app/mycal", `{"value":"{\"slots\":{\"`+day+`|10\":\"focus\",\"`+day+`|11\":\"focus\"}}","version":0}`, 69111); w.Code != 200 {
		t.Fatalf("put: %d %s", w.Code, w.Body.String())
	}
	time.Sleep(300 * time.Millisecond) // the first sync of the callback
	if _, err := u.SyncOne(ctx, scope); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	got := strings.Join(inserts, ";")
	mu.Unlock()
	if got != "Фокус-блок" {
		t.Fatalf("pushed %q", got)
	}
	// 5. the calendar's GET: connected, the bank meeting with its title
	w = req("GET", "/api/v1/app/mycal", "", 69111)
	if !strings.Contains(w.Body.String(), `"connected":true`) || !strings.Contains(w.Body.String(), "Встреча с банком") {
		t.Fatalf("mycal: %s", w.Body.String())
	}
	// 6. disconnect revokes and forgets
	if w := req("POST", "/api/v1/app/gcal/disconnect", "", 69111); w.Code != 200 {
		t.Fatalf("disconnect: %d %s", w.Code, w.Body.String())
	}
	mu.Lock()
	rv := strings.Join(revoked, ";")
	mu.Unlock()
	var n int
	_ = db.Pool.QueryRow(ctx, `SELECT count(*) FROM gcal_users WHERE scope = $1`, scope).Scan(&n)
	if rv != "secret-refresh-69" || n != 0 {
		t.Fatalf("revoked %q, rows %d", rv, n)
	}
	_ = db.Pool.QueryRow(ctx, `SELECT count(*) FROM gcal_user_events WHERE scope = $1`, scope).Scan(&n)
	if n != 0 {
		t.Fatalf("events kept: %d", n)
	}
}
