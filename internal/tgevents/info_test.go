package tgevents

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestParseCount(t *testing.T) {
	for in, want := range map[string]int{"10.2K": 10200, "1,5M": 1500000, "8 001": 8001, "8 001 ": 8001,
		"12,345": 12345, "977": 977, "3.4 К": 3400, "": 0, "x": 0} {
		if got := ParseCount(in); got != want {
			t.Fatalf("%q: %d, want %d", in, got, want)
		}
	}
}

// R77: a channel preview (t.me/s) has the counters; a group card (t.me/<name>)
// has "N members, M online"; a person has neither.
func TestParseInfoAndMentions(t *testing.T) {
	pg := fixture(t) + `<div class="tgme_channel_info"><div class="tgme_channel_info_counters">` +
		`<div class="tgme_channel_info_counter"><span class="counter_value">1.1K</span> <span class="counter_type">photos</span></div>` +
		`<div class="tgme_channel_info_counter"><span class="counter_value">10.2K</span> <span class="counter_type">subscribers</span></div></div></div>`
	pg = strings.Replace(pg, `<head>`, `<head><meta property="og:description" content="Гранты &amp; конкурсы для стартапов">`, 1)
	in := ParseInfo(pg)
	if in.Subs != 10200 || in.Group || in.Desc != "Гранты & конкурсы для стартапов" || len(in.Posts) != 8 ||
		in.Last.Format("2006-01-02") != "2026-10-09" {
		t.Fatalf("%+v (last %v)", in, in.Last)
	}
	grp := ParseInfo(`<meta property="og:title" content="Бизнес чат KZ"><div class="tgme_page_extra">8 001 members, 120 online</div>`)
	if grp.Title != "Бизнес чат KZ" || grp.Subs != 8001 || !grp.Group {
		t.Fatalf("%+v", grp)
	}
	ru := ParseInfo(`<div class="tgme_page_extra">2 345 участников</div>`)
	if ru.Subs != 2345 || !ru.Group {
		t.Fatalf("%+v", ru)
	}
	if p := ParseInfo(`<div class="tgme_page_extra">@some_person</div>`); p.Subs != 0 {
		t.Fatalf("%+v", p)
	}

	posts := []Post{{Channel: "kapitalkz", Text: "Подписывайтесь: t.me/biz_found_kz и https://t.me/s/Other_Channel/12, наш t.me/kapitalkz",
		Links: []string{"https://t.me/biz_found_kz", "https://t.me/joinchat/AAAA", "https://t.me/helper_bot", "https://t.me/+abcdef", "https://example.kz"}},
		{Channel: "kapitalkz", Text: "ещё раз telegram.me/biz_found_kz"}}
	got := strings.Join(Mentions(posts), ",")
	if got != "biz_found_kz,Other_Channel,biz_found_kz" {
		t.Fatal(got)
	}
}

func TestFetchInfoChannelGroupPerson(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Accept-Language") == "" || !strings.HasPrefix(r.Header.Get("Accept-Language"), "en") {
			t.Errorf("Accept-Language %q", r.Header.Get("Accept-Language"))
		}
		switch r.URL.Path {
		case "/s/chan":
			_, _ = w.Write([]byte(`<meta property="og:title" content="Chan"><div class="tgme_header_counter">5.5K subscribers</div>`))
		case "/s/grp", "/s/person":
			_, _ = w.Write([]byte(`<meta property="og:title" content="x"><div class="tgme_page">`)) // t.me/s sends a group away
		case "/grp":
			_, _ = w.Write([]byte(`<meta property="og:title" content="Group"><div class="tgme_page_extra">1 234 members, 5 online</div>`))
		case "/person":
			_, _ = w.Write([]byte(`<meta property="og:title" content="A person"><div class="tgme_page_extra">@person</div>`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	ob, oi := BaseURL, InfoBaseURL
	BaseURL, InfoBaseURL = srv.URL+"/s/", srv.URL+"/"
	defer func() { BaseURL, InfoBaseURL = ob, oi }()
	ctx := context.Background()
	if in, err := FetchInfo(ctx, srv.Client(), "chan"); err != nil || in.Subs != 5500 || in.Group {
		t.Fatal(in, err)
	}
	if in, err := FetchInfo(ctx, srv.Client(), "grp"); err != nil || in.Subs != 1234 || !in.Group || in.Title != "Group" {
		t.Fatal(in, err)
	}
	if _, err := FetchInfo(ctx, srv.Client(), "person"); !errors.Is(err, ErrNoChannel) {
		t.Fatal(err)
	}
	if _, err := FetchInfo(ctx, srv.Client(), "missing"); err == nil {
		t.Fatal("404 must be an error")
	}
}
