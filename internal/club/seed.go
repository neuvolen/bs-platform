package club

import (
	"encoding/json"
	"sort"
	"strings"
	"time"
)

// The platform's club sections (Учёт, Резиденты, Штрафы, Расписание) were
// built on data baked into the page. LiveSeed builds the same structures from
// the club data on the server, so those sections show today's numbers without
// changing how they are drawn. Parts the server does not hold yet (NPS, leads,
// resident tasks, profit reports) are kept from the page's own snapshot.

var monthNames = []string{"Январь", "Февраль", "Март", "Апрель", "Май", "Июнь",
	"Июль", "Август", "Сентябрь", "Октябрь", "Ноябрь", "Декабрь"}

func day(t *time.Time) string {
	if t == nil || t.IsZero() {
		return ""
	}
	return t.In(Almaty).Format("02.01.2006")
}

// SeedResident is one line of the platform's RESIDENTS.
type SeedResident struct {
	Name      string `json:"name"`
	Paid      int64  `json:"paid"`
	Rest      int64  `json:"rest"`
	DebtRenew int64  `json:"debtRenew"`
	Fines     int64  `json:"fines"`
	Total     int64  `json:"total"`
	Tariff    int64  `json:"tariff"`
	Start     string `json:"start"`
	Format    string `json:"format"`
	Done      int64  `json:"done"`
	Granted   int64  `json:"granted"`
	Left      int64  `json:"left"`
	Months    int64  `json:"months"`
}

// SeedFine is one line of the platform's FINES.
type SeedFine struct {
	Res    string `json:"res"`
	Type   string `json:"type"`
	Amount int64  `json:"amount"`
	Date   string `json:"date"`
	Status string `json:"status"`
	// Row: the sheet row, sent back to find the fine (as the app does).
	Row int `json:"row,omitempty"`
}

// SeedMeeting is one line of the platform's SDATA.schedule.
type SeedMeeting struct {
	Res    string `json:"res"`
	Date   string `json:"date"`
	Time   string `json:"time"`
	Place  string `json:"place"`
	Link   string `json:"link"`
	Format string `json:"format"`
	Done   bool   `json:"done,omitempty"`
}

// SeedPLRow is one line of the platform's PL_ROWS: kind head, item or total.
type SeedPLRow struct {
	Name string  `json:"name"`
	Kind string  `json:"kind"`
	Vals []int64 `json:"vals"`
}

// SeedResidents: active residents with their money, as the debts sheet shows it.
func SeedResidents(snap *Snapshot) []SeedResident {
	out := []SeedResident{}
	for _, d := range Debet(snap.Residents, snap.Fines) {
		if d.Admin || d.Name == "" {
			continue
		}
		out = append(out, SeedResident{
			Name: d.Name, Paid: d.PaidEntry, Rest: d.RestEntry, DebtRenew: d.RenewDebt,
			Fines: d.FinesUnpaid, Total: d.TotalDebt, Tariff: d.Tariff, Start: day(d.JoinedAt),
			Format: d.Format, Done: d.Done, Granted: d.Granted, Left: d.MeetingsLeft, Months: d.Months,
		})
	}
	return out
}

func SeedFines(fines []Fine) []SeedFine {
	out := make([]SeedFine, 0, len(fines))
	for _, f := range fines {
		st := "Не оплатил"
		if f.Paid {
			st = "Оплатил"
		}
		d := f.Date
		out = append(out, SeedFine{Res: f.Name, Type: f.Type, Amount: f.Amount, Date: day(&d), Status: st, Row: f.Row})
	}
	return out
}

func SeedSchedule(in []Meeting) []SeedMeeting {
	ms := append([]Meeting(nil), in...)
	sort.SliceStable(ms, func(i, j int) bool { return ms[i].Date.Before(ms[j].Date) })
	out := make([]SeedMeeting, 0, len(ms))
	for _, m := range ms {
		d := m.Date
		f := "offline"
		if m.Online || strings.Contains(m.Link, "meet.google") {
			f = "online"
		}
		out = append(out, SeedMeeting{Res: m.Resident, Date: day(&d), Time: m.Time, Place: m.Place, Link: m.Link, Format: f, Done: m.Done})
	}
	return out
}

// SeedPL lays the P&L out the way the platform's Учёт draws it: from the
// month the cash was reconciled (April) to the current month.
func SeedPL(pl *PL) ([]SeedPLRow, []string) {
	if pl == nil {
		return []SeedPLRow{}, []string{}
	}
	from, to := CashStartMonth, pl.UpTo
	if to < from {
		from = 1
	}
	var months []string
	for m := from; m <= to; m++ {
		months = append(months, monthNames[m-1])
	}
	vals := func(f func(PLMonth) int64) []int64 {
		v := make([]int64, 0, len(months))
		for m := from; m <= to; m++ {
			v = append(v, f(pl.Months[m-1]))
		}
		return v
	}
	zero := make([]int64, len(months))
	rows := []SeedPLRow{{Name: "Остаток на начало", Kind: "total", Vals: zero}, {Name: "ДОХОДЫ", Kind: "head", Vals: zero}}
	for _, name := range pl.IncomeRows {
		n := name
		rows = append(rows, SeedPLRow{Name: n, Kind: "item", Vals: vals(func(m PLMonth) int64 { return m.Income[n] })})
	}
	rows = append(rows, SeedPLRow{Name: "ИТОГО ДОХОДЫ", Kind: "total", Vals: vals(func(m PLMonth) int64 { return m.IncomeSum })},
		SeedPLRow{Name: "РАСХОДЫ", Kind: "head", Vals: zero})
	for _, name := range pl.ExpenseRows {
		n := name
		rows = append(rows, SeedPLRow{Name: n, Kind: "item", Vals: vals(func(m PLMonth) int64 { return m.Expense[n] })})
	}
	rows = append(rows,
		SeedPLRow{Name: "ИТОГО РАСХОДЫ", Kind: "total", Vals: vals(func(m PLMonth) int64 { return m.Expenses })},
		SeedPLRow{Name: "ЧИСТАЯ ПРИБЫЛЬ", Kind: "total", Vals: vals(func(m PLMonth) int64 { return m.Profit })},
		SeedPLRow{Name: "Дивиденды", Kind: "item", Vals: vals(func(m PLMonth) int64 { return m.Dividends })},
		SeedPLRow{Name: "ОСТАТОК", Kind: "total", Vals: vals(func(m PLMonth) int64 { return m.Profit - m.Dividends })},
		SeedPLRow{Name: "На кассе", Kind: "total", Vals: vals(func(m PLMonth) int64 { return m.Cash })},
	)
	return rows, months
}

// LiveSeed builds the platform's club data from snap. static is the page's
// own snapshot, for the parts the server does not hold.
func LiveSeed(snap *Snapshot, static string, now time.Time) (string, error) {
	var base map[string]json.RawMessage
	if json.Unmarshal([]byte(static), &base) != nil || base == nil {
		base = map[string]json.RawMessage{}
	}
	var sdata map[string]json.RawMessage
	if json.Unmarshal(base["SDATA"], &sdata) != nil || sdata == nil {
		sdata = map[string]json.RawMessage{}
	}
	// How many meetings a package has, from the page's snapshot: the server
	// does not keep it, and the visit counters need it.
	var oldVisits []struct {
		Name string  `json:"name"`
		Per  float64 `json:"per"`
	}
	_ = json.Unmarshal(sdata["visits"], &oldVisits)
	per := map[string]float64{}
	for _, v := range oldVisits {
		per[NormName(v.Name)] = v.Per
	}
	type visit struct {
		Name   string  `json:"name"`
		Per    float64 `json:"per"`
		Done   int64   `json:"done"`
		Months float64 `json:"months"`
	}
	visits := []visit{}
	residents := SeedResidents(snap)
	for _, r := range residents {
		p := per[NormName(r.Name)]
		if p == 0 {
			p = 4
		}
		visits = append(visits, visit{Name: r.Name, Per: p, Done: r.Done, Months: float64(r.Months)})
	}
	mustRaw := func(v any) json.RawMessage { b, _ := json.Marshal(v); return b }
	sdata["schedule"] = mustRaw(SeedSchedule(snap.Meetings))
	sdata["visits"] = mustRaw(visits)

	year := now.In(Almaty).Year()
	upTo := int(now.In(Almaty).Month())
	if snap.PL != nil && snap.PL.Year != 0 && snap.PL.Year < year {
		year, upTo = snap.PL.Year, 12
	}
	var plRows []SeedPLRow
	var months []string
	if snap.PL != nil && len(snap.PL.Rows) > 0 {
		plRows, months = SeedPL(BuildPL(snap.Payments, snap.PL, year, upTo))
	} else {
		plRows, months = SeedPL(nil)
	}
	out := map[string]any{
		"RESIDENTS": residents,
		"FINES":     SeedFines(snap.Fines),
		"SDATA":     sdata,
		"PL_ROWS":   plRows,
		"PL_MONTHS": months,
		"PL_YEAR":   year,
		// No timestamp here: an import that changed nothing must not make
		// every open platform reload the same data.
		"LIVE": map[string]string{"source": "server"},
	}
	b, err := json.Marshal(out)
	return string(b), err
}
