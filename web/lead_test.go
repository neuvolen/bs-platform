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

// R27: a lead's session opens the platform page (the lead home is in it), but
// the library files under /dl stay for residents and the team.
func TestLeadSession(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	Register(r, "secret", "bs_session")
	tok := func(role string) string {
		s, _ := jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.MapClaims{"typ": "access", "sub": "tg:777", "role": role}).SignedString([]byte("secret"))
		return s
	}
	get := func(path, role string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		req.AddCookie(&http.Cookie{Name: "bs_session", Value: tok(role)})
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		return w
	}
	w := get("/", "lead")
	if w.Code != http.StatusOK || w.Body.String() != string(appPage.plain) || !strings.Contains(servedText(), "lhRender") {
		t.Fatalf("lead gets the platform page with the lead home: %d", w.Code)
	}
	ents, _ := fs.ReadDir(dlFS, "dl")
	n := 0
	for _, e := range ents {
		if strings.HasPrefix(e.Name(), ".") {
			continue
		}
		if c := get("/dl/"+e.Name(), "lead").Code; c != http.StatusForbidden {
			t.Errorf("%s for a lead: %d", e.Name(), c)
		}
		if c := get("/dl/"+e.Name(), "resident").Code; c != http.StatusOK {
			t.Errorf("%s for a resident: %d", e.Name(), c)
		}
		n++
	}
	if n == 0 {
		t.Fatal("no files in dl")
	}
}
