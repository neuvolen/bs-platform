package club

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func at(s string) time.Time {
	t, ok := Date(s)
	if !ok {
		panic(s)
	}
	return t
}

func ptr(t time.Time) *time.Time { return &t }

func bundleSnap() *Snapshot {
	j := at("19.02.2026")
	return &Snapshot{
		Residents: []Resident{
			{Name: "Рустам", TgID: 453800951, Format: "Онлайн", Admin: true, Exception: true, JoinedAt: ptr(at("28.12.2024")), Months: 21},
			{Name: "Арлан", TgID: 1319900316, Format: "Офлайн", Tariff: 1000000, Granted: 36, Done: 4, RestEntry: 800000, Source: "Сарафан", JoinedAt: &j},
			{Name: "Асет", TgID: 478757502, Format: "", Tariff: 100000, Granted: 3, Done: 2, PaidEntry: 100000, RenewDebt: -5, Partner: "Арлан"},
			{Name: "Ушёл", Former: true, RestEntry: 50000},
			{Name: "Архив", Former: true, Archived: true, RestEntry: 70000},
		},
		Fines: []Fine{
			{Name: "Арлан", Type: "Не сдан отчёт", Amount: 10000, Date: at("26.09.2026"), Row: 3, Status: "Не оплатил"},
			{Name: "асет", Type: "Опоздание", Amount: 5000, Date: at("27.09.2026"), Row: 4, Status: "Оплатил", Paid: true},
			{Name: "Асет", Type: "Прочее", Amount: 3000, Date: at("01.10.2026")}, // written by the server, no row yet
		},
		Reports: []ReportEntry{
			{Name: "Арлан", Text: "  отчёт  ", TgUserID: 1319900316, At: at("01.10.2026 22:10"), ShownAt: ptr(at("01.10.2026 22:10"))},
			{Name: "Асет", Text: strings.Repeat("я", 250), At: at("02.10.2026 00:20"), ShownAt: ptr(at("01.10.2026")), Late: true},
			{Name: "Старый", At: at("01.08.2026 22:00"), ShownAt: ptr(at("01.08.2026 22:00"))},
			{Name: "Без даты", At: at("01.10.2026 20:00")},
		},
		Meetings: []Meeting{
			{Resident: "Арлан", Date: at("05.10.2026"), Time: "15:00", Row: 5, LinkCell: "г.Алматы, Достык 44"},
			{Resident: "Асет", Date: at("06.10.2026"), Time: "11:00", Row: 7, Done: true},
			{Resident: "Чужой", Date: at("07.10.2026"), Time: "10:00"},
			{Resident: "Арлан", Date: at("20.09.2026"), Time: "15:00", Row: 3}, // past
			{Resident: "Пусто", Date: at("08.10.2026"), Time: "", Row: 9},      // no time, place or link
		},
		MeetingLog: []MeetingLogEntry{{Date: at("29.09.2026"), Resident: "Арлан", Time: "29.09.2026 16:00"}},
		Raw: Sheets{SheetPL: {
			{"", "2026"}, {"Статья", "1"}, {"Статья", "Январь"}, {"ДОХОДЫ"},
			{"ИТОГО ДОХОДЫ", "1,000", "2,000", "0", "0", "0", "0", "0", "0", "0", "500", "0", "0"},
			{"ИТОГО РАСХОДЫ", "400", "500", "", "", "", "", "", "", "", "100"},
			{"ЧИСТАЯ ПРИБЫЛЬ", "600", "1,500", "0", "", "", "", "", "", "", "400"},
			{"РЕНТАБЕЛЬНОСТЬ", "60%", "0.75", "", "", "", "", "", "", "", "80"},
			{"Дивиденды", "100", "-200"},
			{"На кассе", "", "", "", "300", "", "", "", "", "", "1,234"},
		}},
	}
}

func TestAppBundleSections(t *testing.T) {
	now := at("02.10.2026 12:00")
	parts, built := AppBundle(bundleSnap(), now)
	if strings.Join(built, ",") != "residents,fines,logs,schedule,debet,totalDebt,monthlyPL,doneMeetings" {
		t.Fatalf("built %v", built)
	}
	res := parts["residents"].([]BundleResident)
	if len(res) != 4 {
		t.Fatalf("the debet sheet only, archived left out: %+v", res)
	}
	ar, as := res[1], res[2]
	if ar.Debt != 810000 || ar.Fines != 10000 || ar.MeetingsLeft != 32 || ar.ChatID != "1319900316" || ar.DateIn != "19.02.2026" ||
		ar.StartDate != "февраля 2026" || ar.Months != 7 || ar.CyclesPaid != 36 || ar.Format != "Офлайн" {
		t.Fatalf("Арлан %+v", ar)
	}
	// Fines by name whatever the case; negative debt counts as 0; empty format is «Офлайн».
	if as.Fines != 3000 || as.Debt != 3000 || as.DebtRenew != -5 || as.Format != "Офлайн" || as.DateIn != "" || as.Partner != "Арлан" {
		t.Fatalf("Асет %+v", as)
	}
	if res[0].CyclesPaid != 1 || res[0].Months != 21 {
		t.Fatalf("Рустам %+v", res[0])
	}
	if total := parts["totalDebt"].(int64); total != 813000 {
		t.Fatalf("totalDebt without admins and former: %d", total)
	}
	fines := parts["fines"].([]BundleFine)
	if fines[2].Row != 5 || fines[2].Status != "Не оплатил" || fines[2].Date != "01.10.2026" || fines[1].Status != "Оплатил" {
		t.Fatalf("fines %+v", fines)
	}
	logs := parts["logs"].([]BundleLog)
	if len(logs) != 2 || logs[0].Time != "22:10" || logs[0].Text != "  отчёт  " || logs[1].Date != "01.10.2026" || logs[1].Time != "23:59" ||
		len([]rune(logs[1].Text)) != 200 || !logs[1].Late || logs[1].ChatID != "" {
		t.Fatalf("logs %+v", logs)
	}
	sched := parts["schedule"].([]BundleMeeting)
	if len(sched) != 3 || sched[0].Link != "г.Алматы, Достык 44" || sched[0].Format != "Офлайн" || !sched[1].Done ||
		sched[2].Row != 10 || sched[2].Format != "Офлайн" {
		t.Fatalf("schedule %+v", sched)
	}
	d := parts["debet"].(BundleDebet)
	if d.NetProfit != 400 || d.Residual != 1234 || d.Dividends != 0 || d.UnpaidFines != 13000 || d.UnpaidCount != 2 {
		t.Fatalf("debet %+v", d)
	}
	done := parts["doneMeetings"].([]BundleDoneMeeting)
	if len(done) != 1 || done[0].Date != "29.09.2026" || done[0].Time != "29.09.2026 16:00" || !done[0].Done {
		t.Fatalf("done %+v", done)
	}
}

func TestMonthlyPLAsTheScript(t *testing.T) {
	pl := MonthlyPL(bundleSnap().Raw[SheetPL], at("02.10.2026 12:00"))
	b, _ := json.Marshal(pl)
	var v struct {
		OK     bool
		Kassa  *float64
		Months []struct {
			Revenue, Expenses, Profit, Dividends float64
			Margin                               int64
			HasData                              bool
			Saldo, Kassa, KassaStart             *float64
		}
		Year struct {
			Revenue, Profit float64
			Margin          int64
			Saldo           *float64
		}
		CurrentMonthIdx int
	}
	if err := json.Unmarshal(b, &v); err != nil {
		t.Fatal(err)
	}
	m := v.Months
	if !v.OK || len(m) != 12 || m[0].Revenue != 1000 || m[0].Margin != 60 || m[1].Margin != 75 || m[9].Margin != 80 || m[1].Dividends != -200 {
		t.Fatalf("months %s", b)
	}
	// No «ОСТАТОК» line: saldo = profit − dividends; kassa from «На кассе», null where empty.
	if *m[1].Saldo != 1700 || m[0].Kassa != nil || *m[3].Kassa != 300 || m[4].KassaStart == nil || *m[4].KassaStart != 300 || m[5].KassaStart != nil {
		t.Fatalf("saldo/kassa %s", b)
	}
	if v.Year.Revenue != 3500 || v.Year.Profit != 2500 || v.Year.Margin != 71 || *v.Year.Saldo != 400 || *v.Kassa != 1234 || v.CurrentMonthIdx != 9 {
		t.Fatalf("year %s", b)
	}
	if e := MonthlyPL(nil, time.Now()); e.OK || e.Error != "Лист PL не найден" {
		t.Fatalf("no sheet %+v", e)
	}
	if e := MonthlyPL([][]string{{"x"}, {"y"}}, time.Now()); e.OK || e.Error != "Лист PL пустой" {
		t.Fatalf("empty %+v", e)
	}
}

func TestReportShownAt(t *testing.T) {
	sent := at("02.09.2026 23:08")
	sent = sent.Add(59*time.Second + 760*time.Millisecond)
	cases := []struct {
		shown *time.Time
		want  string
	}{
		{nil, ""},
		{ptr(at("02.09.2026 23:09")), "02.09.2026 23:08"}, // the shown minute is rounded
		{ptr(at("02.09.2026")), "02.09.2026 23:08"},       // shown without time: the moment it was sent
		{ptr(at("01.09.2026")), "01.09.2026 23:59"},       // put on the day before
		{ptr(at("01.09.2026 23:59")), "01.09.2026 23:59"},
	}
	for _, c := range cases {
		got := ReportShownAt(ReportEntry{At: sent, ShownAt: c.shown})
		s := ""
		if !got.IsZero() {
			s = got.In(Almaty).Format("02.01.2006 15:04")
		}
		if s != c.want {
			t.Fatalf("%v: %s, want %s", c.shown, s, c.want)
		}
	}
}

func TestSlice16(t *testing.T) {
	if Slice16("ab😀cd", 3) != "ab" || Slice16("ab😀cd", 4) != "ab😀" || Slice16("abc", 5) != "abc" {
		t.Fatal("JavaScript slice counts UTF-16 units")
	}
}

func TestNum(t *testing.T) {
	for in, want := range map[string]float64{"1,234,567": 1234567, "-39,590": -39590, "": 0, "78%": 0.78, "−500": -500, "0.06": 0.06} {
		if got, ok := Num(in); !ok || got != want {
			t.Fatalf("%q: %v %v", in, got, ok)
		}
	}
	if _, ok := Num("абв"); ok {
		t.Fatal("text is not a number")
	}
}

func decode(t *testing.T, s string) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal([]byte(s), &m); err != nil {
		t.Fatal(err)
	}
	return m
}

func TestDiffBundleNormalises(t *testing.T) {
	now := at("02.10.2026 12:00")
	sheet := decode(t, `{
		"residents":[{"name":"Асет","debt":10000,"source":"Сарафан ","note":null},{"name":"Арлан","debt":0}],
		"logs":[{"date":"02.09.2026","time":"23:09","name":"Азамат","text":"  длинный отчёт","chatId":"1","late":false},
		        {"date":"02.09.2026","time":"12:30","name":"Край","text":"x","chatId":"","late":false},
		        {"date":"01.10.2026","time":"10:00","name":"Арлан","text":"a","chatId":"2","late":false}],
		"doneMeetings":[{"res":"Арлан","name":"Арлан","date":"29.09.2026","time":"Tue Sep 29 2026 16:00:00 GMT+0500","done":true}],
		"debet":{"netProfit":400,"residual":"1234"},
		"totalDebt":10000,
		"monthlyPL":{"ok":true,"kassa":null,"months":[{"saldo":1.0}]}
	}`)
	server := decode(t, `{
		"residents":[{"name":"Арлан","debt":0},{"name":"Асет","debt":10000.0,"source":"Сарафан"}],
		"logs":[{"date":"01.10.2026","time":"10:00","name":"Арлан","text":"a","chatId":"2","late":false},
		        {"date":"02.09.2026","time":"23:08","name":"Азамат","text":"длинный отчёт","chatId":"1","late":false}],
		"doneMeetings":[{"res":"Арлан","name":"Арлан","date":"29.09.2026","time":"29.09.2026 16:00","done":true}],
		"debet":{"netProfit":400.0,"residual":1234},
		"totalDebt":10000,
		"monthlyPL":{"ok":true,"months":[{"saldo":1}]}
	}`)
	all := []string{"residents", "logs", "doneMeetings", "debet", "totalDebt", "monthlyPL"}
	if d := DiffBundle(sheet, server, all, now); len(d) != 0 {
		t.Fatalf("only form differs: %+v", d)
	}

	// Real differences are found and named.
	server["totalDebt"] = 20000.0
	server["residents"].([]any)[1].(map[string]any)["debt"] = 0.0
	server["doneMeetings"] = []any{}
	server["logs"].([]any)[0].(map[string]any)["late"] = true
	d := DiffBundle(sheet, server, all, now)
	got := map[string]string{}
	for _, x := range d {
		got[x.Field] = x.Sheet + " → " + x.Server
	}
	want := map[string]string{
		"residents[Асет].debt":              "10000 → 0",
		"totalDebt":                         "10000 → 20000",
		"doneMeetings[Арлан|29.09.2026]":    "есть → —",
		"logs[01.10.2026|10:00|Арлан].late": "false → true",
	}
	if len(got) != len(want) {
		t.Fatalf("diff %+v", d)
	}
	for k, v := range want {
		if got[k] != v {
			t.Fatalf("%s: %q, want %q (all %+v)", k, got[k], v, got)
		}
	}
}

func TestDiffBundleDuplicates(t *testing.T) {
	sheet := decode(t, `{"fines":[{"name":"А","kind":"x","amount":1,"date":"d","row":3},{"name":"А","kind":"x","amount":1,"date":"d","row":4}]}`)
	server := decode(t, `{"fines":[{"name":"А","kind":"x","amount":1,"date":"d","row":3}]}`)
	d := DiffBundle(sheet, server, []string{"fines"}, time.Now())
	if len(d) != 1 || d[0].Field != "fines[А|x|1|d#2]" || d[0].Sheet != "есть" {
		t.Fatalf("a repeated row counts: %+v", d)
	}
}

func TestParseKeepsWhatTheBundleShows(t *testing.T) {
	s, _, err := Parse(Sheets{
		SheetDebet: {{"", "BS. ДЕБЕТ"}, {"№", "Имя резидента", "Тариф тг", "Встреч оплачено", "Встреч проведено", "Осталось", "Оплачено (вход)",
			"Остаток (вход)", "Долг продление", "Штрафы", "ОБЩИЙ ДОЛГ", "Формат", "Chat ID", "Бывший", "Исключение", "Админ"},
			{"1", "Асет", "100,000", "3", "2", "1", "0", "0", "0", "0", "0", "Офлайн", "478757502", "Нет", "Нет", "Нет"}},
		SheetFormer:     {{"Имя", "Оплачено"}, {"Ушедший", "10"}},
		SheetDDS:        {{"Дата", "Приход", "Расход", "Источник", "Категория +", "Категория -"}},
		SheetFines:      {{"РЕЕСТР"}, {"№", "Имя резидента", "Тип", "Сумма", "Дата", "Статус"}, {"1", "Асет", "Опоздание", "5,000", "26.09.2026", ""}},
		SheetSchedule:   {{"РАСПИСАНИЕ"}, {"№", "Резидент", "Дата", "Время", "Адрес", "Meet"}, {}, {"2", "Асет", "06.10.2026", "15:00", "", "г.Алматы", "", "Да"}},
		SheetReports:    {{"Дата"}, {"01.10.2026 22:10", "u", "Асет", "отчёт", "1790874600000", "478757502", "9"}},
		SheetMeetingLog: {{"Дата", "Резидент", "Дата+время записи"}, {"29.09.2026", "Асет", "29.09.2026 16:00"}},
		SheetProfiles:   {{"Chat ID"}, {"478757502", "Ниша"}},
		SheetPL:         {{"", "2026"}, {"Статья"}, {"Статья"}, {"ДОХОДЫ"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	f, m := s.Fines[0], s.Meetings[0]
	if f.Row != 3 || f.Status != "Не оплатил" || m.Row != 4 || m.LinkCell != "г.Алматы" || m.AddrCell != "" || m.HCell != "Да" || m.Place != "г.Алматы" {
		t.Fatalf("fine %+v meeting %+v", f, m)
	}
	if s.Reports[0].ShownAt == nil || s.Reports[0].ShownAt.Format("15:04") != "22:10" || s.MeetingLog[0].Time != "29.09.2026 16:00" {
		t.Fatalf("reports %+v log %+v", s.Reports, s.MeetingLog)
	}
	if !s.Residents[1].Archived || s.Residents[0].Archived || len(s.Raw) != 2 || s.Raw[SheetProfiles] == nil || s.Raw[SheetPL] == nil {
		t.Fatalf("archived / raw sheets: %+v %v", s.Residents, s.Raw)
	}
}
