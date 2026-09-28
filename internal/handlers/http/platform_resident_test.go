package http

import (
	"encoding/json"
	"strings"
	"testing"

	pg "github.com/bnursik/business_surgery_backend/internal/repository/pg"
)

func TestNormName(t *testing.T) {
	if normName("  Даулёт   Сайты ") != normName("даулет сайты") {
		t.Fatal("names that differ only in case, ё and spaces must match")
	}
	if normName("Асет") == normName("Азат") {
		t.Fatal("different names matched")
	}
}

func TestFilterForResident(t *testing.T) {
	boards := []pg.PlatformBoard{
		{ID: "b1", Resident: "Асет"},
		{ID: "b2", Resident: "Азат"},
		{ID: "b3", Resident: "асет "},
		{ID: "b4", Resident: ""},
	}
	docs := []pg.PlatformDoc{
		{Scope: "club", Key: "bs_crm", Value: `{"leads":[{"name":"чужой лид"}]}`},
		{Scope: "club", Key: "bs_pl", Value: `[1,2,3]`},
		{Scope: "club", Key: "bs_kanban", Value: `[]`},
		{Scope: "club", Key: "bs_diag", Value: `[]`},
		{Scope: "club", Key: platformSeedKey, Value: `{"RESIDENTS":[{"name":"Асет","paid":100000,"total":10000,"format":"Офлайн"}],"FINES":[{"res":"Асет","amount":10000}],"PL_ROWS":[{"name":"Выручка"}],"SDATA":{"nps":[{"res":"Мади"}],"leads":[{"n":1}],"schedule":[{"res":"Асет"}],"visits":[{"name":"Асет","per":3,"done":1,"months":2,"money":5}]}}`},
		{Scope: "user:tg:478757502", Key: "bs_theme", Value: "light"},
		{Scope: "user:tg:1", Key: "bs_theme", Value: "dark"},
	}
	ob, od := filterForResident(boards, docs, "Асет", "user:tg:478757502")

	ids := []string{}
	for _, b := range ob {
		ids = append(ids, b.ID)
	}
	if strings.Join(ids, ",") != "b1,b3" {
		t.Fatalf("resident must see only own boards, got %v", ids)
	}

	keys := map[string]string{}
	for _, d := range od {
		keys[d.Scope+"/"+d.Key] = d.Value
	}
	for _, banned := range []string{"club/bs_crm", "club/bs_pl", "club/bs_kanban", "user:tg:1/bs_theme"} {
		if _, ok := keys[banned]; ok {
			t.Errorf("resident got %s", banned)
		}
	}
	for _, need := range []string{"club/bs_diag", "user:tg:478757502/bs_theme", "club/" + platformSeedKey} {
		if _, ok := keys[need]; !ok {
			t.Errorf("resident did not get %s", need)
		}
	}

	seed := keys["club/"+platformSeedKey]
	for _, leak := range []string{"100000", "10000", "Выручка", "Мади", `"money"`, `"paid"`, `"amount"`} {
		if strings.Contains(seed, leak) {
			t.Errorf("resident seed leaks %q: %s", leak, seed)
		}
	}
	var s map[string]any
	if err := json.Unmarshal([]byte(seed), &s); err != nil {
		t.Fatalf("resident seed is not JSON: %v", err)
	}
	if !strings.Contains(seed, `"format":"Офлайн"`) || !strings.Contains(seed, `"done":1`) {
		t.Errorf("roster or visits missing from resident seed: %s", seed)
	}
}
