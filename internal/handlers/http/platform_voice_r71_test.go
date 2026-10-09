package http

import (
	"strings"
	"testing"
)

// R71: «некоторые слова произносит неправильно, например "трЭкинг"». The
// dictionary reads the shortcut, the deadline, the fine, P&L and the
// stressed words the model misses; the «трекинг» phrases get a new take.
func TestR71SpeakText(t *testing.T) {
	a := "́"
	for in, want := range map[string]string{
		"Разделы здесь. Ctrl K открывает поиск.":                        "Разделы здесь. Контрол Кей открывает поиск.",
		"Учёт. Деньги клуба: PL, движение денег.":                       "Учёт. Деньги клуба: Пи энд Эл, движение денег.",
		"Каждый день до 22:00 отмечаете. Опоздание стоит 10 000 тенге.": "Каждый день до двадцати двух часов отмечаете. Опоздание стоит десять тысяч те" + a + "нге.",
		"Продажи. CRM лидов, рефералы и скрипты.":                       "Продажи. Си Ар Эм лидов, рефера" + a + "лы и скрипты.",
		"Войдите через Телеграм.":                                       "Войдите через Телегра" + a + "м.",
		"Клиент звонит, договор подписан, облегчить работу.":            "Клиент звони" + a + "т, догово" + a + "р подписан, облегчи" + a + "ть работу.",
		"Коворкинг Достык. Ваша пятерка и пятёрка.":                     "Коворкинг Досты" + a + "к. Ваша пятёрка и пятёрка.",
		"Отчёт в Kaspi и WhatsApp, KPI.":                                "Отчёт в Ка" + a + "спи и Уотса" + a + "п, Кей Пи Ай.",
		"Цикл десять дней, задания цикла.":                              "Цикл десять дней, задания цикла.",
	} {
		if got := SpeakText(in); got != want {
			t.Errorf("SpeakText(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestR71Retake(t *testing.T) {
	v := premiumVoice{ID: "v1", Model: "eleven_multilingual_v2"}
	tr := "Трекинг. Здесь живут доски разборов."
	if k := spokenKey(tr); !strings.HasSuffix(k, "\x00take:r71") || !strings.HasPrefix(k, SpeakText(tr)) {
		t.Fatalf("трекинг: key text %q", k)
	}
	if premiumKey(v, spokenKey(tr)) == premiumKey(v, SpeakText(tr)) {
		t.Fatal("the трекинг phrase keeps its old take")
	}
	for _, s := range []string{"Отчёт. Каждый день.", "Трекер видит то же самое.", "Ваши трекеры подстроятся."} {
		if strings.Contains(strings.ToLower(s), "трек") != strings.Contains(spokenKey(s), "take:") {
			t.Errorf("%q: retake %v", s, strings.Contains(spokenKey(s), "take:"))
		}
	}
	// a phrase without the marked words keeps its file
	if s := "Клуб. Резиденты, встречи, отчёты и штрафы."; spokenKey(s) != SpeakText(s) {
		t.Errorf("%q: key text changed", s)
	}
	// the text sent to the voice has no salt
	if strings.Contains(SpeakText(tr), "take") {
		t.Fatal("salt in the spoken text")
	}
}
