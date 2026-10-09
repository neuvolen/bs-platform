package http

import (
	"context"
	"log"
	"strings"
	"time"

	pg "github.com/bnursik/business_surgery_backend/internal/repository/pg"
)

// R63: the payments entered after the cutover and before R59 pay off the
// debt by themselves (club_payments_r63.go). At every start: Альтаир's
// 07.10 payment (a no-op once counted, the log then shows the debt); once:
// the other residents' «не учтено» payments since the cutover. Only names
// and sums go to the log.

const metaDebtAuto = "r63_debt_auto" // "done:<RFC3339>"

type debtFixRepo interface {
	Master(ctx context.Context) (string, error)
	PayAltairOct7(ctx context.Context) (*pg.DebtPay, string, error)
	AutoCountPayments(ctx context.Context, since time.Time) (done, left []pg.DebtPay, err error)
}

// DebtFixAtStart runs the fix; refresh rebuilds the platform's club data
// when anything was counted.
func DebtFixAtStart(ctx context.Context, repo debtFixRepo, meta MetaStore, refresh func()) {
	if m, err := repo.Master(ctx); err != nil || m != "server" {
		return // the sheet still keeps the club's data: its script counts payments
	}
	changed := false
	p, note, err := repo.PayAltairOct7(ctx)
	switch {
	case err != nil:
		log.Printf("debt fix: Альтаир 07.10: %v", err)
	case p != nil:
		changed = true
		log.Printf("debt fix: Альтаир 07.10: payment %d ₸ (%s) counted for %s: %d ₸ to the debt, debt %d → %d ₸",
			p.Amount, p.Cat, p.Resident, p.Paid, p.Before, p.After)
	default:
		log.Printf("debt fix: Альтаир 07.10: %s", note)
	}
	if v, _ := meta.GetMeta(ctx, metaDebtAuto); strings.HasPrefix(v, "done:") {
		if changed && refresh != nil {
			refresh()
		}
		return
	}
	cut, _ := meta.GetMeta(ctx, metaCutover)
	st, at, _ := strings.Cut(cut, ":")
	since, perr := time.Parse(time.RFC3339, at)
	if st != "done" || perr != nil {
		log.Printf("debt fix: no cutover date (%q), other payments not checked", st)
		if changed && refresh != nil {
			refresh()
		}
		return
	}
	since = since.In(csAlmaty)
	done, left, err := repo.AutoCountPayments(ctx, time.Date(since.Year(), since.Month(), since.Day(), 0, 0, 0, 0, time.UTC))
	if err != nil {
		log.Printf("debt fix: payments since %s: %v", since.Format("02.01.2006"), err)
	} else {
		for _, d := range done {
			log.Printf("debt fix: counted %s %s %d ₸ (%s): %d ₸ to the debt, debt %d → %d ₸", d.Date, d.Resident, d.Amount, d.Cat, d.Paid, d.Before, d.After)
		}
		for _, d := range left {
			log.Printf("debt fix: left «не учтено» %s %s %d ₸ (%s): %s", d.Date, d.Resident, d.Amount, d.Cat, d.Why)
		}
		log.Printf("debt fix: payments since %s: %d counted, %d left for «Учесть»", since.Format("02.01.2006"), len(done), len(left))
		if len(done) > 0 {
			changed = true
		}
		if err := meta.SetMeta(ctx, metaDebtAuto, "done:"+time.Now().UTC().Format(time.RFC3339)); err != nil {
			log.Printf("debt fix: %v", err)
		}
	}
	if changed && refresh != nil {
		refresh()
	}
}
