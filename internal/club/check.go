package club

import (
	"fmt"
	"sort"
)

// Mismatch is one number where the server and the sheet disagree.
type Mismatch struct {
	Where  string `json:"where"`
	Sheet  int64  `json:"sheet"`
	Server int64  `json:"server"`
}

// Check compares what the server computes with what the sheet shows.
// An empty result means the server can take over the sheet's formulas.
func Check(snap *Snapshot, today int) (debet []Mismatch, pl []Mismatch, checked int) {
	for _, d := range Debet(snap.Residents, snap.Fines) {
		pairs := []struct {
			what   string
			sheet  *int64
			server int64
		}{
			{"осталось встреч", d.SheetLeft, d.MeetingsLeft},
			{"штрафы", d.SheetFines, d.FinesUnpaid},
			{"общий долг", d.SheetTotal, d.TotalDebt},
		}
		for _, p := range pairs {
			if p.sheet == nil {
				continue
			}
			checked++
			if *p.sheet != p.server {
				debet = append(debet, Mismatch{Where: d.Name + ": " + p.what, Sheet: *p.sheet, Server: p.server})
			}
		}
	}

	if snap.PL == nil || snap.PL.Year == 0 {
		return debet, pl, checked
	}
	built := BuildPL(snap.Payments, snap.PL, snap.PL.Year, today)
	months := []string{"янв", "фев", "мар", "апр", "май", "июн", "июл", "авг", "сен", "окт", "ноя", "дек"}
	for _, row := range snap.PL.Rows {
		for m := 0; m < today && m < 12; m++ {
			mo := built.Months[m]
			var server int64
			switch row.Section {
			case "income":
				server = mo.Income[row.Name]
			case "expense":
				server = mo.Expense[row.Name]
			case "total_income":
				server = mo.IncomeSum
			case "total_expense":
				server = mo.Expenses
			case "profit":
				server = mo.Profit
			case "dividends":
				server = mo.Dividends
			case "cash":
				if !row.Set[m] && !mo.HasCash {
					continue
				}
				server = mo.Cash
			default:
				continue
			}
			checked++
			if row.Values[m] != server {
				pl = append(pl, Mismatch{Where: fmt.Sprintf("PL %s, %s", row.Name, months[m]), Sheet: row.Values[m], Server: server})
			}
		}
	}
	sort.SliceStable(pl, func(i, j int) bool { return pl[i].Where < pl[j].Where })
	return debet, pl, checked
}
