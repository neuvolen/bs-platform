package pg

import (
	"context"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
)

// R69: «Учёт → Долги и штрафы»: per resident the debt, the fines (open with
// what is paid on each, paid, written off), the payments with what each paid
// (the ledger) and the remainder; the income rows nobody's (to link).

// DebtFine is one fine in the report.
type DebtFine struct {
	ID     int64  `json:"id"`
	Date   string `json:"date"` // YYYY-MM-DD
	Type   string `json:"type"`
	Amount int64  `json:"amount"`
	Paid   int64  `json:"paid"` // by the ledger
	Left   int64  `json:"left"`
	Status string `json:"status"` // Не оплатил | Оплатил | Списан
	Note   string `json:"note,omitempty"`
}

// DebtPayment is one income row of the resident with its ledger lines.
type DebtPayment struct {
	ID      int64       `json:"id"`
	Date    string      `json:"date"`
	Amount  int64       `json:"amount"`
	Cat     string      `json:"cat"`
	Kind    string      `json:"kind"` // fine | debt | "" (one-off)
	Lines   []AllocLine `json:"lines"`
	Paid    int64       `json:"paid"`
	Left    int64       `json:"left"`
	Legacy  bool        `json:"legacy,omitempty"` // «учтено» by the sheet's script, before the ledger
	Account string      `json:"account,omitempty"`
}

// DebtRow is one resident.
type DebtRow struct {
	ID         int64         `json:"id"`
	Name       string        `json:"name"`
	Format     string        `json:"format"`
	Former     bool          `json:"former,omitempty"`
	Tariff     int64         `json:"tariff"`
	Rest       int64         `json:"rest"`  // остаток входа
	Renew      int64         `json:"renew"` // долг продления
	Debt       int64         `json:"debt"`
	FinesOpen  int64         `json:"finesOpen"`
	FinesPaid  int64         `json:"finesPaid"`
	Total      int64         `json:"total"` // debt + open fines
	PaidTotal  int64         `json:"paidTotal"`
	Left       int64         `json:"left"` // remainders of the payments
	Fines      []DebtFine    `json:"fines"`
	Payments   []DebtPayment `json:"payments"`
	LastPayDay string        `json:"lastPayDay,omitempty"`
	LastPaySum int64         `json:"lastPaySum,omitempty"`
	// R70: «Проверить»: why the debt does not fit the payments; the debt's
	// changes with their reasons (newest first)
	Check   []string       `json:"check,omitempty"`
	History []BalanceEvent `json:"history,omitempty"`
}

// DebtsReport is the tab's data.
type DebtsReport struct {
	From      string        `json:"from"` // rows from this day are applied by the server
	Residents []DebtRow     `json:"residents"`
	Unlinked  []DebtPayment `json:"unlinked"` // income rows with a remainder and no resident
	FineTypes []string      `json:"fineTypes"`
}

// Debts builds the report (read only).
func (r *ClubRepo) Debts(ctx context.Context) (*DebtsReport, error) { return r.debtsCached(ctx, true) }

// DebtsLite is the same ledger without the payments and the history: what
// the Telegram app shows (debts, fines with what is paid on each), the
// numbers of «Учёт → Долги и штрафы» to the tenge.
func (r *ClubRepo) DebtsLite(ctx context.Context) (*DebtsReport, error) { return r.debtsCached(ctx, false) }

// R82: «Долги и штрафы почему-то подгружаются дольше». The report is kept as
// a snapshot per database and kind, keyed by a fingerprint of the tables it
// is built from (row counts and the sum of the row versions of each, read in the
// report's own snapshot): any insert, update or delete, by whoever (the tab,
// the bot, the Telegram app, a sheet import), changes it and the next open
// rebuilds. Callers treat the report as read only.
type debtsSnap struct {
	key string
	rep *DebtsReport
}

var (
	debtsMu    sync.Mutex
	debtsCache = map[*DB]map[bool]debtsSnap{}
)

const debtsKeySQL = `SELECT concat_ws('|',
	(SELECT count(*) || '.' || COALESCE(sum(xmin::text::bigint), 0) FROM club_residents),
	(SELECT count(*) || '.' || COALESCE(sum(xmin::text::bigint), 0) FROM club_fines),
	(SELECT count(*) || '.' || COALESCE(sum(xmin::text::bigint), 0) FROM club_payments),
	(SELECT count(*) || '.' || COALESCE(sum(xmin::text::bigint), 0) FROM club_pay_alloc),
	(SELECT count(*) || '.' || COALESCE(max(id), 0) FROM club_balance_log),
	(SELECT value FROM club_meta WHERE key = 'master'),
	(SELECT max(at)::text FROM club_imports WHERE NOT dry_run))`

func (r *ClubRepo) debtsCached(ctx context.Context, full bool) (*DebtsReport, error) {
	// one snapshot for the fingerprint and the report: a write committed in
	// between is in neither, and the next open sees a new fingerprint
	tx, err := r.db.Pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	var key string
	if err := tx.QueryRow(ctx, debtsKeySQL).Scan(&key); err != nil {
		return nil, err
	}
	debtsMu.Lock()
	snap, ok := debtsCache[r.db][full]
	debtsMu.Unlock()
	if ok && snap.key == key {
		return snap.rep, nil
	}
	rep, err := debtsBuild(ctx, tx, full)
	if err != nil {
		return nil, err
	}
	debtsMu.Lock()
	if debtsCache[r.db] == nil {
		debtsCache[r.db] = map[bool]debtsSnap{}
	}
	debtsCache[r.db][full] = debtsSnap{key: key, rep: rep}
	debtsMu.Unlock()
	return rep, nil
}

func debtsBuild(ctx context.Context, tx pgx.Tx, full bool) (*DebtsReport, error) {
	a := &applier{ctx: ctx, tx: tx, at: time.Now()}
	from, _ := a.allocFrom()
	out := &DebtsReport{From: from.Format("2006-01-02"), Residents: []DebtRow{}, Unlinked: []DebtPayment{}, FineTypes: []string{}}
	list, err := a.residents()
	if err != nil {
		return nil, err
	}
	a.resCache = list
	// the money columns of everyone at once (was a query per resident)
	type money struct {
		admin       bool
		rest, renew int64
	}
	mon := map[int64]money{}
	mrows, err := tx.Query(ctx, `SELECT id, admin, rest_entry, renew_debt FROM club_residents`)
	if err != nil {
		return nil, err
	}
	for mrows.Next() {
		var id int64
		var m money
		if err := mrows.Scan(&id, &m.admin, &m.rest, &m.renew); err != nil {
			mrows.Close()
			return nil, err
		}
		mon[id] = m
	}
	mrows.Close()
	if err := mrows.Err(); err != nil {
		return nil, err
	}
	byID := map[int64]*DebtRow{}
	var order []int64
	for _, x := range list {
		if x.archive {
			continue
		}
		m := mon[x.id]
		admin, rest, renew := m.admin, m.rest, m.renew
		if admin {
			continue
		}
		if rest < 0 {
			rest = 0
		}
		if renew < 0 {
			renew = 0
		}
		byID[x.id] = &DebtRow{ID: x.id, Name: x.name, Format: x.format, Former: x.former, Tariff: x.tariff, Rest: rest, Renew: renew,
			Debt: rest + renew, Fines: []DebtFine{}, Payments: []DebtPayment{}}
		order = append(order, x.id)
	}
	byName := map[string]int64{} // the names already matched (0: nobody's)
	owner := func(name string, id *int64) *DebtRow {
		if id != nil && byID[*id] != nil {
			return byID[*id]
		}
		if strings.TrimSpace(name) == "" {
			return nil
		}
		rid, ok := byName[name]
		if !ok {
			if x, err := a.matchResident(name); err == nil {
				rid = x.id
			}
			byName[name] = rid
		}
		return byID[rid]
	}

	// fines
	rows, err := tx.Query(ctx, `SELECT f.id, f.resident, COALESCE(f.date, f.created_at::date), f.type, f.amount, f.status, f.paid, f.note,
			COALESCE((SELECT sum(amount) FROM club_pay_alloc WHERE fine_id = f.id), 0)
		FROM club_fines f ORDER BY COALESCE(f.date, f.created_at::date), f.id`)
	if err != nil {
		return nil, err
	}
	type fr struct {
		f    DebtFine
		name string
		paid bool
	}
	var fines []fr
	types := map[string]bool{}
	for rows.Next() {
		var x fr
		var d time.Time
		if err := rows.Scan(&x.f.ID, &x.name, &d, &x.f.Type, &x.f.Amount, &x.f.Status, &x.paid, &x.f.Note, &x.f.Paid); err != nil {
			rows.Close()
			return nil, err
		}
		x.f.Date = d.Format("2006-01-02")
		fines = append(fines, x)
		if t := strings.TrimSpace(x.f.Type); t != "" {
			types[t] = true
		}
	}
	rows.Close()
	for _, x := range fines {
		d := owner(x.name, nil)
		if d == nil {
			continue
		}
		f := x.f
		switch {
		case f.Status == "Списан":
		case f.Status == "Оплатил" || x.paid:
			f.Status = "Оплатил"
			d.FinesPaid += f.Amount
		default:
			f.Status = "Не оплатил"
			f.Left = f.Amount - f.Paid
			if f.Left < 0 {
				f.Left = 0
			}
			d.FinesOpen += f.Left
			d.FinesPaid += f.Paid
		}
		d.Fines = append(d.Fines, f)
	}

	if !full {
		for _, id := range order {
			d := byID[id]
			d.Total = d.Debt + d.FinesOpen
			out.Residents = append(out.Residents, *d)
		}
		return out, nil
	}

	// payments with their lines
	lines := map[int64][]AllocLine{}
	rows, err = tx.Query(ctx, `SELECT id, payment_id, kind, COALESCE(fine_id, 0), amount, at, by FROM club_pay_alloc ORDER BY id`)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var l AllocLine
		var at time.Time
		if err := rows.Scan(&l.ID, &l.PaymentID, &l.Kind, &l.FineID, &l.Amount, &at, &l.By); err != nil {
			rows.Close()
			return nil, err
		}
		l.At = at.Format(time.RFC3339)
		lines[l.PaymentID] = append(lines[l.PaymentID], l)
	}
	rows.Close()
	rows, err = tx.Query(ctx, `SELECT id, date, income, income_cat, resident, resident_id, applied, account FROM club_payments WHERE income > 0 ORDER BY date, id`)
	if err != nil {
		return nil, err
	}
	type pr struct {
		p    DebtPayment
		name string
		rid  *int64
	}
	var pays []pr
	for rows.Next() {
		var x pr
		var d time.Time
		var applied bool
		if err := rows.Scan(&x.p.ID, &d, &x.p.Amount, &x.p.Cat, &x.name, &x.rid, &applied, &x.p.Account); err != nil {
			rows.Close()
			return nil, err
		}
		x.p.Date = d.Format("2006-01-02")
		x.p.Kind = PayKind(x.p.Cat)
		x.p.Lines = lines[x.p.ID]
		if x.p.Lines == nil {
			x.p.Lines = []AllocLine{}
		}
		for _, l := range x.p.Lines {
			x.p.Paid += l.Amount
		}
		x.p.Left = x.p.Amount - x.p.Paid
		if applied && len(x.p.Lines) == 0 {
			x.p.Legacy, x.p.Left = true, 0
		}
		pays = append(pays, x)
	}
	rows.Close()
	for _, x := range pays {
		d := owner(x.name, x.rid)
		old := x.p.Date < out.From
		if d == nil {
			// a club income nobody's, from the cutover on: to link
			if !old && x.p.Kind != "" && x.p.Left > 0 && !x.p.Legacy && (strings.TrimSpace(x.name) != "" || x.p.Kind == "fine" || debtCat(x.p.Cat)) {
				p := x.p
				p.Cat = x.p.Cat
				if strings.TrimSpace(x.name) != "" {
					p.Cat += " · " + strings.TrimSpace(x.name)
				}
				out.Unlinked = append(out.Unlinked, p)
			}
			continue
		}
		if x.p.Kind == "" {
			continue // one-off services are not the resident's club money
		}
		p := x.p
		if old && len(p.Lines) == 0 {
			p.Left = 0 // the sheet's history
		}
		d.PaidTotal += p.Amount
		d.Left += p.Left
		d.Payments = append(d.Payments, p)
		if p.Date >= d.LastPayDay {
			d.LastPayDay, d.LastPaySum = p.Date, p.Amount
		}
	}
	// R70: the history of the debt, the last 12 changes of everyone in one query.
	// R82: the audit («Проверить») is in the server logs only (ClubR70AtStart)
	hist := map[int64][]BalanceEvent{}
	hrows, err := tx.Query(ctx, `SELECT resident_id, id, name, at, before, after, reason FROM (
			SELECT resident_id, id, name, at, GREATEST(COALESCE(rest_before,0),0) + GREATEST(COALESCE(renew_before,0),0) AS before,
				GREATEST(COALESCE(rest_after,0),0) + GREATEST(COALESCE(renew_after,0),0) AS after, reason,
				row_number() OVER (PARTITION BY resident_id ORDER BY id DESC) AS n
			FROM club_balance_log WHERE resident_id IS NOT NULL) h
		WHERE n <= 12 ORDER BY resident_id, id DESC`)
	if err != nil {
		return nil, err
	}
	for hrows.Next() {
		var rid int64
		var e BalanceEvent
		var at time.Time
		if err := hrows.Scan(&rid, &e.ID, &e.Resident, &at, &e.Before, &e.After, &e.Reason); err != nil {
			hrows.Close()
			return nil, err
		}
		e.At = at.UTC().Format(time.RFC3339)
		hist[rid] = append(hist[rid], e)
	}
	hrows.Close()
	if err := hrows.Err(); err != nil {
		return nil, err
	}
	for _, id := range order {
		d := byID[id]
		if h := hist[id]; len(h) > 0 {
			d.History = h
		}
		d.Total = d.Debt + d.FinesOpen
		sort.Slice(d.Payments, func(i, j int) bool { return d.Payments[i].Date > d.Payments[j].Date })
		if len(d.Payments) > 40 {
			d.Payments = d.Payments[:40]
		}
		out.Residents = append(out.Residents, *d)
	}
	sort.Slice(out.Unlinked, func(i, j int) bool { return out.Unlinked[i].Date > out.Unlinked[j].Date })
	for t := range types {
		out.FineTypes = append(out.FineTypes, t)
	}
	sort.Strings(out.FineTypes)
	return out, nil
}
