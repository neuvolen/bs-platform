package web

import (
	"encoding/json"
	"testing"
)

func TestPlatformNav(t *testing.T) {
	var got struct {
		Blocks  []NavBlock `json:"blocks"`
		Aliases []string   `json:"aliases"`
	}
	if err := json.Unmarshal(PlatformNav(), &got); err != nil {
		t.Fatal(err)
	}
	ids := map[string]bool{}
	keys := map[string]bool{}
	for _, b := range got.Blocks {
		keys[b.Key] = true
		for _, x := range b.Tabs {
			ids[x.ID] = true
		}
	}
	for _, k := range []string{"track", "club", "fin", "sales", "mkt", "lib", "rme"} {
		if !keys[k] {
			t.Errorf("block %s missing", k)
		}
	}
	for _, id := range []string{"fines", "aSched", "pl", "leads", "events", "aiRec", "rFines"} {
		if !ids[id] {
			t.Errorf("tab %s missing", id)
		}
	}
	al := map[string]bool{}
	for _, a := range got.Aliases {
		al[a] = true
	}
	if !al["useful"] || !al["sCA"] {
		t.Errorf("aliases: %v", got.Aliases)
	}
	// Removed from the platform: the app must not show them either.
	for _, id := range []string{"gdoc", "gsheet", "parser"} {
		if ids[id] {
			t.Errorf("tab %s should be gone", id)
		}
	}
}

func TestParseNavShape(t *testing.T) {
	page := "x\nvar BLOCKS = {\n  a:   {name:'А', tabs:[\n    {id:'one', n:'Один'},\n    {id:'two',  n:'Два'}]},\n  // note\n  b: {name:'Б', tabs:[\n    {id:'three', n:'Три'}]}\n};\nvar y = {id:'zz', n:'no'};"
	b := parseNav(page)
	if len(b) != 2 || len(b[0].Tabs) != 2 || b[1].Tabs[0].ID != "three" || b[0].Name != "А" {
		t.Fatalf("%+v", b)
	}
}
