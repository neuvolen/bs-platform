package pg

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/bnursik/business_surgery_backend/internal/club"
	"github.com/jackc/pgx/v5"
)

// R59: a resident's payment pays off their debt. The script did it with
// bsApplyPaymentsFromDDS (rest of the entry fee first, then the renewal debt,
// the ДДС row marked «учтено»); the server's addPayment only extended the
// meetings package, so after the cutover a paid resident still showed a debt.
// A payment whose name matches no resident stays «не учтено» and is linked by
// hand («Привязать платёж», LinkPayment).

// matchResident finds the resident a payment names: the exact name, the same
// name in another case or with ё/е, else the only resident with that first
// name (or whose name starts with what was typed). Former residents count
// only when no active one matches.
func (a *applier) matchResident(name string) (*resRow, error) {
	want := club.NormName(name)
	if want == "" {
		return nil, errors.New("нет имени")
	}
	list, err := a.residents()
	if err != nil {
		return nil, err
	}
	var live []resRow
	for _, r := range list {
		if !r.archive {
			live = append(live, r)
		}
	}
	pick := func(ok func(r resRow) bool) (*resRow, bool) {
		for _, active := range []bool{true, false} {
			var hit []resRow
			for _, r := range live {
				if r.former != active && ok(r) {
					hit = append(hit, r)
				}
			}
			if len(hit) == 1 {
				return &hit[0], true
			}
			if len(hit) > 1 {
				return nil, true // ambiguous: not guessed
			}
		}
		return nil, false
	}
	for _, r := range live {
		if strings.TrimSpace(r.name) == strings.TrimSpace(name) {
			rr := r
			return &rr, nil
		}
	}
	tests := []func(r resRow) bool{
		func(r resRow) bool { return club.NormName(r.name) == want },
		func(r resRow) bool {
			f := strings.Fields(club.NormName(r.name))
			return len(f) > 0 && f[0] == strings.Fields(want)[0] && len(strings.Fields(want)) == 1
		},
		func(r resRow) bool { return strings.HasPrefix(club.NormName(r.name), want+" ") },
	}
	for _, t := range tests {
		if r, done := pick(t); done {
			if r == nil {
				return nil, fmt.Errorf("«%s»: подходит несколько резидентов", name)
			}
			return r, nil
		}
	}
	return nil, fmt.Errorf("резидент «%s» не найден", name)
}

// payDebt counts the payment against the resident's debt: the rest of the
// entry fee first, then the renewal debt. The ДДС row gets the resident's
// exact name and «учтено» (applied) when any of it went to the debt. It
// returns how much went to the debt.
func (a *applier) payDebt(payID int64, r *resRow, amount int64) (int64, error) {
	var rest, renew int64
	if err := a.tx.QueryRow(a.ctx, `SELECT rest_entry, renew_debt FROM club_residents WHERE id = $1`, r.id).Scan(&rest, &renew); err != nil {
		return 0, err
	}
	if rest < 0 {
		rest = 0
	}
	if renew < 0 {
		renew = 0
	}
	left := amount
	payRest := min64(rest, left)
	left -= payRest
	payRenew := min64(renew, left)
	paid := payRest + payRenew
	if paid > 0 {
		if err := a.update("club_residents", r.id, `rest_entry = rest_entry - $2, renew_debt = renew_debt - $3,
			updated_at = now(), updated_by = 'server'`, payRest, payRenew); err != nil {
			return 0, err
		}
	}
	if err := a.update("club_payments", payID, `resident = $2, applied = $3`, r.name, paid > 0); err != nil {
		return 0, err
	}
	return paid, nil
}

func min64(a, b int64) int64 {
	if a < b {
		return a
	}
	return b
}

// LinkResult: what linking a payment did.
type LinkResult struct {
	Resident string `json:"resident"`
	Paid     int64  `json:"paid"` // went to the debt
	Applied  bool   `json:"applied"`
}

// ErrLinkInput: the payment cannot be linked (not found, not an income,
// already counted, a fine's payment, no such resident).
type ErrLinkInput struct{ Msg string }

func (e *ErrLinkInput) Error() string { return e.Msg }

// LinkPayment («Привязать платёж») links an income row of the ДДС to a
// resident and counts it against their debt, once: a row already «учтено»
// is refused. A fine's payment closes fines, not the debt, so it is refused
// too (it is entered with «БХ Штраф»).
func (r *ClubRepo) LinkPayment(ctx context.Context, id int64, name string) (*LinkResult, error) {
	tx, err := r.db.Pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	var income int64
	var cat string
	var applied bool
	err = tx.QueryRow(ctx, `SELECT income, income_cat, applied FROM club_payments WHERE id = $1 FOR UPDATE`, id).Scan(&income, &cat, &applied)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, &ErrLinkInput{"строку ДДС уже удалили, обновите страницу"}
	}
	if err != nil {
		return nil, err
	}
	switch {
	case income <= 0:
		return nil, &ErrLinkInput{"это не приход"}
	case applied:
		return nil, &ErrLinkInput{"эта оплата уже учтена в долге"}
	case strings.Contains(cat, "Штраф"):
		return nil, &ErrLinkInput{"оплата штрафа закрывает штрафы, а не долг"}
	}
	a := &applier{ctx: ctx, tx: tx, at: time.Now()}
	res, err := a.matchResident(name)
	if err != nil {
		return nil, &ErrLinkInput{err.Error()}
	}
	paid, err := a.payDebt(id, res, income)
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return &LinkResult{Resident: res.name, Paid: paid, Applied: paid > 0}, nil
}
