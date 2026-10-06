package web

import (
	"encoding/json"
	"encoding/xml"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"

	"github.com/bnursik/business_surgery_backend/internal/content"
	"github.com/gin-gonic/gin"
)

func publicRouter() *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	Register(r, "test-secret", "bs_session")
	return r
}

func get(t *testing.T, r *gin.Engine, path string) *httptest.ResponseRecorder {
	t.Helper()
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, path, nil))
	return w
}

var ldRe = regexp.MustCompile(`(?s)<script type="application/ld\+json">(.*?)</script>`)

// ldGraph parses every JSON-LD block of a page and returns its nodes.
func ldGraph(t *testing.T, body string) []map[string]any {
	t.Helper()
	ms := ldRe.FindAllStringSubmatch(body, -1)
	if len(ms) == 0 {
		t.Fatal("no JSON-LD on the page")
	}
	var nodes []map[string]any
	for _, m := range ms {
		var doc map[string]any
		if err := json.Unmarshal([]byte(m[1]), &doc); err != nil {
			t.Fatalf("JSON-LD is not valid JSON: %v", err)
		}
		if doc["@context"] != "https://schema.org" {
			t.Errorf("@context = %v", doc["@context"])
		}
		g, _ := doc["@graph"].([]any)
		for _, n := range g {
			if m, ok := n.(map[string]any); ok {
				nodes = append(nodes, m)
			}
		}
	}
	return nodes
}

func types(n map[string]any) []string {
	switch v := n["@type"].(type) {
	case string:
		return []string{v}
	case []any:
		var out []string
		for _, x := range v {
			out = append(out, x.(string))
		}
		return out
	}
	return nil
}

func byType(nodes []map[string]any, typ string) []map[string]any {
	var out []map[string]any
	for _, n := range nodes {
		for _, x := range types(n) {
			if x == typ {
				out = append(out, n)
			}
		}
	}
	return out
}

// Owner's text rule: no em dash anywhere in public texts.
func noEmDash(t *testing.T, where, s string) {
	t.Helper()
	if i := strings.IndexRune(s, '—'); i >= 0 {
		lo := i - 60
		if lo < 0 {
			lo = 0
		}
		t.Errorf("%s has an em dash near %q", where, s[lo:i])
	}
}

func TestRobotsTxt(t *testing.T) {
	r := publicRouter()
	w := get(t, r, "/robots.txt")
	if w.Code != 200 || !strings.HasPrefix(w.Header().Get("Content-Type"), "text/plain") {
		t.Fatalf("robots: %d %s", w.Code, w.Header().Get("Content-Type"))
	}
	body := w.Body.String()
	groups := strings.Split(body, "User-agent: ")[1:]
	want := map[string]bool{}
	for _, a := range append(aiAgents, "*") {
		want[a] = false
	}
	for _, g := range groups {
		name := strings.TrimSpace(strings.SplitN(g, "\n", 2)[0])
		if _, ok := want[name]; !ok {
			t.Errorf("unexpected group %q", name)
		}
		want[name] = true
		for _, line := range []string{"Allow: /about", "Allow: /library", "Allow: /llms.txt", "Disallow: /api/", "Disallow: /platform", "Disallow: /dl/"} {
			if !strings.Contains(g, line+"\n") {
				t.Errorf("group %s lacks %q", name, line)
			}
		}
		if strings.Contains(g, "Disallow: /\n") {
			t.Errorf("group %s closes the whole site", name)
		}
	}
	for _, a := range []string{"OAI-SearchBot", "ChatGPT-User", "GPTBot", "Claude-SearchBot", "Claude-User", "ClaudeBot", "PerplexityBot", "Google-Extended", "Googlebot", "Bingbot", "YandexBot", "*"} {
		if !want[a] {
			t.Errorf("no group for %s", a)
		}
	}
	if !strings.Contains(body, "Sitemap: https://app.bxclub.kz/sitemap.xml") {
		t.Error("no Sitemap line")
	}
}

func TestLlmsTxt(t *testing.T) {
	r := publicRouter()
	w := get(t, r, "/llms.txt")
	if w.Code != 200 || !strings.HasPrefix(w.Header().Get("Content-Type"), "text/plain") {
		t.Fatalf("llms.txt: %d", w.Code)
	}
	s := w.Body.String()
	if !strings.HasPrefix(s, "# Business Surgery (BS)\n\n> ") {
		t.Errorf("llms.txt must start with H1 and a summary quote: %q", s[:60])
	}
	for _, want := range []string{"Алматы", "50 000 ₸", "500 000 ₸", "1 500 000 ₸", "Береке Ерниязов", "Рустам Кабден", "https://app.bxclub.kz/about", "https://t.me/bsurgery_bot", "+7 702 403 50 36", "## Optional", "https://app.bxclub.kz/library/"} {
		if !strings.Contains(s, want) {
			t.Errorf("llms.txt lacks %q", want)
		}
	}
	noEmDash(t, "llms.txt", s)
	full := get(t, r, "/llms-full.txt").Body.String()
	for _, f := range bsProfile.FAQ {
		if !strings.Contains(full, f.Q) || !strings.Contains(full, f.A) {
			t.Errorf("llms-full.txt lacks FAQ %q", f.Q)
		}
	}
	noEmDash(t, "llms-full.txt", full)
	if len(full) > 600<<10 {
		t.Errorf("llms-full.txt is %d KB", len(full)>>10)
	}
}

func TestAboutPage(t *testing.T) {
	r := publicRouter()
	w := get(t, r, "/about")
	if w.Code != 200 || !strings.HasPrefix(w.Header().Get("Content-Type"), "text/html") {
		t.Fatalf("/about: %d", w.Code)
	}
	if x := w.Header().Get("X-Robots-Tag"); strings.Contains(x, "noindex") {
		t.Errorf("/about is noindex: %s", x)
	}
	body := w.Body.String()
	for _, want := range []string{`<link rel="canonical" href="https://app.bxclub.kz/about">`, `property="og:image" content="https://app.bxclub.kz/site/og.png"`, `property="og:title"`, `<html lang="ru">`, "<h1>", "+7 702 403 50 36", "bsurgery_bot", "Береке Ерниязов", "Рустам Кабден", "50 000 ₸"} {
		if !strings.Contains(body, want) {
			t.Errorf("/about lacks %q", want)
		}
	}
	noEmDash(t, "/about", body)
	// light: no scripts except JSON-LD
	if n := strings.Count(body, "<script"); n != strings.Count(body, `<script type="application/ld+json">`) {
		t.Errorf("/about runs %d scripts", n)
	}
	if len(body) > 80<<10 {
		t.Errorf("/about is %d KB", len(body)>>10)
	}

	nodes := ldGraph(t, body)
	org := byType(nodes, "Organization")
	if len(org) != 1 || len(byType(nodes, "ProfessionalService")) != 1 {
		t.Fatalf("need one Organization + ProfessionalService node, got %d", len(org))
	}
	o := org[0]
	for _, k := range []string{"name", "url", "logo", "description", "address", "telephone", "founder", "sameAs", "hasOfferCatalog", "areaServed"} {
		if o[k] == nil {
			t.Errorf("Organization lacks %s", k)
		}
	}
	if o["name"] != "Business Surgery" {
		t.Errorf("name %v", o["name"])
	}
	cat := o["hasOfferCatalog"].(map[string]any)["itemListElement"].([]any)
	if len(cat) != len(bsProfile.Offers) {
		t.Errorf("offers %d", len(cat))
	}
	for _, x := range cat {
		of := x.(map[string]any)
		if of["@type"] != "Offer" || of["priceCurrency"] != "KZT" || !regexp.MustCompile(`^\d+$`).MatchString(of["price"].(string)) {
			t.Errorf("bad offer %v", of)
		}
		if of["itemOffered"].(map[string]any)["@type"] != "Service" {
			t.Errorf("offer without Service: %v", of)
		}
	}
	if p := byType(nodes, "Person"); len(p) != 2 {
		t.Errorf("Person nodes: %d", len(p))
	}
	for _, typ := range []string{"WebSite", "AboutPage"} {
		if len(byType(nodes, typ)) != 1 {
			t.Errorf("no %s node", typ)
		}
	}
	if len(byType(nodes, "Event")) != 0 {
		t.Error("Event without BS_PUBLIC_EVENT")
	}
	faq := byType(nodes, "FAQPage")
	if len(faq) != 1 {
		t.Fatal("no FAQPage")
	}
	qs := faq[0]["mainEntity"].([]any)
	if len(qs) < 10 || len(qs) > 15 {
		t.Errorf("FAQ has %d questions, want 10-15", len(qs))
	}
	for _, q := range qs {
		m := q.(map[string]any)
		a := m["acceptedAnswer"].(map[string]any)
		if m["@type"] != "Question" || a["@type"] != "Answer" || m["name"] == "" || a["text"] == "" {
			t.Errorf("bad question %v", m)
		}
		// Google: FAQ markup must match text visible on the page
		if !strings.Contains(body, hx(m["name"].(string))) || !strings.Contains(body, hx(a["text"].(string))) {
			t.Errorf("FAQ %q is not visible on the page", m["name"])
		}
	}
	// every @id reference points to a node of the graph
	ids := map[string]bool{}
	for _, n := range nodes {
		if id, ok := n["@id"].(string); ok {
			ids[id] = true
		}
	}
	for _, f := range o["founder"].([]any) {
		if id := f.(map[string]any)["@id"].(string); !ids[id] {
			t.Errorf("founder %s is not in the graph", id)
		}
	}
	// /club → /about
	if w := get(t, r, "/club"); w.Code != http.StatusMovedPermanently || w.Header().Get("Location") != "/about" {
		t.Errorf("/club: %d %s", w.Code, w.Header().Get("Location"))
	}
}

func TestAboutEvent(t *testing.T) {
	t.Setenv("BS_PUBLIC_EVENT", `{"name":"Бизнес-завтрак BS","start":"2026-10-24T09:00+05:00","place":"Тестовое место","address":"ул. Тестовая, 1"}`)
	nodes := ldGraph(t, aboutHTML())
	ev := byType(nodes, "Event")
	if len(ev) != 1 || ev[0]["startDate"] != "2026-10-24T09:00+05:00" || ev[0]["location"] == nil || ev[0]["offers"] == nil {
		t.Fatalf("event node: %v", ev)
	}
	t.Setenv("BS_PUBLIC_EVENT", `{"name":"без даты"}`)
	if len(byType(ldGraph(t, aboutHTML()), "Event")) != 0 {
		t.Error("an event without date and place got onto the page")
	}
}

func TestSitemapAndLibraryPages(t *testing.T) {
	r := publicRouter()
	w := get(t, r, "/sitemap.xml")
	if w.Code != 200 || !strings.Contains(w.Header().Get("Content-Type"), "xml") {
		t.Fatalf("sitemap: %d", w.Code)
	}
	var sm struct {
		URLs []struct {
			Loc     string `xml:"loc"`
			Lastmod string `xml:"lastmod"`
		} `xml:"url"`
	}
	if err := xml.Unmarshal(w.Body.Bytes(), &sm); err != nil {
		t.Fatalf("sitemap is not XML: %v", err)
	}
	items, err := content.PublicLibrary()
	if err != nil || len(items) == 0 {
		t.Fatalf("library: %v", err)
	}
	if len(sm.URLs) != len(items)+2 {
		t.Errorf("sitemap has %d urls, want %d", len(sm.URLs), len(items)+2)
	}
	seen := map[string]bool{}
	for _, u := range sm.URLs {
		if seen[u.Loc] {
			t.Errorf("duplicate %s", u.Loc)
		}
		seen[u.Loc] = true
		if !strings.HasPrefix(u.Loc, "https://app.bxclub.kz/") || u.Lastmod == "" {
			t.Errorf("bad url %v", u)
			continue
		}
		p := strings.TrimPrefix(u.Loc, "https://app.bxclub.kz")
		w := get(t, r, p)
		if w.Code != 200 {
			t.Errorf("%s: %d", p, w.Code)
			continue
		}
		body := w.Body.String()
		if !strings.Contains(body, `<link rel="canonical" href="`+u.Loc+`">`) {
			t.Errorf("%s: wrong canonical", p)
		}
		noEmDash(t, p, body)
		nodes := ldGraph(t, body)
		if strings.HasPrefix(p, "/library/") {
			if len(byType(nodes, "Article")) != 1 || len(byType(nodes, "BreadcrumbList")) != 1 {
				t.Errorf("%s: needs Article and BreadcrumbList", p)
			}
			if !strings.Contains(body, "t.me/bsurgery_bot?start=site") || !strings.Contains(body, `href="/about"`) {
				t.Errorf("%s: no way to the bot or the club page", p)
			}
		}
		if len(body) > 120<<10 {
			t.Errorf("%s is %d KB", p, len(body)>>10)
		}
	}
	if w := get(t, r, "/library/net-takoy-kartochki"); w.Code != 404 {
		t.Errorf("unknown card: %d", w.Code)
	}
}

// Hero stories and cases (names, numbers presented as real people) stay off the open pages.
func TestLibraryPagesHaveNoHeroStories(t *testing.T) {
	r := publicRouter()
	l := publicLib()
	var raw []map[string]any
	_ = json.Unmarshal(mustRead(t, "library_rich/tools.json"), &raw)
	checked := 0
	for _, x := range raw {
		hero, _ := x["hero"].(map[string]any)
		name, _ := hero["name"].(string)
		title, _ := x["title"].(string)
		i, ok := l.bySlug[slugify(title)]
		if name == "" || !ok || l.items[i].Kind != "tool" {
			continue
		}
		story, _ := hero["story"].([]any)
		body := get(t, r, "/library/"+l.items[i].Slug).Body.String()
		if len(story) > 0 && strings.Contains(body, hx(story[0].(string))[:60]) {
			t.Errorf("%s shows the hero story", title)
		}
		checked++
	}
	if checked < 10 {
		t.Errorf("checked only %d tools", checked)
	}
	// the first card's hero (Динара) appears only in the story and examples
	if body := get(t, r, "/library/platezhnyy-kalendar").Body.String(); strings.Contains(body, "Динара") {
		t.Error("platezhnyy-kalendar mentions the hero")
	}
}

func mustRead(t *testing.T, name string) []byte {
	t.Helper()
	b, err := content.LibRichRaw(name)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestPrivatePartsStayClosed(t *testing.T) {
	r := publicRouter()
	w := get(t, r, "/platform")
	if !strings.Contains(w.Header().Get("X-Robots-Tag"), "noindex") {
		t.Error("/platform lost noindex")
	}
	if w := get(t, r, "/dl/BS_SMM_Strategiya.pdf"); w.Code != http.StatusUnauthorized {
		t.Errorf("/dl without a session: %d", w.Code)
	}
}

func TestSiteFiles(t *testing.T) {
	r := publicRouter()
	for _, f := range []string{"/site/og.png", "/site/logo.png", "/site/logo-white.png"} {
		w := get(t, r, f)
		if w.Code != 200 || w.Header().Get("Content-Type") != "image/png" || w.Body.Len() < 1000 {
			t.Errorf("%s: %d %s %d", f, w.Code, w.Header().Get("Content-Type"), w.Body.Len())
		}
	}
	if w := get(t, r, "/site/..%2fembed.go"); w.Code != 404 {
		t.Errorf("path escape: %d", w.Code)
	}
}

func TestSlugify(t *testing.T) {
	for in, want := range map[string]string{
		"Кассовые разрывы":          "kassovye-razryvy",
		"Платёжный календарь":       "platezhnyy-kalendar",
		"5 почему?":                 "5-pochemu",
		"HADI цикл":                 "hadi-tsikl",
		"Аудит воронки: где утечка": "audit-voronki-gde-utechka",
		"Продуктовая матрица ABC":   "produktovaya-matritsa-abc",
	} {
		if got := slugify(in); got != want {
			t.Errorf("slugify(%q) = %q, want %q", in, got, want)
		}
	}
}
