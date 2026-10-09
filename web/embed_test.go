package web

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/golang-jwt/jwt/v5"
)

func TestStripSeed(t *testing.T) {
	page := "<html><head></head><body><script>\n" +
		"var PL_ROWS = [{\"name\":\"Выручка\",\"vals\":[5000000]}];\n" +
		"var RESIDENTS = [{\"name\":\"Асет\",\"total\":710000}];\n" +
		"var FINES = [{\"res\":\"Асет\",\"amount\":10000}];\n" +
		"var SDATA = {\"nps\":[{\"res\":\"Мади\"}]};\n" +
		"var KEEP = [1,2,3];\n" +
		"</script></body></html>"
	html, seed := stripSeed(page)
	for _, leak := range []string{"Выручка", "710000", "10000", "Мади"} {
		if strings.Contains(html, leak) {
			t.Errorf("page still contains %q", leak)
		}
	}
	for _, want := range []string{"var PL_ROWS = [];", "var RESIDENTS = [];", "var FINES = [];", "var SDATA = {};", "var KEEP = [1,2,3];"} {
		if !strings.Contains(html, want) {
			t.Errorf("page lacks %q", want)
		}
	}
	var s map[string]json.RawMessage
	if err := json.Unmarshal([]byte(seed), &s); err != nil || len(s) != 4 {
		t.Fatalf("seed wrong: %v %s", err, seed)
	}
	if !strings.Contains(string(s["RESIDENTS"]), "710000") {
		t.Errorf("seed lost data: %s", seed)
	}
}

// The real embedded page must not carry club data once the server starts.
func TestEmbeddedPageHasNoClubData(t *testing.T) {
	var seed map[string]json.RawMessage
	if err := json.Unmarshal([]byte(Seed()), &seed); err != nil {
		t.Fatalf("seed: %v", err)
	}
	var fines []map[string]any
	_ = json.Unmarshal(seed["FINES"], &fines)
	var residents []map[string]any
	_ = json.Unmarshal(seed["RESIDENTS"], &residents)
	if len(residents) == 0 {
		t.Skip("page carries no resident data")
	}
	page := servedText() // R45: the shell and every file it loads
	for _, r := range residents {
		for _, f := range []string{"paid", "total", "debtRenew"} {
			if v, ok := r[f].(float64); ok && v >= 50000 {
				needle, _ := json.Marshal(map[string]any{f: v})
				if strings.Contains(page, strings.Trim(string(needle), "{}")) {
					t.Errorf("page still carries %s of %v", needle, r["name"])
				}
			}
		}
	}
	for _, v := range []string{`PL_ROWS\s*=\s*\[\]`, `RESIDENTS\s*=\s*\[\]`, `FINES\s*=\s*\[\]`, `SDATA\s*=\s*\{\}`} {
		if !regexp.MustCompile(v).MatchString(page) {
			t.Errorf("page lacks %q", v)
		}
	}
	if strings.Contains(string(loginPage.plain), "RESIDENTS") {
		t.Error("login page carries platform code")
	}
}

// Without a valid session "/" serves the club entry page with the logo and
// icons filled in; with one it serves the platform.
func TestRegisterServesEntryPage(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	Register(r, "secret", "bs_session")
	get := func(cookie string) string {
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		if cookie != "" {
			req.AddCookie(&http.Cookie{Name: "bs_session", Value: cookie})
		}
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		if w.Code != http.StatusOK {
			t.Fatalf("status %d", w.Code)
		}
		return w.Body.String()
	}
	for _, c := range []string{"", "garbage"} {
		page := get(c)
		for _, want := range []string{"Вход для резидентов и команды", "Ещё не резидент?", "https://t.me/bsurgery_bot?start=app_login", "https://wa.me/77024035036", `class="lg"`, `rel="apple-touch-icon"`, "/api/v1/platform", "/config", "/auth/telegram", `id="tgBtn"`, "oauth.telegram.org", "tgWebAppData", "tgAuthResult"} {
			if !strings.Contains(page, want) {
				t.Errorf("entry page (cookie %q) lacks %q", c, want)
			}
		}
		for _, bad := range []string{"<!--LOGO-->", "<!--ICONS-->", "—", "window.BS_SERVER"} {
			if strings.Contains(page, bad) {
				t.Errorf("entry page carries %q", bad)
			}
		}
	}
	if len(loginPage.plain) > 120000 {
		t.Errorf("entry page too large: %d bytes", len(loginPage.plain))
	}
	tok, _ := jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.MapClaims{"typ": "access"}).SignedString([]byte("secret"))
	if page := get(tok); !strings.Contains(page, "window.BS_SERVER=1") {
		t.Error("valid session does not get the platform")
	}
}
