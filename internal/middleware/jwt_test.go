package middleware

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/golang-jwt/jwt/v5"
)

func token(t *testing.T, role string) string {
	t.Helper()
	s, err := jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.MapClaims{"typ": "access", "sub": "tg:777", "role": role}).SignedString([]byte("secret"))
	if err != nil {
		t.Fatal(err)
	}
	return s
}

// A lead's token opens nothing guarded by AuthJWT; only AuthJWTAllowLead lets it through.
func TestLeadTokenOnlyWhereAllowed(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	ok := func(c *gin.Context) { c.String(http.StatusOK, c.GetString("role")) }
	r.GET("/team", AuthJWT([]byte("secret")), ok)
	r.GET("/perm", AuthJWT([]byte("secret")), RequirePerm("profile:read"), ok)
	r.GET("/lead", AuthJWTAllowLead([]byte("secret")), RequireRole("lead", "admin"), ok)
	r.GET("/res", AuthJWTAllowLead([]byte("secret")), RequireRole("resident"), ok)
	call := func(path, role string) (int, string) {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		if role != "" {
			req.Header.Set("Authorization", "Bearer "+token(t, role))
		}
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		return w.Code, w.Body.String()
	}
	if c, b := call("/team", "lead"); c != http.StatusForbidden || !strings.Contains(b, "lead_forbidden") {
		t.Fatalf("lead on a team route: %d %s", c, b)
	}
	if c, _ := call("/perm", "lead"); c != http.StatusForbidden {
		t.Fatalf("lead on a perm route: %d", c)
	}
	if c, b := call("/lead", "lead"); c != http.StatusOK || b != "lead" {
		t.Fatalf("lead on the lead route: %d %s", c, b)
	}
	if c, _ := call("/res", "lead"); c != http.StatusForbidden {
		t.Fatalf("lead on a resident route: %d", c)
	}
	if c, b := call("/team", "admin"); c != http.StatusOK || b != "admin" {
		t.Fatalf("admin: %d %s", c, b)
	}
	if c, _ := call("/lead", "resident"); c != http.StatusForbidden {
		t.Fatalf("resident on the lead home: %d", c)
	}
	if c, _ := call("/team", ""); c != http.StatusUnauthorized {
		t.Fatalf("no token: %d", c)
	}
}
