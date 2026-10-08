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
		// трекинг, трекер: «тре́кинг», «тре́кер»
		"трекинг": "тре" + stress + "кинг", "трекинга": "тре" + stress + "кинга", "трекингу": "тре" + stress + "кингу",
		"трекингом": "тре" + stress + "кингом", "трекинге": "тре" + stress + "кинге",
		"трекер": "тре" + stress + "кер", "трекера": "тре" + stress + "кера", "трекеры": "тре" + stress + "керы",
		"трекеров": "тре" + stress + "керов", "трекеру": "тре" + stress + "керу", "трекером": "тре" + stress + "кером",
		// Алматы: «Алматы́»
		"алматы": "Алматы" + stress,
		// саммари: «са́ммари»; тенге: «те́нге»
		"саммари": "са" + stress + "ммари", "тенге": "те" + stress + "нге",
		// Latin: read the Russian way
		"gallup": "Гэ" + stress + "ллап", "crm": "Си Ар Эм", "bs": "Би Эс",
	}
	return m
}()

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
