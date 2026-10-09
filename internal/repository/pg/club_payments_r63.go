package pg

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/bnursik/business_surgery_backend/internal/club"
)

// R63: «Альтаир: реши сам, непонятно, куда нажимать». R59 made a new
// payment pay off the debt, but the payments entered between the cutover and
// R59 stayed «не учтено» and needed «Учесть в долге» by hand. At the start
// the server counts them itself, once per payment (the row becomes «учтено»,
// so a second start finds nothing): a resident's payment that clearly pays
// the club (a tracking, renewal or entry category) for a resident who owes,
// with no manual edit of that resident's debt after the payment's date.
// Anything else is only reported (log and the resident's «Оплаты»).

// DebtPay: one payment the start looked at.
type DebtPay struct {
	ID       int64
	Date     string // YYYY-MM-DD
	Resident string // the resident it was counted for, or the name in the ДДС
	Cat      string
	Amount   int64
	Paid     int64 // went to the debt
	Before   int64 // the resident's debt (entry rest + renewal) before
	After    int64
	Why      string // not counted: why
}

// debtCat: a category that pays the club membership (not a fine, not a
// one-off service).
func debtCat(cat string) bool {
	c := strings.ToLower(cat)
	if strings.Contains(c, "штраф") || club.NonMembershipCat(cat) {
		return false
	}
	for _, k := range []string{"трекинг", "продлен", "вход", "членск", "резидент"} {
		if strings.Contains(c, k) {
			return true
		}
	}
	return false
}

// debtOf: the resident's debt now.
func (a *applier) debtOf(id int64) (int64, error) {
	var rest, renew int64
	if err := a.tx.QueryRow(a.ctx, `SELECT GREATEST(rest_entry, 0), GREATEST(renew_debt, 0) FROM club_residents WHERE id = $1`, id).Scan(&rest, &renew); err != nil {
		return 0, err
	}
	return rest + renew, nil
}

// debtEditedSince: someone set the resident's debt by hand (the audit's
// fix or the resident card) on or after that day.
func (a *applier) debtEditedSince(name string, day time.Time) (bool, error) {
	rows, err := a.tx.Query(a.ctx, `SELECT COALESCE(params->>'name', '') FROM club_writes
		WHERE applied AND action = 'setResidentField' AND params->>'field' IN ('restEntry', 'renewDebt') AND at >= $1`, day)
	if err != nil {
		return false, err
	}
	defer rows.Close()
	want := club.NormName(name)
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			return false, err
		}
		if club.NormName(n) == want {
			return true, nil
		}
	}
	return false, rows.Err()
}

// countPayment counts one payment against its resident's debt.
func (a *applier) countPayment(p *DebtPay, r *resRow) error {
	before, err := a.debtOf(r.id)
	if err != nil {
		return err
	}
	paid, err := a.payDebt(p.ID, r, p.Amount)
	if err != nil {
		return err
	}
	after, err := a.debtOf(r.id)
	if err != nil {
		return err
	}
	p.Resident, p.Paid, p.Before, p.After = r.name, paid, before, after
	return nil
}

// PayAltairOct7: the one payment the owner named: Альтаир, 07.10.2026,
// 50 000 ₸ income. Counted once; nil when there is no such row or it is
// already «учтено» (then the second value says so).
func (r *ClubRepo) PayAltairOct7(ctx context.Context) (*DebtPay, string, error) {
	tx, err := r.db.Pool.Begin(ctx)
	if err != nil {
		return nil, "", err
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	a := &applier{ctx: ctx, tx: tx, at: time.Now()}
	rows, err := tx.Query(ctx, `SELECT id, resident, income_cat, applied FROM club_payments
		WHERE date = '2026-10-07' AND income = 50000 ORDER BY id FOR UPDATE`)
	if err != nil {
		return nil, "", err
	}
	type row struct {
		id      int64
		res     string
		cat     string
		applied bool
	}
	var hit []row
	for rows.Next() {
		var x row
		if err := rows.Scan(&x.id, &x.res, &x.cat, &x.applied); err != nil {
			rows.Close()
			return nil, "", err
		}
		if strings.HasPrefix(club.NormName(x.res), "альтаир") {
			hit = append(hit, x)
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, "", err
	}
	if len(hit) == 0 {
		return nil, "оплаты Альтаир 07.10.2026 на 50 000 ₸ в ДДС нет", nil
	}
	x := hit[0]
	res, err := a.matchResident(x.res)
	if err != nil {
		return nil, "", fmt.Errorf("резидент: %v", err)
	}
	if x.applied {
		d, err := a.debtOf(res.id)
		if err != nil {
			return nil, "", err
		}
		return nil, fmt.Sprintf("уже учтена, долг %s сейчас %d ₸", res.name, d), nil
	}
	p := &DebtPay{ID: x.id, Date: "2026-10-07", Cat: x.cat, Amount: 50000}
	if err := a.countPayment(p, res); err != nil {
		return nil, "", err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, "", err
	}
	return p, "", nil
}

// AutoCountPayments: the residents' payments since the cutover that are
// still «не учтено»: counted when it is clear, reported otherwise.
func (r *ClubRepo) AutoCountPayments(ctx context.Context, since time.Time) (done, left []DebtPay, err error) {
	tx, err := r.db.Pool.Begin(ctx)
	if err != nil {
		return nil, nil, err
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	a := &applier{ctx: ctx, tx: tx, at: time.Now()}
	rows, err := tx.Query(ctx, `SELECT id, date, income, income_cat, resident FROM club_payments
		WHERE income > 0 AND NOT applied AND date >= $1::date ORDER BY date, id FOR UPDATE`, since.Format("2006-01-02"))
	if err != nil {
		return nil, nil, err
	}
	var all []DebtPay
	for rows.Next() {
		var p DebtPay
		var day time.Time
		if err := rows.Scan(&p.ID, &day, &p.Amount, &p.Cat, &p.Resident); err != nil {
			rows.Close()
			return nil, nil, err
		}
		p.Date = day.Format("2006-01-02")
		all = append(all, p)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, nil, err
	}
	for i := range all {
		p := all[i]
		if strings.Contains(strings.ToLower(p.Cat), "штраф") {
			continue // a fine's payment closes fines, not the debt
		}
		if club.NonMembershipCat(p.Cat) {
			continue // R67: an express review or a master class is not membership: not reported as «не учтено»
		}
		if strings.TrimSpace(p.Resident) == "" {
			if debtCat(p.Cat) {
				p.Why = "в ДДС нет имени резидента"
				left = append(left, p)
			}
			continue
		}
		res, err := a.matchResident(p.Resident)
		if err != nil {
			p.Why = err.Error()
			left = append(left, p)
			continue
		}
		p.Resident = res.name
		if !debtCat(p.Cat) {
			p.Why = "категория «" + p.Cat + "» не про членство"
			left = append(left, p)
			continue
		}
		debt, err := a.debtOf(res.id)
		if err != nil {
			return nil, nil, err
		}
		if debt <= 0 {
			p.Why = "долга нет"
			left = append(left, p)
			continue
		}
		day, _ := time.Parse("2006-01-02", p.Date)
		edited, err := a.debtEditedSince(res.name, day)
		if err != nil {
			return nil, nil, err
		}
		if edited {
			p.Why = "долг правили вручную после оплаты"
			left = append(left, p)
			continue
		}
		if err := a.countPayment(&p, res); err != nil {
			return nil, nil, err
		}
		done = append(done, p)
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, nil, err
	}
	return done, left, nil
}
