package http

import (
	"context"
	"testing"
	"time"

	"github.com/bnursik/business_surgery_backend/internal/club"
	pg "github.com/bnursik/business_surgery_backend/internal/repository/pg"
)

// R81: «Штрафы к получению» in the app equals «Штрафы к оплате» of «Учёт → Долги и штрафы».
func TestApplyLedgerFines(t *testing.T) {
	owed := int64(100000)
	snap := &club.Snapshot{Fines: []club.Fine{
		{ID: 1, Name: "Альтаир", Type: "Не пришёл", Amount: 100000, Status: "Не оплатил", Owed: &owed},
		{ID: 2, Name: "Иван Петров", Type: "Отчёт", Amount: 10000, Status: "Не оплатил", Paid: true}, // old «Оплатил»: only the mark
		{ID: 3, Name: "Бывший", Type: "Отчёт", Amount: 10000, Status: "Не оплатил"},                // archived: not in the ledger
		{ID: 4, Name: "иван петров", Type: "Отчёт", Amount: 30000, Status: "Не оплатил"},
	}}
	parts, _ := club.AppBundle(snap, time.Date(2026, 10, 10, 12, 0, 0, 0, club.Almaty))
	old := parts["fines"].([]club.BundleFine)
	if old[1].Status != "Оплатил" {
		t.Fatalf("paid mark must win over the old text, got %q", old[1].Status)
	}
	parts["residents"] = []club.BundleResident{{Name: "Альтаир", Debt: 999}, {Name: "Иван Петров", Debt: 5}, {Name: "Бывший", Debt: 10000, Fines: 10000}}
	rep := &pg.DebtsReport{Residents: []pg.DebtRow{
		{Name: "Альтаир", Debt: 0, FinesOpen: 100000, Total: 100000, Fines: []pg.DebtFine{{ID: 1, Date: "2026-10-09", Type: "Не пришёл", Amount: 100000, Left: 100000, Status: "Не оплатил"}}},
		{Name: "Иван Петров", Debt: 50000, FinesOpen: 20000, Total: 70000, Fines: []pg.DebtFine{
			{ID: 2, Date: "2026-09-01", Type: "Отчёт", Amount: 10000, Status: "Оплатил"},
			{ID: 4, Date: "2026-10-01", Type: "Отчёт", Amount: 30000, Paid: 10000, Left: 20000, Status: "Не оплатил"},
		}},
	}}
	applyLedger(parts, snap, rep, time.Unix(1760000000, 0))
	lv := parts["ledger"].(LedgerView)
	if lv.Fines != 120000 || lv.FinesCount != 2 || lv.Total != 170000 || lv.Debt != 50000 || lv.Debtors != 2 {
		t.Fatalf("ledger totals: %+v", lv)
	}
	fines := parts["fines"].([]club.BundleFine)
	if len(fines) != 3 {
		t.Fatalf("archived resident's fine must go: %+v", fines)
	}
	var open, sum int64
	for _, f := range fines {
		if f.Status == "Не оплатил" {
			open++
			sum += f.Left
		}
		if f.ID == 4 && (f.Name != "иван петров" || f.Res != "Иван Петров" || f.Left != 20000 || f.Paid != 10000 || f.Date != "01.10.2026") {
			t.Fatalf("part paid fine: %+v", f)
		}
	}
	if open != 2 || sum != lv.Fines {
		t.Fatalf("open fines %d, sum %d", open, sum)
	}
	res := parts["residents"].([]club.BundleResident)
	if res[0].Debt != 100000 || res[0].Fines != 100000 || res[1].Debt != 70000 || res[2].Fines != 0 || res[2].Debt != 0 {
		t.Fatalf("residents: %+v", res)
	}
	if parts["totalDebt"].(int64) != 170000 {
		t.Fatalf("totalDebt %v", parts["totalDebt"])
	}
	if d := parts["debet"].(club.BundleDebet); d.UnpaidFines != 120000 || d.UnpaidCount != 2 {
		t.Fatalf("debet %+v", d)
	}
}

type memPrefs struct{ docs map[string]*pg.PlatformDoc }

func (m *memPrefs) GetDoc(_ context.Context, scope, key string) (*pg.PlatformDoc, error) {
	return m.docs[scope+"|"+key], nil
}
func (m *memPrefs) PutDoc(_ context.Context, scope, key string, base int, value string, deleted bool, by string) (*pg.PlatformDoc, error) {
	cur := m.docs[scope+"|"+key]
	if cur != nil && cur.Version != base {
		return cur, pg.ErrPlatformConflict
	}
	v := 1
	if cur != nil {
		v = cur.Version + 1
	}
	d := &pg.PlatformDoc{Scope: scope, Key: key, Value: value, Version: v, Deleted: deleted, UpdatedBy: by}
	m.docs[scope+"|"+key] = d
	return d, nil
}

func TestPrefsMerge(t *testing.T) {
	st := &memPrefs{docs: map[string]*pg.PlatformDoc{}}
	ctx := context.Background()
	if _, err := mergePrefs(ctx, st, "user:tg:7", "app", map[string]any{"hide_events": true, "x": "a"}); err != nil {
		t.Fatal(err)
	}
	pr, err := mergePrefs(ctx, st, "user:tg:7", "platform", map[string]any{"x": nil})
	if err != nil || pr["hide_events"] != true || len(pr) != 1 {
		t.Fatalf("%v %v", pr, err)
	}
	got, ver, _ := readPrefs(ctx, st, "user:tg:7")
	if got["hide_events"] != true || ver != 2 {
		t.Fatalf("%v %d", got, ver)
	}
}
