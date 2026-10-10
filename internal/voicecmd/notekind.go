package voicecmd

import (
	"regexp"
	"strings"
)

// Note kinds (R79): a board note is not always a diagnosis or a tool.
const (
	KindMetric = "metric" // «Цифры»: revenue, conversion, dates, phones: data for Точка А/Б
	KindNote   = "note"   // a plain observation
	KindDiag   = "diag"   // a clear problem: a diagnosis to confirm (never created by itself)
	KindTool   = "tool"   // names an action or a tool
)

// NoteKind: what the sorter decided, and why.
type NoteKind struct {
	Kind  string  `json:"kind"`
	Conf  float64 `json:"conf"`
	Why   string  `json:"why"`
	Match string  `json:"match,omitempty"` // the library title it is near (diagnosis or tool)
	Label string  `json:"label,omitempty"` // metric: «Выручка»
	Value string  `json:"value,omitempty"` // metric: «4 млн»
	Point string  `json:"point,omitempty"` // metric: "A" (now) or "B" (goal)
}

var (
	reNumber  = regexp.MustCompile(`\d`)
	reNumWord = regexp.MustCompile(`(?:^|\s)(?:миллион[а-яa-z0-9]*|млн|млрд|тысяч[а-яa-z0-9]*|тыс|сотен|сотня|процент[а-яa-z0-9]*|полмиллиона|миллиард[а-яa-z0-9]*|ноль|один|два|три|четыре|пять|шесть|семь|восемь|девять|десять|двадцать|тридцать|сорок|пятьдесят|сто|двести|триста|пятьсот)(?:\s|$)`)
	reUnit    = regexp.MustCompile(`\d\s*(?:%|₸|\$|тг|тенге|руб|р(?:[^а-яa-z0-9]|$)|usd|долл|млн|млрд|тыс|к(?:[^а-яa-z0-9]|$)|k(?:[^а-яa-z0-9]|$)|кк(?:[^а-яa-z0-9]|$)|м(?:[^а-яa-z0-9]|$)|шт|чел|человек|клиент|заяв|лид|продаж|сделок|раз|дн|час|мин|мес|год|лет|кг|м2|кв)`)
	rePhone   = regexp.MustCompile(`(?:\+?[78][\s\-(]*\d{3}[\s\-)]*\d{3}[\s\-]*\d{2}[\s\-]*\d{2})|(?:\d{3}[\s\-]\d{2}[\s\-]\d{2})`)
	reDate    = regexp.MustCompile(`^\s*(?:\d{1,2}[./]\d{1,2}(?:[./]\d{2,4})?|\d{1,2}\s+(?:января|февраля|марта|апреля|мая|июня|июля|августа|сентября|октября|ноября|декабря))\s*$`)
	reMetric  = regexp.MustCompile(`(?:выручк|оборот|прибыл|марж|рентаб|конверси|средний чек|чек |сред[а-яa-z0-9]* чек|cac|ltv|cpl|cpa|ctr|romi|roi|нпс|nps|ебитда|ebitda|расход|затрат|фот|зарплат|аренд|налог|долг|кредит|остат|касс[аеуы](?:[^а-яa-z0-9]|$)|денег на счет|бюджет|себестоим|наценк|продаж[и]? в|продаж за|продажи|клиентов|заявок|лидов|сделок|подписчик|охват|сотрудник|штат|человек в|точек|точки|точка в|филиал|ассортимент \d|ассортимент из|позици|скорост|время ответа|отток|retention|возврат|повторн|просмотр|выкуп|дебитор|кредитор|ликвидн|окупаем|доход|инвестиц|капитал|цена|стоимость|тариф|план продаж|показател|kpi|метрик)`)
	reGoalNum = regexp.MustCompile(`(?:хочу|хотим|цель|план|планиру|выйти на|дойти до|вырасти до|увеличить до|к концу|через год|точка б)`)
	reTrend   = regexp.MustCompile(`(?:упал[а-яa-z0-9]*|падает|снизил[а-яa-z0-9]*|снижается|вырос[а-яa-z0-9]*|растет|просел[а-яa-z0-9]*|сократил[а-яa-z0-9]*|уменьшил[а-яa-z0-9]*|увеличил[а-яa-z0-9]*)`)

	// problem words: something is wrong, missing or hurts
	reProblem = regexp.MustCompile(`(?:^|\s)(?:нет\s|не хватает|нехватк|отсутств|не работает|не работают|не умеет|не умеют|не знает|не знают|не понима|не контрол|не считает|не считают|не ведет|не ведут|не ведется|не платят|не отвечают|не доходят|не выполня|не успева|не дели|не довер|не может|не могут|не растет|не продает|не продают|не покупают|не возвраща|не делегир|не системн|бардак|хаос|проблем|кассов[а-яa-z0-9]* разрыв|разрыв[а-яa-z0-9]*|убыт|в минус|минусе|долги|задолжен|просроч|срыва|срыв|жалу|жалоб|теря[а-яa-z0-9]*|потер|утеч|выгора|устал|текучк|увольня|уходят|ушли|зависит от|зависят от|зависим от|делает сам|делает все сам|сам делает|все сам|завязан[а-яa-z0-9]* на|все на мне|всё на мне|сам все|сам всё|нет времени|перегруз|конфликт|ругаются|воруют|кража|слабые|слабый|слабая|плохо|плохая|плохие|низк[а-яa-z0-9]*|падает|упал[а-яa-z0-9]*|просел[а-яa-z0-9]*|сокраща|дорого|дорогой|демпинг|зависим|не масштаб|узкое место|тормоз|хромает|стоит на месте|топчется|не понятно|непонятно|нестабильн|скачет|сезонн[а-яa-z0-9]* провал|выпадает|пропада|забыва|нет порядка|нет системы|нет плана|нет стратегии|нет команды|нет отдела|нет учета|нет учёта|нет денег|нет регламент|нет crm|нет контроля|без учета|без системы|без плана)`)
	// action words at the start: what to do
	reAction   = regexp.MustCompile(`^(?:нужно |надо |необходимо |стоит |следует |давай |давайте |будем |предлагаю |рекомендую |попробовать |попробуй |попробуем )?(?:внедрить|внедряем|внедри|ввести|введи|вводим|сделать|сделай|запустить|запусти|запускаем|настроить|настрой|провести|проведи|написать|напиши|составить|составь|нанять|найми|нанимаем|использовать|используй|применить|примени|автоматизир[а-яa-z0-9]*|делегир[а-яa-z0-9]*|передать|передай|выстроить|выстрой|построить|построй|создать|создай|начать|начни|перейти на|перейди на|завести|заведи|сократить|сократи|поднять|подними|повысить|повысь|снизить|снизь|убрать|убери|отказаться от|разделить|раздели|описать|опиши|посчитать|посчитай|считать|вести|веди|разработать|разработай|обучить|обучи|протестировать|протестируй|тестировать|сегментир[а-яa-z0-9]*|оцифровать|оцифруй|ввести|утвердить|утверди|закрепить|закрепи|поставить|поставь|ставить|еженедельно|ежедневно|каждый день|каждую неделю|раз в неделю)(?:[^а-яa-z0-9]|$)`)
	reToolWord = regexp.MustCompile(`(?:инструмент|методик|скрипт|регламент|чек-лист|чеклист|crm|срм|амо|amocrm|битрикс|воронк|дашборд|таблиц|платежн[а-яa-z0-9]* календар|бюджетирован|unit-экономик|юнит-экономик|okr|kpi систем|планерк|пятиминутк|стендап|канбан|трекер задач|ценообразован|абонемент|подписк|лид-магнит|лидмагнит|автоворонк|рассылк|таргет|реферальн|программ[а-яa-z0-9]* лояльн|оргструктур|должностн[а-яa-z0-9]* инструкц|адаптаци|онбординг|мотивац[а-яa-z0-9]* систем|систем[а-яa-z0-9]* мотивац|бонусн[а-яa-z0-9]* систем|прайс|коммерческ[а-яa-z0-9]* предлож|кп(?:[^а-яa-z0-9]|$)|ддс|опиу|p&l|отчет о движении)`)
)

// ClassifyNote sorts one note. diag and tools are the library's titles (may
// be empty): a note near a library title of the right kind gets it as Match.
func ClassifyNote(text string, diag, tools []string) NoteKind {
	t := Norm(text)
	if t == "" {
		return NoteKind{Kind: KindNote, Conf: 0.3, Why: "пустая"}
	}
	words := strings.Fields(t)
	digits := reNumber.MatchString(t)
	numeric := digits || reNumWord.MatchString(" "+t+" ")

	// 1. phones, bare dates, mostly numbers: data
	if rePhone.MatchString(t) && !reProblem.MatchString(" "+t) {
		return NoteKind{Kind: KindMetric, Conf: 0.95, Why: "телефон", Label: "Телефон", Value: rePhone.FindString(t)}
	}
	if reDate.MatchString(t) {
		return NoteKind{Kind: KindMetric, Conf: 0.9, Why: "дата", Label: "Дата", Value: strings.TrimSpace(t)}
	}
	nd := 0
	for _, w := range words {
		if reNumber.MatchString(w) {
			nd++
		}
	}
	problem := reProblem.MatchString(" " + t)
	metricWord := reMetric.MatchString(t)
	unit := reUnit.MatchString(t)

	// an action said first wins over its numbers: «Поднять цены на 10%»
	if reAction.MatchString(t) && !problem && !reGoalNum.MatchString(t) {
		k := NoteKind{Kind: KindTool, Conf: 0.75, Why: "действие"}
		if m, s := bestIn(t, tools); s >= 0.7 {
			k.Match = m
		}
		return k
	}

	// 2. a number with a metric word or a unit: «выручка 4 млн», «конверсия 12%»
	if numeric && (metricWord || unit || nd*2 >= len(words)) {
		k := NoteKind{Kind: KindMetric, Conf: 0.85, Why: "цифра"}
		if metricWord {
			k.Conf = 0.92
		}
		k.Label, k.Value = metricParts(text)
		k.Point = "A"
		if reGoalNum.MatchString(t) {
			k.Point = "B"
		}
		// «выручка упала на 30%, клиенты уходят»: the number is data, the rest a problem;
		// the card is «Цифры», the diagnosis is offered next to it
		if problem && !reTrend.MatchString(t) {
			k.Why = "цифра и проблема"
		}
		if m, s := Best(t, diag); s >= 0.8 && problem {
			k.Match = m
		}
		return k
	}

	// 3. a tool: the library names it, or the note says what to do
	if m, s := bestIn(t, tools); s >= 0.82 {
		return NoteKind{Kind: KindTool, Conf: s, Why: "инструмент из библиотеки", Match: m}
	}
	if reAction.MatchString(t) && !problem {
		k := NoteKind{Kind: KindTool, Conf: 0.75, Why: "действие"}
		if m, s := bestIn(t, tools); s >= 0.7 {
			k.Match = m
		}
		return k
	}
	if reToolWord.MatchString(t) && !problem && !strings.HasPrefix(t, "есть ") && !strings.HasPrefix(t, "у нас ") {
		k := NoteKind{Kind: KindTool, Conf: 0.65, Why: "название инструмента"}
		if m, s := bestIn(t, tools); s >= 0.7 {
			k.Match = m
		}
		return k
	}

	// 4. a clear problem statement: a diagnosis to confirm
	if m, s := bestIn(t, diag); s >= 0.82 {
		return NoteKind{Kind: KindDiag, Conf: s, Why: "диагноз из библиотеки", Match: m}
	}
	if problem {
		k := NoteKind{Kind: KindDiag, Conf: 0.7, Why: "проблема"}
		if m, s := bestIn(t, diag); s >= 0.66 {
			k.Match = m
		}
		return k
	}

	// 5. everything else is a note
	return NoteKind{Kind: KindNote, Conf: 0.6, Why: "наблюдение"}
}

// bestIn: a library title inside the note («у нас кассовые разрывы» has
// «Кассовые разрывы»), or the note as a whole near a title.
func bestIn(t string, lib []string) (string, float64) {
	best, bs := "", 0.0
	ws := strings.Fields(t)
	for _, title := range lib {
		tn := Norm(title)
		tw := strings.Fields(tn)
		if len(tw) == 0 {
			continue
		}
		var s float64
		if len(tw) >= 2 || len([]rune(tn)) >= 7 {
			for i := 0; i+len(tw) <= len(ws); i++ {
				if v := sim(strings.Join(ws[i:i+len(tw)], " "), tn); v > s {
					s = v
				}
			}
		}
		if len(ws) <= len(tw)+2 {
			s = max(s, sim(t, tn))
		}
		if s > bs {
			best, bs = title, s
		}
	}
	return best, bs
}

var reFirstNum = regexp.MustCompile(`[+\-]?\d[\d\s.,]*\s*(?:%|₸|\$|тг|тенге|млн|млрд|тыс\.?|миллион[а-яa-z0-9]*|тысяч[а-яa-z0-9]*|к(?:[^а-яa-z0-9]|$)|кк(?:[^а-яa-z0-9]|$))?`)

// metricParts: «Выручка 4 млн в месяц» → «Выручка», «4 млн в месяц».
func metricParts(text string) (string, string) {
	s := strings.TrimSpace(text)
	loc := reFirstNum.FindStringIndex(s)
	if loc == nil {
		return "Цифры", s
	}
	label := strings.Trim(strings.TrimSpace(s[:loc[0]]), ":-–,")
	label = strings.TrimSpace(strings.TrimSuffix(strings.TrimSuffix(label, " это"), " составляет"))
	val := strings.TrimSpace(s[loc[0]:])
	if label == "" {
		label = "Цифры"
	}
	return Cap(label), val
}
