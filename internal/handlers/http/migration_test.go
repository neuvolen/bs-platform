package http

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/bnursik/business_surgery_backend/internal/bot"
	"github.com/bnursik/business_surgery_backend/internal/club"
	pg "github.com/bnursik/business_surgery_backend/internal/repository/pg"
	"github.com/bnursik/business_surgery_backend/migrations"
	"github.com/gin-gonic/gin"
	"github.com/jackc/pgx/v5"
)

// The script, as far as club writes and the bundle go.
type fakeClubScript struct {
	mu     sync.Mutex
	mode   map[string]string // action → ok | error | dup | garbage
	calls  []url.Values
	bundle func() []byte
}

func (f *fakeClubScript) handler(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	a := q.Get("action")
	f.mu.Lock()
	f.calls = append(f.calls, q)
	mode, b := f.mode[a], f.bundle
	f.mu.Unlock()
	if a == "getBotCache" {
		_, _ = w.Write(b())
		return
	}
	switch mode {
	case "error":
		_, _ = w.Write([]byte(`{"error":"Резидент не найден"}`))
	case "dup":
		_, _ = w.Write([]byte(`{"ok":true,"deduplicated":true}`))
	case "garbage":
		_, _ = w.Write([]byte(`undefined`))
	default:
		_, _ = w.Write([]byte(`{"ok":true}`))
	}
}

func (f *fakeClubScript) actions() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []string
	for _, q := range f.calls {
		if q.Get("action") != "getBotCache" {
			out = append(out, q.Get("action"))
		}
	}
	return out
}

func (f *fakeClubScript) set(action, mode string) {
	f.mu.Lock()
	f.mode[action] = mode
	f.mu.Unlock()
}

func clubTestDB(t *testing.T) (*pg.DB, *pg.ClubRepo) {
	t.Helper()
	dsn := os.Getenv("BS_TEST_DSN")
	if dsn == "" {
		t.Skip("BS_TEST_DSN not set")
	}
	ctx := context.Background()
	db, err := pg.NewDB(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(db.Pool.Close)
	if err := pg.Migrate(ctx, db, migrations.FS); err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{`TRUNCATE club_residents, club_payments, club_fines, club_meetings, club_reports, club_meeting_log,
		club_settings, club_pl_rows, club_imports, club_sheets, club_writes, club_bundle_checks, club_ops RESTART IDENTITY`,
		`DELETE FROM bot_meta WHERE key LIKE 'bundle_%'`} {
		if _, err := db.Pool.Exec(ctx, q); err != nil {
			t.Fatal(err)
		}
	}
	return db, pg.NewClubRepo(db)
}

func mustDate(s string) time.Time {
	t, ok := club.Date(s)
	if !ok {
		panic(s)
	}
	return t
}

// The sheet as an import brings it.
func sheetSnap() *club.Snapshot {
	j := mustDate("01.06.2026")
	return &club.Snapshot{
		Residents: []club.Resident{
			{Name: "Рустам", TgID: 453800951, Format: "Онлайн", Admin: true, JoinedAt: &j},
			{Name: "Асет", TgID: 478757502, Format: "Офлайн", Tariff: 100000, Granted: 3, Done: 1, RestEntry: 50000, JoinedAt: &j},
			{Name: "Альтаир", TgID: 490685605, Format: "Офлайн", Tariff: 100000, Granted: 3, Done: 2, JoinedAt: &j},
		},
		Fines: []club.Fine{{Name: "Асет", Type: "Не сдан отчёт", Amount: 10000, Date: mustDate("26.09.2026"), Row: 3, Status: "Не оплатил"}},
		Raw:   club.Sheets{club.SheetPL: {{"", "2026"}, {"Статья"}, {"Статья"}, {"ДОХОДЫ"}, {"ИТОГО ДОХОДЫ", "100"}, {"ЧИСТАЯ ПРИБЫЛЬ", "60"}}},
	}
}

// importSheet does what the hourly import does: the sheet's copy replaces the
// club tables, the writes the copy lacks are put back on top.
func importSheet(t *testing.T, repo *pg.ClubRepo, s *club.Snapshot, takenAt time.Time) int {
	t.Helper()
	ctx := context.Background()
	n := 0
	if err := repo.ReplaceAllThen(ctx, s, "sheet", func(ctx context.Context, tx pgx.Tx) error {
		var err error
		n, err = repo.ReapplyAfterImport(ctx, tx, takenAt)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if err := repo.LogImport(ctx, "sheet", false, []byte(`{}`), map[string]any{}); err != nil {
		t.Fatal(err)
	}
	return n
}

type clubEnv struct {
	g      *AppGateway
	f      *fakeClubScript
	r      *gin.Engine
	repo   *pg.ClubRepo
	db     *pg.DB
	writes *ClubWrites
	mig    *BundleMigration
	srvURL string
	now    *time.Time
}

func newClubEnv(t *testing.T) *clubEnv {
	db, repo := clubTestDB(t)
	if err := repo.ReplaceAll(context.Background(), sheetSnap(), "sheet"); err != nil {
		t.Fatal(err)
	}
	f := &fakeClubScript{mode: map[string]string{}}
	srv := httptest.NewServer(http.HandlerFunc(f.handler))
	t.Cleanup(srv.Close)
	g := NewAppGateway(testBotToken, srv.URL+"/exec")
	g.Admins = map[int64]string{453800951: "Рустам"}
	now := time.Now()
	g.now = func() time.Time { return now }
	g.Club = repo
	g.Ops = repo
	w := NewClubWrites(repo, g)
	w.now = func() time.Time { return now }
	g.Writes = w
	m := NewBundleMigration(g, repo, pg.NewBotRepo(db), nil, w)
	m.now = func() time.Time { return now }
	m.Sample = []SampleUser{{"admin", 453800951}, {"resident", 490685605}, {"lead", 999}}
	// The script's bundle is the server's own unless a test changes it.
	f.bundle = func() []byte {
		parts, _, err := g.ServerBundle(context.Background())
		if err != nil {
			return []byte(`{"error":"x"}`)
		}
		return mergeBundle(parts, []byte(`{"leads":{"leads":[{"name":"Лид"}]}}`), now)
	}
	gin.SetMode(gin.TestMode)
	r := gin.New()
	NewAppGatewayModule(g).Register(r)
	return &clubEnv{g: g, f: f, r: r, repo: repo, db: db, writes: w, mig: m, srvURL: srv.URL + "/exec", now: &now}
}

func (e *clubEnv) call(id int64, action string, kv ...string) map[string]any {
	q := url.Values{"action": {action}, "_tg": {makeInitData(testBotToken, id, "Рустам", *e.now)}}
	for i := 0; i+1 < len(kv); i += 2 {
		q.Set(kv[i], kv[i+1])
	}
	w := get(e.r, q)
	var out map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &out)
	if out == nil {
		out = map[string]any{"_raw": w.Body.String()}
	}
	return out
}

func (e *clubEnv) count(t *testing.T, q string, args ...any) int {
	t.Helper()
	var n int
	if err := e.db.Pool.QueryRow(context.Background(), q, args...).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func (e *clubEnv) statuses(t *testing.T) string {
	t.Helper()
	rows, err := e.db.Pool.Query(context.Background(), `SELECT action || ':' || status FROM club_writes ORDER BY id`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var s string
		_ = rows.Scan(&s)
		out = append(out, s)
	}
	return strings.Join(out, " ")
}

const scriptDown = "http://127.0.0.1:1/exec"

// A write goes to the server first; while the script is down it waits, in
// order, survives the hourly import and reaches the sheet once it is back.
func TestClubWritesServerFirstAndRetry(t *testing.T) {
	e := newClubEnv(t)
	ctx := context.Background()
	e.g.scriptURL = scriptDown

	r := e.call(453800951, "addFine", "name", "Альтаир", "type", "Опоздание", "amount", "5000")
	if r["queued"] != true || r["ok"] != true {
		t.Fatalf("the app hears it is kept: %v", r)
	}
	if n := e.count(t, `SELECT count(*) FROM club_fines WHERE resident = 'Альтаир' AND amount = 5000 AND status = 'Не оплатил'`); n != 1 {
		t.Fatalf("the server's tables have it at once: %d", n)
	}
	e.call(453800951, "updateFine", "name", "Асет", "row", "3", "status", "Оплатил")
	if e.statuses(t) != "addFine:pending updateFine:pending" {
		t.Fatalf("both wait: %s", e.statuses(t))
	}
	if e.count(t, `SELECT count(*) FROM club_writes WHERE action = 'updateFine' AND tries = 0`) != 1 {
		t.Fatal("the second waits behind the first without trying")
	}
	if e.count(t, `SELECT count(*) FROM club_ops WHERE action = 'addFine' AND ok`) != 1 {
		t.Fatal("journaled")
	}

	// The app reads the server's bundle in stage server, script or not.
	if err := e.mig.SetStage(ctx, StageServer, "test"); err != nil {
		t.Fatal(err)
	}
	w := get(e.r, url.Values{"action": {"getBotCache"}, "_tg": {makeInitData(testBotToken, 490685605, "Альтаир", *e.now)}})
	var b struct {
		Fines []club.BundleFine
		Leads map[string]any
	}
	_ = json.Unmarshal(w.Body.Bytes(), &b)
	if w.Code != 200 || w.Header().Get("X-BS-Bundle-Source") != "server" || len(b.Fines) != 2 || b.Fines[0].Status != "Оплатил" || b.Leads == nil {
		t.Fatalf("server bundle %d %s %s", w.Code, w.Header().Get("X-BS-Bundle-Source"), w.Body.String())
	}

	// The hourly import comes from a sheet that has neither: both are put back.
	if n := importSheet(t, e.repo, sheetSnap(), e.now.Add(-time.Minute)); n != 2 {
		t.Fatalf("reapplied %d", n)
	}
	if e.count(t, `SELECT count(*) FROM club_fines WHERE resident = 'Альтаир' AND amount = 5000`) != 1 ||
		e.count(t, `SELECT count(*) FROM club_fines WHERE resident = 'Асет' AND paid`) != 1 {
		t.Fatal("an import does not undo waiting writes")
	}

	// The script is back: the writes reach it in order, signed.
	e.g.scriptURL = e.srvURL
	*e.now = e.now.Add(time.Minute)
	if n, err := e.writes.Flush(ctx); err != nil || n != 2 {
		t.Fatalf("flush %d %v", n, err)
	}
	if got := strings.Join(e.f.actions(), ","); got != "addFine,updateFine" {
		t.Fatalf("order %s", got)
	}
	q := e.f.calls[len(e.f.calls)-1]
	if q.Get("chatId") != "453800951" || q.Get("_srv_sig") != AppSign(testBotToken, "updateFine", "453800951", q.Get("_srv_ts")) || q.Get("status") != "Оплатил" {
		t.Fatalf("resent %v", q)
	}
	if e.statuses(t) != "addFine:sent updateFine:sent" {
		t.Fatalf("after: %s", e.statuses(t))
	}
}

// The script refuses: the server's change is undone. A double: the sheet had
// it, the server's copy goes. Not the team: passed on, the server's tables
// untouched.
func TestClubWritesRejectedDoubleAndNotTeam(t *testing.T) {
	e := newClubEnv(t)
	e.f.set("addFine", "error")
	r := e.call(453800951, "addFine", "name", "Альтаир", "amount", "5000")
	if r["error"] != "Резидент не найден" {
		t.Fatalf("the app gets the script's answer: %v", r)
	}
	if e.count(t, `SELECT count(*) FROM club_fines WHERE resident = 'Альтаир'`) != 0 {
		t.Fatal("undone")
	}
	e.f.set("addPayment", "dup")
	e.call(453800951, "addPayment", "type", "income", "src", "БХ Штраф", "amount", "10000", "isCash", "true", "resident", "Асет")
	if e.count(t, `SELECT count(*) FROM club_payments`) != 0 || e.count(t, `SELECT count(*) FROM club_fines WHERE resident = 'Асет'`) != 1 {
		t.Fatal("a double is undone, the fine it closed is back")
	}
	if e.statuses(t) != "addFine:rejected addPayment:sent" {
		t.Fatalf("statuses %s", e.statuses(t))
	}
	e.call(478757502, "addFine", "name", "Альтаир", "amount", "5000")
	if e.count(t, `SELECT count(*) FROM club_fines WHERE resident = 'Альтаир'`) != 0 || e.count(t, `SELECT count(*) FROM club_writes WHERE NOT applied AND tg_id = 478757502`) != 1 {
		t.Fatal("a resident's write is passed on, not applied")
	}
	// A meeting done: one more meeting, «Лог встреч», the package ends → renewal debt.
	e.call(453800951, "confirmMeeting", "res", "Альтаир", "date", "01.10", "time", "15:00")
	if e.count(t, `SELECT count(*) FROM club_residents WHERE name = 'Альтаир' AND meetings_done = 3 AND renew_debt = 100000`) != 1 ||
		e.count(t, `SELECT count(*) FROM club_meeting_log WHERE resident = 'Альтаир'`) != 1 {
		t.Fatal("confirmMeeting")
	}
}

// No answer in time: the script may have written. The write waits for the
// import; seen there it is done without sending it twice, otherwise resent.
func TestClubWritesUnknownWaitsForImport(t *testing.T) {
	e := newClubEnv(t)
	ctx := context.Background()
	e.f.set("addFine", "garbage")
	if r := e.call(453800951, "addFine", "name", "Альтаир", "type", "Опоздание", "amount", "5000"); r["queued"] != true {
		t.Fatalf("answer %v", r)
	}
	if e.statuses(t) != "addFine:unknown" {
		t.Fatal(e.statuses(t))
	}
	e.f.set("addFine", "ok")
	*e.now = e.now.Add(time.Hour)
	if n, _ := e.writes.Flush(ctx); n != 0 || len(e.f.actions()) != 1 {
		t.Fatal("waits for the import, not sent again")
	}
	// The sheet did write it.
	s := sheetSnap()
	s.Fines = append(s.Fines, club.Fine{Name: "Альтаир", Type: "Опоздание", Amount: 5000, Date: club.Today(), Row: 4, Status: "Не оплатил"})
	time.Sleep(10 * time.Millisecond)
	importSheet(t, e.repo, s, time.Now())
	if e.statuses(t) != "addFine:sent" || e.count(t, `SELECT count(*) FROM club_fines WHERE resident = 'Альтаир'`) != 1 {
		t.Fatalf("seen in the sheet: %s", e.statuses(t))
	}

	// Another one the sheet did not get: resent after the import.
	e.f.set("setPartner", "garbage")
	e.call(453800951, "setPartner", "name", "Асет", "partner", "Альтаир")
	if e.count(t, `SELECT count(*) FROM club_residents WHERE partner <> ''`) != 2 {
		t.Fatal("partners both ways")
	}
	e.f.set("setPartner", "ok")
	time.Sleep(10 * time.Millisecond)
	importSheet(t, e.repo, sheetSnap(), time.Now())
	if e.count(t, `SELECT count(*) FROM club_residents WHERE partner <> ''`) != 2 {
		t.Fatal("put back on top of the import")
	}
	*e.now = time.Now().Add(2 * time.Hour)
	if n, err := e.writes.Flush(ctx); err != nil || n != 1 || e.f.actions()[len(e.f.actions())-1] != "setPartner" {
		t.Fatalf("resent: %d %v %v", n, err, e.f.actions())
	}
}

// The stage moves by itself after BundleStreak clean days, the owner can set
// it back; a day with differences reaches the owner once.
func TestBundleMigrationStages(t *testing.T) {
	e := newClubEnv(t)
	ctx := context.Background()
	var sent []string
	e.mig.Notify = func(_ context.Context, text string) { sent = append(sent, text) }
	if e.mig.Stage(ctx) != StageShadow {
		t.Fatal("starts in shadow")
	}
	day0 := time.Date(2026, 10, 1, 21, 30, 0, 0, club.Almaty) // after 21: compared even if not in step
	at := func(d int) {
		*e.now = day0.AddDate(0, 0, d)
		e.mig.lastTry = time.Time{}
		e.mig.stage = ""
	}
	for d := 0; d < BundleStreak; d++ {
		at(d)
		res, err := e.mig.Tick(ctx)
		if err != nil || res == nil || !res.OK || len(res.Users) != 3 {
			t.Fatalf("day %d: %+v %v", d, res, err)
		}
		if res2, _ := e.mig.Tick(ctx); res2 != nil {
			t.Fatal("once a day")
		}
		want := StageShadow
		if d == BundleStreak-1 {
			want = StageServer
		}
		if got := e.mig.Stage(ctx); got != want {
			t.Fatalf("day %d: stage %s", d, got)
		}
	}
	if len(sent) != 0 {
		t.Fatal("clean days are silent")
	}
	st, err := e.mig.Status(ctx)
	if err != nil || st.Streak != BundleStreak || st.StageBy != "auto" || len(st.History) != BundleStreak || len(st.Script) == 0 ||
		st.ScriptUpdate.Latest != bot.LatestScript {
		t.Fatalf("status %+v %v", st, err)
	}

	// The sheet differs: the streak goes, the owner hears once that day.
	f := e.f.bundle
	e.f.bundle = func() []byte {
		var m map[string]any
		_ = json.Unmarshal(f(), &m)
		m["totalDebt"] = 1.0
		b, _ := json.Marshal(m)
		return b
	}
	at(BundleStreak)
	if res, _ := e.mig.Tick(ctx); res == nil || res.OK || res.Count != 1 || res.Mismatches[0].Role != "admin,resident,lead" {
		t.Fatalf("difference %+v", res)
	}
	if _, err := e.mig.RunCheck(ctx); err != nil {
		t.Fatal(err)
	}
	if len(sent) != 1 || !strings.Contains(sent[0], "totalDebt") {
		t.Fatalf("one message: %q", sent)
	}
	if n, _ := e.mig.Streak(ctx); n != 0 || e.mig.Stage(ctx) != StageServer {
		t.Fatal("the server keeps answering; the streak starts again")
	}

	// The owner sends it back to the sheet: no more comparisons.
	if err := e.mig.SetStage(ctx, StageSheet, "tg:453800951"); err != nil {
		t.Fatal(err)
	}
	at(BundleStreak + 1)
	if res, _ := e.mig.Tick(ctx); res != nil {
		t.Fatal("sheet: nothing compared")
	}
	if err := e.mig.SetStage(ctx, "nonsense", "x"); err == nil {
		t.Fatal("unknown stage")
	}
}

// In stage server the script answers when the server cannot build the bundle.
func TestServerBundleFallsBackToScript(t *testing.T) {
	e := newClubEnv(t)
	ctx := context.Background()
	if err := e.mig.SetStage(ctx, StageServer, "test"); err != nil {
		t.Fatal(err)
	}
	e.g.Club = failingSource{}
	e.f.bundle = func() []byte { return []byte(`{"residents":[],"from":"script"}`) }
	w := get(e.r, url.Values{"action": {"getBotCache"}, "_tg": {makeInitData(testBotToken, 999, "Лид", *e.now)}})
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"from":"script"`) {
		t.Fatalf("fallback %d %s", w.Code, w.Body.String())
	}
	if _, fb := e.g.ServerStats(); fb != 1 {
		t.Fatal("counted")
	}
}

type failingSource struct{}

func (failingSource) LoadBundle(context.Context, time.Time) (*club.Snapshot, error) {
	return nil, context.DeadlineExceeded
}

func TestMergeBundleOrderAndDefaults(t *testing.T) {
	now := time.UnixMilli(1790000000000)
	b := mergeBundle(map[string]json.RawMessage{"residents": json.RawMessage(`[1]`)}, []byte(`{"_cached":true,"x":1,"residents":[0],"wheelAll":{"a":1}}`), now)
	s := string(b)
	if !strings.HasPrefix(s, `{"residents":[1],"wheelSummary":null,"wheelAll":{"a":1},"leads":{"leads":[]}`) || !strings.Contains(s, `"ts":1790000000000,"x":1}`) || strings.Contains(s, "_cached") {
		t.Fatal(s)
	}
}

// The script is back: the next write takes the waiting ones along, first.
func TestClubWritesNextWriteSendsWaitingFirst(t *testing.T) {
	e := newClubEnv(t)
	e.g.scriptURL = scriptDown
	e.call(453800951, "addFine", "name", "Альтаир", "amount", "5000")
	e.g.scriptURL = e.srvURL
	r := e.call(453800951, "setPartner", "name", "Асет", "partner", "Альтаир")
	if r["ok"] != true || r["queued"] != nil {
		t.Fatalf("the script's own answer: %v", r)
	}
	if got := strings.Join(e.f.actions(), ","); got != "addFine,setPartner" || e.statuses(t) != "addFine:sent setPartner:sent" {
		t.Fatalf("order %s, %s", got, e.statuses(t))
	}
}

// A resident's payment extends the package as the script's addMeetingsOnPayment
// does (v32): the tariff stays, «проведено» goes to 0, the rest carries over.
// A fine payment leaves the package alone.
func TestClubResidentPaymentExtendsPackage(t *testing.T) {
	e := newClubEnv(t)
	ctx := context.Background()
	row := func() (tariff, granted, done int64) {
		t.Helper()
		if err := e.db.Pool.QueryRow(ctx, `SELECT tariff, meetings_granted, meetings_done FROM club_residents WHERE name = 'Асет'`).Scan(&tariff, &granted, &done); err != nil {
			t.Fatal(err)
		}
		return
	}
	t0, g0, d0 := row()
	if t0 <= 0 {
		t.Fatalf("tariff %d", t0)
	}
	e.call(453800951, "addPayment", "type", "income", "src", "БХ Штраф", "amount", "10000", "isCash", "true", "resident", "Асет")
	if t1, g1, d1 := row(); t1 != t0 || g1 != g0 || d1 != d0 {
		t.Fatalf("fine payment changed the package: %d %d %d", t1, g1, d1)
	}
	e.call(453800951, "addPayment", "type", "income", "src", "БХ Трекинг продление", "amount", strconv.FormatInt(2*t0, 10), "isCash", "false", "resident", "Асет")
	months := int64(3)
	switch {
	case t0 >= 1000000:
		months = 12
	case t0 < 400000:
		months = 1
	}
	if t1, g1, d1 := row(); t1 != t0 || d1 != 0 || g1 != months*3*2+g0-d0 {
		t.Fatalf("after payment: tariff %d granted %d done %d (was %d %d %d)", t1, g1, d1, t0, g0, d0)
	}
}
