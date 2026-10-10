package http

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/bnursik/business_surgery_backend/internal/content"
)

// Польза (R81). Владелец 10.10: «опять пришло… ерунда какая-то. Мы же
// договорились, что про пользу будем». Каждый пост Threads теперь даёт
// конкретную пользу бизнесу, и только двух видов:
//
//  1. Лид-магнит (около 50% постов): «бесплатно собрал для вас …» со ссылкой
//     выдачи (magnets.go, 33 ручных текста).
//  2. Польза: мини-гайд из библиотеки (310 инструментов и 278 диагнозов):
//     шаги, расчёт в ₸, чек-лист признаков или цифры-ориентиры, в конце мягкая
//     ссылка на магнит по теме (платёжный календарь, скрипт, оргструктура…).
//
// Охватные рубрики («спорный вопрос», мифы без цифр, мнения, найм «чтобы
// узнал каждый») и посты ИИ (бесплатная цепочка писала 44-68 знаков) из плана
// ушли. Пост пользы собирает сервер из полей библиотеки, без ИИ, и пропускает
// только через строгую проверку threadsValueProblems: не меньше 2 конкретик
// (число, ₸, %, список шагов, название шаблона), первая строка до 90 знаков с
// крючком, без общих слов («важно понимать», «успех», «мотивация», «каждый
// предприниматель»…), 300-900 знаков (Threads сам ограничивает пост 500
// знаками вместе со ссылкой, поэтому на деле 300-430).
//
// План строится на 7 дней вперёд: владелец видит посты в «SMM» (блок «Threads:
// план на 7 дней»), одобряет или нажимает «Заменить» до отправки.

// threadsValueOnly: the R81 policy (tests of the old AI and reach plan turn it off).
var threadsValueOnly = true

const (
	threadsValueMgPct = 50 // magnets' share under the value policy
	threadsValueMgCap = 6
	threadsAhead      = 7 // days the plan is built ahead for the owner's review
	thValueMinLen     = 300
	thValueMaxLen     = 900
	thValueFirstMax   = 90
)

var threadsValueFormats = []threadsFormat{
	{"v_steps", "Польза: пошагово", "Инструмент библиотеки: 3-5 шагов, пример с цифрой, ссылка на шаблон.", 1, []string{"tool"}, []string{"long"}},
	{"v_calc", "Польза: расчёт в ₸", "Диагноз библиотеки: во сколько он обходится в ₸ и первый шаг.", 1, []string{"diag"}, []string{"long"}},
	{"v_check", "Польза: чек-лист", "Диагноз библиотеки: 4-5 признаков списком и первый шаг.", 1, []string{"diag"}, []string{"long"}},
	{"v_metrics", "Польза: цифры-ориентиры", "Инструмент библиотеки: цифры, по которым видно, что он работает, и первый шаг.", 1, []string{"tool"}, []string{"long"}},
}

var threadsValueByID = func() map[string]*threadsFormat {
	m := map[string]*threadsFormat{}
	for i := range threadsValueFormats {
		m[threadsValueFormats[i].ID] = &threadsValueFormats[i]
	}
	return m
}()

func init() {
	for i := range threadsValueFormats {
		threadsFormatByID[threadsValueFormats[i].ID] = &threadsValueFormats[i]
	}
}

// IsThreadsValue: the format is a «польза» post of R81.
func IsThreadsValue(id string) bool { return threadsValueByID[id] != nil }

// ── The plan: magnets and «польза» by turns ──

// threadsValueMgCount: about half the posts are magnets (4 a day: 2).
func threadsValueMgCount(n int) int {
	if n < 2 {
		return 0
	}
	m := (n*threadsValueMgPct + 50) / 100
	if m > n/2 {
		m = n / 2
	}
	if m > threadsValueMgCap {
		m = threadsValueMgCap
	}
	if m < 1 {
		m = 1
	}
	return m
}

// threadsValuePlan: the formats of a day's n slots: the magnets spread
// evenly (4 a day: the 2nd and the 4th post), the rest «польза» formats
// taking turns in a day-dependent order.
func threadsValuePlan(n int, key string) (formats []string, isMg []bool) {
	formats, isMg = make([]string, n), make([]bool, n)
	m := threadsValueMgCount(n)
	for k := 0; k < m; k++ {
		i := (k*n)/m + n/(2*m)
		if i >= n {
			i = n - 1
		}
		isMg[i] = true
	}
	ids := make([]string, len(threadsValueFormats))
	for i, f := range threadsValueFormats {
		ids[i] = f.ID
	}
	sort.SliceStable(ids, func(a, b int) bool { return hashN(key+"/v/"+ids[a], 997) < hashN(key+"/v/"+ids[b], 997) })
	j := 0
	for i := 0; i < n; i++ {
		if isMg[i] {
			formats[i] = "magnet"
			continue
		}
		formats[i] = ids[j%len(ids)]
		j++
	}
	return formats, isMg
}

// ── The quality gate of «польза» ──

// thValueBan: generic phrases a useful post does without (lower case stems).
var thValueBan = []string{"важно понимать", "важно помнить", "в современном мире", "успех", "успеш", "мотивац", "каждый предприниматель",
	"каждому предпринимателю", "каждый собственник", "каждому собственнику", "ключ к ", "секрет", "залог", "в наше время", "на самом деле",
	"стоит задуматься", "настоящий предприниматель", "вдохнов", "мечт", "лайфхак", "спорный вопрос", "а как считаете вы", "а вы как думаете"}

var (
	thValNotButRe = regexp.MustCompile(`(?i)(^|[\s,.:(«])не [^.!?\n]{1,60}?, а `)
	thListRe      = regexp.MustCompile(`(?m)^\s*(?:\d{1,2}[.)]|[-•])\s+\S`)
	thListMarkRe  = regexp.MustCompile(`(?m)^\s*(?:\d{1,2}[.)]|[-•])\s+`)
	thNumRe       = regexp.MustCompile(`\d+(?:[  ]\d{3})*(?:[.,]\d+)?`)
	thTplRe       = regexp.MustCompile(`«[^»]{3,}»`)
	thEnumRe      = regexp.MustCompile(`(?i)трижды|дважды|первое|во-первых|(?:две|три|четыре) (?:причины|вещи|статьи)`)
	thEnumNextRe  = regexp.MustCompile(`(?i)^(второе|третье|четвёртое|во-вторых|в-третьих|ещё одно|кроме того)(?:[\s:,]|$)`)
	thSentRe      = regexp.MustCompile(`(?:[^.!?]|[.!?]\S)+[.!?]`)
	thHookWordRe  = regexp.MustCompile(`(?i)^(как|сколько|почему|что|где|зачем|когда|чек-лист|шаблон|во сколько)(?:[\s:,?]|$)`)
)

// thSpecifics counts the post's concrete things: numbers (up to 3), ₸, %,
// a list of 3+ steps, a named template.
func thSpecifics(body string) (n int, what []string) {
	plain := thListMarkRe.ReplaceAllString(body, "")
	nums := map[string]bool{}
	for _, x := range thNumRe.FindAllString(plain, -1) {
		nums[x] = true
	}
	k := len(nums)
	if k > 3 {
		k = 3
	}
	if k > 0 {
		n += k
		what = append(what, fmt.Sprintf("чисел %d", len(nums)))
	}
	if strings.Contains(body, "₸") {
		n++
		what = append(what, "₸")
	}
	if strings.Contains(body, "%") {
		n++
		what = append(what, "%")
	}
	if len(thListRe.FindAllString(body, -1)) >= 3 {
		n++
		what = append(what, "список шагов")
	}
	if thTplRe.MatchString(body) {
		n++
		what = append(what, "название шаблона")
	}
	return n, what
}

// thHook: the first line catches: a number, a question or a «how/how much».
func thHook(first string) bool {
	return strings.ContainsAny(first, "0123456789?") || thHookWordRe.MatchString(strings.TrimSpace(first)) || thTplRe.MatchString(first)
}

// threadsValueProblems: why a «польза» text (without its link line) must not
// go out; empty: it may.
func threadsValueProblems(body string) []string {
	var p []string
	n := utf8.RuneCountInString(body)
	if n < thValueMinLen {
		p = append(p, fmt.Sprintf("мало пользы: %d знаков, нужно от %d", n, thValueMinLen))
	}
	if n > thValueMaxLen {
		p = append(p, fmt.Sprintf("длинно: %d знаков", n))
	}
	first := content.FirstLine(body, 1000)
	if fl := utf8.RuneCountInString(first); fl > thValueFirstMax {
		p = append(p, fmt.Sprintf("первая строка %d знаков, нужно до %d", fl, thValueFirstMax))
	}
	if !thHook(first) {
		p = append(p, "первая строка без крючка (цифры, вопроса, «как»)")
	}
	if k, _ := thSpecifics(body); k < 2 {
		p = append(p, fmt.Sprintf("конкретики %d, нужно от 2 (число, ₸, %%, шаги, шаблон)", k))
	}
	low := strings.ToLower(body)
	for _, b := range thValueBan {
		if strings.Contains(low, b) {
			p = append(p, "общие слова: "+strings.TrimSpace(b))
		}
	}
	if thValNotButRe.MatchString(body) || strings.Contains(low, ", а не ") {
		p = append(p, "оборот «не X, а Y»")
	}
	return p
}

// ── Material: the rich library's tools and diagnoses ──

type thVTool struct {
	ID, Organ, Title, Subtitle, Time, Template string
	Steps                                      []struct{ Title, Do, Example string }
	Metrics                                    []string
}

type thVDiag struct {
	ID, Organ, Title, Subtitle, Cost string
	Signs, FirstSteps                []string
}

var (
	thVOnce  sync.Once
	thVTools []*thVTool
	thVDiags []*thVDiag
)

func threadsValuePool() ([]*thVTool, []*thVDiag) {
	thVOnce.Do(func() {
		plain, _, _, err := content.LibRich()
		if err != nil {
			log.Printf("threads: value: rich library: %v", err)
			return
		}
		var lib struct {
			Tools []struct {
				ID, Organ, Title, Subtitle, Time string
				Metrics                          []string `json:"metrics"`
				Template                         *struct {
					Title string `json:"title"`
				} `json:"template"`
				Steps []struct{ Title, Do, Example string } `json:"steps"`
			} `json:"tools"`
			Diag []struct {
				ID, Organ, Title, Subtitle, Cost string
				Signs                            []string `json:"signs"`
				FirstSteps                       []string `json:"first_steps"`
			} `json:"diag"`
		}
		if err := json.Unmarshal(plain, &lib); err != nil {
			log.Printf("threads: value: rich library: %v", err)
			return
		}
		for _, t := range lib.Tools {
			if thPersonal[t.Organ] || len(t.Steps) < 3 {
				continue
			}
			x := &thVTool{ID: t.ID, Organ: t.Organ, Title: t.Title, Subtitle: t.Subtitle, Time: t.Time, Metrics: t.Metrics, Steps: t.Steps}
			if t.Template != nil {
				x.Template = t.Template.Title
			}
			thVTools = append(thVTools, x)
		}
		for _, d := range lib.Diag {
			if thPersonal[d.Organ] {
				continue
			}
			thVDiags = append(thVDiags, &thVDiag{ID: d.ID, Organ: d.Organ, Title: d.Title, Subtitle: d.Subtitle, Cost: d.Cost, Signs: d.Signs, FirstSteps: d.FirstSteps})
		}
	})
	return thVTools, thVDiags
}

// thValueMagnet: the magnet a «польза» post of the organ points to.
var thValueMagnet = map[string]string{"Финансы": "paycal", "Продажи": "script", "Команда": "org", "Процессы": "org",
	"Стратегия": "plan10", "Маркетинг": "diag", "Продукт": "map", "Аналитика": "guides"}

func thValueMagnetFor(organ string) *leadMagnet {
	if m := leadMagnetByID[thValueMagnet[organ]]; m != nil {
		return m
	}
	return leadMagnetByID["guides"]
}

// thValueCTA: the soft last line with the magnet's link (the slot's code is
// put in by magnetize).
func thValueCTA(m *leadMagnet) string {
	if v, ok := thCTACache.Load(m.ID); ok {
		return v.(string)
	}
	s := "Бесплатно забрать «" + m.title() + "»: " + magnetURL(m.ID, "p0000000000")
	thCTACache.Store(m.ID, s)
	return s
}

var thCTACache sync.Map // the magnets' titles count the library once

// ── Composing ──

func thTrimDot(s string) string { return strings.TrimRight(strings.TrimSpace(s), ".;: ") }

// thSentences: the leading whole sentences of s within n signs.
func thSentences(s string, n int) string {
	s = strings.TrimSpace(s)
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	r := []rune(s)
	cut := -1
	for i := 0; i < len(r) && i < n; i++ {
		if (r[i] == '.' || r[i] == '!' || r[i] == '?') && (i+1 == len(r) || r[i+1] == ' ') {
			cut = i + 1
		}
	}
	if cut <= 0 {
		return ""
	}
	return strings.TrimSpace(string(r[:cut]))
}

// thClause: the first clause (before a comma or a full stop).
func thClause(s string) string {
	s = strings.TrimSpace(s)
	for _, sep := range []string{", ", ". ", "; "} { // 1,5 часа keeps its comma
		if i := strings.Index(s, sep); i > 0 {
			s = s[:i]
		}
	}
	return strings.TrimRight(strings.TrimSpace(s), ".;,")
}

func thHasDigit(s string) bool { return strings.ContainsAny(s, "0123456789") }

func ruPlural(n int, one, few, many string) string { return content.Plural(n, one, few, many) }

// thFit: the head and the lines that fit max signs (lines dropped from the end,
// keeping at least keep of them), then the tail lines while they fit.
func thFit(head string, lines []string, keep int, tail []string, max int) string {
	for len(lines) > keep && utf8.RuneCountInString(head+"\n\n"+strings.Join(lines, "\n")) > max {
		lines = lines[:len(lines)-1]
	}
	body := head + "\n\n" + strings.Join(lines, "\n")
	for _, t := range tail {
		if t == "" {
			continue
		}
		if x := body + "\n\n" + t; utf8.RuneCountInString(x) <= max {
			body = x
		}
	}
	return body
}

// composeValue: the «польза» text of a format from a tool or a diagnosis
// (empty when the material does not make one).
func composeValue(format string, t *thVTool, d *thVDiag, max int) string {
	switch format {
	case "v_steps":
		if t == nil {
			return ""
		}
		var lines []string
		for i, s := range t.Steps {
			if i >= 5 {
				break
			}
			lines = append(lines, fmt.Sprintf("%d. %s.", i+1, thTrimDot(s.Title)))
		}
		head := fmt.Sprintf("%s: %d %s", thTrimDot(t.Title), len(lines), ruPlural(len(lines), "шаг", "шага", "шагов"))
		if tm := thClause(t.Time); thHasDigit(tm) && utf8.RuneCountInString(head+", "+tm) <= thValueFirstMax {
			head += ", " + tm
		}
		ex := ""
		for _, s := range t.Steps {
			if thHasDigit(s.Example) {
				if x := thSentences(s.Example, 170); x != "" {
					ex = "Пример: " + x
					break
				}
			}
		}
		tpl := ""
		if t.Template != "" && thTrimDot(t.Template) != thValueMagnetFor(t.Organ).title() {
			tpl = "Шаблон «" + thTrimDot(t.Template) + "» лежит в библиотеке инструментов."
		}
		body := thFit(head, lines, 3, []string{ex, tpl}, max)
		// the head counted the steps: say how many are left
		got := len(thListRe.FindAllString(body, -1))
		if got != len(lines) {
			body = strings.Replace(body, fmt.Sprintf(": %d %s", len(lines), ruPlural(len(lines), "шаг", "шага", "шагов")),
				fmt.Sprintf(": %d %s", got, ruPlural(got, "шаг", "шага", "шагов")), 1)
		}
		return body
	case "v_metrics":
		if t == nil {
			return ""
		}
		var lines []string
		for _, m := range t.Metrics {
			if thHasDigit(m) && len(lines) < 4 {
				lines = append(lines, "- "+thTrimDot(m))
			}
		}
		if len(lines) < 3 {
			return ""
		}
		head := fmt.Sprintf("%s: %d %s, по которым видно, что работает", thTrimDot(t.Title), len(lines), ruPlural(len(lines), "цифра", "цифры", "цифр"))
		if utf8.RuneCountInString(head) > thValueFirstMax {
			head = fmt.Sprintf("%s: %d %s контроля", thTrimDot(t.Title), len(lines), ruPlural(len(lines), "цифра", "цифры", "цифр"))
		}
		first := ""
		if r := []rune(thTrimDot(t.Steps[0].Title)); len(r) > 0 {
			first = "С чего начать: " + strings.ToLower(string(r[:1])) + string(r[1:]) + "."
		}
		tm := ""
		if thHasDigit(t.Time) {
			tm = "Время: " + thTrimDot(t.Time) + "."
		}
		return thFit(head, lines, 3, []string{first, tm}, max)
	case "v_check":
		if d == nil || len(d.Signs) < 4 {
			return ""
		}
		var lines []string
		for _, s := range d.Signs {
			if len(lines) >= 5 {
				break
			}
			s = thTrimDot(s)
			if utf8.RuneCountInString(s) > 110 {
				continue
			}
			lines = append(lines, "- "+s)
		}
		if len(lines) < 4 {
			return ""
		}
		head := fmt.Sprintf("%s? Проверь себя по %d %s", thTrimDot(d.Title), len(lines), ruPlural(len(lines), "признаку", "признакам", "признакам"))
		step := ""
		if len(d.FirstSteps) > 0 {
			if x := thSentences(d.FirstSteps[0], 170); x != "" {
				step = "Совпало 2 и больше: " + strings.ToLower(string([]rune(x)[:1])) + string([]rune(x)[1:])
				if !strings.HasSuffix(step, ".") {
					step += "."
				}
			} else if r := []rune(thTrimDot(d.FirstSteps[0])); len(r) > 0 && len(r) <= 170 {
				step = "Совпало 2 и больше: " + strings.ToLower(string(r[:1])) + string(r[1:]) + "."
			}
		}
		body := thFit(head, lines, 4, []string{step}, max)
		if got := len(thListRe.FindAllString(body, -1)); got != len(lines) {
			body = strings.Replace(body, fmt.Sprintf("по %d %s", len(lines), ruPlural(len(lines), "признаку", "признакам", "признакам")),
				fmt.Sprintf("по %d %s", got, ruPlural(got, "признаку", "признакам", "признакам")), 1)
		}
		return body
	case "v_calc":
		if d == nil || !(strings.Contains(d.Cost, "₸") || strings.Contains(d.Cost, "%")) {
			return ""
		}
		head := "Во сколько обходится «" + thTrimDot(d.Title) + "»?"
		if utf8.RuneCountInString(head) > thValueFirstMax {
			return ""
		}
		cost := strings.TrimSpace(d.Cost)
		if cost == "" || !thHasDigit(cost) {
			return ""
		}
		step := ""
		if len(d.FirstSteps) > 0 {
			if r := []rune(thTrimDot(d.FirstSteps[0])); len(r) > 0 && len(r) <= 160 {
				step = "Первый шаг: " + strings.ToLower(string(r[:1])) + string(r[1:]) + "."
			}
		}
		// the cost text as sentences, so a shorter one still fits with the step
		sents := []string{}
		for _, s := range thSentRe.FindAllString(cost, -1) {
			sents = append(sents, strings.TrimSpace(s))
		}
		if len(sents) == 0 {
			sents = []string{cost}
		}
		all := len(sents)
		for len(sents) > 1 && utf8.RuneCountInString(head+"\n\n"+strings.Join(sents, " ")+"\n\n"+step) > max {
			sents = sents[:len(sents)-1]
		}
		// a cut list («трижды… Первое… Второе…» without the third) reads as unfinished
		if len(sents) < all {
			for len(sents) > 1 && thEnumNextRe.MatchString(sents[len(sents)-1]) {
				sents = sents[:len(sents)-1]
			}
			if thEnumRe.MatchString(strings.Join(sents, " ")) {
				return ""
			}
		}
		body := head + "\n\n" + strings.Join(sents, " ")
		if !strings.ContainsAny(strings.Join(sents, " "), "₸%") { // a calculation shows money or a share
			return ""
		}
		if step != "" && utf8.RuneCountInString(body+"\n\n"+step) <= max {
			body += "\n\n" + step
		}
		return body
	}
	return ""
}

// thValueCand is one «польза» post ready for a slot.
type thValueCand struct {
	Src, Root, Organ, Title, Format, Body, Text string
	Magnet                                      *leadMagnet
}

// valueBodyMax: the body's room so that it and the link line fit one
// Threads post (500 signs) and the old gate's 430.
func valueBodyMax(m *leadMagnet) int {
	room := threadsMaxText - 2 - utf8.RuneCountInString(thValueCTA(m))
	if room > threadsBodyMax {
		room = threadsBodyMax
	}
	return room
}

// buildValue: the text of format from one tool or diagnosis, through both gates.
func buildValue(format string, t *thVTool, d *thVDiag) (*thValueCand, []string) {
	organ, id, title := "", "", ""
	if t != nil {
		organ, id, title = t.Organ, t.ID, t.Title
	} else if d != nil {
		organ, id, title = d.Organ, d.ID, d.Title
	}
	m := thValueMagnetFor(organ)
	body := threadsClean(composeValue(format, t, d, valueBodyMax(m)))
	if body == "" {
		return nil, []string{"материал не подходит формату"}
	}
	full := body + "\n\n" + thValueCTA(m)
	probs := append(threadsQuality(body, full, nil), threadsValueProblems(body)...)
	if len(probs) > 0 {
		return nil, probs
	}
	return &thValueCand{Src: "val:" + format + ":" + id, Root: id, Organ: organ, Title: title, Format: format, Body: body, Text: full, Magnet: m}, nil
}

// pickValue: a «польза» post for each format: material not used in 30 days,
// a different organ than the post before, the organs used least this week
// first; a format whose material ran out tries the other formats.
func pickValue(formats []string, mem *threadsMemory, key, prevOrgan string) []*thValueCand {
	tools, diags := threadsValuePool()
	out := make([]*thValueCand, len(formats))
	dayRoot := map[string]bool{}
	dayOrgan := map[string]int{}
	for i, f := range formats {
		order := []string{f}
		for _, x := range threadsValueFormats {
			if x.ID != f {
				order = append(order, x.ID)
			}
		}
		for _, fid := range order {
			type cand struct {
				t  *thVTool
				d  *thVDiag
				sc [4]int
			}
			var cs []cand
			add := func(t *thVTool, d *thVDiag, id, organ string) {
				if dayRoot[id] {
					return
				}
				if _, used := mem.root[id]; used {
					return
				}
				same := 0
				if organ == prevOrgan {
					same = 1
				}
				cs = append(cs, cand{t, d, [4]int{same, dayOrgan[organ]*100 + mem.organ[organ], 0, hashN(key+fid+id, 1_000_000)}})
			}
			if fid == "v_steps" || fid == "v_metrics" {
				for _, t := range tools {
					add(t, nil, t.ID, t.Organ)
				}
			} else {
				for _, d := range diags {
					add(nil, d, d.ID, d.Organ)
				}
			}
			sort.Slice(cs, func(a, b int) bool {
				for k := range cs[a].sc {
					if cs[a].sc[k] != cs[b].sc[k] {
						return cs[a].sc[k] < cs[b].sc[k]
					}
				}
				return false
			})
			for _, c := range cs {
				v, _ := buildValue(fid, c.t, c.d)
				if v == nil || mem.similar(v.Body) {
					continue
				}
				out[i] = v
				break
			}
			if out[i] != nil {
				break
			}
		}
		if v := out[i]; v != nil {
			dayRoot[v.Root] = true
			dayOrgan[v.Organ]++
			prevOrgan = v.Organ
			mem.root[v.Root] = time.Now()
			mem.remember(v.Body)
		}
	}
	return out
}

// thValueRoot: the library id of a «польза» post's source (val:v_steps:seed_tl_0).
func thValueRoot(src string) string {
	if !strings.HasPrefix(src, "val:") {
		return ""
	}
	if i := strings.LastIndex(src, ":"); i > 3 {
		return src[i+1:]
	}
	return ""
}

// valueItem: the queue item of a «польза» post (without its slot yet).
func valueItem(v *thValueCand) *contentItem {
	return &contentItem{contentItemData: contentItemData{Src: v.Src, Organ: v.Organ, Title: v.Title, Text: v.Text, Format: v.Format,
		Gen: "val", CTA: true, Magnet: v.Magnet.ID}}
}

// ── Replace and approve (the SMM view's 7-day plan) ──

var errThreadsNotOpen = errors.New("Пост уже ушёл или вышел: заменить можно только запланированный")

// ReplaceThreadsPost gives a planned Threads post another text of its kind:
// a magnet another magnet, «польза» another «польза» post (another format
// when its own ran out). The slot, its time and its link code stay.
func (e *ContentEngine) ReplaceThreadsPost(ctx context.Context, id string) (*contentItem, error) {
	d, _, err := e.load(ctx)
	if err != nil {
		return nil, err
	}
	now := e.now()
	it := findContent(d, id)
	if it == nil || it.Channel != "threads" {
		return nil, errContentNotFound
	}
	if it.Status != "planned" && it.Status != "approved" {
		return nil, errThreadsNotOpen
	}
	s, _ := e.state(ctx)
	mem := e.threadsMemory(d, s, now)
	key := contentDay(now) + "/replace/" + id + "/" + strconv.FormatInt(now.UnixNano(), 36)
	var nx *contentItem
	if it.Format == "magnet" {
		// the day's other magnets are not repeated
		dayMg := map[string]bool{it.Magnet: true}
		for _, x := range d.Queue {
			if x.Channel == "threads" && x.day() == it.day() && x.Magnet != "" && x.Format == "magnet" {
				dayMg[x.Magnet] = true
			}
		}
		at, ok := parseContentAt(it.At)
		if !ok || at.Before(now) {
			at = now
		}
		for _, c := range pickMagnets(len(leadMagnetList), mem, key, at) {
			if !dayMg[c.Magnet] {
				nx = c
				break
			}
		}
	} else {
		f := it.Format
		if !IsThreadsValue(f) {
			f = threadsValueFormats[hashN(key, len(threadsValueFormats))].ID
		}
		mem.remember(it.Text)
		if r := thValueRoot(it.Src); r != "" {
			mem.root[r] = now
		}
		if v := pickValue([]string{f}, mem, key, ""); v[0] != nil {
			nx = valueItem(v[0])
		}
	}
	if nx == nil {
		return nil, errors.New("Другого поста этого вида пока нет")
	}
	var out *contentItem
	_, err = e.update(ctx, func(d *contentDoc) bool {
		x := findContent(d, id)
		if x == nil || (x.Status != "planned" && x.Status != "approved") {
			return false
		}
		x.Src, x.V, x.Organ, x.Title, x.Rubric, x.Text, x.Parts = nx.Src, nx.V, nx.Organ, nx.Title, "", nx.Text, nil
		x.Format, x.Gen, x.CTA, x.Edited, x.Magnet = nx.Format, nx.Gen, true, false, nx.Magnet
		x.Status, x.Error = "planned", ""
		if e.stCached(d).manual {
			manualize(x)
		} else {
			magnetize(x)
		}
		cp := *x
		out = &cp
		return true
	})
	if err != nil {
		return nil, err
	}
	if out == nil {
		return nil, errThreadsNotOpen
	}
	log.Printf("threads: replaced %s → %s (%s): %s", id, out.Src, out.Format, content.FirstLine(out.Text, 90))
	return out, nil
}

// ApproveThreadsPost marks a planned post approved (on) or back to planned.
func (e *ContentEngine) ApproveThreadsPost(ctx context.Context, id, by string, on bool) (*contentItem, error) {
	var out *contentItem
	_, err := e.update(ctx, func(d *contentDoc) bool {
		x := findContent(d, id)
		if x == nil || x.Channel != "threads" || (x.Status != "planned" && x.Status != "approved") {
			return false
		}
		if on {
			x.Status, x.ApprovedBy = "approved", by
		} else {
			x.Status = "planned"
		}
		cp := *x
		out = &cp
		return true
	})
	if err != nil {
		return nil, err
	}
	if out == nil {
		return nil, errThreadsNotOpen
	}
	return out, nil
}

// ThreadsWeek: the next 7 days' Threads posts for the owner's review.
func (e *ContentEngine) ThreadsWeek(ctx context.Context) (map[string]any, error) {
	d, _, err := e.load(ctx)
	if err != nil {
		return nil, err
	}
	st := e.settings(ctx, d)
	today := dayStart(e.now())
	var days []map[string]any
	for i := 0; i < threadsAhead; i++ {
		day := today.AddDate(0, 0, i)
		key := contentDay(day)
		var posts []map[string]any
		for _, it := range d.Queue {
			if it.Channel != "threads" || it.day() != key {
				continue
			}
			kind := "other"
			switch {
			case it.Format == "magnet":
				kind = "magnet"
			case IsThreadsValue(it.Format):
				kind = "value"
			}
			p := map[string]any{"id": it.ID, "at": it.At, "status": it.Status, "format": it.Format, "formatName": ThreadsFormatName(it.Format),
				"kind": kind, "text": it.Text, "organ": it.Organ, "title": it.Title, "edited": it.Edited}
			if m := leadMagnetByID[it.Magnet]; m != nil {
				p["magnet"] = m.title()
			}
			if kind == "value" {
				body := it.Text
				if i := strings.LastIndex(body, "\n\n"); i > 0 && mgURLRe.MatchString(body[i:]) {
					body = body[:i]
				}
				n, what := thSpecifics(body)
				p["specifics"] = n
				p["specificsWhat"] = what
			}
			posts = append(posts, p)
		}
		sort.SliceStable(posts, func(a, b int) bool { return posts[a]["at"].(string) < posts[b]["at"].(string) })
		days = append(days, map[string]any{"day": day.Format("2006-01-02"), "on": st.Channels.Threads.On && st.dayOn("threads", day), "posts": posts})
	}
	return map[string]any{"days": days, "perDay": st.threadsPerDay(), "manual": st.manual, "policy": "value", "magnetPct": threadsValueMgPct}, nil
}

// logBatch: the day's posts in the log, a line each (deploy check and the
// owner's questions «что пришло»).
func logBatch(key string, items []*contentItem) {
	for _, it := range items {
		log.Printf("threads: batch %s %s [%s] %s: %s", key, strings.TrimPrefix(it.ID, "th-"), it.Format, it.Src, content.FirstLine(it.Text, 90))
	}
}
