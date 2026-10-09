package pg

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/bnursik/business_surgery_backend/internal/club"
	"github.com/jackc/pgx/v5"
)

// R69: «Альтаир: 100к штраф опять система не засчитала. Реши проблему
// системно». A fine's payment closed only fines it covered in full (100 000
// against a 200 000 fine: nothing), only when the name matched exactly and
// only from the app; the ДДС grid skipped fines altogether, debts only within
// 7 days, and the start-up fixes were one-offs.
//
// One model now. Every income row linked to a resident (by id; by name or
// alias as the fallback) is applied by its category, and what it paid is
// written into the ledger club_pay_alloc:
//   - a fine's category → the resident's oldest unpaid fines (in part too);
//   - membership (tracking, renewal, entry and any other club income) → the
//     rest of the entry fee, then the renewal debt;
//   - one-off services (express review, master class) → nothing.
// What is left is the row's remainder, shown in «Учёт → Долги и штрафы».
// Applying is idempotent: a row's ledger lines say what it already paid.
// Editing a row takes its lines back first and applies it again; deleting it
// takes them back. The app, the bot, the platform's forms and the ДДС grid
// all come here. Rows dated before the cutover are the sheet's history: the
// sheet's balances already have them.

const (
	AllocFine    = "fine"
	AllocRest    = "rest"
	AllocRenew   = "renew"
	AllocPackage = "package"
)

// PayKind: what an income of that category pays: "fine", "debt" or "" (a
// one-off service, nothing).
func PayKind(cat string) string {
	c := club.NormCat(cat)
	if strings.Contains(c, "штраф") {
		return "fine"
	}
	if club.NonMembershipCat(cat) {
		return ""
	}
	return "debt"
}

// AllocLine is one ledger line.
type AllocLine struct {
	ID        int64  `json:"id,omitempty"`
	PaymentID int64  `json:"paymentId"`
	Kind      string `json:"kind"`
	FineID    int64  `json:"fineId,omitempty"`
	Amount    int64  `json:"amount"`
	At        string `json:"at,omitempty"`
	By        string `json:"by,omitempty"`
}

// AllocResult: what applying one row did.
type AllocResult struct {
	PaymentID  int64       `json:"paymentId"`
	Resident   string      `json:"resident"`
	ResidentID int64       `json:"residentId"`
	Kind       string      `json:"kind"`
	Income     int64       `json:"income"`
	Lines      []AllocLine `json:"lines"`
	Paid       int64       `json:"paid"` // in all lines of the row
	Left       int64       `json:"left"` // the remainder
	Why        string      `json:"why,omitempty"`
}

type payRow struct {
	id         int64
	date       time.Time
	income     int64
	cat        string
	resident   string
	residentID *int64
	applied    bool
}

func (a *applier) payment(id int64) (*payRow, error) {
	var p payRow
	err := a.tx.QueryRow(a.ctx, `SELECT id, date, income, income_cat, resident, resident_id, applied FROM club_payments WHERE id = $1`, id).
		Scan(&p.id, &p.date, &p.income, &p.cat, &p.resident, &p.residentID, &p.applied)
	if err != nil {
		return nil, err
	}
	return &p, nil
}

func (a *applier) allocLines(payID int64) ([]AllocLine, error) {
	rows, err := a.tx.Query(a.ctx, `SELECT id, payment_id, kind, COALESCE(fine_id, 0), amount FROM club_pay_alloc WHERE payment_id = $1 ORDER BY id`, payID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []AllocLine
	for rows.Next() {
		var l AllocLine
		if err := rows.Scan(&l.ID, &l.PaymentID, &l.Kind, &l.FineID, &l.Amount); err != nil {
			return nil, err
		}
		out = append(out, l)
	}
	return out, rows.Err()
}

func (a *applier) residentByID(id int64) (*resRow, error) {
	list, err := a.residents()
	if err != nil {
		return nil, err
	}
	for i := range list {
		if list[i].id == id {
			return &list[i], nil
		}
	}
	return nil, fmt.Errorf("резидента #%d нет", id)
}

// payResident: the row's resident by id, else by the name.
func (a *applier) payResident(p *payRow) (*resRow, error) {
	if p.residentID != nil && *p.residentID > 0 {
		if r, err := a.residentByID(*p.residentID); err == nil {
			return r, nil
		}
	}
	if strings.TrimSpace(p.resident) == "" {
		return nil, errors.New("в строке нет резидента")
	}
	return a.matchResident(p.resident)
}

// fineOwner: whether the fine's name is this resident.
func (a *applier) fineOwner(name string, r *resRow) bool {
	if strings.TrimSpace(name) == strings.TrimSpace(r.name) {
		return true
	}
	x, err := a.matchResident(name)
	return err == nil && x.id == r.id
}

type openFine struct {
	id     int64
	date   time.Time
	amount int64
	paid   int64 // by the ledger
}

// openFines: the resident's fines not paid and not written off, oldest
// first, with what the ledger already paid on each.
func (a *applier) openFines(r *resRow) ([]openFine, error) {
	rows, err := a.tx.Query(a.ctx, `SELECT f.id, f.resident, COALESCE(f.date, f.created_at::date), f.amount,
			COALESCE((SELECT sum(amount) FROM club_pay_alloc WHERE fine_id = f.id), 0)
		FROM club_fines f WHERE f.amount > 0 AND f.status NOT IN ('Оплатил', 'Списан') AND NOT f.paid
		ORDER BY COALESCE(f.date, f.created_at::date), f.id`)
	if err != nil {
		return nil, err
	}
	type row struct {
		f    openFine
		name string
	}
	var all []row
	for rows.Next() {
		var x row
		if err := rows.Scan(&x.f.id, &x.name, &x.f.date, &x.f.amount, &x.f.paid); err != nil {
			rows.Close()
			return nil, err
		}
		all = append(all, x)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	var out []openFine
	for _, x := range all {
		if x.f.paid < x.f.amount && a.fineOwner(x.name, r) {
			out = append(out, x.f)
		}
	}
	return out, nil
}

// refreshFine: the fine's status from the ledger: paid in full → «Оплатил»;
// a fine the ledger had paid and no longer does → «Не оплатил». A fine
// written off keeps «Списан».
func (a *applier) refreshFine(id int64) error {
	var amount, paid int64
	var st string
	err := a.tx.QueryRow(a.ctx, `SELECT amount, status, COALESCE((SELECT sum(amount) FROM club_pay_alloc WHERE fine_id = $1), 0)
		FROM club_fines WHERE id = $1`, id).Scan(&amount, &st, &paid)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil || st == "Списан" {
		return err
	}
	switch {
	case paid >= amount && st != "Оплатил":
		return a.update("club_fines", id, `status = 'Оплатил', paid = true, paid_at = COALESCE(paid_at, now())`)
	case paid < amount && st == "Оплатил":
		return a.update("club_fines", id, `status = 'Не оплатил', paid = false, paid_at = NULL`)
	}
	return nil
}

func (a *applier) addLine(payID, resID int64, kind string, fineID, amount int64, by string) (AllocLine, error) {
	var fid any
	if fineID > 0 {
		fid = fineID
	}
	id, err := a.insert("club_pay_alloc", `INSERT INTO club_pay_alloc (payment_id, resident_id, kind, fine_id, amount, by) VALUES ($1,$2,$3,$4,$5,$6)`,
		payID, resID, kind, fid, amount, by)
	return AllocLine{ID: id, PaymentID: payID, Kind: kind, FineID: fineID, Amount: amount, By: by}, err
}

// unallocate takes a row's ledger lines back: the debt it paid owes again,
// the fines it paid are open again (the package it extended stays: the
// meetings are not taken away).
func (a *applier) unallocate(payID int64) ([]AllocLine, error) {
	lines, err := a.allocLines(payID)
	if err != nil || len(lines) == 0 {
		return nil, err
	}
	var resID int64
	_ = a.tx.QueryRow(a.ctx, `SELECT resident_id FROM club_pay_alloc WHERE payment_id = $1 LIMIT 1`, payID).Scan(&resID)
	var fines []int64
	for _, l := range lines {
		if err := a.delete("club_pay_alloc", l.ID); err != nil {
			return nil, err
		}
		switch l.Kind {
		case AllocRest:
			if err := a.update("club_residents", resID, `rest_entry = rest_entry + $2, updated_at = now(), updated_by = 'server'`, l.Amount); err != nil {
				return nil, err
			}
		case AllocRenew:
			if err := a.update("club_residents", resID, `renew_debt = renew_debt + $2, updated_at = now(), updated_by = 'server'`, l.Amount); err != nil {
				return nil, err
			}
		case AllocFine:
			fines = append(fines, l.FineID)
		}
	}
	for _, f := range fines {
		if err := a.refreshFine(f); err != nil {
			return nil, err
		}
	}
	var n int
	if err := a.tx.QueryRow(a.ctx, `SELECT count(*) FROM club_payments WHERE id = $1`, payID).Scan(&n); err == nil && n > 0 {
		if err := a.update("club_payments", payID, `applied = false`); err != nil {
			return nil, err
		}
	}
	return lines, nil
}

// allocate applies the row's remainder (income minus its ledger lines).
// Adding to what it already paid: a fine entered after its payment takes the
// payment's remainder this way.
func (a *applier) allocate(payID int64, by string) (*AllocResult, error) {
	p, err := a.payment(payID)
	if err != nil {
		return nil, err
	}
	res := &AllocResult{PaymentID: p.id, Income: p.income, Kind: PayKind(p.cat), Resident: p.resident}
	lines, err := a.allocLines(p.id)
	if err != nil {
		return nil, err
	}
	for _, l := range lines {
		res.Paid += l.Amount
	}
	res.Lines = lines
	res.Left = p.income - res.Paid
	if p.income <= 0 {
		res.Why = "не приход"
		return res, nil
	}
	if res.Kind == "" {
		res.Why = "разовая услуга, не членство"
		return res, nil
	}
	r, err := a.payResident(p)
	if err != nil {
		res.Why = err.Error()
		return res, nil
	}
	res.Resident, res.ResidentID = r.name, r.id
	left := res.Left
	var added []AllocLine
	if left > 0 && res.Kind == "fine" {
		fs, err := a.openFines(r)
		if err != nil {
			return nil, err
		}
		for _, f := range fs {
			if left <= 0 {
				break
			}
			take := min64(f.amount-f.paid, left)
			l, err := a.addLine(p.id, r.id, AllocFine, f.id, take, by)
			if err != nil {
				return nil, err
			}
			added = append(added, l)
			left -= take
			if err := a.refreshFine(f.id); err != nil {
				return nil, err
			}
		}
		if len(added) == 0 && left > 0 {
			res.Why = "неоплаченных штрафов нет"
		}
	}
	if left > 0 && res.Kind == "debt" {
		var rest, renew int64
		if err := a.tx.QueryRow(a.ctx, `SELECT GREATEST(rest_entry, 0), GREATEST(renew_debt, 0) FROM club_residents WHERE id = $1`, r.id).Scan(&rest, &renew); err != nil {
			return nil, err
		}
		for _, k := range []struct {
			kind string
			due  int64
			col  string
		}{{AllocRest, rest, "rest_entry"}, {AllocRenew, renew, "renew_debt"}} {
			take := min64(k.due, left)
			if take <= 0 {
				continue
			}
			if err := a.update("club_residents", r.id, k.col+` = `+k.col+` - $2, updated_at = now(), updated_by = 'server'`, take); err != nil {
				return nil, err
			}
			l, err := a.addLine(p.id, r.id, k.kind, 0, take, by)
			if err != nil {
				return nil, err
			}
			added = append(added, l)
			left -= take
		}
		if len(added) == 0 && left > 0 {
			res.Why = "долга нет"
		}
	}
	for _, l := range added {
		res.Paid += l.Amount
	}
	res.Lines = append(res.Lines, added...)
	res.Left = left
	if err := a.update("club_payments", p.id, `resident = $2, resident_id = $3, applied = $4`, r.name, r.id, res.Paid > 0); err != nil {
		return nil, err
	}
	return res, nil
}

// reallocate: back, then again (an edited row).
func (a *applier) reallocate(payID int64, by string) (*AllocResult, error) {
	if _, err := a.unallocate(payID); err != nil {
		return nil, err
	}
	return a.allocate(payID, by)
}

// allocFrom: rows dated from this day on are applied by the server; the
// older ones are the sheet's history (the sheet's balances have them). The
// day of the last import from the sheet (the cutover). server: the server
// keeps the club's data (while the sheet does, every row is applied as it
// is entered and the next import replaces the tables anyway).
func (a *applier) allocFrom() (time.Time, bool) {
	var m string
	_ = a.tx.QueryRow(a.ctx, `SELECT value FROM club_meta WHERE key = 'master'`).Scan(&m)
	early := time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC)
	if m != "server" {
		return early, false
	}
	var t *time.Time
	_ = a.tx.QueryRow(a.ctx, `SELECT max(at) FROM club_imports WHERE NOT dry_run`).Scan(&t)
	if t == nil {
		return early, true
	}
	x := t.In(club.Almaty)
	return time.Date(x.Year(), x.Month(), x.Day(), 0, 0, 0, 0, time.UTC), true
}

// autoAllocate: a new or edited row is applied when it is the server's
// (dated from the cutover on) or already has ledger lines.
func (a *applier) autoAllocate(payID int64, changed bool, by string) (*AllocResult, error) {
	p, err := a.payment(payID)
	if err != nil {
		return nil, err
	}
	lines, err := a.allocLines(payID)
	if err != nil {
		return nil, err
	}
	from, _ := a.allocFrom()
	if len(lines) == 0 && p.date.Format("2006-01-02") < from.Format("2006-01-02") {
		return nil, nil
	}
	if len(lines) == 0 && p.applied {
		return nil, nil // counted by the sheet's script: history
	}
	if changed {
		return a.reallocate(payID, by)
	}
	return a.allocate(payID, by)
}

// fineTookPayment: a fine just entered takes the remainder of the
// resident's earlier fine payments (paid before the fine was entered).
func (a *applier) finePrepaid(name string) error {
	r, err := a.matchResident(name)
	if err != nil {
		return nil
	}
	rows, err := a.tx.Query(a.ctx, `SELECT p.id, p.income_cat, p.income - COALESCE((SELECT sum(amount) FROM club_pay_alloc WHERE payment_id = p.id), 0)
		FROM club_payments p WHERE p.income > 0 AND (p.resident_id = $1 OR p.resident_id IS NULL) ORDER BY p.date, p.id`, r.id)
	if err != nil {
		return err
	}
	type c struct {
		id   int64
		cat  string
		left int64
	}
	var cand []c
	for rows.Next() {
		var x c
		if err := rows.Scan(&x.id, &x.cat, &x.left); err != nil {
			rows.Close()
			return err
		}
		if x.left > 0 && PayKind(x.cat) == "fine" {
			cand = append(cand, x)
		}
	}
	rows.Close()
	from, _ := a.allocFrom()
	for _, x := range cand {
		p, err := a.payment(x.id)
		if err != nil {
			return err
		}
		if p.date.Format("2006-01-02") < from.Format("2006-01-02") {
			continue
		}
		if who, err := a.payResident(p); err != nil || who.id != r.id {
			continue
		}
		if _, err := a.allocate(x.id, "server"); err != nil {
			return err
		}
	}
	return nil
}

// fineGone: a deleted or written off fine gives its payments back; they pay
// the resident's other open fines, the rest stays their remainder.
func (a *applier) fineGone(fineID int64) error {
	rows, err := a.tx.Query(a.ctx, `SELECT id, payment_id FROM club_pay_alloc WHERE fine_id = $1`, fineID)
	if err != nil {
		return err
	}
	var ids, pays []int64
	for rows.Next() {
		var id, p int64
		if err := rows.Scan(&id, &p); err != nil {
			rows.Close()
			return err
		}
		ids = append(ids, id)
		pays = append(pays, p)
	}
	rows.Close()
	for _, id := range ids {
		if err := a.delete("club_pay_alloc", id); err != nil {
			return err
		}
	}
	for _, p := range pays {
		if _, err := a.allocate(p, "server"); err != nil {
			return err
		}
	}
	return nil
}

// ── Repository calls ──

// LinkPayment («Привязать платёж»): the row belongs to this resident (by
// id); what it paid before is taken back and it is applied again. A name
// that did not match becomes the resident's alias, so the next payment with
// it is found by itself.
func (r *ClubRepo) LinkPayment(ctx context.Context, id int64, name string) (*LinkResult, error) {
	tx, err := r.db.Pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	a := &applier{ctx: ctx, tx: tx, at: time.Now()}
	p, err := a.payment(id)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, &ErrLinkInput{"строку ДДС уже удалили, обновите страницу"}
	}
	if err != nil {
		return nil, err
	}
	if p.income <= 0 {
		return nil, &ErrLinkInput{"это не приход"}
	}
	res, err := a.matchResident(name)
	if err != nil {
		return nil, &ErrLinkInput{err.Error()}
	}
	lines, err := a.allocLines(id)
	if err != nil {
		return nil, err
	}
	if len(lines) > 0 && p.residentID != nil && *p.residentID == res.id {
		return nil, &ErrLinkInput{"эта оплата уже учтена у " + res.name}
	}
	if _, err := a.unallocate(id); err != nil {
		return nil, err
	}
	typed := strings.TrimSpace(p.resident)
	if typed != "" && club.NormName(typed) != club.NormName(res.name) {
		if other, err := a.matchResident(typed); err != nil || other.id != res.id {
			if !hasAlias(res.aliases, club.NormName(typed)) {
				al := strings.Trim(res.aliases+","+typed, ",")
				if err := a.update("club_residents", res.id, `aliases = $2`, al); err != nil {
					return nil, err
				}
			}
		}
	}
	if err := a.update("club_payments", id, `resident = $2, resident_id = $3`, res.name, res.id); err != nil {
		return nil, err
	}
	out, err := a.allocate(id, "link")
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return &LinkResult{Resident: res.name, Paid: out.Paid, Applied: out.Paid > 0, Left: out.Left, Kind: out.Kind, Why: out.Why}, nil
}

// WriteOffFine («Списать штраф»): the fine is no longer owed, with the
// reason; its payments go to the other open fines.
func (r *ClubRepo) WriteOffFine(ctx context.Context, id int64, reason, who string) error {
	tx, err := r.db.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	a := &applier{ctx: ctx, tx: tx, at: time.Now()}
	var st string
	if err := tx.QueryRow(ctx, `SELECT status FROM club_fines WHERE id = $1`, id).Scan(&st); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return &ErrLinkInput{"штраф уже удалили, обновите страницу"}
		}
		return err
	}
	if st == "Списан" {
		return &ErrLinkInput{"штраф уже списан"}
	}
	note := "Списан " + time.Now().In(club.Almaty).Format("02.01.2006")
	if who != "" {
		note += " (" + who + ")"
	}
	note += ": " + reason
	if err := a.update("club_fines", id, `status = 'Списан', paid = false, note = $2`, note); err != nil {
		return err
	}
	if err := a.fineGone(id); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// ── Backfill ──

// AllocChange: one resident's balances before and after the backfill.
type AllocChange struct {
	Resident                   string
	DebtBefore, DebtAfter      int64
	FinesBefore, FinesAfter    int64
	Rows                       []string
}

// AllocBackfill applies every income row from the cutover on that has no
// ledger lines: a row the server already counted (R59/R63, «учтено») gets
// its line written as it was (the balances already have it); an open row is
// applied unless someone set that resident's debt by hand after the
// payment's date (then it is only reported). dry: nothing is kept.
func (r *ClubRepo) AllocBackfill(ctx context.Context, dry bool) ([]AllocChange, []string, error) {
	tx, err := r.db.Pool.Begin(ctx)
	if err != nil {
		return nil, nil, err
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	a := &applier{ctx: ctx, tx: tx, at: time.Now()}
	from, server := a.allocFrom()
	if !server {
		return nil, []string{"данные клуба ещё в таблице: нечего разносить"}, nil
	}
	notes := []string{"разнос оплат с " + from.Format("02.01.2006")}
	type bal struct{ debt, fines int64 }
	balances := func() (map[int64]bal, map[int64]string, error) {
		out, names := map[int64]bal{}, map[int64]string{}
		rows, err := tx.Query(ctx, `SELECT id, name, GREATEST(rest_entry,0) + GREATEST(renew_debt,0) FROM club_residents WHERE NOT archived`)
		if err != nil {
			return nil, nil, err
		}
		for rows.Next() {
			var id, d int64
			var n string
			if err := rows.Scan(&id, &n, &d); err != nil {
				rows.Close()
				return nil, nil, err
			}
			out[id], names[id] = bal{debt: d}, n
		}
		rows.Close()
		list, err := a.residents()
		if err != nil {
			return nil, nil, err
		}
		for i := range list {
			if list[i].archive {
				continue
			}
			fs, err := a.openFines(&list[i])
			if err != nil {
				return nil, nil, err
			}
			b := out[list[i].id]
			for _, f := range fs {
				b.fines += f.amount - f.paid
			}
			out[list[i].id] = b
		}
		return out, names, nil
	}
	before, names, err := balances()
	if err != nil {
		return nil, nil, err
	}
	rows, err := tx.Query(ctx, `SELECT p.id FROM club_payments p WHERE p.income > 0 AND p.date >= $1::date
		AND NOT EXISTS (SELECT 1 FROM club_pay_alloc x WHERE x.payment_id = p.id) ORDER BY p.date, p.id`, from.Format("2006-01-02"))
	if err != nil {
		return nil, nil, err
	}
	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return nil, nil, err
		}
		ids = append(ids, id)
	}
	rows.Close()
	perRes := map[int64][]string{}
	for _, id := range ids {
		p, err := a.payment(id)
		if err != nil {
			return nil, nil, err
		}
		desc := fmt.Sprintf("%s %s ₸ «%s»", p.date.Format("02.01"), club.FmtMoney(p.income), p.cat)
		kind := PayKind(p.cat)
		if kind == "" {
			notes = append(notes, desc+" «"+p.resident+"»: разовая услуга, не членство")
			continue
		}
		res, err := a.payResident(p)
		if err != nil {
			notes = append(notes, desc+" «"+p.resident+"»: "+err.Error()+" (остаток, «Привязать платёж»)")
			continue
		}
		if p.applied {
			// counted before the ledger (R59/R63): the line as it was, balances untouched
			if _, err := a.addLine(p.id, res.id, AllocRenew, 0, p.income, "r69-legacy"); err != nil {
				return nil, nil, err
			}
			if err := a.update("club_payments", p.id, `resident_id = $2`, res.id); err != nil {
				return nil, nil, err
			}
			perRes[res.id] = append(perRes[res.id], desc+": уже был учтён в долге, записан в журнал")
			continue
		}
		if kind == "debt" {
			edited, err := a.debtEditedSince(res.name, p.date)
			if err != nil {
				return nil, nil, err
			}
			if edited {
				notes = append(notes, desc+" «"+res.name+"»: долг правили вручную после оплаты, не разнесено")
				continue
			}
		}
		out, err := a.allocate(p.id, "r69-backfill")
		if err != nil {
			return nil, nil, err
		}
		line := fmt.Sprintf("%s: разнесено %s ₸", desc, club.FmtMoney(out.Paid))
		if out.Left > 0 {
			line += fmt.Sprintf(", остаток %s ₸", club.FmtMoney(out.Left))
			if out.Why != "" {
				line += " (" + out.Why + ")"
			}
		}
		perRes[res.id] = append(perRes[res.id], line)
	}
	after, _, err := balances()
	if err != nil {
		return nil, nil, err
	}
	var ch []AllocChange
	for id, lines := range perRes {
		ch = append(ch, AllocChange{Resident: names[id], DebtBefore: before[id].debt, DebtAfter: after[id].debt,
			FinesBefore: before[id].fines, FinesAfter: after[id].fines, Rows: lines})
	}
	sort.Slice(ch, func(i, j int) bool { return ch[i].Resident < ch[j].Resident })
	if dry {
		return ch, notes, nil
	}
	if _, err := tx.Exec(ctx, `INSERT INTO club_meta (key, value) VALUES ('r69_alloc', $1)
		ON CONFLICT (key) DO UPDATE SET value = EXCLUDED.value`, "done:"+time.Now().UTC().Format(time.RFC3339)); err != nil {
		return nil, nil, err
	}
	return ch, notes, tx.Commit(ctx)
}

// AllocBackfillDone: the backfill has been applied.
func (r *ClubRepo) AllocBackfillDone(ctx context.Context) bool {
	var v string
	_ = r.db.Pool.QueryRow(ctx, `SELECT value FROM club_meta WHERE key = 'r69_alloc'`).Scan(&v)
	return strings.HasPrefix(v, "done:")
}
