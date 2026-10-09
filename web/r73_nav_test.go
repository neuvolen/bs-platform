package web

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
)

// R73: the resident's menu has eight sections, the measurements are together
// in «Замеры», and no resident screen is lost in the move.
func TestR73ResidentNav(t *testing.T) {
	var got struct {
		Blocks []NavBlock `json:"blocks"`
	}
	if err := json.Unmarshal(PlatformNav(), &got); err != nil {
		t.Fatal(err)
	}
	tabs, names := map[string][]string{}, map[string]string{}
	for _, b := range got.Blocks {
		names[b.Key] = b.Name
		for _, x := range b.Tabs {
			tabs[b.Key] = append(tabs[b.Key], x.ID)
		}
	}
	want := map[string]string{"rtrack": "Мой разбор", "rtd": "Задачи", "rbiz": "Замеры", "rotc": "Отчёты", "rmeet": "Встречи", "rmat": "Материалы", "rclub": "Клуб", "rme": "Профиль"}
	for k, n := range want {
		if names[k] != n {
			t.Errorf("block %s: %q, want %q", k, names[k], n)
		}
	}
	meas := strings.Join(tabs["rbiz"], ",")
	for _, id := range []string{"rMeas", "rwheel", "bizWheel", "rDiag", "rGallup", "rHealth", "rLabs", "rAnalysis"} {
		if !strings.Contains(","+meas+",", ","+id+",") {
			t.Errorf("«Замеры» has no %s: %s", id, meas)
		}
	}
	// every screen the resident had before R73 is still in the menu, once
	seen := map[string]int{}
	for k := range want {
		for _, id := range tabs[k] {
			seen[id]++
		}
	}
	for _, id := range []string{"board", "rtasks", "rreport", "rKit", "rsched", "mycal", "rCalls", "rHist", "rwheel", "bizWheel", "rHealth", "rDiag", "rHistory", "rLabs", "rAnalysis", "rSummary",
		"rRes2", "rFiveList", "rAbout", "rRules", "rNumbers", "rInvite", "rOnboard", "rprofile", "rResults", "rGallup", "rStats", "rAch", "rFines"} {
		if seen[id] != 1 {
			t.Errorf("resident screen %s: in the menu %d times", id, seen[id])
		}
	}
	page, _ := os.ReadFile("platform.html")
	side := string(page)
	i := strings.Index(side, `id="navRes"`)
	if i < 0 {
		t.Fatal("no navRes")
	}
	side = side[i : i+strings.Index(side[i:], "</div>\n  <div class=\"side-foot\"")]
	order := []string{"rtrack", "rtd", "rbiz", "rotc", "rmeet", "rmat", "rclub", "rme"}
	last := -1
	for _, k := range order {
		p := strings.Index(side, `data-b="`+k+`"`)
		if p < last {
			t.Errorf("sidebar order: %s at %d", k, p)
		}
		last = p
	}
}

// R73: the login button is on the page from the first byte: it links to
// Telegram's login page with the bot id, the widget is not needed for it.
func TestR73LoginButton(t *testing.T) {
	page := injectTgBot(string(loginHTML), "123456789:AAAA-secret")
	if !strings.Contains(page, `id="tgBtn"`) || !strings.Contains(page, `data-bot="123456789"`) {
		t.Fatal("no login button with the bot id")
	}
	if strings.Contains(page, "AAAA-secret") {
		t.Fatal("the secret part of the token is on the page")
	}
	if !strings.Contains(page, "https://t.me/bsurgery_bot?start=login") {
		t.Fatal("no fallback link to the bot")
	}
	if got := injectTgBot(string(loginHTML), ""); !strings.Contains(got, `data-bot=""`) {
		t.Fatal("no token: the id must be empty")
	}
	if strings.Contains(page, ` src="/promo/`) {
		t.Fatal("showcase pictures must wait until the section is near (data-src)")
	}
	b, ok := PromoFile("lead_platform.mp4")
	if !ok || len(b) < 100000 || string(b[4:8]) != "ftyp" {
		t.Fatal("no lead video")
	}
}
