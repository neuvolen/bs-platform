package http

import (
	"context"
	"encoding/json"
	"regexp"
	"strings"
	"testing"
	"time"
)

func TestThreadsReachSettings(t *testing.T) {
	for raw, want := range map[string]int{
		`{}`:                                   threadsReachDef,
		`{"channels":{"threads":{"on":true}}}`: 65,
		`{"channels":{"threads":{"on":true,"reachPct":0}}}`:  0,
		`{"channels":{"threads":{"on":true,"reachPct":40}}}`: 40,
		`{"channels":{"threads":{"on":true,"reachPct":95}}}`: threadsReachMax,
		`{"channels":{"threads":{"on":true,"reachPct":-5}}}`: 0,
	} {
		if got := parseContentSettings(json.RawMessage(raw)).threadsReach(); got != want {
			t.Fatalf("%s: reach %d, want %d", raw, got, want)
		}
	}
	// the setting survives a save (the platform writes the doc back as JSON)
	s := parseContentSettings(json.RawMessage(`{"channels":{"threads":{"on":true,"reachPct":0}}}`))
	b, _ := json.Marshal(s)
	if !strings.Contains(string(b), `"reachPct":0`) {
		t.Fatalf("reachPct 0 lost on save: %s", b)
	}
}

func TestThreadsReachOffIsClassic(t *testing.T) {
	for n := 1; n <= threadsPerDayMax; n++ {
		f, c := threadsDayPlan(n, "20261007", 0)
		cf := threadsDayFormats(n, "20261007")
		cc := threadsCTASlots(n, cf)
		if strings.Join(f, ",") != strings.Join(cf, ",") {
			t.Fatalf("n=%d: reach 0 must keep the classic formats", n)
		}
		for i := range c {
			if c[i] != cc[i] {
				t.Fatalf("n=%d: reach 0 must keep the classic CTA", n)
			}
		}
	}
}

func TestThreadsReachPyramid(t *testing.T) {
	for _, pct := range []int{20, 50, 65, 80} {
		for n := 1; n <= threadsPerDayMax; n++ {
			f, cta := threadsDayPlan(n, "20261007", pct)
			if len(f) != n || len(cta) != n {
				t.Fatalf("pct %d n=%d: %d formats %d cta", pct, n, len(f), len(cta))
			}
			reach, ctaN := 0, 0
			for i, x := range f {
				if threadsFormatByID[x] == nil {
					t.Fatalf("pct %d n=%d: unknown format %q", pct, n, x)
				}
				if IsThreadsReach(x) {
					reach++
					if cta[i] {
						t.Fatalf("pct %d n=%d: a reach post sells (%s)", pct, n, x)
					}
				}
				if cta[i] {
					ctaN++
					if x == "question" || i == 0 {
						t.Fatalf("pct %d n=%d: CTA on %d %s", pct, n, i, x)
					}
				}
				if i > 0 && f[i-1] == x {
					t.Fatalf("pct %d n=%d: %s twice in a row: %v", pct, n, x, f)
				}
			}
			if reach != threadsReachCount(n, pct) {
				t.Fatalf("pct %d n=%d: %d reach posts", pct, n, reach)
			}
			if n >= 2 && (reach < 1 || reach > n-1 || ctaN < 1) {
				t.Fatalf("pct %d n=%d: reach %d, CTA %d", pct, n, reach, ctaN)
			}
			if n >= 2 && !IsThreadsReach(f[0]) {
				t.Fatalf("pct %d n=%d: the day must open with a reach post: %v", pct, n, f)
			}
			if n >= 8 {
				if r := float64(ctaN) / float64(n); r > 0.2 {
					t.Fatalf("pct %d n=%d: %d CTA posts, the offer layer is about 10%%", pct, n, ctaN)
				}
			}
		}
	}
	// 16 a day at 65%: 10 reach posts, every reach rubric at least once, 2 CTA
	f, cta := threadsDayPlan(16, "20261007", 65)
	seen, c := map[string]bool{}, 0
	for i, x := range f {
		if IsThreadsReach(x) {
			seen[x] = true
		}
		if cta[i] {
			c++
		}
	}
	if len(seen) != len(threadsReachFormats) || c != 2 {
		t.Fatalf("16 at 65%%: rubrics %v, CTA %d", seen, c)
	}
	// the manual mode (4 a day): 3 reach + 1 trust post with the link
	f4, c4 := threadsDayPlan(4, "20261007", 65)
	n4 := 0
	for i := range f4 {
		if c4[i] {
			n4++
			if IsThreadsReach(f4[i]) {
				t.Fatalf("4 a day: the CTA on a reach post: %v %v", f4, c4)
			}
		}
	}
	if threadsReachCount(4, 65) != 3 || n4 != 1 {
		t.Fatalf("4 a day: %v %v", f4, c4)
	}
	if g, _ := threadsDayPlan(16, "20261008", 65); strings.Join(g, ",") == strings.Join(f, ",") {
		t.Fatal("the reach rubrics must take turns by day")
	}
}

func TestThreadsReachSeedsAndNames(t *testing.T) {
	seeds := threadsReachSeeds()
	kinds := map[string]int{}
	ids := map[string]bool{}
	bad := regexp.MustCompile(`(?i)хирург|операци|прокач|гарантирован|—`)
	for _, s := range seeds {
		kinds[s.Kind]++
		if ids[s.ID] {
			t.Fatalf("seed id twice: %s", s.ID)
		}
		ids[s.ID] = true
		if s.Organ == "" || s.Title == "" || len([]rune(s.Text)) < 60 || len([]rune(s.Text)) > 700 {
			t.Fatalf("seed %s: organ %q title %q text %d", s.ID, s.Organ, s.Title, len([]rune(s.Text)))
		}
		if bad.MatchString(s.Text + s.Title) {
			t.Fatalf("seed %s: banned word or dash", s.ID)
		}
		if thStatRe.MatchString(s.Text) {
			t.Fatalf("seed %s: reads like an unsourced statistic: %s", s.ID, s.Text)
		}
	}
	for _, f := range threadsReachFormats {
		if ThreadsFormatName(f.ID) != f.Name || !strings.HasPrefix(f.Brief, "Охват.") || bad.MatchString(f.Brief) {
			t.Fatalf("format %s", f.ID)
		}
		has := false
		for _, k := range f.Kinds {
			if kinds[k] > 0 {
				has = true
			}
		}
		if !has && f.ID != "r_closing" {
			t.Fatalf("format %s has no reach seeds of its own", f.ID)
		}
	}
	if kinds["r_earn"] < 8 {
		t.Fatalf("too few unit-economics topics: %d", kinds["r_earn"])
	}
	// pickSeeds gives a reach slot a reach seed (a day of only reach rubrics, empty memory)
	mem := &threadsMemory{src: map[string]time.Time{}, root: map[string]time.Time{}, organ: map[string]int{}, first: map[string]bool{}}
	got := pickSeeds(seeds, []string{"r_earn", "r_myth", "r_debate"}, mem, "20261007", "")
	for i, s := range got {
		if s == nil || !strings.HasPrefix(s.ID, "reach/") {
			t.Fatalf("slot %d: %v", i, s)
		}
	}
	if !strings.Contains(threadsSystem, "Охват") {
		t.Fatal("the system prompt must explain the reach rubrics")
	}
}

// The day's batch at the default pyramid: reach rubrics with their own
// material, the link to the 99 checklists only in trust posts.
func TestThreadsReachBatchBuild(t *testing.T) {
	ctx := context.Background()
	docs := &cntDocs{}
	old := thReachPct
	thReachPct = nil // unset: the default 65%
	defer func() { thReachPct = old }()
	thDoc(t, docs, 16)
	now := time.Date(2026, 10, 5, 6, 30, 0, 0, almaty)
	e := cntEngine(docs, &now)
	e.Lib = thLib
	fai := &thFakeAI{bad: map[int]string{}}
	e.AI = fai.call
	res, err := e.BuildThreadsDay(ctx, now, false)
	if err != nil {
		t.Fatal(err)
	}
	items := thItems(docs.doc(t), "20261005")
	if res.Added != 16 || len(items) != 16 {
		t.Fatalf("build: %+v", res)
	}
	p := fai.prompts[0]
	for _, w := range []string{"Охват.", "Сколько зарабатывает", "Допущения", "Охват» пишется"} {
		if !strings.Contains(p, w) && !strings.Contains(threadsSystem, w) {
			t.Fatalf("prompt without %q", w)
		}
	}
	reach, reachSeed, cta := 0, 0, 0
	for _, it := range items {
		if IsThreadsReach(it.Format) {
			reach++
			if strings.HasPrefix(it.Src, "reach/") {
				reachSeed++
			}
			if it.CTA || strings.Contains(it.Text, "t.me/") {
				t.Fatalf("a reach post with the link: %+v", it.contentItemData)
			}
		}
		if it.CTA {
			cta++
		}
	}
	if reach != 10 || reachSeed < 5 || cta != 2 {
		t.Fatalf("reach %d (own topics %d), CTA %d: %+v", reach, reachSeed, cta, res)
	}
}
