package club

import (
	"sort"
	"strings"
	"time"
)

// DebetRow is a resident with the numbers the sheet used to compute with
// formulas: meetings left (F), unpaid fines (J), total debt (K).
type DebetRow struct {
	Resident
	MeetingsLeft int64 `json:"meetingsLeft"`
	FinesUnpaid  int64 `json:"finesUnpaid"`
	TotalDebt    int64 `json:"totalDebt"`
}

// Debet computes every active resident's numbers.
//
//	Осталось встреч = оплачено − проведено          (формула F: =D−E)
//	Штрафы          = неоплаченные штрафы по имени   (формула J: SUMIFS … "<>Оплатил")
//	Общий долг      = остаток входа + долг продления + штрафы (формула K: =H+I+J)
//
// Negative money is shown as zero, as the sheet script did for G, H and I.
func Debet(residents []Resident, fines []Fine) []DebetRow {
	unpaid := map[string]int64{}
	for _, f := range fines {
		if !f.Paid {
			unpaid[NormName(f.Name)] += f.Amount
		}
	}
	var out []DebetRow
	for _, r := range residents {
		if r.Former {
			continue
		}
		r.PaidEntry = nonNeg(r.PaidEntry)
		r.RestEntry = nonNeg(r.RestEntry)
		r.RenewDebt = nonNeg(r.RenewDebt)
		d := DebetRow{Resident: r, MeetingsLeft: r.Granted - r.Done, FinesUnpaid: unpaid[NormName(r.Name)]}
		d.TotalDebt = r.RestEntry + r.RenewDebt + d.FinesUnpaid
		out = append(out, d)
	}
	return out
}

func nonNeg(v int64) int64 {
	if v < 0 {
		return 0
	}
	return v
}

// ── P&L ──────────────────────────────────────────────────────────────────

// CashStartMonth is the first month the cash balance is counted from
// (April 2026: that is where the club's cash was reconciled).
const CashStartMonth = 4

// PLMonth is one month of the P&L.
type PLMonth struct {
	Income    map[string]int64 `json:"income"`  // по строкам доходов PL
	Expense   map[string]int64 `json:"expense"` // по строкам расходов PL
	IncomeSum int64            `json:"incomeTotal"`
	Expenses  int64            `json:"expenseTotal"`
	Profit    int64            `json:"profit"`
	Dividends int64            `json:"dividends"`
	Cash      int64            `json:"cash"`
	HasCash   bool             `json:"hasCash"`
}

// PL is the whole year.
type PL struct {
	Year        int            `json:"year"`
	UpTo        int            `json:"upToMonth"`
	IncomeRows  []string       `json:"incomeRows"`
	ExpenseRows []string       `json:"expenseRows"`
	Months      [12]PLMonth    `json:"months"`
	Unknown     []UnknownEntry `json:"unknown"` // суммы, которым не нашлась статья
}

// UnknownEntry is money the P&L could not place.
type UnknownEntry struct {
	Row    int    `json:"row"`
	Date   string `json:"date"`
	Amount int64  `json:"amount"`
	Kind   string `json:"kind"` // income / expense
	Cat    string `json:"category"`
	Placed string `json:"placedIn"` // куда отнесено, или "" если не учтено
}

// NormCat is how the sheet script compared categories: lower case, ё→е,
// no spaces around "+", no trailing ":" or ".", single spaces.
func NormCat(s string) string {
	s = strings.ToLower(strings.ReplaceAll(s, "ё", "е"))
	s = strings.Join(strings.Fields(s), " ")
	s = strings.ReplaceAll(strings.ReplaceAll(s, " +", "+"), "+ ", "+")
	return strings.TrimRight(s, ":. ")
}

// BuildPL builds the year's P&L from the cash journal, with the same rules
// as the sheet (bsRebuildPL):
//   - only rows of that year, months up to upTo;
//   - income goes to the line named like its «Источник», else «Прочие доходы»;
//   - an expense whose category mentions dividends goes to «Дивиденды»;
//   - other expenses go to the line named like their category, else
//     «Прочие расходы:»; an expense with no category is left out and listed.
//
// Lines come from the sheet's P&L structure.
func BuildPL(payments []Payment, structure *PLSheet, year, upTo int) *PL {
	pl := &PL{Year: year, UpTo: upTo}
	incIdx, expIdx := map[string]string{}, map[string]string{}
	otherIncome, otherExpense := "", ""
	if structure != nil {
		for _, r := range structure.Rows {
			switch r.Section {
			case "income":
				pl.IncomeRows = append(pl.IncomeRows, r.Name)
				incIdx[NormCat(r.Name)] = r.Name
				if strings.HasPrefix(NormCat(r.Name), "прочие доход") {
					otherIncome = r.Name
				}
			case "expense":
				pl.ExpenseRows = append(pl.ExpenseRows, r.Name)
				expIdx[NormCat(r.Name)] = r.Name
				if strings.HasPrefix(NormCat(r.Name), "прочие расход") {
					otherExpense = r.Name
				}
			}
		}
	}
	for m := range pl.Months {
		pl.Months[m].Income = map[string]int64{}
		pl.Months[m].Expense = map[string]int64{}
	}
	rows := append([]Payment(nil), payments...)
	sort.SliceStable(rows, func(i, j int) bool { return rows[i].Row < rows[j].Row })
	for _, p := range rows {
		if p.Date.Year() != year || int(p.Date.Month()) > upTo {
			continue
		}
		mo := &pl.Months[int(p.Date.Month())-1]
		ds := p.Date.Format("02.01.2006")
		if p.Income > 0 {
			line, ok := incIdx[NormCat(p.IncomeCat)]
			if !ok {
				line = otherIncome
				pl.Unknown = append(pl.Unknown, UnknownEntry{Row: p.Row, Date: ds, Amount: p.Income, Kind: "income", Cat: p.IncomeCat, Placed: line})
			}
			if line != "" {
				mo.Income[line] += p.Income
				mo.IncomeSum += p.Income
			}
		}
		if p.Expense > 0 {
			cat := NormCat(p.ExpenseCat)
			switch {
			case cat == "":
				pl.Unknown = append(pl.Unknown, UnknownEntry{Row: p.Row, Date: ds, Amount: p.Expense, Kind: "expense", Cat: "", Placed: ""})
			case strings.Contains(cat, "дивиденд"):
				mo.Dividends += p.Expense
			default:
				line, ok := expIdx[cat]
				if !ok {
					line = otherExpense
					pl.Unknown = append(pl.Unknown, UnknownEntry{Row: p.Row, Date: ds, Amount: p.Expense, Kind: "expense", Cat: p.ExpenseCat, Placed: line})
				}
				if line != "" {
					mo.Expense[line] += p.Expense
					mo.Expenses += p.Expense
				}
			}
		}
	}
	var cash int64
	for m := 0; m < 12; m++ {
		mo := &pl.Months[m]
		mo.Profit = mo.IncomeSum - mo.Expenses
		if m+1 >= CashStartMonth {
			cash += mo.Profit - mo.Dividends
			mo.Cash, mo.HasCash = cash, true
		}
	}
	return pl
}

// Today in Almaty.
func Today() time.Time { return time.Now().In(Almaty) }
