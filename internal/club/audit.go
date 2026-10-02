package club

import (
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"
	"time"
)

// The data audit after the script v31 slips (moving the club off the sheet).
//
// v31 wrote some app actions into the wrong cells of «BS - резиденты дебет»:
//   - setMeetings / renewMeetings swapped «встреч оплачено» and «встреч
//     проведено» (renewMeetings also took the tariff from «Остаток (вход)»);
//   - addResident put the package into «проведено» (9 of 0) and the partner
//     into «Месяцев»; convertToResident lost the Chat ID;
//   - a resident's payment from the app added the amount to «Тариф», zeroed
//     «проведено» and did not extend the package;
//   - a fine's payment paid off «Остаток (вход)» / «Долг продление».
//
// The audit reads what the server has: the journal of every app and platform
// action (club_ops, with its parameters), the debet sheet as imported now and
// the last imports kept (club_imports, about a day), and says, resident by
// resident, which values look damaged and what was likely there. With an
// import from just before and just after the action the answer is exact
// (high); from today's values and the journal alone it is an estimate
// (medium).

// AuditOp is one journal entry the audit relies on.
type AuditOp struct {
	At     time.Time         `json:"at"`
	Action string            `json:"action"`
	By     string            `json:"by"`
	Params map[string]string `json:"params"`
}

// AuditState is the debet sheet as one past import brought it.
type AuditState struct {
	At        time.Time
	Residents []Resident
}

// AuditInput is everything the audit reads.
type AuditInput struct {
	Now        time.Time
	Residents  []Resident   // the server's tables now
	DebetRaw   [][]string   // the debet sheet as displayed (last import)
	Ops        []AuditOp    // the journal, oldest first
	History    []AuditState // past imports, oldest first
	MeetingLog []MeetingLogEntry
	Payments   []Payment
}

// AuditItem is one value that looks damaged.
type AuditItem struct {
	Name       string    `json:"name"`
	Field      string    `json:"field"`
	Now        any       `json:"now"`
	Suggested  any       `json:"suggested"`
	Why        string    `json:"why"`
	Ops        []AuditOp `json:"ops"`
	Confidence string    `json:"confidence"` // high | medium
}

// AuditReport is the club doc bs_data_audit.
type AuditReport struct {
	At      time.Time   `json:"at"`
	Items   []AuditItem `json:"items"`
	Checked int         `json:"checked"`
	// Tariffs: what the club's tariffs are, as read from the data.
	Tariffs []int64 `json:"tariffs"`
}

const (
	High   = "high"
	Medium = "medium"
)

// AuditFields: the resident fields the audit speaks about (as setResidentField names them).
var AuditFields = []string{"tariff", "meetingsGranted", "meetingsDone", "paidEntry", "restEntry", "renewDebt", "months", "chatId", "partner"}

func fieldOf(r Resident, f string) any {
	switch f {
	case "tariff":
		return r.Tariff
	case "meetingsGranted":
		return r.Granted
	case "meetingsDone":
		return r.Done
	case "paidEntry":
		return r.PaidEntry
	case "restEntry":
		return r.RestEntry
	case "renewDebt":
		return r.RenewDebt
	case "months":
		return r.Months
	case "chatId":
		if r.TgID > 0 {
			return strconv.FormatInt(r.TgID, 10)
		}
		return ""
	case "partner":
		return r.Partner
	}
	return nil
}

// FixActions: an audit fix goes as setMeetings (the counters, both at once),
// setPartner (both partners) or setResidentField.
func FixAction(field string) string {
	switch field {
	case "meetingsGranted", "meetingsDone":
		return "setMeetings"
	case "partner":
		return "setPartner"
	}
	return "setResidentField"
}

type auditor struct {
	in      AuditInput
	cur     map[string]Resident
	items   []AuditItem
	seen    map[string]bool
	common  map[int64]bool
	tariffs []int64
}

// Audit runs every rule. The items come strongest first.
func Audit(in AuditInput) AuditReport {
	a := &auditor{in: in, cur: map[string]Resident{}, seen: map[string]bool{}, common: map[int64]bool{}}
	for _, r := range in.Residents {
		if !r.Archived && strings.TrimSpace(r.Name) != "" {
			a.cur[NormName(r.Name)] = r
		}
	}
	sort.SliceStable(a.in.Ops, func(i, j int) bool { return a.in.Ops[i].At.Before(a.in.Ops[j].At) })
	sort.SliceStable(a.in.History, func(i, j int) bool { return a.in.History[i].At.Before(a.in.History[j].At) })
	a.learnTariffs()

	a.ruleSetMeetings()
	a.ruleRenew()
	a.ruleAddResident()
	a.rulePayments()
	a.ruleFinePayments()
	a.ruleMonthsText()
	a.ruleTariffOutliers()
	a.ruleConductedOverPaid()

	sort.SliceStable(a.items, func(i, j int) bool {
		if a.items[i].Confidence != a.items[j].Confidence {
			return a.items[i].Confidence == High
		}
		return a.items[i].Name < a.items[j].Name
	})
	rep := AuditReport{At: in.Now, Items: a.items, Checked: len(a.cur), Tariffs: a.tariffs}
	if rep.Items == nil {
		rep.Items = []AuditItem{}
	}
	if rep.Tariffs == nil {
		rep.Tariffs = []int64{}
	}
	return rep
}

// ── helpers ──

func pnum(s string) (int64, bool) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, false
	}
	v, ok := Money(s)
	return v, ok
}

func (a *auditor) resident(name string) (Resident, bool) {
	r, ok := a.cur[NormName(name)]
	return r, ok
}

// transition finds the first pair of consecutive imports, the later one
// after t, where the resident changed as pred says: the action's effect as
// the sheet showed it (a write may reach the sheet later than it was made,
// when it waited on the server).
func (a *auditor) transition(name string, t time.Time, pred func(before, after Resident) bool) (Resident, Resident, time.Time, time.Time, bool) {
	find := func(s AuditState) (Resident, bool) {
		for _, r := range s.Residents {
			if !r.Archived && NormName(r.Name) == NormName(name) {
				return r, true
			}
		}
		return Resident{}, false
	}
	h := a.in.History
	for i := 1; i < len(h); i++ {
		if !h[i].At.After(t) {
			continue
		}
		b, okB := find(h[i-1])
		f, okA := find(h[i])
		if okB && okA && pred(b, f) {
			return b, f, h[i-1].At, h[i].At, true
		}
	}
	return Resident{}, Resident{}, time.Time{}, time.Time{}, false
}

// firstAfter is the resident in the first import after t that has them.
func (a *auditor) firstAfter(name string, t time.Time) (Resident, bool) {
	for _, s := range a.in.History {
		if !s.At.After(t) {
			continue
		}
		for _, r := range s.Residents {
			if !r.Archived && NormName(r.Name) == NormName(name) {
				return r, true
			}
		}
	}
	return Resident{}, false
}

func opResident(o AuditOp) string {
	for _, k := range []string{"name", "resident", "res"} {
		if v := strings.TrimSpace(o.Params[k]); v != "" {
			return NormName(v)
		}
	}
	return ""
}

// fixed: an audit fix of this field came after t (the item was dealt with).
func (a *auditor) fixed(name, field string, t time.Time) bool {
	for _, o := range a.in.Ops {
		if !o.At.After(t) || NormName(o.Params["name"]) != NormName(name) {
			continue
		}
		if o.Action == "setResidentField" && o.Params["field"] == field {
			return true
		}
		if o.Params["audit"] == "1" && o.Action == FixAction(field) {
			return true
		}
	}
	return false
}

func isAuditFix(o AuditOp) bool { return o.Params["audit"] == "1" || o.Action == "setResidentField" }

// meetingsAfter: meetings marked in the app for the resident after t (the
// journal: confirmMeeting, markAttendance), each adding one to «проведено».
func (a *auditor) meetingsAfter(name string, t time.Time) int64 {
	var n int64
	for _, o := range a.in.Ops {
		if !o.At.After(t) {
			continue
		}
		switch o.Action {
		case "confirmMeeting":
			if NormName(o.Params["res"]) == NormName(name) {
				n++
			}
		case "markAttendance":
			for _, x := range strings.Split(o.Params["names"], "|") {
				if NormName(x) == NormName(name) {
					n++
				}
			}
		}
	}
	return n
}

func (a *auditor) meetingsBetween(name string, from, to time.Time) int64 {
	var n int64
	for _, e := range a.in.MeetingLog {
		if NormName(e.Resident) == NormName(name) && !e.Date.Before(from) && !e.Date.After(to) {
			n++
		}
	}
	return n
}

func pack(tariff, visits int64) int64 {
	if visits <= 0 {
		visits = 3
	}
	return tariffMonthsOf(tariff) * visits
}

// tariffMonthsOf is the script's _tariffToMonths.
func tariffMonthsOf(sum int64) int64 {
	switch {
	case sum >= 1000000:
		return 12
	case sum >= 400000:
		return 3
	case sum > 0:
		return 1
	}
	return 3
}

func nonNeg0(v int64) int64 {
	if v < 0 {
		return 0
	}
	return v
}

func money(v int64) string {
	s := grouped(v)
	return strings.ReplaceAll(s, ",", " ")
}

// add keeps an item unless the same field of the same resident is already
// spoken for, the value is already right, or an audit fix came after t.
func (a *auditor) add(it AuditItem, t time.Time) {
	k := NormName(it.Name) + "|" + it.Field
	if a.seen[k] {
		return
	}
	if fmt.Sprint(it.Now) == fmt.Sprint(it.Suggested) {
		return
	}
	if a.fixed(it.Name, it.Field, t) {
		return
	}
	if it.Ops == nil {
		it.Ops = []AuditOp{}
	}
	a.seen[k] = true
	a.items = append(a.items, it)
}

// learnTariffs: the club's real tariffs are the ones at least two residents
// pay, and the amounts new residents were added with.
func (a *auditor) learnTariffs() {
	count := map[int64]int{}
	for _, r := range a.cur {
		if r.Tariff > 0 && !r.Admin {
			count[r.Tariff]++
		}
	}
	for t, n := range count {
		if n >= 2 {
			a.common[t] = true
		}
	}
	for _, o := range a.in.Ops {
		if o.Action == "addResident" || o.Action == "convertToResident" {
			if d, ok := pnum(o.Params["debt"]); ok && d > 0 {
				a.common[d] = true
			}
		}
	}
	for t := range a.common {
		a.tariffs = append(a.tariffs, t)
	}
	sort.Slice(a.tariffs, func(i, j int) bool { return a.tariffs[i] < a.tariffs[j] })
}

// ── rules ──

// setMeetings in v31 wrote «оплачено» := done and «проведено» := granted.
func (a *auditor) ruleSetMeetings() {
	last := map[string]AuditOp{}
	var order []string
	for _, o := range a.in.Ops {
		if o.Action != "setMeetings" || isAuditFix(o) {
			continue
		}
		k := NormName(o.Params["name"])
		if _, ok := last[k]; !ok {
			order = append(order, k)
		}
		last[k] = o
	}
	for _, k := range order {
		o := last[k]
		cur, ok := a.resident(o.Params["name"])
		d, ok1 := pnum(o.Params["done"])
		g, ok2 := pnum(o.Params["granted"])
		if !ok || !ok1 || !ok2 || d == g {
			continue
		}
		d, g = nonNeg0(d), nonNeg0(g)
		conf := ""
		var wantG, wantD int64
		why := ""
		swapped := func(b, f Resident) bool { return f.Granted == d && f.Done == g && !(b.Granted == d && b.Done == g) }
		if _, after, _, at, ok := a.transition(cur.Name, o.At, swapped); ok {
			conf = High
			wantG, wantD = g+(cur.Granted-after.Granted), d+(cur.Done-after.Done)
			why = fmt.Sprintf("setMeetings %s: отправлено «проведено» %d, «оплачено» %d, а импорт %s показал %d из %d — v31 перепутала колонки",
				o.At.In(Almaty).Format("02.01 15:04"), d, g, at.In(Almaty).Format("02.01 15:04"), after.Done, after.Granted)
		} else if _, seen := a.firstAfter(cur.Name, o.At); !seen && cur.Granted == d && cur.Done >= g {
			conf = Medium
			if cur.Done == g {
				conf = High
			}
			wantG, wantD = g, d+(cur.Done-g)
			why = fmt.Sprintf("setMeetings %s: отправлено «проведено» %d, «оплачено» %d; в таблице «оплачено» %d, «проведено» %d — v31 записала наоборот",
				o.At.In(Almaty).Format("02.01 15:04"), d, g, cur.Granted, cur.Done)
		}
		if conf == "" {
			continue
		}
		ops := []AuditOp{o}
		a.add(AuditItem{Name: cur.Name, Field: "meetingsGranted", Now: cur.Granted, Suggested: nonNeg0(wantG), Why: why, Ops: ops, Confidence: conf}, o.At)
		a.add(AuditItem{Name: cur.Name, Field: "meetingsDone", Now: cur.Done, Suggested: nonNeg0(wantD), Why: why, Ops: ops, Confidence: conf}, o.At)
	}
}

// renewMeetings in v31: tariff read from «Остаток (вход)», «оплачено» := 0,
// «проведено» := pack(остаток) + проведено − оплачено.
func (a *auditor) ruleRenew() {
	for _, o := range a.in.Ops {
		if o.Action != "renewMeetings" || isAuditFix(o) {
			continue
		}
		cur, ok := a.resident(o.Params["name"])
		if !ok {
			continue
		}
		renewed31 := func(b, f Resident) bool {
			return b.Granted != 0 && f.Granted == 0 && f.Done == nonNeg0(pack(b.RestEntry, 3)+b.Done-b.Granted)
		}
		if before, after, bat, aat, ok := a.transition(cur.Name, o.At, renewed31); ok {
			wantG := nonNeg0(pack(before.Tariff, 3)+before.Granted-before.Done) + (cur.Granted - after.Granted)
			wantD := cur.Done - after.Done
			why := fmt.Sprintf("renewMeetings %s: до продления %d из %d (импорт %s), после — 0 оплачено, %d проведено (импорт %s): v31 взяла тариф из «Остаток (вход)» и перепутала колонки. Должно быть: новый пакет %d + остаток %d",
				o.At.In(Almaty).Format("02.01 15:04"), before.Done, before.Granted, bat.In(Almaty).Format("02.01 15:04"), after.Done,
				aat.In(Almaty).Format("02.01 15:04"), pack(before.Tariff, 3), before.Granted-before.Done)
			a.add(AuditItem{Name: cur.Name, Field: "meetingsGranted", Now: cur.Granted, Suggested: nonNeg0(wantG), Why: why, Ops: []AuditOp{o}, Confidence: High}, o.At)
			a.add(AuditItem{Name: cur.Name, Field: "meetingsDone", Now: cur.Done, Suggested: nonNeg0(wantD), Why: why, Ops: []AuditOp{o}, Confidence: High}, o.At)
			continue
		}
		// Only today's values (no import from around it): v31 leaves «оплачено» at 0.
		if _, seen := a.firstAfter(cur.Name, o.At); seen || cur.Granted != 0 {
			continue
		}
		m := a.meetingsAfter(cur.Name, o.At)
		doneAfter := cur.Done - m
		why := fmt.Sprintf("renewMeetings %s, а сейчас «оплачено» 0, «проведено» %d (из них %d встреч отмечено после продления): v31 обнулила «оплачено» и записала пакет в «проведено»",
			o.At.In(Almaty).Format("02.01 15:04"), cur.Done, m)
		var wantG int64
		if doneAfter > 0 {
			// doneAfter = pack(остаток) + проведено − оплачено  →  остаток пакета
			left := doneAfter - pack(cur.RestEntry, 3)
			wantG = nonNeg0(pack(cur.Tariff, 3) + left)
		} else {
			wantG = pack(cur.Tariff, 3)
			why += "; остаток старого пакета не восстановить (v31 обрезала его до 0), взят только новый пакет"
		}
		a.add(AuditItem{Name: cur.Name, Field: "meetingsGranted", Now: cur.Granted, Suggested: wantG, Why: why, Ops: []AuditOp{o}, Confidence: Medium}, o.At)
		a.add(AuditItem{Name: cur.Name, Field: "meetingsDone", Now: cur.Done, Suggested: m, Why: why, Ops: []AuditOp{o}, Confidence: Medium}, o.At)
	}
}

// addResident / convertToResident in v31: «оплачено» 0, «проведено» = пакет;
// the partner in «Месяцев»; convertToResident without the Chat ID.
func (a *auditor) ruleAddResident() {
	for _, o := range a.in.Ops {
		if o.Action != "addResident" && o.Action != "convertToResident" {
			continue
		}
		cur, ok := a.resident(o.Params["name"])
		if !ok {
			continue
		}
		visits, _ := pnum(o.Params["visits"])
		if visits <= 0 {
			visits = 3
		}
		debt, _ := pnum(o.Params["debt"])
		t31, _ := pnum(o.Params["tariff"])
		if o.Action == "convertToResident" {
			t31 = debt
		}
		packV31 := tariffMonthsOf(t31) * visits
		packOK := tariffMonthsOf(debt) * visits
		when := o.At.In(Almaty).Format("02.01 15:04")
		// v31's mark: «оплачено» 0 and the package in «проведено» — in the
		// import right after, or (a later slip, a lost import) today
		slipped := func(r Resident) bool { return r.Granted == 0 && r.Done >= packV31 && packV31 > 0 }
		base := cur
		if after, ok := a.firstAfter(cur.Name, o.At); ok && slipped(after) {
			base = after
		}
		if slipped(base) {
			conf := Medium
			if base.Done == packV31 {
				conf = High
			}
			since := cur.Done - packV31
			why := fmt.Sprintf("%s %s: v31 записала пакет %d в «проведено», «оплачено» 0. Пакет по тарифу %s × %d встреч в месяц = %d",
				o.Action, when, packV31, money(debt), visits, packOK)
			a.add(AuditItem{Name: cur.Name, Field: "meetingsGranted", Now: cur.Granted, Suggested: cur.Granted + packOK, Why: why, Ops: []AuditOp{o}, Confidence: conf}, o.At)
			a.add(AuditItem{Name: cur.Name, Field: "meetingsDone", Now: cur.Done, Suggested: nonNeg0(since), Why: why, Ops: []AuditOp{o}, Confidence: conf}, o.At)
		}
		if o.Action == "convertToResident" && cur.TgID == 0 {
			if id, err := strconv.ParseInt(strings.TrimSpace(o.Params["chatId"]), 10, 64); err == nil && id > 0 {
				a.add(AuditItem{Name: cur.Name, Field: "chatId", Now: "", Suggested: strconv.FormatInt(id, 10),
					Why: fmt.Sprintf("convertToResident %s: подписчик %d переведён в резиденты, но v31 не записала его Chat ID", when, id),
					Ops: []AuditOp{o}, Confidence: High}, o.At)
			}
		}
	}
}

// A resident's payment in v31: «Тариф» += amount, «проведено» := 0, the
// package not extended (addMeetingsOnPayment then saw the bigger tariff).
func (a *auditor) rulePayments() {
	for _, o := range a.in.Ops {
		if o.Action != "addPayment" || o.Params["type"] != "income" || strings.Contains(o.Params["src"], "Штраф") {
			continue
		}
		cur, ok := a.resident(o.Params["resident"])
		amount, ok2 := pnum(o.Params["amount"])
		if !ok || !ok2 || amount <= 0 {
			continue
		}
		when := o.At.In(Almaty).Format("02.01 15:04")
		added := func(b, f Resident) bool { return b.Tariff > 0 && f.Tariff == b.Tariff+amount }
		if before, after, bat, aat, ok := a.transition(cur.Name, o.At, added); ok {
			why := fmt.Sprintf("оплата %s от %s: тариф был %s (импорт %s), стал %s (импорт %s) — v31 прибавила оплату к «Тарифу»",
				money(amount), when, money(before.Tariff), bat.In(Almaty).Format("02.01 15:04"), money(after.Tariff), aat.In(Almaty).Format("02.01 15:04"))
			a.add(AuditItem{Name: cur.Name, Field: "tariff", Now: cur.Tariff, Suggested: before.Tariff + (cur.Tariff - after.Tariff), Why: why, Ops: []AuditOp{o}, Confidence: High}, o.At)
			if after.Done == 0 && before.Done > 0 {
				periods := amount / before.Tariff
				if periods >= 1 {
					want := nonNeg0(pack(before.Tariff, 3)*periods+before.Granted-before.Done) + (cur.Granted - after.Granted)
					a.add(AuditItem{Name: cur.Name, Field: "meetingsGranted", Now: cur.Granted, Suggested: want,
						Why: fmt.Sprintf("та же оплата обнулила «проведено» (было %d из %d), а пакет не продлился: должно быть %d новых + остаток %d",
							before.Done, before.Granted, pack(before.Tariff, 3)*periods, before.Granted-before.Done), Ops: []AuditOp{o}, Confidence: High}, o.At)
				} else {
					a.add(AuditItem{Name: cur.Name, Field: "meetingsDone", Now: cur.Done, Suggested: before.Done + (cur.Done - after.Done),
						Why: fmt.Sprintf("та же оплата (меньше тарифа, пакет не продлевается) обнулила «проведено»: было %d", before.Done),
						Ops: []AuditOp{o}, Confidence: High}, o.At)
				}
			}
			continue
		}
		// Only today's values: the tariff is the club's tariff plus this payment.
		t := cur.Tariff - amount
		if t <= 0 || a.common[cur.Tariff] || !a.common[t] {
			continue
		}
		why := fmt.Sprintf("оплата %s от %s, а тариф %s — не тариф клуба; %s − %s = %s: v31 прибавила оплату к «Тарифу»",
			money(amount), when, money(cur.Tariff), money(cur.Tariff), money(amount), money(t))
		a.add(AuditItem{Name: cur.Name, Field: "tariff", Now: cur.Tariff, Suggested: t, Why: why, Ops: []AuditOp{o}, Confidence: High}, o.At)
		periods := amount / t
		if periods >= 1 {
			// v31 zeroed «проведено» and did not extend: the package is short by
			// a new one; what was used before the payment comes from «Лог встреч».
			m := a.meetingsAfter(cur.Name, o.At)
			from := o.At.AddDate(0, -int(tariffMonthsOf(t)), 0)
			usedBefore := a.meetingsBetween(cur.Name, from, o.At)
			doneAfter := cur.Done - m // 0 when nothing else changed it
			if doneAfter <= 0 {
				want := nonNeg0(pack(t, 3)*periods + cur.Granted - usedBefore)
				a.add(AuditItem{Name: cur.Name, Field: "meetingsGranted", Now: cur.Granted, Suggested: want,
					Why: fmt.Sprintf("та же оплата обнулила «проведено» и не продлила пакет: новый пакет %d + остаток (оплачено %d − проведено до оплаты ≈ %d по «Логу встреч»)",
						pack(t, 3)*periods, cur.Granted, usedBefore), Ops: []AuditOp{o}, Confidence: Medium}, o.At)
			}
		}
	}
}

// A fine's payment in v31 also paid off «Остаток (вход)», then «Долг продление».
func (a *auditor) ruleFinePayments() {
	for _, o := range a.in.Ops {
		if o.Action != "addPayment" || o.Params["type"] != "income" || !strings.Contains(o.Params["src"], "Штраф") {
			continue
		}
		cur, ok := a.resident(o.Params["resident"])
		amount, ok2 := pnum(o.Params["amount"])
		if !ok || !ok2 || amount <= 0 {
			continue
		}
		when := o.At.In(Almaty).Format("02.01 15:04")
		paidOff := func(b, f Resident) bool {
			dRest, dRenew := b.RestEntry-f.RestEntry, b.RenewDebt-f.RenewDebt
			return dRest >= 0 && dRenew >= 0 && dRest+dRenew > 0 && dRest+dRenew == min64(amount, b.RestEntry+b.RenewDebt)
		}
		if before, after, bat, aat, ok := a.transition(cur.Name, o.At, paidOff); ok {
			dRest, dRenew := before.RestEntry-after.RestEntry, before.RenewDebt-after.RenewDebt
			why := fmt.Sprintf("оплата штрафа %s от %s погасила долг: «Остаток (вход)» %s → %s, «Долг продление» %s → %s (импорты %s и %s). Штраф долг не гасит",
				money(amount), when, money(before.RestEntry), money(after.RestEntry), money(before.RenewDebt), money(after.RenewDebt),
				bat.In(Almaty).Format("02.01 15:04"), aat.In(Almaty).Format("02.01 15:04"))
			if dRest > 0 {
				a.add(AuditItem{Name: cur.Name, Field: "restEntry", Now: cur.RestEntry, Suggested: cur.RestEntry + dRest, Why: why, Ops: []AuditOp{o}, Confidence: High}, o.At)
			}
			if dRenew > 0 {
				a.add(AuditItem{Name: cur.Name, Field: "renewDebt", Now: cur.RenewDebt, Suggested: cur.RenewDebt + dRenew, Why: why, Ops: []AuditOp{o}, Confidence: High}, o.At)
			}
			continue
		}
		// Only today's values: the ДДС row of this payment is marked «учтено»,
		// i.e. v31 counted it against the debts.
		applied := false
		for _, p := range a.in.Payments {
			if p.Applied && p.Income == amount && NormName(p.Resident) == NormName(cur.Name) && strings.Contains(p.IncomeCat, "Штраф") &&
				dmy(p.Date) == dmy(o.At) {
				applied = true
				break
			}
		}
		if !applied {
			continue
		}
		why := fmt.Sprintf("оплата штрафа %s от %s отмечена в ДДС «учтено»: v31 погасила ею долг (сначала «Остаток (вход)», затем «Долг продление»)",
			money(amount), when)
		field, now := "renewDebt", cur.RenewDebt
		if cur.RestEntry > 0 {
			// the rest is still owed, so the payment could only have gone to it
			field, now = "restEntry", cur.RestEntry
		}
		a.add(AuditItem{Name: cur.Name, Field: field, Now: now, Suggested: now + amount, Why: why, Ops: []AuditOp{o}, Confidence: Medium}, o.At)
	}
}

func min64(a, b int64) int64 {
	if a < b {
		return a
	}
	return b
}

// The partner in «Месяцев» (v31 addResident): a name where a number belongs.
func (a *auditor) ruleMonthsText() {
	rows := a.in.DebetRaw
	h := headerRow(rows, "Имя резидента")
	if h < 0 {
		return
	}
	col := columns(rows[h], map[string][]string{"name": {"имя"}, "months": {"месяцев"}, "partner": {"партнер"}, "joined": {"дата входа"}})
	if col["name"] < 0 || col["months"] < 0 {
		return
	}
	for i := h + 1; i < len(rows); i++ {
		name := cell(rows[i], col["name"])
		txt := cell(rows[i], col["months"])
		if name == "" || txt == "" {
			continue
		}
		if _, ok := Money(txt); ok {
			continue
		}
		cur, ok := a.resident(name)
		if !ok {
			continue
		}
		var ops []AuditOp
		conf := Medium
		why := fmt.Sprintf("в «Месяцев в проекте» записано «%s» — имя, а не число: v31 addResident писала партнёра в эту колонку", txt)
		t := time.Time{}
		for _, o := range a.in.Ops {
			if o.Action != "addResident" && o.Action != "convertToResident" {
				continue
			}
			pn, nn := NormName(o.Params["partner"]), NormName(o.Params["name"])
			if (nn == NormName(name) && pn == NormName(txt)) || (nn == NormName(txt) && pn == NormName(name)) {
				ops, conf, t = append(ops, o), High, o.At
				why += fmt.Sprintf(" (%s %s, партнёр %s)", o.Action, o.At.In(Almaty).Format("02.01 15:04"), o.Params["partner"])
			}
		}
		other, isRes := a.resident(txt)
		if strings.TrimSpace(cur.Partner) == "" && (isRes || conf == High) {
			pwhy, pconf := why, conf
			if p := strings.TrimSpace(other.Partner); p != "" && NormName(p) != NormName(cur.Name) {
				// setPartner links both ways: the other's partner would be replaced
				pwhy += fmt.Sprintf("; у «%s» уже партнёр «%s» — исправление его заменит", other.Name, p)
				pconf = Medium
			}
			a.add(AuditItem{Name: cur.Name, Field: "partner", Now: "", Suggested: txt, Why: pwhy, Ops: ops, Confidence: pconf}, t)
		}
		var want int64
		if cur.JoinedAt != nil {
			want = int64(math.Floor(float64(a.in.Now.Sub(*cur.JoinedAt)) / float64(30.44*24*float64(time.Hour))))
		}
		a.add(AuditItem{Name: cur.Name, Field: "months", Now: txt, Suggested: want,
			Why: why + "; месяцы считаются от даты входа", Ops: ops, Confidence: conf}, t)
	}
}

// A tariff that is none of the club's, with no journal entry to explain it.
func (a *auditor) ruleTariffOutliers() {
	if len(a.common) == 0 {
		return
	}
	names := make([]string, 0, len(a.cur))
	for k := range a.cur {
		names = append(names, k)
	}
	sort.Strings(names)
	maxT := a.tariffs[len(a.tariffs)-1]
	for _, k := range names {
		r := a.cur[k]
		if r.Tariff <= 0 || r.Admin || r.Former || a.common[r.Tariff] {
			continue
		}
		// Explained by a payment of this resident in ДДС?
		var found *Payment
		for i := range a.in.Payments {
			p := &a.in.Payments[i]
			if NormName(p.Resident) == k && p.Income > 0 && !strings.Contains(p.IncomeCat, "Штраф") && a.common[r.Tariff-p.Income] {
				if found == nil || p.Date.After(found.Date) {
					found = p
				}
			}
		}
		if found != nil {
			a.add(AuditItem{Name: r.Name, Field: "tariff", Now: r.Tariff, Suggested: r.Tariff - found.Income,
				Why: fmt.Sprintf("тариф %s не встречается в клубе; оплата %s от %s (ДДС, строка %d) + тариф %s = %s: похоже, оплата прибавлена к «Тарифу» (v31)",
					money(r.Tariff), money(found.Income), dmy(found.Date), found.Row, money(r.Tariff-found.Income), money(r.Tariff)),
				Confidence: Medium}, found.Date)
			continue
		}
		if r.Tariff%50000 != 0 || r.Tariff > 2*maxT {
			// nearest club tariff below
			want := a.tariffs[0]
			for _, t := range a.tariffs {
				if t <= r.Tariff {
					want = t
				}
			}
			a.add(AuditItem{Name: r.Name, Field: "tariff", Now: r.Tariff, Suggested: want,
				Why:        fmt.Sprintf("тариф %s выбивается из тарифов клуба (%s); проверьте вручную", money(r.Tariff), tariffList(a.tariffs)),
				Confidence: Medium}, time.Time{})
		}
	}
}

func tariffList(ts []int64) string {
	var s []string
	for _, t := range ts {
		s = append(s, money(t))
	}
	return strings.Join(s, ", ")
}

// Conducted more than paid with nothing in the journal to explain it.
func (a *auditor) ruleConductedOverPaid() {
	names := make([]string, 0, len(a.cur))
	for k := range a.cur {
		names = append(names, k)
	}
	sort.Strings(names)
	for _, k := range names {
		r := a.cur[k]
		if r.Admin || r.Former || r.Done <= r.Granted {
			continue
		}
		why := fmt.Sprintf("проведено %d больше оплаченного %d", r.Done, r.Granted)
		if r.Granted == 0 {
			why += ": «оплачено» пусто — так v31 записывала пакет нового резидента и продление (в «проведено»)"
		} else {
			why += ": похоже на перепутанные колонки (v31); если резидент действительно ходит в долг — оставить как есть"
		}
		a.add(AuditItem{Name: r.Name, Field: "meetingsGranted", Now: r.Granted, Suggested: r.Done, Why: why, Confidence: Medium}, time.Time{})
		a.add(AuditItem{Name: r.Name, Field: "meetingsDone", Now: r.Done, Suggested: r.Granted, Why: why, Confidence: Medium}, time.Time{})
	}
}

// ParseDebetRows reads one debet sheet (an old import's) into residents.
func ParseDebetRows(rows [][]string) ([]Resident, error) {
	return parseDebet(rows, func(string, ...any) {})
}
