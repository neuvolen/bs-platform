package web_test

import (
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"

	"github.com/bnursik/business_surgery_backend/internal/server"
	"github.com/bnursik/business_surgery_backend/web"
	"github.com/gin-gonic/gin"
)

// R38a: the real router sends http visitors to https, the custom domain gets HSTS,
// the pages ask the browser to upgrade any http:// resource and contain none.
func TestR38aPagesHTTPS(t *testing.T) {
	gin.SetMode(gin.TestMode)
	t.Setenv("HSTS_HOSTS", "")
	r := server.SetupRouter()
	web.Register(r, "secret", "bs_session")

	req := httptest.NewRequest("GET", "http://app.bxclub.kz/?from=old", nil)
	req.Header.Set("X-Forwarded-Proto", "http")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusMovedPermanently || w.Header().Get("Location") != "https://app.bxclub.kz/?from=old" {
		t.Fatalf("http visit: %d %q", w.Code, w.Header().Get("Location"))
	}

	req = httptest.NewRequest("GET", "http://app.bxclub.kz/", nil)
	req.Header.Set("X-Forwarded-Proto", "https")
	w = httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != 200 {
		t.Fatalf("https visit: %d", w.Code)
	}
	if v := w.Header().Get("Strict-Transport-Security"); v != "max-age=31536000" {
		t.Fatalf("HSTS: %q", v)
	}
	if v := w.Header().Get("Content-Security-Policy"); !strings.Contains(v, "upgrade-insecure-requests") {
		t.Fatalf("CSP: %q", v)
	}
	body := w.Body.String()
	if !strings.Contains(body, "<html") {
		t.Fatal("login page not served")
	}
	// Ни одной http:// ссылки, картинки, скрипта, шрифта (кроме имён XML-пространств SVG)
	bad := regexp.MustCompile(`http://[^\s"'<>)]*`).FindAllString(body, -1)
	for _, u := range bad {
		if !strings.HasPrefix(u, "http://www.w3.org/") {
			t.Fatalf("login page has an insecure address: %s", u)
		}
	}

	// Railway's own domain: served over https, no HSTS
	req = httptest.NewRequest("GET", "http://bs-platform-production.up.railway.app/health", nil)
	req.Header.Set("X-Forwarded-Proto", "https")
	w = httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != 200 || w.Header().Get("Strict-Transport-Security") != "" {
		t.Fatalf("railway domain: %d HSTS %q", w.Code, w.Header().Get("Strict-Transport-Security"))
	}
}

// R38a: the login showcase pictures are public, webp, small, and every one the page uses exists.
func TestR38aPromoPictures(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := server.SetupRouter()
	web.Register(r, "secret", "bs_session")
	req := httptest.NewRequest("GET", "/", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	refs := regexp.MustCompile(`/promo/([\w-]+\.webp)`).FindAllStringSubmatch(w.Body.String(), -1)
	if len(refs) < 5 {
		t.Fatalf("login page shows %d platform screens, want at least 5", len(refs))
	}
	for _, m := range refs {
		req := httptest.NewRequest("GET", "/promo/"+m[1], nil)
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		if w.Code != 200 || w.Header().Get("Content-Type") != "image/webp" {
			t.Fatalf("%s: %d %q", m[1], w.Code, w.Header().Get("Content-Type"))
		}
		b := w.Body.Bytes()
		if len(b) > 300*1024 || len(b) < 12 || string(b[:4]) != "RIFF" || string(b[8:12]) != "WEBP" {
			t.Fatalf("%s: %d bytes, not a small webp", m[1], len(b))
		}
		req = httptest.NewRequest("GET", "/promo/"+m[1], nil)
		req.Header.Set("If-None-Match", w.Header().Get("ETag"))
		w2 := httptest.NewRecorder()
		r.ServeHTTP(w2, req)
		if w2.Code != http.StatusNotModified {
			t.Fatalf("%s: repeat visit %d, want 304", m[1], w2.Code)
		}
	}
	for _, bad := range []string{"/promo/..%2Fplatform.html", "/promo/x.png", "/promo/.hidden.webp", "/promo/nope.webp"} {
		w := httptest.NewRecorder()
		r.ServeHTTP(w, httptest.NewRequest("GET", bad, nil))
		if w.Code != http.StatusNotFound {
			t.Fatalf("%s: %d, want 404", bad, w.Code)
		}
	}
}
