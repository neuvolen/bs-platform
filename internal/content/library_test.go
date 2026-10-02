package content

import (
	"encoding/json"
	"strings"
	"testing"
)

// Whatever the library holds now must load: guides with their organ and
// title, every text with the bot link of its channel.
func TestLibrary(t *testing.T) {
	seen := map[string]bool{}
	for _, it := range Library() {
		if seen[it.Src] {
			t.Fatalf("%s twice", it.Src)
		}
		seen[it.Src] = true
		if it.Rubric == "" && (it.Organ == "" || it.Title == "" || GuideTitle(it.Src) == "") {
			t.Errorf("%s: guide without organ or title", it.Src)
		}
		for _, s := range it.Threads {
			if p := StartParam(s); !strings.HasPrefix(p, "th_") {
				t.Errorf("%s: threads link %q", it.Src, p)
			}
		}
		if it.Telegram != "" && !strings.HasPrefix(StartParam(it.Telegram), "tg_") {
			t.Errorf("%s: telegram link %q", it.Src, StartParam(it.Telegram))
		}
		if LibraryItem(it.Src) != it {
			t.Errorf("%s: lookup", it.Src)
		}
	}
	t.Logf("library: %d items", len(Library()))
}

func TestLibraryRubricItem(t *testing.T) {
	var raw map[string]json.RawMessage
	_ = json.Unmarshal([]byte(`{"id":"case_isfandiyar","rubric":"cases","threads":[{"text":"Исфандияр\nс 200 тыс до 2 млн ₸. t.me/bsurgery_bot?start=th_case"}],
		"telegram":{"text":"Кейс Исфандияра t.me/bsurgery_bot?start=tg_case"},"reels":{"hook":"Хук","beats":["a"],"cta":"b","duration":35}}`), &raw)
	it := parseLibItem("cases.json", raw)
	if it == nil || it.Src != "case_isfandiyar" || it.Rubric != "case" || it.Title != "Кейс Исфандияра" ||
		!it.Has("threads") || !it.Has("reels") || it.Has("carousel") || it.Variants("threads") != 1 {
		t.Fatalf("%+v", it)
	}
	_ = json.Unmarshal([]byte(`{"guide":"g001","threads":[{"text":"a"},{"text":"b"}]}`), &raw)
	g := parseLibItem("x.json", map[string]json.RawMessage{"guide": raw["guide"], "threads": raw["threads"]})
	if g.Organ != "Финансы" || g.Title != GuideTitle("g001") || g.Variants("threads") != 2 {
		t.Fatalf("%+v", g)
	}
	if LinkTitle("case") != "Кейсы" || LinkTitle("g001") != GuideTitle("g001") || LinkTitle("a_b") != "a b" {
		t.Fatal("link titles")
	}
}
