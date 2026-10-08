package web

import (
	"encoding/json"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/bnursik/business_surgery_backend/internal/content"
)

// R60: «Ещё на видео упор на 99 чек-листов, но это же мало: у нас 1000+
// бизнес-идей, много диагнозов, инструментов». The login page and its demo
// show the library's real scale; a number there never exceeds what the
// library holds (diagnoses and tools of the rich library, the ideas catalog).
func TestR60LoginScale(t *testing.T) {
	tools, diag := content.RichTitles()
	real := map[string]int{"diag": len(diag), "tools": len(tools), "ideas": content.IdeasTotal()}
	// the voiced demo says «больше ста семидесяти диагнозов, больше двухсот
	// пятидесяти инструментов и больше тысячи бизнес-идей»
	if real["ideas"] <= 1000 || real["diag"] <= 170 || real["tools"] <= 250 {
		t.Fatalf("library shrank below the voiced demo: %v, change the demo lines", real)
	}
	page := string(loginPage.plain)
	if strings.Contains(page, "{{scale:") {
		t.Fatal("a {{scale:...}} token left in the served page")
	}
	num := func(s string) int {
		n, _ := strconv.Atoi(strings.NewReplacer("&nbsp;", "", " ", "", " ", "").Replace(s))
		return n
	}
	ms := regexp.MustCompile(`<b data-scale="(\w+)">([^<]+)</b>`).FindAllStringSubmatch(page, -1)
	if len(ms) != 3 {
		t.Fatalf("scale strip: %d numbers, want 3", len(ms))
	}
	for _, m := range ms {
		n := num(m[2])
		if n <= 0 || n > real[m[1]] {
			t.Errorf("%s: page says %d, library has %d", m[1], n, real[m[1]])
		}
		// rounded down, never stale by more than 10%
		if n != real[m[1]] {
			t.Errorf("%s: page says %d, library has %d: update the number", m[1], n, real[m[1]])
		}
	}
	// the same numbers in the showcase, the demo's captions, the FAQ of /about and llms.txt
	all := page + "\n" + llmsTxt()
	for _, f := range bsProfile.FAQ {
		all += "\n" + f.A
	}
	for _, re := range []string{`(\d+) диагноз`, `(\d+) инструмент`, `([\d  ]+) бизнес-иде`} {
		for _, m := range regexp.MustCompile(re).FindAllStringSubmatch(strings.ReplaceAll(strings.ReplaceAll(all, "&nbsp;", " "), "\u00a0", " "), -1) {
			n := num(m[1])
			k := map[string]string{`(\d+) диагноз`: "diag", `(\d+) инструмент`: "tools", `([\d  ]+) бизнес-иде`: "ideas"}[re]
			if n > real[k] {
				t.Errorf("%q: more than the library's %d", m[0], real[k])
			}
		}
	}
	if strings.Contains(page, "99 чек-листов") || strings.Contains(page, "99&nbsp;чек-лист") {
		t.Error("login page still leads with 99 чек-листов")
	}

	// Demo order: how a разбор goes, diagnoses, the cost of not treating,
	// tools, help with implementation, then measurements, Gallup, the group, tracking, CTA
	tm := loginTourRe.FindSubmatch(loginPage.plain)
	var d struct {
		Steps []struct{ T, V string } `json:"steps"`
	}
	if err := json.Unmarshal(tm[1], &d); err != nil {
		t.Fatal(err)
	}
	order := []string{"Как проходит разбор", "Доска разбора", "Диагноз", "диагноз", "если не лечить", "инструмент", "внедрить", "Колесо", "Gallup", "Окружение", "Трекинг", "Посмотрим"}
	if len(d.Steps) != len(order) {
		t.Fatalf("%d steps, want %d", len(d.Steps), len(order))
	}
	chars := 0
	for i, s := range d.Steps {
		if !strings.Contains(s.T, order[i]) {
			t.Errorf("step %d is %q, want %q", i+1, s.T, order[i])
		}
		chars += len([]rune(strings.Join(strings.Fields(s.V), " ")))
		for _, bad := range []string{"—", "99"} {
			if strings.Contains(s.V, bad) {
				t.Errorf("step %d line has %q", i+1, bad)
			}
		}
	}
	if chars > 6000 {
		t.Errorf("demo lines: %d characters of ElevenLabs, keep ≤ 6000", chars)
	}
}
