package http

import (
	"bytes"
	"context"
	"encoding/json"
	"math/rand"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/bnursik/business_surgery_backend/internal/club"
	pg "github.com/bnursik/business_surgery_backend/internal/repository/pg"
	"github.com/bnursik/business_surgery_backend/pkg/auth"
	"github.com/gin-gonic/gin"
)

// R32a: ДДС as a spreadsheet in «Учёт». The journal is read and edited by
// id; the P&L the page builds from it must be the P&L the server gives
// (club.BuildPL → bs_seed PL_ROWS), so the page's numbers stay the sheet's.

var ddsIncomeRows = []string{"БХ Трекинг продление", "БХ Трекинг год", "БХ Штраф", "БХ Экспресс разбор", "БХ МК/Завтрак", "Прочие доходы"}
var ddsExpenseRows = []string{"SMM", "Маркет.бюджет", "Таргетолог", "Комиссия+налог", "Tilda + домен", "CRM", "Canva", "Съемки проф",
	"Съемки доп", "HH объявление", "Симка тариф", "Оборудование", "Офис", "Ассистент", "Бухгалтер", "Юрист", "IT", "Прочие расходы:"}

// ddsStructure is the «PL» sheet's lines, as an import brings them.
func ddsStructure(year int) *club.PLSheet {
	s := &club.PLSheet{Year: year}
	for _, n := range ddsIncomeRows {
		s.Rows = append(s.Rows, club.PLRow{Name: n, Section: "income"})
	}
	s.Rows = append(s.Rows, club.PLRow{Name: "ИТОГО ДОХОДЫ", Section: "total_income"})
	for _, n := range ddsExpenseRows {
		s.Rows = append(s.Rows, club.PLRow{Name: n, Section: "expense"})
	}
	s.Rows = append(s.Rows, club.PLRow{Name: "ИТОГО РАСХОДЫ", Section: "total_expense"}, club.PLRow{Name: "ЧИСТАЯ ПРИБЫЛЬ", Section: "profit"},
		club.PLRow{Name: "Дивиденды", Section: "dividends"}, club.PLRow{Name: "На кассе", Section: "cash"})
	return s
}

// DDSFixture: a realistic journal of n rows over the last year and this
// one: residents' payments with the bank's 4% on the same row, expenses by
// article, dividends, articles the P&L does not have (they go to «Прочие»),
// spelling variants («Комиссия + налог»), expenses without an article.
func DDSFixture(n int, now time.Time) []club.Payment {
	rnd := rand.New(rand.NewSource(32))
	residents := []string{"Асет", "Альтаир", "Елена", "Рамиль", "Дамил", "Олжас", "Гульназ бухгалтерия", "Мади Актобе", "Азамат TV", "Даулет Сайты"}
	extraExp := []string{"Аренда:", "Бонус резидента", "Комиссия + налог", "офис", "Дивиденды", "Дивиденды Рустам", ""}
	y := now.Year()
	start := time.Date(y-1, 10, 1, 0, 0, 0, 0, club.Almaty)
	end := time.Date(y, now.Month(), 28, 0, 0, 0, 0, club.Almaty)
	days := int(end.Sub(start).Hours()/24) + 1
	out := make([]club.Payment, 0, n)
	for i := 0; i < n; i++ {
		d := start.AddDate(0, 0, rnd.Intn(days))
		p := club.Payment{Row: i + 2, Date: d}
		switch k := rnd.Intn(100); {
		case k < 38: // income from a resident
			p.Income = int64(5+rnd.Intn(20)) * 10000
			cats := []string{"БХ Трекинг продление", "БХ Трекинг продление", "БХ Трекинг год", "БХ Штраф", "БХ Экспресс разбор", "БХ МК/Завтрак", "Консультация", "бх трекинг продление "}
			p.IncomeCat = cats[rnd.Intn(len(cats))]
			p.Resident = residents[rnd.Intn(len(residents))]
			if rnd.Intn(3) > 0 { // Kaspi: commission and tax on the same row
				p.Expense = (p.Income*4 + 50) / 100
				p.ExpenseCat = "Комиссия+налог"
			}
		case k < 90:
			p.Expense = int64(1+rnd.Intn(120)) * 1500
			p.ExpenseCat = ddsExpenseRows[rnd.Intn(len(ddsExpenseRows))]
		default:
			p.Expense = int64(1+rnd.Intn(60)) * 5000
			p.ExpenseCat = extraExp[rnd.Intn(len(extraExp))]
		}
		out = append(out, p)
	}
	return out
}

func ddsEnv(t *testing.T) (*pg.ClubRepo, *pg.DB, func(tk, method, path string, body any) (*httptest.ResponseRecorder, map[string]any), string) {
	db, repo := clubTestDB(t)
	ctx := context.Background()
	t.Cleanup(func() {
		_, _ = db.Pool.Exec(context.Background(), `UPDATE club_meta SET value = 'sheet' WHERE key = 'master'`)
	})
	_, _ = db.Pool.Exec(ctx, `UPDATE club_meta SET value = 'sheet' WHERE key = 'master'`)
	gin.SetMode(gin.TestMode)
	r := gin.New()
	NewClubModule(NewClubHandler(repo, nil, testBotToken, "dds-secret")).Register(r)
	acc, _, _ := auth.NewManager("dds-secret", time.Hour, time.Hour).GenerateTokens("tg:453800951", "admin", nil)
	do := func(tk, method, path string, body any) (*httptest.ResponseRecorder, map[string]any) {
		var buf bytes.Buffer
		if body != nil {
			_ = json.NewEncoder(&buf).Encode(body)
		}
		req := httptest.NewRequest(method, path, &buf)
		req.Header.Set("Authorization", "Bearer "+tk)
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		var j map[string]any
		_ = json.Unmarshal(w.Body.Bytes(), &j)
		return w, j
	}
	return repo, db, do, acc
}

func TestDDSGridAndPLParity(t *testing.T) {
	repo, db, do, admin := ddsEnv(t)
	defer club.SetSheetMode(club.SheetModeOff)() // after the cutover: the server owns ДДС
	ctx := context.Background()
	now := club.Today()
	pays := DDSFixture(2400, now)
	if path := os.Getenv("BS_DDS_JSON"); path != "" { // a real export, never in the repository
		var real struct {
			Payments []club.Payment `json:"payments"`
		}
		if raw, err := os.ReadFile(path); err == nil && json.Unmarshal(raw, &real) == nil {
			for i := range real.Payments {
				real.Payments[i].Row += 100000
			}
			pays = append(pays, real.Payments...)
		}
	}
	snap := sheetSnap()
	snap.Payments = pays
	snap.PL = ddsStructure(now.Year())
	if err := repo.ReplaceAll(ctx, snap, "sheet"); err != nil {
		t.Fatal(err)
	}

	// While the sheet is the master, the grid only reads
	w, j := do(admin, http.MethodGet, "/api/v1/club/dds", nil)
	if w.Code != 200 || j["editable"] != false || len(j["rows"].([]any)) != len(pays) {
		t.Fatalf("get (sheet): %d editable=%v rows=%d", w.Code, j["editable"], len(j["rows"].([]any)))
	}
	if w, _ := do(admin, http.MethodPost, "/api/v1/club/dds", map[string]any{"ops": []any{map[string]any{"op": "insert", "cid": "x", "set": map[string]any{"income": 1}}}}); w.Code != http.StatusConflict {
		t.Fatalf("sheet master must refuse edits: %d", w.Code)
	}
	if _, err := db.Pool.Exec(ctx, `UPDATE club_meta SET value = 'server' WHERE key = 'master'`); err != nil {
		t.Fatal(err)
	}
	w, j = do(admin, http.MethodGet, "/api/v1/club/dds", nil)
	pl := j["pl"].(map[string]any)
	if w.Code != 200 || j["editable"] != true || len(pl["incomeRows"].([]any)) != len(ddsIncomeRows) || len(pl["expenseRows"].([]any)) != len(ddsExpenseRows) ||
		int(pl["cashStart"].(float64)) != club.CashStartMonth || len(j["residents"].([]any)) != 2 {
		t.Fatalf("get (server): %d %v %v", w.Code, j["editable"], pl)
	}
	// Legacy rollback (SHEET_MODE=legacy): the sheet is the master again, the grid only reads
	func() {
		defer club.SetSheetMode(club.SheetModeLegacy)()
		w, jl := do(admin, http.MethodGet, "/api/v1/club/dds", nil)
		if w.Code != 200 || jl["editable"] != false || jl["master"] != "sheet" || jl["mode"] != "legacy" {
			t.Fatalf("get (legacy): %d editable=%v master=%v mode=%v", w.Code, jl["editable"], jl["master"], jl["mode"])
		}
		if w, _ := do(admin, http.MethodPost, "/api/v1/club/dds", map[string]any{"ops": []any{map[string]any{"op": "insert", "cid": "x", "set": map[string]any{"income": 1}}}}); w.Code != http.StatusConflict {
			t.Fatalf("legacy must refuse edits: %d", w.Code)
		}
	}()

	// What the platform showed before (bs_seed PL_ROWS from club.LiveSeed): the
	// page's P&L from these rows has to give the same numbers (pw_r32a.js, part 5)
	seedOf := func() map[string]json.RawMessage {
		s, err := repo.Load(ctx)
		if err != nil {
			t.Fatal(err)
		}
		raw, err := club.LiveSeed(s, "{}", time.Now())
		if err != nil {
			t.Fatal(err)
		}
		var m map[string]json.RawMessage
		_ = json.Unmarshal([]byte(raw), &m)
		return m
	}
	if out := os.Getenv("R32A_OUT"); out != "" {
		seed := seedOf()
		b, _ := json.Marshal(map[string]any{"dds": j, "seed": map[string]json.RawMessage{"PL_ROWS": seed["PL_ROWS"], "PL_MONTHS": seed["PL_MONTHS"], "PL_YEAR": seed["PL_YEAR"], "RESIDENTS": seed["RESIDENTS"]}})
		if err := os.WriteFile(out, b, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	plNow := func() *club.PL {
		s, _ := repo.Load(ctx)
		return club.BuildPL(s.Payments, s.PL, now.Year(), int(now.Month()))
	}
	before := plNow()
	m := int(now.Month()) - 1
	rows := j["rows"].([]any)
	var victim, edited map[string]any
	for _, x := range rows {
		r := x.(map[string]any)
		if r["date"].(string)[:7] == now.Format("2006-01") && r["expenseCat"] == "Офис" && r["income"].(float64) == 0 {
			if victim == nil {
				victim = r
			} else if edited == nil {
				edited = r
			}
		}
	}
	if victim == nil || edited == nil {
		t.Fatal("fixture: no office expenses this month")
	}
	date := now.Format("02.01.2006")
	ops := []map[string]any{
		{"op": "insert", "cid": "c1", "set": map[string]any{"date": date, "income": "1 500 000", "incomeCat": "БХ Трекинг год", "resident": "Асет", "account": "Kaspi", "comment": "годовой"}},
		{"op": "insert", "cid": "c2", "set": map[string]any{"date": now.Format("02.01"), "expense": 70000.4, "expenseCat": "Юрист"}},
		{"op": "update", "id": edited["id"], "set": map[string]any{"expenseCat": "IT", "comment": "перенёс  статью"}},
		{"op": "delete", "id": victim["id"]},
	}
	w, j = do(admin, http.MethodPost, "/api/v1/club/dds", map[string]any{"ops": ops})
	if w.Code != 200 || j["ok"] != true {
		t.Fatalf("save: %d %s", w.Code, w.Body.String())
	}
	ids := j["ids"].(map[string]any)
	got := j["rows"].([]any)
	if ids["c1"] == nil || ids["c2"] == nil || len(got) != 3 || len(j["deleted"].([]any)) != 1 {
		t.Fatalf("save answer: %s", w.Body.String())
	}
	r1 := got[0].(map[string]any)
	if r1["income"].(float64) != 1500000 || r1["date"] != now.Format("2006-01-02") || r1["account"] != "Kaspi" || r1["comment"] != "годовой" || r1["updatedBy"] != "tg:453800951" {
		t.Fatalf("inserted: %v", r1)
	}
	if r2 := got[1].(map[string]any); r2["expense"].(float64) != 70000 || r2["date"] != now.Format("2006-01-02") {
		t.Fatalf("inserted 2: %v", r2)
	}
	if r3 := got[2].(map[string]any); r3["expenseCat"] != "IT" || r3["comment"] != "перенёс статью" {
		t.Fatalf("updated: %v", r3)
	}
	after := plNow()
	b, a := before.Months[m], after.Months[m]
	ve, ee := victim["expense"].(float64), edited["expense"].(float64)
	if a.Income["БХ Трекинг год"]-b.Income["БХ Трекинг год"] != 1500000 || a.Expense["Юрист"]-b.Expense["Юрист"] != 70000 ||
		b.Expense["Офис"]-a.Expense["Офис"] != int64(ve+ee) || a.Expense["IT"]-b.Expense["IT"] != int64(ee) ||
		a.Profit-b.Profit != 1500000-70000+int64(ve) {
		t.Fatalf("P&L after edits: before %+v after %+v", b, a)
	}

	// A bad cell stops the whole batch: nothing of it is saved
	n0, _ := repo.DDSRows(ctx)
	w, j = do(admin, http.MethodPost, "/api/v1/club/dds", map[string]any{"ops": []any{
		map[string]any{"op": "insert", "cid": "ok", "set": map[string]any{"date": date, "expense": 1000, "expenseCat": "IT"}},
		map[string]any{"op": "update", "id": edited["id"], "set": map[string]any{"date": "31.02.2026"}}}})
	n1, _ := repo.DDSRows(ctx)
	if w.Code != 400 || len(n1) != len(n0) || j["detail"] == nil {
		t.Fatalf("bad batch: %d %v, rows %d → %d", w.Code, j, len(n0), len(n1))
	}
	for _, bad := range []map[string]any{{"income": -5}, {"expense": "abc"}, {"nope": "x"}} {
		if w, _ := do(admin, http.MethodPost, "/api/v1/club/dds", map[string]any{"ops": []any{map[string]any{"op": "update", "id": edited["id"], "set": bad}}}); w.Code != 400 {
			t.Fatalf("bad %v accepted: %d", bad, w.Code)
		}
	}
	// Undo of a delete puts the row back as a new one; deleting twice is fine
	if w, _ := do(admin, http.MethodPost, "/api/v1/club/dds", map[string]any{"ops": []any{map[string]any{"op": "delete", "id": victim["id"]}}}); w.Code != 200 {
		t.Fatalf("delete again: %d", w.Code)
	}
	if w, _ := do(admin, http.MethodPost, "/api/v1/club/dds", map[string]any{"ops": []any{map[string]any{"op": "update", "id": victim["id"], "set": map[string]any{"comment": "x"}}}}); w.Code != 400 {
		t.Fatalf("update of a deleted row: %d", w.Code)
	}
	// Only the team
	resTok, _, _ := auth.NewManager("dds-secret", time.Hour, time.Hour).GenerateTokens("tg:490685605", "resident", nil)
	if w, _ := do(resTok, http.MethodGet, "/api/v1/club/dds", nil); w.Code != http.StatusForbidden {
		t.Fatalf("resident: %d", w.Code)
	}
	// History of changes
	opsLog, _ := repo.Ops(ctx, 10)
	found := false
	for _, o := range opsLog {
		if o.Action == "ddsEdit" && o.Params["added"] == "2" && o.Params["changed"] == "1" && o.Params["deleted"] == "1" {
			found = true
		}
	}
	if !found {
		t.Fatalf("journal: %+v", opsLog)
	}
	// The export to the sheet's copy sees the edits too
	s, _ := repo.Load(ctx)
	if len(s.Payments) != len(pays)+1 { // +2 added, −1 deleted
		t.Fatalf("load: %d rows, want %d", len(s.Payments), len(pays)+1)
	}
}

func TestDDSParse(t *testing.T) {
	now := time.Date(2026, 10, 3, 12, 0, 0, 0, club.Almaty)
	for in, want := range map[string]string{"30.09.2026": "2026-09-30", "30.09.26": "2026-09-30", "30.09": "2026-09-30", "2026-09-30": "2026-09-30",
		"30/09/2026": "2026-09-30", "1.2.2026": "2026-02-01", "2026-09-30T00:00:00+05:00": "2026-09-30"} {
		d, ok := pg.ParseDDSDate(in, now)
		if !ok || d.Format("2006-01-02") != want {
			t.Fatalf("%s → %v %v", in, d, ok)
		}
	}
	for _, bad := range []string{"", "31.02.2026", "13.13.2026", "abc", "1.1.1999"} {
		if _, ok := pg.ParseDDSDate(bad, now); ok {
			t.Fatalf("%q accepted", bad)
		}
	}
	for in, want := range map[string]int64{"100 000": 100000, "100000": 100000, "1 500,50": 1501, "100 000 ₸": 100000, "": 0, "1 000": 1000} {
		if v, ok := pg.ParseDDSMoney(in); !ok || v != want {
			t.Fatalf("%q → %d %v", in, v, ok)
		}
	}
	if _, ok := pg.ParseDDSMoney("-5"); ok {
		t.Fatal("negative accepted")
	}
}
