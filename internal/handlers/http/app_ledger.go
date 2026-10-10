package http

import (
	"context"
	"sort"
	"sync"
	"time"

	"github.com/bnursik/business_surgery_backend/internal/club"
	pg "github.com/bnursik/business_surgery_backend/internal/repository/pg"
)

// R81: the Telegram app shows debts and fines from the payments ledger, the
// same numbers as «Учёт → Долги и штрафы» on the platform.
//
// Before, the bundle's fines were the table's rows as the sheet had them: the
// «Статус» text decided (a fine the platform's old «Оплатил» marked paid kept
// «Не оплатил»), a fine paid in part showed its whole amount, and fines of
// people who are not residents any more (archived, the team) stayed in
// «Штрафы к получению» for ever. Now the ledger decides: which resident a
// fine counts for, what is paid on it, what is still owed.

// ledgerSource is the ledger the bundle is read from (pg.ClubRepo).
type ledgerSource interface {
	DebtsLite(ctx context.Context) (*pg.DebtsReport, error)
}

// LedgerView is the bundle's «ledger»: the totals of «Долги и штрафы».
type LedgerView struct {
	At         int64              `json:"at"` // when it was read, ms
	Debt       int64              `json:"debt"`
	Fines      int64              `json:"fines"`      // open fines, what is still owed
	FinesCount int                `json:"finesCount"` // open fines
	Total      int64              `json:"total"`
	Debtors    int                `json:"debtors"`
	Residents  []LedgerViewResRow `json:"residents"`
}

type LedgerViewResRow struct {
	Name      string `json:"name"`
	Former    bool   `json:"former,omitempty"`
	Debt      int64  `json:"debt"`
	FinesOpen int64  `json:"finesOpen"`
	Total     int64  `json:"total"`
}

// ledgerTTL: the ledger is read again after this (and at once after any
// write: dropBundles).
const ledgerTTL = 3 * time.Second

type ledgerCache struct {
	mu  sync.Mutex
	rep *pg.DebtsReport
	at  time.Time
	gen int
}

func (g *AppGateway) ledger(ctx context.Context) (*pg.DebtsReport, time.Time, bool) {
	src, ok := g.Club.(ledgerSource)
	if !ok {
		return nil, time.Time{}, false
	}
	g.mu.Lock()
	gen := g.gen
	g.mu.Unlock()
	now := g.now()
	g.ledgerC.mu.Lock()
	defer g.ledgerC.mu.Unlock()
	if g.ledgerC.rep != nil && g.ledgerC.gen == gen && now.Sub(g.ledgerC.at) < ledgerTTL && !now.Before(g.ledgerC.at) {
		return g.ledgerC.rep, g.ledgerC.at, true
	}
	rep, err := src.DebtsLite(ctx)
	if err != nil || rep == nil {
		return nil, time.Time{}, false
	}
	g.ledgerC.rep, g.ledgerC.at, g.ledgerC.gen = rep, now, gen
	return rep, now, true
}

// applyLedger puts the ledger over the bundle's money sections: fines,
// residents' debts and fines, «Дебет», totalDebt, and adds «ledger».
func applyLedger(parts map[string]any, snap *club.Snapshot, rep *pg.DebtsReport, at time.Time) {
	if rep == nil {
		return
	}
	// the fines of the table by id (the row and the name the app sends back)
	byID := map[int64]club.Fine{}
	if snap != nil {
		for _, f := range snap.Fines {
			if f.ID > 0 {
				byID[f.ID] = f
			}
		}
	}
	old, _ := parts["fines"].([]club.BundleFine)
	rowOf := map[int64]int{}
	for _, b := range old {
		if b.ID > 0 {
			rowOf[b.ID] = b.Row
		}
	}
	lv := LedgerView{At: at.UnixMilli(), Residents: []LedgerViewResRow{}}
	type fx struct {
		b   club.BundleFine
		day string
	}
	var all []fx
	byName := map[string]pg.DebtRow{}
	for _, r := range rep.Residents {
		byName[club.NormName(r.Name)] = r
		lv.Debt += r.Debt
		lv.Fines += r.FinesOpen
		lv.Total += r.Total
		if r.Total > 0 {
			lv.Debtors++
		}
		lv.Residents = append(lv.Residents, LedgerViewResRow{Name: r.Name, Former: r.Former, Debt: r.Debt, FinesOpen: r.FinesOpen, Total: r.Total})
		for _, f := range r.Fines {
			name := r.Name
			if t, ok := byID[f.ID]; ok && t.Name != "" {
				name = t.Name // as the table has it: «Оплатил» and «Удалить» find the fine by it
			}
			b := club.BundleFine{Row: rowOf[f.ID], Name: name, Kind: f.Type, Amount: f.Amount, Status: f.Status, ID: f.ID, Res: r.Name, Paid: f.Paid, Left: f.Left}
			if d, err := time.Parse("2006-01-02", f.Date); err == nil {
				b.Date = d.Format("02.01.2006")
			}
			if f.Status == "Не оплатил" {
				lv.FinesCount++
			}
			all = append(all, fx{b, f.Date})
		}
	}
	sort.SliceStable(all, func(i, j int) bool {
		if all[i].day != all[j].day {
			return all[i].day < all[j].day
		}
		return all[i].b.ID < all[j].b.ID
	})
	fines := make([]club.BundleFine, 0, len(all))
	for _, x := range all {
		fines = append(fines, x.b)
	}
	parts["fines"] = fines

	if res, ok := parts["residents"].([]club.BundleResident); ok {
		for i := range res {
			if d, ok := byName[club.NormName(res[i].Name)]; ok {
				res[i].Fines = d.FinesOpen
				res[i].Debt = d.Total
			} else if !res[i].IsAdmin {
				res[i].Fines = 0 // not in the ledger (archived): nothing to collect
				res[i].Debt = nonNeg64(res[i].Balance) + nonNeg64(res[i].DebtRenew)
			}
		}
		parts["residents"] = res
	}
	parts["totalDebt"] = lv.Total // «Итого к оплате» of the platform
	switch d := parts["debet"].(type) {
	case club.BundleDebet:
		d.UnpaidFines, d.UnpaidCount = lv.Fines, lv.FinesCount
		parts["debet"] = d
	default:
		parts["debet"] = club.BundleDebet{UnpaidFines: lv.Fines, UnpaidCount: lv.FinesCount}
	}
	parts["ledger"] = lv
}

func nonNeg64(n int64) int64 {
	if n < 0 {
		return 0
	}
	return n
}
