package club

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

// The app's section sheets as the import brings them (display values, as in
// the real sheet).
func sectionSheets() Sheets {
	return Sheets{
		SheetScriptProps: {{"USEFUL_CL_IDX", "1"}, {"WHEEL_AXES_Альтаир", `["Здоровье","Семья","Друзья","Деньги","Рост","Отдых","Смысл"]`}},
		SheetWheel: {
			{"Дата", "Резидент", "Тип", "В1", "В2", "В3", "В4", "В5", "В6", "В7", "В8", "Бизнес"},
			{"04.07.2026 12:38", "Рустам", "ДНК", "8", "10", "8", "8", "3", "2", "9", "3"},
			{"05.07.2026 10:00", "Альтаир", "ДНК", "7", "5", "8", "6", "4", "7", "5", "6", "Кофейня"},
			{"06.07.2026 10:00", "Альтаир", "ДНК", "5", "5", "5", "5", "5", "5", "5", "5", "Доставка"},
			{"07.07.2026 11:00", "Альтаир", "Личное", "5.6", "6", "7", "8", "5", "6", "7", "9", ""},
			{"08.07.2026 09:15", "Альтаир", "ДНК", "8", "6", "8", "6", "4", "7", "5", "6", "Кофейня"},
		},
		SheetLeads: {
			{"Дата", "Имя", "Телефон", "Telegram", "Источник", "Кампания", "Ниша", "Оборот", "Запрос", "Статус", "Ответственный", "Комментарий", "След. касание", "Сумма сделки"},
			{"01.10.2026 10:00", "Старый", "+77010000001", "", "Сайт", "", "", "", "", "", "", "", "", ""},
			{"02.10.2026 09:30", "Новый", "+77010000002", "@new", "Instagram", "осень", "кофе", "50 млн", "разбор", "Взят в работу", "Рустам", "позвонить", "", ""},
			{"", "", "", "", "", "", "", "", "", "", "", "", "", ""},
		},
		SheetProblems: {
			{"Дата", "Резидент", "Проблема", "Стоимость в месяц", "Статус"},
			{"01.10.2026", "Альтаир", "Кассовый разрыв", "1,500,000", "Открыта"},
			{"02.10.2026", "Асет", "Текучка", "300,000", "Решена"},
			{"02.10.2026", "Асет", "Склад", "200,000", ""},
		},
		SheetResTasks: {
			{"Дата", "Резидент", "Задача", "Статус", "Комментарий", "Обновлено", "Кто создал"},
			{"18.08.2026", "Альтаир", "Счет 14к", "Выполнена", "", "18.08.2026 16:31", "Rustam Qabden | Бизнес-хирург"},
			{"19.08.2026", "Альтаир", "Отчёт", "На проверке", "Постоянная", "19.08.2026 10:00", "Резидент"},
			{"20.08.2026", "Асет", "Найм", "Возвращена", "", "", ""},
		},
		SheetSmm: {
			{"Дата", "Площадка", "Рубрика", "Заголовок", "Текст", "Статус", "Ссылка"},
			{"11.10.2026", "", "Кейсы", "Второй", "", "", ""},
			{"10.10.2026", "Threads", "Кейсы", "Первый", "текст", "Готово", "https://x.y"},
			{"12.10.2026", "", "", "", "", "", ""},
		},
		SheetContent: {
			{"📢 КОНТЕНТ-ПЛАН — автопостинг в ПОЛЕЗНОЕ каждые 2 дня"},
			{"№", "Категория", "Текст поста", "Дата отправки", "Статус"},
			{"1", "Маркетинг", "Пост 1", "06.05.2026 9:30:00", "Отправлен"},
			{"2", "Команда", "Пост 2", "20.10.2026 10:00", ""},
			{"3", "Цели", "Пост 3", "12.10.2026 10:00", "Ожидает"},
			{"", "", "", "", ""},
		},
		SheetLeadmagnets: {
			{"Ключ", "Название", "File ID", "Дата загрузки", "Кто загрузил"},
			{"sales", "Реанимация продаж", "BQAC1", "Sun May 31 2026 03:21:58 GMT+0500 (Казахстан)", "hands"},
			{"unit", "Анатомия бизнеса", "BQAC2", "31.05.2026 03:21", "Маркетинг"},
			{"hire", "Найм", "", "", ""},
			{"delegate", "Хирургия рутины", "BQAC3", "", ""},
		},
		SheetLMHistory: {
			{"Chat ID", "Ключ", "Дата", "Название"},
			{"453800951", "sales", "6/3/2026 14:38:16", "Реанимация продаж"},
			{"490685605", "unit", "6/5/2026 17:40:45", "Анатомия бизнеса"},
		},
	}
}

func js(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestSectionsAsTheScriptBuildsThem(t *testing.T) {
	raw := sectionSheets()
	now := time.Date(2026, 10, 2, 9, 0, 0, 0, Almaty) // Friday

	all := WheelAll(raw)
	a := all["Альтаир"]
	if a == nil || js(t, a.LifeAxes) != `["Бизнес","Здоровье","Семья","Друзья","Деньги","Рост","Отдых","Смысл"]` ||
		len(a.Businesses) != 2 || a.Businesses[0].Name != "Кофейня" || len(a.Businesses[0].Rows) != 2 ||
		a.Businesses[0].Rows[0].Date != "08.07.2026 09:15" || js(t, a.DNA) != js(t, a.Businesses[0].Rows) ||
		js(t, a.Life[0].Values) != `[5.6,6,7,8,5,6,7,9]` {
		t.Fatalf("wheelAll Альтаир: %s", js(t, a))
	}
	if r := all["Рустам"]; js(t, r.LifeAxes) != js(t, WheelLifeAxes) || js(t, r.Life) != `[]` || r.Businesses[0].Name != "Основной" {
		t.Fatalf("wheelAll Рустам: %s", js(t, r))
	}

	sum := WheelSummary(raw)
	if js(t, sum) != `{"summary":[{"name":"Альтаир","dnaAvg":5.7,"dnaDate":"08.07.2026","dnaCount":2,"lifeAvg":6.7,"lifeDate":"07.07.2026","total":4},`+
		`{"name":"Рустам","dnaAvg":6.4,"dnaDate":"04.07.2026","dnaCount":1,"lifeAvg":null,"lifeDate":"","total":1}]}` {
		t.Fatalf("wheelSummary %s", js(t, sum))
	}

	leads := Leads(raw)
	if len(leads.Leads) != 2 || leads.Leads[0].Name != "Новый" || leads.Leads[0].Row != 3 || leads.Leads[1].Status != "Новый" ||
		leads.Leads[0].Date != "02.10.2026" || leads.Leads[0].TS != time.Date(2026, 10, 2, 9, 30, 0, 0, Almaty).UnixMilli() {
		t.Fatalf("leads %s", js(t, leads))
	}

	pr := Problems(raw)
	if pr.Total != 1700000 || len(pr.Problems) != 3 || pr.Problems[0].Row != 4 || pr.Problems[0].Status != "Открыта" || pr.Problems[2].Cost != 1500000 {
		t.Fatalf("problems %s", js(t, pr))
	}

	tasks := ResTasks(raw)
	if len(tasks.Tasks) != 3 || tasks.Tasks[0].Status != "Открыта" || tasks.Tasks[0].Author != "Админ" || tasks.Tasks[0].Updated != "" ||
		tasks.Tasks[1].Status != "Выполнена" || !tasks.Tasks[1].Recurring || !tasks.Tasks[1].IsOwn || tasks.Tasks[1].Updated != "19.08" {
		t.Fatalf("resTasks %s", js(t, tasks))
	}

	smm := Smm(raw)
	if len(smm.Items) != 2 || smm.Items[0].Title != "Первый" || smm.Items[1].Platform != "Instagram" || smm.Items[1].Status != "Идея" {
		t.Fatalf("smm %s", js(t, smm))
	}

	cp := ContentPlan(raw)
	// waiting first by date; the hand-typed date is text (ts 0), as the script sees it
	if len(cp.Items) != 3 || cp.Items[0].Text != "Пост 3" || cp.Items[1].Status != "Ожидает" || cp.Items[2].Date != "06.05.2026 9:30:00" ||
		cp.Items[2].TS != 0 || js(t, cp.Items[0].Num) != "3" {
		t.Fatalf("contentPlan %s", js(t, cp))
	}

	lm := Leadmagnets(raw, "490685605")
	if len(lm.Items) != 4 || js(t, lm.Taken) != `{"unit":true}` || lm.Items[1].Category != "маркетинг" {
		t.Fatalf("leadmagnets %s", js(t, lm))
	}
	if js(t, Leadmagnets(raw, "999").Taken) != `{}` {
		t.Fatal("a lead took nothing")
	}

	// three ready checklists, from USEFUL_CL_IDX=1, Mondays at 11:00
	cs := ChecklistSchedule(raw, lm, now)
	if js(t, cs) != `[{"name":"unit","key":"unit","date":"05.10.2026","next":true},{"name":"delegate","key":"delegate","date":"12.10.2026","next":false},`+
		`{"name":"sales","key":"sales","date":"19.10.2026","next":false}]` {
		t.Fatalf("checklistSchedule %s", js(t, cs))
	}
	// on a Monday after 11:00 the next is a week later
	if cs := ChecklistSchedule(raw, lm, time.Date(2026, 10, 5, 12, 0, 0, 0, Almaty)); cs[0].Date != "12.10.2026" {
		t.Fatalf("monday %s", js(t, cs))
	}

	// whole sections: only once the import brings them
	if _, built := AppSections(&Snapshot{Raw: Sheets{SheetWheel: raw[SheetWheel]}}, "1", now); built != nil {
		t.Fatal("an import without _props must leave the sections to the script")
	}
	out, built := AppSections(&Snapshot{Raw: raw}, "490685605", now)
	if len(built) != 9 || out["leadmagnets"].(LeadmagnetsView).Taken["unit"] != true {
		t.Fatalf("built %v", built)
	}
	// a v33 import of a sheet without the sections: the script's empty answers
	out, _ = AppSections(&Snapshot{Raw: Sheets{SheetScriptProps: {}}}, "", now)
	if js(t, out["wheelAll"]) != `{}` || js(t, out["leads"]) != `{"leads":[]}` || js(t, out["smm"]) != `{"items":[]}` ||
		js(t, out["wheelSummary"]) != `{"summary":[]}` || js(t, out["checklistSchedule"]) != `[]` || js(t, out["contentPlan"]) != `{"items":[]}` {
		t.Fatalf("empty %s", js(t, out))
	}
}

func apply(t *testing.T, raw Sheets, action string, p map[string]string, at time.Time) (Sheets, error) {
	t.Helper()
	grids := map[string][][]string{}
	for _, n := range SectionWriteActions[action] {
		if g, ok := raw[n]; ok {
			grids[n] = g
		}
	}
	ch, err := ApplySection(action, p, at, grids)
	if err != nil {
		return raw, err
	}
	out := Sheets{}
	for k, v := range raw {
		out[k] = v
	}
	for k, v := range ch {
		out[k] = v
	}
	return out, nil
}

func TestSectionWritesAsTheScript(t *testing.T) {
	raw := sectionSheets()
	at := time.Date(2026, 10, 2, 14, 5, 30, 0, Almaty)
	var err error
	must := func(action string, kv ...string) {
		t.Helper()
		p := map[string]string{}
		for i := 0; i+1 < len(kv); i += 2 {
			p[kv[i]] = kv[i+1]
		}
		if raw, err = apply(t, raw, action, p, at); err != nil {
			t.Fatalf("%s: %v", action, err)
		}
	}
	refuse := func(want, action string, kv ...string) {
		t.Helper()
		p := map[string]string{}
		for i := 0; i+1 < len(kv); i += 2 {
			p[kv[i]] = kv[i+1]
		}
		if _, err := apply(t, raw, action, p, at); err == nil || err.Error() != want {
			t.Fatalf("%s: %v, want %q", action, err, want)
		}
	}

	// Колесо: life = среднее последних замеров всех направлений ДНК
	must("saveWheel", "name", "Альтаир", "type", "life", "values", "6,7,8,5,6,7,99")
	last := raw[SheetWheel][len(raw[SheetWheel])-1]
	// Кофейня (последний 6.25) и Доставка (5): (6.25+5)/2 = 5.625 → 5.6
	if strings.Join(last, "|") != "02.10.2026 14:05|Альтаир|Личное|5.6|6|7|8|5|6|7|10|" {
		t.Fatalf("life row %q", last)
	}
	must("saveWheel", "name", "Асет", "type", "dna", "values", "1,2,3,4,5,6,7,0", "bizName", "  ")
	if r := raw[SheetWheel][len(raw[SheetWheel])-1]; r[11] != "Основной" || r[10] != "1" {
		t.Fatalf("dna row %q", r)
	}
	refuse("Нужно 8 значений", "saveWheel", "name", "Асет", "type", "dna", "values", "1,2")
	refuse("Неверный тип", "saveWheel", "name", "Асет", "type", "x")
	must("saveWheelAxes", "name", "Асет", "axes", "A|B|C|D|E|F|G")
	must("saveWheelAxes", "name", "Альтаир", "axes", "a|b|c|d|e|f|g")
	if js(t, WheelAll(raw)["Асет"].LifeAxes) != `["Бизнес","A","B","C","D","E","F","G"]` || len(raw[SheetScriptProps]) != 3 ||
		WheelAxes(ScriptProps(raw), "Альтаир")[0] != "a" {
		t.Fatalf("axes %v", raw[SheetScriptProps])
	}

	// Задачи
	must("addResTask", "name", "Асет", "task", "Отчёт", "author", "Рустам")
	must("addOwnTask", "name", "Асет", "task", "Своя", "recurring", "1")
	n := len(raw[SheetResTasks])
	if strings.Join(raw[SheetResTasks][n-1], "|") != "02.10.2026|Асет|Своя|Открыта|Постоянная|02.10.2026 14:05|Резидент" {
		t.Fatalf("own task %q", raw[SheetResTasks][n-1])
	}
	refuse("Задачу от трекера менять нельзя", "editOwnTask", "row", "5", "name", "Асет", "task", "x")
	refuse("Чужая задача", "editOwnTask", "row", "6", "name", "Альтаир", "task", "x")
	must("editOwnTask", "row", "6", "name", "Асет", "task", "Своя 2")
	must("setResTaskStatus", "row", "5", "status", "Выполнена")
	refuse("Bad status", "setResTaskStatus", "row", "5", "status", "Принята")
	must("deleteOwnTask", "row", "6", "name", "Асет")
	if tk := ResTasks(raw).Tasks; len(tk) != 4 || tk[0].Task != "Отчёт" || tk[0].Status != "Выполнена" {
		t.Fatalf("tasks %s", js(t, tk))
	}

	// Проблемы
	must("saveProblem", "name", "Асет", "problem", "Аренда", "cost", "1 200 000 тг")
	if p := Problems(raw).Problems[0]; p.Cost != 1200000 || p.Row != 5 || raw[SheetProblems][4][3] != "1,200,000" {
		t.Fatalf("problem %s", js(t, p))
	}
	must("setProblemStatus", "row", "5", "status", "Решена")
	refuse("Чужая запись", "deleteProblem", "row", "5", "name", "Альтаир")
	must("deleteProblem", "row", "5", "name", "Асет")

	// СММ
	must("saveSmm", "date", "15.10.2026", "title", "Новый", "text", "т")
	if s := raw[SheetSmm][len(raw[SheetSmm])-1]; strings.Join(s, "|") != "15.10.2026|Instagram||Новый|т|Идея|" {
		t.Fatalf("smm %q", s)
	}
	must("saveSmm", "row", "2", "title", "Второй (правка)", "status", "Готово")
	if raw[SheetSmm][1][3] != "Второй (правка)" || raw[SheetSmm][1][0] != "02.10.2026" {
		t.Fatalf("smm row 2 %q", raw[SheetSmm][1])
	}
	must("deleteSmm", "row", "3")

	// Лиды: дубль за 30 дней по телефону ничего не добавляет
	must("addLead", "name", "Тест", "phone", "8 701 000 0002")
	if len(Leads(raw).Leads) != 3 {
		t.Fatalf("lead %s", js(t, Leads(raw)))
	}
	before := len(raw[SheetLeads])
	must("addLead", "name", "Тест 2", "phone", "+7 (701) 000-00-02")
	if len(raw[SheetLeads]) != before {
		t.Fatal("double lead added")
	}
	must("setLeadStatus", "row", "3", "status", "Отказ", "owner", "Береке")
	if l := raw[SheetLeads][2]; l[9] != "Отказ" || l[10] != "Береке" || l[11] != "позвонить" {
		t.Fatalf("lead status %q", l)
	}

	// Контент: № = строк до добавления − 1, дата по умолчанию через 2 дня в 9:00
	must("addContentPost", "text", "Пост 4", "cat", "Цели")
	if c := raw[SheetContent][len(raw[SheetContent])-1]; strings.Join(c, "|") != "4|Цели|Пост 4|04.10.2026 09:00|Ожидает" {
		t.Fatalf("content %q", c)
	}
	must("updateContentPost", "row", "3", "date", "21.10.2026 12:30", "status", "Ожидает")
	if c := raw[SheetContent][2]; c[3] != "21.10.2026 12:30" || c[4] != "Ожидает" {
		t.Fatalf("content upd %q", c)
	}
	must("deleteContentPost", "row", "4")
	refuse("Bad row", "deleteContentPost", "row", "2")

	// Чек-листы
	must("updateLeadmagnet", "key", "hire", "title", "Найм 2.0", "description", "Как нанять", "category", "Команда")
	if l := raw[SheetLeadmagnets][3]; l[1] != "Найм 2.0" || l[3] != "Как нанять" || l[4] != "команда" {
		t.Fatalf("lm %q", l)
	}
	refuse("Чек-лист ещё не загружен", "requestLeadmagnet", "key", "hire", "chatId", "999")
	must("requestLeadmagnet", "key", "delegate", "chatId", "999")
	if js(t, Leadmagnets(raw, "999").Taken) != `{"delegate":true}` {
		t.Fatalf("taken %v", raw[SheetLMHistory])
	}

	// Профиль: строка по Chat ID, иначе новая
	must("saveProfile", "chatId", "490685605", "niche", "Кофейни")
	must("saveProfile", "chatId", "490685605", "niche", "Кофейни и доставка")
	if p := raw[SheetProfiles]; len(p) != 2 || p[1][1] != "Кофейни и доставка" || p[0][0] != "Chat ID" {
		t.Fatalf("profiles %q", p)
	}
}

func TestSectionSeen(t *testing.T) {
	raw := sectionSheets()
	at := time.Date(2026, 10, 2, 14, 5, 0, 0, Almaty)
	p := map[string]string{"name": "Асет", "task": "Отчёт", "author": "Рустам"}
	if s := SectionSeen("addResTask", p, at, raw); s == nil || *s {
		t.Fatal("not there yet")
	}
	raw2, err := apply(t, raw, "addResTask", p, at.Add(3*time.Minute)) // the script wrote it a bit later
	if err != nil {
		t.Fatal(err)
	}
	if s := SectionSeen("addResTask", p, at, raw2); s == nil || !*s {
		t.Fatal("the sheet has it")
	}
	if SectionSeen("setLeadStatus", map[string]string{"row": "2"}, at, raw) != nil {
		t.Fatal("a status cannot be told")
	}
}

func TestDiffBundleSections(t *testing.T) {
	var sheet, server map[string]any
	_ = json.Unmarshal([]byte(`{"leads":{"leads":[{"row":2,"name":"А","ts":1790000000000},{"row":3,"name":"Б","ts":0}]},
		"leadmagnets":{"ok":true,"items":[{"key":"sales","description":"Sun May 31 2026 03:21:00 GMT+0500 (Uzbekistan Standard Time)"}],"taken":{"sales":true}},
		"wheelSummary":{"summary":[{"name":"Б","dnaAvg":5},{"name":"А","dnaAvg":null}]},
		"checklistSchedule":[{"key":"a","date":"05.10.2026"}]}`), &sheet)
	_ = json.Unmarshal([]byte(`{"leads":{"leads":[{"row":3,"name":"Б","ts":0},{"row":2,"name":"А","ts":1790000040000}]},
		"leadmagnets":{"ok":true,"items":[{"key":"sales","description":"31.05.2026 03:21"}],"taken":{"sales":true}},
		"wheelSummary":{"summary":[{"name":"А"},{"name":"Б","dnaAvg":5}]},
		"checklistSchedule":[{"key":"a","date":"05.10.2026"}]}`), &server)
	secs := []string{"leads", "leadmagnets", "wheelSummary", "checklistSchedule"}
	if d := DiffBundle(sheet, server, secs, time.Now()); len(d) != 0 {
		t.Fatalf("%+v", d)
	}
	server["leadmagnets"].(map[string]any)["taken"] = map[string]any{}
	server["leads"].(map[string]any)["leads"].([]any)[1].(map[string]any)["name"] = "В"
	d := DiffBundle(sheet, server, secs, time.Now())
	if len(d) != 2 || d[0].Field != "leads.leads[2].name" || d[1].Field != "leadmagnets.taken.sales" {
		t.Fatalf("%+v", d)
	}
}
