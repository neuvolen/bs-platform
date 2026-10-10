package club

import (
	"math"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode/utf16"

	"github.com/bnursik/business_surgery_backend/internal/calllink"
)

// The Telegram app loads one data bundle (the script's getBotCache). Moving
// the club off the Google Sheet, step 2: the server builds the same bundle
// from its own tables, the sheet's way, field by field, so the two can be
// compared every day and the app switched over once they agree.
//
// The server builds the club's sections: residents and their debts, fines,
// reports, the schedule, finished meetings, the money (from the imported
// «PL» sheet) and the team's profiles (when «Профили» comes with the import).
// The rest of the bundle (wheels, leads, tasks, content, checklists, the
// avatar) still lives in the script's sheets and properties; it is taken
// from the script.

// BundleKeys is the order of the script's bundle.
var BundleKeys = []string{"residents", "fines", "logs", "schedule", "debet", "totalDebt", "monthlyPL",
	"wheelSummary", "wheelAll", "leads", "problems", "resTasks", "smm", "leadmagnets", "contentPlan",
	"checklistSchedule", "doneMeetings", "adminProfiles", "myAvatar", "ts"}

// SheetProfiles is the team's profiles sheet; not parsed, kept raw.
const SheetProfiles = "Профили"

// BundleResident is one resident as the script hands it to the app.
type BundleResident struct {
	Name            string `json:"name"`
	Paid            int64  `json:"paid"`
	Balance         int64  `json:"balance"`
	DebtRenew       int64  `json:"debtRenew"`
	Fines           int64  `json:"fines"`
	Debt            int64  `json:"debt"`
	Tariff          int64  `json:"tariff"`
	DateIn          string `json:"dateIn"`
	Source          string `json:"source"`
	IsFired         bool   `json:"isFired"`
	IsExcluded      bool   `json:"isExcluded"`
	ChatID          string `json:"chatId"`
	Format          string `json:"format"`
	IsAdmin         bool   `json:"isAdmin"`
	MeetingsDone    int64  `json:"meetingsDone"`
	MeetingsGranted int64  `json:"meetingsGranted"`
	MeetingsLeft    int64  `json:"meetingsLeft"`
	CyclesPaid      int64  `json:"cyclesPaid"`
	CyclesUsed      int64  `json:"cyclesUsed"`
	Partner         string `json:"partner"`
	Note            string `json:"note"`
	Months          int64  `json:"months"`
	StartDate       string `json:"startDate"`
}

type BundleFine struct {
	Row    int    `json:"row"`
	Name   string `json:"name"`
	Kind   string `json:"kind"`
	Amount int64  `json:"amount"`
	Date   string `json:"date"`
	Status string `json:"status"`
	// R81: from the payments ledger («Учёт → Долги и штрафы»): the fine's id,
	// the resident it counts for, what is paid on it and what is still owed
	ID   int64  `json:"id,omitempty"`
	Res  string `json:"res,omitempty"`
	Paid int64  `json:"paid,omitempty"`
	Left int64  `json:"left"`
}

type BundleLog struct {
	Date   string `json:"date"`
	Time   string `json:"time"`
	Name   string `json:"name"`
	Text   string `json:"text"`
	ChatID string `json:"chatId"`
	Late   bool   `json:"late"`
}

type BundleMeeting struct {
	Row    int    `json:"row"`
	Res    string `json:"res"`
	Name   string `json:"name"`
	Date   string `json:"date"`
	Time   string `json:"time"`
	Place  string `json:"place"`
	Link   string `json:"link"`
	Format string `json:"format"`
	Done   bool   `json:"done"`
	// Call: the platform's call link of an online meeting (R75 call); link keeps Meet as a fallback
	Call string `json:"call,omitempty"`
}

type BundleDoneMeeting struct {
	Res  string `json:"res"`
	Name string `json:"name"`
	Date string `json:"date"`
	Time string `json:"time"`
	Done bool   `json:"done"`
}

type BundleDebet struct {
	NetProfit   float64 `json:"netProfit"`
	Residual    float64 `json:"residual"`
	Dividends   float64 `json:"dividends"`
	UnpaidFines int64   `json:"unpaidFines"`
	UnpaidCount int     `json:"unpaidCount"`
}

type BundleProfile struct {
	ChatID    string `json:"chatId"`
	Niche     string `json:"niche"`
	Bio       string `json:"bio"`
	Help      string `json:"help"`
	Instagram string `json:"instagram"`
	Phone     string `json:"phone"`
	Avatar    string `json:"avatar"`
}

type PLMonthView struct {
	Idx        int      `json:"idx"`
	Name       string   `json:"name"`
	Short      string   `json:"short"`
	Revenue    float64  `json:"revenue"`
	Expenses   float64  `json:"expenses"`
	Profit     float64  `json:"profit"`
	Dividends  float64  `json:"dividends"`
	Margin     int64    `json:"margin"`
	HasData    bool     `json:"hasData"`
	Saldo      *float64 `json:"saldo"`
	Kassa      *float64 `json:"kassa"`
	KassaStart *float64 `json:"kassaStart"`
}

type PLYearView struct {
	Revenue   float64  `json:"revenue"`
	Expenses  float64  `json:"expenses"`
	Profit    float64  `json:"profit"`
	Dividends float64  `json:"dividends"`
	Margin    int64    `json:"margin"`
	Saldo     *float64 `json:"saldo"`
}

// MonthlyPLView is the script's _miniGetMonthlyPL.
type MonthlyPLView struct {
	OK              bool          `json:"ok"`
	Error           string        `json:"error,omitempty"`
	Kassa           *float64      `json:"kassa,omitempty"`
	Months          []PLMonthView `json:"months,omitempty"`
	Year            *PLYearView   `json:"year,omitempty"`
	CurrentMonthIdx *int          `json:"currentMonthIdx,omitempty"`
	// Source: "server" when counted from the cash journal (R67).
	Source string `json:"source,omitempty"`
}

var (
	monthsGen  = []string{"января", "февраля", "марта", "апреля", "мая", "июня", "июля", "августа", "сентября", "октября", "ноября", "декабря"}
	monthsNom  = []string{"Январь", "Февраль", "Март", "Апрель", "Май", "Июнь", "Июль", "Август", "Сентябрь", "Октябрь", "Ноябрь", "Декабрь"}
	reFirstInt = regexp.MustCompile(`-?\d+`)
)

func dmy(t time.Time) string { return t.In(Almaty).Format("02.01.2006") }

// jsRound is Math.round: halves go up.
func jsRound(x float64) int64 { return int64(math.Floor(x + 0.5)) }

// Slice16 cuts s to n UTF-16 units, as JavaScript's slice does (a split
// surrogate pair is dropped).
func Slice16(s string, n int) string {
	u := utf16.Encode([]rune(s))
	if len(u) <= n {
		return s
	}
	u = u[:n]
	if n > 0 && utf16.IsSurrogate(rune(u[n-1])) && u[n-1] < 0xDC00 {
		u = u[:n-1]
	}
	return string(utf16.Decode(u))
}

// Num reads a displayed sheet number as the script's Number(value) would see
// the cell: "1,234,567" and "-39,590" are numbers, "" is 0; ok is false for
// text.
func Num(v string) (float64, bool) {
	v = strings.TrimSpace(strings.NewReplacer(" ", "", " ", "", ",", "", "−", "-").Replace(v))
	if v == "" {
		return 0, true
	}
	f, err := strconv.ParseFloat(strings.TrimSuffix(v, "%"), 64)
	if err != nil {
		return 0, false
	}
	if strings.HasSuffix(v, "%") {
		f /= 100 // the cell holds a fraction, shown as percent
	}
	return f, true
}

func numOr0(v string) float64 { f, _ := Num(v); return f }

func cellOf(rows [][]string, r, c int) string {
	if r < 0 || r >= len(rows) {
		return ""
	}
	return cell(rows[r], c)
}

// AppBundle builds the club's part of the app's bundle from the server's data.
// It returns the sections it built; the others are left to the script.
func AppBundle(s *Snapshot, now time.Time) (map[string]any, []string) {
	out := map[string]any{}
	var built []string
	put := func(k string, v any) { out[k] = v; built = append(built, k) }

	// ═══ РЕЗИДЕНТЫ: только лист дебета, как в таблице ═══
	unpaid := map[string]int64{}
	for _, f := range s.Fines {
		unpaid[NormName(f.Name)] += f.Due() // R69
	}
	residents := []BundleResident{}
	formats := map[string]string{}
	var total int64
	for _, p := range s.Residents {
		if p.Archived || p.Name == "" {
			continue
		}
		r := BundleResident{
			Name: p.Name, Paid: p.PaidEntry, Balance: p.RestEntry, DebtRenew: p.RenewDebt, Tariff: p.Tariff,
			Source: p.Source, IsFired: p.Former, IsExcluded: p.Exception, IsAdmin: p.Admin,
			Format: strings.TrimSpace(p.Format), MeetingsDone: p.Done, MeetingsGranted: p.Granted,
			MeetingsLeft: p.Granted - p.Done, CyclesPaid: p.Granted, CyclesUsed: p.Done,
			Partner: p.Partner, Note: p.Note, Months: p.Months,
		}
		if r.Format == "" {
			r.Format = "Офлайн"
		}
		if r.CyclesPaid == 0 {
			r.CyclesPaid = 1 // Number(...)||1
		}
		if p.TgID > 0 {
			r.ChatID = strconv.FormatInt(p.TgID, 10)
		}
		// Формулы J и K: неоплаченные штрафы и общий долг
		r.Fines = unpaid[NormName(p.Name)]
		r.Debt = nonNeg(p.RestEntry) + nonNeg(p.RenewDebt) + r.Fines
		if p.JoinedAt != nil {
			j := p.JoinedAt.In(Almaty)
			r.DateIn = dmy(j)
			r.StartDate = monthsGen[j.Month()-1] + " " + strconv.Itoa(j.Year())
			if r.Months == 0 {
				r.Months = int64(math.Floor(float64(now.Sub(j)) / float64(30.44*24*float64(time.Hour))))
			}
		}
		if !r.IsAdmin && !r.IsFired {
			total += r.Debt
		}
		if _, ok := formats[r.Name]; !ok {
			formats[r.Name] = r.Format
		}
		residents = append(residents, r)
	}
	put("residents", residents)

	// ═══ ШТРАФЫ ═══
	fines := []BundleFine{}
	next := 0
	for _, f := range s.Fines {
		if f.Row > next {
			next = f.Row
		}
	}
	var unpaidSum int64
	unpaidCnt := 0
	for _, f := range s.Fines {
		b := BundleFine{Row: f.Row, Name: f.Name, Kind: f.Type, Amount: f.Amount, Status: f.Status, ID: f.ID, Left: f.Due()}
		if b.Row == 0 { // written by the server, the sheet has not placed it yet
			next++
			b.Row = next
		}
		if !f.Date.IsZero() {
			b.Date = dmy(f.Date)
		}
		// R81: as the ledger reads it: written off stays, the «paid» mark wins
		// over an old «Не оплатил» text (the platform's old «Оплатил» set only the mark)
		switch {
		case strings.TrimSpace(b.Status) == "Списан":
			b.Status = "Списан"
		case f.Paid || strings.TrimSpace(b.Status) == "Оплатил":
			b.Status = "Оплатил"
		default:
			b.Status = "Не оплатил"
		}
		if b.Status != "Оплатил" && b.Status != "Списан" {
			unpaidSum += f.Due() // R69: a part paid by the ledger is not owed
			unpaidCnt++
		}
		fines = append(fines, b)
	}
	put("fines", fines)

	// ═══ ОТЧЁТЫ за 30 дней ═══
	since := now.Add(-30 * 24 * time.Hour)
	logs := []BundleLog{}
	for _, e := range s.Reports {
		// Как в таблице: дата и время из колонки «Дата»; строка без даты там не видна
		shown := ReportShownAt(e)
		if shown.IsZero() || shown.Before(since) {
			continue
		}
		a := shown.In(Almaty)
		l := BundleLog{Date: a.Format("02.01.2006"), Time: a.Format("15:04"), Name: e.Name, Text: Slice16(e.Text, 200), Late: e.Late}
		if e.TgUserID > 0 {
			l.ChatID = strconv.FormatInt(e.TgUserID, 10)
		}
		logs = append(logs, l)
	}
	put("logs", logs)

	// ═══ РАСПИСАНИЕ: только сегодня и будущее ═══
	a := now.In(Almaty)
	today0 := time.Date(a.Year(), a.Month(), a.Day(), 0, 0, 0, 0, Almaty)
	sched := []BundleMeeting{}
	nextRow := 0
	for _, m := range s.Meetings {
		if m.Row > nextRow {
			nextRow = m.Row
		}
	}
	for _, m := range s.Meetings {
		if m.Resident == "" || (m.Time == "" && m.AddrCell == "" && m.LinkCell == "") {
			continue // мусорная строка
		}
		if m.Date.In(Almaty).Before(today0) {
			continue
		}
		b := BundleMeeting{Row: m.Row, Res: m.Resident, Name: m.Resident, Date: dmy(m.Date), Time: m.Time,
			Place: m.AddrCell, Link: m.LinkCell, Format: "Офлайн", Done: m.Done || m.HCell == "Да"}
		if b.Row == 0 {
			nextRow++
			b.Row = nextRow
		}
		if f, ok := formats[m.Resident]; ok {
			b.Format = f
		}
		if calllink.IsOnline(b.Link, b.Format) {
			b.Call = calllink.URL(m.Resident)
		}
		sched = append(sched, b)
	}
	put("schedule", sched)

	// ═══ ФИНАНСЫ ═══
	// R67: после переезда лист PL в базе больше не обновляется (это снимок
	// последнего импорта), поэтому «На кассе» и прибыль в приложении застывали.
	// Теперь цифры считаются из ДДС на сервере (BuildPL), как в «Учёте» платформы;
	// лист PL остаётся запасным вариантом, пока у сервера нет структуры P&L.
	if mp, ok := ServerMonthlyPL(s, now); ok {
		d := BundleDebet{UnpaidFines: unpaidSum, UnpaidCount: unpaidCnt}
		if i := *mp.CurrentMonthIdx; i >= 0 && i < len(mp.Months) {
			m := mp.Months[i]
			d.NetProfit, d.Dividends = m.Profit, m.Dividends
			if m.Kassa != nil {
				d.Residual = *m.Kassa
			}
		}
		put("debet", d)
		put("totalDebt", total)
		put("monthlyPL", mp)
	} else {
		pl := s.Raw[SheetPL]
		if len(pl) > 0 {
			d := BundleDebet{UnpaidFines: unpaidSum, UnpaidCount: unpaidCnt}
			col := int(a.Month()) // колонка месяца
			for i := range pl {
				switch cellOf(pl, i, 0) {
				case "ЧИСТАЯ ПРИБЫЛЬ":
					d.NetProfit = numOr0(cellOf(pl, i, col))
				case "На кассе":
					d.Residual = numOr0(cellOf(pl, i, col))
				case "Дивиденды":
					d.Dividends = numOr0(cellOf(pl, i, col))
				}
			}
			put("debet", d)
		} else {
			put("debet", map[string]any{})
		}
		put("totalDebt", total)
		put("monthlyPL", MonthlyPL(pl, now))
	}

	// Состоявшиеся встречи из «Лога встреч», последние 300
	done := []BundleDoneMeeting{}
	for _, e := range s.MeetingLog {
		if e.Resident == "" || e.Date.IsZero() {
			continue
		}
		done = append(done, BundleDoneMeeting{Res: e.Resident, Name: e.Resident, Date: dmy(e.Date), Time: e.Time, Done: true})
	}
	if len(done) > 300 {
		done = done[len(done)-300:]
	}
	put("doneMeetings", done)

	// Профили команды: только если лист пришёл с импортом
	if rows, ok := s.Raw[SheetProfiles]; ok {
		prof := []BundleProfile{}
		for i := 1; i < len(rows); i++ {
			if cell(rows[i], 0) == "" {
				continue
			}
			r := rows[i]
			prof = append(prof, BundleProfile{ChatID: cell(r, 0), Niche: cell(r, 1), Bio: cell(r, 2), Help: cell(r, 3),
				Instagram: cell(r, 4), Phone: cell(r, 5), Avatar: cell(r, 6)})
		}
		put("adminProfiles", prof)
	}
	return out, built
}

// ReportShownAt is the moment column A («Дата») holds for a report. The
// import sees the cell as displayed, and older rows are formatted without
// the time: then it is the moment of sending (column E) on the same day, or
// 23:59 when the row was put on the day before (a report sent before 14:00
// counted for the previous day, the script's old reportDayFor).
func ReportShownAt(e ReportEntry) time.Time {
	if e.ShownAt == nil {
		return time.Time{}
	}
	s := e.ShownAt.In(Almaty)
	if s.Hour() != 0 || s.Minute() != 0 || s.Second() != 0 {
		// The cell holds the moment of sending, as column E: the shown minute
		// may be rounded, the script cuts the seconds off the real one.
		if d := e.At.Sub(s); !e.At.IsZero() && d > -time.Minute && d < time.Minute {
			return e.At.In(Almaty)
		}
		return s
	}
	if e.At.IsZero() {
		return s
	}
	a := e.At.In(Almaty)
	if a.Year() == s.Year() && a.YearDay() == s.YearDay() {
		return a
	}
	return time.Date(s.Year(), s.Month(), s.Day(), 23, 59, 0, 0, Almaty)
}

// ServerMonthlyPL is the app's «Сводка» from the cash journal (BuildPL), the
// same numbers as the platform's «Учёт»: revenue, profit, dividends and the
// cash («На кассе», counted from CashStartMonth) month by month. ok is false
// while the server holds no P&L structure (then the sheet's PL is shown).
func ServerMonthlyPL(s *Snapshot, now time.Time) (MonthlyPLView, bool) {
	if s == nil || s.PL == nil || len(s.PL.Rows) == 0 {
		return MonthlyPLView{}, false
	}
	a := now.In(Almaty)
	year, upTo := a.Year(), int(a.Month())
	if s.PL.Year != 0 && s.PL.Year < year {
		year, upTo = s.PL.Year, 12
	}
	built := BuildPL(s.Payments, s.PL, year, upTo)
	months := make([]PLMonthView, 12)
	y := &PLYearView{}
	var kassa *float64
	for m := 0; m < 12; m++ {
		mo := built.Months[m]
		mv := PLMonthView{Idx: m, Name: monthsNom[m], Short: string([]rune(monthsNom[m])[:3]),
			Revenue: float64(mo.IncomeSum), Expenses: float64(mo.Expenses), Profit: float64(mo.Profit), Dividends: float64(mo.Dividends)}
		if mo.IncomeSum != 0 {
			mv.Margin = jsRound(float64(mo.Profit) / float64(mo.IncomeSum) * 100)
		}
		mv.HasData = mv.Revenue > 0 || mv.Expenses > 0
		if m < upTo {
			sal := mv.Profit - mv.Dividends
			mv.Saldo = &sal
			if mo.HasCash {
				k := float64(mo.Cash)
				mv.Kassa = &k
				kassa = mv.Kassa
			}
		}
		months[m] = mv
		y.Revenue += mv.Revenue
		y.Expenses += mv.Expenses
		y.Profit += mv.Profit
		y.Dividends += mv.Dividends
	}
	for m := 1; m < 12; m++ {
		months[m].KassaStart = months[m-1].Kassa
	}
	if y.Revenue != 0 {
		y.Margin = jsRound(y.Profit / y.Revenue * 100)
	}
	for i := upTo - 1; i >= 0; i-- {
		if months[i].HasData {
			y.Saldo = months[i].Saldo
			break
		}
	}
	cur := upTo - 1
	return MonthlyPLView{OK: true, Kassa: kassa, Months: months, Year: y, CurrentMonthIdx: &cur, Source: "server"}, true
}

// MonthlyPL reads the «PL» sheet as the script's _miniGetMonthlyPL does.
func MonthlyPL(rows [][]string, now time.Time) MonthlyPLView {
	if len(rows) == 0 {
		return MonthlyPLView{OK: false, Error: "Лист PL не найден"}
	}
	lr := 0
	for i := range rows {
		for _, c := range rows[i] {
			if strings.TrimSpace(c) != "" {
				lr = i + 1
				break
			}
		}
	}
	if lr < 5 {
		return MonthlyPLView{OK: false, Error: "Лист PL пустой"}
	}
	rowRev, rowExp, rowProfit, rowMargin, rowDiv, rowOst, rowKassa := -1, -1, -1, -1, -1, -1, -1
	for r := 0; r < lr; r++ {
		label := strings.ToUpper(cellOf(rows, r, 0))
		switch {
		case strings.Contains(label, "ИТОГО ДОХОДЫ"):
			rowRev = r
		case strings.Contains(label, "ИТОГО РАСХОДЫ"):
			rowExp = r
		case strings.Contains(label, "ЧИСТАЯ ПРИБЫЛЬ"):
			rowProfit = r
		case strings.Contains(label, "РЕНТАБЕЛЬНОСТЬ"):
			rowMargin = r
		case strings.Contains(label, "ДИВИДЕНД"):
			rowDiv = r
		}
		if label == "ОСТАТОК" {
			rowOst = r
		}
		if label == "НА КАССЕ" {
			rowKassa = r
		}
	}
	val := func(r, c int) float64 {
		if r < 0 {
			return 0
		}
		return numOr0(cellOf(rows, r, c))
	}
	margin := func(c int) int64 {
		if rowMargin < 0 {
			return 0
		}
		v := cellOf(rows, rowMargin, c)
		if f, ok := Num(v); ok && v != "" {
			if f > 1 {
				return jsRound(f)
			}
			return jsRound(f * 100)
		}
		if m := reFirstInt.FindString(v); m != "" {
			n, _ := strconv.ParseInt(m, 10, 64)
			return n
		}
		return 0
	}
	opt := func(r, c int) *float64 {
		v := cellOf(rows, r, c)
		if v == "" || v == "-" {
			return nil
		}
		f := numOr0(v)
		return &f
	}
	months := make([]PLMonthView, 12)
	for m := 0; m < 12; m++ {
		c := m + 1
		mv := PLMonthView{Idx: m, Name: monthsNom[m], Short: string([]rune(monthsNom[m])[:3]),
			Revenue: val(rowRev, c), Expenses: val(rowExp, c), Profit: val(rowProfit, c), Dividends: val(rowDiv, c), Margin: margin(c)}
		mv.HasData = mv.Revenue > 0 || mv.Expenses > 0
		if rowOst >= 0 {
			mv.Saldo = opt(rowOst, c)
		} else {
			s := mv.Profit - mv.Dividends
			mv.Saldo = &s
		}
		if rowKassa >= 0 {
			mv.Kassa = opt(rowKassa, c)
		}
		months[m] = mv
	}
	for m := 1; m < 12; m++ {
		months[m].KassaStart = months[m-1].Kassa
	}
	y := &PLYearView{}
	for _, mv := range months {
		y.Revenue += mv.Revenue
		y.Expenses += mv.Expenses
		y.Profit += mv.Profit
		y.Dividends += mv.Dividends
	}
	if y.Revenue != 0 {
		y.Margin = jsRound(y.Profit / y.Revenue * 100)
	}
	for i := 11; i >= 0; i-- {
		if months[i].Saldo != nil && months[i].HasData {
			y.Saldo = months[i].Saldo
			break
		}
	}
	var kassa *float64
	for i := 11; i >= 0; i-- {
		if months[i].Kassa != nil {
			kassa = months[i].Kassa
			break
		}
	}
	cur := int(now.In(Almaty).Month()) - 1
	return MonthlyPLView{OK: true, Kassa: kassa, Months: months, Year: y, CurrentMonthIdx: &cur}
}
