package web_test

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	"github.com/bnursik/business_surgery_backend/web"
	"github.com/gin-gonic/gin"
	"github.com/golang-jwt/jwt/v5"
)

// R52: http:// pictures from the data go through /img: a session is needed,
// inside addresses and non-pictures are refused, a picture comes back as is.
func TestR52ImageProxy(t *testing.T) {
	gin.SetMode(gin.TestMode)
	png := []byte("\x89PNG\r\n\x1a\n0000")
	src := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/page" {
			w.Header().Set("Content-Type", "text/html")
			_, _ = w.Write([]byte("<script>alert(1)</script>"))
			return
		}
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write(png)
	}))
	defer src.Close()
	r := gin.New()
	web.Register(r, "secret-r52-0123456789", "bs_session")
	tok, _ := jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.MapClaims{"typ": "access", "role": "admin", "sub": "1", "exp": time.Now().Add(time.Hour).Unix()}).SignedString([]byte("secret-r52-0123456789"))
	get := func(u string, cookie bool) *httptest.ResponseRecorder {
		req := httptest.NewRequest("GET", "/img?u="+url.QueryEscape(u), nil)
		if cookie {
			req.AddCookie(&http.Cookie{Name: "bs_session", Value: tok})
		}
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		return w
	}
	if w := get(src.URL+"/a.png", false); w.Code != 401 {
		t.Fatalf("no session: %d", w.Code)
	}
	for _, u := range []string{src.URL + "/a.png", "http://localhost/a.png", "http://10.0.0.5/a.png", "http://169.254.169.254/latest", "file:///etc/passwd", "ftp://x/a.png"} {
		if w := get(u, true); w.Code != 400 {
			t.Fatalf("%s: %d (inside addresses are refused)", u, w.Code)
		}
	}
	t.Setenv("IMG_PROXY_PRIVATE_OK", "1") // the test server is on 127.0.0.1
	w := get(src.URL+"/a.png", true)
	if w.Code != 200 || w.Header().Get("Content-Type") != "image/png" || w.Body.String() != string(png) || w.Header().Get("X-Content-Type-Options") != "nosniff" {
		t.Fatalf("picture: %d %q", w.Code, w.Header().Get("Content-Type"))
	}
	if w := get(src.URL+"/page", true); w.Code != 502 {
		t.Fatalf("a page is not a picture: %d", w.Code)
	}
}
