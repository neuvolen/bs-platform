package http

import (
	"strings"
	"testing"

	"github.com/bnursik/business_surgery_backend/web"
)

// R74: «трЭ так и нужно же». The owner wants the hard «трэ́кинг», «трэ́кер»:
// every phrase with these words is spoken with «трэ́» and gets a new file key
// (the tour and the login demo are read again), the others keep their files.
func TestR74HardTre(t *testing.T) {
	a := "́"
	for in, want := range map[string]string{
		"Трекинг. Здесь живут доски разборов.":    "Трэ" + a + "кинг. Здесь живут доски разбо" + a + "ров.",
		"Трекер видит то же самое, трекеры тоже.": "Трэ" + a + "кер видит то же самое, трэ" + a + "керы тоже.",
		"Напишите трекеру или трекерам.":          "Напишите трэ" + a + "керу или трэ" + a + "керам.",
	} {
		if got := SpeakText(in); got != want {
			t.Errorf("SpeakText(%q) = %q, want %q", in, got, want)
		}
	}
	v := premiumVoice{ID: "v1", Model: "eleven_multilingual_v2"}
	lines := append(append([]string{}, web.TourTexts()...), web.LoginLines()...)
	n := 0
	for _, s := range lines {
		s = strings.Join(strings.Fields(s), " ")
		low, say := strings.ToLower(s), strings.ToLower(SpeakText(s))
		if strings.Contains(say, "трек") || strings.Contains(say, "тре"+a+"к") {
			t.Errorf("%q: soft «трек» left in %q", s, say)
		}
		if !strings.Contains(low, "трек") {
			if spokenKey(s) != SpeakText(s) {
				t.Errorf("%q: key salted", s)
			}
			continue
		}
		n++
		// R71 keys (soft accent + take salt) are not this phrase's key any more
		old := strings.ReplaceAll(SpeakText(s), "трэ"+a, "тре"+a)
		old = strings.ReplaceAll(old, "Трэ"+a, "Тре"+a)
		if premiumKey(v, spokenKey(s)) == premiumKey(v, old+"\x00take:r71") || premiumKey(v, spokenKey(s)) == premiumKey(v, old) {
			t.Errorf("%q keeps its R71 file", s)
		}
		t.Logf("re-voiced: %s", SpeakText(s))
	}
	if n == 0 {
		t.Fatal("no tour or login line with трекинг/трекер")
	}
	t.Logf("%d lines with трекинг/трекер", n)
}
