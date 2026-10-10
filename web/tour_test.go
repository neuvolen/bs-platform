package web

import (
	"encoding/json"
	"regexp"
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
		"На этом всё", "Резидентство."} {
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

// R81: the resident's tour goes through every section of the R73 menu and
// every tab inside it, each line voiced (premium budget ≤ 4 000 characters).
func TestR81ResidentTourCoversEverySection(t *testing.T) {
	m := tourRe.FindSubmatch(platformHTML)
	if m == nil {
		t.Fatal("no bsTourTexts")
	}
	var d TourDoc
	if err := json.Unmarshal(m[1], &d); err != nil {
		t.Fatal(err)
	}
	var steps []struct{ B, S, T, D string }
	if err := json.Unmarshal(regexp.MustCompile(`(?s)"rsteps":\s*(\[.*?\])`).FindSubmatch(m[1])[1], &steps); err != nil {
		t.Fatal(err)
	}
	if len(steps) != len(d.RSteps) || len(steps) < 25 {
		t.Fatalf("rsteps: %d", len(steps))
	}
	nav := regexp.MustCompile(`(?s)id="navRes"[^>]*>(.*?)</div>\s*</div>`).FindSubmatch(platformHTML)
	if nav == nil {
		t.Fatal("no navRes")
	}
	blocks := regexp.MustCompile(`data-b="(\w+)"`).FindAllSubmatch(nav[1], -1)
	if len(blocks) != 8 {
		t.Fatalf("resident menu: %d sections", len(blocks))
	}
	section := map[string]bool{}
	chars := 0
	texts := map[string]bool{}
	for _, x := range TourTexts() {
		texts[x] = true
	}
	for _, s := range steps {
		chars += len([]rune(s.D))
		if s.S == "" {
			section[s.B] = true
		} else if !strings.Contains(string(platformHTML), "{id:'"+s.S+"'") && !strings.Contains(string(platformHTML), "id: '"+s.S+"'") {
			t.Errorf("tab %s/%s is not in the page", s.B, s.S)
		}
		if !strings.HasPrefix(s.D, s.T) || strings.Contains(s.D, "—") {
			t.Errorf("line %q: starts with its title, no em dash", s.D)
		}
		if !texts[strings.Join(strings.Fields(s.D), " ")] {
			t.Errorf("line %q is not voiced", s.D)
		}
	}
	for _, b := range blocks {
		if !section[string(b[1])] {
			t.Errorf("section %s has no step", b[1])
		}
	}
	if chars > 4000 {
		t.Errorf("resident tour: %d characters, the premium voice budget is 4000", chars)
	}
	for _, k := range []string{"lhome", "lideas"} {
		if d.Nav[k] == "" {
			t.Errorf("lead section %s has no line", k)
		}
	}
}
