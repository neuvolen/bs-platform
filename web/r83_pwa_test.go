package web

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/golang-jwt/jwt/v5"
)

// R83: the platform installs as a phone app; the worker keeps only the
// shell; the sales manager lands in his CRM and never gets the platform.
func TestR83PWAAndManagerRedirect(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	Register(r, "secret", "bs_session")
	get := func(path, role string) *httptest.ResponseRecorder {
		req := httptest.NewRequest("GET", path, nil)
		if role != "" {
			tok, _ := jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.MapClaims{"typ": "access", "role": role}).SignedString([]byte("secret"))
			req.AddCookie(&http.Cookie{Name: "bs_session", Value: tok})
		}
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		return w
	}
	w := get("/manifest.webmanifest", "")
	var m struct {
		StartURL  string `json:"start_url"`
		Display   string `json:"display"`
		Icons     []struct{ Src, Purpose string }
		Shortcuts []struct{ Name, URL string }
	}
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &m) != nil || m.Display != "standalone" || len(m.Icons) != 4 || len(m.Shortcuts) != 1 || m.Shortcuts[0].URL != "/?note=1" {
		t.Fatalf("manifest: %d %s", w.Code, w.Body.String())
	}
	for _, ic := range m.Icons {
		if w := get(ic.Src, ""); w.Code != 200 || w.Header().Get("Content-Type") != "image/png" {
			t.Fatalf("icon %s: %d", ic.Src, w.Code)
		}
	}
	sw := get("/sw.js", "")
	if sw.Code != 200 || !strings.Contains(sw.Header().Get("Content-Type"), "javascript") || !strings.Contains(sw.Body.String(), "'/api/'") || strings.Contains(sw.Body.String(), "caches.open(V).then(function(c){ return c.put") {
		t.Fatalf("sw: %d %s", sw.Code, sw.Body.String())
	}
	if w := get("/pwa/offline.html", ""); w.Code != 200 || !strings.Contains(w.Body.String(), "Нет связи") {
		t.Fatalf("offline: %d", w.Code)
	}
	if w := get("/pwa/../embed.go", ""); w.Code == 200 {
		t.Fatal("path escape")
	}
	// the page links the manifest; the manager is sent to /crm and gets no platform files
	if w := get("/", "admin"); w.Code != 200 || !strings.Contains(w.Body.String(), `rel="manifest"`) || !strings.Contains(w.Body.String(), "r83bScript") {
		t.Fatalf("platform page: %d", w.Code)
	}
	if w := get("/", ""); !strings.Contains(w.Body.String(), `rel="manifest"`) || !strings.Contains(w.Body.String(), "serviceWorker") {
		t.Fatal("login page without the manifest")
	}
	if w := get("/", "sales"); w.Code != http.StatusFound || w.Header().Get("Location") != "/crm" {
		t.Fatalf("manager at /: %d %s", w.Code, w.Header().Get("Location"))
	}
	if w := get("/dl/x.pdf", "sales"); w.Code == 200 {
		t.Fatal("library file for a manager")
	}
	if !strings.Contains(ScriptsDefJS(), "После разбора") || !strings.HasPrefix(ScriptsDefJS(), "[") || !strings.HasSuffix(ScriptsDefJS(), "]") {
		t.Fatalf("scripts: %.80s", ScriptsDefJS())
	}
}
