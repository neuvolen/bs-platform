package web

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"html"
	"net/http"
	"net/http/httptest"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/gin-gonic/gin"
)

// ── R83: SEO/GEO, the daily check of the open pages ──
// «Хочу, чтобы ты сам всё это делал и также ежедневно оптимизировал SEO».
//
// SEOAudit opens every page of the sitemap in-process (the same handlers
// the site serves) and checks: title length, description length, one H1,
// canonical equal to the page's address, JSON-LD that parses and has
// @context/@type, internal links that answer 200, the sitemap and robots,
// llms.txt and llms-full.txt sizes. It fixes what is safe by itself:
//   - lastmod: a page whose content changed since the last run gets today's
//     date in the sitemap (the content hash is kept by the caller);
//   - description: a page with none or shorter than 50 characters gets one
//     from its own first paragraph (a long one is only reported).
// Titles, H1 and broken links go to the report for a person.

// SEOIssue: one finding of the check.
type SEOIssue struct {
	Sev   string `json:"sev"` // error | warn | info
	Check string `json:"check"`
	URL   string `json:"url,omitempty"`
	Msg   string `json:"msg"`
	Fixed bool   `json:"fixed,omitempty"`
}

// SEOPage: what the check remembers of a page between runs.
type SEOPage struct {
	Hash    string `json:"h"`
	Lastmod string `json:"m"`
}

// SEOReport: one run.
type SEOReport struct {
	At        string         `json:"at"`
	Day       string         `json:"day"`
	Took      string         `json:"took"`
	Pages     int            `json:"pages"`
	OKPages   int            `json:"okPages"`
	Score     int            `json:"score"` // % of pages without errors and warnings
	Links     int            `json:"links"` // internal links checked
	Broken    int            `json:"broken"`
	Changed   int            `json:"changed"` // pages with new content (lastmod today)
	Fixed     int            `json:"fixed"`
	Errors    int            `json:"errors"`
	Warns     int            `json:"warns"`
	Counts    map[string]int `json:"counts"`
	LLMS      int            `json:"llms"`     // bytes
	LLMSFull  int            `json:"llmsFull"` // bytes
	Sitemap   int            `json:"sitemap"`  // urls
	Issues    []SEOIssue     `json:"issues"`   // at most seoMaxIssues
	MoreIssue int            `json:"moreIssues,omitempty"`
}

const (
	seoMaxIssues = 300
	seoTitleMin  = 20
	seoTitleMax  = 90 // Google shows about 60; up to 90 the page still reads well in AI answers
	seoDescMin   = 50
	seoDescMax   = 320 // longer is cut in the results and reads as spam
	seoLLMSMax   = 256 << 10
	seoFullMax   = 600 << 10
)

var (
	seoMu       sync.RWMutex
	seoLastmods = map[string]string{} // absolute url → YYYY-MM-DD
	seoDescs    = map[string]string{} // canonical → description
)

// seoLastmod: the page's lastmod for the sitemap (siteUpdated until the job ran).
func seoLastmod(loc string) string {
	seoMu.RLock()
	defer seoMu.RUnlock()
	if m := seoLastmods[loc]; m != "" {
		return m
	}
	return siteUpdated
}

// seoDesc: the page's own description, or the job's one when the own is
// missing or too short.
func seoDesc(canonical, desc string) string {
	if utf8.RuneCountInString(strings.TrimSpace(desc)) >= seoDescMin {
		return desc
	}
	seoMu.RLock()
	defer seoMu.RUnlock()
	if d := seoDescs[canonical]; d != "" {
		return d
	}
	return desc
}

// SetSEOFixes applies the job's lastmods and descriptions; the open pages
// are built again on the next visit. Returns whether anything changed.
func SetSEOFixes(lastmod, desc map[string]string) bool {
	seoMu.Lock()
	changed := !sameMap(seoLastmods, lastmod) || !sameMap(seoDescs, desc)
	if changed {
		seoLastmods, seoDescs = copyMap(lastmod), copyMap(desc)
	}
	seoMu.Unlock()
	if changed {
		resetPublicPages()
	}
	return changed
}

func sameMap(a, b map[string]string) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if b[k] != v {
			return false
		}
	}
	return true
}

func copyMap(m map[string]string) map[string]string {
	out := make(map[string]string, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}

func resetPublicPages() {
	pubMu.Lock()
	pubPages = map[string]page{}
	pubMu.Unlock()
}

var (
	seoTitleRe  = regexp.MustCompile(`(?is)<title>(.*?)</title>`)
	seoDescRe   = regexp.MustCompile(`(?i)<meta name="description" content="([^"]*)"`)
	seoCanonRe  = regexp.MustCompile(`(?i)<link rel="canonical" href="([^"]*)"`)
	seoH1Re     = regexp.MustCompile(`(?i)<h1[\s>]`)
	seoLDRe     = regexp.MustCompile(`(?is)<script type="application/ld\+json">(.*?)</script>`)
	seoHrefRe   = regexp.MustCompile(`(?i)<a\s[^>]*href="([^"]+)"`)
	seoPRe      = regexp.MustCompile(`(?is)<p[^>]*>(.*?)</p>`)
	seoTagRe    = regexp.MustCompile(`<[^>]+>`)
	seoLocRe    = regexp.MustCompile(`<loc>([^<]+)</loc>`)
	seoFooterRe = regexp.MustCompile(`(?s)<footer>.*?</footer>`)
)

// seoSkip: paths of the app, not of the open site (the audit engine does
// not serve them; the main router does).
func seoSkip(p string) bool {
	if p == "/" || p == "" {
		return true
	}
	for _, c := range append([]string{"/t/", "/login", "/app", "/s/", "/b/", "/in/"}, robotsClosed...) {
		if strings.HasPrefix(p, c) || p == strings.TrimSuffix(c, "/") {
			return true
		}
	}
	return false
}

// seoEngine: the open pages alone, served in-process.
func seoEngine() *gin.Engine {
	e := gin.New()
	RegisterPublic(e)
	return e
}

func seoGet(e http.Handler, path string) (int, string) {
	req := httptest.NewRequest(http.MethodGet, path, nil)
	w := httptest.NewRecorder()
	e.ServeHTTP(w, req)
	return w.Code, w.Body.String()
}

func seoPlain(s string) string {
	return strings.Join(strings.Fields(html.UnescapeString(seoTagRe.ReplaceAllString(s, " "))), " ")
}

// seoMakeDesc: up to 155 characters from the page's first real paragraph.
func seoMakeDesc(body string) string {
	main := body
	if i := strings.Index(body, "<main"); i >= 0 {
		main = body[i:]
	}
	for _, m := range seoPRe.FindAllStringSubmatch(main, 12) {
		t := seoPlain(m[1])
		if utf8.RuneCountInString(t) < 60 {
			continue
		}
		r := []rune(t)
		if len(r) <= 155 {
			return t
		}
		cutAt := 155
		for cutAt > 100 && r[cutAt] != ' ' {
			cutAt--
		}
		return strings.TrimRight(string(r[:cutAt]), " ,.;:") + "…"
	}
	return ""
}

// SEOAudit runs the check. prev: the pages remembered by the last run (the
// caller keeps it); returns the report, the pages to remember, the lastmods
// and descriptions to apply (SetSEOFixes).
func SEOAudit(prev map[string]SEOPage, day string) (SEOReport, map[string]SEOPage, map[string]string, map[string]string) {
	start := time.Now()
	e := seoEngine()
	rep := SEOReport{At: start.UTC().Format(time.RFC3339), Day: day, Counts: map[string]int{}}
	var issues []SEOIssue
	add := func(sev, check, url, msg string, fixed bool) {
		rep.Counts[check]++
		switch sev {
		case "error":
			rep.Errors++
		case "warn":
			rep.Warns++
		}
		if fixed {
			rep.Fixed++
		}
		issues = append(issues, SEOIssue{Sev: sev, Check: check, URL: url, Msg: msg, Fixed: fixed})
	}
	pages := map[string]SEOPage{}
	lastmod := map[string]string{}
	descs := map[string]string{}
	// robots and sitemap
	if code, body := seoGet(e, "/robots.txt"); code != 200 || !strings.Contains(body, "Sitemap: "+SiteURL+"/sitemap.xml") {
		add("error", "robots", "/robots.txt", "robots.txt не отдаётся или в нём нет строки Sitemap", false)
	}
	code, sm := seoGet(e, "/sitemap.xml")
	if code != 200 {
		add("error", "sitemap", "/sitemap.xml", "sitemap.xml ответил "+strconv.Itoa(code), false)
	}
	var locs []string
	for _, m := range seoLocRe.FindAllStringSubmatch(sm, -1) {
		locs = append(locs, html.UnescapeString(m[1]))
	}
	rep.Sitemap = len(locs)
	// llms.txt
	for _, f := range []struct {
		path string
		max  int
		dst  *int
	}{{"/llms.txt", seoLLMSMax, &rep.LLMS}, {"/llms-full.txt", seoFullMax, &rep.LLMSFull}} {
		c, b := seoGet(e, f.path)
		*f.dst = len(b)
		switch {
		case c != 200 || len(b) == 0:
			add("error", "llms", f.path, f.path+" не отдаётся", false)
		case len(b) > f.max:
			add("warn", "llms", f.path, f.path+": "+strconv.Itoa(len(b)>>10)+" КБ, больше "+strconv.Itoa(f.max>>10)+" КБ", false)
		case !strings.HasPrefix(b, "# "):
			add("warn", "llms", f.path, f.path+" должен начинаться с заголовка «# »", false)
		}
	}
	links := map[string]string{} // path → the first page linking to it
	for _, loc := range locs {
		if !strings.HasPrefix(loc, SiteURL) {
			add("error", "sitemap", loc, "адрес в sitemap не на "+SiteURL, false)
			continue
		}
		path := strings.TrimPrefix(loc, SiteURL)
		c, body := seoGet(e, path)
		rep.Pages++
		bad := false
		issue := func(sev, check, msg string, fixed bool) {
			if !fixed {
				bad = true
			}
			add(sev, check, path, msg, fixed)
		}
		if c != 200 {
			issue("error", "status", "страница из sitemap ответила "+strconv.Itoa(c), false)
			continue
		}
		// content hash: the page without the footer (its date is the site's)
		sum := sha256.Sum256([]byte(seoFooterRe.ReplaceAllString(body, "")))
		h := hex.EncodeToString(sum[:8])
		p := SEOPage{Hash: h, Lastmod: siteUpdated}
		if old, ok := prev[loc]; ok {
			p.Lastmod = old.Lastmod
			if old.Hash != h {
				p.Lastmod = day
				rep.Changed++
			}
		}
		pages[loc] = p
		lastmod[loc] = p.Lastmod
		// title
		title := ""
		if m := seoTitleRe.FindStringSubmatch(body); m != nil {
			title = html.UnescapeString(strings.TrimSpace(m[1]))
		}
		switch n := utf8.RuneCountInString(title); {
		case n == 0:
			issue("error", "title", "нет <title>", false)
		case n < seoTitleMin:
			issue("warn", "title", "заголовок короткий: "+strconv.Itoa(n)+" символов", false)
		case n > seoTitleMax:
			issue("warn", "title", "заголовок длинный: "+strconv.Itoa(n)+" символов, в выдаче обрежется", false)
		}
		// description (the page shows the job's one already when it was fixed before)
		desc := ""
		if m := seoDescRe.FindStringSubmatch(body); m != nil {
			desc = html.UnescapeString(m[1])
		}
		if n := utf8.RuneCountInString(strings.TrimSpace(desc)); n < seoDescMin {
			if fx := seoMakeDesc(body); fx != "" {
				descs[loc] = fx
				issue("info", "description", "описания не было или в нём "+strconv.Itoa(n)+" символов: взято из текста страницы", true)
			} else {
				issue("warn", "description", "описание "+strconv.Itoa(n)+" символов, в тексте нет абзаца для замены", false)
			}
		} else if n > seoDescMax {
			issue("warn", "description", "описание "+strconv.Itoa(n)+" символов: в выдаче покажут первые 160", false)
		} else {
			seoMu.RLock()
			fx := seoDescs[loc]
			seoMu.RUnlock()
			if fx != "" && fx == desc {
				descs[loc] = fx // keep the fix that is live
			}
		}
		// H1
		if n := len(seoH1Re.FindAllStringIndex(body, -1)); n != 1 {
			issue("error", "h1", "заголовков H1: "+strconv.Itoa(n)+", нужен один", false)
		}
		// canonical
		if m := seoCanonRe.FindStringSubmatch(body); m == nil {
			issue("error", "canonical", "нет canonical", false)
		} else if html.UnescapeString(m[1]) != loc {
			issue("error", "canonical", "canonical "+m[1]+" не совпадает с адресом страницы", false)
		}
		// JSON-LD
		lds := seoLDRe.FindAllStringSubmatch(body, -1)
		if len(lds) == 0 {
			issue("warn", "jsonld", "нет JSON-LD", false)
		}
		for _, m := range lds {
			var v map[string]any
			if err := json.Unmarshal([]byte(m[1]), &v); err != nil {
				issue("error", "jsonld", "JSON-LD не читается: "+err.Error(), false)
				continue
			}
			if v["@context"] == nil || (v["@type"] == nil && v["@graph"] == nil) {
				issue("error", "jsonld", "в JSON-LD нет @context или @type", false)
			}
		}
		// internal links
		for _, m := range seoHrefRe.FindAllStringSubmatch(body, -1) {
			href := html.UnescapeString(m[1])
			if strings.HasPrefix(href, SiteURL) {
				href = strings.TrimPrefix(href, SiteURL)
				if href == "" {
					href = "/"
				}
			}
			if !strings.HasPrefix(href, "/") || strings.HasPrefix(href, "//") || strings.ContainsAny(href, "'\"+{}$<> ") {
				continue // another site, or a link built by the page's script
			}
			if i := strings.IndexAny(href, "?#"); i >= 0 {
				href = href[:i]
			}
			if seoSkip(href) {
				continue
			}
			if _, ok := links[href]; !ok {
				links[href] = path
			}
		}
		if !bad {
			rep.OKPages++
		}
	}
	// internal links: each once
	var lk []string
	for l := range links {
		lk = append(lk, l)
	}
	sort.Strings(lk)
	for _, l := range lk {
		rep.Links++
		if c, _ := seoGet(e, l); c != 200 {
			rep.Broken++
			add("error", "link", links[l], "битая внутренняя ссылка "+l+" (ответ "+strconv.Itoa(c)+")", false)
		}
	}
	// sitemap lastmod: what the sitemap says now vs what the content says
	stale := 0
	for _, loc := range locs {
		if m, ok := lastmod[loc]; ok && seoLastmod(loc) != m {
			stale++
		}
	}
	if stale > 0 {
		add("info", "lastmod", "/sitemap.xml", "lastmod обновлён у "+strconv.Itoa(stale)+" страниц по изменению содержимого", true)
	}
	if rep.Pages > 0 {
		rep.Score = rep.OKPages * 100 / rep.Pages
	}
	// errors first, then warnings, then the fixed
	rank := map[string]int{"error": 0, "warn": 1, "info": 2}
	sort.SliceStable(issues, func(i, j int) bool { return rank[issues[i].Sev] < rank[issues[j].Sev] })
	if len(issues) > seoMaxIssues {
		rep.MoreIssue = len(issues) - seoMaxIssues
		issues = issues[:seoMaxIssues]
	}
	rep.Issues = issues
	rep.Took = time.Since(start).Round(time.Millisecond).String()
	resetPublicPages() // the audit built every page: let visitors rebuild only what they open
	return rep, pages, lastmod, descs
}
