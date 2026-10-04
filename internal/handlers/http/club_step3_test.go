package http

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/bnursik/business_surgery_backend/internal/club"
	pg "github.com/bnursik/business_surgery_backend/internal/repository/pg"
	"github.com/bnursik/business_surgery_backend/pkg/auth"
	"github.com/gin-gonic/gin"
)

// Moving the club off the sheet, step 3: the app's own sections (wheels,
// tasks, leads, content, checklists) and the data audit.

// sectionSnap is the sheet with the app's sections, as a v33 import brings it.
func sectionSnap() *club.Snapshot {
	s := sheetSnap()
	s.Raw[club.SheetScriptProps] = [][]string{{"USEFUL_CL_IDX", "0"}}
	s.Raw[club.SheetWheel] = [][]string{{"Дата", "Резидент", "Тип", "В1", "В2", "В3", "В4", "В5", "В6", "В7", "В8", "Бизнес"},
		{"04.07.2026 12:38", "Альтаир", "ДНК", "8", "10", "8", "8", "3", "2", "9", "3", "Основной"}}
	s.Raw[club.SheetResTasks] = [][]string{{"Дата", "Резидент", "Задача", "Статус", "Комментарий", "Обновлено", "Кто создал"}}
	s.Raw[club.SheetLeadmagnets] = [][]string{{"Ключ", "Название", "File ID", "Дата загрузки", "Кто загрузил"},
		{"sales", "Реанимация продаж", "BQAC1", "", ""}, {"unit", "Анатомия бизнеса", "BQAC2", "", ""}}
	s.Raw[club.SheetLMHistory] = [][]string{{"Chat ID", "Ключ", "Дата", "Название"}, {"490685605", "unit", "6/5/2026 17:40:45", "Анатомия бизнеса"}}
	s.Raw[club.SheetContent] = [][]string{{"📢"}, {"№", "Категория", "Текст поста", "Дата отправки", "Статус"}}
	s.Raw[club.SheetProfiles] = [][]string{{"Chat ID", "Ниша", "О себе", "Чем поможет", "Instagram", "Телефон", "Аватар URL"}}
	return s
}

func (e *clubEnv) bundle(t *testing.T, id int64) (map[string]any, *httptest.ResponseRecorder) {
	t.Helper()
	w := get(e.r, url.Values{"action": {"getBotCache"}, "_tg": {makeInitData(testBotToken, id, "Альтаир", *e.now)}})
	var b map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &b); err != nil {
		t.Fatalf("bundle: %s", w.Body.String())
	}
	return b, w
}

func (e *clubEnv) scriptCalls(action string) int {
	e.f.mu.Lock()
	defer e.f.mu.Unlock()
	n := 0
	for _, q := range e.f.calls {
		if q.Get("action") == action {
			n++
		}
	}
	return n
}

func TestAppSectionsFromTheServer(t *testing.T) {
	e := newClubEnv(t)
	ctx := context.Background()
	importSheet(t, e.repo, sectionSnap(), e.now.Add(-time.Minute))

	// The server builds every section; whose checklists differ by person.
	parts, built, err := e.g.ServerBundle(ctx, 490685605)
	if err != nil {
		t.Fatal(err)
	}
	if len(built) != 19 || !strings.Contains(string(parts["leadmagnets"]), `"taken":{"unit":true}`) || string(parts["myAvatar"]) != `""` {
		t.Fatalf("built %v: %s %s", built, parts["leadmagnets"], parts["myAvatar"])
	}
	if parts, _, _ := e.g.ServerBundle(ctx, 999); !strings.Contains(string(parts["leadmagnets"]), `"taken":{}`) {
		t.Fatalf("lead: %s", parts["leadmagnets"])
	}

	// In stage server the app's bundle needs nothing from the script.
	if err := e.mig.SetStage(ctx, StageServer, "test"); err != nil {
		t.Fatal(err)
	}
	b, w := e.bundle(t, 490685605)
	if w.Header().Get("X-BS-Bundle-Source") != "server" || e.scriptCalls("getBotCache") != 0 {
		t.Fatalf("source %s, script asked %d times", w.Header().Get("X-BS-Bundle-Source"), e.scriptCalls("getBotCache"))
	}
	if wa := b["wheelAll"].(map[string]any)["Альтаир"].(map[string]any); len(wa["dna"].([]any)) != 1 {
		t.Fatalf("wheel %v", wa)
	}

	// A resident's section write: kept and applied at once, also with the
	// script down, then sent; the import does not lose it.
	e.g.scriptURL = scriptDown
	r := e.call(490685605, "saveWheel", "name", "Альтаир", "type", "life", "values", "6,7,8,5,6,7,9")
	if r["queued"] != true {
		t.Fatalf("queued: %v", r)
	}
	e.call(490685605, "addOwnTask", "name", "Альтаир", "task", "Нанять бухгалтера")
	if e.statuses(t) != "saveWheel:pending addOwnTask:pending" || e.count(t, `SELECT count(*) FROM club_writes WHERE applied`) != 2 {
		t.Fatalf("writes: %s", e.statuses(t))
	}
	b, _ = e.bundle(t, 490685605)
	wa := b["wheelAll"].(map[string]any)["Альтаир"].(map[string]any)
	tasks := b["resTasks"].(map[string]any)["tasks"].([]any)
	if len(wa["life"].([]any)) != 1 || len(tasks) != 1 || tasks[0].(map[string]any)["isOwn"] != true {
		t.Fatalf("the bundle shows them at once: %v %v", wa, tasks)
	}
	if n := importSheet(t, e.repo, sectionSnap(), e.now.Add(-time.Minute)); n != 2 {
		t.Fatalf("reapplied %d", n)
	}
	b, _ = e.bundle(t, 490685605)
	if len(b["resTasks"].(map[string]any)["tasks"].([]any)) != 1 {
		t.Fatal("an import does not undo waiting section writes")
	}
	e.g.scriptURL = e.srvURL
	*e.now = e.now.Add(time.Minute)
	if n, err := e.writes.Flush(ctx); err != nil || n != 2 {
		t.Fatalf("flush %d %v", n, err)
	}
	if got := strings.Join(e.f.actions(), ","); got != "saveWheel,addOwnTask" {
		t.Fatalf("sent %s", got)
	}

	// The script refuses one: the server's copy is undone.
	e.f.set("deleteOwnTask", "error")
	e.call(490685605, "deleteOwnTask", "row", "2", "name", "Альтаир")
	if rows, _ := e.repo.Sheets(ctx); len(rows[club.SheetResTasks]) != 2 {
		t.Fatalf("undone: %v", rows[club.SheetResTasks])
	}

	// An import from a script without the sections (v32): the server does
	// not keep them, the writes go to the script only.
	importSheet(t, e.repo, sheetSnap(), e.now.Add(time.Minute))
	e.call(490685605, "saveProblem", "name", "Альтаир", "problem", "Аренда", "cost", "100000")
	if e.count(t, `SELECT count(*) FROM club_writes WHERE action = 'saveProblem' AND NOT applied AND apply_error = ''`) != 1 {
		t.Fatal("not applied without the sections")
	}
	if _, built, _ := e.g.ServerBundle(ctx, 1); len(built) != 9 {
		t.Fatalf("without the sections: %v", built)
	}
}

// The status for the platform: who gives each section, the self-update, writes.
func TestMigrationStatusIsRich(t *testing.T) {
	e := newClubEnv(t)
	ctx := context.Background()
	importSheet(t, e.repo, sectionSnap(), e.now.Add(-time.Minute))
	if err := e.mig.SetStage(ctx, StageServer, "test"); err != nil {
		t.Fatal(err)
	}
	e.call(453800951, "addFine", "name", "Альтаир", "amount", "5000")
	st, err := e.mig.Status(ctx)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(st)
	var m map[string]any
	_ = json.Unmarshal(b, &m)
	secs := m["sections"].(map[string]any)
	if m["needDays"] != float64(5) || secs["wheelAll"] != "server" || secs["residents"] != "server" || len(secs) != 19 {
		t.Fatalf("status %s", b)
	}
	wr := m["writes"].(map[string]any)
	sc := m["script"].(map[string]any)
	if _, ok := wr["failed"]; !ok || wr["lastSent"] == nil || sc["latest"] != "2026-10-04-41" {
		t.Fatalf("writes/script %s", b)
	}
	if err := e.mig.SetStage(ctx, StageShadow, "test"); err != nil {
		t.Fatal(err)
	}
	if st, _ := e.mig.Status(ctx); st.SectionsBy["wheelAll"] != "script" {
		t.Fatal("in shadow the script answers")
	}
}

type memDocs struct {
	mu   sync.Mutex
	docs map[string]string
}

func (d *memDocs) PutServerDoc(_ context.Context, key, value string) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.docs == nil {
		d.docs = map[string]string{}
	}
	d.docs[key] = value
	return nil
}

// The data audit on the server: from the journal and the imports, a fix
// through the write-first path.
func TestDataAuditAndFix(t *testing.T) {
	e := newClubEnv(t)
	ctx := context.Background()
	debet := func(granted, done string) [][]string {
		return [][]string{{"", "BS. ДЕБЕТ"},
			{"№", "Имя резидента", "Тариф тг", "Встреч\nоплачено", "Встреч\nпроведено", "Осталось", "Оплачено\n(вход) тг", "Остаток\n(вход) тг",
				"Долг\nпродление тг", "Штрафы тг", "ОБЩИЙ\nДОЛГ тг", "Формат", "Chat ID", "Бывший", "Исключение", "Админ", "Источник", "Дата входа", "Месяцев\nв проекте", "Примечание", "Партнёр"},
			{"", "Рустам", "0", "0", "0", "0", "0", "0", "0", "0", "0", "Онлайн", "453800951", "Нет", "Да", "Да", "", "28.12.2024", "21"},
			{"1", "Асет", "100,000", "3", "1", "2", "0", "50,000", "0", "10,000", "60,000", "Офлайн", "478757502", "Нет", "Нет", "Нет", "", "01.06.2026", "4"},
			{"2", "Альтаир", "100,000", "3", "2", "1", "0", "0", "0", "0", "0", "Офлайн", "490685605", "Нет", "Нет", "Нет", "", "01.06.2026", "4"},
			{"3", "Бакытжан", "500,000", granted, done, "", "0", "250,000", "0", "0", "250,000", "Онлайн", "334435644", "Нет", "Нет", "Нет", "", "29.08.2026", "1"},
			{"4", "Алишер", "500,000", "9", "3", "6", "0", "250,000", "0", "0", "250,000", "Офлайн", "780850710", "Нет", "Нет", "Нет", "", "30.08.2026", "1"},
		}
	}
	imp := func(granted, done string, at time.Time) {
		t.Helper()
		sheets := club.Sheets{club.SheetDebet: debet(granted, done), club.SheetDDS: {{"Дата", "Приход", "Расход", "Источник", "Категория +", "Категория -"}}}
		s, _, err := club.Parse(sheets)
		if err != nil {
			t.Fatal(err)
		}
		if err := e.repo.ReplaceAll(ctx, s, "sheet"); err != nil {
			t.Fatal(err)
		}
		raw, _ := json.Marshal(map[string]any{"ts": at.Unix(), "sheets": sheets})
		if err := e.repo.LogImport(ctx, "sheet", false, raw, map[string]any{}); err != nil {
			t.Fatal(err)
		}
		if _, err := e.db.Pool.Exec(ctx, `UPDATE club_imports SET at = $1 WHERE id = (SELECT max(id) FROM club_imports)`, at); err != nil {
			t.Fatal(err)
		}
	}
	t0 := e.now.Add(-2 * time.Hour)
	imp("9", "3", t0)
	// v31 setMeetings from the app: «проведено» 4, «оплачено» 9 written the other way round
	if err := e.repo.LogOp(ctx, pg.ClubOp{Source: "app", TgID: 453800951, Who: "Рустам", Action: "setMeetings", OK: true,
		Params: map[string]string{"name": "Бакытжан", "done": "4", "granted": "9", "chatId": "453800951"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := e.db.Pool.Exec(ctx, `UPDATE club_ops SET at = $1`, t0.Add(30*time.Minute)); err != nil {
		t.Fatal(err)
	}
	imp("4", "9", t0.Add(time.Hour))

	docs := &memDocs{}
	a := NewClubAudit(e.repo, docs, e.g)
	a.now = func() time.Time { return *e.now }
	rep, err := a.Run(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(rep.Items) != 2 || rep.Items[0].Name != "Бакытжан" || rep.Items[0].Confidence != club.High || len(rep.Items[0].Ops) != 1 ||
		rep.Items[0].Ops[0].Action != "setMeetings" || rep.Checked != 5 {
		t.Fatalf("audit %+v", rep)
	}
	if !strings.Contains(docs.docs[DataAuditKey], `"field":"meetingsGranted","now":4,"suggested":9`) {
		t.Fatalf("doc %s", docs.docs[DataAuditKey])
	}
	if residentReadableKeys[DataAuditKey] {
		t.Fatal("residents must not read the audit")
	}

	// The fix: team only, through setMeetings (v32 order), server first.
	gin.SetMode(gin.TestMode)
	r := gin.New()
	NewClubAuditModule(a, []byte("audit-test-secret")).Register(r)
	tok := func(sub, role string) string {
		acc, _, _ := auth.NewManager("audit-test-secret", time.Hour, time.Hour).GenerateTokens(sub, role, nil)
		return acc
	}
	post := func(tk, path, body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, path, bytes.NewBufferString(body))
		req.Header.Set("Authorization", "Bearer "+tk)
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		return w
	}
	if w := post(tok("tg:490685605", "resident"), "/api/v1/club/audit", ``); w.Code != http.StatusForbidden {
		t.Fatalf("resident: %d", w.Code)
	}
	if w := post(tok("tg:453800951", "admin"), "/api/v1/club/audit/fix", `{"name":"Бакытжан","field":"months","value":"x"}`); w.Code != 400 {
		t.Fatalf("fix months: %d %s", w.Code, w.Body.String())
	}
	w := post(tok("tg:453800951", "admin"), "/api/v1/club/audit/fix", `{"name":"Бакытжан","field":"meetingsGranted","value":9}`)
	if w.Code != 200 {
		t.Fatalf("fix: %d %s", w.Code, w.Body.String())
	}
	q := e.f.calls[len(e.f.calls)-1]
	if q.Get("action") != "setMeetings" || q.Get("granted") != "9" || q.Get("done") != "9" || q.Get("audit") != "1" {
		t.Fatalf("sent %v", q)
	}
	w = post(tok("tg:453800951", "admin"), "/api/v1/club/audit/fix", `{"name":"Бакытжан","field":"meetingsDone","value":4}`)
	if w.Code != 200 || e.count(t, `SELECT count(*) FROM club_residents WHERE name = 'Бакытжан' AND meetings_granted = 9 AND meetings_done = 4`) != 1 {
		t.Fatalf("server tables: %d %s", w.Code, w.Body.String())
	}
	var out struct {
		Audit club.AuditReport `json:"audit"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &out)
	for _, it := range out.Audit.Items {
		if it.Name == "Бакытжан" {
			t.Fatalf("fixed, still listed: %+v", it)
		}
	}
	// A generic field goes as setResidentField; the server applies it too.
	w = post(tok("tg:453800951", "admin"), "/api/v1/club/audit/fix", `{"name":"Альтаир","field":"renewDebt","value":"150000"}`)
	if w.Code != 200 || e.count(t, `SELECT count(*) FROM club_residents WHERE name = 'Альтаир' AND renew_debt = 150000`) != 1 ||
		e.f.calls[len(e.f.calls)-1].Get("action") != "setResidentField" {
		t.Fatalf("setResidentField: %d %s", w.Code, w.Body.String())
	}
	if w := post(tok("tg:453800951", "admin"), "/api/v1/club/audit/fix", `{"name":"Альтаир","field":"isAdmin","value":"1"}`); w.Code != 400 {
		t.Fatalf("unknown field: %d", w.Code)
	}
	// The script refuses: the server's change is undone.
	e.f.set("setResidentField", "error")
	w = post(tok("tg:453800951", "admin"), "/api/v1/club/audit/fix", `{"name":"Альтаир","field":"tariff","value":"200000"}`)
	if w.Code != 422 || e.count(t, `SELECT count(*) FROM club_residents WHERE name = 'Альтаир' AND tariff = 100000`) != 1 {
		t.Fatalf("refused: %d %s", w.Code, w.Body.String())
	}
}
