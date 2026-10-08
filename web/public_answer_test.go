package web

import (
	"strings"
	"testing"
)

// R62: every open card leads with the question an owner asks an AI and a
// direct answer of 25-60 words (the BLUF tactic from the owner's link), in
// the page, in JSON-LD and in llms-full.txt.
func TestCardAnswers(t *testing.T) {
	l := publicLib()
	if len(l.items) == 0 {
		t.Fatal("no library")
	}
	got := 0
	for i := range l.items {
		it := &l.items[i]
		q, a, ok := cardAnswer(it)
		if !ok {
			continue
		}
		got++
		if n := words(a); n < answerMinWords || n > answerMaxWords {
			t.Errorf("%s: %d words", it.Title, n)
		}
		if !strings.HasSuffix(q, "?") || words(q) < 5 {
			t.Errorf("%s: question %q", it.Title, q)
		}
		if strings.ContainsAny(q+a, "—") {
			t.Errorf("%s: em dash", it.Title)
		}
		if strings.Contains(q, "??") || strings.Contains(q, ".?") {
			t.Errorf("%s: punctuation %q / %q", it.Title, q, a)
		}
		if i < 2 || i == len(l.items)-1 || i%60 == 0 {
			t.Logf("%s\n  Q: %s\n  A (%d): %s", it.Title, q, words(a), a)
		}
	}
	if got*10 < len(l.items)*9 {
		t.Errorf("answers on %d of %d cards", got, len(l.items))
	}
	r := publicRouter()
	it := l.items[0]
	q, a, _ := cardAnswer(&it)
	body := get(t, r, "/library/"+it.Slug).Body.String()
	h1 := strings.Index(body, "<h1>")
	ans := strings.Index(body, `id="answer"`)
	what := strings.Index(body, `id="what"`)
	if h1 < 0 || ans < h1 || (what > 0 && ans > what) {
		t.Errorf("the answer must stand right under the title: h1 %d answer %d what %d", h1, ans, what)
	}
	nodes := ldGraph(t, body)
	if len(byType(nodes, "FAQPage")) != 1 {
		t.Error("no FAQPage for the answer")
	}
	if !strings.Contains(body, hx(q)) {
		t.Error("question not on the page")
	}
	full := get(t, r, "/llms-full.txt").Body.String()
	if !strings.Contains(full, "Отвечает на вопрос: "+q) {
		t.Error("llms-full.txt has no question")
	}
	if !strings.Contains(body, hx(a)) {
		t.Error("answer not on the page")
	}
}

func TestLowerFirstAndSentences(t *testing.T) {
	if !startsWithVerb("За 3 часа подвести итоги года") || !startsWithVerb("Собрать клиентов в одной таблице") || startsWithVerb("Сессия на 90 минут с командой") {
		t.Error("startsWithVerb")
	}
	for in, want := range map[string]string{"Прибыль есть": "прибыль есть", "CRM нет": "CRM нет", "KPI": "KPI", "Я": "Я"} {
		if got := lowerFirst(in); got != want {
			t.Errorf("%q: %q", in, got)
		}
	}
	if s := sentences("Раз. Два 3,5 млн ₸! Три"); len(s) != 3 || s[1] != "Два 3,5 млн ₸!" {
		t.Errorf("%q", s)
	}
}

// The club's FAQ follows the same rule: a direct answer of at most 60 words.
func TestAboutFAQAnswersShort(t *testing.T) {
	for _, f := range bsProfile.FAQ {
		if n := words(f.A); n > answerMaxWords {
			t.Errorf("%q: %d words", f.Q, n)
		}
		if strings.Contains(f.Q+f.A, "—") {
			t.Errorf("%q: em dash", f.Q)
		}
	}
}
