package voicecmd

import (
	"regexp"
	"strings"
	"time"
)

// Node: a board node as the page sends it (voiceContext()).
type Node struct {
	ID     any    `json:"id"`
	Type   string `json:"type"`
	Role   string `json:"role"`
	Title  string `json:"title"`
	Parent any    `json:"parent"`
	Done   bool   `json:"done"`
}

// Context: what the page knows when the command comes.
type Context struct {
	Nodes       []Node   `json:"nodes"`
	Selected    any      `json:"selected"`
	LastCreated []any    `json:"last_created"`
	Residents   []string `json:"residents"`
	DiagLib     []string `json:"diag_library"`
	ToolLib     []string `json:"tool_library"`
	Team        []string `json:"team"`
}

// Action: one board action, in the page's runVoiceActions format.
type Action map[string]any

// Result of the rules.
type Result struct {
	Text    string   `json:"text"`    // the command as understood (corrected)
	Actions []Action `json:"actions"` // in order
	Say     string   `json:"say"`
	Rule    string   `json:"rule"` // which rule understood it (tests, logs)
}

func (c Context) vocab() Vocab {
	v := Vocab{Diag: c.DiagLib, Tools: c.ToolLib, Residents: c.Residents, Team: c.Team}
	for _, n := range c.Nodes {
		if n.Title != "" && n.Type != "root" && len([]rune(n.Title)) <= 60 {
			v.Nodes = append(v.Nodes, n.Title)
		}
	}
	return v
}

// verbs that start a new command inside one phrase («… и назначь …»)
var startVerbs = map[string]bool{}

func init() {
	for _, w := range strings.Fields(`добавь поставь создай запиши назначь открой покажи отметь пометь закрой привяжи прикрепи
		перенеси перемести запланируй внеси прочитай озвучь зачитай засеки запусти включи отмени задача заметка диагноз инструмент
		напомни выполнил выполнена сделал задачу заметку цель вопрос таймер`) {
		startVerbs[w] = true
	}
}

// Parse turns a spoken command into board actions. ok is false when no rule
// understood it (then the AI chain is asked).
func Parse(text string, c Context, now time.Time) (Result, bool) {
	t := StripWake(text)
	if t == "" {
		return Result{Text: "", Say: "Слушаю", Rule: "empty"}, true
	}
	t = Correct(t, c.vocab())
	parts := splitClauses(t)
	res := Result{Text: t}
	var says []string
	for i, p := range parts {
		r, ok := parseClause(p, c, now, i > 0)
		if !ok {
			if i == 0 {
				return Result{Text: t}, false
			}
			// the tail is not a command of its own: it belongs to the previous one
			return Result{Text: t}, false
		}
		res.Actions = append(res.Actions, r.Actions...)
		if r.Say != "" {
			says = append(says, r.Say)
		}
		if res.Rule == "" {
			res.Rule = r.Rule
		} else {
			res.Rule += "+" + r.Rule
		}
	}
	res.Say = strings.Join(says, ". ")
	return res, true
}

// splitClauses: «поставь диагноз X и назначь инструмент Y» → two clauses.
func splitClauses(t string) []string {
	ws := strings.Fields(t)
	var out []string
	cur := []string{}
	for i := 0; i < len(ws); i++ {
		w := ws[i]
		if (w == "и" || w == "потом" || w == "затем" || w == "также" || w == "а") && i+1 < len(ws) && len(cur) > 1 {
			j := i + 1
			if j < len(ws) && (ws[j] == "потом" || ws[j] == "еще" || ws[j] == "также") {
				j++
			}
			if j < len(ws) && startVerbs[ws[j]] {
				out = append(out, strings.Join(cur, " "))
				cur = []string{}
				i = j - 1
				continue
			}
		}
		cur = append(cur, w)
	}
	if len(cur) > 0 {
		out = append(out, strings.Join(cur, " "))
	}
	return out
}

var (
	reStop    = regexp.MustCompile(`^(?:стоп|хватит|отбой|выключись|выключайся|спасибо хватит|перестань слушать|не слушай|выключи (?:микрофон|помощника|прослушку|прослушивание))$`)
	reUndo    = regexp.MustCompile(`^(?:отмени|отмена|отменить|отмени это|отмени последнее(?: действие)?|верни (?:как было|назад|обратно)|откати)$`)
	reSave    = regexp.MustCompile(`^(?:сохрани|сохранить|сохрани доску)$`)
	reTimer   = regexp.MustCompile(`(?:^|\s)(?:таймер|засеки|засечь)(?:\s|$)`)
	reTimerX  = regexp.MustCompile(`^(?:останови|стоп|выключи|убери|сбрось|отмени) таймер|^таймер (?:стоп|выключи)`)
	reSumm    = regexp.MustCompile(`^(?:прочитай|прочти|озвучь|зачитай|расскажи|подведи|скажи|дай)(?: мне| нам| вслух)?\s+(?:итог|итоги|сводку|резюме|саммари|кратко|что на доске|что у нас|summary)|^(?:что у нас на доске|итоги вслух|подведи итог|подведи итоги)$`)
	reOpen    = regexp.MustCompile(`^(?:открой|открыть|покажи|перейди(?: к| на| в)?|переключись(?: на)?|давай)\s+(.+)$`)
	reDone    = regexp.MustCompile(`^(?:отметь|пометь|закрой|заверши|отметить)\s+(?:задачу\s+)?(.+?)(?:\s+(?:как\s+)?(?:выполненн?ой|выполненн?ую|выполнена|выполнено|сделанн?ой|сделанн?ую|сделано|сделана|готовой|готово|готова))?$`)
	reDone2   = regexp.MustCompile(`^(?:задача\s+)?(.+?)\s+(?:выполнена|выполнено|сделана|сделано|готова|готово|закрыта)$`)
	reDone3   = regexp.MustCompile(`^(?:выполнил|выполнила|выполнили|сделал|сделала|сделали|закрыли)\s+(?:задачу\s+)?(.+)$`)
	reAttach  = regexp.MustCompile(`^(привяжи|прикрепи|прицепи|свяжи|соедини|перенеси|перемести|подвесь|переложи)\s+(.+?)\s+(?:к|под|с|в)\s+(.+)$`)
	rePlan    = regexp.MustCompile(`^(?:(?:добавь|внеси|запиши|поставь|занеси|включи)\s+)?(?:в\s+план(?:ы)?(?:\s+команды)?|в\s+канбан)\s+(.+)$|^запланируй\s+(.+)$|^(?:добавь|внеси|запиши)\s+(.+?)\s+в\s+план(?:ы)?$`)
	rePoint   = regexp.MustCompile(`^(?:(?:запиши|заполни|поставь)\s+)?(?:в\s+)?точк[ауе]\s+(а|б|a|b)\s+(.+)$`)
	reDiag    = regexp.MustCompile(`^(?:(?:поставь|добавь|создай|запиши|ставим|ставлю|поставить|добавить)\s+)?(?:новый\s+)?(?:диагноз|болезнь)\s+(.+)$`)
	reTool    = regexp.MustCompile(`^(?:(?:назначь|добавь|поставь|создай|пропиши|назначить|добавить)\s+)?(?:новый\s+)?(?:инструмент|лечение)\s+(.+)$|^(?:назначь|пропиши)\s+(.+)$`)
	reTask    = regexp.MustCompile(`^(?:(?:добавь|поставь|создай|создая|создаю|создать|запиши|заведи|сделай|поставить|добавить|новая)\s+)?(?:новую\s+)?(?:задачу|задача|таск)\s+(.+)$|^напомни(?:\s+мне)?\s+(.+)$`)
	reGoal    = regexp.MustCompile(`^(?:(?:добавь|поставь|создай|запиши)\s+)?(?:новую\s+)?цель\s+(.+)$`)
	reQuest   = regexp.MustCompile(`^(?:(?:добавь|задай|запиши)\s+)?вопрос\s+(.+)$`)
	reNote    = regexp.MustCompile(`^(?:(?:добавь|создай|сделай|запиши)\s+)?(?:заметку|заметка|запись)\s+(.+)$|^(?:запиши|записать|пометка|отметь что|зафиксируй)\s+(.+)$`)
	rePresent = regexp.MustCompile(`^(?:режим\s+)?(?:презентаци[яюи]|показ)(?:\s+(вкл|включи|выкл|выключи))?$|^(?:включи|выключи|закрой)\s+презентацию$`)
)

func first(m []string) string {
	for _, s := range m[1:] {
		if strings.TrimSpace(s) != "" {
			return strings.TrimSpace(s)
		}
	}
	return ""
}

func parseClause(t string, c Context, now time.Time, chained bool) (Result, bool) {
	t = strings.TrimSpace(fillerRe.ReplaceAllString(t, ""))
	switch {
	case reStop.MatchString(t):
		return Result{Actions: []Action{{"op": "stop_listen"}}, Say: "Выключаюсь", Rule: "stop"}, true
	case reUndo.MatchString(t):
		return Result{Actions: []Action{{"op": "undo"}}, Say: "Отменил", Rule: "undo"}, true
	case reSave.MatchString(t):
		return Result{Actions: []Action{{"op": "save"}}, Say: "Сохранил", Rule: "save"}, true
	case reTimerX.MatchString(t):
		return Result{Actions: []Action{{"op": "timer", "sec": 0}}, Say: "Таймер остановлен", Rule: "timer"}, true
	case reTimer.MatchString(t):
		if sec, ok := timerSec(t); ok {
			return Result{Actions: []Action{{"op": "timer", "sec": sec}}, Say: "Таймер на " + durSay(sec), Rule: "timer"}, true
		}
	case reSumm.MatchString(t):
		return Result{Actions: []Action{{"op": "read_summary"}}, Say: "", Rule: "summary"}, true
	}
	if m := rePresent.FindStringSubmatch(t); m != nil {
		on := !(strings.HasPrefix(m[1], "вы") || strings.HasPrefix(t, "выключи") || strings.HasPrefix(t, "закрой"))
		return Result{Actions: []Action{{"op": "presentation", "on": on}}, Say: map[bool]string{true: "Режим презентации", false: "Презентация выключена"}[on], Rule: "present"}, true
	}
	if m := rePoint.FindStringSubmatch(t); m != nil {
		which := "A"
		if m[1] == "б" || m[1] == "b" {
			which = "B"
		}
		return Result{Actions: []Action{{"op": "set_point", "which": which, "text": Cap(m[2])}}, Say: "Точка " + map[string]string{"A": "А", "B": "Б"}[which] + " записана", Rule: "point"}, true
	}
	if m := rePlan.FindStringSubmatch(t); m != nil {
		body := first(m)
		title, date, who := taskFields(body, c, now)
		a := Action{"op": "plan_add", "title": Cap(title)}
		say := "В плане: " + Cap(title)
		if !date.IsZero() {
			a["date"] = date.Format("2006-01-02")
			say += ", до " + date.Format("02.01")
		}
		if who != "" {
			a["who"] = who
		}
		return Result{Actions: []Action{a}, Say: say, Rule: "plan"}, true
	}
	if m := reAttach.FindStringSubmatch(t); m != nil {
		x, y := nodeRef(m[2], c, ""), nodeRef(m[3], c, "")
		if x != nil && y != nil {
			op := "attach"
			if m[1] == "свяжи" || m[1] == "соедини" {
				op = "link"
			}
			if op == "link" {
				return Result{Actions: []Action{{"op": "link", "a": x, "b": y}}, Say: "Связал", Rule: "link"}, true
			}
			return Result{Actions: []Action{{"op": "attach", "id": x, "to": y}}, Say: "Перенёс", Rule: "attach"}, true
		}
	}
	for _, re := range []*regexp.Regexp{reDone3, reDone2, reDone} {
		if m := re.FindStringSubmatch(t); m != nil {
			if strings.HasPrefix(t, "задача ") && re == reDone {
				continue
			}
			if id := nodeRef(m[1], c, "task"); id != nil {
				return Result{Actions: []Action{{"op": "task_done", "id": id}}, Say: "Задача выполнена", Rule: "done"}, true
			}
		}
	}
	if m := reDiag.FindStringSubmatch(t); m != nil {
		q, parent := splitParent(m[1], c)
		a := Action{"op": "pick_diag", "query": libTitle(q, c.DiagLib)}
		if parent != nil {
			a["parent"] = parent
		}
		return Result{Actions: []Action{a}, Say: "Диагноз «" + Cap(a["query"].(string)) + "»", Rule: "diag"}, true
	}
	if m := reTool.FindStringSubmatch(t); m != nil {
		q, parent := splitParent(first(m), c)
		a := Action{"op": "pick_tool", "query": libTitle(q, c.ToolLib)}
		if parent != nil {
			a["parent"] = parent
		} else if chained {
			a["parent"] = "last"
		}
		return Result{Actions: []Action{a}, Say: "Инструмент «" + Cap(a["query"].(string)) + "»", Rule: "tool"}, true
	}
	if m := reTask.FindStringSubmatch(t); m != nil {
		body, parent := splitParent(first(m), c)
		title, date, who := taskFields(body, c, now)
		title = restoreNames(title, c)
		if title == "" {
			return Result{}, false
		}
		a := Action{"op": "add_node", "type": "task", "title": Cap(title)}
		say := "Задача: " + Cap(title)
		if !date.IsZero() {
			a["date"] = date.Format("2006-01-02")
			say += ", срок " + date.Format("02.01")
		}
		if who != "" {
			a["who"] = who
			say += ", ответственный " + who
		}
		if parent != nil {
			a["parent"] = parent
		} else if chained {
			a["parent"] = "last"
		}
		return Result{Actions: []Action{a}, Say: say, Rule: "task"}, true
	}
	if m := reGoal.FindStringSubmatch(t); m != nil {
		return Result{Actions: []Action{{"op": "add_node", "type": "goal", "title": Cap(m[1])}}, Say: "Цель добавлена", Rule: "goal"}, true
	}
	if m := reQuest.FindStringSubmatch(t); m != nil {
		return Result{Actions: []Action{{"op": "add_node", "type": "quest", "title": Cap(m[1])}}, Say: "Вопрос добавлен", Rule: "quest"}, true
	}
	if m := reNote.FindStringSubmatch(t); m != nil {
		body, parent := splitParent(first(m), c)
		a := Action{"op": "add_node", "type": "note", "title": Cap(body)}
		if parent != nil {
			a["parent"] = parent
		}
		return Result{Actions: []Action{a}, Say: "Заметка добавлена", Rule: "note"}, true
	}
	if m := reOpen.FindStringSubmatch(t); m != nil {
		if a, say, ok := openTarget(m[1], c); ok {
			return Result{Actions: []Action{a}, Say: say, Rule: "open"}, true
		}
	}
	return Result{}, false
}

// ── timer ──

func timerSec(t string) (int, bool) {
	ws := strings.Fields(t)
	for i := range ws {
		n, u := cardinal(ws, i)
		if ws[i] == "полчаса" {
			return 1800, true
		}
		if u == 0 {
			continue
		}
		j := i + u
		if j >= len(ws) {
			continue
		}
		switch {
		case strings.HasPrefix(ws[j], "минут"):
			if ws[i] == "полтора" {
				return 90, true
			}
			return n * 60, n > 0
		case strings.HasPrefix(ws[j], "секунд"):
			return n, n > 0
		case strings.HasPrefix(ws[j], "час"):
			if ws[i] == "полтора" {
				return 5400, true
			}
			return n * 3600, n > 0
		}
	}
	for _, w := range ws {
		if strings.HasPrefix(w, "минуту") {
			return 60, true
		}
		if w == "час" {
			return 3600, true
		}
	}
	return 0, false
}

func durSay(sec int) string {
	switch {
	case sec%3600 == 0:
		return plural(sec/3600, "час", "часа", "часов")
	case sec%60 == 0:
		return plural(sec/60, "минуту", "минуты", "минут")
	}
	return plural(sec, "секунду", "секунды", "секунд")
}

func plural(n int, one, few, many string) string {
	m10, m100 := n%10, n%100
	w := many
	if m10 == 1 && m100 != 11 {
		w = one
	} else if m10 >= 2 && m10 <= 4 && (m100 < 12 || m100 > 14) {
		w = few
	}
	return itoa(n) + " " + w
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	s := ""
	neg := n < 0
	if neg {
		n = -n
	}
	for n > 0 {
		s = string(rune('0'+n%10)) + s
		n /= 10
	}
	if neg {
		s = "-" + s
	}
	return s
}

// ── references to board nodes ──

var pronouns = map[string]string{
	"это": "selected", "этот": "selected", "эту": "selected", "его": "selected", "ее": "selected", "нему": "selected", "ней": "selected",
	"него": "selected", "выделенный": "selected", "выделенную": "selected", "выделенное": "selected", "этому": "selected", "этой": "selected", "сюда": "selected",
	"последний": "last", "последнюю": "last", "последнее": "last", "последней": "last", "последнему": "last",
}

// nodeRef finds a node of the board by what was said: a pronoun, or the
// node's title. kind limits the node type ("task"); "" any.
func nodeRef(q string, c Context, kind string) any {
	q = strings.TrimSpace(strings.TrimPrefix(strings.TrimPrefix(q, "задачу "), "узел "))
	for _, w := range []string{"задачи ", "диагнозу ", "диагноза ", "инструменту ", "инструмента ", "заметке ", "заметку ", "диагноз ", "инструмент "} {
		q = strings.TrimPrefix(q, w)
	}
	if p, ok := pronouns[q]; ok {
		if p == "selected" && c.Selected == nil {
			if len(c.LastCreated) > 0 {
				return c.LastCreated[0]
			}
			return nil
		}
		return p
	}
	switch q {
	case "точке а", "точка а", "точку а":
		return "pointA"
	case "точке б", "точка б", "точку б":
		return "pointB"
	case "центру", "центр", "резиденту":
		return "root"
	}
	var titles []string
	var ids []any
	for _, n := range c.Nodes {
		if kind != "" && n.Type != kind {
			continue
		}
		if n.Title == "" {
			continue
		}
		titles = append(titles, n.Title)
		ids = append(ids, n.ID)
	}
	best, s := Best(q, titles)
	if best == "" || s < 0.7 {
		// a word of the title is enough when only one node has it
		qn := Norm(q)
		hit := -1
		for i, t := range titles {
			for _, w := range strings.Fields(Norm(t)) {
				if len([]rune(w)) >= 5 && len([]rune(qn)) >= 5 && sim(w, qn) >= 0.8 {
					if hit >= 0 && hit != i {
						return nil
					}
					hit = i
				}
			}
		}
		if hit >= 0 {
			return ids[hit]
		}
		return nil
	}
	for i, t := range titles {
		if t == best {
			return ids[i]
		}
	}
	return nil
}

// splitParent: «… к кассовым разрывам», «… к нему»: the parent node, when
// the tail names one; the rest is the title.
func splitParent(s string, c Context) (string, any) {
	ws := strings.Fields(s)
	for i := len(ws) - 1; i >= 1; i-- {
		if ws[i] != "к" && ws[i] != "под" && ws[i] != "для" {
			continue
		}
		tail := strings.Join(ws[i+1:], " ")
		if tail == "" {
			continue
		}
		if p, ok := pronouns[tail]; ok {
			return strings.Join(ws[:i], " "), p
		}
		if id := nodeRef(tail, c, ""); id != nil {
			if _, isPron := id.(string); !isPron || id == "pointA" || id == "pointB" || id == "root" {
				return strings.Join(ws[:i], " "), id
			}
		}
	}
	return s, nil
}

// libTitle: the library's exact title when the query is near one.
func libTitle(q string, lib []string) string {
	q = strings.TrimSpace(q)
	if best, s := Best(q, lib); best != "" && s >= 0.78 {
		return best
	}
	return Cap(q)
}

// ── task fields: deadline and owner ──

var (
	reWho    = regexp.MustCompile(`(?:^|\s)(?:ответственн(?:ый|ая|ые|ым|ой)|ответственность на|исполнитель|поручи(?:ть)?|назначь на|пусть сделает|пусть)\s+([а-яa-z]+)`)
	reWhoFor = regexp.MustCompile(`(?:^|\s)(?:для|на)\s+([а-яa-z]+)(?:\s|$)`)
)

// R83: «дедлайн» said in every way Whisper and the browser's model write it
// («дед лайн», «дедлаин», «дидлайн», «дэдлайн», «deadline», «с дедлайном»,
// «крайний срок») becomes the plain «срок» the deadline reader knows.
var reDeadline = regexp.MustCompile(`(?:^|\s)(?:(?:с|со)\s+)?(?:крайн(?:ий|им|его)\s+срок(?:ом|а)?|сроком|срок|д[еэиа]д\s*-?\s*ла[йи]?н(?:ом|а|у|е|ы)?|dead\s*line|дедлайн(?:ом|а|у|е|ы)?)(?:\s+(?:исполнения|выполнения|сдачи|это|будет|ставим|ставлю|поставь|стоит))?(?:\s|$)`)

// DeadlineWords: the deadline word of a task in one form («срок»).
func DeadlineWords(s string) string {
	for i := 0; i < 2; i++ { // twice: neighbours share the space between them
		s = reDeadline.ReplaceAllString(s, " срок ")
	}
	return strings.Join(strings.Fields(s), " ")
}

// taskFields pulls the deadline and the owner out of a task's words.
func taskFields(s string, c Context, now time.Time) (title string, date time.Time, who string) {
	s = DeadlineWords(s)
	ws := strings.Fields(s)
	keep := make([]bool, len(ws))
	for i := range keep {
		keep[i] = true
	}
	// deadline: «до пятницы», «к пятому октября», «на завтра», «срок …», or a bare «завтра»
	for i := 0; i < len(ws); i++ {
		j := i
		pre := 0
		if ws[i] == "до" || ws[i] == "да" || ws[i] == "к" || ws[i] == "на" || ws[i] == "в" || ws[i] == "во" || ws[i] == "срок" {
			j = i + 1
			pre = 1
			if j < len(ws) && (ws[j] == "до" || ws[j] == "в" || ws[j] == "на" || ((ws[i] == "срок") && (ws[j] == "к" || ws[j] == "ко" || ws[j] == "во"))) {
				j++
				pre++
			}
		}
		if d, u := dateAt(ws, j, now); u > 0 {
			date = d
			for k := i; k < j+u; k++ {
				keep[k] = false
			}
			_ = pre
			break
		}
	}
	rest := []string{}
	for i, w := range ws {
		if keep[i] {
			rest = append(rest, w)
		}
	}
	s = strings.Join(rest, " ")
	people := append(append([]string{}, c.Team...), c.Residents...)
	if m := reWho.FindStringSubmatchIndex(s); m != nil {
		name := s[m[2]:m[3]]
		if p, sc := Best(name, firstNames(people)); p != "" && sc >= 0.75 {
			who = p
		} else {
			who = Cap(name)
		}
		s = strings.TrimSpace(s[:m[0]] + " " + s[m[1]:])
	} else if m := reWhoFor.FindStringSubmatchIndex(s); m != nil {
		name := s[m[2]:m[3]]
		if p, sc := Best(name, firstNames(people)); p != "" && sc >= 0.8 {
			who = p
			s = strings.TrimSpace(s[:m[0]] + " " + s[m[1]:])
		}
	}
	s = strings.TrimSpace(regexp.MustCompile(`^(?:что(?:бы)?|нужно|надо|чтобы)\s+`).ReplaceAllString(s, ""))
	return strings.Join(strings.Fields(s), " "), date, who
}

// restoreNames: people's names in a title get their capital letter back
// («позвонить асету» → «позвонить Асету»).
func restoreNames(s string, c Context) string {
	names := firstNames(append(append([]string{}, c.Team...), c.Residents...))
	ws := strings.Fields(s)
	for i, w := range ws {
		for _, n := range names {
			nn := Norm(n)
			if len([]rune(nn)) >= 3 && strings.HasPrefix(w, nn) && len([]rune(w))-len([]rune(nn)) <= 2 {
				ws[i] = Cap(w)
			}
		}
	}
	return strings.Join(ws, " ")
}

// firstNames: «Даулет Сайты» → «Даулет»; the team names as they are.
func firstNames(list []string) []string {
	var out []string
	seen := map[string]bool{}
	for _, n := range list {
		f := strings.Fields(n)
		if len(f) == 0 {
			continue
		}
		if !seen[f[0]] {
			seen[f[0]] = true
			out = append(out, f[0])
		}
	}
	return out
}

// ── open ──

var panels = []struct{ stem, tab, say string }{
	{"подготовк", "prep", "Открыл подготовку"}, {"данны", "data", "Открыл данные"}, {"тест", "tests", "Открыл тесты"},
	{"итог", "summary", "Открыл итоги"}, {"сводк", "summary", "Открыл итоги"}, {"задачи резидента", "tasks", "Открыл задачи"},
	{"отчет", "reports", "Открыл отчёты"}, {"звонк", "calls", "Открыл звонки"}, {"созвон", "calls", "Открыл звонки"},
}

func openTarget(s string, c Context) (Action, string, bool) {
	s = strings.TrimSpace(s)
	for _, p := range []string{"доску резидента ", "доску ", "разбор ", "резидента ", "карточку ", "клиента "} {
		if strings.HasPrefix(s, p) {
			name := strings.TrimSpace(strings.TrimPrefix(s, p))
			if r := resident(name, c); r != "" {
				return Action{"op": "open_resident", "name": r}, "Открываю доску: " + r, true
			}
			if p != "резидента " && p != "клиента " {
				return Action{"op": "open_resident", "name": Cap(name)}, "Открываю доску: " + Cap(name), true
			}
		}
	}
	for _, p := range panels {
		if strings.Contains(s, p.stem) {
			return Action{"op": "open_panel", "tab": p.tab}, p.say, true
		}
	}
	if r := resident(s, c); r != "" {
		return Action{"op": "open_resident", "name": r}, "Открываю доску: " + r, true
	}
	return nil, "", false
}

// resident: the resident's name as listed, from a spoken (and declined)
// form: «Даулета», «Асету», «доску Мади».
func resident(s string, c Context) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	cands := []string{s}
	ws := strings.Fields(s)
	stem := func(w string) string {
		for _, suf := range []string{"ому", "ему", "ой", "ей", "ом", "ем", "ую", "юю", "а", "у", "е", "ы", "и", "я", "ю"} {
			if strings.HasSuffix(w, suf) && len([]rune(w))-len([]rune(suf)) >= 3 {
				return strings.TrimSuffix(w, suf)
			}
		}
		return w
	}
	cands = append(cands, stem(ws[0]))
	best, bs := "", 0.0
	for _, r := range c.Residents {
		rn := Norm(r)
		rf := strings.Fields(rn)
		for _, q := range cands {
			q = Norm(q)
			s1 := sim(q, rn)
			if len(rf) > 0 {
				s1 = max(s1, sim(q, rf[0]), sim(stem(q), stem(rf[0])))
				if strings.HasPrefix(rf[0], q) && len([]rune(q)) >= 3 {
					s1 = max(s1, 0.9)
				}
			}
			if s1 > bs {
				best, bs = r, s1
			}
		}
	}
	if bs >= 0.75 {
		return best
	}
	return ""
}
