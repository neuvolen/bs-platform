package pg

import (
	"errors"
	"fmt"
	"strings"

	"github.com/bnursik/business_surgery_backend/internal/club"
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
		func(r resRow) bool { return hasAlias(r.aliases, want) }, // R69: another spelling kept by «Привязать платёж»
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

// hasAlias: want (normalized) is one of the comma separated aliases.
func hasAlias(aliases, want string) bool {
	for _, x := range strings.Split(aliases, ",") {
		if x = club.NormName(x); x != "" && x == want {
			return true
		}
	}
	return false
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
	Paid     int64  `json:"paid"` // went to the debt or the fines
	Applied  bool   `json:"applied"`
	Left     int64  `json:"left"`
	Kind     string `json:"kind"`
	Why      string `json:"why,omitempty"`
}

// ErrLinkInput: the payment cannot be linked (not found, not an income, no
// such resident) or a fine cannot be written off.
type ErrLinkInput struct{ Msg string }

func (e *ErrLinkInput) Error() string { return e.Msg }
