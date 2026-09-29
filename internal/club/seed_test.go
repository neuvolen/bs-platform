package club

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"
)

// The platform's Учёт must show the sheet's own numbers: every P&L total,
// month by month, and every resident's debt.
func TestLiveSeedMatchesSheet(t *testing.T) {
	path := os.Getenv("BS_SHEET_JSON")
	if path == "" {
		t.Skip("BS_SHEET_JSON not set")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var s Sheets
	if err := json.Unmarshal(raw, &s); err != nil {
		t.Fatal(err)
	}
	snap, _, err := Parse(s)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 9, 28, 12, 0, 0, 0, Almaty) // the day of the export
	seed, err := LiveSeed(snap, `{"SDATA":{"nps":[{"res":"x"}],"visits":[{"name":"Елена","per":4}]}}`, now)
	if err != nil {
		t.Fatal(err)
	}
	var out struct {
		Residents []SeedResident   `json:"RESIDENTS"`
		Fines     []SeedFine       `json:"FINES"`
		PL        []SeedPLRow      `json:"PL_ROWS"`
		Months    []string         `json:"PL_MONTHS"`
		SData     map[string][]any `json:"SDATA"`
	}
	if err := json.Unmarshal([]byte(seed), &out); err != nil {
		t.Fatal(err)
	}
	if strings.Join(out.Months, ",") != "Апрель,Май,Июнь,Июль,Август,Сентябрь" {
		t.Fatalf("months %v", out.Months)
	}
	sheetRow := map[string]PLRow{}
	for _, r := range snap.PL.Rows {
		sheetRow[strings.ToLower(r.Name)] = r
	}
	checked := 0
	for _, name := range []string{"ИТОГО ДОХОДЫ", "ИТОГО РАСХОДЫ", "ЧИСТАЯ ПРИБЫЛЬ", "Дивиденды", "На кассе"} {
		var row *SeedPLRow
		for i := range out.PL {
			if out.PL[i].Name == name {
				row = &out.PL[i]
			}
		}
		sh, ok := sheetRow[strings.ToLower(name)]
		if row == nil || !ok {
			t.Fatalf("line %s: platform %v, sheet %v", name, row != nil, ok)
		}
		for i := range out.Months {
			m := 3 + i // April is index 3
			if !sh.Set[m] {
				continue
			}
			if row.Vals[i] != sh.Values[m] {
				t.Errorf("%s, %s: platform %d, sheet %d", name, out.Months[i], row.Vals[i], sh.Values[m])
			}
			checked++
		}
	}
	// Every item line of the platform sums to the totals it shows.
	for i := range out.Months {
		var inc, exp int64
		mode := ""
		for _, r := range out.PL {
			switch {
			case r.Name == "ДОХОДЫ":
				mode = "inc"
			case r.Name == "РАСХОДЫ":
				mode = "exp"
			case r.Kind == "item" && mode == "inc":
				inc += r.Vals[i]
			case r.Kind == "item" && mode == "exp" && r.Name != "Дивиденды":
				exp += r.Vals[i]
			}
		}
		for _, r := range out.PL {
			if r.Name == "ИТОГО ДОХОДЫ" && r.Vals[i] != inc || r.Name == "ИТОГО РАСХОДЫ" && r.Vals[i] != exp {
				t.Errorf("%s %s: lines add up to %d/%d", r.Name, out.Months[i], inc, exp)
			}
		}
	}
	// Debts as the sheet has them.
	debts := 0
	for _, r := range snap.Residents {
		if r.Former || r.Admin || r.SheetTotal == nil {
			continue
		}
		for _, x := range out.Residents {
			if x.Name == r.Name {
				if x.Total != *r.SheetTotal {
					t.Errorf("%s: debt platform %d, sheet %d", r.Name, x.Total, *r.SheetTotal)
				}
				debts++
			}
		}
	}
	if len(out.SData["nps"]) != 1 || len(out.SData["schedule"]) != len(snap.Meetings) {
		t.Fatalf("sdata: nps %d, schedule %d", len(out.SData["nps"]), len(out.SData["schedule"]))
	}
	t.Logf("PL: %d monthly totals equal to the sheet; debts: %d residents; fines %d; meetings %d", checked, debts, len(out.Fines), len(snap.Meetings))
}
