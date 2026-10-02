package club

import (
	"fmt"
	"strconv"
	"strings"
	"testing"
	"time"
)

// The debet sheet as the hourly import brings it (header of the real sheet).
var debetHeader = []string{"№", "Имя резидента", "Тариф тг", "Встреч\nоплачено", "Встреч\nпроведено", "Осталось\nвстреч",
	"Оплачено\n(вход) тг", "Остаток\n(вход) тг", "Долг\nпродление тг", "Штрафы тг", "ОБЩИЙ\nДОЛГ тг", "Формат", "Chat ID",
	"Бывший", "Исключение", "Админ", "Источник", "Дата входа", "Месяцев\nв проекте", "Примечание", "Партнёр"}

type dr struct {
	name                          string
	tariff, granted, done         int64
	rest, renew                   int64
	chat, months, partner, joined string
}

func debetGrid(rows ...dr) [][]string {
	g := [][]string{{"", "BS. ДЕБЕТ"}, debetHeader}
	f := func(n int64) string { return grouped(n) }
	for i, r := range rows {
		joined := r.joined
		if joined == "" {
			joined = "01.06.2026"
		}
		g = append(g, []string{strconv.Itoa(i + 1), r.name, f(r.tariff), f(r.granted), f(r.done), f(r.granted - r.done),
			"0", f(r.rest), f(r.renew), "0", f(r.rest + r.renew), "Офлайн", r.chat, "Нет", "Нет", "Нет", "Знакомый", joined, r.months, "", r.partner})
	}
	return g
}

func residentsOf(t *testing.T, g [][]string) []Resident {
	t.Helper()
	res, err := ParseDebetRows(g)
	if err != nil {
		t.Fatal(err)
	}
	return res
}

func aat(s string) time.Time { return mustAudit(Date(s)) }

func mustAudit(t time.Time, ok bool) time.Time {
	if !ok {
		panic("date")
	}
	return t
}

// The club as the real sheet has it (tariffs 100 000, 500 000, 1 000 000).
func baseRows() []dr {
	return []dr{
		{name: "Альтаир", tariff: 100000, granted: 3, done: 2, chat: "490685605", months: "21"},
		{name: "Дмитрий Цой", tariff: 100000, granted: 3, done: 1, renew: 700000, chat: "874109046", months: "20"},
		{name: "Асет", tariff: 100000, granted: 3, done: 2, chat: "478757502", months: "18"},
		{name: "Елена", tariff: 300000, granted: 4, done: 1, renew: 100000, chat: "1104338759", months: "15"},
		{name: "Азамат TV", tariff: 1000000, granted: 36, done: 4, chat: "856015708", months: "13"},
		{name: "Арлан", tariff: 1000000, granted: 36, done: 4, rest: 800000, chat: "1319900316", months: "7"},
		{name: "Бакытжан", tariff: 500000, granted: 9, done: 3, rest: 250000, chat: "334435644", months: "1"},
		{name: "Алишер", tariff: 500000, granted: 9, done: 3, rest: 250000, chat: "780850710", months: "1"},
		{name: "Азат", tariff: 1000000, granted: 36, done: 2, chat: "445216687", months: "0"},
	}
}

func with(rows []dr, name string, f func(r *dr)) []dr {
	out := append([]dr(nil), rows...)
	for i := range out {
		if out[i].name == name {
			f(&out[i])
		}
	}
	return out
}

func op(when, action string, kv ...string) AuditOp {
	p := map[string]string{"chatId": "453800951", "userName": "Рустам"}
	for i := 0; i+1 < len(kv); i += 2 {
		p[kv[i]] = kv[i+1]
	}
	return AuditOp{At: aat(when), Action: action, By: "Рустам", Params: p}
}

func findItem(rep AuditReport, name, field string) *AuditItem {
	for i := range rep.Items {
		if rep.Items[i].Name == name && rep.Items[i].Field == field {
			return &rep.Items[i]
		}
	}
	return nil
}

func expect(t *testing.T, rep AuditReport, name, field string, now, want any, conf string) {
	t.Helper()
	it := findItem(rep, name, field)
	if it == nil {
		t.Fatalf("no item %s.%s; items: %s", name, field, dumpItems(rep))
	}
	if fmt.Sprint(it.Now) != fmt.Sprint(now) || fmt.Sprint(it.Suggested) != fmt.Sprint(want) || it.Confidence != conf {
		t.Fatalf("%s.%s: now %v → %v (%s), want %v → %v (%s); why: %s", name, field, it.Now, it.Suggested, it.Confidence, now, want, conf, it.Why)
	}
	if it.Why == "" || it.Ops == nil {
		t.Fatalf("%s.%s: no why/ops", name, field)
	}
}

func dumpItems(rep AuditReport) string {
	var b strings.Builder
	for _, it := range rep.Items {
		fmt.Fprintf(&b, "\n  %s %s.%s %v→%v %s", it.Confidence, it.Name, it.Field, it.Now, it.Suggested, it.Why)
	}
	return b.String()
}

func noItem(t *testing.T, rep AuditReport, name string) {
	t.Helper()
	for _, it := range rep.Items {
		if it.Name == name {
			t.Fatalf("unexpected item for %s: %s", name, dumpItems(rep))
		}
	}
}

// audit runs the audit on the sheet as imported at each moment (the last is
// the current state).
func audit(t *testing.T, ops []AuditOp, extra func(in *AuditInput), states ...struct {
	at   string
	rows []dr
}) AuditReport {
	t.Helper()
	in := AuditInput{Now: aat("02.10.2026 12:00"), Ops: ops}
	for _, s := range states {
		g := debetGrid(s.rows...)
		in.History = append(in.History, AuditState{At: aat(s.at), Residents: residentsOf(t, g)})
		in.DebetRaw = g
		in.Residents = residentsOf(t, g)
	}
	if extra != nil {
		extra(&in)
	}
	return Audit(in)
}

type st = struct {
	at   string
	rows []dr
}

func TestAuditCleanClubHasNothing(t *testing.T) {
	rep := audit(t, nil, nil, st{"02.10.2026 09:00", baseRows()})
	if len(rep.Items) != 0 || rep.Checked != 9 {
		t.Fatalf("checked %d: %s", rep.Checked, dumpItems(rep))
	}
	if fmt.Sprint(rep.Tariffs) != "[100000 500000 1000000]" {
		t.Fatalf("tariffs %v", rep.Tariffs)
	}
}

// v31 setMeetings: «оплачено» := done, «проведено» := granted.
func TestAuditSetMeetingsSwapped(t *testing.T) {
	ops := []AuditOp{op("02.10.2026 10:15", "setMeetings", "name", "Бакытжан", "done", "4", "granted", "9")}
	swapped := with(baseRows(), "Бакытжан", func(r *dr) { r.granted, r.done = 4, 9 })
	// with the imports around it: exact
	rep := audit(t, ops, nil, st{"02.10.2026 10:00", baseRows()}, st{"02.10.2026 11:00", swapped})
	expect(t, rep, "Бакытжан", "meetingsGranted", 4, 9, High)
	expect(t, rep, "Бакытжан", "meetingsDone", 9, 4, High)
	// one meeting marked after the import: it counts on top of the right value
	later := with(swapped, "Бакытжан", func(r *dr) { r.done = 10 })
	rep = audit(t, ops, nil, st{"02.10.2026 10:00", baseRows()}, st{"02.10.2026 11:00", swapped}, st{"02.10.2026 11:30", later})
	expect(t, rep, "Бакытжан", "meetingsDone", 10, 5, High)
	// only today's values (the journal is older than the imports kept)
	rep = audit(t, ops, nil, st{"01.10.2026 09:00", swapped})
	expect(t, rep, "Бакытжан", "meetingsGranted", 4, 9, High)
	// the script did it right (v32): nothing
	right := with(baseRows(), "Бакытжан", func(r *dr) { r.granted, r.done = 9, 4 })
	rep = audit(t, ops, nil, st{"02.10.2026 10:00", baseRows()}, st{"02.10.2026 11:00", right})
	noItem(t, rep, "Бакытжан")
}

// v31 renewMeetings (the stand: Азат 2 of 36 → 0 of 0).
func TestAuditRenewMeetings(t *testing.T) {
	ops := []AuditOp{op("02.10.2026 10:15", "renewMeetings", "name", "Азат")}
	after := with(baseRows(), "Азат", func(r *dr) { r.granted, r.done = 0, 0 })
	rep := audit(t, ops, nil, st{"02.10.2026 10:00", baseRows()}, st{"02.10.2026 11:00", after})
	expect(t, rep, "Азат", "meetingsGranted", 0, 70, High) // 36 new + 34 left
	if findItem(rep, "Азат", "meetingsDone") != nil {
		t.Fatalf("done is right (0): %s", dumpItems(rep))
	}
	// Без импортов вокруг: pack(остаток 0) = 9 ≤ остаток, обрезано до 0 — только новый пакет
	rep = audit(t, ops, nil, st{"01.10.2026 09:00", after})
	expect(t, rep, "Азат", "meetingsGranted", 0, 36, Medium)
}

// v31 addResident: 9 of 0, the partner in «Месяцев»; convertToResident without the Chat ID.
func TestAuditAddResidentAndConvert(t *testing.T) {
	ops := []AuditOp{
		op("02.10.2026 10:10", "addResident", "name", "Тест Новый", "debt", "500000", "visits", "3", "residentChatId", "777000111"),
		op("02.10.2026 10:11", "addResident", "name", "Тест Партнёр", "debt", "1000000", "visits", "3", "partner", "Алишер", "residentChatId", "777000333"),
		op("02.10.2026 10:12", "convertToResident", "name", "Тест Конверт", "chatId", "777000222", "debt", "500000", "visits", "3"),
	}
	now := append(baseRows(),
		dr{name: "Тест Новый", tariff: 500000, granted: 0, done: 9, rest: 500000, chat: "777000111", joined: "02.10.2026"},
		dr{name: "Тест Партнёр", tariff: 1000000, granted: 0, done: 9, rest: 1000000, chat: "777000333", months: "Алишер", joined: "02.10.2026"},
		dr{name: "Тест Конверт", tariff: 500000, granted: 0, done: 9, rest: 500000, joined: "02.10.2026"},
	)
	now = with(now, "Алишер", func(r *dr) { r.months = "Тест Партнёр" }) // v31 wrote the name to the partner too (months was empty there)
	rep := audit(t, ops, nil, st{"02.10.2026 10:00", baseRows()}, st{"02.10.2026 11:00", now})
	expect(t, rep, "Тест Новый", "meetingsGranted", 0, 9, High)
	expect(t, rep, "Тест Новый", "meetingsDone", 9, 0, High)
	expect(t, rep, "Тест Партнёр", "meetingsGranted", 0, 36, High)
	expect(t, rep, "Тест Партнёр", "partner", "", "Алишер", High)
	expect(t, rep, "Тест Партнёр", "months", "Алишер", 0, High)
	expect(t, rep, "Алишер", "partner", "", "Тест Партнёр", High)
	expect(t, rep, "Алишер", "months", "Тест Партнёр", 4, High) // от 01.06.2026
	expect(t, rep, "Тест Конверт", "chatId", "", "777000222", High)
	expect(t, rep, "Тест Конверт", "meetingsGranted", 0, 9, High)
}

func TestAuditPartnerAlreadyLinkedIsMedium(t *testing.T) {
	ops := []AuditOp{op("02.10.2026 10:11", "addResident", "name", "Тест Партнёр", "debt", "1000000", "partner", "Арлан")}
	now := append(with(baseRows(), "Арлан", func(r *dr) { r.partner = "Азат" }),
		dr{name: "Тест Партнёр", tariff: 1000000, granted: 36, months: "Арлан", joined: "02.10.2026"})
	rep := audit(t, ops, nil, st{"02.10.2026 11:00", now})
	it := findItem(rep, "Тест Партнёр", "partner")
	if it == nil || it.Confidence != Medium || !strings.Contains(it.Why, "уже партнёр «Азат»") {
		t.Fatalf("%s", dumpItems(rep))
	}
}

// v31 resident payment: tariff += amount, «проведено» := 0, no new package.
func TestAuditResidentPayment(t *testing.T) {
	ops := []AuditOp{op("02.10.2026 10:20", "addPayment", "type", "income", "src", "БХ Трекинг продление", "amount", "500000", "isCash", "false", "resident", "Асет")}
	after := with(baseRows(), "Асет", func(r *dr) { r.tariff, r.done = 600000, 0 })
	rep := audit(t, ops, nil, st{"02.10.2026 10:00", baseRows()}, st{"02.10.2026 11:00", after})
	expect(t, rep, "Асет", "tariff", 600000, 100000, High)
	expect(t, rep, "Асет", "meetingsGranted", 3, 16, High) // 5 months × 3 + 1 left (2 of 3)
	// the write waited on the server and reached the sheet two imports later
	rep = audit(t, ops, nil, st{"02.10.2026 10:00", baseRows()}, st{"02.10.2026 11:00", baseRows()}, st{"02.10.2026 12:00", after})
	expect(t, rep, "Асет", "tariff", 600000, 100000, High)
	// only today's values: 600 000 is no tariff of the club, 600 000 − 500 000 is
	rep = audit(t, ops, func(in *AuditInput) {
		in.MeetingLog = []MeetingLogEntry{{Date: aat("20.09.2026"), Resident: "Асет"}, {Date: aat("27.09.2026"), Resident: "Асет"}}
	}, st{"01.10.2026 09:00", after})
	expect(t, rep, "Асет", "tariff", 600000, 100000, High)
	expect(t, rep, "Асет", "meetingsGranted", 3, 16, Medium) // 15 new + 3 paid − 2 held this month
}

// v31: a fine's payment paid off the debt too.
func TestAuditFinePaymentReducedDebt(t *testing.T) {
	ops := []AuditOp{op("02.10.2026 10:30", "addPayment", "type", "income", "src", "БХ Штраф", "amount", "10000", "isCash", "false", "resident", "Дмитрий Цой")}
	after := with(baseRows(), "Дмитрий Цой", func(r *dr) { r.renew = 690000 })
	rep := audit(t, ops, nil, st{"02.10.2026 10:00", baseRows()}, st{"02.10.2026 11:00", after})
	expect(t, rep, "Дмитрий Цой", "renewDebt", 690000, 700000, High)
	// rest first, then renew
	ops2 := []AuditOp{op("02.10.2026 10:30", "addPayment", "type", "income", "src", "БХ Штраф", "amount", "10000", "resident", "Бакытжан")}
	after2 := with(baseRows(), "Бакытжан", func(r *dr) { r.rest = 240000 })
	rep = audit(t, ops2, nil, st{"02.10.2026 10:00", baseRows()}, st{"02.10.2026 11:00", after2})
	expect(t, rep, "Бакытжан", "restEntry", 240000, 250000, High)
	// only today's values: the ДДС row of the payment is marked «учтено»
	rep = audit(t, ops, func(in *AuditInput) {
		in.Payments = []Payment{{Row: 120, Date: aat("02.10.2026"), Income: 10000, IncomeCat: "БХ Штраф", Resident: "Дмитрий Цой", Applied: true}}
	}, st{"01.10.2026 09:00", after})
	expect(t, rep, "Дмитрий Цой", "renewDebt", 690000, 700000, Medium)
	// not marked: v32 did not count it against the debt
	rep = audit(t, ops, func(in *AuditInput) {
		in.Payments = []Payment{{Row: 120, Date: aat("02.10.2026"), Income: 10000, IncomeCat: "БХ Штраф", Resident: "Дмитрий Цой"}}
	}, st{"01.10.2026 09:00", after})
	noItem(t, rep, "Дмитрий Цой")
}

func TestAuditTariffOutliersAndConductedOverPaid(t *testing.T) {
	rows := append(baseRows(), dr{name: "Мади Актобе", tariff: 1000000, granted: 36, done: 4, chat: "354788446", months: "3"})
	rows = with(rows, "Азамат TV", func(r *dr) { r.tariff = 1500000 }) // + an old payment of 500 000
	rows = with(rows, "Арлан", func(r *dr) { r.tariff = 1037500 })     // not round
	rows = with(rows, "Альтаир", func(r *dr) { r.granted, r.done = 0, 5 })
	rep := audit(t, nil, func(in *AuditInput) {
		in.Payments = []Payment{{Row: 40, Date: aat("15.08.2026"), Income: 500000, IncomeCat: "БХ Трекинг", Resident: "Азамат TV", Applied: true}}
	}, st{"02.10.2026 11:00", rows})
	expect(t, rep, "Азамат TV", "tariff", 1500000, 1000000, Medium)
	expect(t, rep, "Арлан", "tariff", 1037500, 1000000, Medium)
	expect(t, rep, "Альтаир", "meetingsGranted", 0, 5, Medium)
	expect(t, rep, "Альтаир", "meetingsDone", 5, 0, Medium)
	if findItem(rep, "Елена", "tariff") != nil { // 300 000: one resident, but a round plan
		t.Fatalf("%s", dumpItems(rep))
	}
}

// An audit fix closes the item, even where the evidence stays (the ДДС mark).
func TestAuditFixedItemsDrop(t *testing.T) {
	ops := []AuditOp{
		op("02.10.2026 10:30", "addPayment", "type", "income", "src", "БХ Штраф", "amount", "10000", "resident", "Дмитрий Цой"),
		op("02.10.2026 11:30", "setResidentField", "name", "Дмитрий Цой", "field", "renewDebt", "value", "700000", "audit", "1"),
	}
	after := with(baseRows(), "Дмитрий Цой", func(r *dr) { r.renew = 700000 }) // fixed, then the payment again marked
	rep := audit(t, ops, func(in *AuditInput) {
		in.Payments = []Payment{{Row: 120, Date: aat("02.10.2026"), Income: 10000, IncomeCat: "БХ Штраф", Resident: "Дмитрий Цой", Applied: true}}
	}, st{"01.10.2026 09:00", after})
	noItem(t, rep, "Дмитрий Цой")
	// a setMeetings sent by the audit is a fix, not a slip
	ops = []AuditOp{op("02.10.2026 10:15", "setMeetings", "name", "Бакытжан", "done", "4", "granted", "9", "audit", "1")}
	rep = audit(t, ops, nil, st{"02.10.2026 10:00", baseRows()}, st{"02.10.2026 11:00", baseRows()})
	noItem(t, rep, "Бакытжан")
}

func TestFixParams(t *testing.T) {
	for _, c := range []struct{ field, action string }{{"meetingsGranted", "setMeetings"}, {"partner", "setPartner"}, {"tariff", "setResidentField"}, {"chatId", "setResidentField"}} {
		if a := FixAction(c.field); a != c.action {
			t.Fatalf("%s: %s", c.field, a)
		}
	}
}

// The import right after the action was right, the slip came later (or that
// import is gone): today's values carry v31's mark.
func TestAuditAddResidentSlipToday(t *testing.T) {
	ops := []AuditOp{op("02.10.2026 10:10", "addResident", "name", "Тест", "debt", "1000000", "visits", "3")}
	right := append(baseRows(), dr{name: "Тест", tariff: 1000000, granted: 36, joined: "02.10.2026"})
	slip := append(baseRows(), dr{name: "Тест", tariff: 1000000, granted: 0, done: 9, joined: "02.10.2026"})
	rep := audit(t, ops, nil, st{"02.10.2026 10:00", baseRows()}, st{"02.10.2026 11:00", right}, st{"02.10.2026 12:00", slip})
	expect(t, rep, "Тест", "meetingsGranted", 0, 36, High)
	expect(t, rep, "Тест", "meetingsDone", 9, 0, High)
}
