package http

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/bnursik/business_surgery_backend/internal/club"
	pg "github.com/bnursik/business_surgery_backend/internal/repository/pg"
	"github.com/bnursik/business_surgery_backend/pkg/auth"
	"github.com/gin-gonic/gin"
)

// R75: referral bonuses («Выплачено» → ДДС), «Перенести оплату», «Особая
// цена» and the owner's two events of 10.10 applied at start.

// fakeLedger: the cash journal in memory, as DDSApply writes it.
type fakeLedger struct {
	mu     sync.Mutex
	master string
	rows   []pg.DDSRow
	next   int64
	calls  int
}

func (l *fakeLedger) Master(ctx context.Context) (string, error) { return l.master, nil }

func (l *fakeLedger) DDSRows(ctx context.Context) ([]pg.DDSRow, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]pg.DDSRow{}, l.rows...), nil
}

func (l *fakeLedger) DDSApply(ctx context.Context, ops []pg.DDSOp, who string, now time.Time) (*pg.DDSResult, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.calls++
	res := &pg.DDSResult{IDs: map[string]int64{}}
	str := func(raw json.RawMessage) string { var s string; _ = json.Unmarshal(raw, &s); return s }
	num := func(raw json.RawMessage) int64 { var n int64; _ = json.Unmarshal(raw, &n); return n }
	apply := func(r *pg.DDSRow, set map[string]json.RawMessage) {
		for k, v := range set {
			switch k {
			case "date":
				t, ok := pg.ParseDDSDate(str(v), now)
				if !ok {
					panic("date " + str(v))
				}
				r.Date = t.Format("2006-01-02")
			case "income":
				r.Income = num(v)
			case "expense":
				r.Expense = num(v)
			case "incomeCat":
				r.IncomeCat = str(v)
			case "expenseCat":
				r.ExpenseCat = str(v)
			case "comment":
				r.Comment = str(v)
			case "account":
				r.Account = str(v)
			case "resident":
				r.Resident = str(v)
			}
		}
		r.UpdatedBy = who
	}
	for _, op := range ops {
		switch op.Op {
		case "insert":
			if l.next == 0 {
				l.next = 1000
			}
			l.next++
			r := pg.DDSRow{ID: l.next, Source: "platform"}
			apply(&r, op.Set)
			l.rows = append(l.rows, r)
			res.IDs[op.CID] = r.ID
		case "update":
			for i := range l.rows {
				if l.rows[i].ID == op.ID {
					apply(&l.rows[i], op.Set)
				}
			}
		}
	}
	return res, nil
}

func (l *fakeLedger) row(id int64) pg.DDSRow {
	l.mu.Lock()
	defer l.mu.Unlock()
	for _, r := range l.rows {
		if r.ID == id {
			return r
		}
	}
	return pg.DDSRow{}
}

type fakeMeta struct{ m map[string]string }

func (f *fakeMeta) Meta(ctx context.Context, k string) string { return f.m[k] }
func (f *fakeMeta) MetaSet(ctx context.Context, k, v string) error {
	if f.m == nil {
		f.m = map[string]string{}
	}
	f.m[k] = v
	return nil
}

// 10.10.2026 12:00 Almaty
var r75Now = time.Date(2026, 10, 10, 12, 0, 0, 0, club.Almaty)

func r75Book(t *testing.T) (*SalesBook, *salesFakeDocs, *fakeLedger, *salesTG) {
	t.Helper()
	t.Cleanup(club.SetSheetMode(club.SheetModeOff)) // after the cutover: the server owns ДДС
	docs := &salesFakeDocs{}
	led := &fakeLedger{master: "server", rows: []pg.DDSRow{
		{ID: 355, Date: "2026-10-06", Income: 10000, IncomeCat: "БХ МК/Завтрак", Source: "app"},
		{ID: 353, Date: "2026-10-06", Income: 50000, IncomeCat: "БХ Экспресс разбор", Resident: "Рустам"},
	}}
	tg := &salesTG{}
	now := r75Now
	b := &SalesBook{Docs: docs, Ledger: led, Send: tg.send, Admins: []int64{1}, Now: func() time.Time { return now },
		Residents: func(ctx context.Context) ([]club.Resident, error) {
			return []club.Resident{{Name: "Асет", TgID: 11}, {Name: "Амирхан", TgID: 22}}, nil
		}}
	return b, docs, led, tg
}

func TestR75PayoutFlowWritesExpenseOnce(t *testing.T) {
	b, docs, led, _ := r75Book(t)
	ctx := context.Background()
	docs.put(t, "server", salesSettingsKey, map[string]any{"refBonus": 120000})

	p, created, err := EnsurePayout(ctx, docs, RefPayout{Key: "k1", Resident: "Амирхан"}, r75Now)
	if err != nil || !created || p.Amount != 120000 || p.Status != "due" || p.Referrer != "" {
		t.Fatalf("payout: %+v created=%v err=%v", p, created, err)
	}
	if _, again, _ := EnsurePayout(ctx, docs, RefPayout{Key: "other", Resident: "амирхан "}, r75Now); again {
		t.Fatal("the same resident must not get a second bonus")
	}
	// «Выплачено» without a referrer: refused, nothing in ДДС
	if _, err := b.PayPayout(ctx, p.ID, "", "", "tg:1"); err == nil || !strings.Contains(err.Error(), "кто привёл") {
		t.Fatalf("pay without referrer: %v", err)
	}
	if led.calls != 0 {
		t.Fatal("ДДС written without a referrer")
	}
	// «Кто привёл?» on the resident card fills the open payout
	if err := b.SetWho(ctx, "Амирхан", "Асет"); err != nil {
		t.Fatal(err)
	}
	if who, got := b.Who(ctx, "Амирхан"); who != "Асет" || got == nil || got.Referrer != "Асет" {
		t.Fatalf("who: %q %+v", who, got)
	}
	paid, err := b.PayPayout(ctx, p.ID, "10.10.2026", "Kaspi", "tg:1")
	if err != nil || paid.Status != "paid" || paid.DDSID == 0 {
		t.Fatalf("paid: %+v %v", paid, err)
	}
	r := led.row(paid.DDSID)
	if r.Expense != 120000 || r.ExpenseCat != RefBonusCat || r.Date != "2026-10-10" || r.Account != "Kaspi" || !strings.Contains(r.Comment, "Асет за Амирхан") {
		t.Fatalf("ДДС row: %+v", r)
	}
	// a second click: no second expense
	if _, err := b.PayPayout(ctx, p.ID, "", "", "tg:1"); err != nil || led.calls != 1 {
		t.Fatalf("second pay: err=%v calls=%d", err, led.calls)
	}
	// the sheet keeps ДДС: «Выплачено» refused
	led.master = "sheet"
	p2, _, _ := EnsurePayout(ctx, docs, RefPayout{Key: "k2", Resident: "Новый", Referrer: "Асет"}, r75Now)
	if _, err := b.PayPayout(ctx, p2.ID, "", "", "tg:1"); err == nil {
		t.Fatal("must refuse while ДДС is read-only")
	}
}

func TestR75PayoutReminderOnce(t *testing.T) {
	b, docs, _, tg := r75Book(t)
	ctx := context.Background()
	if _, _, err := EnsurePayout(ctx, docs, RefPayout{Key: "k", Resident: "Амирхан", RemindAt: "2026-10-10"}, r75Now); err != nil {
		t.Fatal(err)
	}
	if _, _, err := EnsurePayout(ctx, docs, RefPayout{Key: "later", Resident: "Другой"}, r75Now); err != nil { // reminds in 2 days
		t.Fatal(err)
	}
	if n := b.RemindOnce(ctx); n != 1 || len(tg.sent) != 1 || !strings.Contains(tg.sent[0].Text, "Кто привёл?") || !strings.Contains(tg.sent[0].Text, "100 000 ₸") {
		t.Fatalf("remind: %d %+v", n, tg.sent)
	}
	if n := b.RemindOnce(ctx); n != 0 {
		t.Fatal("reminded twice")
	}
	night := r75Now.Add(12 * time.Hour)
	b.Now = func() time.Time { return night.Add(48 * time.Hour) }
	if n := b.RemindOnce(ctx); n != 0 {
		t.Fatal("no reminders at night")
	}
}

func r75Lead(id string, extra map[string]any) map[string]any {
	l := map[string]any{"id": id, "col": "qual", "name": "Тест Лид"}
	for k, v := range extra {
		l[k] = v
	}
	return l
}

func TestR75MoveAndSpecialPrice(t *testing.T) {
	b, docs, led, _ := r75Book(t)
	ctx := context.Background()
	docs.put(t, "club", "bs_crm", map[string]any{"leads": []any{r75Lead("L1", map[string]any{"razborSlot": "s1",
		"payments": []any{map[string]any{"id": "p1", "product": "breakfast", "amount": 10000, "date": "06.10.2026", "ddsId": 355, "status": "paid"}}})}})
	docs.put(t, "club", slotsDoc, map[string]any{"slots": []any{map[string]any{"id": "s1", "start": "2026-10-14T11:00:00+05:00", "status": "booked", "booking": map[string]any{"tgId": 5, "name": "Тест"}}}})

	if _, err := b.Move(ctx, "L1", "p1", "breakfast", "", "tg:1"); err == nil {
		t.Fatal("moving to the same product must be refused")
	}
	l, err := b.Move(ctx, "L1", "p1", "razbor", "завтрак не собрали", "tg:1")
	if err != nil {
		t.Fatal(err)
	}
	p := leadPay(l, "p1")
	if p["product"] != "razbor" || p["movedFrom"] != "breakfast" {
		t.Fatalf("moved: %v", p)
	}
	if r := led.row(355); r.IncomeCat != "БХ Экспресс разбор" || !strings.Contains(r.Comment, "перенесено с «БХ МК/Завтрак»: завтрак не собрали") || r.Income != 10000 {
		t.Fatalf("ДДС row after move: %+v", r)
	}
	if _, err := b.Special(ctx, "L1", "razbor", 10000, "", "tg:1"); err == nil {
		t.Fatal("a special price needs a reason")
	}
	l, err = b.Special(ctx, "L1", "razbor", 10000, "спецусловия", "tg:1")
	if err != nil {
		t.Fatal(err)
	}
	ds := LeadSummary(l)
	if len(ds) != 1 || ds[0].Price != 50000 || ds[0].Special != 10000 || ds[0].Paid != 10000 || ds[0].Due != 0 || ds[0].Price2Go != 10000 {
		t.Fatalf("summary: %+v", ds)
	}
	// the booking carries the special price: the lead's app shows it
	slots := docs.get("club", slotsDoc)
	bk := slots["slots"].([]any)[0].(map[string]any)["booking"].(map[string]any)
	if bookingPrice(bk, 50000) != 10000 {
		t.Fatalf("booking: %v", bk)
	}
	// another payment of the lead, with its ДДС income row
	l, err = b.Pay(ctx, "L1", "", "razbor", 5000, "10.10.2026", "предоплата", "tg:1")
	if err != nil {
		t.Fatal(err)
	}
	ds = LeadSummary(l)
	if ds[0].Paid != 15000 || ds[0].Over != 5000 {
		t.Fatalf("summary after pay: %+v", ds)
	}
	var added pg.DDSRow
	for _, r := range led.rows {
		if r.ID > 1000 {
			added = r
		}
	}
	if added.Income != 5000 || added.IncomeCat != "БХ Экспресс разбор" || added.Date != "2026-10-10" {
		t.Fatalf("income row: %+v", added)
	}
	// removing the special price: the list price again
	l, _ = b.Special(ctx, "L1", "razbor", 0, "", "tg:1")
	if ds = LeadSummary(l); ds[0].Special != 0 || ds[0].Due != 35000 {
		t.Fatalf("summary without special: %+v", ds)
	}
}

func TestR75StartupOpsPlaceholderLead(t *testing.T) {
	b, docs, led, _ := r75Book(t)
	ctx := context.Background()
	// Амирхан's lead came by a resident's link
	docs.put(t, "club", "bs_crm", map[string]any{"leads": []any{
		map[string]any{"id": "tg22", "tgId": 22, "col": "decide", "name": "Амирхан", "ref": 11, "refName": "Асет"},
		map[string]any{"id": "x1", "col": "new", "name": "Другой", "source": "Threads"},
	}})
	meta := &fakeMeta{}
	SalesOpsAtStart(ctx, b, meta)

	items := b.Payouts(ctx)
	if len(items) != 1 || items[0].Referrer != "Асет" || items[0].Resident != "Амирхан" || items[0].Status != "due" || items[0].LeadID != "tg22" {
		t.Fatalf("payouts: %+v", items)
	}
	crm := docs.get("club", "bs_crm")
	am := leadByID(crm, "tg22")
	if am["col"] != "won" || am["next"] != "Выплатить реферальный бонус" {
		t.Fatalf("Амирхан's lead: %v", am)
	}
	// breakfast: no lead from the target mentions it: a card the team fills
	l := leadByID(crm, R75LeadID)
	if l == nil || l["col"] != "prepay" {
		t.Fatalf("placeholder lead: %v", l)
	}
	if r := led.row(355); r.IncomeCat != "БХ Экспресс разбор" || r.Income != 10000 {
		t.Fatalf("ДДС #355: %+v", r)
	}
	ds := LeadSummary(l)
	if len(ds) != 1 || ds[0].Special != 10000 || ds[0].Paid != 10000 || ds[0].Unknown != 1 {
		t.Fatalf("deal: %+v", ds)
	}
	if pp := leadPay(l, R75PayPrepay); pp == nil || pp["status"] != "placeholder" {
		t.Fatalf("prepayment placeholder: %v", pp)
	}
	if meta.m[r75Mark] == "" {
		t.Fatal("the case is not marked done")
	}
	// a restart changes nothing
	calls := led.calls
	before := docs.get("club", "bs_crm")
	SalesOpsAtStart(ctx, b, meta)
	after := docs.get("club", "bs_crm")
	bj, _ := json.Marshal(before)
	aj, _ := json.Marshal(after)
	if led.calls != calls || string(bj) != string(aj) || len(b.Payouts(ctx)) != 1 {
		t.Fatal("the start step is not idempotent")
	}
	// the owner fills the prepayment: «Указать сумму» → ДДС income, the placeholder is paid
	l, err := b.Pay(ctx, R75LeadID, R75PayPrepay, "razbor", 20000, "09.10.2026", "", "tg:1")
	if err != nil {
		t.Fatal(err)
	}
	if pp := leadPay(l, R75PayPrepay); pp["status"] != "paid" || anyInt(pp["amount"]) != 20000 || anyInt(pp["ddsId"]) == 0 {
		t.Fatalf("filled: %v", pp)
	}
	if _, err := b.Pay(ctx, R75LeadID, R75PayPrepay, "razbor", 20000, "", "", "tg:1"); err == nil {
		t.Fatal("a filled payment cannot be filled twice")
	}
}

func TestR75StartupOpsFindsTargetLeadAndUnknownReferrer(t *testing.T) {
	b, docs, _, _ := r75Book(t)
	ctx := context.Background()
	docs.put(t, "club", "bs_crm", map[string]any{"leads": []any{
		map[string]any{"id": "t1", "col": "new", "name": "Ерлан", "source": "Сайт (Tilda): таргет", "note": "хочет на бизнес-завтрак"},
		map[string]any{"id": "e1", "col": "new", "name": "Бот", "source": "Мероприятие: Бизнес-завтрак Business Surgery"},
	}})
	SalesOpsAtStart(ctx, b, &fakeMeta{})
	crm := docs.get("club", "bs_crm")
	if leadByID(crm, R75LeadID) != nil {
		t.Fatal("the target lead exists: no new card")
	}
	l := leadByID(crm, "t1")
	if l["col"] != "prepay" || leadPay(l, R75PayMoved) == nil || leadPay(l, R75PayPrepay) == nil {
		t.Fatalf("target lead: %v", l)
	}
	if leadByID(crm, "e1")["col"] != "new" {
		t.Fatal("the bot's breakfast lead must not be touched")
	}
	// Амирхан has no lead: the payout waits for «Кто привёл?»
	items := b.Payouts(ctx)
	if len(items) != 1 || items[0].Referrer != "" || items[0].Status != "due" {
		t.Fatalf("payouts: %+v", items)
	}
	// the referral loop later learns the referrer: the same record gets it, no second bonus
	if _, created, _ := EnsurePayout(ctx, docs, RefPayout{Key: "lead:x", Resident: "Амирхан", Referrer: "Асет"}, r75Now); created {
		t.Fatal("second bonus")
	}
	if b.Payouts(ctx)[0].Referrer != "Асет" {
		t.Fatal("the referrer was not filled")
	}
}

func TestR75HTTP(t *testing.T) {
	b, docs, led, _ := r75Book(t)
	docs.put(t, "club", "bs_crm", map[string]any{"leads": []any{r75Lead("L1", map[string]any{"col": "won", "ref": 11, "refName": "Асет",
		"payments": []any{map[string]any{"id": "p1", "product": "breakfast", "amount": 10000, "ddsId": 355, "status": "paid"}}})}})
	gin.SetMode(gin.TestMode)
	r := gin.New()
	h := NewClubHandler(nil, nil, testBotToken, "r75-secret")
	h.Book = b
	NewClubModule(h).Register(r)
	tk, _, _ := auth.NewManager("r75-secret", time.Hour, time.Hour).GenerateTokens("tg:1", "admin", nil)
	do := func(method, path string, body any) (int, map[string]any) {
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
		return w.Code, j
	}
	if code, _ := do(http.MethodGet, "/api/v1/club/referral/payouts", nil); code != 200 {
		t.Fatalf("payouts: %d", code)
	}
	// without a token: no access
	req := httptest.NewRequest(http.MethodGet, "/api/v1/club/referral/payouts", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("no token: %d", w.Code)
	}
	code, j := do(http.MethodPost, "/api/v1/club/referral/pay-lead", map[string]any{"leadId": "L1"})
	if code != 200 || j["item"].(map[string]any)["status"] != "paid" {
		t.Fatalf("pay-lead: %d %v", code, j)
	}
	if l := leadByID(docs.get("club", "bs_crm"), "L1"); l["refPaid"] != true {
		t.Fatalf("lead not marked paid: %v", l)
	}
	code, j = do(http.MethodPost, "/api/v1/club/leads/L1/payments/move", map[string]any{"payId": "p1", "to": "razbor"})
	if code != 200 || led.row(355).IncomeCat != "БХ Экспресс разбор" {
		t.Fatalf("move: %d %v", code, j)
	}
	code, j = do(http.MethodPost, "/api/v1/club/leads/L1/special-price", map[string]any{"product": "razbor", "price": "10 000", "reason": "особый случай"})
	if code != 200 {
		t.Fatalf("special: %d %v", code, j)
	}
	if code, j = do(http.MethodPost, "/api/v1/club/leads/L1/payments", map[string]any{"product": "razbor", "amount": "abc"}); code != 400 {
		t.Fatalf("bad amount: %d %v", code, j)
	}
	if code, j = do(http.MethodPost, "/api/v1/club/leads/NOPE/payments/move", map[string]any{"payId": "p1", "to": "razbor"}); code != 400 {
		t.Fatalf("unknown lead: %d %v", code, j)
	}
	code, j = do(http.MethodGet, "/api/v1/club/leads/L1/payments", nil)
	if code != 200 || len(j["products"].([]any)) != len(LeadProducts) {
		t.Fatalf("lead payments: %d %v", code, j)
	}
	code, j = do(http.MethodPost, "/api/v1/club/referral/who", map[string]any{"resident": "Амирхан", "referrer": "Асет"})
	if code != 200 || j["referrer"] != "Асет" {
		t.Fatalf("who: %d %v", code, j)
	}
}

// With the real journal: «Выплачено» is an expense row, «Перенести оплату»
// changes the row's income category and keeps the sum.
func TestR75RealLedger(t *testing.T) {
	db, repo := clubTestDB(t)
	defer club.SetSheetMode(club.SheetModeOff)()
	ctx := context.Background()
	t.Cleanup(func() {
		_, _ = db.Pool.Exec(context.Background(), `UPDATE club_meta SET value = 'sheet' WHERE key = 'master'`)
	})
	if err := repo.ReplaceAll(ctx, sheetSnap(), "sheet"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Pool.Exec(ctx, `UPDATE club_meta SET value = 'server' WHERE key = 'master'`); err != nil {
		t.Fatal(err)
	}
	res, err := repo.DDSApply(ctx, []pg.DDSOp{{Op: "insert", CID: "bb", Set: map[string]json.RawMessage{
		"date": rawJSON("06.10.2026"), "income": rawJSON(10000), "incomeCat": rawJSON("БХ МК/Завтрак")}}}, "test", r75Now)
	if err != nil {
		t.Fatal(err)
	}
	bb := res.IDs["bb"]
	docs := &salesFakeDocs{}
	docs.put(t, "club", "bs_crm", map[string]any{"leads": []any{r75Lead("L1", map[string]any{
		"payments": []any{map[string]any{"id": "p1", "product": "breakfast", "amount": 10000, "ddsId": bb, "status": "paid"}}})}})
	b := &SalesBook{Docs: docs, Ledger: repo, Now: func() time.Time { return r75Now }}
	p, _, err := EnsurePayout(ctx, docs, RefPayout{Key: "k", Resident: "Амирхан", Referrer: "Асет"}, r75Now)
	if err != nil {
		t.Fatal(err)
	}
	paid, err := b.PayPayout(ctx, p.ID, "", "", "tg:1")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := b.Move(ctx, "L1", "p1", "razbor", "завтрак не собрали", "tg:1"); err != nil {
		t.Fatal(err)
	}
	rows, _ := repo.DDSRows(ctx)
	var exp, inc pg.DDSRow
	for _, r := range rows {
		switch r.ID {
		case paid.DDSID:
			exp = r
		case bb:
			inc = r
		}
	}
	if exp.Expense != 100000 || exp.ExpenseCat != RefBonusCat || exp.Income != 0 {
		t.Fatalf("expense row: %+v", exp)
	}
	if inc.Income != 10000 || inc.IncomeCat != "БХ Экспресс разбор" || !strings.Contains(inc.Comment, "перенесено с «БХ МК/Завтрак»") {
		t.Fatalf("moved row: %+v", inc)
	}
}
