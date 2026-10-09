package tgevents

import (
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/bnursik/business_surgery_backend/internal/ai"
)

// Almaty: dates in posts are local.
var Almaty = time.FixedZone("Almaty", 5*3600)

// KindOpportunity: grants, programmes, contests: the date is the deadline.
const KindOpportunity = "возможность"

// Result of reading one post by rules.
type Result struct {
	Event ai.Event
	OK    bool // a dated event or opportunity
	// NeedAI: the rules could not read it, but it looks like an announcement
	// (a digest with many dates, or a programme without a clear date).
	NeedAI bool
}

type found struct {
	pos, end, line int
	date           string // YYYY-MM-DD
	deadline       bool
}

var months = []struct {
	re string
	m  int
}{
	{`январ[а-яё]*|january|jan`, 1}, {`феврал[а-яё]*|february|feb`, 2}, {`март[а-яё]*|march|mar`, 3},
	{`апрел[а-яё]*|april|apr`, 4}, {`ма[йя]|may`, 5}, {`июн[а-яё]*|june|jun`, 6},
	{`июл[а-яё]*|july|jul`, 7}, {`август[а-яё]*|august|aug`, 8}, {`сентябр[а-яё]*|september|sept|sep`, 9},
	{`октябр[а-яё]*|october|oct`, 10}, {`ноябр[а-яё]*|november|nov`, 11}, {`декабр[а-яё]*|december|dec`, 12},
}

var (
	monRes     []*regexp.Regexp
	monAlt     string
	dayMonRe   *regexp.Regexp // 15 октября 2026, 15-16 октября
	monDayRe   *regexp.Regexp // October 15, 2026
	numDateRe  = regexp.MustCompile(`(?:^|[^\d.])(\d{1,2})\.(\d{2})(?:\.(\d{4}|\d{2}))?(?:[^\d]|$)`)
	isoRe      = regexp.MustCompile(`\b(20\d{2})-(\d{2})-(\d{2})\b`)
	timeRe2    = regexp.MustCompile(`(?:^|[^\d])([01]?\d|2[0-3]):([0-5]\d)(?:[^\d]|$)`)
	deadlineRe = regexp.MustCompile(`(?i)(дедлайн|deadline|(?:^|[^а-яё])до(?:[^а-яё]|$)|при[её]м[а-я]* заяв|заявк[а-я]* принима|подать заявку|подача заяв|окончани[а-я]* при[её]ма|регистраци[а-я]* до|apply by|apply until|applications? close|due|срок)`)
	oppRe      = regexp.MustCompile(`(?i)(грант|акселерат|инкубат|конкурс|стипенди|программ|стажиров|челлендж|хакатон|fellowship|scholarship|grant|accelerat|incubat|competition|challenge|programme|program|internship|award|премия|отбор|питч|pitch)`)
	onlineRe   = regexp.MustCompile(`(?i)(онлайн|online|zoom|google meet|вебинар|webinar|в эфире)`)
	placeRe    = regexp.MustCompile(`(?i)^(?:место(?: проведения)?|где|адрес|локация|location|venue|place)\s*[:\-–—]\s*(.+)$`)
	pinRe      = regexp.MustCompile(`^(?:📍|🏢|🗺️?)\s*`)
	orgRe      = regexp.MustCompile(`(?i)^(?:организатор[ыи]?|организует|organi[sz]er|organi[sz]ed by|host(?:ed by)?)\s*[:\-–—]?\s*(.+)$`)
	freeRe     = regexp.MustCompile(`(?i)(бесплатн|free of charge|\bfree\b)`)
	cityRe     = regexp.MustCompile(`(?i)(?:^|[^\p{L}])(алмат[ыае]|астан[аеы]|шымкент[а-я]*|караганд[а-я]*|бишкек[а-я]*|ташкент[а-я]*|almaty|astana)(?:[^\p{L}]|$)`)
	adRe       = regexp.MustCompile(`(?i)(#реклама|#ad\b|erid|присоединяйтесь к закрытому|подписывайтесь на|промокод)`)
	eventyRe   = regexp.MustCompile(`(?i)(регистрац|мероприят|ивент|event|митап|meetup|конференц|форум|нетворкинг|воркшоп|workshop|мастер-класс|лекци|интенсив|demo ?day|хакатон|hackathon|акселерат|грант|конкурс|программ|roadmap|календар)`)
	tagWords   = []struct {
		re  *regexp.Regexp
		tag string
	}{
		{regexp.MustCompile(`(?i)грант|grant`), "грант"},
		{regexp.MustCompile(`(?i)акселерат|инкубат|accelerat|incubat|pre-?incubation`), "акселератор"},
		{regexp.MustCompile(`(?i)конкурс|челлендж|competition|challenge|award|премия`), "конкурс"},
		{regexp.MustCompile(`(?i)хакатон|hackathon`), "хакатон"},
		{regexp.MustCompile(`(?i)стажиров|internship|fellowship|стипенди|scholarship`), "стажировка"},
		{regexp.MustCompile(`(?i)нетворкинг|networking|митап|meetup`), "нетворкинг"},
		{regexp.MustCompile(`(?i)конференц|форум|conference|forum|summit|саммит`), "конференция"},
		{regexp.MustCompile(`(?i)мастер-класс|воркшоп|workshop|лекци|интенсив|курс|обучени|training|bootcamp`), "обучение"},
		{regexp.MustCompile(`(?i)инвест|венчур|invest|venture|vc\b`), "инвестиции"},
		{regexp.MustCompile(`\bIT\b|\bAI\b|(?i:искусственн|\btech|технолог|digital|цифров)`), "IT"},
		{regexp.MustCompile(`(?i)стартап|startup`), "стартапы"},
	}
)

func init() {
	alts := make([]string, len(months))
	for i, m := range months {
		alts[i] = m.re
	}
	monAlt = strings.Join(alts, "|")
	for _, m := range months {
		monRes = append(monRes, regexp.MustCompile(`^(?:`+m.re+`)$`))
	}
	dayMonRe = regexp.MustCompile(`(?i)(?:^|[^\d])(\d{1,2})(?:\s*[-–—]\s*\d{1,2})?\s+(` + monAlt + `)\.?(?:\s+(20\d{2}))?`)
	monDayRe = regexp.MustCompile(`(?i)\b(` + monAlt + `)\.?\s+(\d{1,2})(?:st|nd|rd|th)?\b(?:,?\s+(20\d{2}))?`)
}

func monthOf(s string) int {
	s = strings.ToLower(s)
	for i, re := range monRes {
		if re.MatchString(s) {
			return months[i].m
		}
	}
	return 0
}

// mkDate: YYYY-MM-DD; without a year, the year of the post, or the next one
// when the date would be long before the post.
func mkDate(d, m int, y string, post time.Time) string {
	if d < 1 || d > 31 || m < 1 || m > 12 {
		return ""
	}
	year := post.Year()
	if y != "" {
		year, _ = strconv.Atoi(y)
		if year < 100 {
			year += 2000
		}
	}
	t := time.Date(year, time.Month(m), d, 0, 0, 0, 0, Almaty)
	if t.Day() != d {
		return ""
	}
	if y == "" && t.Before(post.AddDate(0, 0, -60)) {
		t = t.AddDate(1, 0, 0)
	}
	return t.Format("2006-01-02")
}

// dates: every date mentioned in text, in order, with whether its context
// marks it as an application deadline.
func dates(text string, post time.Time) []found {
	var out []found
	add := func(pos, end int, date string) {
		if date == "" {
			return
		}
		for _, f := range out {
			if pos < f.end && end > f.pos {
				return // overlaps a date already read
			}
		}
		out = append(out, found{pos: pos, end: end, date: date})
	}
	for _, m := range isoRe.FindAllStringSubmatchIndex(text, -1) {
		y, mo, d := text[m[2]:m[3]], text[m[4]:m[5]], text[m[6]:m[7]]
		mi, _ := strconv.Atoi(mo)
		di, _ := strconv.Atoi(d)
		add(m[0], m[1], mkDate(di, mi, y, post))
	}
	for _, m := range dayMonRe.FindAllStringSubmatchIndex(text, -1) {
		if letterAt(text, m[5]) {
			continue // "3 marketing", "5 майских": not a month
		}
		d, _ := strconv.Atoi(text[m[2]:m[3]])
		y := ""
		if m[6] >= 0 {
			y = text[m[6]:m[7]]
		}
		add(m[2], m[1], mkDate(d, monthOf(text[m[4]:m[5]]), y, post))
	}
	for _, m := range monDayRe.FindAllStringSubmatchIndex(text, -1) {
		if letterAt(text, m[3]) {
			continue
		}
		d, _ := strconv.Atoi(text[m[4]:m[5]])
		y := ""
		if m[6] >= 0 {
			y = text[m[6]:m[7]]
		}
		add(m[0], m[1], mkDate(d, monthOf(text[m[2]:m[3]]), y, post))
	}
	for _, m := range numDateRe.FindAllStringSubmatchIndex(text, -1) {
		d, _ := strconv.Atoi(text[m[2]:m[3]])
		mo, _ := strconv.Atoi(text[m[4]:m[5]])
		y := ""
		if m[6] >= 0 {
			y = text[m[6]:m[7]]
		}
		add(m[2], m[3], mkDate(d, mo, y, post))
	}
	sort.Slice(out, func(i, j int) bool { return out[i].pos < out[j].pos })
	for i := range out {
		ls := strings.LastIndex(text[:out[i].pos], "\n") + 1
		from := ls
		if i > 0 && out[i-1].end > from {
			from = out[i-1].end
		}
		out[i].line = strings.Count(text[:out[i].pos], "\n")
		out[i].deadline = deadlineRe.MatchString(text[from:out[i].pos])
	}
	return out
}

// Extract reads one post by rules. today: YYYY-MM-DD in Almaty.
func Extract(p Post, today string) Result {
	text := strings.TrimSpace(p.Text)
	if text == "" || adRe.MatchString(text) {
		return Result{}
	}
	post := p.Time.In(Almaty)
	if p.Time.IsZero() {
		post = time.Now().In(Almaty)
	}
	lines := strings.Split(text, "\n")
	e := ai.Event{Source: "t.me/" + p.Channel, Post: p.URL(), Origin: "tg"}
	var tl int
	e.Title, tl = title(lines)
	if e.Title == "" {
		return Result{}
	}
	ds := dates(text, post)
	distinct := map[string]bool{}
	for _, d := range ds {
		distinct[d.date] = true
	}
	if len(distinct) >= 5 {
		return Result{NeedAI: true} // a digest: several events in one post
	}
	var ev *found
	for i := range ds {
		d := &ds[i]
		if d.deadline {
			if e.Deadline == "" && d.date >= today {
				e.Deadline = d.date
			}
		} else if ev == nil {
			ev = d
		}
	}
	isOpp := oppRe.MatchString(text)
	switch {
	case e.Deadline != "" && (ev == nil || isOpp):
		e.Kind, e.Date = KindOpportunity, e.Deadline
	case ev != nil:
		e.Date = ev.date
		if m := timeRe2.FindStringSubmatch(lines[ev.line]); m != nil {
			e.Time = pad2(m[1]) + ":" + m[2]
		}
	}
	if e.Date == "" {
		if len(ds) == 0 && eventyRe.MatchString(text) && len([]rune(text)) > 120 {
			return Result{NeedAI: true}
		}
		return Result{}
	}
	for _, l := range lines {
		pin := pinRe.FindString(l)
		rest := strings.TrimSpace(l[len(pin):])
		if e.Place == "" {
			if m := placeRe.FindStringSubmatch(rest); m != nil {
				e.Place = cut(strings.TrimSpace(m[1]), 120)
			} else if pin != "" && rest != "" && !onlineRe.MatchString(rest) {
				e.Place = cut(rest, 120)
			}
		}
		if m := orgRe.FindStringSubmatch(l); m != nil && e.Org == "" {
			e.Org = cut(strings.TrimSpace(m[1]), 100)
		}
	}
	e.Online = onlineRe.MatchString(text)
	if e.Place == "" {
		if m := cityRe.FindStringSubmatch(text); m != nil {
			e.Place = cityName(m[1])
		} else if e.Online {
			e.Place = "Онлайн"
		}
	}
	if freeRe.MatchString(text) {
		e.Price = "бесплатно"
	}
	e.URL = link(p)
	e.Desc = desc(lines, tl)
	if e.Kind == KindOpportunity {
		e.Tags = append(e.Tags, KindOpportunity)
	}
	for _, t := range tagWords {
		if t.re.MatchString(text) && len(e.Tags) < 4 {
			e.Tags = append(e.Tags, t.tag)
		}
	}
	if e.Online {
		e.Tags = append(e.Tags, "онлайн")
	}
	return Result{Event: e, OK: e.Date >= today}
}

// letterAt: text has a letter at byte i (a month word that goes on).
func letterAt(text string, i int) bool {
	for _, r := range text[i:] {
		return unicode.IsLetter(r)
	}
	return false
}

func cityName(s string) string {
	s = strings.ToLower(s)
	switch {
	case strings.HasPrefix(s, "алмат"), s == "almaty":
		return "Алматы"
	case strings.HasPrefix(s, "астан"), s == "astana":
		return "Астана"
	case strings.HasPrefix(s, "шымкент"):
		return "Шымкент"
	case strings.HasPrefix(s, "караганд"):
		return "Караганда"
	case strings.HasPrefix(s, "бишкек"):
		return "Бишкек"
	case strings.HasPrefix(s, "ташкент"):
		return "Ташкент"
	}
	return s
}

// title: the first line with words, without leading emoji and marks; and
// the index of that line.
func title(lines []string) (string, int) {
	for i, l := range lines {
		t := strings.TrimLeftFunc(l, func(r rune) bool {
			return !(unicode.IsLetter(r) || unicode.IsDigit(r) || r == '«' || r == '"')
		})
		t = strings.TrimRightFunc(t, func(r rune) bool {
			return unicode.IsSpace(r) || unicode.IsSymbol(r) || r == ':' || r == '-' || r == '\u200d' || r == '\ufe0f'
		})
		if strings.HasPrefix(t, "#") || len([]rune(t)) < 6 {
			continue
		}
		letters := 0
		for _, r := range t {
			if unicode.IsLetter(r) {
				letters++
			}
		}
		if letters < 4 {
			continue
		}
		return cut(t, 140), i
	}
	return "", -1
}

func desc(lines []string, skip int) string {
	var parts []string
	n := 0
	for i, l := range lines {
		l = strings.TrimSpace(l)
		if i <= skip || l == "" || strings.HasPrefix(l, "http") || strings.HasPrefix(l, "#") {
			continue
		}
		parts = append(parts, l)
		n += len([]rune(l))
		if n > 300 {
			break
		}
	}
	return cut(strings.Join(parts, " "), 280)
}

// link: the first link outside Telegram (registration, site), else the post.
func link(p Post) string {
	for _, h := range p.Links {
		u, err := url.Parse(h)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") {
			continue
		}
		host := strings.TrimPrefix(strings.ToLower(u.Host), "www.")
		if host == "t.me" || host == "telegram.me" || host == "telegram.org" {
			continue
		}
		return h
	}
	return p.URL()
}

func cut(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return strings.TrimSpace(string(r[:n-1])) + "…"
}

func pad2(s string) string {
	if len(s) == 1 {
		return "0" + s
	}
	return s
}
