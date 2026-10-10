package voicecmd

import (
	"fmt"
	"strings"
	"testing"
	"time"
)

var testNow = time.Date(2026, 10, 10, 15, 0, 0, 0, time.FixedZone("Almaty", 5*3600)) // суббота

func testCtx() Context {
	return Context{
		Nodes: []Node{
			{ID: 1.0, Type: "root", Title: "Даулет"},
			{ID: 2.0, Type: "point", Role: "pointA", Title: "Оборот 5 млн"},
			{ID: 7.0, Type: "diag", Title: "Кассовые разрывы", Parent: 1.0},
			{ID: 8.0, Type: "tool", Title: "Платёжный календарь", Parent: 7.0},
			{ID: 12.0, Type: "task", Title: "Позвонить поставщику", Parent: 7.0},
			{ID: 13.0, Type: "task", Title: "Собрать отчёт по продажам"},
			{ID: 14.0, Type: "note", Title: "Клиенты жалуются на долгий ответ"},
			{ID: 15.0, Type: "diag", Title: "Нет системы продаж"},
		},
		Selected:  7.0,
		Residents: []string{"Даулет Сайты", "Асет", "Мади Актобе", "Гульназ бухгалтерия", "Айгерим", "Олжас"},
		DiagLib:   []string{"Кассовые разрывы", "Нет системы продаж", "Собственник делает всё сам", "Нет управленческого учёта", "Высокая текучка персонала", "Низкая конверсия в продажу"},
		ToolLib:   []string{"Платёжный календарь", "Скрипты продаж", "CRM-система", "Регламент отдела продаж", "Еженедельная планёрка", "Отчёт о движении денег"},
		Team:      []string{"Рустам", "Береке"},
	}
}

type ic struct {
	say  string            // the phrase (as Whisper may write it)
	op   string            // first action's op ("" = rules must not understand it)
	want map[string]string // fields of the first action (fmt %v)
}

var intentCases = []ic{
	// tasks with deadline and owner
	{"Джарвис, добавь задачу позвонить поставщику до пятницы", "add_node", map[string]string{"type": "task", "title": "Позвонить поставщику", "date": "2026-10-16"}},
	{"Джарвис добавь задачу собрать отчёт до 15.10 ответственный Береке", "add_node", map[string]string{"title": "Собрать отчет", "date": "2026-10-15", "who": "Береке"}},
	{"Ассистент, поставь задачу нанять бухгалтера к пятому ноября", "add_node", map[string]string{"title": "Нанять бухгалтера", "date": "2026-11-05"}},
	{"задача обзвонить старых клиентов завтра", "add_node", map[string]string{"title": "Обзвонить старых клиентов", "date": "2026-10-11"}},
	{"Джарвис, новая задача сделать прайс через неделю, поручи Рустаму", "add_node", map[string]string{"title": "Сделать прайс", "date": "2026-10-17", "who": "Рустам"}},
	{"создай задачу описать воронку на понедельник", "add_node", map[string]string{"title": "Описать воронку", "date": "2026-10-12"}},
	{"напомни проверить оплату от Асета послезавтра", "add_node", map[string]string{"type": "task", "date": "2026-10-12"}},
	{"Джарвис, пожалуйста, задача заполнить таблицу к нему", "add_node", map[string]string{"title": "Заполнить таблицу", "parent": "selected"}},
	{"добавь задачу провести планёрку до конца недели ответственная Айгерим", "add_node", map[string]string{"date": "2026-10-16", "who": "Айгерим"}},
	{"задачу посчитать маржу к кассовым разрывам", "add_node", map[string]string{"title": "Посчитать маржу", "parent": "7"}},
	// diagnoses
	{"Джарвис, поставь диагноз кассовые разрывы", "pick_diag", map[string]string{"query": "Кассовые разрывы"}},
	{"Ассистент поставь диагноз касовые разрыв", "pick_diag", map[string]string{"query": "Кассовые разрывы"}},
	{"диагноз собственник делает всё сам", "pick_diag", map[string]string{"query": "Собственник делает всё сам"}},
	{"добавь диагноз нет управленческого учета", "pick_diag", map[string]string{"query": "Нет управленческого учёта"}},
	{"поставь диагноз хаос на складе", "pick_diag", map[string]string{"query": "Хаос на складе"}},
	// tools
	{"назначь инструмент платежный календарь", "pick_tool", map[string]string{"query": "Платёжный календарь"}},
	{"Джарвис, инструмент скрипты продаж к нет системы продаж", "pick_tool", map[string]string{"query": "Скрипты продаж", "parent": "15"}},
	{"назначь срм систему", "pick_tool", map[string]string{"query": "CRM-система"}},
	{"добавь инструмент еженедельная планерка", "pick_tool", map[string]string{"query": "Еженедельная планёрка"}},
	// notes, goals, questions, points
	{"заметка клиенты приходят по сарафану", "add_node", map[string]string{"type": "note", "title": "Клиенты приходят по сарафану"}},
	{"Джарвис, запиши что резидент боится нанимать", "add_node", map[string]string{"type": "note"}},
	{"цель выйти на десять миллионов", "add_node", map[string]string{"type": "goal"}},
	{"вопрос кто отвечает за маркетинг", "add_node", map[string]string{"type": "quest"}},
	{"точка а оборот пять миллионов прибыль шестьсот тысяч", "set_point", map[string]string{"which": "A"}},
	{"запиши в точку б выручка 10 млн", "set_point", map[string]string{"which": "B"}},
	// mark done
	{"Джарвис, отметь задачу позвонить поставщику выполненной", "task_done", map[string]string{"id": "12"}},
	{"задача собрать отчет готова", "task_done", map[string]string{"id": "13"}},
	{"выполнил задачу позвонить поставщику", "task_done", map[string]string{"id": "12"}},
	{"закрой задачу отчёт по продажам", "task_done", map[string]string{"id": "13"}},
	// attach / move
	{"привяжи платёжный календарь к нет системы продаж", "attach", map[string]string{"id": "8", "to": "15"}},
	{"перенеси заметку клиенты жалуются к кассовым разрывам", "attach", map[string]string{"id": "14", "to": "7"}},
	{"свяжи кассовые разрывы с нет системы продаж", "link", map[string]string{"a": "7", "b": "15"}},
	// open resident / panels
	{"Джарвис, открой доску Даулета", "open_resident", map[string]string{"name": "Даулет Сайты"}},
	{"открой разбор Асета", "open_resident", map[string]string{"name": "Асет"}},
	{"покажи доску Мади", "open_resident", map[string]string{"name": "Мади Актобе"}},
	{"перейди к Гульназ", "open_resident", map[string]string{"name": "Гульназ бухгалтерия"}},
	{"открой подготовку", "open_panel", map[string]string{"tab": "prep"}},
	// timer
	{"Джарвис, поставь таймер на пять минут", "timer", map[string]string{"sec": "300"}},
	{"засеки двадцать пять минут", "timer", map[string]string{"sec": "1500"}},
	{"таймер на полчаса", "timer", map[string]string{"sec": "1800"}},
	{"останови таймер", "timer", map[string]string{"sec": "0"}},
	// plan
	{"добавь в план запустить рекламу до конца месяца", "plan_add", map[string]string{"title": "Запустить рекламу", "date": "2026-10-31"}},
	{"запланируй встречу с банком на вторник, ответственный Береке", "plan_add", map[string]string{"date": "2026-10-13", "who": "Береке"}},
	{"внеси в план обновить сайт", "plan_add", map[string]string{"title": "Обновить сайт"}},
	// summary, undo, stop
	{"Джарвис, прочитай итоги", "read_summary", nil},
	{"озвучь сводку", "read_summary", nil},
	{"подведи итог", "read_summary", nil},
	{"отмени", "undo", nil},
	{"Джарвис, отмени последнее действие", "undo", nil},
	{"стоп", "stop_listen", nil},
	{"хватит", "stop_listen", nil},
	// several commands in one phrase
	{"поставь диагноз кассовые разрывы и назначь платежный календарь", "pick_diag", map[string]string{"query": "Кассовые разрывы"}},
	// the browser's rough transcript (its own model, before Whisper)
	{"джерви с добавь задачу позвонить поставщику да пятница", "add_node", map[string]string{"title": "Позвонить поставщику", "date": "2026-10-16"}},
	// not commands: the AI decides
	{"ну вот у него вообще все очень сложно с деньгами", "", nil},
	{"как дела у вас сегодня", "", nil},
}

func TestParseIntents(t *testing.T) {
	c := testCtx()
	ok := 0
	for _, tc := range intentCases {
		r, got := Parse(tc.say, c, testNow)
		if tc.op == "" {
			if got && len(r.Actions) > 0 {
				t.Errorf("%q: rules should not understand it, got %v", tc.say, r.Actions)
				continue
			}
			ok++
			continue
		}
		if !got || len(r.Actions) == 0 {
			t.Errorf("%q: not understood (text %q)", tc.say, r.Text)
			continue
		}
		a := r.Actions[0]
		if a["op"] != tc.op {
			t.Errorf("%q: op %v, want %s (%v)", tc.say, a["op"], tc.op, a)
			continue
		}
		bad := false
		for k, v := range tc.want {
			if g := fmt.Sprint(a[k]); g != v {
				t.Errorf("%q: %s = %q, want %q (%v)", tc.say, k, g, v, a)
				bad = true
			}
		}
		if !bad {
			ok++
		}
	}
	if len(intentCases) < 40 {
		t.Fatalf("need at least 40 phrasings, have %d", len(intentCases))
	}
	t.Logf("intents: %d/%d phrasings understood as expected", ok, len(intentCases))
}

func TestParseCompound(t *testing.T) {
	r, ok := Parse("поставь диагноз кассовые разрывы и назначь платежный календарь и задачу заполнить его до пятницы", testCtx(), testNow)
	if !ok || len(r.Actions) != 3 {
		t.Fatalf("want 3 actions, got %v (%v)", r.Actions, ok)
	}
	if r.Actions[1]["op"] != "pick_tool" || r.Actions[1]["parent"] != "last" || r.Actions[2]["op"] != "add_node" {
		t.Fatalf("chain: %v", r.Actions)
	}
	if !strings.Contains(r.Say, "Диагноз") {
		t.Fatalf("say: %q", r.Say)
	}
}

func TestStripWake(t *testing.T) {
	for in, want := range map[string]string{
		"Джарвис, сделай задачу":          "сделай задачу",
		"Слушай, Джарвис, запиши заметку": "запиши заметку",
		"Ассистент. Пожалуйста, отмени":   "отмени",
		"джервис добавь задачу":           "добавь задачу",
		"Jarvis, стоп": "стоп",
		"Окей, Джарвис, давай таймер": "таймер",
	} {
		if g := StripWake(in); g != want {
			t.Errorf("%q → %q, want %q", in, g, want)
		}
	}
}

func TestCorrectVocab(t *testing.T) {
	v := testCtx().vocab()
	for in, want := range map[string]string{
		"поставь диагноз касовые разрывы": "поставь диагноз кассовые разрывы",
		"открой доску даулед":             "открой доску даулет",
		"назначь платежный календар":      "назначь платежный календарь",
	} {
		if g := Correct(in, v); g != want {
			t.Errorf("%q → %q, want %q", in, g, want)
		}
	}
}
