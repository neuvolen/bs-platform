package http

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
)

// Logging out must drop the session cookie, so "/" serves the entry page again.
func TestPlatformLogoutClearsSessionCookie(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.POST("/api/v1/platform/auth/logout", (&PlatformAuthHandler{}).Logout)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/platform/auth/logout", nil)
	req.Header.Set("X-Forwarded-Proto", "https")
	req.AddCookie(&http.Cookie{Name: PlatformSessionCookie, Value: "old"})
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusNoContent {
		t.Fatalf("status %d", w.Code)
	}
	res := w.Result()
	var got *http.Cookie
	for _, c := range res.Cookies() {
		if c.Name == PlatformSessionCookie {
			got = c
		}
	}
	if got == nil {
		t.Fatal("no Set-Cookie for the session")
	}
	if got.Value != "" || got.MaxAge >= 0 || got.Path != "/" || !got.HttpOnly || !got.Secure {
		t.Errorf("session cookie not cleared properly: %+v", got)
	}
}
