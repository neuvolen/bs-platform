package http

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"hash/fnv"
	"log"
	"net"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/bnursik/business_surgery_backend/internal/ai"
	"github.com/bnursik/business_surgery_backend/internal/content"
)

// Threads через пользу: 16 постов в день (настройка 1-25) с 08:00 до 22:00
// по Алматы, у каждого поста своё время, не ровно по часам.
//
// Пачку дня сервер собирает заранее (в 06:30, settings.channels.threads.buildAt),
// чтобы команда видела и правила её в календаре Контент-завода. Материал:
// богатая библиотека (инструменты и диагнозы: шаг, ошибка, метрика, симптом,
// расчёт), один конкретный факт на пост. Форматы чередуются: совет,
// мини-чек-лист, цифры в ₸, миф и факт, вопрос собственнику, мини-кейс,
// симптом, серия (2-4 связанных поста ответами друг на друга). Примерно
// каждый 4-й пост заканчивается мягкой ссылкой на 99 чек-листов в боте
// (start=th_99, лиды видны в CRM как «Threads: 99 чек-листов»).
//
// Повторы: тема (пункт библиотеки) не берётся снова 30 дней, похожий текст
// (по словам) и тот же зачин отбрасываются. Перед публикацией каждый пост
// проходит проверку качества (threadsQuality). ИИ не ответил или пост не
// прошёл проверку: слот закрывает готовый пост из библиотеки, иначе слот
// пропускается. Владельцу об этом не пишется.
//
// Публикация: один пост Threads за минуту, не чаще раза в 2 минуты, не больше
// 240 за сутки (лимит API 250). Ошибка токена или лимита ставит Threads на
// паузу, пост повторяется позже, владелец узнаёт об этом один раз в день.

const (
	threadsPerDayDef  = 16
	threadsPerDayMax  = 25
	threadsQuota24h   = 250 // Threads API: posts per 24 hours
	threadsQuotaSafe  = 240 // the server stops here, the rest is left for manual posts
	threadsDedupe     = 30 * 24 * time.Hour
	threadsLateBatch  = 90 * time.Minute // a batch post this late is skipped, not published in a heap
	threadsGap        = 2 * time.Minute  // between two Threads posts
	threadsCTAParam   = "th_99"
	threadsBodyMax    = 430 // the AI text; the CTA line comes on top
	threadsRecentKeep = 45  // days in the dedupe log
)

// contentThreadsPerDay: the default cadence (tests of the classic plan set 1).
var contentThreadsPerDay = threadsPerDayDef

// ── Settings ──

func (s contentSettings) threadsPerDay() int {
	if s.manual {
		return s.manualPerDay()
	}
	n := s.Channels.Threads.PerDay
	if n <= 0 {
		n = contentThreadsPerDay
	}
	if n < 1 {
		n = 1
	}
	if n > threadsPerDayMax {
		n = threadsPerDayMax
	}
	return n
}

// threadsBatch: Threads posts several times a day from the daily batch.
func (s contentSettings) threadsBatch() bool { return s.manual || s.threadsPerDay() > 1 }

func hmMinutes(v string, def int) int {
	h, m, ok := strings.Cut(strings.TrimSpace(v), ":")
	hh, e1 := strconv.Atoi(h)
	mm, e2 := strconv.Atoi(m)
	if !ok || e1 != nil || e2 != nil || hh < 0 || hh > 23 || mm < 0 || mm > 59 {
		return def
	}
	return hh*60 + mm
}

// threadsWindow: the minutes of the day the posts go out in (08:00-22:00).
func (s contentSettings) threadsWindow() (int, int) {
	from, to := hmMinutes(s.Channels.Threads.From, 8*60), hmMinutes(s.Channels.Threads.To, 22*60)
	if to-from < 60 {
		return 8 * 60, 22 * 60
	}
	return from, to
}

// threadsBuildAt: when the day's batch is made (06:30).
func (s contentSettings) threadsBuildAt() int {
	return hmMinutes(s.Channels.Threads.BuildAt, 6*60+30)
}

// ── Times ──

func hashN(s string, n int) int {
	h := fnv.New32a()
	_, _ = h.Write([]byte(s))
	return int(h.Sum32() % uint32(n))
}

func dayStart(t time.Time) time.Time {
	a := t.In(almaty)
	return time.Date(a.Year(), a.Month(), a.Day(), 0, 0, 0, 0, almaty)
}

// threadsSlots: n times spread over the window, one in each equal segment,
// jittered by the day (the same day gives the same times), never on :00,
// :15, :30 or :45.
func threadsSlots(day time.Time, n, from, to int) []time.Time {
	if n < 1 {
		return nil
	}
	base := dayStart(day)
	key := contentDay(base)
	seg := float64(to-from) / float64(n)
	out := make([]time.Time, 0, n)
	prev := from - 10
	for i := 0; i < n; i++ {
		r := float64(hashN(key+"/"+strconv.Itoa(i), 1000)) / 1000
		m := int(float64(from) + float64(i)*seg + seg*(0.15+0.7*r))
		if m < prev+6 {
			m = prev + 6
		}
		for m%15 == 0 {
			m++
		}
		if m >= to {
			m = to - 1
			for m%15 == 0 {
				m--
			}
		}
		prev = m
		out = append(out, base.Add(time.Duration(m)*time.Minute))
	}
	return out
}

// ── Formats ──

type threadsFormat struct {
	ID, Name, Brief string
	Weight          int
	Kinds           []string // the seed kinds it is made from
	Length          []string // short, medium, long by turns
}

var threadsFormats = []threadsFormat{
	{"tip", "Совет", "Один конкретный шаг, который собственник может сделать сегодня: что сделать, как, сколько времени займёт и что изменится.", 2,
		[]string{"step", "metric", "first"}, []string{"short", "medium"}},
	{"checklist", "Мини-чек-лист", "Первая строка: крючок. Дальше 3-5 коротких пунктов, каждый с новой строки и с «- » в начале. Читатель за минуту проверяет себя.", 2,
		[]string{"signs", "when", "first"}, []string{"medium", "long"}},
	{"numbers", "Цифры в ₸", "Расчёт на живом примере в тенге: исходные цифры, вычисление, вывод одной фразой. Цифры только из материала или явный пример «допустим, выручка 10 млн ₸».", 2,
		[]string{"cost", "example", "hero"}, []string{"medium", "long"}},
	{"myth", "Миф и факт", "Первая строка: убеждение, в которое верят собственники. Дальше: как на самом деле и что сделать вместо этого.", 2,
		[]string{"mistake", "cause"}, []string{"medium", "short"}},
	{"question", "Вопрос собственнику", "Один неудобный вопрос, на который честно ответить больно, и одна-две фразы, почему ответ важен. Без ответа за читателя.", 1,
		[]string{"question"}, []string{"short"}},
	{"case", "Мини-кейс", "Кто (имя, бизнес, город строго из материала), что было в цифрах, что сделали, что стало. 3-5 предложений.", 2,
		[]string{"case", "hero"}, []string{"long", "medium"}},
	{"symptom", "Симптом", "Признак, который собственник узнает у себя, почему так происходит и первый шаг. Начни с самой ситуации, а не с названия проблемы.", 2,
		[]string{"sign", "risk"}, []string{"short", "medium"}},
	{"series", "Серия", "Серия из 3-4 связанных постов, они выйдут цепочкой ответов. text: первый пост с крючком и обещанием, parts: продолжения, по одному шагу в каждом, до 400 знаков каждое.", 1,
		[]string{"tool"}, []string{"medium"}},
	// R32e (план маркетинга a1): 5 рубрик Threads: диагностика (симптом, чек-лист, вопрос),
	// мнение, кейсы (кейс, цифры), ошибки (миф и факт), закулисье
	{"opinion", "Мнение", "Позиция основателей Business Surgery по спорному вопросу управления: тезис первой строкой, 2-3 аргумента из материала, вывод одной фразой. Без морали и без «мы лучшие».", 1,
		[]string{"mistake", "cause", "risk"}, []string{"medium", "short"}},
	{"backstage", "Закулисье", "Как это выглядит изнутри разбора Business Surgery: по какому признаку трекеры видят эту проблему, какой вопрос задают собственнику первым и с чего начинают лечение. Без выдуманных имён и цифр.", 1,
		[]string{"signs", "sign", "question", "first"}, []string{"medium"}},
}

var threadsFormatByID = func() map[string]*threadsFormat {
	m := map[string]*threadsFormat{}
	for i := range threadsFormats {
		m[threadsFormats[i].ID] = &threadsFormats[i]
	}
	return m
}()

// ThreadsFormatName: how a format is called on the platform.
func ThreadsFormatName(id string) string {
	if f := threadsFormatByID[id]; f != nil {
		return f.Name
	}
	if id == "library" {
		return "Из библиотеки"
	}
	if id == "magnet" {
		return "Лид-магнит"
	}
	return ""
}

var threadsLengths = map[string][2]int{"short": {100, 180}, "medium": {180, 300}, "long": {300, 400}}

// threadsDayFormats: the formats of a day's n slots in proportion to their
// weights, never the same format twice in a row, the order varies by day.
func threadsDayFormats(n int, key string) []string {
	total := 0
	for _, f := range threadsFormats {
		total += f.Weight
	}
	type alloc struct {
		id   string
		n    int
		frac float64
	}
	var al []alloc
	sum := 0
	for _, f := range threadsFormats {
		x := float64(f.Weight*n) / float64(total)
		al = append(al, alloc{f.ID, int(x), x - float64(int(x))})
		sum += int(x)
	}
	order := make([]int, len(al))
	for i := range order {
		order[i] = i
	}
	sort.SliceStable(order, func(a, b int) bool {
		if al[order[a]].frac != al[order[b]].frac {
			return al[order[a]].frac > al[order[b]].frac
		}
		return hashN(key+al[order[a]].id, 997) < hashN(key+al[order[b]].id, 997)
	})
	for k := 0; sum < n; k++ {
		al[order[k%len(order)]].n++
		sum++
	}
	left := map[string]int{}
	for _, a := range al {
		left[a.id] = a.n
	}
	if left["series"] > 1 { // one series a day is enough
		left["tip"] += left["series"] - 1
		left["series"] = 1
	}
	out := make([]string, 0, n)
	prev := ""
	for len(out) < n {
		best := ""
		for _, f := range threadsFormats {
			c := left[f.ID]
			if c == 0 || (f.ID == prev && len(out) < n-1) {
				continue
			}
			if best == "" || c > left[best] || (c == left[best] && hashN(key+"/"+strconv.Itoa(len(out))+f.ID, 997) < hashN(key+"/"+strconv.Itoa(len(out))+best, 997)) {
				best = f.ID
			}
		}
		if best == "" {
			for _, f := range threadsFormats {
				if left[f.ID] > 0 {
					best = f.ID
					break
				}
			}
		}
		left[best]--
		out = append(out, best)
		prev = best
	}
	return out
}

// threadsCTASlots: which of n slots end with the link to the 99 checklists:
// about one in four, never the first post, never a question post.
func threadsCTASlots(n int, formats []string) []bool {
	out := make([]bool, n)
	if n < 2 {
		return out
	}
	c := (n + 2) / 4
	for k := 0; k < c; k++ {
		i := (k+1)*n/c - 1
		if i < 1 {
			i = 1
		}
		for j := i; j >= 1; j-- { // a question asks for comments, not for a click
			if !out[j] && (j >= len(formats) || formats[j] != "question") {
				i = j
				break
			}
		}
		out[i] = true
	}
	return out
}

var threadsCTALines = []string{
	"Ещё 99 чек-листов для собственника лежат в боте, бесплатно: t.me/bsurgery_bot?start=" + threadsCTAParam,
	"Таких чек-листов у нас 99, забрать бесплатно: t.me/bsurgery_bot?start=" + threadsCTAParam,
	"Если полезно, в боте 99 чек-листов по деньгам, продажам и команде: t.me/bsurgery_bot?start=" + threadsCTAParam,
	"Проверить себя по 99 чек-листам можно в боте, это бесплатно: t.me/bsurgery_bot?start=" + threadsCTAParam,
}

// ── Seeds: one concrete fact of the library per post ──

type threadsSeed struct {
	ID, Root, Organ, Title, Kind, Text, Extra string
}

var (
	thSeedsOnce sync.Once
	thSeeds     []*threadsSeed
)

func cutRunes(s string, n int) string {
	s = strings.TrimSpace(s)
	if r := []rune(s); len(r) > n {
		return strings.TrimSpace(string(r[:n-1])) + "…"
	}
	return s
}

// threadsSeedPool: the rich library's tools and diagnoses cut into seeds.
func threadsSeedPool() []*threadsSeed {
	thSeedsOnce.Do(func() {
		plain, _, _, err := content.LibRich()
		if err != nil {
			log.Printf("threads: rich library: %v", err)
			return
		}
		var lib struct {
			Tools []struct {
				ID, Organ, Title, Subtitle string
				When                       []string `json:"when"`
				Metrics                    []string `json:"metrics"`
				Hero                       *struct {
					Name     string   `json:"name"`
					Business string   `json:"business"`
					City     string   `json:"city"`
					Story    []string `json:"story"`
					Before   []struct{ Label, Value string }
					After    []struct{ Label, Value string }
				} `json:"hero"`
				Steps []struct {
					Title, Do, Example, Mistake string
				} `json:"steps"`
			} `json:"tools"`
			Diag []struct {
				ID, Organ, Title, Subtitle, Desc, Cost, Risk string
				Signs                                        []string `json:"signs"`
				Causes                                       []string `json:"causes"`
				FirstSteps                                   []string `json:"first_steps"`
				Case                                         *struct {
					Name, Business, Story, Result string
				} `json:"case"`
				Test struct {
					Questions []struct {
						Q string `json:"q"`
					} `json:"questions"`
				} `json:"test"`
			} `json:"diag"`
		}
		if err := json.Unmarshal(plain, &lib); err != nil {
			log.Printf("threads: rich library: %v", err)
			return
		}
		add := func(root, organ, title, kind string, i int, text, extra string) {
			if strings.TrimSpace(text) == "" || organ == "" {
				return
			}
			thSeeds = append(thSeeds, &threadsSeed{ID: fmt.Sprintf("%s/%s%d", root, kind, i), Root: root, Organ: organ, Title: title, Kind: kind,
				Text: cutRunes(text, 700), Extra: cutRunes(extra, 500)})
		}
		pair := func(xs []struct{ Label, Value string }) string {
			var p []string
			for _, x := range xs {
				p = append(p, x.Label+": "+x.Value)
			}
			return strings.Join(p, "; ")
		}
		digits := regexp.MustCompile(`\d`)
		for _, t := range lib.Tools {
			for i, s := range t.Steps {
				add(t.ID, t.Organ, t.Title, "step", i, s.Title+". "+s.Do, s.Example)
				if s.Mistake != "" {
					add(t.ID, t.Organ, t.Title, "mistake", i, s.Mistake, s.Title+". "+s.Do)
				}
				if s.Example != "" && digits.MatchString(s.Example) && strings.Contains(s.Example, "₸") {
					add(t.ID, t.Organ, t.Title, "example", i, s.Example, s.Title)
				}
			}
			for i, m := range t.Metrics {
				add(t.ID, t.Organ, t.Title, "metric", i, m, t.Subtitle)
			}
			if len(t.When) >= 3 {
				add(t.ID, t.Organ, t.Title, "when", 0, "Признаки, что пора: "+strings.Join(t.When, "; "), t.Subtitle)
			}
			if h := t.Hero; h != nil && len(h.Story) > 0 {
				add(t.ID, t.Organ, t.Title, "hero", 0, h.Name+", "+h.Business+", "+h.City+". "+strings.Join(h.Story, " "),
					"Было: "+pair(h.Before)+". Стало: "+pair(h.After))
			}
			if len(t.Steps) >= 3 {
				var b strings.Builder
				b.WriteString(t.Title + ". " + t.Subtitle + "\n")
				for i, s := range t.Steps {
					if i >= 4 {
						break
					}
					fmt.Fprintf(&b, "%d) %s. %s\n", i+1, s.Title, cutRunes(s.Do, 220))
				}
				thSeeds = append(thSeeds, &threadsSeed{ID: t.ID + "/tool0", Root: t.ID, Organ: t.Organ, Title: t.Title, Kind: "tool", Text: b.String()})
			}
		}
		for _, d := range lib.Diag {
			for i, s := range d.Signs {
				add(d.ID, d.Organ, d.Title, "sign", i, s, d.Subtitle+". "+firstSentence(d.Desc))
			}
			if len(d.Signs) >= 3 {
				add(d.ID, d.Organ, d.Title, "signs", 0, strings.Join(d.Signs, "; "), d.Subtitle)
			}
			for i, c := range d.Causes {
				add(d.ID, d.Organ, d.Title, "cause", i, c, d.Subtitle)
			}
			add(d.ID, d.Organ, d.Title, "cost", 0, d.Cost, d.Subtitle)
			add(d.ID, d.Organ, d.Title, "risk", 0, d.Risk, d.Subtitle)
			if len(d.FirstSteps) > 0 {
				add(d.ID, d.Organ, d.Title, "first", 0, strings.Join(d.FirstSteps, "; "), d.Subtitle)
			}
			if c := d.Case; c != nil && c.Story != "" {
				add(d.ID, d.Organ, d.Title, "case", 0, c.Name+", "+c.Business+". "+c.Story, c.Result)
			}
			for i, q := range d.Test.Questions {
				add(d.ID, d.Organ, d.Title, "question", i, q.Q, d.Title+": "+d.Subtitle)
			}
		}
	})
	return thSeeds
}

func firstSentence(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexAny(s, ".!?"); i > 0 {
		return s[:i+1]
	}
	return s
}

// ── The day's history for dedupe ──

type threadsSig struct {
	Day   string `json:"d"`
	Src   string `json:"s"`
	Root  string `json:"r,omitempty"`
	Organ string `json:"o,omitempty"`
	First string `json:"f,omitempty"`
	Keys  string `json:"k,omitempty"`
}

type threadsDayState struct {
	N       int    `json:"n"`       // the cadence the batch was built for
	Added   int    `json:"added"`   // posts the last build added
	Missing int    `json:"missing"` // future slots left empty
	Tries   int    `json:"tries"`   // builds with the AI failing
	At      string `json:"at"`
	AI      string `json:"ai,omitempty"`   // ok or the AI's error
	Plan    int    `json:"plan,omitempty"` // R76: the plan's revision (threadsPlanRev)
}

// threadsPlanRev: a day built before the magnets (R76) is re-planned once:
// its untouched future posts give way to the new plan.
const threadsPlanRev = 76

var thWordRe = regexp.MustCompile(`[\p{L}\p{N}]+`)

var thStop = map[string]bool{"чтобы": true, "когда": true, "если": true, "потом": true, "который": true, "которые": true, "этого": true, "этом": true,
	"тебя": true, "тебе": true, "твой": true, "твоя": true, "твоё": true, "твои": true, "только": true, "можно": true, "нужно": true, "просто": true,
	"будет": true, "есть": true, "даже": true, "один": true, "одна": true, "всех": true, "всего": true, "сейчас": true, "здесь": true}

// thKeys: the post's word stems (first 5 letters of words of 4+ letters).
func thKeys(s string) map[string]bool {
	out := map[string]bool{}
	s = startLinkStrip(s)
	for _, w := range thWordRe.FindAllString(strings.ToLower(s), -1) {
		r := []rune(w)
		if len(r) < 4 || thStop[w] {
			continue
		}
		if len(r) > 5 {
			r = r[:5]
		}
		out[string(r)] = true
	}
	return out
}

func keysString(k map[string]bool) string {
	var l []string
	for x := range k {
		l = append(l, x)
	}
	sort.Strings(l)
	if len(l) > 60 {
		l = l[:60]
	}
	return strings.Join(l, " ")
}

func keysOf(s string) map[string]bool {
	out := map[string]bool{}
	for _, x := range strings.Fields(s) {
		out[x] = true
	}
	return out
}

func jaccard(a, b map[string]bool) float64 {
	if len(a) == 0 || len(b) == 0 {
		return 0
	}
	n := 0
	for k := range a {
		if b[k] {
			n++
		}
	}
	return float64(n) / float64(len(a)+len(b)-n)
}

var thLinkRe = regexp.MustCompile(`(?:https?://)?t\.me/\S+`)

func startLinkStrip(s string) string { return thLinkRe.ReplaceAllString(s, "") }

// thFirst: the first line, normalised, for «the same opening».
func thFirst(s string) string {
	f := strings.ToLower(content.FirstLine(s, 60))
	return strings.Join(thWordRe.FindAllString(f, -1), " ")
}

// threadsMemory: what Threads said in the last 30 days.
type threadsMemory struct {
	src   map[string]time.Time // seed or library source → last use
	root  map[string]time.Time
	first map[string]bool
	keys  []map[string]bool
	organ map[string]int // organ uses in the last 7 days
}

func (e *ContentEngine) threadsMemory(d *contentDoc, s contentState, now time.Time) *threadsMemory {
	m := &threadsMemory{src: map[string]time.Time{}, root: map[string]time.Time{}, first: map[string]bool{}, organ: map[string]int{}}
	since := now.Add(-threadsDedupe)
	note := func(at time.Time, src, root, organ, first string, keys map[string]bool) {
		if at.Before(since) {
			return
		}
		if src != "" && at.After(m.src[src]) {
			m.src[src] = at
		}
		if root != "" && at.After(m.root[root]) {
			m.root[root] = at
		}
		if organ != "" && at.After(now.Add(-7*24*time.Hour)) {
			m.organ[organ]++
		}
		if first != "" {
			m.first[first] = true
		}
		if len(keys) > 0 {
			m.keys = append(m.keys, keys)
		}
	}
	for _, sg := range s.Recent {
		t, err := time.ParseInLocation("20060102", sg.Day, almaty)
		if err != nil {
			continue
		}
		note(t.Add(12*time.Hour), sg.Src, sg.Root, sg.Organ, sg.First, keysOf(sg.Keys))
	}
	for _, list := range [][]*contentItem{d.History, d.Queue} {
		for _, it := range list {
			if it.Channel != "threads" || it.Status == "skipped" || it.Status == "failed" {
				continue
			}
			at, ok := parseContentAt(it.At)
			if !ok {
				continue
			}
			src := it.Src
			if it.Gen == "lib" || (it.Gen == "" && src != "") {
				src = fmt.Sprintf("lib:%s/%d", it.Src, it.V)
			}
			root := ""
			if it.Gen == "ai" {
				root, _, _ = strings.Cut(it.Src, "/")
			}
			txt := it.Text + " " + strings.Join(it.Parts, " ")
			note(at, src, root, it.Organ, thFirst(it.Text), thKeys(txt))
		}
	}
	return m
}

// similar: the text repeats a post of the last 30 days (or of the batch).
func (m *threadsMemory) similar(text string) bool {
	if f := thFirst(text); f != "" && m.first[f] {
		return true
	}
	k := thKeys(text)
	for _, o := range m.keys {
		if jaccard(k, o) >= 0.5 {
			return true
		}
	}
	return false
}

func (m *threadsMemory) remember(text string) {
	if f := thFirst(text); f != "" {
		m.first[f] = true
	}
	m.keys = append(m.keys, thKeys(text))
}

// thPersonal: the owner's own organs (not the business's): one post a day at most.
var thPersonal = map[string]bool{"Мышление": true, "Энергия": true, "Цели": true, "Окружение": true}

// pickSeeds: a seed per slot, not used in 30 days, a different organ than
// the post before, one item of the library at most once a day, the organs
// used least this week first.
func pickSeeds(pool []*threadsSeed, formats []string, mem *threadsMemory, key, prevOrgan string) []*threadsSeed {
	out := make([]*threadsSeed, len(formats))
	dayRoot := map[string]bool{}
	dayOrgan := map[string]int{}
	personal := 0
	for i, fid := range formats {
		f := threadsFormatByID[fid]
		if f == nil {
			continue
		}
		kinds := map[string]bool{}
		for _, k := range f.Kinds {
			kinds[k] = true
		}
		var best *threadsSeed
		var bestScore [5]int
		for _, s := range pool {
			if !kinds[s.Kind] || dayRoot[s.Root] || (thPersonal[s.Organ] && personal > 0) {
				continue
			}
			if _, used := mem.src[s.ID]; used {
				continue
			}
			same := 0
			if s.Organ == prevOrgan {
				same = 1
			}
			recent := 0
			if t, ok := mem.root[s.Root]; ok {
				recent = 1 + int(t.Unix()/86400)
			}
			// a reach rubric takes its own topics first (R58), then the library
			own := 0
			if IsThreadsReach(fid) && s.Kind != f.Kinds[0] {
				own = 1
			}
			sc := [5]int{own, same, dayOrgan[s.Organ]*100 + mem.organ[s.Organ], recent, hashN(key+s.ID, 1_000_000)}
			if best == nil || lessScore(sc, bestScore) {
				best, bestScore = s, sc
			}
		}
		if best == nil {
			continue
		}
		out[i] = best
		if thPersonal[best.Organ] {
			personal++
		}
		dayRoot[best.Root] = true
		dayOrgan[best.Organ]++
		prevOrgan = best.Organ
	}
	return out
}

func lessScore(a, b [5]int) bool {
	for i := range a {
		if a[i] != b[i] {
			return a[i] < b[i]
		}
	}
	return false
}

// ── Quality gate ──

// threadsCliches: the AI's tired phrases ($S/lib2/check2.py and more).
var threadsCliches = []string{"важно отметить", "ключ к успеху", "в современном мире", "исследования показ", "по данным исследован", "согласно исследован",
	"не секрет, что", "давайте разбер", "в заключение", "стоит отметить", "секрет успеха", "волшебн", "лайфхак", "на сегодняшний день",
	"играет важную роль", "играет ключевую роль", "в наше время", "уникальная возможность", "друзья,", "как известно", "залог успеха",
	"в мире бизнеса", "в этой статье", "в этом посте",
	// R76: the brand's banned words
	"хирург", "прокача", "гарантированн", "операция", "операцию", "операции "}

var (
	thStatRe = regexp.MustCompile(`(?i)(по статистике|статистика показ|исследовани|опрос[а-яё]* показ|(?:^|[^а-яё])учён(?:ые|ых)|(?:^|[^а-яё])ученые|` +
		`по данным (?:исследован|опрос|статистик|эксперт|аналитик|агентств|минист|бюро)|согласно данным|` +
		`\d+\s?%\s*(собственник|предпринимател|владельц|компани|бизнес|людей|руководител|стартап|россиян|казахстанц)|` +
		`\d+\s+из\s+10\s+(собственник|предпринимател|владельц|компани|бизнес|руководител|стартап))`)
	thURLRe   = regexp.MustCompile(`(?i)(https?://|www\.|t\.me/|\.kz/|\.com/)`)
	thHashRe  = regexp.MustCompile(`#[\p{L}\p{N}_]+`)
	thRangeRe = regexp.MustCompile(`(\d)\s?[–—]\s?(\d)`)
	thBigNum  = regexp.MustCompile(`\d{4,}`)
	thWeakRe  = regexp.MustCompile(`(?i)^(привет|всем привет|сегодня поговорим|сегодня расскажу|в этом посте|друзья|дорогие)`)
)

// thousands: 10000 → 10 000, 4500 ₸ → 4 500 ₸ (a year like 2026 stays).
func thousands(s string) string {
	idx := thBigNum.FindAllStringIndex(s, -1)
	if len(idx) == 0 {
		return s
	}
	var b strings.Builder
	last := 0
	for _, p := range idx {
		num := s[p[0]:p[1]]
		before := s[:p[0]]
		after := s[p[1]:]
		// part of a decimal (2,5) or a longer token (+77011234567, a code): leave
		if strings.HasSuffix(before, ",") || strings.HasSuffix(before, ".") || strings.HasSuffix(before, "+") || strings.HasPrefix(after, ",") && len(after) > 1 && after[1] >= '0' && after[1] <= '9' {
			continue
		}
		if len(num) == 4 {
			a := strings.TrimLeft(after, "  ")
			money := strings.HasPrefix(a, "₸") || strings.HasPrefix(a, "тг") || strings.HasPrefix(a, "тенге") || strings.HasPrefix(a, "руб")
			if !money {
				continue
			}
		}
		var g []string
		for len(num) > 3 {
			g = append([]string{num[len(num)-3:]}, g...)
			num = num[:len(num)-3]
		}
		g = append([]string{num}, g...)
		b.WriteString(s[last:p[0]])
		b.WriteString(strings.Join(g, " "))
		last = p[1]
	}
	b.WriteString(s[last:])
	return b.String()
}

// threadsClean: what is safe to fix by itself (ranges with dashes, digits,
// spaces). An em dash in a sentence is not fixed: the gate refuses it.
func threadsClean(s string) string {
	s = strings.ReplaceAll(s, "\r", "")
	s = thRangeRe.ReplaceAllString(s, "$1-$2")
	s = thousands(s)
	s = strings.ReplaceAll(s, " ", " ")
	lines := strings.Split(s, "\n")
	for i, l := range lines {
		lines[i] = strings.TrimRight(strings.Join(strings.Fields(l), " "), " ")
	}
	s = strings.Join(lines, "\n")
	for strings.Contains(s, "\n\n\n") {
		s = strings.ReplaceAll(s, "\n\n\n", "\n\n")
	}
	return strings.TrimSpace(s)
}

func emojiCount(s string) int {
	n := 0
	for _, r := range s {
		if r >= 0x1F300 && r <= 0x1FAFF || r >= 0x2600 && r <= 0x27BF {
			n++
		}
	}
	return n
}

// threadsQuality lists why a post must not go out (empty: it may).
// body is the text without the server's CTA line, full is what is published.
func threadsQuality(body, full string, parts []string) []string {
	var p []string
	all := full + "\n" + strings.Join(parts, "\n")
	if strings.ContainsAny(all, "—–") {
		p = append(p, "длинное тире")
	}
	low := strings.ToLower(all)
	for _, c := range threadsCliches {
		if strings.Contains(low, c) {
			p = append(p, "штамп: "+c)
		}
	}
	if m := thStatRe.FindString(all); m != "" {
		p = append(p, "статистика без источника: "+m)
	}
	if thURLRe.MatchString(body + "\n" + strings.Join(parts, "\n")) {
		p = append(p, "ссылка в тексте")
	}
	if n := len(thHashRe.FindAllString(all, -1)); n > 1 {
		p = append(p, fmt.Sprintf("хэштегов: %d", n))
	}
	if n := emojiCount(all); n > 2 {
		p = append(p, fmt.Sprintf("эмодзи: %d", n))
	}
	n := utf8.RuneCountInString(body)
	min := 70
	if len(parts) > 0 {
		min = 40
	}
	if n < min {
		p = append(p, fmt.Sprintf("коротко: %d", n))
	}
	if n > threadsBodyMax {
		p = append(p, fmt.Sprintf("длинно: %d", n))
	}
	if utf8.RuneCountInString(full) > threadsMaxText {
		p = append(p, fmt.Sprintf("больше %d знаков", threadsMaxText))
	}
	for i, x := range parts {
		if c := utf8.RuneCountInString(x); c < 40 || c > threadsMaxText {
			p = append(p, fmt.Sprintf("часть %d: %d знаков", i+2, c))
		}
	}
	first := content.FirstLine(body, 1000)
	if first == "" {
		p = append(p, "нет первой строки")
	} else if utf8.RuneCountInString(first) > 120 {
		p = append(p, "первая строка длиннее 120 знаков")
	}
	if thWeakRe.MatchString(first) {
		p = append(p, "слабый зачин")
	}
	letters, latin := 0, 0
	for _, r := range all {
		if unicode.IsLetter(r) {
			letters++
			if r < 0x250 {
				latin++
			}
		}
	}
	if letters > 0 && latin*100/letters > 25 {
		p = append(p, "не по-русски")
	}
	return p
}

// ── Generation ──

const threadsSystem = `Ты пишешь посты для Threads от имени Business Surgery (BS), клуба собственников бизнеса в Алматы.
Читатель: собственник малого и среднего бизнеса в Казахстане. Задача: дать пользу, после которой пост хочется сохранить и подписаться. Не продавать.

Правила:
1. Русский язык, на «ты», тон практика: конкретно, спокойно, без пафоса и мотивационных лозунгов.
2. Первая строка отдельной строкой, до 80 знаков, это крючок: цифра, неудобный вопрос, узнаваемая ситуация или спорное утверждение.
3. Один пост: одна мысль. Один шаг, одна ошибка, одна метрика или один расчёт. Бери их только из материала к посту.
4. Цифры только из материала или явный пример расчёта («допустим, выручка 10 млн ₸»). Никакой статистики вроде «80% собственников», «исследования показывают», «по данным».
5. Деньги в тенге со знаком ₸, разряды через пробел: 10 000 ₸, 2,5 млн ₸.
6. Не используй тире (— и –) как знак препинания: вместо них точка, двоеточие или запятая. Дефис только в диапазонах: 3-5 дней.
7. Без хэштегов, ссылок и эмодзи. Без штампов: «друзья», «давайте разберёмся», «важно отметить», «ключ к успеху», «в современном мире», «не секрет, что», «лайфхак», «залог успеха».
8. Не зови подписаться и не давай ссылок: где нужно, ссылку на 99 чек-листов сервер добавит сам последней строкой.
9. Держи длину слота: короткий 100-180 знаков, средний 180-300, длинный 300-400. Абсолютный предел 430 знаков.
10. Имена, бизнесы и города только из материала. Не выдумывай клиентов и истории.
11. Не начинай так же, как уже вышедшие посты из списка.
12. Посты одного дня должны звучать по-разному: разные зачины, разный ритм, разная длина.
13. Формат с пометкой «Охват» пишется для широкой аудитории Алматы, не только для собственников: понятно человеку без бизнеса, первая строка цепляет любопытством (цифра, вопрос, спор), в конце вопрос или вывод, которым хочется поделиться. Допущения расчёта называй прямо («допустим», «по модели»). Не называй выручку или прибыль реальных компаний и брендов.

Ответ: только JSON {"posts":[{"n":1,"text":"...","parts":[]}]}. parts заполняй только для серии: продолжения первого поста, 2-3 штуки, каждое до 400 знаков.`

type threadsSlotPlan struct {
	At     time.Time
	Format string
	Length string
	CTA    bool
	Seed   *threadsSeed
}

// threadsPrompt: the day's slots with their material.
func threadsPrompt(day time.Time, slots []threadsSlotPlan, avoid []string) string {
	var b strings.Builder
	a := day.In(almaty)
	fmt.Fprintf(&b, "День: %d %s. Постов: %d.\n", a.Day(), ruMonthsGen[a.Month()-1], len(slots))
	for i, s := range slots {
		f := threadsFormatByID[s.Format]
		lr := threadsLengths[s.Length]
		fmt.Fprintf(&b, "\n### Пост %d · %s · %s (%d-%d знаков)\n", i+1, f.Name, map[string]string{"short": "короткий", "medium": "средний", "long": "длинный"}[s.Length], lr[0], lr[1])
		b.WriteString("Формат: " + f.Brief + "\n")
		if s.CTA {
			b.WriteString("В конце сервер добавит мягкую ссылку на 99 чек-листов: пусть пост подводит к тому, чтобы проверить себя. Не больше 380 знаков.\n")
		}
		if s.Seed != nil {
			fmt.Fprintf(&b, "Орган: %s. Тема: %s\nМатериал: %s\n", s.Seed.Organ, s.Seed.Title, s.Seed.Text)
			if s.Seed.Extra != "" {
				b.WriteString("Подробности: " + s.Seed.Extra + "\n")
			}
		}
	}
	if len(avoid) > 0 {
		b.WriteString("\nУже вышли посты с такими зачинами, не повторяй их:\n")
		for _, x := range avoid {
			b.WriteString("- " + x + "\n")
		}
	}
	b.WriteString("\nВерни JSON с постами 1-" + strconv.Itoa(len(slots)) + ".")
	return b.String()
}

type threadsAIPost struct {
	N     int      `json:"n"`
	Text  string   `json:"text"`
	Parts []string `json:"parts"`
}

func parseThreadsAI(ans string) ([]threadsAIPost, error) {
	js := ai.JSONFrom(ans)
	if js == "" {
		return nil, errors.New("ИИ ответил без JSON")
	}
	var out struct {
		Posts []threadsAIPost `json:"posts"`
	}
	if err := json.Unmarshal([]byte(js), &out); err != nil {
		return nil, fmt.Errorf("ИИ ответил неразборчиво: %w", err)
	}
	if len(out.Posts) == 0 {
		return nil, errors.New("ИИ не прислал постов")
	}
	return out.Posts, nil
}

// libThreadsPost: a ready Threads text of the library for a slot, without
// its own link (a sales link like «Разбор за 50 000 ₸» does not fit a value
// stream); a CTA slot gets the soft line to the 99 checklists instead.
func libThreadsPost(lib []*content.LibItem, mem *threadsMemory, cta bool, ctaLine, key string, used map[string]bool) (*content.LibItem, int, string) {
	type cand struct {
		li *content.LibItem
		v  int
	}
	var cs []cand
	for _, li := range lib {
		for v := 0; v < li.Variants("threads"); v++ {
			src := fmt.Sprintf("lib:%s/%d", li.Src, v)
			if _, ok := mem.src[src]; ok || used[src] {
				continue
			}
			cs = append(cs, cand{li, v})
		}
	}
	sort.SliceStable(cs, func(i, j int) bool {
		return hashN(key+cs[i].li.Src+strconv.Itoa(cs[i].v), 1_000_000) < hashN(key+cs[j].li.Src+strconv.Itoa(cs[j].v), 1_000_000)
	})
	for _, c := range cs {
		tx := strings.TrimSpace(c.li.Threads[c.v%len(c.li.Threads)])
		var keep []string
		for _, l := range strings.Split(tx, "\n") {
			if !thLinkRe.MatchString(l) {
				keep = append(keep, l)
			}
		}
		body := threadsClean(strings.Join(keep, "\n"))
		tx = body
		if cta {
			tx = body + "\n\n" + ctaLine
		}
		if utf8.RuneCountInString(body) < 80 || mem.similar(body) || len(threadsQuality(body, tx, nil)) > 0 {
			continue
		}
		used[fmt.Sprintf("lib:%s/%d", c.li.Src, c.v)] = true
		return c.li, c.v, tx
	}
	return nil, 0, ""
}

// ThreadsBuild is what a batch build did.
type ThreadsBuild struct {
	Day     string `json:"day"`
	PerDay  int    `json:"perDay"`
	Slots   int    `json:"slots"`   // free future slots it tried to fill
	Added   int    `json:"added"`   // posts added
	AI      int    `json:"ai"`      // of them written by the AI
	Lib     int    `json:"lib"`     // taken ready from the library
	Missing int    `json:"missing"` // slots left empty
	CTA     int    `json:"cta"`
	Magnets int    `json:"magnets"` // R76: lead magnet posts
	AIError string `json:"aiError,omitempty"`
	Prompt  string `json:"-"`
}

var errThreadsNoBatch = errors.New("Threads публикует 1 пост в день: дневная пачка не нужна")

// BuildThreadsDay makes the day's batch: the free slots after now get posts.
// force first removes the day's untouched future batch posts.
func (e *ContentEngine) BuildThreadsDay(ctx context.Context, day time.Time, force bool) (*ThreadsBuild, error) {
	d, _, err := e.load(ctx)
	if err != nil {
		return nil, err
	}
	st := e.settings(ctx, d)
	if !st.Channels.Threads.On {
		return nil, errors.New("Threads выключен в настройках")
	}
	if !st.threadsBatch() {
		return nil, errThreadsNoBatch
	}
	if !st.dayOn("threads", day) {
		return nil, errors.New("В этот день Threads не публикуется")
	}
	now := e.now()
	n := st.threadsPerDay()
	from, to := st.threadsWindow()
	key := contentDay(day)
	res := &ThreadsBuild{Day: key, PerDay: n}
	slots := threadsSlots(day, n, from, to)
	formats, ctas, isMg := threadsDayPlanMg(n, key, st.threadsReach(), st.threadsMagnet()) // R76: magnets.go
	if st.manual {
		// by hand: posts at 09:30, 12:30, 16:30, 19:30 (jittered), no series (a reply chain needs the API)
		slots = threadsManualSlots(day, n)
		for i, f := range formats {
			if f == "series" {
				formats[i] = "tip"
			}
		}
	}

	removable := func(it *contentItem) bool {
		at, ok := parseContentAt(it.At)
		return force && it.Channel == "threads" && it.Gen != "" && it.Auto && !it.Edited && it.Status == "planned" && ok && at.After(now.Add(2*time.Minute))
	}
	var have []time.Time
	count := 0
	for _, it := range d.Queue {
		if it.Channel != "threads" || it.day() != key || removable(it) {
			continue
		}
		if at, ok := parseContentAt(it.At); ok {
			have = append(have, at)
			count++
		}
	}
	var free []int
	for i, s := range slots {
		if !s.After(now.Add(10 * time.Minute)) {
			continue
		}
		taken := false
		for _, h := range have {
			if dd := h.Sub(s); dd < 12*time.Minute && dd > -12*time.Minute {
				taken = true
			}
		}
		if !taken {
			free = append(free, i)
		}
	}
	if room := n - count; len(free) > room {
		if room < 0 {
			room = 0
		}
		free = free[len(free)-room:]
	}
	res.Slots = len(free)
	if len(free) == 0 {
		e.noteBuild(ctx, key, n, res, nil)
		return res, nil
	}

	state, _ := e.state(ctx)
	mem := e.threadsMemory(d, state, now)
	prevOrgan := ""
	var prevAt time.Time
	for _, it := range d.Queue {
		if at, ok := parseContentAt(it.At); ok && it.Channel == "threads" && at.Before(slots[free[0]]) && at.After(prevAt) {
			prevAt, prevOrgan = at, it.Organ
		}
	}
	// R76: the magnet slots get the hand-written magnet posts; the rest as before
	var mgSlots []int
	var rest []int
	for _, i := range free {
		if isMg[i] {
			mgSlots = append(mgSlots, i)
		} else {
			rest = append(rest, i)
		}
	}
	mgItems := pickMagnets(len(mgSlots), mem, key, now)
	free = rest
	plan := make([]threadsSlotPlan, len(free))
	fs := make([]string, len(free))
	for k, i := range free {
		fs[k] = formats[i]
	}
	seeds := pickSeeds(append(append([]*threadsSeed(nil), threadsSeedPool()...), threadsReachSeeds()...), fs, mem, key, prevOrgan)
	for k, i := range free {
		f := threadsFormatByID[formats[i]]
		plan[k] = threadsSlotPlan{At: slots[i], Format: formats[i], Length: f.Length[(i/len(threadsFormats)+hashN(key+strconv.Itoa(i), 7))%len(f.Length)], CTA: ctas[i], Seed: seeds[k]}
	}

	// the AI writes the slots that have material
	var withSeed []int
	for k := range plan {
		if plan[k].Seed != nil {
			withSeed = append(withSeed, k)
		}
	}
	posts := map[int]threadsAIPost{}
	if len(withSeed) > 0 && e.AI != nil {
		var ps []threadsSlotPlan
		for _, k := range withSeed {
			ps = append(ps, plan[k])
		}
		var avoid []string
		for f := range mem.first {
			avoid = append(avoid, f)
		}
		sort.Strings(avoid)
		if len(avoid) > 40 {
			avoid = avoid[len(avoid)-40:]
		}
		res.Prompt = threadsPrompt(day, ps, avoid)
		c, cancel := context.WithTimeout(ctx, 4*time.Minute)
		ans, aerr := e.AI(c, threadsSystem, res.Prompt)
		cancel()
		var got []threadsAIPost
		if aerr == nil {
			got, aerr = parseThreadsAI(ans)
		}
		if aerr != nil {
			res.AIError = aerr.Error()
			log.Printf("threads: batch %s: AI: %v (library posts instead)", key, aerr)
		}
		for _, p := range got {
			if p.N >= 1 && p.N <= len(withSeed) {
				posts[withSeed[p.N-1]] = p
			}
		}
	} else if len(withSeed) > 0 {
		res.AIError = "ИИ не подключён"
	}

	lib := e.lib()
	usedLib := map[string]bool{}
	var items []*contentItem
	var sigs []threadsSig
	rejected, ctaN := 0, 0
	for k, sp := range plan {
		// the CTA lines take turns within the day
		ctaLine := threadsCTALines[(hashN(key, len(threadsCTALines))+ctaN)%len(threadsCTALines)]
		var it *contentItem
		if p, ok := posts[k]; ok && sp.Seed != nil {
			body := threadsClean(splitHook(p.Text)) // R76: a long first line gets its own line
			var parts []string
			if sp.Format == "series" {
				for _, x := range p.Parts {
					if x = threadsClean(x); x != "" {
						parts = append(parts, x)
					}
				}
				if len(parts) > 3 {
					parts = parts[:3]
				}
			}
			full := body
			link := ""
			cta := sp.CTA
			if cta {
				if len(parts) > 0 {
					parts[len(parts)-1] += "\n\n" + ctaLine
				} else {
					full = body + "\n\n" + ctaLine
				}
				link = contentBotLink + threadsCTAParam
			}
			probs := threadsQuality(body, full, partsNoCTA(parts, cta))
			if sp.Format == "series" && len(parts) < 1 {
				probs = append(probs, "серия без продолжения")
			}
			if len(probs) == 0 && mem.similar(body+" "+strings.Join(parts, " ")) {
				probs = append(probs, "похоже на пост последних 30 дней")
			}
			if len(probs) == 0 {
				it = &contentItem{contentItemData: contentItemData{Src: sp.Seed.ID, Organ: sp.Seed.Organ, Title: sp.Seed.Title, Text: full, Parts: parts,
					Format: sp.Format, Gen: "ai", CTA: cta, Link: link}}
				res.AI++
				sigs = append(sigs, threadsSig{Day: key, Src: sp.Seed.ID, Root: sp.Seed.Root, Organ: sp.Seed.Organ, First: thFirst(body), Keys: keysString(thKeys(body + " " + strings.Join(parts, " ")))})
				mem.remember(body + " " + strings.Join(parts, " "))
				mem.src[sp.Seed.ID] = sp.At
			} else {
				rejected++
				log.Printf("threads: batch %s post %d refused: %s", key, k+1, strings.Join(probs, "; "))
			}
		}
		if it == nil { // the library's ready post instead
			li, v, tx := libThreadsPost(lib, mem, sp.CTA, ctaLine, key+strconv.Itoa(k), usedLib)
			if li == nil {
				res.Missing++
				continue
			}
			it = &contentItem{contentItemData: contentItemData{Src: li.Src, V: v, Organ: li.Organ, Title: li.Title, Rubric: li.Rubric, Text: tx,
				Format: "library", Gen: "lib", CTA: sp.CTA}}
			if sp.CTA {
				it.Link = contentBotLink + threadsCTAParam
			}
			res.Lib++
			sigs = append(sigs, threadsSig{Day: key, Src: fmt.Sprintf("lib:%s/%d", li.Src, v), Organ: li.Organ, First: thFirst(tx), Keys: keysString(thKeys(tx))})
			mem.remember(tx)
		}
		it.ID = "th-" + sp.At.In(almaty).Format("20060102-1504")
		it.Kind, it.Channel = "threads", "threads"
		it.At = sp.At.In(almaty).Format(time.RFC3339)
		it.Status, it.Auto = "planned", true
		if st.manual {
			manualize(it) // each post gets its own bot link (th_p…) for the leads
		}
		if it.CTA {
			res.CTA++
			ctaN++
		}
		items = append(items, it)
	}
	if rejected > 0 {
		log.Printf("threads: batch %s: %d AI posts refused by the quality gate", key, rejected)
	}
	// R76: the magnet posts into their slots
	var mgNames []string
	for k, i := range mgSlots {
		if k >= len(mgItems) {
			res.Missing++
			continue
		}
		it := mgItems[k]
		it.ID = "th-" + slots[i].In(almaty).Format("20060102-1504")
		it.Kind, it.Channel = "threads", "threads"
		it.At = slots[i].In(almaty).Format(time.RFC3339)
		it.Status, it.Auto = "planned", true
		magnetize(it)
		res.Magnets++
		res.CTA++
		sigs = append(sigs, threadsSig{Day: key, Src: it.Src, Organ: it.Organ, First: thFirst(it.Text)})
		mgNames = append(mgNames, strings.TrimPrefix(it.Src, "mg:"))
		items = append(items, it)
	}
	if len(mgSlots) > 0 {
		log.Printf("threads: batch %s: magnets %d (%s)", key, len(mgNames), strings.Join(mgNames, ", "))
	}

	added := 0
	_, err = e.update(ctx, func(d *contentDoc) bool {
		added = 0
		if force {
			q := d.Queue[:0]
			for _, it := range d.Queue {
				if it.day() == key && removable(it) {
					continue
				}
				q = append(q, it)
			}
			d.Queue = q
		}
		for _, it := range items {
			at, _ := parseContentAt(it.At)
			clash := findContent(d, it.ID) != nil
			for _, x := range d.Queue {
				if y, ok := parseContentAt(x.At); ok && x.Channel == "threads" {
					if dd := y.Sub(at); dd < 12*time.Minute && dd > -12*time.Minute {
						clash = true
					}
				}
			}
			if clash {
				continue
			}
			cp := *it
			d.Queue = append(d.Queue, &cp)
			added++
		}
		sort.SliceStable(d.Queue, func(i, j int) bool {
			a, _ := parseContentAt(d.Queue[i].At)
			b, _ := parseContentAt(d.Queue[j].At)
			return a.Before(b)
		})
		return added > 0 || force
	})
	if err != nil {
		return res, err
	}
	res.Added = added
	e.noteBuild(ctx, key, n, res, sigs)
	log.Printf("threads: batch %s: %d posts (AI %d, library %d, magnets %d), %d empty", key, added, res.AI, res.Lib, res.Magnets, res.Missing)
	return res, nil
}

func partsNoCTA(parts []string, cta bool) []string {
	if !cta || len(parts) == 0 {
		return parts
	}
	out := append([]string(nil), parts...)
	last := out[len(out)-1]
	if i := strings.LastIndex(last, "\n\n"); i > 0 {
		out[len(out)-1] = last[:i]
	}
	return out
}

// noteBuild remembers the build of a day and the posts' signatures.
func (e *ContentEngine) noteBuild(ctx context.Context, key string, n int, res *ThreadsBuild, sigs []threadsSig) {
	e.saveState(ctx, func(s *contentState) {
		if s.Threads == nil {
			s.Threads = map[string]*threadsDayState{}
		}
		prev := s.Threads[key]
		ds := &threadsDayState{N: n, Added: res.Added, Missing: res.Missing, At: e.now().UTC().Format(time.RFC3339), AI: "ok", Plan: threadsPlanRev}
		if res.AIError != "" {
			ds.AI = res.AIError
			ds.Tries = 1
			if prev != nil {
				ds.Tries = prev.Tries + 1
			}
		}
		s.Threads[key] = ds
		if len(s.Threads) > 14 {
			var keys []string
			for k := range s.Threads {
				keys = append(keys, k)
			}
			sort.Strings(keys)
			for _, k := range keys[:len(keys)-14] {
				delete(s.Threads, k)
			}
		}
		s.Recent = append(s.Recent, sigs...)
		cut := contentDay(e.now().AddDate(0, 0, -threadsRecentKeep))
		r := s.Recent[:0]
		for _, x := range s.Recent {
			if x.Day >= cut {
				r = append(r, x)
			}
		}
		s.Recent = r
	})
}

// buildDue starts today's batch after buildAt (once; again when the cadence
// grew or the AI failed and slots stayed empty, at most 3 times an hour apart).
func (e *ContentEngine) buildDue(ctx context.Context, d *contentDoc) {
	st := e.settings(ctx, d)
	if !st.Channels.Threads.On || !st.threadsBatch() {
		return
	}
	now := e.now().In(almaty)
	mins := now.Hour()*60 + now.Minute()
	_, to := st.threadsWindow()
	if mins < st.threadsBuildAt() || mins >= to-10 || !st.dayOn("threads", now) {
		return
	}
	s, _ := e.state(ctx)
	ds := s.Threads[contentDay(now)]
	need := ds == nil || ds.N < st.threadsPerDay()
	if !need && ds.Missing > 0 && ds.AI != "ok" && ds.Tries < 3 {
		if t, err := time.Parse(time.RFC3339, ds.At); err == nil && now.Sub(t) >= time.Hour {
			need = true
		}
	}
	replan := !need && ds != nil && ds.Plan < threadsPlanRev // R76: once, the day's untouched future posts
	if !(need || replan) || !e.building.CompareAndSwap(false, true) {
		return
	}
	run := e.Go
	if run == nil {
		run = func(f func()) { go f() }
	}
	run(func() {
		defer e.building.Store(false)
		c, cancel := context.WithTimeout(context.Background(), 6*time.Minute)
		defer cancel()
		if replan {
			log.Printf("threads: batch %s: re-planned for the magnets rubric (R76)", contentDay(now))
		}
		if _, err := e.BuildThreadsDay(c, now, replan); err != nil {
			log.Printf("threads: batch: %v", err)
		}
	})
}

// ── Publishing safety ──

// threadsAPIError is Threads' refusal with its code.
type threadsAPIError struct {
	Status  int
	Code    int
	Subcode int
	Type    string
	Msg     string
}

func (e *threadsAPIError) Error() string { return "Threads: " + e.Msg }

// threadsErrKind: auth (the token), rate (the limits), temp (worth a retry) or other.
func threadsErrKind(err error) string {
	var ae *threadsAPIError
	if errors.As(err, &ae) {
		switch {
		case ae.Code == 190 || ae.Code == 102 || ae.Status == 401 || (ae.Type == "OAuthException" && ae.Code == 10):
			return "auth"
		case ae.Code == 4 || ae.Code == 17 || ae.Code == 32 || ae.Code == 613 || ae.Status == 429:
			return "rate"
		case ae.Status >= 500 || ae.Code == 1 || ae.Code == 2:
			return "temp"
		}
	}
	low := strings.ToLower(err.Error())
	switch {
	case strings.Contains(low, "threads_token") || strings.Contains(low, "access token") || strings.Contains(low, "session has expired") ||
		strings.Contains(low, "токен") || strings.Contains(low, "не подключён"):
		return "auth"
	case strings.Contains(low, "rate limit") || strings.Contains(low, "limit reached") || strings.Contains(low, "too many") || strings.Contains(low, "лимит"):
		return "rate"
	}
	var ne net.Error
	if errors.Is(err, context.DeadlineExceeded) || errors.As(err, &ne) || strings.Contains(low, "timeout") || strings.Contains(low, "connection") ||
		strings.Contains(low, "eof") || strings.Contains(low, "no such host") {
		return "temp"
	}
	return "other"
}

// threads24h: Threads posts the server published in the last 24 hours.
func threads24h(d *contentDoc, now time.Time) (int, time.Time) {
	n := 0
	var oldest time.Time
	seen := map[string]bool{}
	for _, list := range [][]*contentItem{d.Queue, d.History} {
		for _, it := range list {
			if it.Channel != "threads" || it.Status != "published" || seen[it.ID] {
				continue
			}
			seen[it.ID] = true
			t, ok := parseContentAt(it.PublishedAt)
			if !ok || now.Sub(t) >= 24*time.Hour || t.After(now) {
				continue
			}
			n++
			if oldest.IsZero() || t.Before(oldest) {
				oldest = t
			}
		}
	}
	return n, oldest
}

// thHeld: Threads waits (a token or limit error, the gap between posts).
func (e *ContentEngine) thHeld(now time.Time) bool {
	e.thMu.Lock()
	defer e.thMu.Unlock()
	return now.Before(e.thHold) || (!e.thLast.IsZero() && now.Sub(e.thLast) < threadsGap)
}

func (e *ContentEngine) thSet(hold, last time.Time) {
	e.thMu.Lock()
	defer e.thMu.Unlock()
	if !hold.IsZero() {
		e.thHold = hold
	}
	if !last.IsZero() {
		e.thLast = last
	}
}

// saveState changes content_state (the server's), keeping its other fields.
func (e *ContentEngine) saveState(ctx context.Context, fn func(s *contentState)) {
	for try := 0; try < 4; try++ {
		s, base := e.state(ctx)
		fn(&s)
		val, _ := json.Marshal(s)
		if _, err := e.docs.PutDoc(ctx, "server", contentStateKey, base, string(val), false, "server:content"); err == nil {
			return
		} else if try == 3 {
			log.Printf("content: state: %v", err)
		}
	}
}

// alertOnce tells the owner once a day per kind; false when already told.
func (e *ContentEngine) alertOnce(ctx context.Context, kind, text string) bool {
	day := contentDay(e.now())
	told := false
	e.saveState(ctx, func(s *contentState) {
		told = false
		if s.Alerts == nil {
			s.Alerts = map[string]string{}
		}
		if s.Alerts[kind] == day {
			told = true
			return
		}
		s.Alerts[kind] = day
	})
	if told {
		return false
	}
	e.tellOwner(ctx, text)
	return true
}

// threadsRecovered: a post went out after a token alert: say so once, clear the alerts.
func (e *ContentEngine) threadsRecovered(ctx context.Context) {
	s, _ := e.state(ctx)
	if len(s.Alerts) == 0 {
		return
	}
	auth := s.Alerts["th_auth"] != ""
	has := false
	for k := range s.Alerts {
		if strings.HasPrefix(k, "th_") {
			has = true
		}
	}
	if !has {
		return
	}
	e.saveState(ctx, func(s *contentState) {
		for k := range s.Alerts {
			if strings.HasPrefix(k, "th_") {
				delete(s.Alerts, k)
			}
		}
	})
	if auth {
		e.tellOwner(ctx, "✅ Threads снова публикует: токен принят, посты дня идут по плану.")
	}
}

func threadsAlertText(kind string, it *contentItem, err error) string {
	switch kind {
	case "auth":
		return "⚠️ Threads не принимает токен: он истёк или отозван.\nПосты в Threads на паузе, сервер пробует снова каждые 30 минут. Обновите THREADS_TOKEN в Railway.\n\n" + err.Error()
	case "rate":
		return "⚠️ Threads: достигнут лимит публикаций. Пауза на час, потом посты дня продолжат выходить.\n\n" + err.Error()
	case "temp":
		return "⚠️ Threads не отвечает: пост не вышел после 3 попыток.\n«" + content.FirstLine(it.Text, 80) + "»\n\n" + err.Error()
	case "quota":
		return fmt.Sprintf("⚠️ Threads: за сутки вышло %d постов, лимит API %d. Остальные посты ждут, пока освободится место.", threadsQuotaSafe, threadsQuota24h)
	}
	return fmt.Sprintf("⚠️ Пост не вышел\n%s · %s\n«%s»\n\n%s", contentChannelName[it.Channel], atClock(it.At), content.FirstLine(it.Text, 80), err.Error())
}

// ── Platform: per-day counts ──

// ThreadsDays: the Threads posts per day for the planner (14 days from today).
func (e *ContentEngine) ThreadsDays(ctx context.Context) (map[string]any, error) {
	d, _, err := e.load(ctx)
	if err != nil {
		return nil, err
	}
	st := e.settings(ctx, d)
	s, _ := e.state(ctx)
	today := dayStart(e.now())
	days := []map[string]any{}
	for i := 0; i < contentDays; i++ {
		day := today.AddDate(0, 0, i)
		key := contentDay(day)
		c := map[string]int{}
		for _, it := range d.Queue {
			if it.Channel == "threads" && it.day() == key {
				c[it.Status]++
				c["all"]++
			}
		}
		x := map[string]any{"day": day.Format("2006-01-02"), "on": st.Channels.Threads.On && st.dayOn("threads", day), "count": c}
		if ds := s.Threads[key]; ds != nil {
			x["build"] = ds
		}
		days = append(days, x)
	}
	from, to := st.threadsWindow()
	bh := st.threadsBuildAt()
	return map[string]any{"perDay": st.threadsPerDay(), "from": fmt.Sprintf("%02d:%02d", from/60, from%60), "to": fmt.Sprintf("%02d:%02d", to/60, to%60),
		"buildAt": fmt.Sprintf("%02d:%02d", bh/60, bh%60), "days": days, "max": threadsPerDayMax,
		"manual": st.manual, "manualPerDay": st.manualPerDay()}, nil
}

// threadsTrim: the cadence went down, so a day's untouched future batch posts
// beyond it go (evenly, so the rest stays spread over the day).
func threadsTrim(q []*contentItem, st contentSettings, now time.Time) []*contentItem {
	if !st.threadsBatch() {
		return q
	}
	n := st.threadsPerDay()
	byDay := map[string][]*contentItem{}
	for _, it := range q {
		if it.Channel == "threads" && it.Status != "skipped" {
			byDay[it.day()] = append(byDay[it.day()], it)
		}
	}
	drop := map[*contentItem]bool{}
	for _, list := range byDay {
		excess := len(list) - n
		if excess <= 0 {
			continue
		}
		var can []*contentItem
		for _, it := range list {
			if at, ok := parseContentAt(it.At); ok && it.Gen != "" && it.Auto && !it.Edited && it.Status == "planned" && at.After(now) {
				can = append(can, it)
			}
		}
		sort.SliceStable(can, func(i, j int) bool { return can[i].At < can[j].At })
		if excess > len(can) {
			excess = len(can)
		}
		for j := 0; j < excess; j++ {
			drop[can[(2*j+1)*len(can)/(2*excess)]] = true
		}
	}
	if len(drop) == 0 {
		return q
	}
	out := q[:0]
	for _, it := range q {
		if !drop[it] {
			out = append(out, it)
		}
	}
	return out
}

// ThreadsReserve: the share of the answering free provider's daily budget
// the Threads batch leaves for the team's own tasks (R42).
const ThreadsReserve = 40

// ErrThreadsBudget: the batch goes without AI to save the free limit.
var ErrThreadsBudget = errors.New("бесплатный лимит ИИ бережём для задач команды: посты дня из библиотеки")

// ThreadsAI: the batch's AI, used only while the free budget allows (R42);
// otherwise the batch takes the library's ready posts.
func ThreadsAI(c *ai.Client) func(ctx context.Context, system, prompt string) (string, error) {
	return func(ctx context.Context, system, prompt string) (string, error) {
		if !c.BudgetAllows(ThreadsReserve) {
			return "", ErrThreadsBudget
		}
		return c.Text(ctx, system, prompt)
	}
}
