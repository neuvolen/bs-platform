package web

import (
	"bytes"
	"compress/zlib"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// R83: every page the server opens shows the platform icon, /favicon.ico is
// served (a PDF tab asks for it), and the PDFs in the library have a title.
func TestR83Icons(t *testing.T) {
	r := publicRouter()
	fav := platformIcons()["favicon.png"]
	if fav == nil || !bytes.HasPrefix(fav.b, []byte("\x89PNG")) {
		t.Fatal("the platform icon is not cut out of platform.html")
	}
	for path, want := range map[string]string{
		"/favicon.ico": "image/x-icon", "/favicon.png": "image/png", "/apple-touch-icon.png": "image/png",
		"/apple-touch-icon-precomposed.png": "image/png", "/site.webmanifest": "application/manifest+json",
	} {
		w := get(t, r, path)
		if w.Code != 200 || !strings.HasPrefix(w.Header().Get("Content-Type"), want) {
			t.Errorf("%s: %d %s", path, w.Code, w.Header().Get("Content-Type"))
		}
	}
	ico := get(t, r, "/favicon.ico").Body.Bytes()
	if !bytes.HasPrefix(ico, []byte{0, 0, 1, 0, 1, 0}) || !bytes.Contains(ico, fav.b) {
		t.Error("/favicon.ico is not an icon with the platform PNG")
	}
	for _, path := range []string{"/about", "/library", "/privacy", "/terms", "/library/no-such-card"} {
		if b := get(t, r, path).Body.String(); !strings.Contains(b, IconLinks) || strings.Contains(b, `rel="icon" type="image/png" href="/site/logo.png"`) {
			t.Errorf("%s: no platform icon in <head>", path)
		}
	}
	if !strings.Contains(string(CallPage()), `href="/favicon.ico"`) {
		t.Error("the call page has another icon")
	}
	pdfs, _ := filepath.Glob("dl/*.pdf")
	for _, p := range pdfs {
		b, _ := os.ReadFile(p)
		if !pdfHasTitle(b) {
			t.Errorf("%s: no PDF title, the tab shows the file name", p)
		}
	}
}

// pdfHasTitle: the document info has /Title (also inside compressed object streams).
func pdfHasTitle(b []byte) bool {
	if bytes.Contains(b, []byte("/Title")) {
		return true
	}
	for rest := b; ; {
		i := bytes.Index(rest, []byte("stream"))
		if i < 0 {
			return false
		}
		rest = rest[i+6:]
		rest = bytes.TrimLeft(rest, "\r\n")
		j := bytes.Index(rest, []byte("endstream"))
		if j < 0 {
			return false
		}
		if zr, err := zlib.NewReader(bytes.NewReader(rest[:j])); err == nil {
			d, _ := io.ReadAll(io.LimitReader(zr, 1<<20))
			if bytes.Contains(d, []byte("/Title")) {
				return true
			}
		}
		rest = rest[j+9:]
	}
}

// R83: the page's reports reach the server log, one line, rate limited.
func TestR83ClientLog(t *testing.T) {
	r := publicRouter()
	post := func(body string) int {
		w := httptest.NewRecorder()
		r.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/clog", strings.NewReader(body)))
		return w.Code
	}
	if c := post(`{"k":"voice","ev":"stop","peak":-12}`); c != http.StatusNoContent {
		t.Fatalf("/clog: %d", c)
	}
	if g := clogClean("a\nb\x00c"+strings.Repeat("я", 50), 10); strings.ContainsAny(g, "\n\x00") || len([]rune(g)) > 11 {
		t.Errorf("clogClean: %q", g)
	}
	n := 0
	for i := 0; i < 60; i++ {
		if post(`{}`) == http.StatusTooManyRequests {
			n++
		}
	}
	if n == 0 {
		t.Error("no rate limit")
	}
}
