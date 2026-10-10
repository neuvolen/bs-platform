package content

import (
	"strings"
	"testing"
)

// R83: «5 букв продажи» is shipped (ext + rich with the call scoring sheet),
// linked from the sales diagnoses; the @Fantastik_12 materials are external
// cards with their t.me link and a source; no em dash, no banned words.
func TestR83SalesLettersAndExternal(t *testing.T) {
	ext, err := LibExt()
	if err != nil {
		t.Fatal(err)
	}
	const title = "5 букв продажи: ВП, ПП, Д, НД, ПКС"
	tool := RichToolByID("tl_r83_5bukv")
	if tool == nil || tool.Title != title || tool.Template == nil || len(tool.Template.Blocks) < 3 {
		t.Fatal("no rich tool «5 букв продажи»")
	}
	cols := strings.Join(tool.Template.Blocks[1].Columns, "|")
	for _, c := range []string{"Менеджер", "Клиент", "Ссылка на сделку", "Длит.", "ВП", "ПП", "Д", "НД", "ПКС", "Балл"} {
		if !strings.Contains("|"+cols+"|", "|"+c+"|") {
			t.Fatalf("scoring sheet lacks %q: %s", c, cols)
		}
	}
	ids := map[string]map[string]any{}
	for _, x := range ext.Tools {
		id, _ := x["id"].(string)
		ids[id] = x
	}
	if ids["tl_r83_5bukv"] == nil {
		t.Fatal("not in the extension")
	}
	posts := map[string]bool{}
	n := 0
	for id, x := range ids {
		if !strings.HasPrefix(id, "ext_f12_") {
			continue
		}
		n++
		link, _ := x["link"].(string)
		if x["isExt"] != true || !strings.HasPrefix(link, "https://t.me/Fantastik_12/") || x["source"] == "" || x["srcBadge"] != "Telegram" || posts[link] {
			t.Fatalf("external %s: %v", id, x)
		}
		posts[link] = true
	}
	if n != 10 {
		t.Fatalf("external cards %d", n)
	}
	for _, x := range append([]map[string]any{ids["tl_r83_5bukv"]}, func() []map[string]any {
		var l []map[string]any
		for id, x := range ids {
			if strings.HasPrefix(id, "ext_f12_") {
				l = append(l, x)
			}
		}
		return l
	}()...) {
		s := strings.ToLower(strings.Join(func() []string {
			var o []string
			for _, k := range []string{"title", "short", "why", "example", "time"} {
				v, _ := x[k].(string)
				o = append(o, v)
			}
			return o
		}(), " "))
		for _, bad := range []string{"—", "хирург", "операци", "прокача", "гарантированн", "в 100% продажа"} {
			if strings.Contains(s, bad) {
				t.Fatalf("%v: %q", x["id"], bad)
			}
		}
	}
	linked := 0
	for _, id := range []string{"seed_dx_3", "seed_dx_16", "dx_sal_02", "konsp_dx_everyone", "dx3_sal_02", "dx3_sal_03", "dx4_sal_03"} {
		d, ok := RichDiagByID(id)
		for _, c := range d.Cure {
			if ok && c == title {
				linked++
			}
		}
	}
	if linked < 7 {
		t.Fatalf("linked from %d diagnoses", linked)
	}
}
