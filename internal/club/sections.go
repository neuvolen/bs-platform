package club

import (
	"encoding/json"
	"math"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"
)

// The app's own sections (moving the club off the sheet, step 3): the
// wheels, leads, problems, residents' tasks, the SMM plan, the content
// queue, the checklists and their schedule. The script builds them from its
// sheets (_miniGetWheelAll, _miniGetLeads…); since v33 it sends those sheets
// with the hourly import, as displayed, and the server builds the same
// sections from them here, field by field as the script does.
//
// A section is built only when the import brought the script's sections
// (SheetScriptProps is there): an older script does not send them, and the
// app then still gets them from the script.

// WheelDNAAxes and WheelLifeAxes are the script's WHEEL_DNA_AXES / WHEEL_LIFE_AXES.
var (
	WheelDNAAxes  = []string{"Стратегия", "Финансы", "Команда", "Процессы", "Продажи", "Маркетинг", "Продукт", "Делегирование"}
	WheelLifeAxes = []string{"Бизнес", "Здоровье", "Семья", "Окружение", "Личные финансы", "Развитие", "Отдых", "Смысл"}
)

// HasSections: the import brought the app's section sheets.
func HasSections(raw Sheets) bool {
	_, ok := raw[SheetScriptProps]
	return ok
}

// ScriptProps reads the pseudo-sheet of script properties.
func ScriptProps(raw Sheets) map[string]string {
	out := map[string]string{}
	for _, r := range raw[SheetScriptProps] {
		if len(r) >= 2 && strings.TrimSpace(r[0]) != "" {
			out[strings.TrimSpace(r[0])] = r[1]
		}
	}
	return out
}

// dataRows: the sheet's rows from the given 1-based row on, as the script's
// getRange(from, 1, lastRow-from+1, n) reads them; i is the 1-based row.
func dataRows(rows [][]string, from int, fn func(i int, r []string)) {
	last := lastRow(rows)
	for i := from; i <= last; i++ {
		fn(i, rows[i-1])
	}
}

// lastRow is the sheet's getLastRow: the last row with anything in it.
func lastRow(rows [][]string) int {
	for i := len(rows) - 1; i >= 0; i-- {
		for _, c := range rows[i] {
			if c != "" {
				return i + 1
			}
		}
	}
	return 0
}

// jsStr is String(cell||"") of a displayed cell.
func jsStr(r []string, i int) string {
	if i < 0 || i >= len(r) {
		return ""
	}
	return r[i]
}

// jsNum is Number(cell)||0.
func jsNum(r []string, i int) float64 {
	v := strings.TrimSpace(jsStr(r, i))
	if v == "" {
		return 0
	}
	f, ok := Num(v)
	if !ok || math.IsNaN(f) {
		return 0
	}
	return f
}

// A date cell as the sheet displays it: the script's formats (DD.MM.YYYY,
// with HH:mm or HH:mm:ss) or the sheet's default (M/d/yyyy H:mm:ss). Text
// that only looks like a date ("06.05.2026 9:30:00" typed by hand) is text
// for the script too.
var reDateCell = regexp.MustCompile(`^(\d{2}\.\d{2}\.\d{4}( \d{2}:\d{2}(:\d{2})?)?|\d{1,2}/\d{1,2}/\d{4}( \d{1,2}:\d{2}:\d{2})?)$`)

// cellDate: the cell holds a date (as the script's instanceof Date sees it).
func cellDate(r []string, i int) (time.Time, bool) {
	v := strings.TrimSpace(jsStr(r, i))
	if v == "" || !reDateCell.MatchString(v) {
		return time.Time{}, false
	}
	return Date(v)
}

func fmtCell(r []string, i int, layout string) string {
	if d, ok := cellDate(r, i); ok {
		return d.In(Almaty).Format(layout)
	}
	return jsStr(r, i)
}

func tsCell(r []string, i int) int64 {
	if d, ok := cellDate(r, i); ok {
		return d.UnixMilli()
	}
	return 0
}

// jsRound1 is Math.round(x*10)/10.
func jsRound1(x float64) float64 { return float64(jsRound(x*10)) / 10 }

// localeLess approximates String.localeCompare for names: case and ё do not
// matter, Latin before Cyrillic.
func localeLess(a, b string) bool {
	fold := func(s string) string {
		return strings.Map(func(r rune) rune {
			if r == 'ё' || r == 'Ё' {
				return 'е'
			}
			return unicode.ToLower(r)
		}, s)
	}
	fa, fb := fold(a), fold(b)
	if fa != fb {
		return fa < fb
	}
	return a < b
}

// ── Колесо ──

type WheelRec struct {
	Date   string    `json:"date"`
	Values []float64 `json:"values"`
}

type WheelBiz struct {
	Name string     `json:"name"`
	Rows []WheelRec `json:"rows"`
}

type WheelOf struct {
	DNAAxes    []string   `json:"dnaAxes"`
	LifeAxes   []string   `json:"lifeAxes"`
	Businesses []WheelBiz `json:"businesses"`
	DNA        []WheelRec `json:"dna"`
	Life       []WheelRec `json:"life"`
}

// WheelAxes: a resident's own names of the 7 personal axes (nil: not set).
func WheelAxes(props map[string]string, name string) []string {
	raw := props["WHEEL_AXES_"+name]
	if raw == "" {
		return nil
	}
	var arr []string
	if json.Unmarshal([]byte(raw), &arr) != nil || len(arr) != 7 {
		return nil
	}
	return arr
}

// WheelAll is _miniGetWheelAll: every resident's wheels, newest first.
func WheelAll(raw Sheets) map[string]*WheelOf {
	rows := raw[SheetWheel]
	props := ScriptProps(raw)
	out := map[string]*WheelOf{}
	type acc struct {
		w     *WheelOf
		biz   map[string]int
		order []string
	}
	by := map[string]*acc{}
	last := lastRow(rows)
	for i := last; i >= 2; i-- {
		r := rows[i-1]
		nm := strings.TrimSpace(jsStr(r, 1))
		if nm == "" {
			continue
		}
		tp := strings.TrimSpace(jsStr(r, 2))
		vals := make([]float64, 0, 8)
		for j := 3; j < 11; j++ {
			vals = append(vals, jsNum(r, j))
		}
		a := by[nm]
		if a == nil {
			life := WheelLifeAxes
			if c := WheelAxes(props, nm); c != nil {
				life = append([]string{"Бизнес"}, c...)
			}
			a = &acc{w: &WheelOf{DNAAxes: WheelDNAAxes, LifeAxes: life, Businesses: []WheelBiz{}, DNA: []WheelRec{}, Life: []WheelRec{}},
				biz: map[string]int{}}
			by[nm] = a
			out[nm] = a.w
		}
		rec := WheelRec{Date: fmtCell(r, 0, "02.01.2006 15:04"), Values: vals}
		switch tp {
		case "ДНК":
			bn := strings.TrimSpace(jsStr(r, 11))
			if bn == "" {
				bn = "Основной"
			}
			k, ok := a.biz[bn]
			if !ok {
				k = len(a.w.Businesses)
				a.biz[bn] = k
				a.w.Businesses = append(a.w.Businesses, WheelBiz{Name: bn, Rows: []WheelRec{}})
			}
			a.w.Businesses[k].Rows = append(a.w.Businesses[k].Rows, rec)
		case "Личное":
			a.w.Life = append(a.w.Life, rec)
		}
	}
	for _, a := range by {
		if len(a.w.Businesses) > 0 {
			a.w.DNA = a.w.Businesses[0].Rows
		}
	}
	return out
}

type WheelSummaryRow struct {
	Name     string   `json:"name"`
	DNAAvg   *float64 `json:"dnaAvg"`
	DNADate  string   `json:"dnaDate"`
	DNACount int      `json:"dnaCount"`
	LifeAvg  *float64 `json:"lifeAvg"`
	LifeDate string   `json:"lifeDate"`
	Total    int      `json:"total"`
}

type WheelSummaryView struct {
	Summary []WheelSummaryRow `json:"summary"`
}

// WheelSummary is _miniGetWheelSummary: the last measurements of everyone.
func WheelSummary(raw Sheets) WheelSummaryView {
	rows := raw[SheetWheel]
	type last struct {
		avg  float64
		date string
	}
	type res struct {
		biz   map[string]last
		order []string // Object.keys: in the order first seen
		life  *last
		total int
	}
	by := map[string]*res{}
	var names []string
	dataRows(rows, 2, func(_ int, r []string) {
		nm := strings.TrimSpace(jsStr(r, 1))
		if nm == "" {
			return
		}
		tp := strings.TrimSpace(jsStr(r, 2))
		x := by[nm]
		if x == nil {
			x = &res{biz: map[string]last{}}
			by[nm] = x
			names = append(names, nm)
		}
		x.total++
		var sum float64
		for j := 3; j < 11; j++ {
			sum += jsNum(r, j)
		}
		l := last{avg: jsRound1(sum / 8), date: fmtCell(r, 0, "02.01.2006")}
		switch tp {
		case "ДНК":
			bn := strings.TrimSpace(jsStr(r, 11))
			if bn == "" {
				bn = "Основной"
			}
			if _, ok := x.biz[bn]; !ok {
				x.order = append(x.order, bn)
			}
			x.biz[bn] = l
		case "Личное":
			x.life = &l
		}
	})
	out := WheelSummaryView{Summary: []WheelSummaryRow{}}
	for _, nm := range names {
		x := by[nm]
		row := WheelSummaryRow{Name: nm, DNACount: len(x.biz), Total: x.total}
		if len(x.biz) > 0 {
			var t float64
			for _, bn := range x.order {
				t += x.biz[bn].avg
				if x.biz[bn].date > row.DNADate {
					row.DNADate = x.biz[bn].date
				}
			}
			v := jsRound1(t / float64(len(x.biz)))
			row.DNAAvg = &v
		}
		if x.life != nil {
			v := x.life.avg
			row.LifeAvg, row.LifeDate = &v, x.life.date
		}
		out.Summary = append(out.Summary, row)
	}
	sort.SliceStable(out.Summary, func(i, j int) bool { return localeLess(out.Summary[i].Name, out.Summary[j].Name) })
	return out
}

// ── CRM Лиды ──

type LeadView struct {
	Row      int    `json:"row"`
	Date     string `json:"date"`
	TS       int64  `json:"ts"`
	Name     string `json:"name"`
	Phone    string `json:"phone"`
	Telegram string `json:"telegram"`
	Source   string `json:"source"`
	Campaign string `json:"campaign"`
	Niche    string `json:"niche"`
	Revenue  string `json:"revenue"`
	Request  string `json:"request"`
	Status   string `json:"status"`
	Owner    string `json:"owner"`
	Comment  string `json:"comment"`
}

type LeadsView struct {
	Leads []LeadView `json:"leads"`
}

func orDefault(v, def string) string {
	if v == "" {
		return def
	}
	return v
}

// Leads is _miniGetLeads: newest first.
func Leads(raw Sheets) LeadsView {
	out := LeadsView{Leads: []LeadView{}}
	dataRows(raw[SheetLeads], 2, func(i int, r []string) {
		name, phone := strings.TrimSpace(jsStr(r, 1)), strings.TrimSpace(jsStr(r, 2))
		if name == "" && phone == "" {
			return
		}
		l := LeadView{Row: i, Name: name, Phone: phone, Telegram: jsStr(r, 3), Source: jsStr(r, 4), Campaign: jsStr(r, 5),
			Niche: jsStr(r, 6), Revenue: jsStr(r, 7), Request: jsStr(r, 8), Status: strings.TrimSpace(orDefault(jsStr(r, 9), "Новый")),
			Owner: jsStr(r, 10), Comment: jsStr(r, 11), TS: tsCell(r, 0)}
		if d, ok := cellDate(r, 0); ok {
			l.Date = dmy(d)
		}
		out.Leads = append(out.Leads, l)
	})
	sort.SliceStable(out.Leads, func(i, j int) bool { return out.Leads[i].TS > out.Leads[j].TS })
	return out
}

// ── Стоимость проблем ──

type ProblemView struct {
	Row     int     `json:"row"`
	Name    string  `json:"name"`
	Date    string  `json:"date"`
	Problem string  `json:"problem"`
	Cost    float64 `json:"cost"`
	Status  string  `json:"status"`
}

type ProblemsView struct {
	Problems []ProblemView `json:"problems"`
	Total    float64       `json:"total"`
}

// Problems is _miniGetProblems({}): everyone's, newest first.
func Problems(raw Sheets) ProblemsView {
	out := ProblemsView{Problems: []ProblemView{}}
	dataRows(raw[SheetProblems], 2, func(i int, r []string) {
		nm := strings.TrimSpace(jsStr(r, 1))
		if nm == "" {
			return
		}
		p := ProblemView{Row: i, Name: nm, Date: fmtCell(r, 0, "02.01.2006"), Problem: jsStr(r, 2), Cost: jsNum(r, 3),
			Status: strings.TrimSpace(orDefault(jsStr(r, 4), "Открыта"))}
		if p.Status == "Открыта" {
			out.Total += p.Cost
		}
		out.Problems = append(out.Problems, p)
	})
	reverse(out.Problems)
	return out
}

func reverse[T any](s []T) {
	for i, j := 0, len(s)-1; i < j; i, j = i+1, j-1 {
		s[i], s[j] = s[j], s[i]
	}
}

// ── Задачи резидентов ──

type ResTaskView struct {
	Row       int    `json:"row"`
	Date      string `json:"date"`
	Name      string `json:"name"`
	Task      string `json:"task"`
	Status    string `json:"status"`
	Comment   string `json:"comment"`
	Recurring bool   `json:"recurring"`
	Author    string `json:"author"`
	IsOwn     bool   `json:"isOwn"`
	Updated   string `json:"updated"`
}

type ResTasksView struct {
	Tasks []ResTaskView `json:"tasks"`
}

// ResTasks is _miniGetResTasks({}): everyone's, newest first.
func ResTasks(raw Sheets) ResTasksView {
	out := ResTasksView{Tasks: []ResTaskView{}}
	dataRows(raw[SheetResTasks], 2, func(i int, r []string) {
		rn := strings.TrimSpace(jsStr(r, 1))
		if rn == "" {
			return
		}
		st := strings.TrimSpace(orDefault(jsStr(r, 3), "Открыта"))
		switch st {
		case "Принята", "На проверке":
			st = "Выполнена"
		case "Возвращена":
			st = "Открыта"
		}
		out.Tasks = append(out.Tasks, ResTaskView{Row: i, Date: fmtCell(r, 0, "02.01.2006"), Name: rn, Task: jsStr(r, 2),
			Status: st, Comment: jsStr(r, 4), Recurring: strings.Contains(jsStr(r, 4), "Постоянная"),
			Author: strings.TrimSpace(orDefault(jsStr(r, 6), "Админ")), IsOwn: strings.TrimSpace(jsStr(r, 6)) == "Резидент",
			Updated: fmtCell(r, 5, "02.01")})
	})
	reverse(out.Tasks)
	return out
}

// ── СММ план ──

type SmmItem struct {
	Row      int    `json:"row"`
	Date     string `json:"date"`
	TS       int64  `json:"ts"`
	Platform string `json:"platform"`
	Rubric   string `json:"rubric"`
	Title    string `json:"title"`
	Text     string `json:"text"`
	Status   string `json:"status"`
	Link     string `json:"link"`
}

type SmmView struct {
	Items []SmmItem `json:"items"`
}

// Smm is _miniGetSmm: by date.
func Smm(raw Sheets) SmmView {
	out := SmmView{Items: []SmmItem{}}
	dataRows(raw[SheetSmm], 2, func(i int, r []string) {
		title := strings.TrimSpace(jsStr(r, 3))
		if title == "" && strings.TrimSpace(jsStr(r, 4)) == "" {
			return
		}
		out.Items = append(out.Items, SmmItem{Row: i, Date: fmtCell(r, 0, "02.01.2006"), TS: tsCell(r, 0),
			Platform: orDefault(jsStr(r, 1), "Instagram"), Rubric: jsStr(r, 2), Title: title, Text: jsStr(r, 4),
			Status: strings.TrimSpace(orDefault(jsStr(r, 5), "Идея")), Link: jsStr(r, 6)})
	})
	sort.SliceStable(out.Items, func(i, j int) bool { return out.Items[i].TS < out.Items[j].TS })
	return out
}

// ── Контент (очередь «Полезного») ──

type ContentItem struct {
	Row    int    `json:"row"`
	Num    any    `json:"num"`
	Cat    string `json:"cat"`
	Text   string `json:"text"`
	Date   string `json:"date"`
	TS     int64  `json:"ts"`
	Status string `json:"status"`
}

type ContentView struct {
	Items []ContentItem `json:"items"`
}

// ContentPlan is _miniGetContentPlan: waiting posts first, by date.
func ContentPlan(raw Sheets) ContentView {
	out := ContentView{Items: []ContentItem{}}
	rows, ok := raw[SheetContent]
	if !ok {
		return out
	}
	dataRows(rows, 3, func(i int, r []string) {
		text := strings.TrimSpace(jsStr(r, 2))
		if text == "" {
			return
		}
		var num any = jsStr(r, 0)
		if v := strings.TrimSpace(jsStr(r, 0)); v != "" {
			if f, ok := Num(v); ok {
				num = f
			}
		}
		out.Items = append(out.Items, ContentItem{Row: i, Num: num, Cat: jsStr(r, 1), Text: text,
			Date: fmtCell(r, 3, "02.01.2006 15:04"), TS: tsCell(r, 3), Status: strings.TrimSpace(orDefault(jsStr(r, 4), "Ожидает"))})
	})
	sort.SliceStable(out.Items, func(i, j int) bool {
		a, b := out.Items[i], out.Items[j]
		if a.Status == b.Status {
			return a.TS < b.TS
		}
		return a.Status == "Ожидает"
	})
	return out
}

// ── Лид-магниты ──

type LeadmagnetItem struct {
	Key         string `json:"key"`
	Title       string `json:"title"`
	FileID      string `json:"fileId"`
	Description string `json:"description"`
	Category    string `json:"category"`
}

type LeadmagnetsView struct {
	OK    bool             `json:"ok"`
	Items []LeadmagnetItem `json:"items"`
	Taken map[string]bool  `json:"taken"`
}

// Leadmagnets is _miniGetLeadmagnetsList({chatId}): the checklists and which
// of them this person already took.
func Leadmagnets(raw Sheets, chatID string) LeadmagnetsView {
	out := LeadmagnetsView{OK: true, Items: []LeadmagnetItem{}, Taken: map[string]bool{}}
	dataRows(raw[SheetLeadmagnets], 2, func(_ int, r []string) {
		out.Items = append(out.Items, LeadmagnetItem{Key: jsStr(r, 0), Title: jsStr(r, 1), FileID: jsStr(r, 2),
			Description: jsStr(r, 3), Category: strings.ToLower(jsStr(r, 4))})
	})
	if chatID != "" {
		dataRows(raw[SheetLMHistory], 2, func(_ int, r []string) {
			if jsStr(r, 0) == chatID {
				out.Taken[jsStr(r, 1)] = true
			}
		})
	}
	return out
}

type ChecklistSlot struct {
	Name string `json:"name"`
	Key  string `json:"key"`
	Date string `json:"date"`
	Next bool   `json:"next"`
}

// ChecklistSchedule is the script's «График чек-листов»: the next six
// Mondays at 11:00, the ready checklists in turn from USEFUL_CL_IDX.
func ChecklistSchedule(raw Sheets, lm LeadmagnetsView, now time.Time) []ChecklistSlot {
	var ready []LeadmagnetItem
	for _, it := range lm.Items {
		if it.FileID != "" {
			ready = append(ready, it)
		}
	}
	out := []ChecklistSlot{}
	if len(ready) == 0 {
		return out
	}
	idx, err := strconv.Atoi(strings.TrimSpace(ScriptProps(raw)["USEFUL_CL_IDX"]))
	if err != nil {
		idx = 0
	}
	n := now.In(Almaty)
	dd := time.Date(n.Year(), n.Month(), n.Day(), 11, 0, 0, 0, Almaty)
	for dd.Weekday() != time.Monday || !dd.After(n) {
		dd = dd.AddDate(0, 0, 1)
	}
	for s := 0; s < len(ready) && s < 6; s++ {
		k := (idx + s) % len(ready)
		if k < 0 {
			k += len(ready)
		}
		it := ready[k]
		out = append(out, ChecklistSlot{Name: it.Key, Key: it.Key, Date: dmy(dd.Add(time.Duration(s) * 7 * 24 * time.Hour)), Next: s == 0})
	}
	return out
}

// SectionKeys are the bundle's sections built here.
var SectionKeys = []string{"wheelSummary", "wheelAll", "leads", "problems", "resTasks", "smm", "leadmagnets", "contentPlan", "checklistSchedule"}

// AppSections builds the app's own sections for one person (chatID: the
// caller, for the checklists they took). Nothing when the import did not
// bring the section sheets.
func AppSections(s *Snapshot, chatID string, now time.Time) (map[string]any, []string) {
	if s == nil || !HasSections(s.Raw) {
		return map[string]any{}, nil
	}
	lm := Leadmagnets(s.Raw, chatID)
	out := map[string]any{
		"wheelSummary": WheelSummary(s.Raw), "wheelAll": WheelAll(s.Raw), "leads": Leads(s.Raw),
		"problems": Problems(s.Raw), "resTasks": ResTasks(s.Raw), "smm": Smm(s.Raw), "leadmagnets": lm,
		"contentPlan": ContentPlan(s.Raw), "checklistSchedule": ChecklistSchedule(s.Raw, lm, now),
	}
	return out, append([]string(nil), SectionKeys...)
}
