package http

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/bnursik/business_surgery_backend/internal/bot"
	"github.com/bnursik/business_surgery_backend/internal/club"
	"github.com/bnursik/business_surgery_backend/internal/content"
	pg "github.com/bnursik/business_surgery_backend/internal/repository/pg"
	"github.com/gin-gonic/gin"
	"github.com/golang-jwt/jwt/v5"
)

// R32d: the server owns the club's data. The sheet's copy is optional (an
// admin's switch, on for the first week by default), the sheet is compared
// once at the deploy and gives only what it alone has, an admin can import
// a copy by hand in an emergency, and SHEET_MODE=legacy rolls everything back.

const ownerSecret = "test-secret" // newOffEnv's club handler

func ownerJWT(t *testing.T, role string) string {
	t.Helper()
	tok := jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.MapClaims{"sub": "tg:453800951", "role": role, "typ": "access",
		"exp": time.Now().Add(time.Hour).Unix()})
	s, err := tok.SignedString([]byte(ownerSecret))
	if err != nil {
		t.Fatal(err)
	}
	return s
}

type ownerEnv struct {
	*offEnv
	owner *SheetOwner
}

func newOwnerEnv(t *testing.T, master string, imported *time.Time) *ownerEnv {
	e := newOffEnv(t, master, imported)
	o := NewSheetOwner(e.repo, pg.NewBotRepo(e.db), e.cut, testBotToken)
	t.Cleanup(o.Install())
	NewSheetOwnerModule(o, []byte(ownerSecret)).Register(e.r)
	return &ownerEnv{offEnv: e, owner: o}
}

func (e *ownerEnv) req(method, path, tok string, body any) (int, map[string]any) {
	var b []byte
	if body != nil {
		b, _ = json.Marshal(body)
	}
	r := httptest.NewRequest(method, path, bytes.NewReader(b))
	r.Header.Set("Content-Type", "application/json")
	if tok != "" {
		r.Header.Set("Authorization", "Bearer "+tok)
	}
	w := httptest.NewRecorder()
	e.r.ServeHTTP(w, r)
	var out map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &out)
	return w.Code, out
}

func (e *ownerEnv) signedCode(path string, v map[string]any) (int, map[string]any) {
	v["ts"] = time.Now().Unix()
	b, _ := json.Marshal(v)
	r := httptest.NewRequest("POST", path, bytes.NewReader(b))
	r.Header.Set("X-BS-Signature", sign(b))
	w := httptest.NewRecorder()
	e.r.ServeHTTP(w, r)
	var out map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &out)
	return w.Code, out
}

// The sheet as the dormant script sends it.
func ownerSheets(extraDebet [][]string, dds [][]string, fines [][]string) club.Sheets {
	debet := [][]string{
		{"№", "Имя резидента", "Тариф", "Встреч оплачено", "Встреч проведено", "Осталось", "Оплачено (вход)", "Остаток (вход)",
			"Долг продление", "Штрафы", "Общий долг", "Бывший", "Исключение", "Chat ID", "Формат", "Админ"},
		{"1", "Асет", "100000", "3", "1", "2", "0", "50000", "0", "0", "0", "", "", "478757502", "Офлайн", ""},
		{"2", "Альтаир", "100000", "3", "2", "1", "0", "0", "0", "0", "0", "", "", "490685605", "Офлайн", ""},
		{"3", "Рустам", "0", "0", "0", "0", "0", "0", "0", "0", "0", "", "", "453800951", "Онлайн", "Да"},
	}
	debet = append(debet, extraDebet...)
	s := club.Sheets{
		club.SheetDebet: debet,
		club.SheetDDS:   append([][]string{{"Дата", "Приход", "Расход", "Источник", "Категория +", "Категория -"}}, dds...),
		club.SheetFines: append([][]string{{"№", "Имя резидента", "Тип", "Сумма", "Дата", "Статус"},
			{"1", "Асет", "Не сдан отчёт", "10,000", "26.09.2026", "Не оплатил"}}, fines...),
	}
	return s
}

func TestSheetExportSwitch(t *testing.T) {
	t.Setenv("SHEET_EXPORT", "")
	e := newOwnerEnv(t, "server", nil)
	ctx := context.Background()
	if st, _ := e.cut.State(ctx); st != "done" {
		t.Fatalf("cutover %q", st)
	}
	// The first week after the cutover: on, the script is told "mirror".
	if x := e.owner.Export(ctx); !x.On || x.Why != "week" || x.Until == "" {
		t.Fatalf("export %+v", x)
	}
	if ctl := e.signed("/api/v1/bot/control", map[string]any{"version": bot.LatestScript}); ctl["sheetMode"] != "mirror" {
		t.Fatalf("control %v", ctl)
	}
	code, out := e.signedCode("/api/v1/club/export", map[string]any{"tables": true})
	if code != 200 {
		t.Fatalf("export %d %v", code, out)
	}
	var names []string
	for _, x := range out["tables"].([]any) {
		names = append(names, x.(map[string]any)["name"].(string))
	}
	if strings.Join(names, ",") != "Резиденты,Штрафы,Встречи,Отчёты" || out["payments"] == nil {
		t.Fatalf("copy tabs %v", names)
	}
	// A week later: off by itself.
	e.owner.now = func() time.Time { return time.Now().Add(ExportWeek + time.Hour) }
	if x := e.owner.Export(ctx); x.On {
		t.Fatalf("after the week %+v", x)
	}
	if ctl := e.signed("/api/v1/bot/control", map[string]any{"version": bot.LatestScript}); ctl["sheetMode"] != "off" {
		t.Fatalf("control after the week %v", ctl)
	}
	if code, out := e.signedCode("/api/v1/club/export", map[string]any{}); code != http.StatusConflict || out["error"] != "export_off" {
		t.Fatalf("export when off: %d %v", code, out)
	}
	// The admin's switch on the platform; a moderator may look, not switch.
	admin, mod := ownerJWT(t, "admin"), ownerJWT(t, "moderator")
	if code, _ := e.req("POST", "/api/v1/club/sheet/export", mod, map[string]any{"on": true}); code != http.StatusForbidden {
		t.Fatalf("moderator switched: %d", code)
	}
	code, st := e.req("POST", "/api/v1/club/sheet/export", admin, map[string]any{"on": true})
	if ex, _ := st["export"].(map[string]any); code != 200 || ex["on"] != true || ex["why"] != "admin" {
		t.Fatalf("admin on: %d %v", code, st)
	}
	if code, _ := e.signedCode("/api/v1/club/export", map[string]any{}); code != 200 {
		t.Fatalf("export after the admin's on: %d", code)
	}
	if code, st := e.req("GET", "/api/v1/club/sheet", mod, nil); code != 200 || st["master"] != "server" {
		t.Fatalf("status %d %v", code, st)
	}
	code, st = e.req("POST", "/api/v1/club/sheet/export", admin, map[string]any{"on": false})
	if ex, _ := st["export"].(map[string]any); code != 200 || ex["on"] != false {
		t.Fatalf("admin off: %v", st)
	}
	// Back to the default; SHEET_EXPORT sets it without a code change.
	t.Setenv("SHEET_EXPORT", "on")
	_, st = e.req("POST", "/api/v1/club/sheet/export", admin, map[string]any{"reset": true})
	if ex, _ := st["export"].(map[string]any); ex["on"] != true || ex["why"] != "env" {
		t.Fatalf("env default: %v", st)
	}
	if n := atomic.LoadInt64(e.script); n != 0 {
		t.Fatalf("the script was called %d times", n)
	}
}

func TestSheetReconcileOnceAtDeploy(t *testing.T) {
	imported := time.Now().Add(-48 * time.Hour)
	e := newOwnerEnv(t, "server", &imported)
	ctx := context.Background()
	e.owner.Begin(ctx)
	if !e.owner.ReconcileWanted(ctx) {
		t.Fatal("the deploy did not ask for the sheet's copy")
	}
	if ctl := e.signed("/api/v1/bot/control", map[string]any{"version": bot.LatestScript}); ctl["reconcile"] != true {
		t.Fatalf("control %v", ctl)
	}
	before := e.n(`SELECT count(*) FROM club_payments`)
	today := time.Now().In(club.Almaty).Format("02.01.2006")
	sheets := ownerSheets(
		[][]string{{"4", "Дана", "100000", "3", "0", "3", "0", "0", "0", "0", "0", "", "", "555000111", "Онлайн", ""}},
		[][]string{{today, "15,000", "", "БХ Трекинг", "Асет", ""}, {"01.07.2026", "9,000", "", "Штраф", "Асет", ""}},
		[][]string{{"2", "Альтаир", "Опоздание", "5,000", today, "Не оплатил"}},
	)
	sheets[club.SheetProfit] = [][]string{{"Дата", "Резидент", "Выручка", "Прибыль"}, {today, "Дана", "1,000,000", "200,000"}}
	code, res := e.signedCode("/api/v1/club/reconcile", map[string]any{"sheets": sheets})
	if code != 200 || res["saved"] != true || res["mode"] != "merge" {
		t.Fatalf("reconcile %d %v", code, res)
	}
	if n := e.n(`SELECT count(*) FROM club_payments`); n != before+1 {
		t.Fatalf("payments %d → %d: only today's is new", before, n)
	}
	if e.n(`SELECT count(*) FROM club_payments WHERE income = 9000`) != 0 {
		t.Fatal("an old row only the sheet had came back")
	}
	if e.n(`SELECT count(*) FROM club_residents WHERE name = 'Дана' AND tg_id = 555000111`) != 1 {
		t.Fatal("the new resident was not taken")
	}
	if e.n(`SELECT count(*) FROM club_fines WHERE resident = 'Альтаир' AND type = 'Опоздание'`) != 1 {
		t.Fatal("the new fine was not taken")
	}
	if e.n(`SELECT count(*) FROM club_residents WHERE name = 'Асет'`) != 1 {
		t.Fatal("a resident both have was doubled")
	}
	if e.n(`SELECT count(*) FROM club_sheets WHERE name = $1`, club.SheetProfit) != 1 {
		t.Fatal("the archive sheet the server lacked was not taken")
	}
	// Done once: the script is not asked again, a second copy is refused.
	if e.owner.ReconcileWanted(ctx) {
		t.Fatal("still wanted")
	}
	if code, _ := e.signedCode("/api/v1/club/reconcile", map[string]any{"sheets": sheets}); code != http.StatusConflict {
		t.Fatalf("second copy: %d", code)
	}
	_, st := e.req("GET", "/api/v1/club/sheet", ownerJWT(t, "moderator"), nil)
	last, _ := st["last"].(map[string]any)
	if st["reconcile"] != "done" || last == nil || last["added"].(float64) < 4 || !strings.Contains(last["summary"].(string), "взято из таблицы") {
		t.Fatalf("status %v", st)
	}
	// An admin asks again (e.g. after a hand edit in the sheet).
	if code, _ := e.req("POST", "/api/v1/club/import/request", ownerJWT(t, "admin"), nil); code != 200 || !e.owner.ReconcileWanted(ctx) {
		t.Fatalf("request %d", code)
	}
	// No copy within two days: given up, the server keeps its data.
	e.owner.now = func() time.Time { return time.Now().Add(reconcileWait + time.Hour) }
	e.owner.cache = map[string]cachedMeta{}
	e.owner.Check(ctx)
	if st, _ := e.owner.ReconcileState(ctx); st != "skipped" {
		t.Fatalf("state %q", st)
	}
}

func TestSheetFinalImportIsReconciled(t *testing.T) {
	recent := time.Now().Add(-30 * time.Minute)
	e := newOwnerEnv(t, "sheet", &recent)
	ctx := context.Background()
	if !e.cut.Pending(ctx) {
		t.Fatal("not waiting for the final import")
	}
	ch := NewClubHandler(e.repo, nil, testBotToken, ownerSecret)
	ch.Cutover, ch.Owner = e.cut, e.owner
	r := gin.New()
	NewClubModule(ch).Register(r)
	sheets := ownerSheets([][]string{{"4", "Дана", "100000", "3", "0", "3", "0", "0", "0", "0", "0", "", "", "555000111", "Онлайн", ""}}, nil, nil)
	b, _ := json.Marshal(map[string]any{"ts": time.Now().Unix(), "sheets": sheets, "final": true})
	req := httptest.NewRequest("POST", "/api/v1/club/import", bytes.NewReader(b))
	req.Header.Set("X-BS-Signature", sign(b))
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != 200 {
		t.Fatalf("final import %d %s", w.Code, w.Body.String())
	}
	res := e.owner.LastResult(ctx)
	if res == nil || res.Mode != "final" || res.Report == nil || res.Report.Differences == 0 {
		t.Fatalf("final import not reconciled: %+v", res)
	}
	if st, _ := e.owner.ReconcileState(ctx); st != "done" {
		t.Fatalf("state %q", st)
	}
	if m, _ := e.repo.Master(ctx); m != "server" {
		t.Fatalf("master %q", m)
	}
}

func TestSheetManualImport(t *testing.T) {
	e := newOwnerEnv(t, "server", nil)
	ctx := context.Background()
	admin := ownerJWT(t, "admin")
	today := time.Now().In(club.Almaty).Format("02.01.2006")
	body := map[string]any{"sheets": ownerSheets(nil, [][]string{{today, "20,000", "", "БХ Трекинг", "Альтаир", ""}}, nil)}
	if code, _ := e.req("POST", "/api/v1/club/import/manual", ownerJWT(t, "moderator"), body); code != http.StatusForbidden {
		t.Fatalf("moderator imported: %d", code)
	}
	before := e.n(`SELECT count(*) FROM club_payments`)
	code, res := e.req("POST", "/api/v1/club/import/manual?dry=1", admin, body)
	if code != 200 || res["saved"] != false || e.n(`SELECT count(*) FROM club_payments`) != before {
		t.Fatalf("dry %d %v", code, res)
	}
	code, res = e.req("POST", "/api/v1/club/import/manual", admin, body)
	if code != 200 || res["saved"] != true || e.n(`SELECT count(*) FROM club_payments`) != before+1 {
		t.Fatalf("merge %d %v", code, res)
	}
	// Merging the same copy again adds nothing.
	if _, res = e.req("POST", "/api/v1/club/import/manual", admin, body); res["added"].(float64) != 0 {
		t.Fatalf("second merge %v", res)
	}
	// Replace: only with the confirmation; the server stays the master.
	if code, _ := e.req("POST", "/api/v1/club/import/manual?mode=replace", admin, body); code != http.StatusBadRequest {
		t.Fatalf("replace without confirm: %d", code)
	}
	code, res = e.req("POST", "/api/v1/club/import/manual?mode=replace&confirm=replace", admin, body)
	if code != 200 || res["saved"] != true {
		t.Fatalf("replace %d %v", code, res)
	}
	if n := e.n(`SELECT count(*) FROM club_payments`); n != 1 {
		t.Fatalf("after replace %d payments", n)
	}
	if m, _ := e.repo.Master(ctx); m != "server" {
		t.Fatalf("master %q", m)
	}
}

func TestSheetRollbackLegacy(t *testing.T) {
	e := newOwnerEnv(t, "server", nil)
	ctx := context.Background()
	e.owner.Begin(ctx)
	defer club.SetSheetMode(club.SheetModeLegacy)()
	ctl := e.signed("/api/v1/bot/control", map[string]any{"version": bot.LatestScript})
	if ctl["sheetMode"] != "legacy" || ctl["reconcile"] != false {
		t.Fatalf("control %v", ctl)
	}
	if x := e.owner.Export(ctx); x.On || x.Why != "legacy" {
		t.Fatalf("export %+v", x)
	}
	if code, _ := e.signedCode("/api/v1/club/reconcile", map[string]any{"sheets": ownerSheets(nil, nil, nil)}); code != http.StatusConflict {
		t.Fatalf("reconcile in legacy: %d", code)
	}
	if code, _ := e.req("POST", "/api/v1/club/import/request", ownerJWT(t, "admin"), nil); code != http.StatusConflict {
		t.Fatalf("request in legacy: %d", code)
	}
	// Back to off: the server owns everything again, without a code change.
	club.SetSheetMode(club.SheetModeOff)
	if ctl := e.signed("/api/v1/bot/control", map[string]any{"version": bot.LatestScript}); ctl["sheetMode"] == "legacy" {
		t.Fatalf("control %v", ctl)
	}
}

// The Go bot owns the webhook: pointed elsewhere, it is taken back.
func TestBotTakesWebhookBack(t *testing.T) {
	prev := bot.WebhookWatchEvery
	bot.WebhookWatchEvery = 50 * time.Millisecond
	t.Cleanup(func() { bot.WebhookWatchEvery = prev })
	e := newOffEnv(t, "server", nil)
	want := "https://srv.example.kz/api/v1/bot/webhook"
	wait := func() {
		for i := 0; i < 200; i++ {
			e.tg.mu.Lock()
			h := e.tg.hook
			e.tg.mu.Unlock()
			if h == want {
				return
			}
			time.Sleep(20 * time.Millisecond)
		}
		t.Fatal("the webhook is not the server's")
	}
	wait()
	e.tg.mu.Lock()
	e.tg.hook = "https://script.google.com/macros/s/OLD/exec" // the old script's menu
	e.tg.mu.Unlock()
	wait()
}

// The script v41 the server ships: sends the reconciliation copy once when
// asked, writes the copy tabs, never moves the webhook while dormant, and
// wakes its triggers on a rollback.
func TestScriptV41(t *testing.T) {
	if v := content.ScriptVersion(); v != bot.LatestScript {
		t.Fatalf("Code.js %s, LatestScript %s", v, bot.LatestScript)
	}
	code, err := os.ReadFile("../../content/script/Code.js")
	if err != nil {
		t.Fatal(err)
	}
	src := string(code)
	for _, want := range []string{
		"reconcile: r.j.reconcile === true",
		"try{ bsReconcileSend(); }catch(e)",
		`if(!ctl || ctl.forced || ctl.master !== "server" || ctl.reconcile !== true) return;`,
		`bsSignedPost("/api/v1/club/reconcile", {sheets: sheets, mirroredDDS: mirrored})`,
		`bsSignedPost("/api/v1/club/export", {tables: true})`,
		"bsWriteCopyTab(ss, t, r.j.at",
		"function bsReconnectBot(){\n  if(bsWebhookLocked()) return;",
		"function setupWebhook(){\n  if(bsWebhookLocked()) return;",
		"function _reinstallWebhook(){\n  if(bsWebhookLocked()) return;",
		"function _reinstallWebhookKeepPending(){\n  if(bsWebhookLocked()) return;",
		"function manualReinstallWebhook(){\n  if(bsWebhookLocked()) return;",
		`if(props.getProperty("BS_DORMANT")){ props.deleteProperty("BS_DORMANT"); try{ bsWakeTriggers(); }`,
	} {
		if !strings.Contains(src, want) {
			t.Fatalf("Code.js lacks %q", want)
		}
	}
}
