package http

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/bnursik/business_surgery_backend/internal/ai"
	"github.com/bnursik/business_surgery_backend/internal/content"
	"github.com/bnursik/business_surgery_backend/internal/tgevents"
)

// R77: the seed is large, every row is complete and has a source; links are
// real-looking https and unique; no em dash in the texts.
func TestCommunitiesSeed(t *testing.T) {
	seed := commSeed()
	if len(seed) < 150 {
		t.Fatalf("seed %d < 150", len(seed))
	}
	ids, urls, plat := map[string]bool{}, map[string]bool{}, map[string]int{}
	whys := map[string]bool{"лиды": true, "партнёрства": true, "выступления": true, "контент": true}
	for _, it := range seed {
		if it.ID == "" || it.Name == "" || it.Cat == "" || it.Topic == "" || it.City == "" || it.Join == "" || it.Src == "" || it.Checked != "2026-10-10" || len(it.Why) == 0 {
			t.Fatalf("incomplete %+v", it)
		}
		if !strings.HasPrefix(it.URL, "https://") && !strings.HasPrefix(it.URL, "http://") {
			t.Fatalf("url %q", it.URL)
		}
		if ids[it.ID] || urls[it.URL] {
			t.Fatalf("duplicate %s %s", it.ID, it.URL)
		}
		ids[it.ID], urls[it.URL] = true, true
		plat[it.Platform]++
		for _, w := range it.Why {
			if !whys[w] {
				t.Fatalf("why %q", w)
			}
		}
		if strings.ContainsAny(it.Name+it.Topic, "—") {
			t.Fatalf("em dash in %s", it.ID)
		}
		if it.Platform == "telegram" && tgName(it.URL) == "" && !strings.Contains(it.URL, "/+") {
			t.Fatalf("telegram url %q", it.URL)
		}
	}
	if plat["telegram"] < 80 || plat["instagram"] < 10 || plat["facebook"] < 5 || plat["offline"] < 10 || plat["linkedin"] < 3 {
		t.Fatalf("platforms %v", plat)
	}
	var comp []map[string]any
	if err := json.Unmarshal(content.CompetitorsR77, &comp); err != nil || len(comp) != 4 {
		t.Fatal(err, len(comp))
	}
	for _, c := range comp {
		src, _ := c["sources"].([]any)
		if c["asof"] != "10.10.2026" || len(src) == 0 || c["borrow"] == "" || c["our_edge"] == "" {
			t.Fatalf("competitor %v", c["name"])
		}
		b, _ := json.Marshal(c)
		if strings.Contains(string(b), "—") {
			t.Fatalf("em dash in %v", c["name"])
		}
	}
}

func TestMergeCommSeed(t *testing.T) {
	c := commCat{Items: []commItem{{ID: "tg-a", Name: "Old", Origin: "seed"}, {ID: "tg-f", Name: "Found", Origin: "found"}}}
	seed := []commItem{{ID: "tg-a", Name: "New", Origin: "seed"}, {ID: "tg-f", Name: "Seed f", Origin: "seed"}, {ID: "tg-b", Name: "B", Origin: "seed"}}
	if !mergeCommSeed(&c, seed) || len(c.Items) != 3 || c.Items[0].Name != "New" || c.Items[1].Name != "Found" || c.Items[2].ID != "tg-b" {
		t.Fatalf("%+v", c.Items)
	}
	if mergeCommSeed(&c, seed) {
		t.Fatal("the second merge must change nothing")
	}
}

// R77: the daily refresh reads the channels and groups, keeps the last good
// numbers of a channel that failed, finds a mentioned business channel and
// adds it as «новое», rejects a small or off-topic one, and does not read
// the same names again the next day.
func TestRefreshCommunities(t *testing.T) {
	repo, ctx := testPlatformDB(t, commCatKey)
	post := func(ch, text string) string {
		return `<div class="tgme_widget_message_wrap"><div class="tgme_widget_message" data-post="` + ch + `/7">` +
			`<div class="tgme_widget_message_text js-message_text" dir="auto">` + text + `</div>` +
			`<a class="tgme_widget_message_date"><time datetime="2026-10-09T08:00:00+00:00"></time></a></div></div>`
	}
	var reads atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reads.Add(1)
		switch r.URL.Path {
		case "/s/chan_one":
			_, _ = w.Write([]byte(`<meta property="og:title" content="Chan One"><div class="tgme_channel_info">` +
				`<span class="counter_value">12.5K</span> <span class="counter_type">subscribers</span></div>` +
				post("chan_one", `Читайте <a href="https://t.me/biz_found_kz">канал</a>, t.me/tiny_biz и t.me/cats_daily, бот t.me/x_helper_bot`)))
		case "/s/group_one", "/s/biz_found_kz_grp":
			_, _ = w.Write([]byte(`<div class="tgme_page"></div>`))
		case "/group_one":
			_, _ = w.Write([]byte(`<meta property="og:title" content="Group One"><div class="tgme_page_extra">2 001 members, 7 online</div>`))
		case "/s/biz_found_kz":
			_, _ = w.Write([]byte(`<meta property="og:title" content="Бизнес Алматы: предприниматели"><meta property="og:description" content="Кейсы собственников Казахстана">` +
				`<div class="tgme_channel_info"><span class="counter_value">3.4K</span> <span class="counter_type">subscribers</span></div>` + post("biz_found_kz", "привет")))
		case "/s/tiny_biz":
			_, _ = w.Write([]byte(`<meta property="og:title" content="Tiny business"><span class="counter_value">120</span> <span class="counter_type">subscribers</span>` + post("tiny_biz", "x")))
		case "/s/cats_daily":
			_, _ = w.Write([]byte(`<meta property="og:title" content="Котики"><span class="counter_value">50K</span> <span class="counter_type">subscribers</span>` + post("cats_daily", "x")))
		default:
			http.Error(w, "no", http.StatusInternalServerError)
		}
	}))
	defer srv.Close()
	ob, oi, op, oh := tgevents.BaseURL, tgevents.InfoBaseURL, commPause, commHTTP
	tgevents.BaseURL, tgevents.InfoBaseURL, commPause, commHTTP = srv.URL+"/s/", srv.URL+"/", 0, srv.Client()
	defer func() { tgevents.BaseURL, tgevents.InfoBaseURL, commPause, commHTTP = ob, oi, op, oh }()

	h := NewPlatformAI(repo, &ai.Client{})
	cat := commCat{Items: []commItem{
		{ID: "tg-chan_one", Name: "Chan One", URL: "https://t.me/chan_one", Platform: "telegram", Origin: "seed"},
		{ID: "tg-group_one", Name: "Group One", URL: "https://t.me/group_one", Platform: "telegram", Origin: "seed"},
		{ID: "tg-broken_one", Name: "Broken", URL: "https://t.me/broken_one", Platform: "telegram", Origin: "seed"},
		{ID: "tg-invite", Name: "Invite", URL: "https://t.me/+abcdef", Platform: "telegram", Origin: "seed"},
		{ID: "in-x", Name: "Insta", URL: "https://www.instagram.com/x/", Platform: "instagram", Origin: "seed"},
	}, TG: map[string]*commTG{"broken_one": {Subs: 900, Title: "Broken"}}}
	val, _ := json.Marshal(cat)
	if _, err := repo.PutDoc(ctx, "club", commCatKey, 0, string(val), false, "test"); err != nil {
		t.Fatal(err)
	}
	res, err := h.refreshCommunities(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if res.Checked != 3 || res.OK != 2 || res.Failed != 1 || res.Mention != 3 || res.Read != 3 || res.Added != 1 {
		t.Fatalf("run %+v", res)
	}
	got, _ := h.loadComm(ctx)
	if c := got.TG["chan_one"]; c == nil || c.Subs != 12500 || c.Group || c.Last != "2026-10-09T08:00:00Z" {
		t.Fatalf("chan %+v", c)
	}
	if g := got.TG["group_one"]; g == nil || g.Subs != 2001 || !g.Group {
		t.Fatalf("group %+v", g)
	}
	if b := got.TG["broken_one"]; b == nil || b.Err == "" || b.Subs != 900 {
		t.Fatalf("broken %+v", b)
	}
	var found *commItem
	for i, it := range got.Items {
		if it.Origin == "found" {
			found = &got.Items[i]
		}
	}
	if found == nil || found.ID != "tg-biz_found_kz" || found.Size != 3400 || found.City != "Казахстан" || found.Src != "https://t.me/chan_one" || len(got.Items) != 6 {
		t.Fatalf("found %+v (%d items)", found, len(got.Items))
	}
	if got.Cand["tiny_biz"].Why != "мало участников" || got.Cand["cats_daily"].Why != "не про бизнес" || got.Cand["x_helper_bot"] != nil {
		t.Fatalf("cand %+v %+v", got.Cand["tiny_biz"], got.Cand["cats_daily"])
	}
	if got.Run == nil || got.Run.Added != 1 {
		t.Fatalf("run %+v", got.Run)
	}

	// the next run: the found channel is read as a member of the catalogue,
	// the rejected names wait 30 days
	res, err = h.refreshCommunities(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if res.Checked != 4 || res.Read != 0 || res.Added != 0 {
		t.Fatalf("second run %+v", res)
	}
	got, _ = h.loadComm(ctx)
	if len(got.Items) != 6 || got.TG["biz_found_kz"].Subs != 3400 {
		t.Fatalf("second: %d items %+v", len(got.Items), got.TG["biz_found_kz"])
	}

	// the seed on start keeps the found rows and the numbers
	h.SeedCommunities(ctx)
	got, _ = h.loadComm(ctx)
	if len(got.Items) < 150 || got.TG["chan_one"] == nil {
		t.Fatalf("seeded %d", len(got.Items))
	}
	foundStill := false
	for _, it := range got.Items {
		foundStill = foundStill || it.ID == "tg-biz_found_kz"
	}
	if !foundStill {
		t.Fatal("the found row was lost by the seed")
	}
}

// R77: the four competitors go once into the team's analysis.
func TestMigrateCompetitorsR77(t *testing.T) {
	repo, ctx := testPlatformDB(t, "bs_mkt_analysis", mktR77Key)
	doc := `{"competitors":[{"name":"BizPride Club"},{"name":"Аномалия (уже есть)","offer":"Десятки","price":"","our_edge":"Своё","strengths":["Очень дешёвый вход"]}]}`
	if _, err := repo.PutDoc(ctx, "club", "bs_mkt_analysis", 0, doc, false, "test"); err != nil {
		t.Fatal(err)
	}
	h := NewPlatformAI(repo, &ai.Client{})
	h.MigrateCompetitorsR77(ctx)
	h.MigrateCompetitorsR77(ctx)
	d, _ := repo.GetDoc(ctx, "club", "bs_mkt_analysis")
	var v struct {
		Competitors []map[string]any `json:"competitors"`
	}
	_ = json.Unmarshal([]byte(d.Value), &v)
	names := []string{}
	for _, c := range v.Competitors {
		names = append(names, c["name"].(string))
	}
	if len(v.Competitors) != 5 || !strings.Contains(strings.Join(names, "|"), "BURN Club") || v.Competitors[4]["key"] != nil || v.Competitors[4]["asof"] != "10.10.2026" {
		t.Fatalf("%v", names)
	}
	// the card that was there keeps its name and the team's text, takes the research
	a := v.Competitors[1]
	st, _ := a["strengths"].([]any)
	src, _ := a["sources"].([]any)
	if a["name"] != "Аномалия (уже есть)" || a["our_edge"] != "Своё" || a["asof"] != "10.10.2026" || len(src) == 0 || a["borrow"] == nil ||
		!strings.HasPrefix(a["price"].(string), "5 000 ₽") || len(st) < 3 || st[0] != "Очень дешёвый вход" || !strings.Contains(a["offer"].(string), "десятки") {
		t.Fatalf("enriched %v", a)
	}
}
