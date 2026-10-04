package web

import (
	"encoding/json"
	"testing"
)

// R32b: у резидента четыре раздела; чек-листы стали вкладкой Инструментов,
// «Заметки» маркетинга скрыты (в приложении пункт тоже пропадает).
func TestR32bNav(t *testing.T) {
	var got struct {
		Blocks []NavBlock `json:"blocks"`
	}
	if err := json.Unmarshal(PlatformNav(), &got); err != nil {
		t.Fatal(err)
	}
	tabs := map[string][]string{}
	for _, b := range got.Blocks {
		for _, x := range b.Tabs {
			tabs[b.Key] = append(tabs[b.Key], x.ID)
		}
	}
	for _, k := range []string{"rtrack", "rbiz", "rclub", "rme"} {
		if len(tabs[k]) < 4 {
			t.Errorf("resident block %s: %v", k, tabs[k])
		}
	}
	for _, k := range []string{"rtask", "rrep", "rcont", "rfive", "rmeas", "rbs"} {
		if _, ok := tabs[k]; ok {
			t.Errorf("old resident block %s still there", k)
		}
	}
	for k, ids := range tabs {
		for _, id := range ids {
			if id == "mNotes" || id == "guides" {
				t.Errorf("%s still has %s", k, id)
			}
		}
	}
}
