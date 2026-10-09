package http

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/bnursik/business_surgery_backend/internal/club"
	pg "github.com/bnursik/business_surgery_backend/internal/repository/pg"
)

func r70Debt(t *testing.T, e *offEnv) (done, renew int) {
	t.Helper()
	if err := e.db.Pool.QueryRow(context.Background(), `SELECT meetings_done, renew_debt FROM club_residents WHERE name = 'Альтаир'`).Scan(&done, &renew); err != nil {
		t.Fatal(err)
	}
	return
}

// R70: «Почему у Альтаира опять стоит, что он должен 100к?» The sheet closed
// his package (3/3) and charged it; he paid it (2 × 50 000); the next
// meeting on the full counter must not charge the same package again.
func TestR70FullCounterNotChargedTwice(t *testing.T) {
	e := newOffEnv(t, "server", nil)
	ctx := context.Background()
	_, _ = e.db.Pool.Exec(ctx, `DELETE FROM club_debt_adjust; DELETE FROM club_balance_log`)
	_, _ = e.db.Pool.Exec(ctx, `DELETE FROM club_meeting_log`)
	if r := e.call(offOwner, "setMeetings", "name", "Альтаир", "done", "3", "granted", "3"); r["ok"] != true {
		t.Fatalf("set %v", r)
	}
	_, _ = e.db.Pool.Exec(ctx, `UPDATE club_residents SET renew_debt = 100000 WHERE name = 'Альтаир'`)
	for i := 0; i < 2; i++ {
		if r := e.call(offOwner, "addPayment", "type", "income", "amount", "50000", "src", "БХ Трекинг продление", "resident", "Альтаир", "isCash", "true"); r["ok"] != true {
			t.Fatalf("payment %v", r)
		}
	}
	if _, renew := r70Debt(t, e); renew != 0 {
		t.Fatalf("paid, debt %d", renew)
	}
	if r := e.call(offOwner, "markAttendance", "names", "Альтаир", "date", r69Today()); r["ok"] != true {
		t.Fatalf("attendance %v", r)
	}
	if done, renew := r70Debt(t, e); done != 1 || renew != 0 {
		t.Fatalf("after the meeting on 3/3: %d/3, debt %d; want 1/3 and 0", done, renew)
	}
	// the group marked again the same day
	_ = e.call(offOwner, "markAttendance", "names", "Альтаир|Асет", "date", r69Today())
	if done, renew := r70Debt(t, e); done != 1 || renew != 0 {
		t.Fatalf("marked twice: %d/3, debt %d", done, renew)
	}
	// every debt change has its reason
	if n := e.n(`SELECT count(*) FROM club_balance_log WHERE name = 'Альтаир' AND reason LIKE 'оплата 50 000 ₸%'`); n != 2 {
		t.Fatalf("balance log of the payments: %d", n)
	}
}

// R70: a renewal paid before the package closed pays the charge at once.
func TestR70PrepaidPackage(t *testing.T) {
	e := newOffEnv(t, "server", nil)
	ctx := context.Background()
	_, _ = e.db.Pool.Exec(ctx, `DELETE FROM club_debt_adjust; DELETE FROM club_balance_log`)
	_, _ = e.db.Pool.Exec(ctx, `DELETE FROM club_meeting_log`)
	_, _ = e.db.Pool.Exec(ctx, `UPDATE club_residents SET renew_debt = 0, rest_entry = 0 WHERE name = 'Альтаир'`)
	if r := e.call(offOwner, "addPayment", "type", "income", "amount", "100000", "src", "БХ Трекинг продление", "resident", "Альтаир", "isCash", "true"); r["ok"] != true {
		t.Fatalf("payment %v", r)
	}
	// Альтаир 2/3: this meeting closes the package
	if r := e.call(offOwner, "confirmMeeting", "res", "Альтаир", "date", r69Today(), "time", ""); r["ok"] != true {
		t.Fatalf("confirm %v", r)
	}
	// the payment over the debt extended the package (2/3 → 0/4 today), the meeting is 1/4: no charge
	if done, renew := r70Debt(t, e); done != 1 || renew != 0 {
		t.Fatalf("prepaid package: %d done, debt %d; want 1 and 0", done, renew)
	}
	if n := e.n(`SELECT count(*) FROM club_balance_log WHERE name = 'Альтаир'`); n != 0 {
		t.Fatalf("debt changed: %d", n)
	}
	// a remainder smaller than the tariff pays the next charge at once
	_, _ = e.db.Pool.Exec(ctx, `UPDATE club_residents SET meetings_granted = 3, meetings_adjust = meetings_adjust + 1 WHERE name = 'Альтаир'`)
	if r := e.call(offOwner, "addPayment", "type", "income", "amount", "30000", "src", "БХ Трекинг продление", "resident", "Альтаир", "isCash", "true"); r["ok"] != true {
		t.Fatalf("payment 2 %v", r)
	}
	if r := e.call(offOwner, "markAttendance", "names", "Альтаир", "date", time.Now().In(club.Almaty).AddDate(0, 0, 1).Format("02.01.2006")); r["ok"] != true {
		t.Fatalf("attendance %v", r)
	}
	if done, renew := r70Debt(t, e); done != 0 || renew != 70000 {
		t.Fatalf("charge with a 30 000 prepayment: %d done, debt %d; want 0 and 70 000", done, renew)
	}
	if n := e.n(`SELECT count(*) FROM club_balance_log WHERE name = 'Альтаир' AND reason LIKE 'пакет 3/3 закрыт встречей%'`); n != 1 {
		t.Fatalf("charge reason: %d", n)
	}
}

// R70: «Установить долг» holds: deleting a payment made before it does not
// bring the old debt back; the start-up adjustment is made once; the audit
// flags a debt that does not fit and stops once the team set it.
func TestR70SetDebtHolds(t *testing.T) {
	e := newOffEnv(t, "server", nil)
	ctx := context.Background()
	_, _ = e.db.Pool.Exec(ctx, `DELETE FROM club_debt_adjust; DELETE FROM club_balance_log`)
	_, _ = e.db.Pool.Exec(ctx, `UPDATE club_residents SET renew_debt = 300000, rest_entry = 0 WHERE name = 'Альтаир'`)
	res, err := e.repo.DDSApply(ctx, []pg.DDSOp{{Op: "insert", CID: "a", Set: map[string]json.RawMessage{
		"income": json.RawMessage(`"100 000"`), "incomeCat": json.RawMessage(`"БХ Трекинг продление"`), "resident": json.RawMessage(`"Альтаир"`)}}}, "test", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if _, renew := r70Debt(t, e); renew != 200000 {
		t.Fatalf("after the payment %d", renew)
	}
	var row *pg.DebtRow
	find := func() {
		rep, err := e.repo.Debts(ctx)
		if err != nil {
			t.Fatal(err)
		}
		row = nil
		for i := range rep.Residents {
			if rep.Residents[i].Name == "Альтаир" {
				row = &rep.Residents[i]
			}
		}
		if row == nil {
			t.Fatal("no Альтаир in the report")
		}
	}
	find()
	if len(row.Check) == 0 {
		t.Fatalf("200 000 at a 100 000 tariff is not flagged: %+v", row)
	}
	ClubR70AtStart(ctx, e.repo, nil)
	ClubR70AtStart(ctx, e.repo, nil)
	if n := e.n(`SELECT count(*) FROM club_debt_adjust WHERE key = 'r70-altair-2026-10-10'`); n != 1 {
		t.Fatalf("adjustments %d", n)
	}
	if _, renew := r70Debt(t, e); renew != 0 {
		t.Fatalf("after the adjustment %d", renew)
	}
	find()
	if len(row.Check) != 0 || len(row.History) == 0 {
		t.Fatalf("after the adjustment: check %v, history %d", row.Check, len(row.History))
	}
	if _, err := e.repo.DDSApply(ctx, []pg.DDSOp{{Op: "delete", ID: res.IDs["a"]}}, "test", time.Now()); err != nil {
		t.Fatal(err)
	}
	if _, renew := r70Debt(t, e); renew != 0 {
		t.Fatalf("a deleted old payment brought the debt back: %d", renew)
	}
	// «Установить долг» from the tab
	var id int64
	_ = e.db.Pool.QueryRow(ctx, `SELECT id FROM club_residents WHERE name = 'Альтаир'`).Scan(&id)
	if _, err := e.repo.SetDebt(ctx, id, "", 50000, "договорились", "test", ""); err != nil {
		t.Fatal(err)
	}
	if _, renew := r70Debt(t, e); renew != 50000 {
		t.Fatalf("set 50 000: %d", renew)
	}
}
