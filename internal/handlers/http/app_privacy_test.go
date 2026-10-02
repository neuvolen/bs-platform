package http

import (
	"encoding/json"
	"os"
	"strconv"
	"testing"
)

func TestBundleByRole(t *testing.T) {
	raw, err := os.ReadFile("testdata/bundle.json")
	if err != nil {
		t.Fatal(err)
	}
	var full map[string]any
	_ = json.Unmarshal(raw, &full)
	var me map[string]any
	for _, r := range full["residents"].([]any) {
		m := r.(map[string]any)
		if id := anyString(m["chatId"]); id != "" && id != "453800951" && m["isFired"] != true {
			me = m
			break
		}
	}
	myID, _ := strconv.ParseInt(anyString(me["chatId"]), 10, 64)
	g := &AppGateway{Admins: map[int64]string{453800951: "Рустам"}}
	if string(g.forUser(453800951, raw)) != string(raw) {
		t.Fatal("the team sees everything")
	}
	var res map[string]any
	_ = json.Unmarshal(g.forUser(myID, raw), &res)
	sawOwnMoney, sawOtherMoney := false, false
	for _, r := range res["residents"].([]any) {
		m := r.(map[string]any)
		if _, ok := m["debt"]; ok {
			if m["name"] == me["name"] {
				sawOwnMoney = true
			} else {
				sawOtherMoney = true
			}
		}
	}
	if !sawOwnMoney || sawOtherMoney {
		t.Fatalf("resident money: own %v other %v", sawOwnMoney, sawOtherMoney)
	}
	if res["totalDebt"].(float64) != 0 || len(res["leads"].(map[string]any)["leads"].([]any)) != 0 {
		t.Fatal("club money or leads leaked to a resident")
	}
	for _, f := range res["fines"].([]any) {
		if f.(map[string]any)["name"] != me["name"] {
			t.Fatal("someone else's fine")
		}
	}
	if len(res["logs"].([]any)) == 0 {
		t.Fatal("a resident keeps the club's reports")
	}
	var lead map[string]any
	_ = json.Unmarshal(g.forUser(999, raw), &lead)
	for _, r := range lead["residents"].([]any) {
		if len(r.(map[string]any)) > 2 {
			t.Fatalf("lead sees resident details: %v", r)
		}
	}
	for _, k := range []string{"fines", "logs", "adminProfiles"} {
		if len(lead[k].([]any)) != 0 {
			t.Fatalf("lead sees %s", k)
		}
	}
	if lead["leadmagnets"] == nil {
		t.Fatal("lead keeps the materials")
	}
}
