package web

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestStripSeed(t *testing.T) {
	page := "<html><head></head><body><script>\n" +
		"var PL_ROWS = [{\"name\":\"Выручка\",\"vals\":[5000000]}];\n" +
		"var RESIDENTS = [{\"name\":\"Асет\",\"total\":710000}];\n" +
		"var FINES = [{\"res\":\"Асет\",\"amount\":10000}];\n" +
		"var SDATA = {\"nps\":[{\"res\":\"Мади\"}]};\n" +
		"var KEEP = [1,2,3];\n" +
		"</script></body></html>"
	html, seed := stripSeed(page)
	for _, leak := range []string{"Выручка", "710000", "10000", "Мади"} {
		if strings.Contains(html, leak) {
			t.Errorf("page still contains %q", leak)
		}
	}
	for _, want := range []string{"var PL_ROWS = [];", "var RESIDENTS = [];", "var FINES = [];", "var SDATA = {};", "var KEEP = [1,2,3];"} {
		if !strings.Contains(html, want) {
			t.Errorf("page lacks %q", want)
		}
	}
	var s map[string]json.RawMessage
	if err := json.Unmarshal([]byte(seed), &s); err != nil || len(s) != 4 {
		t.Fatalf("seed wrong: %v %s", err, seed)
	}
	if !strings.Contains(string(s["RESIDENTS"]), "710000") {
		t.Errorf("seed lost data: %s", seed)
	}
}

// The real embedded page must not carry club data once the server starts.
func TestEmbeddedPageHasNoClubData(t *testing.T) {
	var seed map[string]json.RawMessage
	if err := json.Unmarshal([]byte(Seed()), &seed); err != nil {
		t.Fatalf("seed: %v", err)
	}
	var fines []map[string]any
	_ = json.Unmarshal(seed["FINES"], &fines)
	var residents []map[string]any
	_ = json.Unmarshal(seed["RESIDENTS"], &residents)
	if len(residents) == 0 {
		t.Skip("page carries no resident data")
	}
	page := string(appPage.plain)
	for _, r := range residents {
		for _, f := range []string{"paid", "total", "debtRenew"} {
			if v, ok := r[f].(float64); ok && v >= 50000 {
				needle, _ := json.Marshal(map[string]any{f: v})
				if strings.Contains(page, strings.Trim(string(needle), "{}")) {
					t.Errorf("page still carries %s of %v", needle, r["name"])
				}
			}
		}
	}
	for _, v := range []string{"var PL_ROWS = [];", "var RESIDENTS = [];", "var FINES = [];", "var SDATA = {};"} {
		if !strings.Contains(page, v) {
			t.Errorf("page lacks %q", v)
		}
	}
	if strings.Contains(string(loginPage.plain), "RESIDENTS") {
		t.Error("login page carries platform code")
	}
}
