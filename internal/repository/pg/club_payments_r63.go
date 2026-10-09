package pg

import (
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

