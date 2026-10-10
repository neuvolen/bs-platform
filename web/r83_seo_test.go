package web

import (
	"regexp"
	"strings"
	"testing"
)

var (
	r83Digit = regexp.MustCompile(`\d`)
	r83NotA  = regexp.MustCompile(`(?i)(?:^|[^а-яё])не [^,.;:!?]{1,40}, а |, а не `)
	r83Ban   = regexp.MustCompile(`(?i)хирург|операци|прокача|гарантированн`)
)

func r83Text(t *testing.T, where, s string) {
	t.Helper()
	noEmDash(t, where, s)
	if m := r83NotA.FindString(s); m != "" {
		t.Errorf("%s: «не X, а Y» %q", where, m)
	}
	if m := r83Ban.FindString(s); m != "" {
		t.Errorf("%s: banned word %q", where, m)
	}
}

// R83: ten answer pages (BLUF): the question is the only H1, the answer at
// most 60 words with a number, FAQPage JSON-LD, the canonical; listed in
// /answers, /about, the sitemap, llms.txt and llms-full.txt. The five new
// long FAQ answers follow the same rules.
func TestAnswerPages(t *testing.T) {
	if len(qaPages) != 10 {
		t.Fatalf("answer pages: %d", len(qaPages))
	}
	r := publicRouter()
	about := get(t, r, "/about").Body.String()
	llms := get(t, r, "/llms.txt").Body.String()
	full := get(t, r, "/llms-full.txt").Body.String()
	sm := get(t, r, "/sitemap.xml").Body.String()
	hub := get(t, r, "/answers")
	if hub.Code != 200 || strings.Count(hub.Body.String(), "<h1") != 1 {
		t.Fatalf("/answers %d", hub.Code)
	}
	slugs := map[string]bool{}
	for _, p := range qaPages {
		if slugs[p.Slug] || slugify(p.Slug) != p.Slug {
			t.Fatalf("slug %q", p.Slug)
		}
		slugs[p.Slug] = true
		if n := words(p.A); n > answerMaxWords || n < 30 || !r83Digit.MatchString(p.A) {
			t.Errorf("%s: answer %d words, needs 30-60 and a number", p.Slug, n)
		}
		r83Text(t, p.Slug, p.Q+" "+p.A+" "+strings.Join(p.More, " "))
		w := get(t, r, "/answers/"+p.Slug)
		body := w.Body.String()
		if w.Code != 200 || strings.Count(body, "<h1") != 1 || !strings.Contains(body, `<link rel="canonical" href="`+qaURL(p.Slug)+`">`) {
			t.Fatalf("%s: %d", p.Slug, w.Code)
		}
		faq := 0
		for _, n := range ldGraph(t, body) {
			if n["@type"] == "FAQPage" {
				faq++
			}
		}
		if faq != 1 {
			t.Errorf("%s: FAQPage nodes %d", p.Slug, faq)
		}
		for where, s := range map[string]string{"about": about, "llms.txt": llms, "llms-full.txt": full, "sitemap": sm} {
			if !strings.Contains(s, "/answers/"+p.Slug) {
				t.Errorf("%s lacks /answers/%s", where, p.Slug)
			}
		}
		if !strings.Contains(hub.Body.String(), hx(p.Q)) {
			t.Errorf("hub lacks %q", p.Q)
		}
	}
	if get(t, r, "/answers/nope").Code != 404 {
		t.Error("an unknown answer must be 404")
	}
	if !strings.Contains(get(t, r, "/robots.txt").Body.String(), "Allow: /answers") {
		t.Error("robots.txt must allow /answers")
	}
	n := 0
	for _, f := range bsProfile.FAQ {
		if len(strings.Fields(f.Q)) >= 9 {
			n++
			if w := words(f.A); w > answerMaxWords || !r83Digit.MatchString(f.A) {
				t.Errorf("FAQ %q: %d words", f.Q, w)
			}
			r83Text(t, "FAQ", f.Q+" "+f.A)
		}
	}
	if n < 7 {
		t.Errorf("long FAQ questions: %d", n)
	}
}

// R83: the daily audit finds nothing wrong on the site as shipped, then sees
// a changed page (lastmod today in the sitemap) and fills a missing
// description from the page's text.
func TestSEOAudit(t *testing.T) {
	defer SetSEOFixes(nil, nil)
	rep, pages, lastmod, descs := SEOAudit(nil, "2026-10-11")
	if rep.Pages < 1000 || rep.Pages != rep.Sitemap || rep.Errors != 0 || rep.Warns != 0 || rep.Broken != 0 || rep.Links < 50 || rep.Score != 100 {
		for _, is := range rep.Issues {
			t.Log(is)
		}
		t.Fatalf("audit %+v", rep.Counts)
	}
	if rep.LLMS == 0 || rep.LLMSFull == 0 || len(pages) != rep.Pages || lastmod[SiteURL+"/about"] != siteUpdated || len(descs) != 0 {
		t.Fatalf("pages %d, lastmod %q, descs %d", len(pages), lastmod[SiteURL+"/about"], len(descs))
	}
	// /about changed since the last run
	about := SiteURL + "/about"
	p := pages[about]
	p.Hash = "old"
	pages[about] = p
	rep2, _, lastmod2, _ := SEOAudit(pages, "2026-10-11")
	if rep2.Changed != 1 || lastmod2[about] != "2026-10-11" || rep2.Fixed != 1 {
		t.Fatalf("changed %d, lastmod %q, fixed %d", rep2.Changed, lastmod2[about], rep2.Fixed)
	}
	if !SetSEOFixes(lastmod2, nil) {
		t.Fatal("fixes must apply")
	}
	sm := get(t, publicRouter(), "/sitemap.xml").Body.String()
	if !strings.Contains(sm, "<loc>"+about+"</loc><lastmod>2026-10-11</lastmod>") || !strings.Contains(sm, "<lastmod>"+siteUpdated+"</lastmod>") {
		t.Fatal("sitemap lastmod not applied")
	}
	// a page with no description gets one from its first paragraph
	body := `<html><head></head><body><main><h1>X</h1><p>Коротко.</p><p>` + strings.Repeat("Собственник считает деньги каждую неделю и видит прибыль. ", 5) + `</p></main></body></html>`
	d := seoMakeDesc(body)
	if n := len([]rune(d)); n < 60 || n > 156 || !strings.HasPrefix(d, "Собственник считает") {
		t.Fatalf("desc %q", d)
	}
	SetSEOFixes(nil, map[string]string{"https://x.kz/p": d})
	if seoDesc("https://x.kz/p", "") != d || seoDesc("https://x.kz/p", strings.Repeat("свой текст ", 6)) == d {
		t.Fatal("seoDesc: the fix only replaces a missing or short description")
	}
	if !strings.Contains(pageHead("T", "", "https://x.kz/p", "website", ""), hx(d)) {
		t.Fatal("pageHead does not use the fix")
	}
}
