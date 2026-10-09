package http

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/bnursik/business_surgery_backend/internal/club"
	pg "github.com/bnursik/business_surgery_backend/internal/repository/pg"
)

func r69Today() string { return time.Now().In(club.Almaty).Format("02.01.2006") }

func r69Fine(t *testing.T, e *offEnv, name string) (status string, paid int) {
	t.Helper()
	if err := e.db.Pool.QueryRow(context.Background(), `SELECT f.status, COALESCE((SELECT sum(amount) FROM club_pay_alloc WHERE fine_id = f.id), 0)
		FROM club_fines f WHERE f.resident = $1 AND f.type = 'Цена слова'`, name).Scan(&status, &paid); err != nil {
		t.Fatal(err)
	}
	return
}

// R69: «Альтаир: 100к штраф опять система не засчитала». A fine's payment
// pays the fine in part, from the app and from the ДДС grid alike; an edit
// or a delete of the row takes it back; a fine entered after its payment
// takes the payment.
func TestR69FinePaymentsLedger(t *testing.T) {
	e := newOffEnv(t, "server", nil)
	ctx := context.Background()
	if r := e.call(offOwner, "addFine", "name", "Альтаир", "type", "Цена слова", "amount", "200000", "date", r69Today()); r["ok"] != true {
		t.Fatalf("fine %v", r)
	}
	// 100 000 of a 200 000 fine, the name in another case (the bug: nothing happened)
	if r := e.call(offOwner, "addPayment", "type", "income", "amount", "100000", "src", "БХ Штраф", "resident", "альтаир", "isCash", "true"); r["ok"] != true {
		t.Fatalf("payment %v", r)
	}
	if st, paid := r69Fine(t, e, "Альтаир"); st != "Не оплатил" || paid != 100000 {
		t.Fatalf("after 100 000: %s, paid %d", st, paid)
	}
	if n := e.n(`SELECT renew_debt + rest_entry FROM club_residents WHERE name = 'Альтаир'`); n != 0 {
		t.Fatalf("a fine's payment touched the debt: %d", n)
	}
	// the rest from the ДДС grid
	res, err := e.repo.DDSApply(ctx, []pg.DDSOp{{Op: "insert", CID: "a", Set: map[string]json.RawMessage{
		"income": json.RawMessage(`"100 000"`), "incomeCat": json.RawMessage(`"БХ Штраф"`), "resident": json.RawMessage(`"Альтаир"`)}}}, "test", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if st, paid := r69Fine(t, e, "Альтаир"); st != "Оплатил" || paid != 200000 {
		t.Fatalf("after the grid row: %s, paid %d", st, paid)
	}
	gridID := res.IDs["a"]
	// a wrong category fixed in the grid: the money moves from the fine to the debt
	_, _ = e.db.Pool.Exec(ctx, `UPDATE club_residents SET renew_debt = 70000 WHERE name = 'Альтаир'`)
	if _, err := e.repo.DDSApply(ctx, []pg.DDSOp{{Op: "update", ID: gridID, Set: map[string]json.RawMessage{"incomeCat": json.RawMessage(`"БХ Трекинг продление"`)}}}, "test", time.Now()); err != nil {
		t.Fatal(err)
	}
	if st, paid := r69Fine(t, e, "Альтаир"); st != "Не оплатил" || paid != 100000 {
		t.Fatalf("after the category edit: %s, paid %d", st, paid)
	}
	if n := e.n(`SELECT renew_debt FROM club_residents WHERE name = 'Альтаир'`); n != 0 {
		t.Fatalf("debt after the edit: %d", n)
	}
	// deleting the row: the debt owes again
	if _, err := e.repo.DDSApply(ctx, []pg.DDSOp{{Op: "delete", ID: gridID}}, "test", time.Now()); err != nil {
		t.Fatal(err)
	}
	if n := e.n(`SELECT renew_debt FROM club_residents WHERE name = 'Альтаир'`); n != 70000 {
		t.Fatalf("debt after the delete: %d", n)
	}
	if n := e.n(`SELECT count(*) FROM club_pay_alloc WHERE payment_id = $1`, gridID); n != 0 {
		t.Fatalf("ledger lines of a deleted row: %d", n)
	}
	// paid before the fine was entered: the fine takes it
	if r := e.call(offOwner, "addPayment", "type", "income", "amount", "20000", "src", "БХ Штраф", "resident", "Асет", "isCash", "true"); r["ok"] != true {
		t.Fatalf("prepay %v", r)
	}
	if r := e.call(offOwner, "addFine", "name", "Асет", "type", "Нарушение", "amount", "20000", "date", r69Today()); r["ok"] != true {
		t.Fatalf("fine 2 %v", r)
	}
	// the old 10 000 fine of 26.09 is the oldest: the prepayment paid it, then 10 000 of the new one
	if n := e.n(`SELECT count(*) FROM club_fines WHERE resident = 'Асет' AND status = 'Оплатил'`); n != 1 {
		t.Fatalf("Асет's fines paid: %d", n)
	}
	rep, err := e.repo.Debts(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var aset *pg.DebtRow
	for i := range rep.Residents {
		if rep.Residents[i].Name == "Асет" {
			aset = &rep.Residents[i]
		}
	}
	if aset == nil || aset.FinesOpen != 10000 || aset.Debt != 50000 || aset.Total != 60000 {
		t.Fatalf("report %+v", aset)
	}
	// write-off with a reason: no longer owed
	var fid int64
	_ = e.db.Pool.QueryRow(ctx, `SELECT id FROM club_fines WHERE resident = 'Асет' AND type = 'Нарушение'`).Scan(&fid)
	if err := e.repo.WriteOffFine(ctx, fid, "уважительная причина", ""); err != nil {
		t.Fatal(err)
	}
	if n := e.n(`SELECT count(*) FROM club_fines WHERE id = $1 AND status = 'Списан' AND note LIKE '%уважительная причина'`, fid); n != 1 {
		t.Fatal("write-off not kept")
	}
	// its 10 000 came back as the payment's remainder
	if n := e.n(`SELECT count(*) FROM club_pay_alloc WHERE fine_id = $1`, fid); n != 0 {
		t.Fatal("a written-off fine keeps its payments")
	}
}

// R69: a name that matches nobody is linked by hand once; the spelling is
// kept and the next payment with it is found by itself.
func TestR69LinkKeepsAlias(t *testing.T) {
	e := newOffEnv(t, "server", nil)
	ctx := context.Background()
	_, _ = e.db.Pool.Exec(ctx, `UPDATE club_residents SET renew_debt = 100000 WHERE name = 'Альтаир'`)
	if r := e.call(offOwner, "addPayment", "type", "income", "amount", "30000", "src", "БХ Трекинг продление", "resident", "Алтаир Б.", "isCash", "true"); r["ok"] != true {
		t.Fatalf("payment %v", r)
	}
	var id int64
	if err := e.db.Pool.QueryRow(ctx, `SELECT id FROM club_payments WHERE resident = 'Алтаир Б.'`).Scan(&id); err != nil {
		t.Fatal(err)
	}
	rep, _ := e.repo.Debts(ctx)
	if len(rep.Unlinked) != 1 || rep.Unlinked[0].ID != id {
		t.Fatalf("unlinked %+v", rep.Unlinked)
	}
	res, err := e.repo.LinkPayment(ctx, id, "Альтаир")
	if err != nil || res.Paid != 30000 {
		t.Fatalf("link %+v %v", res, err)
	}
	if _, err := e.repo.LinkPayment(ctx, id, "Альтаир"); err == nil {
		t.Fatal("linked twice")
	}
	if r := e.call(offOwner, "addPayment", "type", "income", "amount", "20000", "src", "БХ Трекинг продление", "resident", "Алтаир Б.", "isCash", "true"); r["ok"] != true {
		t.Fatalf("payment 2 %v", r)
	}
	if n := e.n(`SELECT renew_debt FROM club_residents WHERE name = 'Альтаир'`); n != 50000 {
		t.Fatalf("the alias did not match: debt %d", n)
	}
}

// R69: the backfill: a dry run changes nothing; the real run applies the
// open rows since the cutover, writes the lines of rows already counted,
// skips a debt someone set by hand, and a second run finds nothing.
func TestR69AllocBackfill(t *testing.T) {
	cut := time.Now().AddDate(0, 0, -5)
	e := newOffEnv(t, "server", &cut)
	ctx := context.Background()
	_, _ = e.db.Pool.Exec(ctx, `UPDATE club_residents SET renew_debt = 100000, rest_entry = 0 WHERE name = 'Альтаир'`)
	_, _ = e.db.Pool.Exec(ctx, `INSERT INTO club_fines (resident, type, amount, date, status) VALUES ('Альтаир', 'Цена слова', 200000, $1, 'Не оплатил')`, cut.Format("2006-01-02"))
	day := func(d int) string { return time.Now().AddDate(0, 0, d).Format("2006-01-02") }
	ins := func(date string, amount int64, cat, res string, applied bool) {
		if _, err := e.db.Pool.Exec(ctx, `INSERT INTO club_payments (date, income, income_cat, resident, source, applied) VALUES ($1,$2,$3,$4,'server',$5)`, date, amount, cat, res, applied); err != nil {
			t.Fatal(err)
		}
	}
	ins(day(-1), 100000, "БХ Штраф", "Альтаир", false)             // the bug's row
	ins(day(-2), 50000, "БХ Трекинг продление", "Альтаир", true)   // counted by R63
	ins(day(-1), 40000, "БХ Трекинг продление", "Асет", false)     // Асет owes 50 000 entry rest
	ins(day(-1), 30000, "БХ Экспресс разбор", "Рустам", false)     // one-off
	ins(day(-30), 70000, "БХ Трекинг продление", "Альтаир", false) // the sheet's history
	ch, notes, err := e.repo.AllocBackfill(ctx, true)
	if err != nil {
		t.Fatal(err)
	}
	if len(ch) != 2 || len(notes) < 2 {
		t.Fatalf("dry run %+v %v", ch, notes)
	}
	if n := e.n(`SELECT count(*) FROM club_pay_alloc`); n != 0 {
		t.Fatal("the dry run kept lines")
	}
	ch, _, err = e.repo.AllocBackfill(ctx, false)
	if err != nil {
		t.Fatal(err)
	}
	var alt pg.AllocChange
	for _, c := range ch {
		if c.Resident == "Альтаир" {
			alt = c
		}
	}
	if alt.FinesBefore != 200000 || alt.FinesAfter != 100000 || alt.DebtAfter != 100000 {
		t.Fatalf("Альтаир %+v", alt)
	}
	if n := e.n(`SELECT rest_entry FROM club_residents WHERE name = 'Асет'`); n != 10000 {
		t.Fatalf("Асет rest %d", n)
	}
	if n := e.n(`SELECT count(*) FROM club_pay_alloc WHERE by = 'r69-legacy'`); n != 1 {
		t.Fatalf("legacy lines %d", n)
	}
	if n := e.n(`SELECT count(*) FROM club_payments p WHERE date < $1 AND EXISTS (SELECT 1 FROM club_pay_alloc x WHERE x.payment_id = p.id)`, cut.Format("2006-01-02")); n != 0 {
		t.Fatal("history before the cutover applied")
	}
	if !e.repo.AllocBackfillDone(ctx) {
		t.Fatal("not marked done")
	}
	ch, _, _ = e.repo.AllocBackfill(ctx, false)
	if len(ch) != 0 {
		t.Fatalf("second run %+v", ch)
	}
}

// R69: «проведено» is computed from «Лог встреч»: the 3/3 package closes once
// (renewal debt, new package from the next day), a manual edit is a
// correction to the log, and the reconstruction counts a year's package from
// its anniversary.
func TestR69MeetingsFromLog(t *testing.T) {
	e := newOffEnv(t, "server", nil)
	ctx := context.Background()
	_, _ = e.db.Pool.Exec(ctx, `DELETE FROM club_meeting_log`)
	// Альтаир 2/3 in the sheet: the third meeting closes the package
	if r := e.call(offOwner, "confirmMeeting", "res", "Альтаир", "date", r69Today(), "time", ""); r["ok"] != true {
		t.Fatalf("confirm %v", r)
	}
	if n := e.n(`SELECT meetings_done FROM club_residents WHERE name = 'Альтаир'`); n != 0 {
		t.Fatalf("after 3/3: %d", n)
	}
	if n := e.n(`SELECT renew_debt FROM club_residents WHERE name = 'Альтаир'`); n != 100000 {
		t.Fatalf("renewal %d", n)
	}
	// the same day again: no second package, no second debt
	_ = e.call(offOwner, "markAttendance", "names", "Альтаир", "date", r69Today())
	if n := e.n(`SELECT renew_debt FROM club_residents WHERE name = 'Альтаир'`); n != 100000 {
		t.Fatalf("renewal twice %d", n)
	}
	// manual edit: 1/3, then a recount keeps it
	if r := e.call(offOwner, "setMeetings", "name", "Асет", "done", "2", "granted", "3"); r["ok"] != true {
		t.Fatalf("set %v", r)
	}
	if _, err := e.repo.RecountMeetings(ctx); err != nil {
		t.Fatal(err)
	}
	if n := e.n(`SELECT meetings_done FROM club_residents WHERE name = 'Асет'`); n != 2 {
		t.Fatalf("Асет %d", n)
	}
	// a counter written over by hand in the table drifts back to the log
	_, _ = e.db.Pool.Exec(ctx, `UPDATE club_residents SET meetings_done = 9 WHERE name = 'Асет'`)
	if ch, err := e.repo.RecountMeetings(ctx); err != nil || len(ch) != 1 {
		t.Fatalf("recount %+v %v", ch, err)
	}
	if n := e.n(`SELECT meetings_done FROM club_residents WHERE name = 'Асет'`); n != 2 {
		t.Fatalf("drift kept %d", n)
	}
	// a year's package wiped by the script: the reconstruction counts the log from the anniversary
	join := time.Now().AddDate(-1, 0, -20)
	_, _ = e.db.Pool.Exec(ctx, `INSERT INTO club_residents (name, format, tariff, meetings_granted, meetings_done, joined_at) VALUES ('Азамат TV', 'Офлайн', 1000000, 36, 0, $1)`, join.Format("2006-01-02"))
	for _, d := range []int{-15, -9, -3} {
		_, _ = e.db.Pool.Exec(ctx, `INSERT INTO club_meeting_log (date, resident) VALUES ($1, 'Азамат TV')`, time.Now().AddDate(0, 0, d).Format("2006-01-02"))
	}
	_, _ = e.db.Pool.Exec(ctx, `INSERT INTO club_meeting_log (date, resident) VALUES ($1, 'Азамат TV')`, time.Now().AddDate(0, 0, -40).Format("2006-01-02")) // last year's package
	keep := pg.R69Restore
	pg.R69Restore = nil // the owner's dates are for the real data
	defer func() { pg.R69Restore = keep }()
	ch, _, err := e.repo.MeetBackfill(ctx, false)
	if err != nil {
		t.Fatal(err)
	}
	if n := e.n(`SELECT meetings_done FROM club_residents WHERE name = 'Азамат TV'`); n != 3 {
		t.Fatalf("Азамат %d (%+v)", n, ch)
	}
	// the confirmation by first name only still counts
	if r := e.call(offOwner, "confirmMeeting", "res", "Азамат", "date", r69Today(), "time", ""); r["ok"] != true {
		t.Fatalf("confirm Азамат %v", r)
	}
	if n := e.n(`SELECT meetings_done FROM club_residents WHERE name = 'Азамат TV'`); n != 4 {
		t.Fatalf("Азамат after confirm %d", n)
	}
}

// R69: «не пришёл без предупреждения»: the meeting is marked, a 50 000 fine
// is written once and the resident gets one bot message.
func TestR69NoShowFine(t *testing.T) {
	e := newOffEnv(t, "server", nil)
	ctx := context.Background()
	_, _ = e.db.Pool.Exec(ctx, `DELETE FROM club_meetings`)
	day := r69Today()
	if x := e.call(offOwner, "addSchedule", "res", "Асет", "date", day, "time", "10:00"); x["ok"] != true {
		t.Fatalf("schedule %v", x)
	}
	for i := 0; i < 2; i++ {
		if x := e.call(offOwner, "missMeeting", "res", "Асет", "date", day, "time", "10:00"); x["ok"] != true {
			t.Fatalf("miss %v", x)
		}
	}
	if n := e.n(`SELECT count(*) FROM club_fines WHERE resident = 'Асет' AND type = $1 AND amount = 50000`, pg.NoShowType); n != 1 {
		t.Fatalf("no-show fines %d", n)
	}
	if n := e.n(`SELECT count(*) FROM club_meetings WHERE resident = 'Асет' AND status = 'noshow' AND NOT done`); n != 1 {
		t.Fatal("meeting not marked")
	}
	if n := e.n(`SELECT meetings_done FROM club_residents WHERE name = 'Асет'`); n != 1 {
		t.Fatalf("a missed meeting counted: %d", n)
	}
	count := func() int {
		e.tg.mu.Lock()
		defer e.tg.mu.Unlock()
		n := 0
		for _, c := range e.tg.calls {
			if txt, _ := c["text"].(string); c["_m"] == "sendMessage" && strings.Contains(txt, "не пришли на встречу") {
				if id, _ := c["chat_id"].(float64); int64(id) != 478757502 {
					t.Fatalf("sent to %v", c["chat_id"])
				}
				n++
			}
		}
		return n
	}
	e.svc.Tick(ctx, time.Now())
	e.svc.Tick(ctx, time.Now())
	if n := count(); n != 1 {
		t.Fatalf("messages %d", n)
	}
}
