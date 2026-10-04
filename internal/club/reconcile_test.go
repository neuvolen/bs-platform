package club

import (
	"context"
	"strings"
	"testing"
	"time"
)

func rd(s string) time.Time {
	t, ok := Date(s)
	if !ok {
		panic(s)
	}
	return t
}

func recTable(r *Reconciliation, name string) RecTable {
	for _, t := range r.Tables {
		if t.Name == name {
			return t
		}
	}
	return RecTable{Name: name}
}

// R32d: what the server takes from a copy of the sheet, and what it keeps.
func TestReconcileTakesOnlyWhatTheSheetAloneHas(t *testing.T) {
	since := rd("01.10.2026")
	server := &Snapshot{
		Residents: []Resident{{Name: "Асет", TgID: 1, Format: "Офлайн"}, {Name: "Новый на сервере"}},
		Payments: []Payment{
			{Date: rd("15.09.2026"), Income: 100000, Resident: "Асет", IncomeCat: "БХ Трекинг"},
			{Date: rd("02.10.2026"), Income: 50000, Resident: "Асет", IncomeCat: "БХ Трекинг"}, // edited on the server (was 40 000)
		},
		Fines:    []Fine{{Name: "Асет", Type: "Не сдан отчёт", Amount: 10000, Date: rd("20.09.2026"), Paid: true, Status: "Оплатил"}},
		Meetings: []Meeting{{Resident: "Асет", Date: rd("05.10.2026"), Time: "12:00"}},
		Settings: []Setting{{Key: "hello", Value: "Привет (сервер)"}},
		Raw: Sheets{
			SheetProfit: {{"Дата", "Резидент", "Выручка", "Прибыль"}, {"01.09.2026", "Асет", "1,000", "100"}},
			SheetLeads:  {{"Дата", "Имя", "Телефон"}, {"01.10.2026", "Лид", "+7701 (статус сервера)"}},
		},
	}
	sheet := &Snapshot{
		Residents: []Resident{{Name: "Асет", TgID: 1, Format: "Онлайн"}, {Name: "Дана", TgID: 2}},
		Payments: []Payment{
			{Date: rd("15.09.2026"), Income: 100000, Resident: "Асет", IncomeCat: "БХ Трекинг"},
			{Date: rd("02.10.2026"), Income: 40000, Resident: "Асет", IncomeCat: "БХ Трекинг"}, // the old version: new date, so taken
			{Date: rd("10.09.2026"), Income: 7000, Resident: "Асет", IncomeCat: "Штраф"},       // older than the last import: the server removed it
			{Date: rd("03.10.2026"), Expense: 2500, ExpenseCat: "Кофе"},                        // new in the sheet
		},
		Fines: []Fine{
			{Name: "Асет", Type: "Не сдан отчёт", Amount: 10000, Date: rd("20.09.2026"), Status: "Не оплатил"}, // changed: the server's stands
			{Name: "Дана", Type: "Опоздание", Amount: 5000, Date: rd("03.10.2026")},
		},
		Meetings: []Meeting{{Resident: "Асет", Date: rd("05.10.2026"), Time: "12:00", Done: true}, {Resident: "Дана", Date: rd("06.10.2026"), Time: "15:00"}},
		Reports:  []ReportEntry{{At: rd("02.10.2026").Add(20 * time.Hour), Name: "Асет", Text: "Отчёт"}},
		Settings: []Setting{{Key: "hello", Value: "Привет (таблица)"}, {Key: "bye", Value: "Пока"}},
		Raw: Sheets{
			SheetProfit:         {{"Дата", "Резидент", "Выручка", "Прибыль"}, {"01.09.2026", "Асет", "1,000", "100"}, {"01.10.2026", "Дана", "5,000", "500"}},
			SheetLeads:          {{"Дата", "Имя", "Телефон"}, {"01.10.2026", "Лид", "+7701 (статус таблицы)"}},
			"Подписчики канала": {{"Дата", "Chat ID"}, {"01.01.2026", "77"}},
			SheetDebet:          {{"skip"}},
		},
	}
	rep, add := Reconcile(server, sheet, RecOptions{Since: since})

	if len(add.Residents) != 1 || add.Residents[0].Name != "Дана" {
		t.Fatalf("residents taken %+v", add.Residents)
	}
	if r := recTable(rep, "Резиденты"); r.Changed != 1 || r.OnlyServer != 1 {
		t.Fatalf("residents %+v", r)
	}
	if len(add.Payments) != 2 || add.Payments[1].ExpenseCat != "Кофе" {
		t.Fatalf("payments taken %+v", add.Payments)
	}
	if r := recTable(rep, "ДДС"); r.OnlySheet != 3 || r.Taken != 2 || r.OnlyServer != 1 {
		t.Fatalf("payments %+v", r)
	}
	if len(add.Fines) != 1 || add.Fines[0].Name != "Дана" {
		t.Fatalf("fines taken %+v", add.Fines)
	}
	if r := recTable(rep, "Штрафы"); r.Changed != 1 {
		t.Fatalf("fines %+v", r)
	}
	if len(add.Meetings) != 1 || recTable(rep, "Встречи").Changed != 1 {
		t.Fatalf("meetings %+v %+v", add.Meetings, recTable(rep, "Встречи"))
	}
	if len(add.Reports) != 1 {
		t.Fatalf("reports %+v", add.Reports)
	}
	if len(add.Settings) != 1 || add.Settings[0].Key != "bye" || recTable(rep, "Тексты бота").Changed != 1 {
		t.Fatalf("settings %+v", add.Settings)
	}
	// Append-only logs take their missing rows; other sheets only list them.
	if rows := add.AppendRows[SheetProfit]; len(rows) != 1 || rows[0][1] != "Дана" {
		t.Fatalf("profit rows %v", add.AppendRows)
	}
	if _, ok := add.AppendRows[SheetLeads]; ok {
		t.Fatal("a lead edited on the server would come back twice")
	}
	if r := recTable(rep, SheetLeads); r.OnlySheet != 1 || r.Taken != 0 {
		t.Fatalf("leads %+v", r)
	}
	if _, ok := add.NewSheets["Подписчики канала"]; !ok {
		t.Fatal("a sheet the server lacks is taken whole")
	}
	if _, ok := add.NewSheets[SheetDebet]; ok {
		t.Fatal("parsed sheets are not raw copies")
	}
	if rep.Taken == 0 || rep.Differences <= rep.Taken || !strings.Contains(rep.Summary(), "взято из таблицы") {
		t.Fatalf("summary %q", rep.Summary())
	}

	// The ДДС the server copied into the sheet is not compared.
	rep2, add2 := Reconcile(server, sheet, RecOptions{Since: since, MirroredDDS: true})
	if len(add2.Payments) != 0 || recTable(rep2, "ДДС").Skipped == "" {
		t.Fatalf("mirrored ДДС %+v", recTable(rep2, "ДДС"))
	}
	// The same data: nothing to take, nothing listed.
	rep3, add3 := Reconcile(server, server, RecOptions{Since: since})
	if !add3.Empty() || rep3.Differences != 0 || rep3.Summary() != "сервер и таблица совпадают" {
		t.Fatalf("same data: %d differences, %q", rep3.Differences, rep3.Summary())
	}
}

func TestScriptModeFollowsExport(t *testing.T) {
	defer SetSheetMode(SheetModeOff)()
	on := true
	defer SetSheetHooks(func(context.Context) bool { return on }, func(context.Context) bool { return true })()
	if ScriptMode(context.Background()) != SheetModeMirror || !ReconcileWanted(context.Background()) {
		t.Fatal("export on: mirror")
	}
	on = false
	if ScriptMode(context.Background()) != SheetModeOff {
		t.Fatal("export off: off")
	}
	SetSheetMode(SheetModeLegacy)
	on = true
	if ScriptMode(context.Background()) != SheetModeLegacy || ExportOn(context.Background()) || ReconcileWanted(context.Background()) {
		t.Fatal("rollback: legacy, no export, no reconciliation")
	}
}
