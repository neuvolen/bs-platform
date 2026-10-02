package club

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// The app's writes into its own sections (wheel, tasks, problems, SMM, leads,
// content, checklists, profiles) go to the server first, like the club's
// writes: the server changes its copy of the sheet (kept as displayed) the
// way the script changes the sheet, then the write goes on to the script.
// Each function below follows the script's _mini… function of the same
// action; a refusal returns the script's error text.

// SectionWriteActions: the app's section writes and the sheets they touch.
var SectionWriteActions = map[string][]string{
	"saveWheel": {SheetWheel}, "saveWheelAxes": {SheetScriptProps},
	"addResTask": {SheetResTasks}, "setResTaskStatus": {SheetResTasks}, "addOwnTask": {SheetResTasks},
	"editOwnTask": {SheetResTasks}, "deleteOwnTask": {SheetResTasks},
	"saveProblem": {SheetProblems}, "setProblemStatus": {SheetProblems}, "deleteProblem": {SheetProblems},
	"saveSmm": {SheetSmm}, "deleteSmm": {SheetSmm},
	"addLead": {SheetLeads}, "setLeadStatus": {SheetLeads},
	"addContentPost": {SheetContent}, "updateContentPost": {SheetContent}, "deleteContentPost": {SheetContent},
	"updateLeadmagnet": {SheetLeadmagnets}, "requestLeadmagnet": {SheetLeadmagnets, SheetLMHistory},
	"saveProfile": {SheetProfiles},
}

// SectionReapplyAfterSend: section writes that set a value (by row or key),
// safe to put on top of an import again even if the sheet has them.
var SectionReapplyAfterSend = map[string]bool{"saveWheelAxes": true, "setResTaskStatus": true, "editOwnTask": true,
	"setProblemStatus": true, "setLeadStatus": true, "updateContentPost": true, "updateLeadmagnet": true, "saveProfile": true}

var sectionHeaders = map[string][]string{
	SheetWheel:       {"Дата", "Резидент", "Тип", "В1", "В2", "В3", "В4", "В5", "В6", "В7", "В8", "Бизнес"},
	SheetResTasks:    {"Дата", "Резидент", "Задача", "Статус", "Комментарий", "Обновлено", "Кто создал"},
	SheetProblems:    {"Дата", "Резидент", "Проблема", "Стоимость в месяц", "Статус"},
	SheetSmm:         {"Дата", "Площадка", "Рубрика", "Заголовок", "Текст", "Статус", "Ссылка"},
	SheetLeads:       {"Дата", "Имя", "Телефон", "Telegram", "Источник", "Кампания", "Ниша", "Оборот", "Запрос", "Статус", "Ответственный", "Комментарий", "След. касание", "Сумма сделки"},
	SheetLMHistory:   {"Chat ID", "Ключ", "Дата", "Название"},
	SheetProfiles:    {"Chat ID", "Ниша", "О себе", "Чем поможет", "Instagram", "Телефон", "Аватар URL"},
	SheetLeadmagnets: {"Ключ", "Название", "File ID", "Дата загрузки", "Кто загрузил"},
}

// grid is one sheet being changed.
type grid struct{ rows [][]string }

func (g *grid) trim() { g.rows = g.rows[:lastRow(g.rows)] }

func (g *grid) last() int { return lastRow(g.rows) }

func (g *grid) append(vals ...string) int {
	g.trim()
	g.rows = append(g.rows, vals)
	return len(g.rows)
}

func (g *grid) get(row, col int) string {
	if row < 1 || row > len(g.rows) {
		return ""
	}
	return jsStr(g.rows[row-1], col-1)
}

func (g *grid) set(row, col int, v string) {
	for len(g.rows) < row {
		g.rows = append(g.rows, []string{})
	}
	r := g.rows[row-1]
	for len(r) < col {
		r = append(r, "")
	}
	r[col-1] = v
	g.rows[row-1] = r
}

func (g *grid) del(row int) {
	if row >= 1 && row <= len(g.rows) {
		g.rows = append(g.rows[:row-1], g.rows[row:]...)
	}
}

func copyGrid(rows [][]string) [][]string {
	out := make([][]string, len(rows))
	for i, r := range rows {
		out[i] = append([]string(nil), r...)
	}
	return out
}

func dt(t time.Time) string { return t.In(Almaty).Format("02.01.2006 15:04") }

// jsNumStr is a number as the sheet displays a plain number cell.
func jsNumStr(f float64) string { return strconv.FormatFloat(f, 'f', -1, 64) }

// grouped is "#,##0".
func grouped(n int64) string {
	s := strconv.FormatInt(n, 10)
	neg := strings.HasPrefix(s, "-")
	s = strings.TrimPrefix(s, "-")
	var b strings.Builder
	for i, c := range s {
		if i > 0 && (len(s)-i)%3 == 0 {
			b.WriteByte(',')
		}
		b.WriteRune(c)
	}
	if neg {
		return "-" + b.String()
	}
	return b.String()
}

var reLeadInt = regexp.MustCompile(`^\s*[+-]?\d+`)

// jsParseInt is parseInt(s); ok is false for NaN.
func jsParseInt(s string) (int, bool) {
	m := reLeadInt.FindString(s)
	if m == "" {
		return 0, false
	}
	n, err := strconv.Atoi(strings.TrimSpace(m))
	return n, err == nil
}

// parseAppDateTime reads the app's "dd.MM.yyyy[ HH:mm]" (time default def).
func parseAppDateTime(s, def string) (time.Time, bool) {
	parts := strings.SplitN(strings.TrimSpace(s), " ", 2)
	dp := strings.Split(parts[0], ".")
	if len(dp) != 3 {
		return time.Time{}, false
	}
	tp := def
	if len(parts) > 1 && strings.TrimSpace(parts[1]) != "" {
		tp = strings.TrimSpace(parts[1])
	}
	hm := strings.Split(tp, ":")
	d, ok1 := jsParseInt(dp[0])
	m, ok2 := jsParseInt(dp[1])
	y, ok3 := jsParseInt(dp[2])
	h, _ := jsParseInt(hm[0])
	mi := 0
	if len(hm) > 1 {
		mi, _ = jsParseInt(hm[1])
	}
	if !ok1 || !ok2 || !ok3 {
		return time.Time{}, false
	}
	return time.Date(y, time.Month(m), d, h, mi, 0, 0, Almaty), true
}

func jsonNoEscape(v any) string {
	var b bytes.Buffer
	e := json.NewEncoder(&b)
	e.SetEscapeHTML(false)
	_ = e.Encode(v)
	return strings.TrimRight(b.String(), "\n")
}

// ErrNoSection: the server does not hold this sheet (yet); the write goes
// to the script only.
var ErrNoSection = errors.New("no such section on the server")

// ApplySection changes the sheets an app section write touches. grids holds
// those sheets (absent: the sheet does not exist); it returns the sheets
// changed, with their new rows.
func ApplySection(action string, p map[string]string, at time.Time, grids map[string][][]string) (map[string][][]string, error) {
	sheets, ok := SectionWriteActions[action]
	if !ok {
		return nil, ErrNoSection
	}
	gs := map[string]*grid{}
	for _, n := range sheets {
		if rows, ok := grids[n]; ok {
			gs[n] = &grid{rows: copyGrid(rows)}
		}
	}
	changed := map[string]bool{}
	// sheet gives a sheet to change, created with its header as the script does.
	sheet := func(n string) *grid {
		changed[n] = true
		if g := gs[n]; g != nil {
			return g
		}
		g := &grid{}
		if h := sectionHeaders[n]; h != nil {
			g.rows = [][]string{append([]string(nil), h...)}
		}
		gs[n] = g
		return g
	}
	read := func(n string) *grid {
		if g := gs[n]; g != nil {
			return g
		}
		return &grid{}
	}
	str := func(k string) string { return strings.TrimSpace(p[k]) }
	row := func() (int, bool) { n, ok := jsParseInt(p["row"]); return n, ok }

	switch action {
	case "saveWheel":
		name, typ := str("name"), str("type")
		if name == "" {
			return nil, errors.New("Нет имени")
		}
		if typ != "dna" && typ != "life" {
			return nil, errors.New("Неверный тип")
		}
		var vals []string
		var nums []int
		for _, v := range strings.Split(str("values"), ",") {
			n, ok := jsParseInt(v)
			if !ok || n < 1 {
				n = 1
			}
			if n > 10 {
				n = 10
			}
			nums = append(nums, n)
			vals = append(vals, strconv.Itoa(n))
		}
		if typ == "dna" {
			if len(nums) != 8 {
				return nil, errors.New("Нужно 8 значений")
			}
			biz := Slice16(strings.TrimSpace(orDefault(p["bizName"], "Основной")), 30)
			if biz == "" {
				biz = "Основной"
			}
			sheet(SheetWheel).append(append(append([]string{dt(at), name, "ДНК"}, vals...), biz)...)
			break
		}
		if len(nums) != 7 {
			return nil, errors.New("Нужно 7 значений")
		}
		// Бизнес = среднее последних замеров всех направлений ДНК
		bizAvg := 5.0
		w := read(SheetWheel)
		last := map[string]float64{}
		var order []string
		for i := w.last(); i >= 2; i-- {
			r := w.rows[i-1]
			if strings.TrimSpace(jsStr(r, 1)) != name || strings.TrimSpace(jsStr(r, 2)) != "ДНК" {
				continue
			}
			bn := strings.TrimSpace(jsStr(r, 11))
			if bn == "" {
				bn = "Основной"
			}
			if _, ok := last[bn]; ok {
				continue
			}
			var sum float64
			for j := 3; j < 11; j++ {
				sum += jsNum(r, j)
			}
			last[bn] = sum / 8
			order = append(order, bn)
		}
		if len(order) > 0 {
			var t float64
			for _, bn := range order {
				t += last[bn]
			}
			bizAvg = jsRound1(t / float64(len(order)))
		}
		sheet(SheetWheel).append(append(append([]string{dt(at), name, "Личное", jsNumStr(bizAvg)}, vals...), "")...)

	case "saveWheelAxes":
		name := str("name")
		if name == "" {
			return nil, errors.New("Нет имени")
		}
		var axes []string
		for _, a := range strings.Split(str("axes"), "|") {
			if a = Slice16(strings.TrimSpace(a), 20); a != "" {
				axes = append(axes, a)
			}
		}
		if len(axes) != 7 {
			return nil, errors.New("Нужно 7 названий")
		}
		g := sheet(SheetScriptProps)
		key, val := "WHEEL_AXES_"+name, jsonNoEscape(axes)
		for i := 1; i <= g.last(); i++ {
			if g.get(i, 1) == key {
				g.set(i, 2, val)
				return result(gs, changed), nil
			}
		}
		g.append(key, val)

	case "addResTask", "addOwnTask":
		name, task := str("name"), str("task")
		if name == "" || task == "" {
			return nil, errors.New("Нет данных")
		}
		author, comment := orDefault(str("author"), "Админ"), ""
		if action == "addOwnTask" {
			author = "Резидент"
			if strings.TrimSpace(p["recurring"]) == "1" {
				comment = "Постоянная"
			}
		}
		sheet(SheetResTasks).append(dmy(at), name, task, "Открыта", comment, dt(at), author)

	case "setResTaskStatus":
		r, ok := row()
		st := str("status")
		if !ok || r < 2 {
			return nil, errors.New("Bad row")
		}
		if st != "Открыта" && st != "Выполнена" {
			return nil, errors.New("Bad status")
		}
		g := sheet(SheetResTasks)
		if strings.TrimSpace(g.get(r, 2)) == "" {
			return nil, errors.New("Задача не найдена")
		}
		g.set(r, 4, st)
		g.set(r, 6, dt(at))

	case "editOwnTask", "deleteOwnTask":
		r, ok := row()
		name, task := str("name"), str("task")
		if !ok || r < 2 || (action == "editOwnTask" && task == "") {
			if action == "editOwnTask" {
				return nil, errors.New("Нет данных")
			}
			return nil, errors.New("Bad row")
		}
		g := sheet(SheetResTasks)
		if strings.TrimSpace(g.get(r, 2)) != name {
			return nil, errors.New("Чужая задача")
		}
		if strings.TrimSpace(g.get(r, 7)) != "Резидент" {
			if action == "editOwnTask" {
				return nil, errors.New("Задачу от трекера менять нельзя")
			}
			return nil, errors.New("Задачу от трекера удалять нельзя")
		}
		if action == "deleteOwnTask" {
			g.del(r)
			break
		}
		g.set(r, 3, task)
		g.set(r, 6, dt(at))

	case "saveProblem":
		name, text := str("name"), str("problem")
		if name == "" || text == "" {
			return nil, errors.New("Нет данных")
		}
		digits := strings.Map(func(r rune) rune {
			if r >= '0' && r <= '9' {
				return r
			}
			return -1
		}, orDefault(p["cost"], "0"))
		cost, _ := strconv.ParseInt(digits, 10, 64)
		sheet(SheetProblems).append(dmy(at), name, text, grouped(cost), "Открыта")

	case "setProblemStatus":
		r, ok := row()
		st := str("status")
		if !ok || r < 2 {
			return nil, errors.New("Bad row")
		}
		if st != "Открыта" && st != "Решена" {
			return nil, errors.New("Bad status")
		}
		sheet(SheetProblems).set(r, 5, st)

	case "deleteProblem":
		r, ok := row()
		if !ok || r < 2 {
			return nil, errors.New("Bad row")
		}
		g := sheet(SheetProblems)
		if strings.TrimSpace(g.get(r, 2)) != str("name") {
			return nil, errors.New("Чужая запись")
		}
		g.del(r)

	case "saveSmm":
		d := at
		if dp := strings.Split(p["date"], "."); p["date"] != "" && len(dp) == 3 {
			dd, _ := jsParseInt(dp[0])
			mm, _ := jsParseInt(dp[1])
			yy, _ := jsParseInt(dp[2])
			d = time.Date(yy, time.Month(mm), dd, 0, 0, 0, 0, Almaty)
		}
		vals := []string{dmy(d), orDefault(p["platform"], "Instagram"), p["rubric"], p["title"], p["text"], orDefault(p["status"], "Идея"), p["link"]}
		g := sheet(SheetSmm)
		if r, ok := row(); ok && r >= 2 {
			for i, v := range vals {
				g.set(r, i+1, v)
			}
		} else {
			g.append(vals...)
		}

	case "deleteSmm":
		r, ok := row()
		if !ok || r < 2 {
			return nil, errors.New("Bad row")
		}
		sheet(SheetSmm).del(r)

	case "addLead":
		name := str("name")
		phone := strings.Map(func(r rune) rune {
			if (r >= '0' && r <= '9') || r == '+' {
				return r
			}
			return -1
		}, p["phone"])
		tg := str("telegram")
		if phone != "" && !strings.HasPrefix(phone, "+") {
			phone = "+" + phone
		}
		if name == "" && phone == "" && tg == "" {
			return nil, errors.New("Пустой лид")
		}
		g := read(SheetLeads)
		monthAgo := at.Add(-30 * 24 * time.Hour)
		for i := 2; i <= g.last(); i++ {
			r := g.rows[i-1]
			if d, ok := cellDate(r, 0); ok && d.Before(monthAgo) {
				continue
			}
			ep := strings.Map(func(r rune) rune {
				if (r >= '0' && r <= '9') || r == '+' {
					return r
				}
				return -1
			}, jsStr(r, 2))
			et := strings.TrimSpace(jsStr(r, 3))
			if (phone != "" && ep == phone) || (tg != "" && et == tg) {
				return map[string][][]string{}, nil // дубль: таблица его не добавит
			}
		}
		sheet(SheetLeads).append(dt(at), name, phone, tg, orDefault(p["source"], "Не указан"), p["campaign"], p["niche"],
			p["revenue"], p["request"], "Новый", "", p["comment"], "", "")

	case "setLeadStatus":
		r, ok := row()
		if !ok || r < 2 {
			return nil, errors.New("Bad row")
		}
		g := sheet(SheetLeads)
		g.set(r, 10, str("status"))
		if p["comment"] != "" {
			g.set(r, 12, p["comment"])
		}
		if p["owner"] != "" {
			g.set(r, 11, p["owner"])
		}

	case "addContentPost":
		text := str("text")
		if text == "" {
			return nil, errors.New("Пустой текст")
		}
		if gs[SheetContent] == nil {
			return nil, errors.New("Нет листа")
		}
		g := sheet(SheetContent)
		lr := g.last()
		when := at.In(Almaty).Add(48 * time.Hour)
		when = time.Date(when.Year(), when.Month(), when.Day(), 9, 0, 0, 0, Almaty)
		if p["date"] != "" {
			if t, ok := parseAppDateTime(p["date"], "09:00"); ok {
				when = t
			}
		}
		g.append(strconv.Itoa(lr-1), orDefault(str("cat"), "Прочее"), text, dt(when), "Ожидает")

	case "updateContentPost":
		r, ok := row()
		if !ok || r < 3 {
			return nil, errors.New("Bad row")
		}
		if gs[SheetContent] == nil {
			return nil, errors.New("Нет листа")
		}
		g := sheet(SheetContent)
		if t := strings.TrimSpace(p["text"]); t != "" {
			g.set(r, 3, t)
		}
		if p["date"] != "" {
			if t, ok := parseAppDateTime(p["date"], "09:00"); ok {
				g.set(r, 4, dt(t))
			}
		}
		if p["status"] != "" {
			g.set(r, 5, p["status"])
		}

	case "deleteContentPost":
		r, ok := row()
		if !ok || r < 3 {
			return nil, errors.New("Bad row")
		}
		if gs[SheetContent] == nil {
			return nil, errors.New("Нет листа")
		}
		sheet(SheetContent).del(r)

	case "updateLeadmagnet":
		key, title := str("key"), str("title")
		if key == "" {
			return nil, errors.New("Нет key")
		}
		if title == "" {
			return nil, errors.New("Нет названия")
		}
		g := sheet(SheetLeadmagnets)
		if g.last() < 2 {
			return nil, errors.New("Лист пуст")
		}
		found := -1
		for i := 2; i <= g.last(); i++ {
			if g.get(i, 1) == key {
				found = i
				break
			}
		}
		if found < 0 {
			return nil, errors.New("Чек-лист не найден")
		}
		g.set(found, 2, title)
		g.set(found, 4, str("description"))
		g.set(found, 5, strings.ToLower(str("category")))
		if g.get(1, 4) == "" {
			g.set(1, 4, "Описание")
		}
		if g.get(1, 5) == "" {
			g.set(1, 5, "Категория")
		}

	case "requestLeadmagnet":
		key, cid := str("key"), str("chatId")
		if key == "" {
			return nil, errors.New("Нет ключа")
		}
		if cid == "" {
			return nil, errors.New("Не определён пользователь")
		}
		lm := read(SheetLeadmagnets)
		title, file := "", ""
		found := false
		for i := 2; i <= lm.last(); i++ {
			if lm.get(i, 1) == key {
				title, file, found = lm.get(i, 2), lm.get(i, 3), true
				break
			}
		}
		if !found {
			return nil, errors.New("Чек-лист не найден")
		}
		if file == "" {
			return nil, errors.New("Чек-лист ещё не загружен")
		}
		a := at.In(Almaty)
		sheet(SheetLMHistory).append(cid, key, fmt.Sprintf("%d/%d/%d %d:%02d:%02d", a.Month(), a.Day(), a.Year(), a.Hour(), a.Minute(), a.Second()), title)

	case "saveProfile":
		cid := strings.TrimSpace(p["chatId"])
		if cid == "" {
			return nil, errors.New("No chatId")
		}
		g := sheet(SheetProfiles)
		lr := g.last()
		r := lr + 1
		for i := 2; i <= lr; i++ {
			if g.get(i, 1) == cid {
				r = i
				break
			}
		}
		for i, v := range []string{cid, p["niche"], p["bio"], p["help"], p["instagram"], p["phone"], p["avatar"]} {
			g.set(r, i+1, v)
		}
	}
	return result(gs, changed), nil
}

func result(gs map[string]*grid, changed map[string]bool) map[string][][]string {
	out := map[string][][]string{}
	for n := range changed {
		g := gs[n]
		g.trim()
		out[n] = g.rows
	}
	return out
}

// SectionSeen: the sheet (as the last import brought it) already has what an
// adding write adds. nil: cannot tell for this action.
func SectionSeen(action string, p map[string]string, at time.Time, grids map[string][][]string) *bool {
	str := func(k string) string { return strings.TrimSpace(p[k]) }
	since := at.Add(-2 * time.Minute)
	recent := func(r []string) bool {
		d, ok := cellDate(r, 0)
		return !ok || !d.Before(since.Truncate(time.Minute)) || dmy(d) == dmy(at) && d.Hour() == 0 && d.Minute() == 0
	}
	find := func(sheet string, match func(r []string) bool) *bool {
		rows := grids[sheet]
		yes := false
		dataRows(rows, 2, func(_ int, r []string) {
			if !yes && match(r) {
				yes = true
			}
		})
		return &yes
	}
	switch action {
	case "saveWheel":
		tp := "ДНК"
		if str("type") == "life" {
			tp = "Личное"
		}
		vals := strings.Split(str("values"), ",")
		return find(SheetWheel, func(r []string) bool {
			if strings.TrimSpace(jsStr(r, 1)) != str("name") || strings.TrimSpace(jsStr(r, 2)) != tp || !recent(r) {
				return false
			}
			off := 3
			if tp == "Личное" {
				off = 4
			}
			for i, v := range vals {
				n, _ := jsParseInt(v)
				if jsNum(r, off+i) != float64(clamp(n)) {
					return false
				}
			}
			return true
		})
	case "addResTask", "addOwnTask":
		return find(SheetResTasks, func(r []string) bool {
			return strings.TrimSpace(jsStr(r, 1)) == str("name") && strings.TrimSpace(jsStr(r, 2)) == str("task") && recent(r)
		})
	case "saveProblem":
		return find(SheetProblems, func(r []string) bool {
			return strings.TrimSpace(jsStr(r, 1)) == str("name") && strings.TrimSpace(jsStr(r, 2)) == str("problem") && recent(r)
		})
	case "addLead":
		return find(SheetLeads, func(r []string) bool {
			return strings.TrimSpace(jsStr(r, 1)) == str("name") && strings.TrimSpace(jsStr(r, 3)) == str("telegram") && recent(r)
		})
	case "addContentPost":
		yes := false
		dataRows(grids[SheetContent], 3, func(_ int, r []string) {
			if strings.TrimSpace(jsStr(r, 2)) == str("text") {
				yes = true
			}
		})
		return &yes
	case "saveSmm":
		if n, ok := jsParseInt(p["row"]); ok && n >= 2 {
			return nil
		}
		return find(SheetSmm, func(r []string) bool {
			return jsStr(r, 3) == p["title"] && jsStr(r, 4) == p["text"]
		})
	case "requestLeadmagnet":
		return find(SheetLMHistory, func(r []string) bool {
			return jsStr(r, 0) == str("chatId") && jsStr(r, 1) == str("key") && recent(r)
		})
	}
	return nil
}

func clamp(n int) int {
	if n < 1 {
		return 1
	}
	if n > 10 {
		return 10
	}
	return n
}
