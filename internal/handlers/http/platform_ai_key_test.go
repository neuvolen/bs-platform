package http

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/bnursik/business_surgery_backend/internal/ai"
	"github.com/gin-gonic/gin"
)

// R34a: «Ключ Claude» in the settings: admin only, sealed in the database,
// only the last four characters go back, «Проверить» calls Claude.
func TestR34aClaudeKeySettings(t *testing.T) {
	repo, ctx := testPlatformDB(t, aiKeyDoc)
	const key = "sk-ant-api03-TESTKEY-0123456789-WXYZ"
	cl := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.ReadAll(r.Body)
		if r.Header.Get("x-api-key") != key {
			w.WriteHeader(401)
			_, _ = w.Write([]byte(`{"type":"error","error":{"type":"authentication_error","message":"invalid x-api-key"}}`))
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"stop_reason": "end_turn", "content": []any{map[string]any{"type": "text", "text": "ок"}}})
	}))
	defer cl.Close()
	t.Setenv("AI_KEYS_SECRET", "")
	gin.SetMode(gin.TestMode)
	mk := func() (*PlatformAI, *ai.Client) {
		c := &ai.Client{AnthropicBase: cl.URL, ClaudeModel: "claude-sonnet-5", HTTP: cl.Client(), Keys: &ai.KeyBox{}}
		h := NewPlatformAI(repo, c)
		h.KeySecret = []byte("jwt-secret-for-tests-0123456789")
		return h, c
	}
	h, c := mk()
	router := func(h *PlatformAI, role string) *gin.Engine {
		r := gin.New()
		r.Use(func(c *gin.Context) { c.Set("role", role); c.Set("userID", "tg:1") })
		r.GET("/ai/key", h.AIKey)
		r.PUT("/ai/key", h.PutAIKey)
		r.DELETE("/ai/key", h.DeleteAIKey)
		r.POST("/ai/key/test", h.TestAIKey)
		r.GET("/ai/status", h.Status)
		return r
	}
	call := func(r *gin.Engine, method, path, body string) (int, map[string]any, string) {
		w := httptest.NewRecorder()
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		r.ServeHTTP(w, req)
		var j map[string]any
		_ = json.Unmarshal(w.Body.Bytes(), &j)
		return w.Code, j, w.Body.String()
	}
	adm := router(h, "admin")
	for _, role := range []string{"resident", "lead", ""} {
		for _, m := range [][2]string{{"GET", "/ai/key"}, {"PUT", "/ai/key"}, {"DELETE", "/ai/key"}, {"POST", "/ai/key/test"}} {
			if code, _, _ := call(router(h, role), m[0], m[1], `{"key":"`+key+`"}`); code != http.StatusForbidden {
				t.Fatalf("%s %s by %q: %d", m[0], m[1], role, code)
			}
		}
	}
	if c.HasText() {
		t.Fatal("a key before saving")
	}
	if _, j, _ := call(adm, "GET", "/ai/key", ""); j["source"] != "" || j["last4"] != nil {
		t.Fatalf("empty: %+v", j)
	}
	// a bad shape is refused, nothing saved
	if code, j, _ := call(adm, "PUT", "/ai/key", `{"key":"привет"}`); code != 400 || j["error"] != "bad_key" {
		t.Fatalf("bad shape: %d %+v", code, j)
	}
	// «Проверить» a pasted key before saving it
	if _, j, _ := call(adm, "POST", "/ai/key/test", `{"key":"sk-ant-api03-WRONG-000000000000"}`); j["ok"] != false || j["message"] != ai.KeyRejected {
		t.Fatalf("wrong key test: %+v", j)
	}
	if _, j, _ := call(adm, "POST", "/ai/key/test", `{"key":"`+key+`"}`); j["ok"] != true || !strings.Contains(j["message"].(string), "claude-sonnet-5") {
		t.Fatalf("test: %+v", j)
	}
	code, j, raw := call(adm, "PUT", "/ai/key", `{"key":"  `+key+`  "}`)
	if code != 200 || j["source"] != "settings" || j["last4"] != "…WXYZ" || strings.Contains(raw, "TESTKEY") {
		t.Fatalf("save: %d %s", code, raw)
	}
	if !c.HasText() || c.KeySource() != "settings" {
		t.Fatal("not used at once")
	}
	// sealed at rest: the database has no trace of the key
	d, err := repo.GetDoc(ctx, "server", aiKeyDoc)
	if err != nil || d == nil || !strings.HasPrefix(d.Value, "v1:") || strings.Contains(d.Value, "TESTKEY") || strings.Contains(d.Value, "sk-ant") {
		t.Fatalf("at rest: %+v %v", d, err)
	}
	// GET and the status never carry it
	for _, p := range []string{"/ai/key", "/ai/status"} {
		if _, _, raw := call(adm, "GET", p, ""); strings.Contains(raw, "TESTKEY") || strings.Contains(raw, key) {
			t.Fatalf("%s leaks the key: %s", p, raw)
		}
	}
	if _, j, _ := call(adm, "POST", "/ai/key/test", `{}`); j["ok"] != true {
		t.Fatalf("test saved key: %+v", j)
	}
	if ans, err := c.Text(ctx, "s", "p"); err != nil || ans != "ок" {
		t.Fatalf("text with the saved key: %q %v", ans, err)
	}
	// after a restart the key is read back from the database
	h2, c2 := mk()
	h2.LoadAIKey(ctx)
	if c2.KeySource() != "settings" || !c2.HasText() {
		t.Fatal("not loaded after a restart")
	}
	// another secret cannot open it (and does not crash)
	h3, c3 := mk()
	h3.KeySecret = []byte("another-secret")
	h3.LoadAIKey(ctx)
	if c3.HasText() {
		t.Fatal("opened with another secret")
	}
	// the env key wins and is shown masked
	c.Anthropic = "sk-ant-env-key-0000000000-ENVK"
	if _, j, raw := call(adm, "GET", "/ai/key", ""); j["source"] != "env" || j["last4"] != "…ENVK" || j["saved"] != true || strings.Contains(raw, "env-key") {
		t.Fatalf("env: %s", raw)
	}
	c.Anthropic = ""
	// delete
	if code, j, _ := call(adm, "DELETE", "/ai/key", ""); code != 200 || j["source"] != "" || c.HasText() {
		t.Fatalf("delete: %d %+v", code, j)
	}
	if d, _ := repo.GetDoc(ctx, "server", aiKeyDoc); d == nil || !d.Deleted {
		t.Fatalf("deleted at rest: %+v", d)
	}
	h4, c4 := mk()
	h4.LoadAIKey(ctx)
	if c4.HasText() {
		t.Fatal("a deleted key came back")
	}
}
