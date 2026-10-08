package web

// R62: «короткий ответ» на каждой открытой карточке библиотеки.
//
// Владелец прислал пост @gorokhov__ (Threads, 08.10.2026): как попасть в
// ответы ChatGPT и ИИ Google. Суть: люди спрашивают ИИ длинными вопросами
// (7+ слов), такие запросы видно в Google Search Console фильтром по
// регулярному выражению; страница попадает в ответ ИИ, когда вопрос стоит
// в начале, а сразу под ним прямой ответ на 40-60 слов (BLUF, «вывод
// вперёд»). Здесь это применено к библиотеке: под заголовком карточки вопрос
// так, как его задаёт собственник («Что делать, если …?», «Как …?»), и ответ
// до 60 слов из самой карточки; тот же вопрос и ответ в JSON-LD (FAQPage),
// вопрос в llms-full.txt (текст ответа там уже есть).

import (
	"strings"
	"unicode"
	"unicode/utf8"
)

const (
	answerMinWords = 25 // shorter: the card has too little text for an answer
	answerMaxWords = 60
)

// lowerFirst: «Прибыль есть» → «прибыль есть», but «CRM есть» and «KPI» stay.
func lowerFirst(s string) string {
	r, n := utf8.DecodeRuneInString(s)
	if r == utf8.RuneError || n == len(s) {
		return s
	}
	r2, _ := utf8.DecodeRuneInString(s[n:])
	if unicode.IsUpper(r2) || r < 0x400 {
		return s
	}
	return string(unicode.ToLower(r)) + s[n:]
}

func trimEnd(s string) string {
	return strings.TrimRight(strings.TrimSpace(s), ".!?…:;, ")
}

// sentences: a text split after . ! ? (abbreviations like «млн ₸» do not end
// with a dot in the library, so the simple split is enough).
func sentences(s string) []string {
	var out []string
	start := 0
	rs := []rune(s)
	for i, r := range rs {
		if (r == '.' || r == '!' || r == '?') && (i+1 == len(rs) || rs[i+1] == ' ') {
			if x := strings.TrimSpace(string(rs[start : i+1])); x != "" {
				out = append(out, x)
			}
			start = i + 1
		}
	}
	if x := strings.TrimSpace(string(rs[start:])); x != "" {
		out = append(out, x)
	}
	return out
}

// startsWithVerb: «Собрать клиентов…», «За 3 часа подвести итоги…»: an
// infinitive among the first 4 words makes a natural «Как …?» question.
func startsWithVerb(s string) bool {
	for i, w := range strings.Fields(s) {
		if i >= 4 {
			break
		}
		w = strings.ToLower(strings.Trim(w, ",:;«»"))
		for _, end := range []string{"ть", "ться", "ти", "чь"} {
			if utf8.RuneCountInString(w) > 3 && strings.HasSuffix(w, end) {
				return true
			}
		}
	}
	return false
}

func words(s string) int { return len(strings.Fields(s)) }

// fitWords: whole parts in order while the answer stays within max words.
func fitWords(parts []string, max int) string {
	var b []string
	n := 0
	for _, p := range parts {
		w := words(p)
		if n+w > max {
			break
		}
		b = append(b, p)
		n += w
	}
	return strings.Join(b, " ")
}

// cardAnswer: the question a card answers and a direct answer of at most 60
// words, ok false when the card has too little text.
func cardAnswer(it *libEntry) (q, a string, ok bool) {
	sub := trimEnd(it.Subtitle)
	if sub == "" {
		return "", "", false
	}
	var parts []string
	if it.Kind == "tool" {
		q = "Что даёт инструмент «" + it.Title + "» и как его применить?"
		if startsWithVerb(sub) {
			q = "Как " + lowerFirst(sub) + "?"
		}
		ps := sentences(it.Promise)
		if len(ps) > 0 {
			parts = append(parts, ps[0])
		}
		var st []string
		for _, s := range it.Steps {
			if t := trimEnd(s.Title); t != "" {
				st = append(st, lowerFirst(t))
			}
		}
		if len(st) > 0 {
			steps := "Шаги: " + strings.Join(st, "; ") + "."
			for words(steps) > 30 && len(st) > 2 {
				st = st[:len(st)-1]
				steps = "Шаги: " + strings.Join(st, "; ") + "; остальные шаги в карточке."
			}
			parts = append(parts, steps)
		}
		if len(ps) > 1 {
			parts = append(parts, ps[1:]...)
		}
	} else {
		q = "Что делать, если " + lowerFirst(sub) + "?"
		ds := sentences(it.Desc)
		if len(ds) > 0 {
			parts = append(parts, "Это диагноз «"+it.Title+"»: "+lowerFirst(ds[0]))
		}
		if len(it.FirstSteps) > 0 {
			parts = append(parts, "Начни с этого: "+lowerFirst(trimEnd(it.FirstSteps[0]))+".")
			for i, s := range it.FirstSteps[1:] {
				parts = append(parts, []string{"Дальше: ", "Потом: ", "И ещё: "}[i%3]+lowerFirst(trimEnd(s))+".")
			}
		}
		if len(ds) > 1 {
			parts = append(parts, ds[1:]...)
		}
	}
	a = fitWords(parts, answerMaxWords)
	if words(a) < answerMinWords {
		return "", "", false
	}
	return q, a, true
}

// answerHTML: the block right under the card's title.
func answerHTML(q, a string) string {
	return `<section id="answer"><div class="w"><div class="box"><h2 style="font-size:22px">` + hx(q) + `</h2><p>` + hx(a) + `</p></div></div></section>` + "\n"
}

// answerLD: the same question for AI and search (schema.org FAQPage).
func answerLD(url, q, a string) obj {
	return obj{"@type": "FAQPage", "@id": url + "#answer", "url": url + "#answer", "inLanguage": "ru",
		"mainEntity": []any{obj{"@type": "Question", "name": q, "acceptedAnswer": obj{"@type": "Answer", "text": a}}}}
}
