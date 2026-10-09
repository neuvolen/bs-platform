package club

import (
	"encoding/json"
	"testing"
	"time"
)

// R67: «захожу, а там остаток неверный». The app's «Сводка» took «На кассе»
// from the PL sheet kept since the last import; after the cutover that sheet
// never changes. The bundle now counts it from the cash journal, as «Учёт».
func TestR67SummaryFromCashJournal(t *testing.T) {
	day := func(s string) time.Time { d, _ := time.ParseInLocation("02.01.2006", s, Almaty); return d }
	snap := &Snapshot{
		PL: &PLSheet{Year: 2026, Rows: []PLRow{
			{Name: "БХ Трекинг продление", Section: "income"},
			{Name: "Прочие доходы", Section: "income"},
			{Name: "Аренда", Section: "expense"},
			{Name: "Прочие расходы:", Section: "expense"},
		}},
		Payments: []Payment{
			{Row: 1, Date: day("10.04.2026"), Income: 1000000, IncomeCat: "БХ Трекинг продление"},
			{Row: 2, Date: day("12.04.2026"), Expense: 200000, ExpenseCat: "Аренда"},
			{Row: 3, Date: day("01.09.2026"), Expense: 300000, ExpenseCat: "Дивиденды"},
			{Row: 4, Date: day("03.10.2026"), Income: 500000, IncomeCat: "БХ Трекинг продление", Resident: "Альтаир"},
			// after the cutover: the stale sheet does not know these
			{Row: 5, Date: day("07.10.2026"), Income: 50000, IncomeCat: "БХ Экспресс разбор", Resident: "Рустам"},
			{Row: 6, Date: day("08.10.2026"), Expense: 20000, ExpenseCat: "Аренда"},
			{Row: 7, Date: day("08.03.2026"), Income: 777, IncomeCat: "Прочие доходы"}, // before April: not in the cash
		},
		Raw: Sheets{SheetPL: {
			{"PL", "янв", "фев", "мар", "апр", "май", "июн", "июл", "авг", "сен", "окт", "ноя", "дек"},
			{"ИТОГО ДОХОДЫ", "", "", "", "1000000", "", "", "", "", "", "500000"},
			{"ИТОГО РАСХОДЫ", "", "", "", "200000", "", "", "", "", "", "0"},
			{"ЧИСТАЯ ПРИБЫЛЬ", "", "", "", "800000", "", "", "", "", "", "500000"},
			{"Дивиденды", "", "", "", "", "", "", "", "", "300000", ""},
			{"На кассе", "", "", "", "800000", "800000", "800000", "800000", "800000", "500000", "1000000"},
		}},
	}
	now := time.Date(2026, 10, 9, 15, 0, 0, 0, Almaty)
	out, _ := AppBundle(snap, now)
	mp, ok := out["monthlyPL"].(MonthlyPLView)
	if !ok {
		t.Fatalf("monthlyPL is %T", out["monthlyPL"])
	}
	if mp.Source != "server" || *mp.CurrentMonthIdx != 9 {
		t.Fatalf("source %q idx %d", mp.Source, *mp.CurrentMonthIdx)
	}
	oct := mp.Months[9]
	// April 800 000, September −300 000 dividends, October 500 000 + 50 000 − 20 000
	if oct.Kassa == nil || *oct.Kassa != 1030000 {
		t.Fatalf("October cash %v, want 1 030 000 (the stale sheet says 1 000 000)", oct.Kassa)
	}
	if oct.Revenue != 550000 || oct.Expenses != 20000 || oct.Profit != 530000 {
		t.Fatalf("October %+v", oct)
	}
	if sep := mp.Months[8]; sep.Kassa == nil || *sep.Kassa != 500000 || sep.Dividends != 300000 {
		t.Fatalf("September %+v", sep)
	}
	if mp.Months[2].Kassa != nil || mp.Months[10].Kassa != nil {
		t.Fatal("cash before April or after the current month must be empty")
	}
	if mp.Months[2].Revenue != 777 {
		t.Fatalf("March revenue %v", mp.Months[2].Revenue)
	}
	if mp.Kassa == nil || *mp.Kassa != 1030000 {
		t.Fatalf("kassa %v", mp.Kassa)
	}
	d := out["debet"].(BundleDebet)
	if d.Residual != 1030000 || d.NetProfit != 530000 {
		t.Fatalf("debet %+v", d)
	}
	b, _ := json.Marshal(mp)
	var back map[string]any
	_ = json.Unmarshal(b, &back)
	if back["ok"] != true || back["months"] == nil {
		t.Fatalf("json %s", b)
	}

	// No P&L structure on the server yet: the sheet's numbers, as before.
	snap.PL = nil
	out, _ = AppBundle(snap, now)
	if mp := out["monthlyPL"].(MonthlyPLView); mp.Source != "" || *mp.Months[9].Kassa != 1000000 {
		t.Fatalf("fallback %+v", mp.Months[9])
	}
}
