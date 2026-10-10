// Package voicecmd (R79): the board's voice assistant without AI. A spoken
// command (Whisper on the server, or the browser's own streaming model) is
// cleaned, its board words are corrected against the board's vocabulary
// (diagnosis and tool names, resident names), and rules turn it into the
// board's actions. Only what the rules do not understand goes to the free AI
// chain. The same package sorts board notes: numbers, plain notes, problems
// that may be a diagnosis, actions that may be a tool.
package voicecmd

import (
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode"
)

// Norm: lower case, ё → е, punctuation to spaces, single spaces.
func Norm(s string) string {
	s = strings.ToLower(strings.ReplaceAll(strings.ReplaceAll(s, "ё", "е"), "Ё", "Е"))
	var b strings.Builder
	sp := true
	for _, r := range s {
		ok := unicode.IsLetter(r) || unicode.IsDigit(r) || r == '%' || r == '.' || r == ':' || r == '+' || r == '-' || r == '₸' || r == '$'
		if r == '-' || r == '.' || r == ':' {
			// keep inside numbers (05.10, 10:30) and words (что-то), not as separators
			ok = true
		}
		if !ok {
			if !sp {
				b.WriteByte(' ')
				sp = true
			}
			continue
		}
		b.WriteRune(r)
		sp = false
	}
	out := strings.TrimSpace(b.String())
	// a dot or a dash at a word's end is punctuation
	out = trailPunct.ReplaceAllString(out, "$1 ")
	out = dashRe.ReplaceAllString(out, "$1 $2")
	out = aliasRe.ReplaceAllStringFunc(out, func(w string) string { return " " + aliases[strings.TrimSpace(w)] + " " })
	return strings.Join(strings.Fields(out), " ")
}

// a dash between letters is a space («CRM-система» = «crm система»)
var dashRe = regexp.MustCompile(`([^\d\s])-([^\d\s])`)

// spoken forms of Latin names
var aliases = map[string]string{"срм": "crm", "црм": "crm", "си ар эм": "crm", "амо": "amo", "амоцрм": "amocrm", "амосрм": "amocrm",
	"кпи": "kpi", "кипиай": "kpi", "окр": "okr", "нпс": "nps", "ромми": "romi", "роми": "romi"}
var aliasRe = regexp.MustCompile(`(?:^|\s)(?:срм|црм|амоцрм|амосрм|амо|кпи|кипиай|окр|нпс|роми|ромми)(?:\s|$)`)

var trailPunct = regexp.MustCompile(`([^\d])[.:\-]+(\s|$)`)

// wake words and the filler around them («Джарвис, пожалуйста, …»)
var (
	wakeRe   = regexp.MustCompile(`^(?:(?:слушай|эй|окей|ок|hey|ok)\s+)?(?:джарвис|жарвис|джервис|джавис|джарвиз|джарви с|джерви с|джави с|жарви с|джар вис|джер вис|jarvis|ассистент|асистент|помощник)(?:\s+|$)`)
	fillerRe = regexp.MustCompile(`^(?:(?:пожалуйста|будь добр|давай|ну|так|слушай|короче|значит|а|и|теперь|еще|ещё)\s+)+`)
	pleaseRe = regexp.MustCompile(`\s+пожалуйста(\s|$)`)
)

// StripWake: the command without the wake word and the filler words in front.
func StripWake(s string) string {
	t := Norm(s)
	for i := 0; i < 3; i++ {
		t0 := t
		t = fillerRe.ReplaceAllString(t, "")
		t = wakeRe.ReplaceAllString(t, "")
		if t == t0 {
			break
		}
	}
	t = pleaseRe.ReplaceAllString(t, " ")
	return strings.TrimSpace(t)
}

// ── numbers said in words ──

var numWords = map[string]int{
	"ноль": 0, "один": 1, "одна": 1, "одну": 1, "одного": 1, "два": 2, "две": 2, "двух": 2, "три": 3, "трех": 3, "четыре": 4, "четырех": 4,
	"пять": 5, "пяти": 5, "шесть": 6, "шести": 6, "семь": 7, "семи": 7, "восемь": 8, "восьми": 8, "девять": 9, "девяти": 9,
	"десять": 10, "десяти": 10, "одиннадцать": 11, "двенадцать": 12, "тринадцать": 13, "четырнадцать": 14, "пятнадцать": 15,
	"шестнадцать": 16, "семнадцать": 17, "восемнадцать": 18, "девятнадцать": 19, "двадцать": 20, "тридцать": 30, "сорок": 40,
	"пятьдесят": 50, "шестьдесят": 60, "девяносто": 90, "сто": 100, "полчаса": 30, "полтора": 1, "пару": 2, "пара": 2,
}

var ordWords = map[string]int{
	"первого": 1, "второго": 2, "третьего": 3, "четвертого": 4, "пятого": 5, "шестого": 6, "седьмого": 7, "восьмого": 8,
	"девятого": 9, "десятого": 10, "одиннадцатого": 11, "двенадцатого": 12, "тринадцатого": 13, "четырнадцатого": 14,
	"пятнадцатого": 15, "шестнадцатого": 16, "семнадцатого": 17, "восемнадцатого": 18, "девятнадцатого": 19,
	"двадцатого": 20, "тридцатого": 30,
}

// ordinal: «пятого», «двадцать пятого», «5-го», «5»
func ordinal(ws []string, i int) (n, used int) {
	if i >= len(ws) {
		return 0, 0
	}
	if v, err := strconv.Atoi(strings.TrimSuffix(strings.TrimSuffix(ws[i], "-го"), "-е")); err == nil && v >= 1 && v <= 31 {
		return v, 1
	}
	if v, ok := ordWords[ws[i]]; ok {
		return v, 1
	}
	if v, ok := ordWords[strings.TrimSuffix(ws[i], "ому")+"ого"]; ok && strings.HasSuffix(ws[i], "ому") {
		return v, 1
	}
	if (ws[i] == "двадцать" || ws[i] == "тридцать") && i+1 < len(ws) {
		if v, ok := ordWords[ws[i+1]]; ok && v < 10 {
			return numWords[ws[i]] + v, 2
		}
	}
	return 0, 0
}

// cardinal: «5», «пять», «двадцать пять», «пару»
func cardinal(ws []string, i int) (n, used int) {
	if i >= len(ws) {
		return 0, 0
	}
	if v, err := strconv.Atoi(strings.ReplaceAll(ws[i], ",", "")); err == nil {
		return v, 1
	}
	v, ok := numWords[ws[i]]
	if !ok {
		return 0, 0
	}
	if v >= 20 && v%10 == 0 && i+1 < len(ws) {
		if u, ok := numWords[ws[i+1]]; ok && u < 10 {
			return v + u, 2
		}
	}
	return v, 1
}

var months = map[string]time.Month{
	"января": 1, "февраля": 2, "марта": 3, "апреля": 4, "мая": 5, "июня": 6, "июля": 7, "августа": 8,
	"сентября": 9, "октября": 10, "ноября": 11, "декабря": 12,
}

var weekdays = map[string]time.Weekday{
	"понедельник": time.Monday, "понедельника": time.Monday, "вторник": time.Tuesday, "вторника": time.Tuesday,
	"среду": time.Wednesday, "среда": time.Wednesday, "среды": time.Wednesday, "четверг": time.Thursday, "четверга": time.Thursday,
	"пятницу": time.Friday, "пятница": time.Friday, "пятницы": time.Friday, "субботу": time.Saturday, "суббота": time.Saturday,
	"субботы": time.Saturday, "воскресенье": time.Sunday, "воскресенья": time.Sunday,
}

var dmRe = regexp.MustCompile(`^(\d{1,2})[./](\d{1,2})(?:[./](\d{2,4}))?$`)

// dateAt reads a date starting at ws[i]: «завтра», «пятницу», «5.10»,
// «пятого октября», «через неделю», «конца недели». It returns the date and
// how many words it took.
func dateAt(ws []string, i int, now time.Time) (time.Time, int) {
	if i >= len(ws) {
		return time.Time{}, 0
	}
	day := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
	w := ws[i]
	switch w {
	case "сегодня", "сегодняшнего":
		return day, 1
	case "завтра", "завтрашнего":
		return day.AddDate(0, 0, 1), 1
	case "послезавтра":
		return day.AddDate(0, 0, 2), 1
	case "конца":
		if i+1 < len(ws) {
			switch ws[i+1] {
			case "недели":
				return nextWeekday(day, time.Friday, true), 2
			case "месяца":
				return time.Date(day.Year(), day.Month()+1, 0, 0, 0, 0, 0, day.Location()), 2
			case "дня":
				return day, 2
			}
		}
	case "следующей", "следующего", "следующую", "следующий":
		if i+1 < len(ws) {
			if ws[i+1] == "недели" || ws[i+1] == "неделе" {
				return nextWeekday(day, time.Monday, false).AddDate(0, 0, 0), 2
			}
			if wd, ok := weekdays[ws[i+1]]; ok {
				// «в следующую пятницу»: the one of next week
				t := nextWeekday(day, wd, false)
				if isoMon(t).Equal(isoMon(day)) {
					t = t.AddDate(0, 0, 7)
				}
				return t, 2
			}
		}
	case "через":
		n, u := cardinal(ws, i+1)
		if u == 0 {
			n, u = 1, 0
		}
		if j := i + 1 + u; j < len(ws) {
			unit := ws[j]
			switch {
			case strings.HasPrefix(unit, "дн") || strings.HasPrefix(unit, "день"):
				return day.AddDate(0, 0, n), 2 + u
			case strings.HasPrefix(unit, "недел"):
				return day.AddDate(0, 0, 7*n), 2 + u
			case strings.HasPrefix(unit, "месяц"):
				return day.AddDate(0, n, 0), 2 + u
			}
		}
	}
	if wd, ok := weekdays[w]; ok {
		return nextWeekday(day, wd, false), 1
	}
	if m := dmRe.FindStringSubmatch(w); m != nil {
		d, _ := strconv.Atoi(m[1])
		mo, _ := strconv.Atoi(m[2])
		y := day.Year()
		if m[3] != "" {
			y, _ = strconv.Atoi(m[3])
			if y < 100 {
				y += 2000
			}
		}
		if d >= 1 && d <= 31 && mo >= 1 && mo <= 12 {
			t := time.Date(y, time.Month(mo), d, 0, 0, 0, 0, day.Location())
			if m[3] == "" && t.Before(day.AddDate(0, 0, -60)) {
				t = t.AddDate(1, 0, 0)
			}
			return t, 1
		}
	}
	if d, u := ordinal(ws, i); u > 0 {
		j := i + u
		if j < len(ws) && ws[j] == "числа" {
			t := time.Date(day.Year(), day.Month(), d, 0, 0, 0, 0, day.Location())
			if t.Before(day) {
				t = t.AddDate(0, 1, 0)
			}
			return t, u + 1
		}
		if j < len(ws) {
			if mo, ok := months[ws[j]]; ok {
				t := time.Date(day.Year(), mo, d, 0, 0, 0, 0, day.Location())
				if t.Before(day.AddDate(0, 0, -60)) {
					t = t.AddDate(1, 0, 0)
				}
				return t, u + 1
			}
		}
	}
	return time.Time{}, 0
}

// isoMon: the Monday of the date's week.
func isoMon(t time.Time) time.Time {
	return t.AddDate(0, 0, -((int(t.Weekday()) + 6) % 7))
}

// nextWeekday: the next such weekday after today (today itself when
// orToday and today is that day).
func nextWeekday(day time.Time, wd time.Weekday, orToday bool) time.Time {
	d := (int(wd) - int(day.Weekday()) + 7) % 7
	if d == 0 && !orToday {
		d = 7
	}
	return day.AddDate(0, 0, d)
}

// Cap: first letter upper case.
func Cap(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return s
	}
	r := []rune(s)
	r[0] = unicode.ToUpper(r[0])
	return string(r)
}
