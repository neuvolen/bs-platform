package http

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/bnursik/business_surgery_backend/internal/club"
	pg "github.com/bnursik/business_surgery_backend/internal/repository/pg"
)

// R82: «Долги и штрафы почему-то подгружаются дольше». The tab's report on a
// club the size of the real one (70 residents, 3 000 journal rows, 400 fines)
// is built under 300 ms, and a second open is served from the snapshot until
// something is written.
func TestR82DebtsFast(t *testing.T) {
	e := newOffEnv(t, "server", nil)
	ctx := context.Background()
	db := e.db.Pool
	for i := 0; i < 70; i++ {
		if _, err := db.Exec(ctx, `INSERT INTO club_residents (name, format, tariff, meetings_granted, renew_debt, former) VALUES ($1, $2, 100000, 3, $3, $4)`,
			fmt.Sprintf("Перф Резидент%02d", i), []string{"Онлайн", "Офлайн"}[i%2], int64(i%4)*50000, i%9 == 0); err != nil {
			t.Fatal(err)
		}
	}
	day := time.Now().AddDate(0, 0, -400)
	for i := 0; i < 3000; i++ {
		cat := []string{"БХ Трекинг продление", "БХ Штраф", "Консультация", "БХ Трекинг вход"}[i%4]
		name := fmt.Sprintf("Перф Резидент%02d", i%70)
		if i%7 == 0 {
			name = fmt.Sprintf("перф резидент%02d", i%70) // another spelling: matched by name
		}
		if i%31 == 0 {
			name = "Кто-то Неизвестный"
		}
		if _, err := db.Exec(ctx, `INSERT INTO club_payments (date, income, income_cat, resident) VALUES ($1, $2, $3, $4)`,
			day.AddDate(0, 0, i/8), 10000+int64(i%5)*10000, cat, name); err != nil {
			t.Fatal(err)
		}
	}
	for i := 0; i < 400; i++ {
		if _, err := db.Exec(ctx, `INSERT INTO club_fines (resident, type, amount, date, status) VALUES ($1, 'Не сдан отчёт', 10000, $2, $3)`,
			fmt.Sprintf("Перф Резидент%02d", i%70), day.AddDate(0, 0, i), []string{"Не оплатил", "Оплатил"}[i%2]); err != nil {
			t.Fatal(err)
		}
	}
	t0 := time.Now()
	rep, err := e.repo.Debts(ctx)
	if err != nil {
		t.Fatal(err)
	}
	cold := time.Since(t0)
	if len(rep.Residents) < 70 {
		t.Fatalf("residents %d", len(rep.Residents))
	}
	t1 := time.Now()
	rep2, err := e.repo.Debts(ctx)
	if err != nil {
		t.Fatal(err)
	}
	warm := time.Since(t1)
	t.Logf("debts: cold %v, warm %v", cold, warm)
	if cold > 300*time.Millisecond {
		t.Fatalf("cold report %v, want under 300 ms", cold)
	}
	if warm > 30*time.Millisecond || rep2 != rep {
		t.Fatalf("second open %v (cached %v), want the snapshot", warm, rep2 == rep)
	}
	// a write (a fine) makes the next open rebuild the report
	if _, err := db.Exec(ctx, `INSERT INTO club_fines (resident, type, amount, date, status) VALUES ('Перф Резидент01', 'Опоздание', 5000, now(), 'Не оплатил')`); err != nil {
		t.Fatal(err)
	}
	rep3, err := e.repo.Debts(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if rep3 == rep {
		t.Fatal("the snapshot outlived a write")
	}
	var open int64
	for _, r := range rep3.Residents {
		if r.Name == "Перф Резидент01" {
			open = r.FinesOpen
		}
	}
	var was int64
	for _, r := range rep.Residents {
		if r.Name == "Перф Резидент01" {
			was = r.FinesOpen
		}
	}
	if open != was+5000 {
		t.Fatalf("new fine not in the report: %d → %d", was, open)
	}
	// an update (a fine marked paid) too
	if _, err := db.Exec(ctx, `UPDATE club_fines SET status = 'Оплатил' WHERE resident = 'Перф Резидент01' AND type = 'Опоздание'`); err != nil {
		t.Fatal(err)
	}
	rep4, err := e.repo.Debts(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if rep4 == rep3 {
		t.Fatal("the snapshot outlived an update")
	}
	// the audit stays in the server logs only: the tab gets no «Проверить»
	for _, r := range rep4.Residents {
		if len(r.Check) > 0 {
			t.Fatalf("check in the report: %s %v", r.Name, r.Check)
		}
	}
}

// R82: «Дмитрий оплатил 50к, 50к позже заплатит»: the start-up adjustment sets
// his debt to 50 000 once, with the owner's reason; the audit no longer flags him.
func TestR82TsoiAdjust(t *testing.T) {
	e := newOffEnv(t, "server", nil)
	ctx := context.Background()
	_, _ = e.db.Pool.Exec(ctx, `DELETE FROM club_debt_adjust; DELETE FROM club_balance_log`)
	if _, err := e.db.Pool.Exec(ctx, `INSERT INTO club_residents (name, format, tariff, meetings_granted, renew_debt) VALUES ('Дмитрий Цой', 'Офлайн', 100000, 3, 750000)`); err != nil {
		t.Fatal(err)
	}
	if r := e.call(offOwner, "addPayment", "type", "income", "amount", "50000", "src", "БХ Трекинг продление", "resident", "Дмитрий Цой", "isCash", "true"); r["ok"] != true {
		t.Fatalf("payment %v", r)
	}
	ClubR70AtStart(ctx, e.repo, nil)
	ClubR70AtStart(ctx, e.repo, nil)
	if n := e.n(`SELECT count(*) FROM club_debt_adjust WHERE key = 'r82-tsoi-2026-10-10' AND reason = 'Договорились: 50 000 оплачено, 50 000 позже (10.10)'`); n != 1 {
		t.Fatalf("adjustments %d", n)
	}
	var rest, renew int64
	_ = e.db.Pool.QueryRow(ctx, `SELECT rest_entry, renew_debt FROM club_residents WHERE name = 'Дмитрий Цой'`).Scan(&rest, &renew)
	if rest+renew != 50000 {
		t.Fatalf("debt %d", rest+renew)
	}
	fl, err := e.repo.DebtAudit(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range fl {
		if f.Resident == "Дмитрий Цой" {
			t.Fatalf("still flagged: %v", f.Why)
		}
	}
}

// R82: «Бывшие» with their data, and «Вернуть в резиденты» keeps the history
// and starts a new package today.
func TestR82FormerReturn(t *testing.T) {
	e := newOffEnv(t, "server", nil)
	ctx := context.Background()
	db := e.db.Pool
	var id int64
	if err := db.QueryRow(ctx, `INSERT INTO club_residents (name, format, tariff, meetings_granted, meetings_done, renew_debt, former, joined_at, left_at, note)
		VALUES ('Бывший Тестов', 'Онлайн', 100000, 3, 2, 30000, true, '2025-03-01', '2026-08-01', 'ушёл летом') RETURNING id`).Scan(&id); err != nil {
		t.Fatal(err)
	}
	_, _ = db.Exec(ctx, `INSERT INTO club_payments (date, income, income_cat, resident) VALUES ('2026-05-02', 100000, 'БХ Трекинг продление', 'Бывший Тестов')`)
	_, _ = db.Exec(ctx, `INSERT INTO club_fines (resident, type, amount, date, status) VALUES ('Бывший Тестов', 'Опоздание', 10000, '2026-06-01', 'Не оплатил')`)
	_, _ = db.Exec(ctx, `INSERT INTO club_meeting_log (date, resident) VALUES ('2026-07-01', 'Бывший Тестов'), ('2026-07-15', 'Бывший Тестов')`)
	list, err := e.repo.FormerResidents(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var f *pg.FormerRow
	for i := range list {
		if list[i].ID == id {
			f = &list[i]
		}
	}
	if f == nil || f.PaidTotal != 100000 || f.FinesOpen != 10000 || f.Meetings != 2 || f.LastMeet != "15.07.2026" || f.Debt != 30000 || f.Left != "01.08.2026" {
		t.Fatalf("former row %+v", f)
	}
	res, err := e.repo.ReturnResident(ctx, id, "Офлайн", 0, "test")
	if err != nil {
		t.Fatal(err)
	}
	var former bool
	var granted, done, renew int64
	var format, note string
	var from *time.Time
	_ = db.QueryRow(ctx, `SELECT former, meetings_granted, meetings_done, renew_debt, format, note, package_from FROM club_residents WHERE id = $1`, id).Scan(&former, &granted, &done, &renew, &format, &note, &from)
	if former || granted != 3 || done != 0 || renew != 30000 || format != "Офлайн" || from == nil || res.Granted != 3 {
		t.Fatalf("returned: former %v %d/%d debt %d %s from %v", former, done, granted, renew, format, from)
	}
	if note != "ушёл летом; вернулся в клуб "+time.Now().In(club.Almaty).Format("02.01.2006") {
		t.Fatalf("note %q", note)
	}
	if _, err := e.repo.ReturnResident(ctx, id, "", 0, "test"); err == nil {
		t.Fatal("returned twice")
	}
	if n := e.n(`SELECT count(*) FROM club_payments WHERE resident = 'Бывший Тестов'`); n != 1 {
		t.Fatalf("payments %d", n)
	}
}

// R82: the meme stickers go into the club's pack once, with their categories.
func TestR82MemeStickers(t *testing.T) {
	repo, ctx := testPlatformDB(t, "bs_stickerpack_srv")
	if _, err := repo.PutDoc(ctx, "club", "bs_stickerpack_srv", 0, `[{"id":"old1","name":"Старый"}]`, false, "test"); err != nil {
		t.Fatal(err)
	}
	h := NewPlatformAI(repo, nil)
	h.LoadMemeStickers(ctx)
	h.LoadMemeStickers(ctx)
	d, err := repo.GetDoc(ctx, "club", "bs_stickerpack_srv")
	if err != nil || d == nil {
		t.Fatal(err)
	}
	var list []struct{ ID, Name, Cat string }
	if err := json.Unmarshal([]byte(d.Value), &list); err != nil {
		t.Fatal(err)
	}
	cats := map[string]int{}
	for _, x := range list[1:] {
		cats[x.Cat]++
		if !repo.FileExists(ctx, x.ID) {
			t.Fatalf("no file for %s", x.Name)
		}
	}
	if list[0].ID != "old1" || len(list) != 43 || len(cats) != 5 {
		t.Fatalf("pack %d, cats %v", len(list), cats)
	}
}
