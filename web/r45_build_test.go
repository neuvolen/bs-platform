package web

import (
	"net/http"
	"net/http/httptest"
	"os"
	"regexp"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
)

// R45: the page is served as a small shell plus cached files.

func TestR45PageIsTakenApart(t *testing.T) {
	m := lastManifest
	if len(m.Errors) > 0 {
		t.Fatalf("scripts left whole: %v", m.Errors)
	}
	shell := appHTML
	if strings.Contains(shell, "<style") {
		t.Fatalf("a <style> is left in the shell")
	}
	if n := strings.Count(shell, "<script src=\"/a/"); n != len(m.Scripts) || n < 15 {
		t.Fatalf("scripts as files: %d of %d", n, len(m.Scripts))
	}
	if strings.Contains(shell, "fonts.googleapis.com") || !strings.Contains(shell, m.Fonts) {
		t.Fatalf("own fonts")
	}
	if strings.Contains(shell, "data:image/png;base64,iVBORw0KGgoAAAANSUhEUgAAAaQ") {
		t.Fatalf("the big logo is still inline")
	}
	if !strings.Contains(shell, `id="bsTourTexts"`) || len(ParseTourTexts([]byte(shell))) == 0 {
		t.Fatalf("the tour's texts must stay in the shell (voice map)")
	}
	if len(m.Chunks) < 10 {
		t.Fatalf("lazy chunks: %d", len(m.Chunks))
	}
	t.Logf("shell %d B, br %d B; scripts %d, chunks %d", len(shell), len(appPage.br), len(m.Scripts), len(m.Chunks))
	// no business data in any file (stripSeed runs before)
	for _, n := range AssetNames() {
		a := assetByName(n)
		if strings.HasSuffix(n, ".js") && regexp.MustCompile(`var (PL_ROWS|RESIDENTS|FINES|SDATA) = [\[{]["\d]`).Match(a.plain) {
			t.Fatalf("%s carries seed data", n)
		}
	}
}

func TestR45AssetServing(t *testing.T) {
	gin.SetMode(gin.TestMode)
	WaitCompressed()
	r := gin.New()
	Register(r, "secret-r45-0123456789-0123456789", "bs_session")
	css := lastManifest.CSS
	// a page file needs a session; the fonts do not
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest("GET", css, nil))
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("css without a session: %d", w.Code)
	}
	w = httptest.NewRecorder()
	req := httptest.NewRequest("GET", lastManifest.Fonts, nil)
	req.Header.Set("Accept-Encoding", "gzip, deflate, br, zstd")
	r.ServeHTTP(w, req)
	if w.Code != 200 || w.Header().Get("Content-Encoding") != "br" || !strings.Contains(w.Header().Get("Cache-Control"), "immutable") {
		t.Fatalf("fonts css: %d %v", w.Code, w.Header())
	}
	et := w.Header().Get("ETag")
	w = httptest.NewRecorder()
	req = httptest.NewRequest("GET", lastManifest.Fonts, nil)
	req.Header.Set("Accept-Encoding", "gzip")
	req.Header.Set("If-None-Match", et)
	r.ServeHTTP(w, req)
	if w.Code != http.StatusNotModified {
		t.Fatalf("revalidation: %d", w.Code)
	}
	w = httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest("GET", "/a/nope.0123456789abcdef.js", nil))
	if w.Code != 404 {
		t.Fatalf("unknown file: %d", w.Code)
	}
}

func TestR45LazyTransform(t *testing.T) {
	src := "var a = 1;\nfunction big(x, y){ var s = 0; for(var i = 0; i < 10; i++){ s += i; } /* " + strings.Repeat("pad ", 200) + " */ return s + x + y + a; }\n" +
		"function small(){ return 1 }\nvar re = /}{/g, d = 4 / 2; function big2(){ return '" + strings.Repeat("}", 600) + "'; }\n"
	out, err := lazify(src, "t", 0, 1, func(string, string) bool { return false })
	if err != nil {
		t.Fatal(err)
	}
	if len(out.Chunks) != 1 || len(out.Chunks[0].Funcs) != 2 || out.Chunks[0].Names[0] != "big" || out.Chunks[0].Names[1] != "big2" {
		t.Fatalf("chunks: %+v", out.Chunks)
	}
	if !strings.Contains(out.Script, "function big(x, y){return __LZ.f(0,0,this,arguments,new.target)}") || !strings.Contains(out.Script, "function small(){ return 1 }") {
		t.Fatalf("stub: %s", out.Script)
	}
	// an IIFE gets its eval hook after "use strict"
	src2 := "(function(){\n'use strict';\nvar k = 2;\nfunction f(a){ return a + k + '" + strings.Repeat("x", 600) + "'; }\nwindow.f = f;\n})();"
	out, err = lazify(src2, "t2", 5, 7, func(string, string) bool { return false })
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.Script, "'use strict';function __lzE(s){return eval(s)}__LZ.s(7,__lzE);") || out.Chunks[0].Scope != 7 ||
		!strings.Contains(out.Script, "function f(a){return __LZ.f(5,0,") {
		t.Fatalf("iife: %s", out.Script)
	}
	// a broken boundary is refused, the script stays whole
	if _, err := lazify("function f(){ "+strings.Repeat("a;", 400)+" }}", "t3", 0, 0, func(string, string) bool { return false }); err == nil {
		t.Fatalf("unbalanced script accepted")
	}
}

// The browser suites open web/platform.html as a file. The same page as the
// server ships it (functions moved to chunks, data behind getters), but in
// one file and not minified, is written to $R45_INLINE_OUT so the r45 runner
// can run every suite on it too.
func TestR45InlineVariant(t *testing.T) {
	out, err := buildSiteWith(string(platformHTML), buildOpts{Inline: true})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out, `<script src="/a/`) || !strings.Contains(out, `id="lzc-0"`) || !strings.Contains(out, "__LZ.f(") {
		t.Fatalf("inline variant: not transformed")
	}
	if !strings.Contains(out, "var RESIDENTS = [") {
		t.Fatalf("the file variant keeps the page's own data (as the suites expect)")
	}
	if f := os.Getenv("R45_INLINE_OUT"); f != "" {
		if err := os.WriteFile(f, []byte(out), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

// R8DOC's version is read on every open as R8DOC_VER, without touching the
// data (it comes from a file on first use): the two must stay equal.
func TestR45R8DocVersion(t *testing.T) {
	html := string(platformHTML)
	m := regexp.MustCompile(`(?m)^var R8DOC = \{"ver":"([^"]+)"`).FindStringSubmatch(html)
	v := regexp.MustCompile(`(?m)^var R8DOC_VER = '([^']+)';`).FindStringSubmatch(html)
	if m == nil || v == nil || m[1] != v[1] {
		t.Fatalf("R8DOC.ver %v and R8DOC_VER %v differ: bump both", m, v)
	}
	// the lazy data keeps its keys and the start-up ones stay in the page
	shell := appHTML
	for _, n := range lastManifest.Scripts {
		a := assetByName(n)
		if a != nil && strings.Contains(string(a.plain), "__LZ.o(") {
			shell += string(a.plain)
		}
	}
	if !strings.Contains(shell, `var R8DOC=__LZ.o(`) && !strings.Contains(shell, `var R8DOC = __LZ.o(`) {
		t.Fatalf("R8DOC is not behind a getter")
	}
	if lastManifest.Data["R8DOC"] == "" || lastManifest.Data["GALLUP_DB"] == "" {
		t.Fatalf("lazy data files: %v", lastManifest.Data)
	}
}

// The login page also takes our fonts and the page's icons as files.
func TestR45LoginPage(t *testing.T) {
	l := string(loginPage.plain)
	if strings.Contains(l, "fonts.googleapis.com") || !strings.Contains(l, lastManifest.Fonts) || !strings.Contains(l, `rel="preload" as="font"`) {
		t.Fatalf("login fonts")
	}
	if strings.Contains(l, "data:image/png;base64,iVBORw0KGgoAAAANSUhEUgAAALQ") || !strings.Contains(l, `<img class="lg" alt="" src="/a/img.`) {
		t.Fatalf("login icons as files")
	}
	if len(loginPage.br) == 0 || len(loginPage.br) >= len(loginPage.gz) {
		t.Fatalf("login brotli %d gzip %d", len(loginPage.br), len(loginPage.gz))
	}
}

// servedText: the shell and the text of every file it loads (scripts,
// chunks, data, CSS), for tests that look for code in "the page".
func servedText() string {
	var b strings.Builder
	b.Write(appPage.plain)
	for _, n := range AssetNames() {
		if a := assetByName(n); a != nil && !strings.HasPrefix(a.ctype, "font/") && !strings.HasPrefix(a.ctype, "image/") {
			b.WriteString("\n")
			b.Write(a.plain)
		}
	}
	return b.String()
}
