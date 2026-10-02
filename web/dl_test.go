package web

import (
	"io/fs"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/golang-jwt/jwt/v5"
)

// /dl/<name>: only signed-in users, only plain names, right type and name.
func TestDLFiles(t *testing.T) {
	for _, bad := range []string{"", ".keep", "../embed.go", "a/b.pdf", "nope.pdf"} {
		if _, _, ok := DLFile(bad); ok {
			t.Errorf("%q must not be served", bad)
		}
	}
	gin.SetMode(gin.TestMode)
	r := gin.New()
	Register(r, "secret", "bs_session")
	tok, _ := jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.MapClaims{"typ": "access"}).SignedString([]byte("secret"))
	ents, _ := fs.ReadDir(dlFS, "dl")
	served := 0
	for _, e := range ents {
		name := e.Name()
		if strings.HasPrefix(name, ".") {
			continue
		}
		req := httptest.NewRequest(http.MethodGet, "/dl/"+name, nil)
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		if w.Code != http.StatusUnauthorized {
			t.Errorf("%s without session: %d", name, w.Code)
		}
		req = httptest.NewRequest(http.MethodGet, "/dl/"+name, nil)
		req.AddCookie(&http.Cookie{Name: "bs_session", Value: tok})
		w = httptest.NewRecorder()
		r.ServeHTTP(w, req)
		cd := w.Header().Get("Content-Disposition")
		if w.Code != http.StatusOK || w.Body.Len() == 0 || !strings.Contains(cd, "attachment") || !strings.Contains(cd, name) {
			t.Errorf("%s: %d %q", name, w.Code, cd)
		}
		ct := w.Header().Get("Content-Type")
		if (strings.HasSuffix(name, ".pdf") && ct != "application/pdf") || (strings.HasSuffix(name, ".xlsx") && !strings.Contains(ct, "spreadsheetml")) {
			t.Errorf("%s: content type %q", name, ct)
		}
		served++
	}
	req := httptest.NewRequest(http.MethodGet, "/dl/nope.xlsx", nil)
	req.AddCookie(&http.Cookie{Name: "bs_session", Value: tok})
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusNotFound {
		t.Errorf("missing file: %d", w.Code)
	}
	t.Logf("files served: %d", served)
}
