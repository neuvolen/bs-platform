package app

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"regexp"
	"testing"

	pg "github.com/bnursik/business_surgery_backend/internal/repository/pg"
	"github.com/bnursik/business_surgery_backend/internal/server"
	"github.com/golang-jwt/jwt/v5"
	"golang.org/x/oauth2"
)

// R27: the older API (users, diseases, admin, moderator, me/*) refuses a
// lead's platform token on every route that needs a login.
func TestLeadTokenOnOlderAPI(t *testing.T) {
	dsn := os.Getenv("BS_TEST_DSN")
	if dsn == "" {
		t.Skip("BS_TEST_DSN not set")
	}
	db, err := pg.NewDB(context.Background(), dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Pool.Close()
	const secret = "lead-routes-secret-0123456789"
	r := server.SetupRouter(BuildHTTPModules(BuildDeps(db, secret, "1h", "24h"), secret, &oauth2.Config{}, "")...)
	tok, _ := jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.MapClaims{"typ": "access", "sub": "tg:777001", "role": "lead"}).SignedString([]byte(secret))
	param := regexp.MustCompile(`[:*][A-Za-z_]+`)
	call := func(method, path, bearer string) int {
		req := httptest.NewRequest(method, path, nil)
		if bearer != "" {
			req.Header.Set("Authorization", "Bearer "+bearer)
		}
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		return w.Code
	}
	checked := 0
	for _, rt := range r.Routes() {
		path := param.ReplaceAllString(rt.Path, "00000000-0000-0000-0000-000000000000")
		if a := call(rt.Method, path, ""); a != http.StatusUnauthorized {
			continue
		}
		checked++
		// 403 where the bearer is read; 401 where it is not (refresh takes a refresh token)
		if c := call(rt.Method, path, tok); c != http.StatusForbidden && c != http.StatusUnauthorized {
			t.Errorf("%s %s: lead got %d", rt.Method, rt.Path, c)
		}
	}
	if checked < 20 {
		t.Fatalf("only %d guarded routes", checked)
	}
	t.Logf("older API: lead refused on %d guarded routes", checked)
}
