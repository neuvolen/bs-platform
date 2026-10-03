package web

import (
	"strings"
	"testing"
)

// The page's tour texts are the server's: every spoken phrase is found.
func TestTourTextsOfThePage(t *testing.T) {
	texts := TourTexts()
	if len(texts) < 25 {
		t.Fatalf("only %d phrases", len(texts))
	}
	has := func(sub string) bool {
		for _, x := range texts {
			if strings.Contains(x, sub) {
				return true
			}
		}
		return false
	}
	for _, sub := range []string{"Я голосовой гид платформы", "Нажмите «Начать обучение»", "Трекинг. Здесь живут доски",
		"Ctrl K", "На этом всё", "Резидентство."} {
		if !has(sub) {
			t.Fatalf("no phrase with %q", sub)
		}
	}
	seen := map[string]bool{}
	for _, x := range texts {
		if seen[x] || strings.TrimSpace(x) != x || x == "" {
			t.Fatalf("phrase %q twice or untrimmed", x)
		}
		seen[x] = true
	}
}
