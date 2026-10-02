package club

import (
	"fmt"
	"math"
	"regexp"
	"strconv"
	"strings"
	"time"
	_ "time/tzdata" // Asia/Almaty must work even in a bare container
)

// Almaty is the club's clock: every date in the sheet is Almaty time.
var Almaty = mustLoc("Asia/Almaty")

func mustLoc(name string) *time.Location {
	l, err := time.LoadLocation(name)
	if err != nil {
		panic(err)
	}
	return l
}

// Sheets is the raw import: sheet name -> rows -> cells, exactly as the
// sheet displays them (Apps Script getDisplayValues).
type Sheets map[string][][]string

const (
	SheetDebet      = "BS - резиденты дебет"
	SheetDDS        = "Учет ДДС"
	SheetPL         = "PL"
	SheetFines      = "Штрафы"
	SheetSchedule   = "Расписание"
	SheetReports    = "Лог отчётов"
	SheetMeetingLog = "Лог встреч"
	SheetSettings   = "Настройки"
	SheetFormer     = "Бывшие резиденты"

	// The app's own sections (migration step 3): kept as displayed in
	// club_sheets and read by sections.go the way the script reads them.
	SheetWheel       = "Колесо"
	SheetLeads       = "CRM Лиды"
	SheetProblems    = "Стоимость проблем"
	SheetResTasks    = "Задачи резидентов"
	SheetSmm         = "СММ план"
	SheetContent     = "Контент" // the script's contentPlan reads «Контент», not «Контент-план»
	SheetLeadmagnets = "Лид-магниты"
	SheetLMHistory   = "История лид-магнитов"
	// SheetScriptProps is not a sheet: the script's properties the app's
	// sections need (WHEEL_AXES_<имя>, USEFUL_CL_IDX) as rows [key, value].
	// Its presence marks an import from a script that sends the sections.
	SheetScriptProps = "_props"
)

// SectionSheets are the app's section sheets the script sends since v33.
var SectionSheets = []string{SheetWheel, SheetLeads, SheetProblems, SheetResTasks, SheetSmm, SheetContent,
	SheetLeadmagnets, SheetLMHistory, SheetScriptProps}

// Parse reads every sheet the club needs. It never guesses silently: every
// row it cannot read is listed in the returned warnings.
func Parse(s Sheets) (*Snapshot, []string, error) {
	var warn []string
	w := func(f string, a ...any) { warn = append(warn, fmt.Sprintf(f, a...)) }
	snap := &Snapshot{}

	res, err := parseDebet(s[SheetDebet], w)
	if err != nil {
		return nil, warn, err
	}
	snap.Residents = res
	snap.Residents = append(snap.Residents, parseFormer(s[SheetFormer], res, w)...)
	if snap.Payments, err = parseDDS(s[SheetDDS], w); err != nil {
		return nil, warn, err
	}
	if snap.Fines, err = parseFines(s[SheetFines], w); err != nil {
		return nil, warn, err
	}
	snap.Meetings = parseSchedule(s[SheetSchedule], w)
	snap.Reports = parseReports(s[SheetReports], w)
	snap.MeetingLog = parseMeetingLog(s[SheetMeetingLog], w)
	snap.Settings = parseSettings(s[SheetSettings])
	snap.PL = parsePL(s[SheetPL], w)
	snap.Raw = Sheets{}
	for name, rows := range s {
		// The debet sheet is kept as displayed too: the data audit reads cells
		// the tables keep only as numbers («Месяцев» holding a partner's name).
		if !parsedSheets[name] || name == SheetPL || name == SheetDebet {
			snap.Raw[name] = rows
		}
	}
	return snap, warn, nil
}

// parsedSheets are read into the club tables; any other sheet is kept raw.
var parsedSheets = map[string]bool{SheetDebet: true, SheetDDS: true, SheetPL: true, SheetFines: true, SheetSchedule: true,
	SheetReports: true, SheetMeetingLog: true, SheetSettings: true, SheetFormer: true}

// ── cells ────────────────────────────────────────────────────────────────

func cell(row []string, i int) string {
	if i < 0 || i >= len(row) {
		return ""
	}
	return strings.TrimSpace(row[i])
}

func blank(v string) bool {
	v = strings.TrimSpace(v)
	return v == "" || v == "—" || v == "-" || v == "–"
}

var nonNum = regexp.MustCompile(`[^0-9.\-]`)

// Money reads "10,000", "-39,590", "1 000 000", "0" and "" (= 0).
// Grouping commas and spaces are dropped; a dot is a decimal point.
func Money(v string) (int64, bool) {
	if blank(v) {
		return 0, true
	}
	c := nonNum.ReplaceAllString(v, "")
	if c == "" || c == "-" {
		return 0, false
	}
	f, err := strconv.ParseFloat(c, 64)
	if err != nil {
		return 0, false
	}
	return int64(math.Round(f)), true
}

func yes(v string) bool {
	v = strings.ToLower(strings.TrimSpace(v))
	return v == "да" || v == "true" || v == "yes" || v == "1"
}

var (
	reDMY  = regexp.MustCompile(`^(\d{1,2})\.(\d{1,2})\.(\d{4})(?:[ T]+(\d{1,2}):(\d{2})(?::(\d{2}))?)?`)
	reMDY  = regexp.MustCompile(`^(\d{1,2})/(\d{1,2})/(\d{4})(?:[ T]+(\d{1,2}):(\d{2})(?::(\d{2}))?)?`)
	reISO  = regexp.MustCompile(`^(\d{4})-(\d{2})-(\d{2})(?:[ T]+(\d{1,2}):(\d{2})(?::(\d{2}))?)?`)
	reTime = regexp.MustCompile(`^(\d{1,2}):(\d{2})`)
)

// Date reads the date formats found in the sheet, as Almaty time.
func Date(v string) (time.Time, bool) {
	v = strings.TrimSpace(v)
	atoi := func(s string) int { n, _ := strconv.Atoi(s); return n }
	mk := func(y, mo, d int, hh, mm, ss string) (time.Time, bool) {
		if mo < 1 || mo > 12 || d < 1 || d > 31 {
			return time.Time{}, false
		}
		return time.Date(y, time.Month(mo), d, atoi(hh), atoi(mm), atoi(ss), 0, Almaty), true
	}
	if m := reDMY.FindStringSubmatch(v); m != nil {
		return mk(atoi(m[3]), atoi(m[2]), atoi(m[1]), m[4], m[5], m[6])
	}
	if m := reMDY.FindStringSubmatch(v); m != nil {
		return mk(atoi(m[3]), atoi(m[1]), atoi(m[2]), m[4], m[5], m[6])
	}
	if m := reISO.FindStringSubmatch(v); m != nil {
		return mk(atoi(m[1]), atoi(m[2]), atoi(m[3]), m[4], m[5], m[6])
	}
	return time.Time{}, false
}

// ClockTime normalises "15:00", "9:00", "15:00:00" to "15:00".
func ClockTime(v string) string {
	m := reTime.FindStringSubmatch(strings.TrimSpace(v))
	if m == nil {
		return ""
	}
	h, _ := strconv.Atoi(m[1])
	return fmt.Sprintf("%02d:%s", h, m[2])
}

func normHeader(s string) string {
	s = strings.ToLower(strings.ReplaceAll(s, "ё", "е"))
	return strings.Join(strings.Fields(s), " ")
}

// headerRow finds the header among the first rows: the first row that has
// a cell equal to one of the given titles.
func headerRow(rows [][]string, titles ...string) int {
	for i := 0; i < len(rows) && i < 6; i++ {
		for _, c := range rows[i] {
			n := normHeader(c)
			for _, t := range titles {
				if n == normHeader(t) {
					return i
				}
			}
		}
	}
	return -1
}

// columns maps wanted fields to column indexes by header text. A field whose
// header contains the given words (all of them) gets that column.
func columns(header []string, want map[string][]string) map[string]int {
	out := map[string]int{}
	for field, words := range want {
		out[field] = -1
		for i, h := range header {
			n := normHeader(h)
			ok := n != ""
			for _, wd := range words {
				if !strings.Contains(n, wd) {
					ok = false
					break
				}
			}
			if ok {
				out[field] = i
				break
			}
		}
	}
	return out
}

// ── листы ────────────────────────────────────────────────────────────────

func parseDebet(rows [][]string, w func(string, ...any)) ([]Resident, error) {
	h := headerRow(rows, "Имя резидента")
	if h < 0 {
		return nil, fmt.Errorf("лист «%s»: не найдена шапка с колонкой «Имя резидента»", SheetDebet)
	}
	col := columns(rows[h], map[string][]string{
		"name": {"имя"}, "tariff": {"тариф"}, "granted": {"встреч", "оплачено"}, "done": {"встреч", "проведено"},
		"left": {"осталось"}, "paid": {"оплачено", "вход"}, "rest": {"остаток", "вход"}, "renew": {"долг", "продление"},
		"fines": {"штрафы"}, "total": {"общий", "долг"}, "format": {"формат"}, "chat": {"chat id"},
		"former": {"бывший"}, "except": {"исключение"}, "admin": {"админ"}, "source": {"источник"},
		"joined": {"дата входа"}, "months": {"месяцев"}, "note": {"примечание"}, "partner": {"партнер"},
	})
	for _, need := range []string{"name", "tariff", "granted", "done", "paid", "rest", "renew", "chat", "former", "except", "admin", "format"} {
		if col[need] < 0 {
			return nil, fmt.Errorf("лист «%s»: нет колонки для «%s»", SheetDebet, need)
		}
	}
	var out []Resident
	for i := h + 1; i < len(rows); i++ {
		r := rows[i]
		name := cell(r, col["name"])
		if name == "" {
			continue
		}
		num := func(f string) int64 {
			v, ok := Money(cell(r, col[f]))
			if !ok {
				w("%s, строка %d (%s): «%s» не число, взят 0", SheetDebet, i+1, name, cell(r, col[f]))
			}
			return v
		}
		p := Resident{
			Name: name, Tariff: num("tariff"), Granted: num("granted"), Done: num("done"),
			PaidEntry: num("paid"), RestEntry: num("rest"), RenewDebt: num("renew"),
			Format: cell(r, col["format"]), Former: yes(cell(r, col["former"])),
			Exception: yes(cell(r, col["except"])), Admin: yes(cell(r, col["admin"])),
			Source: cell(r, col["source"]), Note: cell(r, col["note"]), Partner: cell(r, col["partner"]),
		}
		if col["months"] >= 0 {
			p.Months, _ = Money(cell(r, col["months"]))
		}
		if id := strings.TrimSpace(cell(r, col["chat"])); id != "" {
			// A Chat ID is an integer; "123456789.0" or a stray space must not add digits.
			if f, err := strconv.ParseFloat(strings.ReplaceAll(id, ",", ""), 64); err == nil && f > 0 && f == math.Trunc(f) {
				p.TgID = int64(f)
			} else {
				w("%s, строка %d (%s): Chat ID «%s» не читается", SheetDebet, i+1, name, id)
			}
		}
		if d, ok := Date(cell(r, col["joined"])); ok {
			p.JoinedAt = &d
		}
		for f, dst := range map[string]**int64{"left": &p.SheetLeft, "fines": &p.SheetFines, "total": &p.SheetTotal} {
			if col[f] >= 0 {
				if v, ok := Money(cell(r, col[f])); ok {
					vv := v
					*dst = &vv
				}
			}
		}
		out = append(out, p)
	}
	return out, nil
}

func parseFormer(rows [][]string, active []Resident, w func(string, ...any)) []Resident {
	if len(rows) == 0 {
		return nil
	}
	h := headerRow(rows, "Имя")
	if h < 0 {
		w("лист «%s»: не найдена шапка, бывшие не перенесены", SheetFormer)
		return nil
	}
	col := columns(rows[h], map[string][]string{
		"name": {"имя"}, "paid": {"оплачено"}, "rest": {"остаток"}, "renew": {"долг"}, "tariff": {"тариф"},
		"source": {"источник"}, "joined": {"дата входа"}, "left": {"дата выхода"}, "note": {"примечание"}, "status": {"статус"},
	})
	isActive := map[string]bool{}
	for _, a := range active {
		isActive[NormName(a.Name)] = true
	}
	var out []Resident
	for i := h + 1; i < len(rows); i++ {
		r := rows[i]
		name := cell(r, col["name"])
		if blank(name) || isActive[NormName(name)] {
			continue // вернувшийся резидент живёт в дебете
		}
		m := func(f string) int64 { v, _ := Money(cell(r, col[f])); return v }
		p := Resident{Name: name, Former: true, Archived: true, PaidEntry: m("paid"), RestEntry: m("rest"), RenewDebt: m("renew"),
			Tariff: m("tariff"), Source: strings.Trim(cell(r, col["source"]), "— "), Note: strings.Trim(cell(r, col["note"]), "— ")}
		if d, ok := Date(cell(r, col["joined"])); ok {
			p.JoinedAt = &d
		}
		if d, ok := Date(cell(r, col["left"])); ok {
			p.LeftAt = &d
		}
		out = append(out, p)
	}
	return out
}

func parseDDS(rows [][]string, w func(string, ...any)) ([]Payment, error) {
	h := headerRow(rows, "Дата")
	if h < 0 {
		return nil, fmt.Errorf("лист «%s»: не найдена шапка", SheetDDS)
	}
	col := columns(rows[h], map[string][]string{
		"date": {"дата"}, "in": {"приход"}, "out": {"расход"}, "src": {"источник"}, "cplus": {"категория +"}, "cminus": {"категория -"},
	})
	for _, need := range []string{"date", "in", "out", "src", "cplus", "cminus"} {
		if col[need] < 0 {
			return nil, fmt.Errorf("лист «%s»: нет колонки для «%s»", SheetDDS, need)
		}
	}
	applied := col["cminus"] + 1 // безымянная колонка G: «учтено»
	var out []Payment
	for i := h + 1; i < len(rows); i++ {
		r := rows[i]
		ds, in, ex := cell(r, col["date"]), cell(r, col["in"]), cell(r, col["out"])
		if ds == "" && blank(in) && blank(ex) {
			continue
		}
		d, ok := Date(ds)
		if !ok {
			w("%s, строка %d: дата «%s» не читается, строка пропущена", SheetDDS, i+1, ds)
			continue
		}
		iv, ok1 := Money(in)
		ev, ok2 := Money(ex)
		if !ok1 || !ok2 {
			w("%s, строка %d: сумма не читается («%s» / «%s»), строка пропущена", SheetDDS, i+1, in, ex)
			continue
		}
		out = append(out, Payment{Row: i + 1, Date: d, Income: iv, Expense: ev,
			IncomeCat: cell(r, col["src"]), Resident: cell(r, col["cplus"]), ExpenseCat: cell(r, col["cminus"]),
			Applied: strings.EqualFold(cell(r, applied), "учтено")})
	}
	return out, nil
}

func parseFines(rows [][]string, w func(string, ...any)) ([]Fine, error) {
	if len(rows) == 0 {
		return nil, nil
	}
	h := headerRow(rows, "Имя резидента")
	if h < 0 {
		return nil, fmt.Errorf("лист «%s»: не найдена шапка", SheetFines)
	}
	col := columns(rows[h], map[string][]string{"name": {"имя"}, "type": {"тип"}, "amount": {"сумма"}, "date": {"дата"}, "status": {"статус"}})
	var out []Fine
	for i := h + 1; i < len(rows); i++ {
		r := rows[i]
		name := cell(r, col["name"])
		if name == "" {
			continue
		}
		a, ok := Money(cell(r, col["amount"]))
		if !ok {
			w("%s, строка %d (%s): сумма «%s» не читается, штраф пропущен", SheetFines, i+1, name, cell(r, col["amount"]))
			continue
		}
		d, _ := Date(cell(r, col["date"]))
		st := cell(r, col["status"])
		if st == "" {
			st = "Не оплатил" // as the script reads an empty status
		}
		out = append(out, Fine{Name: name, Type: cell(r, col["type"]), Amount: a, Date: d,
			Paid: strings.EqualFold(st, "Оплатил"), Row: i + 1, Status: st})
	}
	return out, nil
}

func parseSchedule(rows [][]string, w func(string, ...any)) []Meeting {
	h := headerRow(rows, "Резидент")
	if h < 0 {
		return nil
	}
	col := columns(rows[h], map[string][]string{"res": {"резидент"}, "date": {"дата"}, "time": {"время"}, "addr": {"адрес"}, "link": {"meet"}})
	var out []Meeting
	for i := h + 1; i < len(rows); i++ {
		r := rows[i]
		name := cell(r, col["res"])
		if name == "" {
			continue // пустые строки с одними галочками
		}
		d, ok := Date(cell(r, col["date"]))
		if !ok {
			w("%s, строка %d (%s): дата «%s» не читается", SheetSchedule, i+1, name, cell(r, col["date"]))
			continue
		}
		m := Meeting{Resident: name, Date: d, Time: ClockTime(cell(r, col["time"])), Row: i + 1,
			AddrCell: cell(r, 4), LinkCell: cell(r, 5), HCell: cell(r, 7)}
		place := cell(r, col["addr"])
		link := cell(r, col["link"])
		if strings.HasPrefix(link, "http") {
			m.Link, m.Online = link, true
		} else if link != "" && place == "" {
			place = link // адрес офлайн-встречи скрипт кладёт в колонку ссылки
		}
		m.Place = place
		g, hh, ii, jj := cell(r, 6), cell(r, 7), cell(r, 8), cell(r, 9)
		m.Done = strings.Contains(g, "Проведена")
		m.Sent3d = strings.Contains(g, "✅")
		m.Sent1d = strings.Contains(hh, "✅")
		m.Sent1h = strings.Contains(ii, "✅")
		if jj != "" && !strings.Contains(jj, "✅") {
			m.EventID = jj
		}
		out = append(out, m)
	}
	return out
}

func parseReports(rows [][]string, w func(string, ...any)) []ReportEntry {
	var out []ReportEntry
	for i := 1; i < len(rows); i++ { // первая строка — шапка
		r := rows[i]
		if cell(r, 2) == "" {
			continue
		}
		e := ReportEntry{Username: cell(r, 1), Name: cell(r, 2), Text: cell(r, 3), Thread: cell(r, 6), Late: yes(cell(r, 7))}
		if ms, ok := Money(cell(r, 4)); ok && ms > 1e12 {
			e.At = time.UnixMilli(ms).In(Almaty) // точный момент отправки
		} else if d, ok := Date(cell(r, 0)); ok {
			e.At = d
		} else {
			w("%s, строка %d (%s): время не читается", SheetReports, i+1, e.Name)
			continue
		}
		if id, ok := Money(cell(r, 5)); ok {
			e.TgUserID = id
		}
		if d, ok := Date(cell(r, 0)); ok {
			e.ShownAt = &d
		}
		out = append(out, e)
	}
	return out
}

func parseMeetingLog(rows [][]string, w func(string, ...any)) []MeetingLogEntry {
	var out []MeetingLogEntry
	for i := 1; i < len(rows); i++ {
		r := rows[i]
		if cell(r, 1) == "" {
			continue
		}
		d, ok := Date(cell(r, 0))
		if !ok {
			w("%s, строка %d: дата «%s» не читается", SheetMeetingLog, i+1, cell(r, 0))
			continue
		}
		out = append(out, MeetingLogEntry{Date: d, Resident: cell(r, 1), Time: cell(r, 2)})
	}
	return out
}

func parseSettings(rows [][]string) []Setting {
	var out []Setting
	for _, r := range rows {
		k, v := cell(r, 0), ""
		if len(r) > 1 {
			v = r[1] // текст бота целиком, с переносами строк
		}
		if k == "" || strings.HasPrefix(k, "──") || strings.HasPrefix(k, "⚙") || normHeader(k) == "ключ" || v == "" {
			continue
		}
		out = append(out, Setting{Key: k, Value: v})
	}
	return out
}

func parsePL(rows [][]string, w func(string, ...any)) *PLSheet {
	if len(rows) < 4 {
		return nil
	}
	pl := &PLSheet{}
	if len(rows[0]) > 1 {
		pl.Year, _ = strconv.Atoi(cell(rows[0], 1))
	}
	section := ""
	for i := 2; i < len(rows); i++ {
		r := rows[i]
		name := cell(r, 0)
		n := normHeader(name)
		switch {
		case name == "" || n == "статья":
			continue
		case n == "доходы":
			section = "income"
			continue
		case n == "расходы":
			section = "expense"
			continue
		}
		row := PLRow{Name: name, Section: section}
		switch {
		case strings.HasPrefix(n, "итого доход"):
			row.Section = "total_income"
		case strings.HasPrefix(n, "итого расход"):
			row.Section = "total_expense"
		case strings.HasPrefix(n, "чистая прибыль"):
			row.Section = "profit"
		case strings.HasPrefix(n, "дивиденд"):
			row.Section = "dividends"
		case strings.HasPrefix(n, "на кассе"):
			row.Section = "cash"
		}
		for m := 0; m < 12; m++ {
			v := cell(r, m+1)
			if blank(v) {
				continue
			}
			if x, ok := Money(v); ok {
				row.Values[m], row.Set[m] = x, true
			}
		}
		pl.Rows = append(pl.Rows, row)
	}
	return pl
}

// NormName makes "Даулёт  Сайты" and "даулет сайты" equal.
func NormName(s string) string {
	s = strings.ToLower(strings.ReplaceAll(strings.ReplaceAll(s, "Ё", "Е"), "ё", "е"))
	return strings.Join(strings.Fields(s), " ")
}
