package http

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	pg "github.com/bnursik/business_surgery_backend/internal/repository/pg"
	"github.com/gin-gonic/gin"
)

const leadTestSecret = "lead-test-secret-0123456789"

type leadEnv struct {
	r     *gin.Engine
	repo  *pg.PlatformRepo
	auth  *PlatformAuthHandler
	pm    *PlatformModule
	clean func()
}

// newLeadEnv: the platform's and the club's modules on the test database
// (no background loops), the way the server registers them.
func newLeadEnv(t *testing.T) *leadEnv {
	e := newBotEnv(t)
	ctx := context.Background()
	for _, q := range []string{
		`DELETE FROM platform_docs WHERE scope='club' AND key IN ('bs_crm','bs_slots','bs_events_feed')`,
		`DELETE FROM platform_residents WHERE tg_id IN (777001, 777002)`,
		`INSERT INTO platform_residents (tg_id, name, active) VALUES (777002, 'Резидент Платформы', true)`,
	} {
		if _, err := e.db.Pool.Exec(ctx, q); err != nil {
			t.Fatal(err)
		}
	}
	repo := pg.NewPlatformRepo(e.db)
	clubRepo := pg.NewClubRepo(e.db)
	h := NewPlatformHandler(repo)
	a := NewPlatformAuthHandler(testBotToken, "453800951:Рустам", leadTestSecret)
	a.repo, a.names = repo, h.names
	a.leads = &LeadFunnel{docs: repo, now: time.Now}
	pm := &PlatformModule{h: h, auth: a, secret: []byte(leadTestSecret), AI: NewPlatformAI(repo, nil), lead: &LeadHome{f: a.leads}}
	gw := NewAppGateway(testBotToken, "http://127.0.0.1:1/exec")
	writes := NewClubWrites(clubRepo, gw)
	gw.Writes = writes
	action := NewClubActionHandler(gw, clubRepo, repo, "")
	gin.SetMode(gin.TestMode)
	r := gin.New()
	for _, m := range []RoutesRegistrar{
		pm,
		NewContentModule(NewContentEngine(repo), []byte(leadTestSecret)),
		NewClubModule(NewClubHandler(clubRepo, repo, testBotToken, leadTestSecret)),
		NewClubActionModule(action, []byte(leadTestSecret)),
		NewClubResidentModule(action, []byte(leadTestSecret)),
		NewMigrationModule(NewBundleMigration(gw, clubRepo, pg.NewBotRepo(e.db), repo, writes), []byte(leadTestSecret)),
		NewClubAuditModule(NewClubAudit(clubRepo, repo, gw), []byte(leadTestSecret)),
	} {
		m.Register(r)
	}
	return &leadEnv{r: r, repo: repo, auth: a, pm: pm}
}

func (le *leadEnv) token(t *testing.T, tg int64, role string) string {
	tok, _, err := le.auth.jwt.GenerateTokens("tg:"+strconv.FormatInt(tg, 10), role, []string{})
	if err != nil {
		t.Fatal(err)
	}
	return tok
}

func (le *leadEnv) call(method, path, tok string, body any) *httptest.ResponseRecorder {
	var rd *bytes.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	} else {
		rd = bytes.NewReader(nil)
	}
	req := httptest.NewRequest(method, path, rd)
	req.Header.Set("Content-Type", "application/json")
	if tok != "" {
		req.Header.Set("Authorization", "Bearer "+tok)
	}
	w := httptest.NewRecorder()
	le.r.ServeHTTP(w, req)
	return w
}

func widgetFor(id int64, first, username string) map[string]any {
	f := map[string]string{"id": strconv.FormatInt(id, 10), "first_name": first, "auth_date": strconv.FormatInt(time.Now().Unix(), 10)}
	if username != "" {
		f["username"] = username
	}
	out := map[string]any{}
	for k, v := range f {
		out[k] = v
	}
	out["hash"] = signWidget(f, testBotToken)
	return out
}

func crmCard(t *testing.T, repo *pg.PlatformRepo, tg int64) map[string]any {
	t.Helper()
	d, err := repo.GetDoc(context.Background(), "club", "bs_crm")
	if err != nil || d == nil {
		return nil
	}
	var crm map[string]any
	_ = json.Unmarshal([]byte(d.Value), &crm)
	leads, _ := crm["leads"].([]any)
	return findLeadByTg(leads, tg)
}

// Login: anyone with a valid Telegram is a lead (team and residents as before),
// lands in the CRM quietly and keeps an earlier source.
func TestPlatformLoginLead(t *testing.T) {
	le := newLeadEnv(t)
	ctx := context.Background()
	login := func(id int64, first, user string) (int, map[string]any) {
		w := le.call("POST", "/api/v1/platform/auth/telegram", "", map[string]any{"widget": widgetFor(id, first, user)})
		var j map[string]any
		_ = json.Unmarshal(w.Body.Bytes(), &j)
		return w.Code, j
	}
	code, j := login(777001, "Айгерим", "aigerim")
	u, _ := j["user"].(map[string]any)
	if code != 200 || u["role"] != "lead" || j["token"] == "" {
		t.Fatalf("lead login: %d %v", code, j)
	}
	if team, _ := j["team"].(map[string]any); len(team) != 0 {
		t.Fatalf("a lead must not get the team list: %v", team)
	}
	l := crmCard(t, le.repo, 777001)
	if l == nil || l["source"] != "platform_login" || l["col"] != "new" || l["tg"] != "@aigerim" || l["funnel"] != "platform" {
		t.Fatalf("crm card: %v", l)
	}
	// an existing lead keeps its source
	_ = le.auth.leads.mutate(ctx, "bs_crm", func(crm map[string]any) bool {
		leads, _ := crm["leads"].([]any)
		findLeadByTg(leads, 777001)["source"] = "Threads"
		return true
	})
	if code, _ := login(777001, "Айгерим", "aigerim"); code != 200 {
		t.Fatal(code)
	}
	if l := crmCard(t, le.repo, 777001); l["source"] != "Threads" {
		t.Fatalf("source overwritten: %v", l["source"])
	}
	if code, j := login(777002, "Рез", ""); code != 200 || j["user"].(map[string]any)["role"] != "resident" {
		t.Fatalf("resident login: %d %v", code, j)
	}
	if code, j := login(453800951, "Рустам", ""); code != 200 || j["user"].(map[string]any)["role"] != "admin" {
		t.Fatalf("team login: %d %v", code, j)
	}
	if l := crmCard(t, le.repo, 453800951); l != nil {
		t.Fatal("the team must not land in the CRM")
	}
}

var routeParam = regexp.MustCompile(`[:*][A-Za-z_]+`)

// Every route of the platform and the club that needs a login refuses a
// lead's token; the lead home is the only thing it opens.
func TestLeadDeniedEverywhere(t *testing.T) {
	le := newLeadEnv(t)
	lead := le.token(t, 777001, "lead")
	checked, denied := 0, 0
	for _, rt := range le.r.Routes() {
		path := routeParam.ReplaceAllString(rt.Path, "x")
		if strings.HasPrefix(rt.Path, "/api/v1/platform/lead/") {
			continue
		}
		anon := le.call(rt.Method, path, "", nil).Code
		if anon != http.StatusUnauthorized && anon != http.StatusForbidden {
			continue // public: config, login, signed script calls, open templates
		}
		checked++
		w := le.call(rt.Method, path, lead, nil)
		// signed script calls (import, sync, ingest) ignore the bearer: 401 as for anyone
		if w.Code != http.StatusForbidden && w.Code != http.StatusUnauthorized {
			t.Errorf("%s %s: lead got %d %s", rt.Method, rt.Path, w.Code, w.Body.String())
			continue
		}
		if w.Code == http.StatusForbidden {
			denied++
		}
	}
	if checked < 60 {
		t.Fatalf("only %d guarded routes checked", checked)
	}
	t.Logf("guarded routes: %d, lead refused on all (403 on %d JWT routes, the rest are signed script calls)", checked, denied)
	for _, p := range []string{"/api/v1/platform/sync", "/api/v1/platform/library/rich", "/api/v1/platform/ops",
		"/api/v1/platform/crm/chats", "/api/v1/club/residents", "/api/v1/platform/library/template/seed_tl_0.pdf"} {
		if c := le.call("GET", p, lead, nil).Code; c != http.StatusForbidden {
			t.Errorf("GET %s: lead %d", p, c)
		}
	}
	if c := le.call("POST", "/api/v1/club/claim", lead, map[string]any{"tg": 1, "action": "lead"}).Code; c != http.StatusForbidden {
		t.Errorf("claim decision by a lead: %d", c)
	}
	if c := le.call("PUT", "/api/v1/platform/docs/bs_crm", lead, map[string]any{"value": "{}"}).Code; c != http.StatusForbidden {
		t.Errorf("CRM write by a lead: %d", c)
	}
	// a resident's token does not open the lead home (the resident has their own)
	if c := le.call("GET", "/api/v1/platform/lead/home", le.token(t, 777002, "resident"), nil).Code; c != http.StatusForbidden {
		t.Errorf("resident on the lead home: %d", c)
	}
}

// Only platform_module.go opens routes to leads.
func TestAllowLeadOnlyForLeadHome(t *testing.T) {
	files, _ := filepath.Glob("*.go")
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		b, _ := os.ReadFile(f)
		if n := strings.Count(string(b), "AuthJWTAllowLead("); n > 0 && (f != "platform_module.go" || n != 1) {
			t.Errorf("%s uses AuthJWTAllowLead %d times", f, n)
		}
	}
}

func TestLeadHome(t *testing.T) {
	le := newLeadEnv(t)
	ctx := context.Background()
	w := le.call("POST", "/api/v1/platform/auth/telegram", "", map[string]any{"widget": widgetFor(777001, "Айгерим", "aigerim")})
	var lj map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &lj)
	lead, _ := lj["token"].(string)
	admin := le.token(t, 453800951, "admin")
	now := time.Now().In(almaty)
	slotAt := now.Add(48 * time.Hour).Truncate(time.Hour)
	slots := map[string]any{"price": 50000, "kaspiLink": "https://pay.kaspi.kz/pay/test", "slots": []any{
		map[string]any{"id": "s1", "start": slotAt.Format(time.RFC3339), "dur": 60, "format": "Онлайн", "status": "free"},
	}}
	sb, _ := json.Marshal(slots)
	if _, err := le.repo.PutDoc(ctx, "club", "bs_slots", 0, string(sb), false, "test"); err != nil {
		t.Fatal(err)
	}
	feed := map[string]any{"items": []any{
		map[string]any{"title": "Прошедшее", "date": now.AddDate(0, 0, -2).Format("2006-01-02")},
		map[string]any{"title": "Форум предпринимателей", "date": now.AddDate(0, 0, 3).Format("2006-01-02"), "time": "10:00", "url": "https://example.kz"},
	}}
	fb, _ := json.Marshal(feed)
	if _, err := le.repo.PutDoc(ctx, "club", "bs_events_feed", 0, string(fb), false, "test"); err != nil {
		t.Fatal(err)
	}

	w = le.call("GET", "/api/v1/platform/lead/home", lead, nil)
	if w.Code != 200 {
		t.Fatalf("home: %d %s", w.Code, w.Body.String())
	}
	if strings.Contains(w.Body.String(), `"_seed"`) {
		t.Fatal("the library's internal hashes leak to a lead")
	}
	var home struct {
		Preview bool                  `json:"preview"`
		User    struct{ Name string } `json:"user"`
		Express struct {
			Items []struct {
				ID        string   `json:"id"`
				Organ     string   `json:"organ"`
				Questions []string `json:"questions"`
			} `json:"items"`
		} `json:"express"`
		Lib struct {
			Open   []map[string]any `json:"open"`
			Locked []map[string]any `json:"locked"`
			Tools  int              `json:"tools"`
			Diag   int              `json:"diag"`
		} `json:"lib"`
		Events []map[string]any `json:"events"`
		Razbor struct {
			Price     int64            `json:"price"`
			KaspiLink string           `json:"kaspiLink"`
			Slots     []map[string]any `json:"slots"`
		} `json:"razbor"`
		Bot map[string]string `json:"bot"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &home)
	organs := map[string]bool{}
	for _, it := range home.Express.Items {
		organs[it.Organ] = true
		if len(it.Questions) != 3 {
			t.Errorf("%s: %d questions", it.ID, len(it.Questions))
		}
	}
	if home.Preview || home.User.Name != "Айгерим" || len(home.Express.Items) != 8 || len(organs) != 8 {
		t.Fatalf("express: preview=%v name=%q items=%d organs=%v", home.Preview, home.User.Name, len(home.Express.Items), organs)
	}
	if len(home.Lib.Open) != 11 || len(home.Lib.Open)+len(home.Lib.Locked) != home.Lib.Tools+home.Lib.Diag || home.Lib.Tools < 100 {
		t.Fatalf("library: open %d locked %d of %d+%d", len(home.Lib.Open), len(home.Lib.Locked), home.Lib.Tools, home.Lib.Diag)
	}
	for _, l := range home.Lib.Locked {
		if l["steps"] != nil || l["desc"] != nil || l["template"] != nil || l["test"] != nil {
			t.Fatalf("a locked item carries its content: %v", l["id"])
		}
	}
	if len(home.Events) != 1 || home.Events[0]["title"] != "Форум предпринимателей" {
		t.Fatalf("events: %v", home.Events)
	}
	if home.Razbor.Price != 50000 || home.Razbor.KaspiLink != "https://pay.kaspi.kz/pay/test" || len(home.Razbor.Slots) != 1 {
		t.Fatalf("razbor: %+v", home.Razbor)
	}
	if !strings.Contains(home.Bot["checklists"], "?start=99") {
		t.Fatalf("bot: %v", home.Bot)
	}

	// express diagnostic: wrong answers refused, a lead's result in the CRM
	ans := map[string][]int{}
	for i, id := range LeadExpress {
		switch {
		case i < 2:
			ans[id] = []int{1, 1, 0} // sick
		case i == 2:
			ans[id] = []int{0, 1, 0} // risk
		default:
			ans[id] = []int{0, 0, 0}
		}
	}
	if c := le.call("POST", "/api/v1/platform/lead/diag", lead, map[string]any{"answers": map[string][]int{"seed_dx_0": {1}}}).Code; c != 400 {
		t.Fatalf("incomplete answers: %d", c)
	}
	w = le.call("POST", "/api/v1/platform/lead/diag", lead, map[string]any{"answers": ans})
	var dr struct{ Results []LeadDiagResult }
	_ = json.Unmarshal(w.Body.Bytes(), &dr)
	if w.Code != 200 || len(dr.Results) != 8 || dr.Results[0].Level != "sick" || dr.Results[2].Level != "risk" || dr.Results[3].Level != "ok" {
		t.Fatalf("diag: %d %s", w.Code, w.Body.String())
	}
	l := crmCard(t, le.repo, 777001)
	ex, _ := l["express"].(map[string]any)
	if ex == nil || l["hot"] != true || !strings.Contains(lastLog(l), "болит Финансы, Продажи") {
		t.Fatalf("crm express: %v / %s", ex, lastLog(l))
	}
	w = le.call("GET", "/api/v1/platform/lead/home", lead, nil)
	if !strings.Contains(w.Body.String(), `"result":{`) {
		t.Fatal("home must return the saved result")
	}

	// the team's preview: same home, no CRM write, no booking
	w = le.call("GET", "/api/v1/platform/lead/home", admin, nil)
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"preview":true`) {
		t.Fatalf("preview: %d", w.Code)
	}
	if c := le.call("POST", "/api/v1/platform/lead/diag", admin, map[string]any{"answers": ans}).Code; c != 200 {
		t.Fatalf("preview diag: %d", c)
	}
	if crmCard(t, le.repo, 453800951) != nil {
		t.Fatal("the preview wrote a CRM card")
	}
	if c := le.call("POST", "/api/v1/platform/lead/book", admin, map[string]any{"slotId": "s1"}).Code; c != 403 {
		t.Fatalf("preview booking: %d", c)
	}

	// booking: the slot, the CRM («Записан на разбор»), warming stops
	w = le.call("POST", "/api/v1/platform/lead/book", lead, map[string]any{"slotId": "s1", "phone": "+7 701 000 00 00", "niche": "кофейни"})
	if w.Code != 200 || !strings.Contains(w.Body.String(), "pay.kaspi.kz") {
		t.Fatalf("book: %d %s", w.Code, w.Body.String())
	}
	if c := le.call("POST", "/api/v1/platform/lead/book", lead, map[string]any{"slotId": "s1"}).Code; c != 409 {
		t.Fatalf("second booking: %d", c)
	}
	l = crmCard(t, le.repo, 777001)
	if l["col"] != "meet" || l["warmStop"] != true || l["phone"] != "+7 701 000 00 00" || !strings.Contains(lastLog(l), "Записался на разбор") {
		t.Fatalf("crm after booking: %v %s", l["col"], lastLog(l))
	}
	w = le.call("GET", "/api/v1/platform/lead/home", lead, nil)
	if !strings.Contains(w.Body.String(), `"mine":{`) {
		t.Fatal("home must show the lead's booking")
	}
	// no free slot: a request with a phone
	if c := le.call("POST", "/api/v1/platform/lead/request", lead, map[string]any{"phone": "12"}).Code; c != 400 {
		t.Fatalf("request without a phone: %d", c)
	}
	if c := le.call("POST", "/api/v1/platform/lead/request", lead, map[string]any{"phone": "87010000000", "question": "Рост"}).Code; c != 200 {
		t.Fatalf("request: %d", c)
	}
	if l = crmCard(t, le.repo, 777001); l["razborRequest"] == nil {
		t.Fatal("request not in the card")
	}
}

func lastLog(l map[string]any) string {
	lg, _ := l["log"].([]any)
	if len(lg) == 0 {
		return ""
	}
	m, _ := lg[len(lg)-1].(map[string]any)
	s, _ := m["text"].(string)
	return s
}
