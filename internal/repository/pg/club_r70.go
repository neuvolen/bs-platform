package pg

import (
	"context"
	"errors"
	"fmt"
	"log"
	"sort"
	"strings"
	"time"

	"github.com/bnursik/business_surgery_backend/internal/club"
	"github.com/jackc/pgx/v5"
)

// R70: «Почему у Альтаира опять стоит, что он должен 100к? Он должен 100к
// штрафа, и всё. Хватит эту проблему делать».
//
// What happened: the sheet closed Альтаир's package at 3/3 and charged the
// renewal (100 000) before the cutover; the counter stayed at 3/3. He paid
// that renewal on 07.10 (2 × 50 000, debt 0). His next meeting (09.10) made
// the counter 4/3, and the R59 rule «3/3 → the tariff into the renewal debt»
// charged the same package a second time: 100 000 again.
//
// The fix, for everyone:
//   - a package is charged once: only the meeting that makes it exactly
//     full charges the tariff. A meeting on a counter already full opens the
//     next package without a charge when that package is owed already or was
//     paid in the last 35 days (the sheet charged it);
//   - a charge is paid at once by the resident's prepayments (membership
//     income with a remainder: paid before the debt existed);
//   - «Установить долг» (club_debt_adjust): the team sets the debt with the
//     reason; payment lines written before it never bring the old debt back;
//   - every change of a debt goes to club_balance_log with its reason (a
//     trigger, whoever writes), printed at start and shown in the tab;
//   - the audit flags debts that do not fit the payments («Проверить»).

// why: the reason for the debt changes that follow in this transaction
// (club_balance_log).
func (a *applier) why(s string) error {
	_, err := a.tx.Exec(a.ctx, `SELECT set_config('bs.reason', $1, true)`, s)
	return err
}

// lastAdjust: when the team last set this resident's debt by hand.
func (a *applier) lastAdjust(resID int64) (time.Time, bool) {
	var t *time.Time
	_ = a.tx.QueryRow(a.ctx, `SELECT max(at) FROM club_debt_adjust WHERE resident_id = $1`, resID).Scan(&t)
	if t == nil {
		return time.Time{}, false
	}
	return *t, true
}

// resPay is one income row of the resident with its remainder.
type resPay struct {
	id     int64
	date   time.Time
	income int64
	cat    string
	left   int64
	lines  int
	legacy bool // «учтено» by the sheet's script, no ledger lines
}

// payRaw is one income row as read for residentPays.
type payRaw struct {
	p     resPay
	name  string
	resID *int64
}

func (a *applier) payRows() ([]payRaw, error) {
	if a.payCache != nil {
		return a.payCache, nil
	}
	rows, err := a.tx.Query(a.ctx, `SELECT p.id, p.date, p.income, p.income_cat, p.resident, p.resident_id, p.applied,
			p.income - COALESCE((SELECT sum(amount) FROM club_pay_alloc WHERE payment_id = p.id), 0),
			(SELECT count(*) FROM club_pay_alloc WHERE payment_id = p.id)
		FROM club_payments p WHERE p.income > 0 ORDER BY p.date, p.id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	all := []payRaw{}
	for rows.Next() {
		var x payRaw
		var applied bool
		if err := rows.Scan(&x.p.id, &x.p.date, &x.p.income, &x.p.cat, &x.name, &x.resID, &applied, &x.p.left, &x.p.lines); err != nil {
			return nil, err
		}
		x.p.legacy = applied && x.p.lines == 0
		all = append(all, x)
	}
	return all, rows.Err()
}

// cached: a read-only report reads the income rows and matches the names once.
func (a *applier) cached() error {
	all, err := a.payRows()
	if err != nil {
		return err
	}
	a.payCache = all
	if a.matchCache == nil {
		a.matchCache = map[string]int64{}
	}
	return nil
}

// residentPays: the resident's income rows (by id, else by name or alias),
// oldest first.
func (a *applier) residentPays(r *resRow) ([]resPay, error) {
	all, err := a.payRows()
	if err != nil {
		return nil, err
	}
	names := residentNames(r)
	cache := a.matchCache
	if cache == nil {
		cache = map[string]int64{}
	}
	var out []resPay
	for _, x := range all {
		mine := false
		switch {
		case x.resID != nil && *x.resID > 0:
			mine = *x.resID == r.id
		case names[club.NormName(x.name)]:
			mine = true
		case strings.TrimSpace(x.name) != "":
			k := club.NormName(x.name)
			id, ok := cache[k]
			if !ok {
				id = -1
				if m, err := a.matchResident(x.name); err == nil {
					id = m.id
				}
				cache[k] = id
			}
			mine = id == r.id
		}
		if mine {
			out = append(out, x.p)
		}
	}
	return out, nil
}

// memberPaid: the resident's membership income dated from since on.
func memberPaid(pays []resPay, since time.Time) int64 {
	s := since.Format("2006-01-02")
	var n int64
	for _, p := range pays {
		if PayKind(p.cat) == "debt" && p.date.Format("2006-01-02") >= s {
			n += p.income
		}
	}
	return n
}

// chargeRenewal: the package's tariff goes into the renewal debt; the
// resident's prepayments pay it at once.
func (a *applier) chargeRenewal(r *resRow, reason string) error {
	if err := a.why(reason); err != nil {
		return err
	}
	if err := a.update("club_residents", r.id, `renew_debt = renew_debt + $2, updated_at = now(), updated_by = 'server'`, r.tariff); err != nil {
		return err
	}
	pays, err := a.residentPays(r)
	if err != nil {
		return err
	}
	from, _ := a.allocFrom()
	for _, p := range pays {
		if p.left <= 0 || PayKind(p.cat) != "debt" || p.legacy {
			continue
		}
		if p.lines == 0 && p.date.Format("2006-01-02") < from.Format("2006-01-02") {
			continue // the sheet's history
		}
		if _, err := a.allocate(p.id, "prepaid"); err != nil {
			return err
		}
	}
	return nil
}

// packageDone: the meeting of day d made «проведено» reach the package
// (done ≥ granted). Charged once per package.
func (a *applier) packageDone(r *resRow, d time.Time, done int64) error {
	day := d.In(club.Almaty).Format("02.01.2006")
	if done > r.granted {
		// the counter was full before this meeting: that package ended earlier
		// (the sheet's 3/3 at the cutover) and was charged there. This meeting
		// is the first of the next package.
		var renew int64
		if err := a.tx.QueryRow(a.ctx, `SELECT GREATEST(renew_debt, 0) FROM club_residents WHERE id = $1`, r.id).Scan(&renew); err != nil {
			return err
		}
		pays, err := a.residentPays(r)
		if err != nil {
			return err
		}
		paid := memberPaid(pays, d.AddDate(0, 0, -35))
		switch {
		case r.tariff <= 0:
		case renew > 0:
			log.Printf("r70 package: %s: встреча %s после закрытого пакета (%d/%d): продление уже в долге (%s ₸), второй раз не начисляется",
				r.name, day, done-1, r.granted, club.FmtMoney(renew))
		case paid >= r.tariff:
			log.Printf("r70 package: %s: встреча %s после закрытого пакета (%d/%d): пакет оплачен (%s ₸ за 35 дней), долг не начисляется",
				r.name, day, done-1, r.granted, club.FmtMoney(paid))
		default:
			if err := a.chargeRenewal(r, fmt.Sprintf("пакет %d/%d закрыт до встречи %s, оплаты за него нет: тариф в долг продления", done-1, r.granted, day)); err != nil {
				return err
			}
		}
		return a.newPackage(r, d)
	}
	if r.tariff > 0 {
		if err := a.chargeRenewal(r, fmt.Sprintf("пакет %d/%d закрыт встречей %s: тариф в долг продления", done, r.granted, day)); err != nil {
			return err
		}
	}
	return a.newPackage(r, d.AddDate(0, 0, 1))
}

// ── «Установить долг» ──

// DebtSet is what setting a debt did.
type DebtSet struct {
	ResidentID  int64  `json:"residentId"`
	Resident    string `json:"resident"`
	RestBefore  int64  `json:"restBefore"`
	RenewBefore int64  `json:"renewBefore"`
	RestAfter   int64  `json:"restAfter"`
	RenewAfter  int64  `json:"renewAfter"`
	Reason      string `json:"reason"`
	Already     bool   `json:"already,omitempty"` // the keyed adjustment was made before
}

// setDebt: the resident owes debt from now on (the entry fee's rest first,
// then the renewal). key: a one-off adjustment (made once).
func (a *applier) setDebt(res *resRow, debt int64, reason, by, key string) (*DebtSet, error) {
	if debt < 0 {
		return nil, &ErrLinkInput{"долг не может быть меньше нуля"}
	}
	out := &DebtSet{ResidentID: res.id, Resident: res.name, Reason: reason}
	if err := a.tx.QueryRow(a.ctx, `SELECT rest_entry, renew_debt FROM club_residents WHERE id = $1`, res.id).Scan(&out.RestBefore, &out.RenewBefore); err != nil {
		return nil, err
	}
	if key != "" {
		var n int
		if err := a.tx.QueryRow(a.ctx, `SELECT count(*) FROM club_debt_adjust WHERE key = $1`, key).Scan(&n); err != nil {
			return nil, err
		}
		if n > 0 {
			out.RestAfter, out.RenewAfter, out.Already = out.RestBefore, out.RenewBefore, true
			return out, nil
		}
	}
	rest := out.RestBefore
	if rest < 0 {
		rest = 0
	}
	out.RestAfter = min64(rest, debt)
	out.RenewAfter = debt - out.RestAfter
	w := "Установить долг: " + reason
	if by != "" {
		w += " (" + by + ")"
	}
	if err := a.why(w); err != nil {
		return nil, err
	}
	if err := a.update("club_residents", res.id, `rest_entry = $2, renew_debt = $3, updated_at = now(), updated_by = 'adjust'`, out.RestAfter, out.RenewAfter); err != nil {
		return nil, err
	}
	var k any
	if key != "" {
		k = key
	}
	if _, err := a.insert("club_debt_adjust", `INSERT INTO club_debt_adjust (resident_id, rest_before, renew_before, rest_after, renew_after, reason, by, key)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8)`, res.id, out.RestBefore, out.RenewBefore, out.RestAfter, out.RenewAfter, reason, by, k); err != nil {
		return nil, err
	}
	return out, nil
}

// SetDebt («Установить долг»): resident by id (or name), the debt, the reason.
func (r *ClubRepo) SetDebt(ctx context.Context, id int64, name string, debt int64, reason, by, key string) (*DebtSet, error) {
	tx, err := r.db.Pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	a := &applier{ctx: ctx, tx: tx, at: time.Now()}
	var res *resRow
	if id > 0 {
		res, err = a.residentByID(id)
	} else {
		res, err = a.matchResident(name)
	}
	if err != nil {
		return nil, &ErrLinkInput{err.Error()}
	}
	out, err := a.setDebt(res, debt, reason, by, key)
	if err != nil {
		return nil, err
	}
	if out.Already {
		return out, nil
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	log.Printf("r70 debt set: %s: долг %s → %s ₸ (%s)", out.Resident, club.FmtMoney(nonNeg(out.RestBefore)+nonNeg(out.RenewBefore)),
		club.FmtMoney(out.RestAfter+out.RenewAfter), reason)
	return out, nil
}

func nonNeg(v int64) int64 {
	if v < 0 {
		return 0
	}
	return v
}

// ── Audit ──

// DebtFlag: a resident whose debt does not fit the payments.
type DebtFlag struct {
	Resident string
	Debt     int64
	Why      []string
}

// auditDebt: why this debt needs a look (nothing: it fits).
func (a *applier) auditDebt(r *resRow, rest, renew int64, pays []resPay) ([]string, error) {
	var why []string
	if rest < 0 || renew < 0 {
		why = append(why, "в базе отрицательный долг (остаток входа "+club.FmtMoney(rest)+", продление "+club.FmtMoney(renew)+")")
	}
	debt := nonNeg(rest) + nonNeg(renew)
	if debt == 0 {
		return why, nil
	}
	if at, ok := a.lastAdjust(r.id); ok {
		var later int
		if err := a.tx.QueryRow(a.ctx, `SELECT count(*) FROM club_balance_log WHERE resident_id = $1 AND at > $2`, r.id, at).Scan(&later); err != nil {
			return nil, err
		}
		if later == 0 {
			return why, nil // the team set this debt and nothing changed it since
		}
	}
	if r.tariff > 0 && debt > r.tariff {
		why = append(why, fmt.Sprintf("долг %s ₸ больше тарифа %s ₸ (больше одного пакета)", club.FmtMoney(debt), club.FmtMoney(r.tariff)))
	}
	var from *time.Time
	if err := a.tx.QueryRow(a.ctx, `SELECT package_from FROM club_residents WHERE id = $1`, r.id).Scan(&from); err != nil {
		return nil, err
	}
	if from != nil {
		since := from.AddDate(0, 0, -14)
		if paid := memberPaid(pays, since); paid >= debt {
			why = append(why, fmt.Sprintf("с %s оплачено %s ₸ за членство, а долг %s ₸ (пакет с %s): возможно, пакет уже оплачен",
				since.Format("02.01"), club.FmtMoney(paid), club.FmtMoney(debt), from.Format("02.01")))
		}
	}
	var left int64
	for _, p := range pays {
		if p.left > 0 && p.lines > 0 && PayKind(p.cat) == "debt" {
			left += p.left
		}
	}
	if left > 0 {
		why = append(why, fmt.Sprintf("остаток оплат %s ₸ не ушёл в долг", club.FmtMoney(left)))
	}
	return why, nil
}

// DebtAudit: every active resident's debt against the payments.
func (r *ClubRepo) DebtAudit(ctx context.Context) ([]DebtFlag, error) {
	tx, err := r.db.Pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	a := &applier{ctx: ctx, tx: tx, at: time.Now()}
	if err := a.cached(); err != nil {
		return nil, err
	}
	list, err := a.residents()
	if err != nil {
		return nil, err
	}
	var out []DebtFlag
	for i := range list {
		x := &list[i]
		if x.archive || x.former {
			continue
		}
		var admin bool
		var rest, renew int64
		if err := tx.QueryRow(ctx, `SELECT admin, rest_entry, renew_debt FROM club_residents WHERE id = $1`, x.id).Scan(&admin, &rest, &renew); err != nil {
			return nil, err
		}
		if admin {
			continue
		}
		pays, err := a.residentPays(x)
		if err != nil {
			return nil, err
		}
		why, err := a.auditDebt(x, rest, renew, pays)
		if err != nil {
			return nil, err
		}
		if len(why) > 0 {
			out = append(out, DebtFlag{Resident: x.name, Debt: nonNeg(rest) + nonNeg(renew), Why: why})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Resident < out[j].Resident })
	return out, nil
}

// ── The balance log ──

// BalanceEvent is one change of a resident's debt.
type BalanceEvent struct {
	ID       int64  `json:"id"`
	Resident string `json:"resident"`
	At       string `json:"at"` // RFC3339
	Before   int64  `json:"before"`
	After    int64  `json:"after"`
	Reason   string `json:"reason"`
}

// BalanceLogSince: the debt changes after id (oldest first, at most limit).
func (r *ClubRepo) BalanceLogSince(ctx context.Context, id int64, limit int) ([]BalanceEvent, error) {
	rows, err := r.db.Pool.Query(ctx, `SELECT id, name, at, GREATEST(COALESCE(rest_before,0),0) + GREATEST(COALESCE(renew_before,0),0),
			GREATEST(COALESCE(rest_after,0),0) + GREATEST(COALESCE(renew_after,0),0), reason
		FROM club_balance_log WHERE id > $1 ORDER BY id LIMIT $2`, id, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []BalanceEvent
	for rows.Next() {
		var e BalanceEvent
		var at time.Time
		if err := rows.Scan(&e.ID, &e.Resident, &at, &e.Before, &e.After, &e.Reason); err != nil {
			return nil, err
		}
		e.At = at.UTC().Format(time.RFC3339)
		out = append(out, e)
	}
	return out, rows.Err()
}

// balanceHistory: a resident's last debt changes, newest first.
func (a *applier) balanceHistory(resID int64, n int) ([]BalanceEvent, error) {
	rows, err := a.tx.Query(a.ctx, `SELECT id, name, at, GREATEST(COALESCE(rest_before,0),0) + GREATEST(COALESCE(renew_before,0),0),
			GREATEST(COALESCE(rest_after,0),0) + GREATEST(COALESCE(renew_after,0),0), reason
		FROM club_balance_log WHERE resident_id = $1 ORDER BY id DESC LIMIT $2`, resID, n)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []BalanceEvent{}
	for rows.Next() {
		var e BalanceEvent
		var at time.Time
		if err := rows.Scan(&e.ID, &e.Resident, &at, &e.Before, &e.After, &e.Reason); err != nil {
			return nil, err
		}
		e.At = at.UTC().Format(time.RFC3339)
		out = append(out, e)
	}
	return out, rows.Err()
}

// Meta / MetaSet: club_meta values.
func (r *ClubRepo) Meta(ctx context.Context, key string) string {
	var v string
	_ = r.db.Pool.QueryRow(ctx, `SELECT value FROM club_meta WHERE key = $1`, key).Scan(&v)
	return v
}

func (r *ClubRepo) MetaSet(ctx context.Context, key, value string) error {
	_, err := r.db.Pool.Exec(ctx, `INSERT INTO club_meta (key, value) VALUES ($1, $2)
		ON CONFLICT (key) DO UPDATE SET value = EXCLUDED.value`, key, value)
	return err
}

// DebtState: one resident's debt and open fines now (by the ledger).
type DebtState struct {
	Resident  string
	Debt      int64
	FinesOpen int64
}

func (r *ClubRepo) DebtState(ctx context.Context, name string) (*DebtState, error) {
	tx, err := r.db.Pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	a := &applier{ctx: ctx, tx: tx, at: time.Now()}
	res, err := a.matchResident(name)
	if err != nil {
		return nil, err
	}
	out := &DebtState{Resident: res.name}
	var rest, renew int64
	if err := tx.QueryRow(ctx, `SELECT rest_entry, renew_debt FROM club_residents WHERE id = $1`, res.id).Scan(&rest, &renew); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, fmt.Errorf("резидента «%s» нет", name)
		}
		return nil, err
	}
	out.Debt = nonNeg(rest) + nonNeg(renew)
	fs, err := a.openFines(res)
	if err != nil {
		return nil, err
	}
	for _, f := range fs {
		out.FinesOpen += f.amount - f.paid
	}
	return out, nil
}
