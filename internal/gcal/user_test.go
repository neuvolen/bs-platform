package gcal

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/bnursik/business_surgery_backend/internal/club"
)

// ── an in-memory store and work calendar ──

type memUsers struct {
	mu     sync.Mutex
	conns  map[string]UserConn
	events map[string]map[string]UserEvent // scope → cal|id
	logs   []string
	docs   map[string]string
	ver    map[string]int
	at     map[string]time.Time
	now    func() time.Time
}

func newMemUsers(now func() time.Time) *memUsers {
	return &memUsers{conns: map[string]UserConn{}, events: map[string]map[string]UserEvent{}, docs: map[string]string{},
		ver: map[string]int{}, at: map[string]time.Time{}, now: now}
}

func (m *memUsers) GcalConns(context.Context) ([]UserConn, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []UserConn
	for _, c := range m.conns {
		out = append(out, c)
	}
	return out, nil
}
func (m *memUsers) GcalConn(_ context.Context, s string) (*UserConn, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	c, ok := m.conns[s]
	if !ok {
		return nil, nil
	}
	return &c, nil
}
func (m *memUsers) GcalSaveConn(_ context.Context, c UserConn) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.conns[c.Scope] = c
	return nil
}
func (m *memUsers) GcalDeleteConn(_ context.Context, s string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.conns, s)
	delete(m.events, s)
	return nil
}
func (m *memUsers) GcalEvents(_ context.Context, s string) ([]UserEvent, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []UserEvent
	for _, e := range m.events[s] {
		out = append(out, e)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Start.Before(out[j].Start) })
	return out, nil
}
func (m *memUsers) GcalPutEvent(_ context.Context, s string, e UserEvent) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.events[s] == nil {
		m.events[s] = map[string]UserEvent{}
	}
	m.events[s][e.Cal+"|"+e.ID] = e
	return nil
}
func (m *memUsers) GcalDelEvent(_ context.Context, s, cal, id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.events[s], cal+"|"+id)
	return nil
}
func (m *memUsers) GcalDelCal(_ context.Context, s, cal string, ours bool) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for k, e := range m.events[s] {
		if e.Cal == cal && e.Ours == ours {
			delete(m.events[s], k)
		}
	}
	return nil
}
func (m *memUsers) GcalLog(_ context.Context, s, kind, text string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.logs = append(m.logs, kind+": "+text)
	return nil
}
func (m *memUsers) LoadCal(_ context.Context, s string) (string, int, time.Time, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.docs[s] == "" {
		return `{"slots":{}}`, m.ver[s], time.Time{}, nil
	}
	return m.docs[s], m.ver[s], m.at[s], nil
}
func (m *memUsers) SaveCal(_ context.Context, s string, v int, val string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if v != m.ver[s] {
		return ErrCalConflict
	}
	m.docs[s], m.ver[s], m.at[s] = val, v+1, m.now()
	return nil
}

// edit: the person edits the work calendar on the platform.
func (m *memUsers) edit(s string, f func(d *calDoc)) {
	v, ver, _, _ := m.LoadCal(context.Background(), s)
	d := parseCal(v)
	f(d)
	if err := m.SaveCal(context.Background(), s, ver, d.json()); err != nil {
		panic(err)
	}
}

func (m *memUsers) slots(s string) map[string]string {
	v, _, _, _ := m.LoadCal(context.Background(), s)
	return parseCal(v).slots
}

// ── a fake Google: OAuth, calendars, events with sync tokens ──

type fEv struct {
	ev  map[string]any
	seq int
}

type fakeG struct {
	mu        sync.Mutex
	t         *testing.T
	seq       int
	cals      map[string]map[string]*fEv // cal → id → event
	names     map[string]string
	nextID    int
	revoked   []string
	dead      bool // refresh answers invalid_grant
	gone      bool // the next syncToken read answers 410
	writes    []string
	syncReads int
	fullReads int
	clock     func() time.Time
	refresh   string
}

func newFakeG(t *testing.T, clock func() time.Time) *fakeG {
	return &fakeG{t: t, cals: map[string]map[string]*fEv{"primary": {}}, names: map[string]string{"primary": "me@example.com"}, clock: clock, refresh: "user-rt-1"}
}

func (f *fakeG) put(cal string, ev map[string]any) {
	f.seq++
	if f.cals[cal] == nil {
		f.cals[cal] = map[string]*fEv{}
	}
	ev["updated"] = f.clock().UTC().Add(time.Duration(f.seq) * time.Millisecond).Format(time.RFC3339Nano)
	f.cals[cal][ev["id"].(string)] = &fEv{ev: ev, seq: f.seq}
}

func (f *fakeG) handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		body, _ := io.ReadAll(r.Body)
		p := r.URL.Path
		js := func(v any) { _ = json.NewEncoder(w).Encode(v) }
		switch {
		case p == "/token":
			v, _ := url.ParseQuery(string(body))
			if v.Get("grant_type") == "authorization_code" {
				if v.Get("code") != "good-code" {
					w.WriteHeader(400)
					js(map[string]string{"error": "invalid_grant"})
					return
				}
				js(map[string]any{"access_token": "uat1", "refresh_token": f.refresh, "expires_in": 3600, "scope": UserScopes})
				return
			}
			if f.dead {
				w.WriteHeader(400)
				js(map[string]string{"error": "invalid_grant", "error_description": "Token has been expired or revoked."})
				return
			}
			js(map[string]any{"access_token": "uat2", "expires_in": 3600})
		case p == "/revoke":
			v, _ := url.ParseQuery(string(body))
			f.revoked = append(f.revoked, v.Get("token"))
		case p == "/api/users/me/calendarList/primary":
			js(map[string]string{"id": "me@example.com"})
		case p == "/api/users/me/calendarList":
			var items []map[string]any
			for id, n := range f.names {
				items = append(items, map[string]any{"id": id, "summary": n, "accessRole": "owner", "primary": id == "primary"})
			}
			js(map[string]any{"items": items})
		case p == "/api/calendars" && r.Method == http.MethodPost:
			var in map[string]any
			_ = json.Unmarshal(body, &in)
			id := fmt.Sprintf("bs%d@group.calendar.google.com", len(f.names))
			f.names[id] = in["summary"].(string)
			f.cals[id] = map[string]*fEv{}
			js(map[string]string{"id": id})
		case strings.HasPrefix(p, "/api/calendars/") && strings.Contains(p, "/events"):
			rest := strings.TrimPrefix(p, "/api/calendars/")
			cal, tail, _ := strings.Cut(rest, "/events")
			cal, _ = url.PathUnescape(cal)
			id := strings.TrimPrefix(tail, "/")
			evs := f.cals[cal]
			if evs == nil {
				w.WriteHeader(404)
				js(map[string]any{"error": map[string]any{"status": "NOT_FOUND"}})
				return
			}
			if r.Method != http.MethodGet && r.URL.Query().Get("sendUpdates") != "none" {
				f.t.Errorf("%s %s without sendUpdates=none", r.Method, p)
			}
			switch r.Method {
			case http.MethodGet:
				q := r.URL.Query()
				var items []map[string]any
				if st := q.Get("syncToken"); st != "" {
					if f.gone {
						f.gone = false
						w.WriteHeader(410)
						js(map[string]any{"error": map[string]any{"status": "GONE", "message": "Sync token is no longer valid"}})
						return
					}
					f.syncReads++
					var since int
					fmt.Sscanf(st, "s%d", &since)
					for _, e := range evs {
						if e.seq > since {
							items = append(items, e.ev)
						}
					}
				} else {
					f.fullReads++
					if q.Get("timeMin") == "" {
						f.t.Errorf("a full read without timeMin")
					}
					for _, e := range evs {
						if e.ev["status"] != "cancelled" {
							items = append(items, e.ev)
						}
					}
				}
				js(map[string]any{"items": items, "nextSyncToken": fmt.Sprintf("s%d", f.seq)})
			case http.MethodPost:
				var in map[string]any
				_ = json.Unmarshal(body, &in)
				rem, _ := in["reminders"].(map[string]any)
				if rem == nil || rem["useDefault"] != false {
					f.t.Errorf("insert with reminders: %s", body)
				}
				f.nextID++
				in["id"] = fmt.Sprintf("ev%d", f.nextID)
				in["status"] = "confirmed"
				f.put(cal, in)
				f.writes = append(f.writes, "insert "+in["summary"].(string))
				js(in)
			case http.MethodPatch:
				e := evs[id]
				if e == nil {
					w.WriteHeader(404)
					js(map[string]any{"error": map[string]any{"status": "NOT_FOUND"}})
					return
				}
				var in map[string]any
				_ = json.Unmarshal(body, &in)
				for k, v := range in {
					e.ev[k] = v
				}
				f.put(cal, e.ev)
				f.writes = append(f.writes, "patch "+in["summary"].(string))
				js(e.ev)
			case http.MethodDelete:
				e := evs[id]
				if e == nil {
					w.WriteHeader(410)
					return
				}
				e.ev["status"] = "cancelled"
				f.put(cal, e.ev)
				f.writes = append(f.writes, "delete "+id)
				w.WriteHeader(204)
			}
		default:
			f.t.Errorf("unexpected %s %s", r.Method, p)
			w.WriteHeader(404)
		}
	})
}

// googleEdit: the person edits our event in Google.
func (f *fakeG) googleEdit(cal, id string, ch func(ev map[string]any)) {
	f.mu.Lock()
	defer f.mu.Unlock()
	e := f.cals[cal][id]
	if e == nil {
		f.t.Fatalf("no event %s", id)
	}
	ch(e.ev)
	f.put(cal, e.ev)
}

func (f *fakeG) find(cal, summary string) (string, map[string]any) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for id, e := range f.cals[cal] {
		if e.ev["summary"] == summary && e.ev["status"] != "cancelled" {
			return id, e.ev
		}
	}
	return "", nil
}

func (f *fakeG) live(cal string) []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []string
	for _, e := range f.cals[cal] {
		if e.ev["status"] != "cancelled" {
			out = append(out, e.ev["summary"].(string))
		}
	}
	sort.Strings(out)
	return out
}

type userEnv struct {
	u     *UserSync
	store *memUsers
	g     *fakeG
	now   *time.Time
	told  []string
}

func newUserEnv(t *testing.T) *userEnv {
	now := time.Date(2026, 10, 12, 9, 0, 0, 0, club.Almaty)
	env := &userEnv{now: &now}
	clock := func() time.Time { return *env.now }
	env.g = newFakeG(t, clock)
	srv := httptest.NewServer(env.g.handler())
	t.Cleanup(srv.Close)
	owner := New(&memStore{m: map[string]string{}})
	owner.TokenURL, owner.AuthURL, owner.API, owner.Now = srv.URL+"/token", srv.URL+"/auth", srv.URL+"/api", clock
	t.Setenv("GOOGLE_CAL_CLIENT_ID", "")
	t.Setenv("GOOGLE_CAL_CLIENT_SECRET", "")
	env.store = newMemUsers(clock)
	env.u = &UserSync{Owner: owner, Store: env.store, Docs: env.store, Secret: []byte("jwt-secret-for-tests"),
		Notify: func(_ context.Context, s string) { env.told = append(env.told, s) }}
	return env
}

func (e *userEnv) connect(t *testing.T, scope string) {
	ctx := context.Background()
	if err := e.u.Owner.SaveCreds(ctx, Creds{ID: "1234-abc.apps.googleusercontent.com", Secret: "GOCSPX-secret-123"}); err != nil {
		t.Fatal(err)
	}
	u, err := e.u.ConsentURL(ctx, scope, "a", "https://app.bxclub.kz/api/v1/gcal/callback")
	if err != nil {
		t.Fatal(err)
	}
	pu, _ := url.Parse(u)
	st, origin, err := e.u.ReadState(pu.Query().Get("state"))
	if err != nil || st != scope || origin != "a" {
		t.Fatalf("state: %q %q %v", st, origin, err)
	}
	if err := e.u.Connect(ctx, scope, "good-code", "https://app.bxclub.kz/api/v1/gcal/callback"); err != nil {
		t.Fatal(err)
	}
}

func (e *userEnv) sync(t *testing.T, scope string) Result {
	t.Helper()
	res, err := e.u.SyncOne(context.Background(), scope)
	if err != nil {
		t.Fatalf("sync: %v", err)
	}
	return res
}

const scopeA = "user:tg:490685605"

func TestUserConnectWithoutOwnerClient(t *testing.T) {
	env := newUserEnv(t)
	if env.u.Available(context.Background()) {
		t.Fatal("available without the owner's client")
	}
	_, err := env.u.ConsentURL(context.Background(), scopeA, "p", "https://x/cb")
	if err != ErrNoClient || !strings.Contains(err.Error(), "после настройки Google в /calendar") {
		t.Fatalf("err = %v", err)
	}
	st, _ := env.u.View(context.Background(), scopeA, true)
	if st.Available || st.Hint != ErrNoClient.Error() {
		t.Fatalf("status %+v", st)
	}
}

func TestUserConsentAndConnect(t *testing.T) {
	env := newUserEnv(t)
	ctx := context.Background()
	_ = env.u.Owner.SaveCreds(ctx, Creds{ID: "1234-abc.apps.googleusercontent.com", Secret: "GOCSPX-secret-123"})
	u, _ := env.u.ConsentURL(ctx, scopeA, "p", "https://app.bxclub.kz/api/v1/gcal/callback")
	pu, _ := url.Parse(u)
	q := pu.Query()
	for _, s := range []string{"calendar.events", "calendar.readonly", "calendar.app.created"} {
		if !strings.Contains(q.Get("scope"), s) {
			t.Errorf("scope %q lacks %s", q.Get("scope"), s)
		}
	}
	if q.Get("access_type") != "offline" || q.Get("redirect_uri") != "https://app.bxclub.kz/api/v1/gcal/callback" || !IsUserState(q.Get("state")) {
		t.Fatalf("consent url %s", u)
	}
	// a forged or expired state is refused
	if _, _, err := env.u.ReadState(q.Get("state") + "x"); err == nil {
		t.Fatal("forged state accepted")
	}
	*env.now = env.now.Add(31 * time.Minute)
	if _, _, err := env.u.ReadState(q.Get("state")); err == nil {
		t.Fatal("old state accepted")
	}
	env.connect(t, scopeA)
	c, _ := env.store.GcalConn(ctx, scopeA)
	if c == nil || c.Email != "me@example.com" || !c.OwnCal || c.WriteName != UserCalName || c.ReadCal != "primary" {
		t.Fatalf("conn %+v", c)
	}
	if strings.Contains(c.RefreshEnc, "user-rt-1") || !strings.HasPrefix(c.RefreshEnc, "v1:") {
		t.Fatalf("refresh token kept in clear: %q", c.RefreshEnc)
	}
	if rt, err := env.u.open(c.RefreshEnc); err != nil || rt != "user-rt-1" {
		t.Fatalf("open: %q %v", rt, err)
	}
	// a second connection finds the calendar instead of making another
	env.connect(t, scopeA)
	n := 0
	for _, name := range env.g.names {
		if name == UserCalName {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("%d calendars «%s»", n, UserCalName)
	}
}

func TestUserTwoWaySync(t *testing.T) {
	env := newUserEnv(t)
	ctx := context.Background()
	env.connect(t, scopeA)
	c, _ := env.store.GcalConn(ctx, scopeA)
	bs := c.WriteCal
	// the person's own Google event, shown read only
	env.g.mu.Lock()
	env.g.put("primary", map[string]any{"id": "mine1", "status": "confirmed", "summary": "Стоматолог",
		"start": map[string]string{"dateTime": "2026-10-13T12:30:00+05:00"}, "end": map[string]string{"dateTime": "2026-10-13T13:15:00+05:00"}})
	env.g.mu.Unlock()
	env.store.edit(scopeA, func(d *calDoc) {
		d.slots["2026-10-13|10"], d.slots["2026-10-13|11"] = "work", "work"
		d.notes["2026-10-13|10"] = "созвон с поставщиком"
		d.slots["2026-10-13|14"] = "focus"
		d.slots["2026-10-13|15"] = "focus"
		d.slots["2026-10-14|9"] = "busy"
	})
	res := env.sync(t, scopeA)
	if res.Inserted != 3 {
		t.Fatalf("inserted %d, writes %v", res.Inserted, env.g.writes)
	}
	got := strings.Join(env.g.live(bs), ";")
	if got != "Занято;Работа · созвон с поставщиком;Фокус-блок" {
		t.Fatalf("google has %q", got)
	}
	_, ev := env.g.find(bs, "Фокус-блок")
	if ev["start"].(map[string]any)["dateTime"] != "2026-10-13T14:00:00+05:00" || ev["end"].(map[string]any)["dateTime"] != "2026-10-13T16:00:00+05:00" {
		t.Fatalf("focus times %v %v", ev["start"], ev["end"])
	}
	// shown on the platform: titles to the person, «Занято (Google)» to the team
	st, evs := env.u.View(ctx, scopeA, true)
	if !st.Connected || len(evs) != 1 || evs[0].Title != "Стоматолог" || evs[0].Date != "2026-10-13" || evs[0].H0 != 12 || evs[0].H1 != 14 || evs[0].From != "12:30" {
		t.Fatalf("view %+v %+v", st, evs)
	}
	if _, tv := env.u.View(ctx, scopeA, false); tv[0].Title != "Занято (Google)" {
		t.Fatalf("team sees %q", tv[0].Title)
	}
	// nothing changed: no writes, the incremental read is used
	w0 := len(env.g.writes)
	reads := env.g.syncReads
	res = env.sync(t, scopeA)
	if len(env.g.writes) != w0 || res.DocChanged || env.g.syncReads <= reads {
		t.Fatalf("idle pass wrote %v, res %+v, syncReads %d", env.g.writes[w0:], res, env.g.syncReads)
	}

	// Google: the focus block moved to 16–18 and renamed with a note
	*env.now = env.now.Add(10 * time.Minute)
	fid, _ := env.g.find(bs, "Фокус-блок")
	env.g.googleEdit(bs, fid, func(ev map[string]any) {
		ev["summary"] = "Фокус-блок · стратегия"
		ev["start"] = map[string]any{"dateTime": "2026-10-13T16:00:00+05:00"}
		ev["end"] = map[string]any{"dateTime": "2026-10-13T18:00:00+05:00"}
	})
	res = env.sync(t, scopeA)
	s := env.store.slots(scopeA)
	if !res.DocChanged || s["2026-10-13|14"] != "" || s["2026-10-13|16"] != "focus" || s["2026-10-13|17"] != "focus" {
		t.Fatalf("after the Google edit: %+v slots %v", res, s)
	}
	v, _, _, _ := env.store.LoadCal(ctx, scopeA)
	if parseCal(v).notes["2026-10-13|16"] != "стратегия" {
		t.Fatalf("note %v", parseCal(v).notes)
	}
	if n := len(env.g.writes); n != w0 {
		t.Fatalf("the Google edit was echoed back: %v", env.g.writes[w0:])
	}

	// Google: the busy block deleted → gone on the platform
	bid, _ := env.g.find(bs, "Занято")
	env.g.mu.Lock()
	env.g.cals[bs][bid].ev["status"] = "cancelled"
	env.g.put(bs, env.g.cals[bs][bid].ev)
	env.g.mu.Unlock()
	env.sync(t, scopeA)
	if env.store.slots(scopeA)["2026-10-14|9"] != "" {
		t.Fatal("deleted in Google, still on the platform")
	}

	// platform: the work block extended to 12, the focus block removed
	*env.now = env.now.Add(10 * time.Minute)
	env.store.edit(scopeA, func(d *calDoc) {
		d.slots["2026-10-13|12"] = "work"
		delete(d.slots, "2026-10-13|16")
		delete(d.slots, "2026-10-13|17")
		delete(d.notes, "2026-10-13|16")
	})
	res = env.sync(t, scopeA)
	if res.Patched != 1 || res.Deleted != 1 {
		t.Fatalf("platform edit: %+v writes %v", res, env.g.writes)
	}
	_, wev := env.g.find(bs, "Работа · созвон с поставщиком")
	if wev == nil || wev["end"].(map[string]any)["dateTime"] != "2026-10-13T13:00:00+05:00" {
		t.Fatalf("work block %v", wev)
	}
	if got := strings.Join(env.g.live(bs), ";"); got != "Работа · созвон с поставщиком" {
		t.Fatalf("google has %q", got)
	}

	// the sync token expired: the window is read again, nothing duplicated
	env.g.mu.Lock()
	env.g.gone = true
	full := env.g.fullReads
	env.g.mu.Unlock()
	env.sync(t, scopeA)
	if env.g.fullReads <= full || len(env.g.live(bs)) != 1 {
		t.Fatalf("after 410: full reads %d → %d, live %v", full, env.g.fullReads, env.g.live(bs))
	}
	if _, evs := env.u.View(ctx, scopeA, true); len(evs) != 1 {
		t.Fatalf("after 410 shown %v", evs)
	}
}

func TestUserConflictLastEditWins(t *testing.T) {
	env := newUserEnv(t)
	ctx := context.Background()
	env.connect(t, scopeA)
	c, _ := env.store.GcalConn(ctx, scopeA)
	bs := c.WriteCal
	env.store.edit(scopeA, func(d *calDoc) { d.slots["2026-10-15|10"] = "work" })
	env.sync(t, scopeA)
	id, _ := env.g.find(bs, "Работа")

	// Google first, the platform later: the platform wins
	*env.now = env.now.Add(time.Minute)
	env.g.googleEdit(bs, id, func(ev map[string]any) { ev["summary"] = "Работа · из Google" })
	*env.now = env.now.Add(time.Minute)
	env.store.edit(scopeA, func(d *calDoc) { d.slots["2026-10-15|10"] = "busy" })
	res := env.sync(t, scopeA)
	if res.Conflicts != 1 || env.store.slots(scopeA)["2026-10-15|10"] != "busy" {
		t.Fatalf("platform later: %+v %v", res, env.store.slots(scopeA))
	}
	if got := strings.Join(env.g.live(bs), ";"); got != "Занято" {
		t.Fatalf("google has %q", got)
	}

	// the platform first, Google later: Google wins
	*env.now = env.now.Add(time.Minute)
	env.store.edit(scopeA, func(d *calDoc) { d.slots["2026-10-15|10"] = "rest" })
	*env.now = env.now.Add(time.Minute)
	id, _ = env.g.find(bs, "Занято")
	env.g.googleEdit(bs, id, func(ev map[string]any) { ev["summary"] = "Занято · переговоры" })
	res = env.sync(t, scopeA)
	v, _, _, _ := env.store.LoadCal(ctx, scopeA)
	d := parseCal(v)
	if res.Conflicts != 1 || d.slots["2026-10-15|10"] != "busy" || d.notes["2026-10-15|10"] != "переговоры" {
		t.Fatalf("google later: %+v %v %v", res, d.slots, d.notes)
	}
	n := 0
	for _, l := range env.store.logs {
		if strings.HasPrefix(l, "conflict: ") {
			n++
		}
	}
	if n != 2 {
		t.Fatalf("conflicts logged: %v", env.store.logs)
	}
}

func TestUserLostAccessTestingHint(t *testing.T) {
	env := newUserEnv(t)
	ctx := context.Background()
	env.connect(t, scopeA)
	env.u.mu.Lock()
	env.u.tokens = nil // the access token expired
	env.u.mu.Unlock()
	*env.now = env.now.Add(7*24*time.Hour + time.Hour)
	env.g.mu.Lock()
	env.g.dead = true
	env.g.mu.Unlock()
	_, err := env.u.SyncOne(ctx, scopeA)
	if err == nil {
		t.Fatal("no error")
	}
	c, _ := env.store.GcalConn(ctx, scopeA)
	if !c.NeedConsent || !strings.Contains(c.Error, "подключите Google Календарь заново") {
		t.Fatalf("conn %+v", c)
	}
	if len(env.told) != 1 || !strings.Contains(env.told[0], "Publish app") || !strings.Contains(env.told[0], "Testing") {
		t.Fatalf("owner told %v", env.told)
	}
	st, _ := env.u.View(ctx, scopeA, true)
	if !st.Need {
		t.Fatalf("status %+v", st)
	}
	// the background pass skips it, no more requests
	env.u.SyncAll(ctx)
	if len(env.told) != 1 {
		t.Fatal("told twice")
	}
}

func TestUserDisconnectRevokes(t *testing.T) {
	env := newUserEnv(t)
	ctx := context.Background()
	env.connect(t, scopeA)
	env.store.edit(scopeA, func(d *calDoc) { d.slots["2026-10-15|10"] = "work" })
	env.sync(t, scopeA)
	if err := env.u.Disconnect(ctx, scopeA); err != nil {
		t.Fatal(err)
	}
	if len(env.g.revoked) != 1 || env.g.revoked[0] != "user-rt-1" {
		t.Fatalf("revoked %v", env.g.revoked)
	}
	if c, _ := env.store.GcalConn(ctx, scopeA); c != nil {
		t.Fatal("connection kept")
	}
	if evs, _ := env.store.GcalEvents(ctx, scopeA); len(evs) != 0 {
		t.Fatal("events kept")
	}
}

func TestUserSettingsMoveToPrimary(t *testing.T) {
	env := newUserEnv(t)
	ctx := context.Background()
	env.connect(t, scopeA)
	c, _ := env.store.GcalConn(ctx, scopeA)
	old := c.WriteCal
	env.store.edit(scopeA, func(d *calDoc) { d.slots["2026-10-15|10"] = "focus" })
	env.sync(t, scopeA)
	if _, err := env.u.Settings(ctx, scopeA, "", "primary"); err != nil {
		t.Fatal(err)
	}
	env.sync(t, scopeA)
	if len(env.g.live(old)) != 0 || strings.Join(env.g.live("primary"), ";") != "Фокус-блок" {
		t.Fatalf("old %v primary %v", env.g.live(old), env.g.live("primary"))
	}
}

func TestBlocksGrouping(t *testing.T) {
	d := parseCal(`{"slots":{"2026-10-13|9":"work","2026-10-13|10":"work","2026-10-13|11":"work","2026-10-13|12":"focus","2026-10-13|20":"work"},"notes":{"2026-10-13|11":"обед"},"other":1}`)
	var got []string
	for _, b := range d.Blocks() {
		got = append(got, b.Sig())
	}
	want := "2026-10-13|11|12|work|обед;2026-10-13|12|13|focus|;2026-10-13|20|21|work|;2026-10-13|9|11|work|"
	if strings.Join(got, ";") != want {
		t.Fatalf("blocks %v", got)
	}
	if !strings.Contains(d.json(), `"other":1`) {
		t.Fatal("other fields lost")
	}
}
