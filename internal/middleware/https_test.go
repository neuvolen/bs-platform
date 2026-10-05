package middleware

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
)

func httpsRouter(t *testing.T) *gin.Engine {
	t.Helper()
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(HTTPS())
	ok := func(c *gin.Context) { c.String(http.StatusOK, "ok") }
	r.GET("/*p", ok)
	r.POST("/*p", ok)
	r.HEAD("/*p", ok)
	return r
}

func do(r *gin.Engine, method, url string, hdr map[string]string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, url, strings.NewReader("{}"))
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w
}

func TestHTTPSRedirectsPlainHTTP(t *testing.T) {
	t.Setenv("HSTS_HOSTS", "")
	r := httpsRouter(t)
	cases := []struct {
		method, url, proto string
		code               int
		loc                string
	}{
		{"GET", "http://app.bxclub.kz/", "http", 301, "https://app.bxclub.kz/"},
		{"GET", "http://app.bxclub.kz/platform?x=1", "HTTP", 301, "https://app.bxclub.kz/platform?x=1"},
		{"HEAD", "http://app.bxclub.kz/", "http", 301, "https://app.bxclub.kz/"},
		{"POST", "http://app.bxclub.kz/api/v1/platform/auth/telegram", "http", 308, "https://app.bxclub.kz/api/v1/platform/auth/telegram"},
		{"GET", "http://bs-platform-production.up.railway.app/dl/a.pdf", "http, https", 301, "https://bs-platform-production.up.railway.app/dl/a.pdf"},
	}
	for _, c := range cases {
		w := do(r, c.method, c.url, map[string]string{"X-Forwarded-Proto": c.proto})
		if w.Code != c.code || w.Header().Get("Location") != c.loc {
			t.Fatalf("%s %s (%s): got %d %q, want %d %q", c.method, c.url, c.proto, w.Code, w.Header().Get("Location"), c.code, c.loc)
		}
		if w.Header().Get("Strict-Transport-Security") != "" {
			t.Fatalf("HSTS must not be sent over plain http: %s", c.url)
		}
	}
}

func TestHTTPSServesWithoutRedirect(t *testing.T) {
	t.Setenv("HSTS_HOSTS", "")
	r := httpsRouter(t)
	for _, c := range []struct{ url, proto string }{
		{"http://app.bxclub.kz/", "https"},            // through Railway's proxy on https
		{"http://healthcheck.railway.app/health", ""}, // health check: no header
		{"http://localhost:8080/", "http"},            // local run
		{"http://127.0.0.1:8080/", "http"},
		{"http://10.0.0.5:8080/", "http"}, // private network
	} {
		h := map[string]string{}
		if c.proto != "" {
			h["X-Forwarded-Proto"] = c.proto
		}
		if w := do(r, "GET", c.url, h); w.Code != 200 {
			t.Fatalf("%s (%q): got %d, want 200", c.url, c.proto, w.Code)
		}
	}
}

func TestHSTSOnlyOnCustomDomainOverHTTPS(t *testing.T) {
	t.Setenv("HSTS_HOSTS", "")
	r := httpsRouter(t)
	get := func(url, proto string) string {
		return do(r, "GET", url, map[string]string{"X-Forwarded-Proto": proto}).Header().Get("Strict-Transport-Security")
	}
	if v := get("http://app.bxclub.kz/", "https"); v != "max-age=31536000" {
		t.Fatalf("app.bxclub.kz over https: HSTS %q", v)
	}
	if v := get("http://APP.BXCLUB.KZ:443/", "https"); v != "max-age=31536000" {
		t.Fatalf("host with port and capitals: HSTS %q", v)
	}
	if v := get("http://bs-platform-production.up.railway.app/", "https"); v != "" {
		t.Fatalf("railway domain must not get HSTS: %q", v)
	}
	if v := get("http://evilbxclub.kz/", "https"); v != "" {
		t.Fatalf("lookalike host must not match: %q", v)
	}
	if strings.Contains(HSTSValue, "includeSubDomains") {
		t.Fatal("HSTS must not cover bxclub.kz subdomains (the Tilda site)")
	}
	t.Setenv("HSTS_HOSTS", "example.org, .bxclub.kz")
	r2 := httpsRouter(t)
	if v := do(r2, "GET", "http://x.example.org/", map[string]string{"X-Forwarded-Proto": "https"}).Header().Get("Strict-Transport-Security"); v == "" {
		t.Fatal("HSTS_HOSTS from env is not used")
	}
}

func TestHTTPSURL(t *testing.T) {
	for in, want := range map[string]string{
		"":                              "",
		"https://app.bxclub.kz/":        "https://app.bxclub.kz",
		"http://app.bxclub.kz":          "https://app.bxclub.kz",
		"HTTP://app.bxclub.kz/platform": "https://app.bxclub.kz/platform",
		"app.bxclub.kz":                 "https://app.bxclub.kz",
		" bs-platform-production.up.railway.app ": "https://bs-platform-production.up.railway.app",
		"http://localhost:8080":                   "http://localhost:8080",
		"http://127.0.0.1:9000/x":                 "http://127.0.0.1:9000/x",
	} {
		if got := HTTPSURL(in); got != want {
			t.Fatalf("HTTPSURL(%q) = %q, want %q", in, got, want)
		}
	}
}
