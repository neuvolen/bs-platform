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

// With live data a resident sees their own money, fines and meetings, and
// nobody else's.
func TestResidentSeedLive(t *testing.T) {
	seed := `{"LIVE":{"source":"server"},
	  "RESIDENTS":[{"name":"Асет","total":10000,"fines":10000,"paid":100000,"format":"Офлайн","start":"03.03.2025"},
	               {"name":"Альтаир","total":20000,"fines":20000,"paid":200000,"format":"Офлайн","start":"28.12.2024"}],
	  "FINES":[{"res":"Асет","amount":10000},{"res":"Альтаир","amount":20000}],
	  "PL_ROWS":[{"name":"ДОХОДЫ","kind":"head","vals":[1]}],
	  "SDATA":{"schedule":[{"res":"Асет","date":"01.10.2026"},{"res":"Альтаир","date":"02.10.2026"}],
	           "visits":[{"name":"Асет","per":3,"done":1,"months":18}],"nps":[{"res":"Асет"}],"leads":[{"x":1}]}}`
	var out struct {
		Live      map[string]string           `json:"LIVE"`
		Residents []map[string]any            `json:"RESIDENTS"`
		Fines     []map[string]any            `json:"FINES"`
		PL        []any                       `json:"PL_ROWS"`
		SData     map[string][]map[string]any `json:"SDATA"`
	}
	if err := json.Unmarshal([]byte(residentSeed(seed, "асет")), &out); err != nil {
		t.Fatal(err)
	}
	if out.Live["source"] == "server" {
		t.Fatal("a resident's seed must not look like the club's live P&L")
	}
	for _, r := range out.Residents {
		_, hasMoney := r["total"]
		if r["name"] == "Асет" && (!hasMoney || r["total"].(float64) != 10000) {
			t.Fatalf("own line without money: %v", r)
		}
		if r["name"] == "Альтаир" && hasMoney {
			t.Fatalf("someone else's money leaked: %v", r)
		}
	}
	if len(out.Fines) != 1 || out.Fines[0]["res"] != "Асет" {
		t.Fatalf("fines %v", out.Fines)
	}
	if len(out.SData["schedule"]) != 1 || out.SData["schedule"][0]["res"] != "Асет" {
		t.Fatalf("schedule %v", out.SData["schedule"])
	}
	if len(out.PL) != 0 || len(out.SData["nps"]) != 0 || len(out.SData["leads"]) != 0 {
		t.Fatal("club P&L, NPS or leads leaked")
	}
	// A snapshot (not live) still gives no money at all.
	var old struct {
		Residents []map[string]any `json:"RESIDENTS"`
		Fines     []any            `json:"FINES"`
	}
	_ = json.Unmarshal([]byte(residentSeed(strings.Replace(seed, `"LIVE":{"source":"server"},`, "", 1), "Асет")), &old)
	for _, r := range old.Residents {
		if _, ok := r["total"]; ok {
			t.Fatal("money from a snapshot")
		}
	}
	if len(old.Fines) != 0 {
		t.Fatal("fines from a snapshot")
	}
}
