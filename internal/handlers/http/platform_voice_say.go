package http

import (
	"strings"
	"unicode"
	"unicode/utf8"
)

// R62: «Слово "Замер": ударение не туда в озвучке».
//
// ElevenLabs (eleven_multilingual_v2) often puts the Russian stress on the
// wrong syllable and reads Latin names and abbreviations the English way.
// SpeakText is the text that goes to ElevenLabs: the stressed vowel gets a
// combining acute accent (U+0301, «заме́р»), a name is written as it sounds
// («Гэ́ллап»). Only the voice changes: the page shows and keys its phrases by
// the original text. The file name hashes the spoken text, so a phrase the
// dictionary changes is read again once, the others keep their files.

const stress = "\u0301"

// sayPhrases: whole phrases first (a word that needs its context).
var sayPhrases = []struct{ from, to string }{
	{"рекомендации ИИ", "рекомендации искусственного интеллекта"},
	// R71: the shortcut, the deadline and the fine are read as words
	{"Ctrl K", "Контрол Кей"},
	{"до 22:00", "до двадцати двух часов"},
	{"10 000 тенге", "десять тысяч те" + stress + "нге"},
}

// sayWords: a word (lower case, the forms in use) → how to say it. The first
// letter keeps its case in the text.
var sayWords = func() map[string]string {
	m := map[string]string{
		// замер: stress on the second syllable, the model says «за́мер»
		"замер": "заме" + stress + "р", "замеры": "заме" + stress + "ры", "замера": "заме" + stress + "ра",
		"замеров": "заме" + stress + "ров", "замерам": "заме" + stress + "рам", "замерами": "заме" + stress + "рами",
		"замерах": "заме" + stress + "рах", "замере": "заме" + stress + "ре", "замеру": "заме" + stress + "ру",
		// разбор: «разбо́р» (the model sometimes says «ра́збор»)
		"разбор": "разбо" + stress + "р", "разбора": "разбо" + stress + "ра", "разборы": "разбо" + stress + "ры",
		"разборов": "разбо" + stress + "ров", "разборе": "разбо" + stress + "ре", "разбору": "разбо" + stress + "ру",
		"разборам": "разбо" + stress + "рам", "разборами": "разбо" + stress + "рами", "разборах": "разбо" + stress + "рах",
		// трекинг, трекер: R74 «трЭ так и нужно»: твёрдое «трэ́кинг», «трэ́кер» (как говорит владелец)
		"трекинг": "трэ" + stress + "кинг", "трекинга": "трэ" + stress + "кинга", "трекингу": "трэ" + stress + "кингу",
		"трекингом": "трэ" + stress + "кингом", "трекинге": "трэ" + stress + "кинге",
		"трекер": "трэ" + stress + "кер", "трекера": "трэ" + stress + "кера", "трекеры": "трэ" + stress + "керы",
		"трекеров": "трэ" + stress + "керов", "трекеру": "трэ" + stress + "керу", "трекером": "трэ" + stress + "кером",
		"трекерам": "трэ" + stress + "керам", "трекерами": "трэ" + stress + "керами", "трекерах": "трэ" + stress + "керах",
		"трекере": "трэ" + stress + "кере", "трекинговый": "трэ" + stress + "кинговый", "трекинговая": "трэ" + stress + "кинговая",
		"трекинговые": "трэ" + stress + "кинговые", "трекингового": "трэ" + stress + "кингового",
		// Алматы: «Алматы́»
		"алматы": "Алматы" + stress,
		// R63: Береке (трекер): мягкое Е, ударение на последний слог, «Береке́»
		"береке": "Береке" + stress,
		// саммари: «са́ммари»; тенге: «те́нге»
		"саммари": "са" + stress + "ммари", "тенге": "те" + stress + "нге",
		// Latin: read the Russian way
		"gallup": "Гэ" + stress + "ллап", "crm": "Си Ар Эм", "bs": "Би Эс",
		// R71: P&L, Kaspi, WhatsApp, KPI, Ctrl
		"pl": "Пи энд Эл", "kaspi": "Ка" + stress + "спи", "whatsapp": "Уотса" + stress + "п", "kpi": "Кей Пи Ай",
		"ctrl": "Контрол",
		// R71: Достык (коворкинг), Телеграм
		"достык": "Досты" + stress + "к",
	}
	// R71: the stress the model often misses, every form in use
	forms := func(stem, acc string, ends ...string) {
		for _, e := range ends {
			m[stem+e] = acc + e
		}
	}
	forms("реферал", "рефера"+stress+"л", "", "ы", "а", "ов", "ам", "ами", "ах", "е", "у", "ом")
	forms("телеграм", "телегра"+stress+"м", "", "е", "а", "у", "ом")
	forms("договор", "догово"+stress+"р", "", "а", "ы", "ов", "ам", "ами", "ах", "е", "у", "ом")
	for w, to := range map[string]string{
		"звонит": "звони" + stress + "т", "звонят": "звоня" + stress + "т", "звонишь": "звони" + stress + "шь",
		"звоним": "звони" + stress + "м", "звоните": "звони" + stress + "те",
		"облегчить": "облегчи" + stress + "ть", "облегчит": "облегчи" + stress + "т", "облегчим": "облегчи" + stress + "м",
	} {
		m[w] = to
	}
	// ё where the text was typed with е: «пятёрка» (the model reads «пяте́рка»)
	forms("пятерк", "пятёрк", "а", "и", "е", "у", "ой", "ам", "ами")
	return m
}()

// sayRetake: a phrase whose kept take came out wrong is read again without a
// change of its text: the mark salts the file key (a new take), the text sent
// stays the same. R71 salted «тре́к»; R74 changed the text itself to the hard
// «трэ́кинг» («трЭ так и нужно»), so those phrases get new keys anyway and the
// list is empty.
var sayRetake = []struct {
	mark string
	take string
}{}

// spokenKey: what the file key of a phrase hashes (the spoken text and its
// take).
func spokenKey(t string) string {
	say := SpeakText(t)
	low := strings.ToLower(say)
	for _, r := range sayRetake {
		if strings.Contains(low, r.mark) {
			return say + "\x00take:" + r.take
		}
	}
	return say
}

// SpeakText: the text ElevenLabs reads for a phrase (spaces collapsed).
func SpeakText(t string) string {
	t = strings.Join(strings.Fields(t), " ")
	for _, p := range sayPhrases {
		t = strings.ReplaceAll(t, p.from, p.to)
	}
	var b strings.Builder
	b.Grow(len(t) + 16)
	i := 0
	for i < len(t) {
		r, n := utf8.DecodeRuneInString(t[i:])
		if !unicode.IsLetter(r) {
			b.WriteString(t[i : i+n])
			i += n
			continue
		}
		j := i
		for j < len(t) {
			r2, n2 := utf8.DecodeRuneInString(t[j:])
			if !unicode.IsLetter(r2) && r2 != '\u0301' {
				break
			}
			j += n2
		}
		w := t[i:j]
		b.WriteString(sayWord(w))
		i = j
	}
	return b.String()
}

func sayWord(w string) string {
	if strings.Contains(w, stress) {
		return w // already marked by the author
	}
	to, ok := sayWords[strings.ToLower(w)]
	if !ok {
		return w
	}
	// Latin names and abbreviations: the replacement as written
	if r, _ := utf8.DecodeRuneInString(w); r < 0x80 {
		return to
	}
	first, n := utf8.DecodeRuneInString(to)
	if r, _ := utf8.DecodeRuneInString(w); unicode.IsUpper(r) {
		return string(unicode.ToUpper(first)) + to[n:]
	}
	return string(unicode.ToLower(first)) + to[n:]
}
