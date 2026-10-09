package server

import (
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
)

// R70: the Mini App (GitHub Pages) sends initData in X-Tg-Init: the preflight allows it.
func TestCORSAllowsTgInitHeader(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := SetupRouter()
	req := httptest.NewRequest("OPTIONS", "/api/v1/app/call?action=getBotCache", nil)
	req.Header.Set("Origin", "https://neuvolen.github.io")
	req.Header.Set("Access-Control-Request-Method", "GET")
	req.Header.Set("Access-Control-Request-Headers", "x-tg-init")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code >= 300 || !strings.Contains(strings.ToLower(w.Header().Get("Access-Control-Allow-Headers")), "x-tg-init") {
		t.Fatalf("preflight %d, allow headers %q", w.Code, w.Header().Get("Access-Control-Allow-Headers"))
	}
}
