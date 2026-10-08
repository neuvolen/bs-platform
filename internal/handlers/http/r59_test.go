package http

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	pg "github.com/bnursik/business_surgery_backend/internal/repository/pg"
)

// R59: a resident's payment pays off their debt (the server never did it
// after the cutover); a name that matches nobody is linked by hand.
func TestR59PaymentPaysDebt(t *testing.T) {
	e := newOffEnv(t, "sheet", nil)
	ctx := context.Background()
	// Альтаир owes the renewal (50 000) and the rest of the entry fee (20 000)
	if _, err := e.db.Pool.Exec(ctx, `UPDATE club_residents SET renew_debt = 50000, rest_entry = 20000 WHERE name = 'Альтаир'`); err != nil {
		t.Fatal(err)
	}
	debt := func() (rest, renew int) {
		t.Helper()
		if err := e.db.Pool.QueryRow(ctx, `SELECT rest_entry, renew_debt FROM club_residents WHERE name = 'Альтаир'`).Scan(&rest, &renew); err != nil {
			t.Fatal(err)
		}
		return
	}
	// The name typed in another case: matched, the rest goes first, then the renewal
	if r := e.call(offOwner, "addPayment", "type", "income", "amount", "50000", "src", "БХ Трекинг продление", "resident", "альтаир", "isCash", "true"); r["ok"] != true {
		t.Fatalf("payment %v", r)
	}
	if rest, renew := debt(); rest != 0 || renew != 20000 {
		t.Fatalf("after payment: rest %d renew %d, want 0 and 20000", rest, renew)
	}
	if n := e.n(`SELECT count(*) FROM club_payments WHERE resident = 'Альтаир' AND income = 50000 AND applied`); n != 1 {
		t.Fatalf("the payment is not linked and «учтено»: %d", n)
	}
	// The package is not extended by money that paid the debt
	if n := e.n(`SELECT meetings_granted FROM club_residents WHERE name = 'Альтаир'`); n != 3 {
		t.Fatalf("granted %d, want 3", n)
	}
	// A misspelt name: the row stays «не учтено»; linking it by hand pays the rest, once
	if r := e.call(offOwner, "addPayment", "type", "income", "amount", "30000", "src", "БХ Трекинг продление", "resident", "Алтаир", "isCash", "true"); r["ok"] != true {
		t.Fatalf("payment 2 %v", r)
	}
	var id int64
	if err := e.db.Pool.QueryRow(ctx, `SELECT id FROM club_payments WHERE resident = 'Алтаир' AND NOT applied`).Scan(&id); err != nil {
		t.Fatalf("unmatched payment: %v", err)
	}
	if _, renew := debt(); renew != 20000 {
		t.Fatalf("an unmatched payment changed the debt: %d", renew)
	}
	res, err := e.repo.LinkPayment(ctx, id, "Альтаир")
	if err != nil || res.Paid != 20000 || res.Resident != "Альтаир" {
		t.Fatalf("link %+v %v", res, err)
	}
	if rest, renew := debt(); rest != 0 || renew != 0 {
		t.Fatalf("after link: %d %d", rest, renew)
	}
	if _, err := e.repo.LinkPayment(ctx, id, "Альтаир"); err == nil {
		t.Fatal("a payment was counted twice")
	}
	// A fine's payment is not linked to the debt
	if r := e.call(offOwner, "addPayment", "type", "income", "amount", "10000", "src", "БХ Штраф", "resident", "Асет", "isCash", "true"); r["ok"] != true {
		t.Fatalf("fine payment %v", r)
	}
	var fid int64
	_ = e.db.Pool.QueryRow(ctx, `SELECT id FROM club_payments WHERE income_cat = 'БХ Штраф' AND resident = 'Асет'`).Scan(&fid)
	if _, err := e.repo.LinkPayment(ctx, fid, "Асет"); err == nil {
		t.Fatal("a fine's payment paid the debt")
	}
	if n := e.n(`SELECT rest_entry FROM club_residents WHERE name = 'Асет'`); n != 50000 {
		t.Fatalf("Асет's rest changed: %d", n)
	}
	// A row added in the ДДС grid with a resident pays the debt too
	if _, err := e.db.Pool.Exec(ctx, `UPDATE club_residents SET renew_debt = 40000 WHERE name = 'Альтаир'`); err != nil {
		t.Fatal(err)
	}
	raw := func(v string) json.RawMessage { return json.RawMessage(v) }
	ops := []pg.DDSOp{
		{Op: "insert", CID: "c1", Set: map[string]json.RawMessage{"income": raw(`40000`), "resident": raw(`"Альтаир"`), "incomeCat": raw(`"БХ Трекинг продление"`)}},
		// an old row pasted as history is not counted against today's debt
		{Op: "insert", CID: "c2", Set: map[string]json.RawMessage{"date": raw(`"01.01.2026"`), "income": raw(`40000`), "resident": raw(`"Альтаир"`), "incomeCat": raw(`"БХ Трекинг продление"`)}},
	}
	out, err := e.repo.DDSApply(ctx, ops, "test", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if _, renew := debt(); renew != 0 {
		t.Fatalf("grid row did not pay the debt: %d", renew)
	}
	if n := e.n(`SELECT count(*) FROM club_payments WHERE id = $1 AND NOT applied`, out.IDs["c2"]); n != 1 {
		t.Fatal("an old pasted row was counted")
	}
}

// R59: the meetings package: at 3/3 the tariff goes to the debt once and the
// counter starts again at 0/3; the meeting stays in «Лог встреч».
func TestR59MeetingsPackageResets(t *testing.T) {
	e := newOffEnv(t, "sheet", nil)
	ctx := context.Background()
	day := "05.10.2026"
	// Альтаир: 2 of 3 done, tariff 100 000, no debt
	if r := e.call(offOwner, "markAttendance", "names", "Альтаир", "date", day); r["ok"] != true {
		t.Fatalf("attendance %v", r)
	}
	var done, granted, renew int
	if err := e.db.Pool.QueryRow(ctx, `SELECT meetings_done, meetings_granted, renew_debt FROM club_residents WHERE name = 'Альтаир'`).Scan(&done, &granted, &renew); err != nil {
		t.Fatal(err)
	}
	if done != 0 || granted != 3 || renew != 100000 {
		t.Fatalf("after 3/3: %d/%d debt %d, want 0/3 and 100000", done, granted, renew)
	}
	if n := e.n(`SELECT count(*) FROM club_meeting_log WHERE resident = 'Альтаир' AND date = '2026-10-05'`); n != 1 {
		t.Fatalf("meeting log %d", n)
	}
	// The next meeting counts 1/3, no new debt
	if r := e.call(offOwner, "markAttendance", "names", "Альтаир", "date", "06.10.2026"); r["ok"] != true {
		t.Fatalf("attendance 2 %v", r)
	}
	if err := e.db.Pool.QueryRow(ctx, `SELECT meetings_done, meetings_granted, renew_debt FROM club_residents WHERE name = 'Альтаир'`).Scan(&done, &granted, &renew); err != nil {
		t.Fatal(err)
	}
	if done != 1 || granted != 3 || renew != 100000 {
		t.Fatalf("after 1/3: %d/%d debt %d", done, granted, renew)
	}
	// Paying the tariff closes the debt and keeps the package where it is
	if r := e.call(offOwner, "addPayment", "type", "income", "amount", "100000", "src", "БХ Трекинг продление", "resident", "Альтаир", "isCash", "true"); r["ok"] != true {
		t.Fatalf("payment %v", r)
	}
	if err := e.db.Pool.QueryRow(ctx, `SELECT meetings_done, meetings_granted, renew_debt FROM club_residents WHERE name = 'Альтаир'`).Scan(&done, &granted, &renew); err != nil {
		t.Fatal(err)
	}
	if done != 1 || granted != 3 || renew != 0 {
		t.Fatalf("after payment: %d/%d debt %d", done, granted, renew)
	}
}

// R59: the team's preview of a resident's calendar reads that resident's
// calendar, not the admin's own; two residents never share one.
func TestR59CalendarPerResident(t *testing.T) {
	g, _, r, now := newGateway(t)
	g.Boards = &fakeBoards{byTg: map[int64]string{111: "Альтаир", 222: "Асет"}}
	fs := &fakeSyncTg{fakeSync: fakeSync{docs: map[string]*pg.PlatformDoc{}}, tg: map[string]int64{"Альтаир": 111, "Асет": 222}}
	g.Sync = fs
	req := func(method, path, body string, id int64) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		r.ServeHTTP(w, httptest.NewRequest(method, path+"&_tg="+url.QueryEscape(makeInitData(testBotToken, id, "X", *now)), strings.NewReader(body)))
		return w
	}
	// Альтаир fills his calendar himself
	if w := req("PUT", "/api/v1/app/mycal?x=1", `{"value":"{\"slots\":{\"2026-10-08|10\":\"focus\"}}","version":0}`, 111); w.Code != 200 {
		t.Fatalf("put: %d %s", w.Code, w.Body.String())
	}
	// The admin looks at Альтаир: sees his calendar
	if w := req("GET", "/api/v1/app/mycal?name="+url.QueryEscape("Альтаир"), "", 453800951); !strings.Contains(w.Body.String(), "focus") {
		t.Fatalf("admin view of Альтаир: %s", w.Body.String())
	}
	// …then Асет: empty, not Альтаир's
	if w := req("GET", "/api/v1/app/mycal?name="+url.QueryEscape("Асет"), "", 453800951); strings.Contains(w.Body.String(), "focus") || !strings.Contains(w.Body.String(), `"version":0`) {
		t.Fatalf("Альтаир's calendar shown for Асет: %s", w.Body.String())
	}
	// The admin edits Асет's calendar: it lands in Асет's scope only
	if w := req("PUT", "/api/v1/app/mycal?name="+url.QueryEscape("Асет"), `{"value":"{\"slots\":{\"2026-10-09|12\":\"meet\"}}","version":0}`, 453800951); w.Code != 200 {
		t.Fatalf("admin put: %d %s", w.Code, w.Body.String())
	}
	if fs.docs["user:tg:222/bs_mycal"] == nil || strings.Contains(fs.docs["user:tg:111/bs_mycal"].Value, "meet") || fs.docs["user:tg:453800951/bs_mycal"] != nil {
		t.Fatalf("scopes: %+v", fs.docs)
	}
	// A resident's own ?name= is ignored: nobody reads another resident's calendar
	if w := req("GET", "/api/v1/app/mycal?name="+url.QueryEscape("Асет"), "", 111); strings.Contains(w.Body.String(), "meet") {
		t.Fatalf("a resident read another's calendar: %s", w.Body.String())
	}
}

type fakeSyncTg struct {
	fakeSync
	tg map[string]int64
}

func (f *fakeSyncTg) ResidentTgByName(_ context.Context, name string) (int64, string, error) {
	return f.tg[name], name, nil
}
