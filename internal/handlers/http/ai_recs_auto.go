package http

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/bnursik/business_surgery_backend/internal/ai"
	"github.com/bnursik/business_surgery_backend/internal/bot"
	"github.com/bnursik/business_surgery_backend/internal/content"
	pg "github.com/bnursik/business_surgery_backend/internal/repository/pg"
	"github.com/gin-gonic/gin"
)

// Рекомендации ИИ решаются сами (просьба владельца): после того как
// ежедневный кандидат найден (ai_recs.go), сервер решает.
//
//   - Понятное, не дублирующее, качественное дополнение: ИИ оформляет его в
//     богатый формат библиотеки (история, шаги с примером и ошибкой, метрики,
//     шаблон или тест), сервер проверяет его по правилам библиотеки, кладёт
//     карточку в bs_tools / bs_diag, богатую карточку в хранилище сервера
//     (scope "server", lib_rich_ai, отдаётся через /library/rich вместе со
//     встроенными, шаблон PDF по /library/template/<id>.pdf) и пишет в
//     истории рекомендаций «Добавлено автоматически».
//   - Кардинальное (похоже на существующий пункт, стоит заменить или слить,
//     противоречит методологии BS, ИИ не уверен или просит решения): статус
//     "ask", владельцу уходит сообщение в боте с кнопками «Добавить»,
//     «Заменить «X»», «Отклонить», «Подробнее на платформе». Только с 10:00
//     до 20:00 по Алматы (иначе ждёт), не больше одного сообщения в день.

// RecsBot: how the server asks the owner (wired in app.WireCalls).
type RecsBot struct {
	Send     func(ctx context.Context, chatID int64, text string, kb map[string]any) (int64, error)
	Edit     func(ctx context.Context, chatID, msgID int64, text string, kb map[string]any) error
	Platform string // the platform's address for «Подробнее на платформе»
}

const (
	recsRichScope = "server"
	recsRichKey   = "lib_rich_ai"
	recAskFrom    = 10 // quiet hours: the owner is asked 10:00-20:00 Almaty only
	recAskUntil   = 20
	recMinConf    = 0.7
	recNoteAuto   = "Добавлено автоматически"
)

// recRichStore: lib_rich_ai = {"tools":[rich], "diag":[rich], "pending":{recID: rich}}.
type recRichStore struct {
	Tools   []json.RawMessage          `json:"tools"`
	Diag    []json.RawMessage          `json:"diag"`
	Pending map[string]json.RawMessage `json:"pending"`
}

func (h *PlatformAI) richStore(ctx context.Context) (recRichStore, int, error) {
	var st recRichStore
	d, err := h.repo.GetDoc(ctx, recsRichScope, recsRichKey)
	if err != nil {
		return st, 0, err
	}
	if d == nil {
		return st, 0, nil
	}
	if !d.Deleted {
		_ = json.Unmarshal([]byte(d.Value), &st)
	}
	return st, d.Version, nil
}

func (h *PlatformAI) updateRichStore(ctx context.Context, fn func(*recRichStore) bool) error {
	var err error
	for try := 0; try < 4; try++ {
		st, ver, e := h.richStore(ctx)
		if e != nil {
			return e
		}
		if !fn(&st) {
			return nil
		}
		if st.Pending == nil {
			st.Pending = map[string]json.RawMessage{}
		}
		b, _ := json.Marshal(st)
		if _, err = h.repo.PutDoc(ctx, recsRichScope, recsRichKey, ver, string(b), false, "server:ai_recs"); err == nil {
			return nil
		} else if err != pg.ErrPlatformConflict {
			return err
		}
	}
	return err
}

// LoadRichExtra puts the rich items added at runtime into the library the
// page reads (/library/rich) and the template renderer.
func (h *PlatformAI) LoadRichExtra(ctx context.Context) error {
	if h.repo == nil {
		return nil
	}
	st, _, err := h.richStore(ctx)
	if err != nil {
		return err
	}
	return content.SetRichExtra(st.Tools, st.Diag)
}

// ── the library as the judge and the validator see it ──

type recLibItem struct {
	Kind, Organ, Title, Line string
}

// recLibrary: every tool and diagnosis known (club docs, library extension, rich).
func (h *PlatformAI) recLibrary(ctx context.Context) []recLibItem {
	var out []recLibItem
	seen := map[string]bool{}
	add := func(kind, organ, title, line string) {
		k := kind + ":" + libNorm(title)
		if libNorm(title) == "" || seen[k] {
			return
		}
		seen[k] = true
		out = append(out, recLibItem{Kind: kind, Organ: organ, Title: strings.TrimSpace(title), Line: recCut(line, 160)})
	}
	str := func(m map[string]any, k string) string { s, _ := m[k].(string); return s }
	from := func(kind string, list []map[string]any) {
		for _, it := range list {
			line := str(it, "short")
			if kind == "diag" {
				line = str(it, "desc")
			}
			add(kind, str(it, "organ"), str(it, "title"), line)
		}
	}
	for kind, key := range map[string]string{"tool": "bs_tools", "diag": "bs_diag"} {
		if d, err := h.repo.GetDoc(ctx, "club", key); err == nil && d != nil && !d.Deleted {
			var list []map[string]any
			_ = json.Unmarshal([]byte(d.Value), &list)
			from(kind, list)
		}
	}
	if ext, err := content.LibExt(); err == nil {
		from("tool", ext.Tools)
		from("diag", ext.Diag)
	}
	rt, rd := content.RichTitles()
	for _, t := range rt {
		add("tool", "", t, "")
	}
	for _, t := range rd {
		add("diag", "", t, "")
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Kind > out[j].Kind })
	return out
}

func recCut(s string, n int) string {
	s = strings.Join(strings.Fields(s), " ")
	if r := []rune(s); len(r) > n {
		return string(r[:n]) + "..."
	}
	return s
}

// ── the judge: add by itself or ask the owner ──

const recJudgePrompt = `Ты главный редактор библиотеки клуба Business Surgery (BS). Методология BS: бизнес как организм из органов (Стратегия, Маркетинг, Продажи, Команда, Финансы, Процессы, Аналитика, Продукт). Диагноз описывает болезнь органа: признаки, причины, тест. Инструмент лечит конкретную болезнь за 1-4 недели, результат проверяется цифрами. Без воды, без волшебных таблеток, без советов, которые требуют больших бюджетов. Резиденты: собственники малого и среднего бизнеса в Казахстане, чистая прибыль 2-20 млн ₸ в месяц.

Кандидат (%s, орган «%s»): «%s»
Суть: %s
Почему работает: %s
Шаги: %s
Источник: %s

Что уже есть в библиотеке (вид, название: суть):
%s

Реши, можно ли добавить кандидата в библиотеку без вопроса владельцу. Спросить владельца нужно, если хотя бы одно верно:
1) кандидат близок к существующему пункту (тот же метод или та же болезнь под другим названием) и его стоит слить с ним или заменить его;
2) кандидат противоречит методологии BS или не подходит малому бизнесу в Казахстане;
3) источник сомнительный или ты не уверен, что это полезно;
4) нужен выбор владельца.
Похожая тема без совпадения метода не мешает добавить.
Верни ТОЛЬКО JSON: {"decision":"add или ask","confidence":число от 0 до 1,"similar":"точное название похожего пункта из списка или пусто","replace":true или false,"conflict":"чем противоречит методологии или пусто","question":"вопрос владельцу или пусто","reason":"одно короткое предложение, почему так"}`

type recVerdict struct {
	Decision   string  `json:"decision"`
	Confidence float64 `json:"confidence"`
	Similar    string  `json:"similar"`
	Replace    bool    `json:"replace"`
	Conflict   string  `json:"conflict"`
	Question   string  `json:"question"`
	Reason     string  `json:"reason"`
}

// ask: the verdict needs the owner. why is said to him in one line.
func (v recVerdict) ask(rec map[string]any) (bool, string) {
	switch {
	case v.Similar != "" && v.Replace:
		return true, "можно заменить существующий пункт «" + v.Similar + "»"
	case v.Similar != "":
		return true, "похоже на «" + v.Similar + "», возможно дубль"
	case v.Conflict != "":
		return true, "может противоречить методологии BS: " + v.Conflict
	case v.Question != "":
		return true, v.Question
	case v.Decision != "add":
		return true, recOr(v.Reason, "ИИ просит решения владельца")
	case v.Confidence < recMinConf:
		return true, fmt.Sprintf("ИИ не уверен в пользе (уверенность %d%%)", int(v.Confidence*100+0.5))
	case recStr(rec, "url") == "" && recStr(rec, "source") == "":
		return true, "нет источника"
	}
	return false, ""
}

func recOr(s, def string) string {
	if strings.TrimSpace(s) == "" {
		return def
	}
	return s
}

func recKindName(kind string) string {
	if kind == "diag" {
		return "диагноз"
	}
	return "инструмент"
}

func recJoin(v any) string {
	var out []string
	switch x := v.(type) {
	case []any:
		for _, s := range x {
			out = append(out, fmt.Sprint(s))
		}
	case []string:
		out = x
	}
	return strings.Join(out, "; ")
}

func (h *PlatformAI) recJudge(ctx context.Context, rec map[string]any, lib []recLibItem) (recVerdict, error) {
	var lines []string
	for _, it := range lib {
		l := fmt.Sprintf("- %s, %s", recKindName(it.Kind), it.Title)
		if it.Line != "" {
			l += ": " + it.Line
		}
		lines = append(lines, l)
	}
	list := strings.Join(lines, "\n")
	if r := []rune(list); len(r) > 20000 {
		list = string(r[:20000])
	}
	src := strings.Join(nonEmpty(recStr(rec, "source"), recStr(rec, "company"), recStr(rec, "author"), recStr(rec, "url")), ", ")
	prompt := fmt.Sprintf(recJudgePrompt, recKindName(recStr(rec, "kind")), recStr(rec, "organ"), recStr(rec, "title"),
		recStr(rec, "summary"), recStr(rec, "why"), recJoin(rec["steps"]), recOr(src, "не указан"), list)
	cx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	ans, err := h.AI.JSON(cx, "Ты редактор библиотеки инструментов для собственников бизнеса. Отвечай только JSON.", prompt)
	if err != nil {
		return recVerdict{}, err
	}
	var v recVerdict
	if js := ai.JSONFrom(ans); js == "" || json.Unmarshal([]byte(js), &v) != nil {
		return recVerdict{}, errors.New("ИИ не вернул решение")
	}
	v.Decision = strings.ToLower(strings.TrimSpace(v.Decision))
	v.Similar = strings.Trim(recClean(v.Similar), "«»\"' .")
	v.Conflict, v.Question, v.Reason = recClean(v.Conflict), recClean(v.Question), recClean(v.Reason)
	// The similar item must be a real one: its exact title.
	if v.Similar != "" {
		found := ""
		for _, it := range lib {
			if libNorm(it.Title) == libNorm(v.Similar) {
				found = it.Title
				break
			}
		}
		if found == "" {
			v.Replace = false // nothing to replace; still a possible duplicate, asked as such
		} else {
			v.Similar = found
		}
	}
	return v, nil
}

// ── the rich format ──

const recRichSpecTool = `{"kind":"tool","organ":"...","title":"...",
 "subtitle":"одна строка: какой результат за какое время",
 "promise":"2 предложения: что будет у собственника после применения, обращение на «ты»",
 "when":["3-5 конкретных ситуаций, когда применять"],
 "hero":{"name":"имя","business":"бизнес","city":"город Казахстана","story":["2 коротких абзаца"],"before":[{"label":"...","value":"..."}],"after":[{"label":"...","value":"..."}]},
 "steps":[{"title":"...","do":"что именно сделать, 2-4 предложения","example":"конкретный пример с цифрами в ₸","mistake":"типичная ошибка"}],
 "metrics":["2-4 цифры, за которыми следить, и целевые значения"],
 "template":{"title":"название рабочего листа","intro":"1-2 предложения, как заполнять","blocks":[
   {"type":"fields","title":"...","fields":["поле"]},
   {"type":"table","title":"...","columns":["..."],"rows":[["пример строки"]],"empty_rows":6},
   {"type":"checklist","title":"...","items":["..."]},
   {"type":"scale","title":"...","items":["критерий"],"min":1,"max":10},
   {"type":"note","title":"...","lines":4}]},
 "time":"например: 2 часа на внедрение, 15 минут в неделю","level":"базовый|средний|продвинутый",
 "source":"автор или метод"}
Требования: шагов 4-7, в hero по 3 пары before и after с согласованными цифрами, в шаблоне 2-5 блоков (реальный рабочий лист, который собственник распечатает и заполнит).`

const recRichSpecDiag = `{"kind":"diag","organ":"...","title":"...","subtitle":"одна строка",
 "desc":"3-4 предложения, больно и конкретно",
 "signs":["5-7 наблюдаемых признаков"],
 "causes":["3-5 корневых причин"],
 "cost":"как именно это стоит денег: механизм и пример с цифрами в ₸, без статистики",
 "test":{"questions":[{"q":"вопрос, на который «да» означает симптом","yes":1}],"scale":"0-2 норма, 3-5 риск, 6+ диагноз подтверждён"},
 "case":{"name":"имя","business":"бизнес","story":"4-6 предложений с цифрами","result":"что изменилось после лечения"},
 "cure":["2-4 названия инструментов ТОЧНО из списка ниже"],
 "first_steps":["3 действия на эту неделю"],
 "risk":"что будет через год, если не лечить"}
Требования: вопросов в тесте 6-8.`

const recRichPrompt = `Оформи рекомендацию для библиотеки клуба Business Surgery в богатую карточку. Это продукт BS: глубоко, конкретно, как лучшие разборы клуба.

Рекомендация (%s, орган «%s»): «%s»
Суть: %s
Почему работает: %s
Шаги внедрения: %s
Метрики: %s
Адаптация для Казахстана: %s
Источник: %s

Формат (один JSON объект):
%s
%s
Правила: по-русски, обращение на «ты» в шагах и обещании; числа как «10 000 ₸», «2,4 млн ₸»; никаких тире «—», вместо них двоеточие или запятая; не выдумывай статистику и исследования, не пиши «по данным исследований»; без штампов («важно отметить», «ключ к успеху», «в современном мире», «не просто X, а Y»); контекст Казахстана: ₸, Kaspi, 2GIS, Алматы, Астана, Шымкент, Караганда, реальные типы бизнеса.
Верни ТОЛЬКО JSON объект.`

var recCliches = []string{"важно отметить", "ключ к успеху", "в современном мире", "исследования показ", "по данным исследован", "согласно исследован"}

// recCleanAny: the brand rules (no em dash) on every string of a JSON value.
func recCleanAny(v any) any {
	switch x := v.(type) {
	case string:
		return recClean(x)
	case []any:
		for i := range x {
			x[i] = recCleanAny(x[i])
		}
		return x
	case map[string]any:
		for k := range x {
			x[k] = recCleanAny(x[k])
		}
		return x
	}
	return v
}

func richEmpty(v any) bool {
	switch x := v.(type) {
	case nil:
		return true
	case string:
		return strings.TrimSpace(x) == ""
	case []any:
		return len(x) == 0
	case map[string]any:
		return len(x) == 0
	}
	return false
}

// ValidateRich applies the library's rules (the same as the build's
// check2.py) to one rich item. tools: the exact titles of existing tools;
// the cures of a diagnosis are fixed up to those titles, unknown ones dropped.
func ValidateRich(o map[string]any, tools []string) []string {
	var p []string
	b, _ := json.Marshal(o)
	s := string(b)
	if strings.Contains(s, "—") {
		p = append(p, "em dash")
	}
	low := strings.ToLower(s)
	for _, w := range recCliches {
		if strings.Contains(low, w) {
			p = append(p, "штамп: "+w)
		}
	}
	for _, k := range []string{"id", "organ", "title"} {
		if richEmpty(o[k]) {
			p = append(p, "нет "+k)
		}
	}
	switch o["kind"] {
	case "tool":
		for _, k := range []string{"subtitle", "promise", "when", "hero", "steps", "metrics", "template", "time", "level"} {
			if richEmpty(o[k]) {
				p = append(p, "нет "+k)
			}
		}
		steps, _ := o["steps"].([]any)
		if len(steps) < 4 {
			p = append(p, "шагов меньше 4")
		}
		for i, st := range steps {
			m, _ := st.(map[string]any)
			for _, k := range []string{"title", "do", "example", "mistake"} {
				if m == nil || richEmpty(m[k]) {
					p = append(p, fmt.Sprintf("шаг %d: нет %s", i+1, k))
				}
			}
		}
		tpl, _ := o["template"].(map[string]any)
		bl, _ := tpl["blocks"].([]any)
		if len(bl) < 2 || len(bl) > 5 {
			p = append(p, fmt.Sprintf("блоков шаблона %d (нужно 2-5)", len(bl)))
		}
		if tpl != nil && richEmpty(tpl["title"]) {
			p = append(p, "нет названия шаблона")
		}
		okType := map[string]bool{"fields": true, "table": true, "checklist": true, "scale": true, "note": true}
		for i, x := range bl {
			m, _ := x.(map[string]any)
			if t, _ := m["type"].(string); !okType[t] {
				p = append(p, fmt.Sprintf("блок %d: тип %q", i+1, t))
			}
		}
	case "diag":
		for _, k := range []string{"subtitle", "desc", "signs", "causes", "cost", "test", "case", "cure", "first_steps", "risk"} {
			if richEmpty(o[k]) {
				p = append(p, "нет "+k)
			}
		}
		test, _ := o["test"].(map[string]any)
		qs, _ := test["questions"].([]any)
		if len(qs) < 6 {
			p = append(p, "вопросов теста меньше 6")
		}
		// Cures: exactly the titles of existing tools.
		exact := map[string]string{}
		for _, t := range tools {
			exact[libNorm(t)] = t
		}
		cures, _ := o["cure"].([]any)
		var fixed []any
		var unknown []string
		for _, c := range cures {
			cs, _ := c.(string)
			if t, ok := exact[libNorm(cs)]; ok {
				fixed = append(fixed, t)
			} else if cs != "" {
				unknown = append(unknown, cs)
			}
		}
		if len(cures) > 0 {
			o["cure"] = fixed
		}
		if len(fixed) == 0 {
			p = append(p, "лечение: ни одного существующего инструмента ("+strings.Join(unknown, ", ")+")")
		}
	default:
		p = append(p, "kind?")
	}
	return p
}

// recEnrich asks the model for the rich card and validates it (two tries).
func (h *PlatformAI) recEnrich(ctx context.Context, rec map[string]any, tools []string) (map[string]any, error) {
	kind := recStr(rec, "kind")
	spec, extra := recRichSpecTool, ""
	if kind == "diag" {
		spec = recRichSpecDiag
		extra = "Инструменты библиотеки (для cure бери названия ТОЛЬКО отсюда, слово в слово):\n- " + strings.Join(tools, "\n- ")
		if r := []rune(extra); len(r) > 12000 {
			extra = string(r[:12000])
		}
	}
	src := strings.Join(nonEmpty(recStr(rec, "source"), recStr(rec, "company"), recStr(rec, "author"), recStr(rec, "url")), ", ")
	base := fmt.Sprintf(recRichPrompt, recKindName(kind), recStr(rec, "organ"), recStr(rec, "title"), recStr(rec, "summary"),
		recStr(rec, "why"), recJoin(rec["steps"]), recJoin(rec["metrics"]), recStr(rec, "adapt"), recOr(src, "не указан"), spec, extra)
	prompt := base
	var last []string
	for try := 0; try < 2; try++ {
		cx, cancel := context.WithTimeout(ctx, 3*time.Minute)
		ans, err := h.AI.JSON(cx, "Ты редактор библиотеки Business Surgery. Пишешь по-русски, точно и конкретно. Отвечай только JSON.", prompt)
		cancel()
		if err != nil {
			return nil, err
		}
		var o map[string]any
		if js := ai.JSONFrom(ans); js == "" || json.Unmarshal([]byte(js), &o) != nil {
			last = []string{"ответ не JSON"}
		} else {
			o = recCleanAny(o).(map[string]any)
			o["kind"], o["organ"], o["title"] = kind, recStr(rec, "organ"), recStr(rec, "title")
			o["id"] = recRichID(recStr(rec, "id"))
			o["origin"] = "ai_rec"
			if u := recStr(rec, "url"); u != "" {
				o["url"] = u
			}
			if last = ValidateRich(o, tools); len(last) == 0 {
				return o, nil
			}
		}
		prompt = base + "\n\nПрошлый ответ не прошёл проверку: " + strings.Join(last, "; ") + ". Исправь и верни весь объект заново."
	}
	return nil, errors.New("карточка не прошла проверку: " + strings.Join(last, "; "))
}

// recRichID: the id of the library item made from a recommendation.
func recRichID(recID string) string { return "ai_" + strings.TrimPrefix(recID, "rec_") }

// ── deciding ──

// autoRec decides on a fresh recommendation: added by itself, or put to the owner.
func (h *PlatformAI) autoRec(ctx context.Context, rec map[string]any, now time.Time) (string, error) {
	id := recStr(rec, "id")
	lib := h.recLibrary(ctx)
	var tools []string
	for _, it := range lib {
		if it.Kind == "tool" {
			tools = append(tools, it.Title)
		}
	}
	askWhy, similar := "", ""
	v, err := h.recJudge(ctx, rec, lib)
	if err != nil {
		askWhy = "ИИ не смог оценить кандидата (" + err.Error() + ")"
	} else {
		var ask bool
		if ask, askWhy = v.ask(rec); !ask {
			askWhy = ""
		}
		similar = v.Similar
	}
	// The rich card is made either way: the owner's «Добавить» is then instant.
	rich, rerr := h.recEnrich(ctx, rec, tools)
	if rerr != nil {
		log.Printf("ai recs: rich card for %s: %v", id, rerr)
		if askWhy == "" {
			askWhy = "не получилось оформить в полный формат библиотеки (" + rerr.Error() + ")"
		}
	} else {
		b, _ := json.Marshal(rich)
		if err := h.updateRichStore(ctx, func(st *recRichStore) bool {
			if st.Pending == nil {
				st.Pending = map[string]json.RawMessage{}
			}
			st.Pending[id] = b
			return true
		}); err != nil {
			return "", err
		}
	}
	if askWhy == "" {
		code, out := h.recApply(ctx, id, "add", "server:auto", recNoteAuto)
		if code != http.StatusOK {
			// Could not add (library not on the server yet, a conflict): ask instead.
			askWhy = fmt.Sprint("не получилось добавить: ", out["error"])
		} else {
			return "added", nil
		}
	}
	stamp := now.UTC().Format(time.RFC3339)
	err = h.updateRecs(ctx, func(items []any) ([]any, bool) {
		for _, it := range items {
			if m, ok := it.(map[string]any); ok && recStr(m, "id") == id {
				if st := recStr(m, "status"); st != "new" && st != "" {
					return items, false
				}
				m["status"], m["askWhy"], m["autoAt"] = "ask", askWhy, stamp
				m["note"] = "Ждёт решения владельца: " + askWhy
				if similar != "" {
					m["similar"] = similar
				}
				if v.Replace && similar != "" {
					m["canReplace"] = true
				}
				return items, true
			}
		}
		return items, false
	})
	if err != nil {
		return "", err
	}
	h.maybeSendAsk(ctx, now)
	return "ask", nil
}

// recApply: add | replace | reject a recommendation (team on the platform,
// the owner's buttons in the bot, or the server itself). note goes into the
// recommendations' history.
func (h *PlatformAI) recApply(ctx context.Context, id, action, by, note string) (int, gin.H) {
	var rec map[string]any
	if d, err := h.repo.GetDoc(ctx, "club", aiRecsKey); err == nil && d != nil && !d.Deleted {
		_, items := readRecs(d.Value)
		for _, it := range items {
			if recStr(it, "id") == id {
				rec, _ = it.(map[string]any)
			}
		}
	}
	if rec == nil {
		return http.StatusNotFound, gin.H{"error": "not_found"}
	}
	now := time.Now().UTC().Format(time.RFC3339)
	key, added, target := "", false, ""
	if action == "add" || action == "replace" {
		item, _ := rec["item"].(map[string]any)
		if item == nil {
			return http.StatusBadRequest, gin.H{"error": "no_item"}
		}
		key = "bs_tools"
		if recStr(rec, "kind") == "diag" {
			key = "bs_diag"
		}
		if h.clubDocInt(ctx, "bs_libver") < libExtMinLib {
			return http.StatusConflict, gin.H{"error": "Библиотека ещё не сохранена на сервере: откройте её на платформе и повторите"}
		}
		// The rich card made for it, if any: the card gets its id, so the
		// reader shows the rich view and the template is there.
		st, _, _ := h.richStore(ctx)
		raw, hasRich := st.Pending[id]
		item = cloneMap(item)
		if hasRich {
			item["id"] = recRichID(id)
		}
		var err error
		if action == "replace" {
			target = recStr(rec, "similar")
			if target == "" {
				return http.StatusBadRequest, gin.H{"error": "нечего заменять"}
			}
			added, err = h.replaceLibItem(ctx, key, target, item)
		} else {
			added, err = h.appendLibItem(ctx, key, item)
		}
		if err != nil {
			return http.StatusConflict, gin.H{"error": err.Error()}
		}
		if hasRich {
			if err := h.publishRich(ctx, id, raw); err != nil {
				log.Printf("ai recs: publish rich %s: %v", id, err)
			}
		}
	}
	err := h.updateRecs(ctx, func(items []any) ([]any, bool) {
		for _, it := range items {
			if m, ok := it.(map[string]any); ok && recStr(m, "id") == id {
				switch action {
				case "add", "replace":
					m["status"], m["addedAt"], m["addedTo"] = "added", now, key
					if target != "" {
						m["replaced"] = target
					}
				default:
					m["status"], m["rejectedAt"] = "rejected", now
				}
				if by != "" {
					m["by"] = by
				}
				if note != "" {
					m["note"] = note
				} else {
					delete(m, "note")
				}
				if by == "server:auto" {
					m["auto"] = true
				}
				return items, true
			}
		}
		return items, false
	})
	if err != nil {
		return http.StatusConflict, gin.H{"error": err.Error()}
	}
	if action == "reject" {
		_ = h.updateRichStore(ctx, func(st *recRichStore) bool {
			if _, ok := st.Pending[id]; !ok {
				return false
			}
			delete(st.Pending, id)
			return true
		})
	}
	status := map[string]string{"add": "added", "replace": "added", "reject": "rejected"}[action]
	return http.StatusOK, gin.H{"ok": true, "status": status, "key": key, "added": added, "replaced": target}
}

func cloneMap(m map[string]any) map[string]any {
	out := make(map[string]any, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}

// publishRich moves the rich card from pending into the library.
func (h *PlatformAI) publishRich(ctx context.Context, recID string, raw json.RawMessage) error {
	var probe struct {
		ID   string `json:"id"`
		Kind string `json:"kind"`
	}
	_ = json.Unmarshal(raw, &probe)
	err := h.updateRichStore(ctx, func(st *recRichStore) bool {
		delete(st.Pending, recID)
		list := &st.Tools
		if probe.Kind == "diag" {
			list = &st.Diag
		}
		for i, r := range *list {
			var x struct {
				ID string `json:"id"`
			}
			_ = json.Unmarshal(r, &x)
			if x.ID == probe.ID {
				(*list)[i] = raw
				return true
			}
		}
		*list = append(*list, raw)
		return true
	})
	if err != nil {
		return err
	}
	return h.LoadRichExtra(ctx)
}

// replaceLibItem puts item in place of the card titled target (appends when
// there is none). false: a card with item's title is already there.
func (h *PlatformAI) replaceLibItem(ctx context.Context, key, target string, item map[string]any) (bool, error) {
	title, _ := item["title"].(string)
	for try := 0; try < 4; try++ {
		d, err := h.repo.GetDoc(ctx, "club", key)
		if err != nil {
			return false, err
		}
		if d == nil || d.Deleted {
			return false, errors.New("библиотеки нет на сервере")
		}
		var list []map[string]any
		if json.Unmarshal([]byte(d.Value), &list) != nil {
			return false, errors.New("библиотека не читается")
		}
		at := -1
		for i, it := range list {
			t, _ := it["title"].(string)
			if libNorm(t) == libNorm(title) {
				return false, nil
			}
			if at < 0 && libNorm(t) == libNorm(target) {
				at = i
			}
		}
		if at >= 0 {
			list[at] = item
		} else {
			list = append(list, item)
		}
		val, _ := json.Marshal(list)
		if _, err := h.repo.PutDoc(ctx, "club", key, d.Version, string(val), false, "server:ai_recs"); err == nil {
			return true, nil
		} else if err != pg.ErrPlatformConflict {
			return false, err
		}
	}
	return false, pg.ErrPlatformConflict
}

// ── asking the owner in the bot ──

func recAskWindow(now time.Time) bool {
	h := now.In(almaty).Hour()
	return h >= recAskFrom && h < recAskUntil
}

func recAskText(rec map[string]any) string {
	var b strings.Builder
	b.WriteString("🤖 Рекомендация ИИ: нужно ваше решение\n\n")
	kind := "Инструмент"
	if recStr(rec, "kind") == "diag" {
		kind = "Диагноз"
	}
	fmt.Fprintf(&b, "%s · %s\n«%s»\n", kind, recStr(rec, "organ"), recStr(rec, "title"))
	if s := recStr(rec, "summary"); s != "" {
		fmt.Fprintf(&b, "%s\n", s)
	}
	if src := strings.Join(nonEmpty(recStr(rec, "company"), recStr(rec, "author"), recStr(rec, "source")), ", "); src != "" {
		fmt.Fprintf(&b, "Источник: %s\n", src)
	}
	fmt.Fprintf(&b, "\nПочему спрашиваю: %s", recStr(rec, "askWhy"))
	return strings.TrimSpace(b.String())
}

func recBtnTitle(s string) string {
	if r := []rune(s); len(r) > 28 {
		return string(r[:27]) + "…"
	}
	return s
}

func (h *PlatformAI) recAskKB(rec map[string]any) map[string]any {
	id := recStr(rec, "id")
	row1 := []map[string]any{{"text": "✅ Добавить", "callback_data": "airec_add_" + id}}
	if sim := recStr(rec, "similar"); sim != "" {
		row1 = append(row1, map[string]any{"text": "🔁 Заменить «" + recBtnTitle(sim) + "»", "callback_data": "airec_rep_" + id})
	}
	rows := [][]map[string]any{row1, {{"text": "✖️ Отклонить", "callback_data": "airec_rej_" + id}}}
	if u := h.RecsBot.Platform; u != "" {
		sep := "?"
		if strings.Contains(u, "?") {
			sep = "&"
		}
		rows = append(rows, []map[string]any{{"text": "Подробнее на платформе", "url": u + sep + "section=aiRec&id=" + id}})
	}
	return map[string]any{"inline_keyboard": rows}
}

// maybeSendAsk sends the newest recommendation waiting for the owner, within
// 10:00-20:00 Almaty and at most one message a day.
func (h *PlatformAI) maybeSendAsk(ctx context.Context, now time.Time) {
	if h.RecsBot.Send == nil || h.Owner == 0 || !recAskWindow(now) {
		return
	}
	today := now.In(almaty).Format("2006-01-02")
	d, err := h.repo.GetDoc(ctx, "club", aiRecsKey)
	if err != nil || d == nil || d.Deleted {
		return
	}
	doc, items := readRecs(d.Value)
	if s, _ := doc["askDay"].(string); s == today {
		return
	}
	var rec map[string]any
	for _, it := range items {
		if m, ok := it.(map[string]any); ok && recStr(m, "status") == "ask" && recStr(m, "askSent") == "" {
			rec = m
			break
		}
	}
	if rec == nil {
		return
	}
	id := recStr(rec, "id")
	// Claim the day first: a second instance or a retry must not send twice.
	claimed := false
	err = h.updateRecsDoc(ctx, func(doc map[string]any, items []any) bool {
		if s, _ := doc["askDay"].(string); s == today {
			return false
		}
		doc["askDay"] = today
		claimed = true
		return true
	})
	if err != nil || !claimed {
		return
	}
	msgID, err := h.RecsBot.Send(ctx, h.Owner, recAskText(rec), h.recAskKB(rec))
	if err != nil {
		log.Printf("ai recs: ask the owner: %v", err)
		_ = h.updateRecsDoc(ctx, func(doc map[string]any, items []any) bool {
			delete(doc, "askDay") // try again later today
			return true
		})
		return
	}
	stamp := now.UTC().Format(time.RFC3339)
	_ = h.updateRecs(ctx, func(items []any) ([]any, bool) {
		for _, it := range items {
			if m, ok := it.(map[string]any); ok && recStr(m, "id") == id {
				m["askSent"], m["askMsg"] = stamp, msgID
				return items, true
			}
		}
		return items, false
	})
}

// updateRecsDoc changes bs_ai_recs as a whole (fields and items).
func (h *PlatformAI) updateRecsDoc(ctx context.Context, fn func(doc map[string]any, items []any) bool) error {
	var err error
	for try := 0; try < 4; try++ {
		base, raw := 0, ""
		if d, e := h.repo.GetDoc(ctx, "club", aiRecsKey); e == nil && d != nil {
			base = d.Version
			if !d.Deleted {
				raw = d.Value
			}
		}
		doc, items := readRecs(raw)
		if !fn(doc, items) {
			return nil
		}
		if items == nil {
			items = []any{}
		}
		doc["items"], doc["updated"] = items, time.Now().UTC().Format(time.RFC3339)
		val, _ := json.Marshal(doc)
		if _, err = h.repo.PutDoc(ctx, "club", aiRecsKey, base, string(val), false, "server:ai_recs"); err == nil {
			return nil
		}
	}
	return err
}

// HandleRecCallback answers the owner's buttons (airec_add_<id>,
// airec_rep_<id>, airec_rej_<id>); the bot calls it for the team only.
func (h *PlatformAI) HandleRecCallback(ctx context.Context, cb bot.CallbackUpdate) (string, bool) {
	data := strings.TrimPrefix(cb.Data, "airec_")
	var action, id string
	switch {
	case strings.HasPrefix(data, "add_"):
		action, id = "add", strings.TrimPrefix(data, "add_")
	case strings.HasPrefix(data, "rep_"):
		action, id = "replace", strings.TrimPrefix(data, "rep_")
	case strings.HasPrefix(data, "rej_"):
		action, id = "reject", strings.TrimPrefix(data, "rej_")
	default:
		return "", false
	}
	var rec map[string]any
	if d, err := h.repo.GetDoc(ctx, "club", aiRecsKey); err == nil && d != nil && !d.Deleted {
		_, items := readRecs(d.Value)
		for _, it := range items {
			if recStr(it, "id") == id {
				rec, _ = it.(map[string]any)
			}
		}
	}
	if rec == nil {
		return "Рекомендация не найдена", true
	}
	if st := recStr(rec, "status"); st != "ask" && st != "new" {
		return "Уже решено: " + map[string]string{"added": "добавлено", "rejected": "отклонено"}[st], true
	}
	by := fmt.Sprintf("tg:%d", cb.FromID)
	note := map[string]string{"add": "Добавлено владельцем в Telegram", "replace": "Заменено владельцем в Telegram",
		"reject": "Отклонено владельцем в Telegram"}[action]
	code, out := h.recApply(ctx, id, action, by, note)
	if code != http.StatusOK {
		return fmt.Sprint("Не получилось: ", out["error"]), true
	}
	done := map[string]string{"add": "✅ Добавлено в библиотеку", "reject": "✖️ Отклонено",
		"replace": "🔁 Заменено «" + recStr(rec, "similar") + "»"}[action]
	if h.RecsBot.Edit != nil && cb.MessageID != 0 {
		if err := h.RecsBot.Edit(ctx, cb.ChatID, cb.MessageID, recAskText(rec)+"\n\n"+done, nil); err != nil {
			log.Printf("ai recs: edit the question: %v", err)
		}
	}
	return done, true
}

// pendingAutoRec: today's recommendation the server has not decided on yet
// (a restart between finding and deciding).
func pendingAutoRec(items []any, today string) map[string]any {
	for _, it := range items {
		if m, ok := it.(map[string]any); ok && recStr(m, "date") == today && recStr(m, "status") == "new" && recStr(m, "autoAt") == "" && m["auto"] == nil {
			return m
		}
	}
	return nil
}

// recsAskLoop sends a queued question once the quiet hours are over.
func (h *PlatformAI) recsAskLoop(ctx context.Context) {
	t := time.NewTicker(10 * time.Minute)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		c, cancel := context.WithTimeout(ctx, time.Minute)
		h.maybeSendAsk(c, time.Now())
		cancel()
	}
}
