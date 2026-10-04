package club

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Reconcile compares what the server keeps with a copy of the Google Sheet
// (R32d: the sheet is no longer the source of truth). It never decides by
// itself what wins: it lists, table by table, the rows only the sheet has,
// the rows only the server has and the rows both have with different values,
// and it picks the rows the sheet may give the server without undoing the
// server's own changes:
//
//   - a row only the sheet has, dated on or after Since (the day the server
//     last took the sheet's data), is new in the sheet: it is taken;
//   - an older row only the sheet has was changed or removed on the server
//     since: the server's version stands, the row is only listed;
//   - a row both have with different values: the server's stands, listed;
//   - a whole sheet the server does not keep is taken as it is;
//   - the append-only logs the script kept (profit, CustDev, offer
//     acceptances, diagnostics, the wheel) take every row they miss.
//
// The ДДС is skipped when the sheet's ДДС was the server's own hourly copy
// (MirroredDDS): its rows only echo the server's.

// RecTable is how one table compares.
type RecTable struct {
	Name       string   `json:"name"`
	Server     int      `json:"server"`
	Sheet      int      `json:"sheet"`
	OnlySheet  int      `json:"onlySheet"`
	OnlyServer int      `json:"onlyServer"`
	Changed    int      `json:"changed"`
	Taken      int      `json:"taken"` // rows the server takes from the sheet
	Skipped    string   `json:"skipped,omitempty"`
	Samples    []string `json:"samples,omitempty"`
}

// Reconciliation is one comparison of the server with the sheet.
type Reconciliation struct {
	Since  string     `json:"since,omitempty"` // dd.mm.yyyy
	Tables []RecTable `json:"tables"`
	// Differences: rows that differ in any way (only one side or changed).
	Differences int `json:"differences"`
	Taken       int `json:"taken"`
}

// RecAdd is what the server takes from the sheet.
type RecAdd struct {
	Residents  []Resident
	Payments   []Payment
	Fines      []Fine
	Meetings   []Meeting
	Reports    []ReportEntry
	MeetingLog []MeetingLogEntry
	Settings   []Setting
	// NewSheets: whole sheets the server does not keep.
	NewSheets Sheets
	// AppendRows: rows appended to a sheet the server keeps (append-only logs).
	AppendRows Sheets
}

// Empty: nothing to take.
func (a *RecAdd) Empty() bool {
	if a == nil {
		return true
	}
	n := len(a.Residents) + len(a.Payments) + len(a.Fines) + len(a.Meetings) + len(a.Reports) + len(a.MeetingLog) + len(a.Settings)
	for _, r := range a.NewSheets {
		n += len(r) + 1
	}
	for _, r := range a.AppendRows {
		n += len(r)
	}
	return n == 0
}

// RecOptions tune a comparison.
type RecOptions struct {
	// Since: rows dated before it that only the sheet has are not taken.
	// Zero: every row only the sheet has is taken.
	Since time.Time
	// MirroredDDS: the sheet's ДДС was the server's copy, not compared.
	MirroredDDS bool
	// Samples kept per table (default 15).
	Samples int
}

// AppendOnlySheets: the logs the script kept, rows added, never changed in
// place; key columns (0-based) tell rows apart.
var AppendOnlySheets = map[string][]int{
	SheetProfit:       {0, 1, 2, 3},
	SheetNPS:          {0, 1, 3},
	SheetAcceptsLog:   {1},
	SheetDiagRequests: {0, 1, 2},
	SheetDiagnostics:  {0, 1},
	SheetWheel:        {0, 1, 2},
}

// sheets the comparison never takes rows from: they are parsed into the
// club tables, or are the script's own settings.
var recParsed = map[string]bool{SheetDebet: true, SheetDDS: true, SheetFines: true, SheetSchedule: true, SheetReports: true,
	SheetMeetingLog: true, SheetSettings: true, SheetFormer: true, SheetScriptProps: true}

func normKey(parts ...string) string {
	for i, p := range parts {
		parts[i] = strings.ToLower(strings.Join(strings.Fields(p), " "))
	}
	return strings.Join(parts, "|")
}

func recDMY(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.In(Almaty).Format("02.01.2006")
}

func recDay(t time.Time) time.Time {
	a := t.In(Almaty)
	return time.Date(a.Year(), a.Month(), a.Day(), 0, 0, 0, 0, Almaty)
}

// recNew: a row dated d is new in the sheet (on or after since).
func recNew(d time.Time, since time.Time) bool {
	if since.IsZero() {
		return true
	}
	if d.IsZero() {
		return false
	}
	return !recDay(d).Before(recDay(since))
}

type recSide[T any] struct {
	key  func(T) string
	val  func(T) string // values compared once keys match ("" = nothing to compare)
	date func(T) time.Time
	show func(T) string
}

// compare matches rows by key as multisets.
func compare[T any](name string, server, sheet []T, s recSide[T], o RecOptions, rep *Reconciliation) []T {
	t := RecTable{Name: name, Server: len(server), Sheet: len(sheet)}
	have := map[string][]T{}
	for _, r := range server {
		k := s.key(r)
		have[k] = append(have[k], r)
	}
	var take []T
	sample := func(txt string) {
		if len(t.Samples) < o.Samples {
			t.Samples = append(t.Samples, txt)
		}
	}
	for _, r := range sheet {
		k := s.key(r)
		if list := have[k]; len(list) > 0 {
			srv := list[0]
			have[k] = list[1:]
			if s.val != nil && s.val(srv) != s.val(r) {
				t.Changed++
				sample("изменено: " + s.show(r) + " → на сервере " + s.val(srv))
			}
			continue
		}
		t.OnlySheet++
		if recNew(s.date(r), o.Since) {
			take = append(take, r)
			t.Taken++
			sample("только в таблице, берём: " + s.show(r))
		} else {
			sample("только в таблице, старше переезда, оставлено как на сервере: " + s.show(r))
		}
	}
	for _, list := range have {
		t.OnlyServer += len(list)
		for _, r := range list {
			if len(t.Samples) >= o.Samples {
				break
			}
			sample("только на сервере: " + s.show(r))
		}
	}
	rep.Tables = append(rep.Tables, t)
	rep.Differences += t.OnlySheet + t.OnlyServer + t.Changed
	rep.Taken += t.Taken
	return take
}

func recI64(n int64) string { return strconv.FormatInt(n, 10) }

// Reconcile compares the server's snapshot with the sheet's.
func Reconcile(server, sheet *Snapshot, o RecOptions) (*Reconciliation, *RecAdd) {
	if o.Samples <= 0 {
		o.Samples = 15
	}
	if server == nil {
		server = &Snapshot{}
	}
	rep := &Reconciliation{Tables: []RecTable{}}
	if !o.Since.IsZero() {
		rep.Since = recDMY(o.Since)
	}
	add := &RecAdd{NewSheets: Sheets{}, AppendRows: Sheets{}}
	if sheet == nil {
		return rep, add
	}
	// Residents: by name. A resident the sheet alone has is always taken: a
	// person is not deleted on the server, only marked former.
	add.Residents = compare("Резиденты", server.Residents, sheet.Residents, recSide[Resident]{
		key: func(r Resident) string { return normKey(r.Name) },
		val: func(r Resident) string {
			return fmt.Sprintf("tg %d, %s, тариф %d, встреч %d/%d, бывший %v", r.TgID, r.Format, r.Tariff, r.Done, r.Granted, r.Former)
		},
		date: func(Resident) time.Time { return time.Now() },
		show: func(r Resident) string { return r.Name },
	}, o, rep)

	if o.MirroredDDS {
		rep.Tables = append(rep.Tables, RecTable{Name: "ДДС", Server: len(server.Payments), Sheet: len(sheet.Payments),
			Skipped: "ДДС в таблице был копией сервера"})
	} else {
		add.Payments = compare("ДДС", server.Payments, sheet.Payments, recSide[Payment]{
			key: func(p Payment) string {
				return normKey(recDMY(p.Date), recI64(p.Income), recI64(p.Expense), p.Resident, p.IncomeCat, p.ExpenseCat)
			},
			date: func(p Payment) time.Time { return p.Date },
			show: func(p Payment) string {
				return fmt.Sprintf("%s +%d −%d %s %s %s", recDMY(p.Date), p.Income, p.Expense, p.IncomeCat, p.Resident, p.ExpenseCat)
			},
		}, o, rep)
	}

	add.Fines = compare("Штрафы", server.Fines, sheet.Fines, recSide[Fine]{
		key: func(f Fine) string { return normKey(f.Name, f.Type, recDMY(f.Date), recI64(f.Amount)) },
		val: func(f Fine) string {
			if f.Paid || strings.TrimSpace(f.Status) == "Оплатил" {
				return "оплачен"
			}
			return "не оплачен"
		},
		date: func(f Fine) time.Time { return f.Date },
		show: func(f Fine) string { return fmt.Sprintf("%s, %s, %d, %s", f.Name, f.Type, f.Amount, recDMY(f.Date)) },
	}, o, rep)

	add.Meetings = compare("Встречи", server.Meetings, sheet.Meetings, recSide[Meeting]{
		key: func(m Meeting) string { return normKey(m.Resident, recDMY(m.Date), m.Time) },
		val: func(m Meeting) string {
			if m.Done {
				return "проведена"
			}
			return "не проведена"
		},
		date: func(m Meeting) time.Time { return m.Date },
		show: func(m Meeting) string { return fmt.Sprintf("%s %s %s", m.Resident, recDMY(m.Date), m.Time) },
	}, o, rep)

	reportKey := func(r ReportEntry) string {
		who := r.Name
		if r.TgUserID != 0 {
			who = recI64(r.TgUserID)
		}
		txt := []rune(strings.Join(strings.Fields(r.Text), " "))
		if len(txt) > 60 {
			txt = txt[:60]
		}
		return normKey(who, recDMY(r.At), string(txt))
	}
	add.Reports = compare("Отчёты", server.Reports, sheet.Reports, recSide[ReportEntry]{
		key:  reportKey,
		date: func(r ReportEntry) time.Time { return r.At },
		show: func(r ReportEntry) string { return r.Name + " " + recDMY(r.At) },
	}, o, rep)

	add.MeetingLog = compare("Лог встреч", server.MeetingLog, sheet.MeetingLog, recSide[MeetingLogEntry]{
		key:  func(m MeetingLogEntry) string { return normKey(recDMY(m.Date), m.Resident) },
		date: func(m MeetingLogEntry) time.Time { return m.Date },
		show: func(m MeetingLogEntry) string { return m.Resident + " " + recDMY(m.Date) },
	}, o, rep)

	// Bot texts: a text the server lacks is taken; a different one stays.
	add.Settings = compare("Тексты бота", server.Settings, sheet.Settings, recSide[Setting]{
		key:  func(s Setting) string { return s.Key },
		val:  func(s Setting) string { return s.Value },
		date: func(Setting) time.Time { return time.Now() },
		show: func(s Setting) string { return s.Key },
	}, o, rep)

	// Sheets kept as displayed.
	var names []string
	for n := range sheet.Raw {
		if !recParsed[n] {
			names = append(names, n)
		}
	}
	sort.Strings(names)
	for _, n := range names {
		rows := sheet.Raw[n]
		srv, has := server.Raw[n]
		if !has {
			if len(rows) == 0 {
				continue
			}
			add.NewSheets[n] = rows
			t := RecTable{Name: n, Sheet: recDataRows(rows), OnlySheet: recDataRows(rows), Taken: recDataRows(rows),
				Samples: []string{"лист есть только в таблице, берём целиком"}}
			rep.Tables = append(rep.Tables, t)
			rep.Differences += t.OnlySheet
			rep.Taken += t.Taken
			continue
		}
		cols, appendOnly := AppendOnlySheets[n]
		rowKey := func(r []string) string {
			if appendOnly {
				parts := make([]string, len(cols))
				for i, c := range cols {
					parts[i] = recCell(r, c)
				}
				return normKey(parts...)
			}
			return normKey(r...)
		}
		t := RecTable{Name: n, Server: recDataRows(srv), Sheet: recDataRows(rows)}
		have := map[string]int{}
		for i, r := range srv {
			if i > 0 && !recBlank(r) {
				have[rowKey(r)]++
			}
		}
		for i, r := range rows {
			if i == 0 || recBlank(r) {
				continue
			}
			k := rowKey(r)
			if have[k] > 0 {
				have[k]--
				continue
			}
			t.OnlySheet++
			if appendOnly {
				add.AppendRows[n] = append(add.AppendRows[n], r)
				t.Taken++
				if len(t.Samples) < o.Samples {
					t.Samples = append(t.Samples, "строка только в таблице, берём: "+strings.Join(recTrim(r, 4), " · "))
				}
			} else if len(t.Samples) < o.Samples {
				t.Samples = append(t.Samples, "строка только в таблице, оставлено как на сервере: "+strings.Join(recTrim(r, 4), " · "))
			}
		}
		for _, c := range have {
			t.OnlyServer += c
		}
		if t.OnlySheet+t.OnlyServer == 0 {
			continue // the same: not listed
		}
		rep.Tables = append(rep.Tables, t)
		rep.Differences += t.OnlySheet + t.OnlyServer
		rep.Taken += t.Taken
	}
	return rep, add
}

func recCell(r []string, i int) string {
	if i < len(r) {
		return r[i]
	}
	return ""
}

func recBlank(r []string) bool {
	for _, c := range r {
		if strings.TrimSpace(c) != "" {
			return false
		}
	}
	return true
}

func recDataRows(rows [][]string) int {
	n := 0
	for i, r := range rows {
		if i > 0 && !recBlank(r) {
			n++
		}
	}
	return n
}

func recTrim(r []string, n int) []string {
	var out []string
	for _, c := range r {
		if c = strings.TrimSpace(c); c != "" {
			out = append(out, c)
			if len(out) == n {
				break
			}
		}
	}
	return out
}

// Summary is one line for the log.
func (r *Reconciliation) Summary() string {
	if r == nil {
		return "нет сверки"
	}
	var parts []string
	for _, t := range r.Tables {
		if t.Skipped != "" || t.OnlySheet+t.OnlyServer+t.Changed == 0 {
			continue
		}
		parts = append(parts, fmt.Sprintf("%s: только в таблице %d (взято %d), только на сервере %d, изменено %d",
			t.Name, t.OnlySheet, t.Taken, t.OnlyServer, t.Changed))
	}
	if len(parts) == 0 {
		return "сервер и таблица совпадают"
	}
	return fmt.Sprintf("различий %d, взято из таблицы %d. ", r.Differences, r.Taken) + strings.Join(parts, "; ")
}
