package http

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/bnursik/business_surgery_backend/internal/ai"
	"github.com/bnursik/business_surgery_backend/internal/tgevents"
)

func TestCleanTGUser(t *testing.T) {
	for in, want := range map[string]string{
		"https://t.me/s/startup_course_com": "startup_course_com", "@zula_opps": "zula_opps",
		"t.me/abc_def/123": "abc_def", "telegram.me/abcd1?x=1": "abcd1", "a b": "", "": "",
	} {
		if got := cleanTGUser(in); got != want {
			t.Fatalf("%q: %q", in, got)
		}
	}
}

func TestMergeAndPrune(t *testing.T) {
	f := eventsFeed{Items: []ai.Event{
		{Title: "Прошедшее", Date: "2026-10-08"},
		{Title: "Возможность с дедлайном вчера", Date: "2026-10-08", Deadline: "2026-10-08", Kind: tgevents.KindOpportunity, Origin: "tg"},
		{Title: "Нетворкинг для фаундеров!", Date: "2026-10-17", URL: "https://ticketon.kz/x", Price: "5000"},
		{Title: "Сегодня", Date: "2026-10-09"},
	}}
	if n := pruneFeed(&f, "2026-10-09"); n != 2 || len(f.Items) != 2 || f.Cleaned.Removed != 2 || f.Cleaned.Total != 2 {
		t.Fatal(n, f.Items, f.Cleaned)
	}
	pruneFeed(&f, "2026-10-10")
	if len(f.Items) != 1 || f.Cleaned.Day != "2026-10-10" || f.Cleaned.Removed != 1 || f.Cleaned.Total != 3 {
		t.Fatal(f.Items, f.Cleaned)
	}
	tg := ai.Event{Title: "Нетворкинг для фаундеров", Date: "2026-10-17", Time: "19:00", URL: "https://t.me/startup_course_com/6309", Origin: "tg", Org: "NU STeP"}
	out := mergeEvents(f.Items, []ai.Event{tg, {Title: "Другое", Date: "2026-10-20", Origin: "tg"}})
	if len(out) != 2 || out[0].Origin != "tg" || out[0].URL != "https://ticketon.kz/x" || out[0].Price != "5000" || out[0].Time != "19:00" || out[0].Org != "NU STeP" {
		t.Fatalf("%+v", out)
	}
}

// R70: the channels are read from t.me/s pages; new posts become events,
// a second run takes nothing twice, the web search keeps them, what passed goes.
func TestRefreshTGChannels(t *testing.T) {
	repo, ctx := testPlatformDB(t, eventsFeedKey, eventsTGKey)
	page, err := os.ReadFile("../../tgevents/testdata/startup_course_com.html")
	if err != nil {
		t.Fatal(err)
	}
	var hits, guesses atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		switch {
		case r.URL.Path == "/s/startup_course_com" && r.URL.Query().Get("before") == "":
			_, _ = w.Write(page)
		case r.URL.Path == "/s/startup_course_com":
			_, _ = w.Write([]byte(`<html><head><meta property="og:title" content="x"></head><body><div class="tgme_channel_info"></div></body></html>`))
		case r.URL.Path == "/s/zula_opps":
			guesses.Add(1)
			_, _ = w.Write([]byte(strings.ReplaceAll(strings.ReplaceAll(string(page), "startup_course_com", "zula_opps"),
				"📢 Возможности 🚀 startup-course.com 💰", "Opportunities with Zula")))
		default:
			guesses.Add(1)
			_, _ = w.Write([]byte(`<html><body class="tgme_page"></body></html>`))
		}
	}))
	defer srv.Close()
	oldBase, oldSrc, oldPause, oldHTTP := tgevents.BaseURL, tgSources, tgPause, tgHTTP
	tgevents.BaseURL, tgPause, tgHTTP = srv.URL+"/s/", 0, srv.Client()
	tgSources = []tgSource{
		{Key: "startup_course_com", Name: "Возможности Startup course", Username: "startup_course_com"},
		{Key: "zula", Name: "Opportunities with Zula", Match: "zula", Guess: []string{"no_such_zula", "zula_opps"}},
	}
	defer func() { tgevents.BaseURL, tgSources, tgPause, tgHTTP = oldBase, oldSrc, oldPause, oldHTTP }()

	today := time.Now().In(tgevents.Almaty).Format("2006-01-02")
	if today > "2027-01-14" {
		t.Skip("the fixture's dates have passed")
	}
	seed, _ := json.Marshal(eventsFeed{Items: []ai.Event{
		{Title: "Прошедший форум", Date: "2020-01-01", URL: "https://ex.kz/old"},
		{Title: "Будущий форум", Date: "2099-01-01", URL: "https://ex.kz/f"},
	}})
	if _, err := repo.PutDoc(ctx, "club", eventsFeedKey, 0, string(seed), false, "test"); err != nil {
		t.Fatal(err)
	}
	h := NewPlatformAI(repo, &ai.Client{})
	n, err := h.refreshTG(ctx)
	if err != nil {
		t.Fatal(err)
	}
	f, _ := h.loadFeed(ctx)
	st, zl := f.TG["startup_course_com"], f.TG["zula"]
	if st == nil || st.Last != 6310 || st.Title == "" || st.Err != "" || zl == nil || zl.Username != "zula_opps" || zl.Last != 6310 {
		t.Fatalf("state %+v %+v", st, zl)
	}
	for _, e := range f.Items {
		if e.Date < today {
			t.Fatalf("a passed event stayed: %+v", e)
		}
	}
	titles := map[string]bool{}
	for _, e := range f.Items {
		titles[e.Title] = true
	}
	if titles["Прошедший форум"] || !titles["Будущий форум"] || f.Cleaned == nil || f.Cleaned.Removed < 1 {
		t.Fatalf("cleanup %v %+v", titles, f.Cleaned)
	}
	// Both channels carry the same posts here: one event each, not two
	if n == 0 || st.Total == 0 || len(f.Items) > 1+st.Total+zl.Total {
		t.Fatalf("n=%d items=%d st=%d zl=%d", n, len(f.Items), st.Total, zl.Total)
	}
	if today <= "2026-10-14" && !titles["Кейс-челлендж Centras Leadership Challenge: выиграй до 1 000 000 ₸!"] {
		t.Fatalf("centras missing: %v", titles)
	}
	if !titles["GSEA Kazakhstan: конкурс для студентов-предпринимателей"] {
		t.Fatalf("gsea missing: %v", titles)
	}

	// The second run: nothing new, the guessed username is kept (no guessing)
	g0, items0 := guesses.Load(), len(f.Items)
	if _, err := h.refreshTG(ctx); err != nil {
		t.Fatal(err)
	}
	f, _ = h.loadFeed(ctx)
	if len(f.Items) != items0 || f.TG["startup_course_com"].Found != 0 || guesses.Load() != g0+1 {
		t.Fatalf("second run: %d items (was %d), found %d, guesses %d→%d", len(f.Items), items0, f.TG["startup_course_com"].Found, g0, guesses.Load())
	}

	// The web search replaces only its own items
	if err := h.mutateFeed(ctx, func(f *eventsFeed) {
		var tg []ai.Event
		for _, e := range f.Items {
			if e.Origin == "tg" {
				tg = append(tg, e)
			}
		}
		f.Items = sortFeed(mergeEvents(tg, []ai.Event{{Title: "Новый форум", Date: "2099-02-02", URL: "https://ex.kz/n"}}))
	}); err != nil {
		t.Fatal(err)
	}
	f, _ = h.loadFeed(ctx)
	tgN := 0
	for _, e := range f.Items {
		if e.Origin == "tg" {
			tgN++
		}
		if e.Title == "Будущий форум" {
			t.Fatal("the old web item stays")
		}
	}
	if tgN != items0-1 || f.TG["zula"].Username != "zula_opps" {
		t.Fatalf("tg items %d of %d", tgN, items0-1)
	}

	// A channel added by the team in Мероприятия is read too
	_, _ = repo.PutDoc(ctx, "club", eventsTGKey, 0, `{"channels":["https://t.me/s/extra_channel","bad name"]}`, false, "test")
	list := h.tgSourceList(ctx)
	if len(list) != 3 || list[2].Username != "extra_channel" {
		t.Fatalf("%+v", list)
	}
}
