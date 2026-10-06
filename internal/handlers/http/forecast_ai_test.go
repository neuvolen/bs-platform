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

// The same cases as «проверка параметров» in r48/fc_test.js (BSFC.validate).
func TestValidateForecastItems(t *testing.T) {
	var in []any
	_ = json.Unmarshal([]byte(`[{"t":"res","n":0,"price":500000},{"t":"res","n":3,"price":5e9},{"t":"tax","rate":80},
		{"t":"hire","salary":400000,"title":"<b>Менеджер</b>"},{"t":"zzz"},{"t":"horizon","months":7},{"t":"price","pct":0},
		{"t":"once","amount":1e6,"m":30},null]`), &in)
	out, dropped := validateForecastItems(in)
	if len(out) != 2 || dropped != 7 {
		t.Fatalf("out %v dropped %d", out, dropped)
	}
	if out[0]["title"] != "b Менеджер /b" || out[0]["from"] != 1.0 || out[1]["m"] != 1.0 || out[1]["title"] != "Разовый расход" {
		t.Fatalf("items %v", out)
	}
	many := make([]any, 25)
	for i := range many {
		many[i] = map[string]any{"t": "tax", "rate": 3.0}
	}
	if o, d := validateForecastItems(many); len(o) != 20 || d != 5 {
		t.Fatalf("cap: %d %d", len(o), d)
	}
	// Rounding and defaults as on the page
	var in2 []any
	_ = json.Unmarshal([]byte(`[{"t":"res","n":"3","price":500000.4,"every":3},{"t":"kaspi","rate":0.954},{"t":"churn","pct":"2.5"},
		{"t":"exp","pct":-15,"cat":""},{"t":"div"},{"t":"goal","amount":5000000},{"t":"res","n":true,"price":1000},{"t":"horizon","months":"24"}]`), &in2)
	o2, d2 := validateForecastItems(in2)
	want := `[{"every":3,"from":1,"n":3,"price":500000,"t":"res","term":1},{"rate":0.95,"t":"kaspi"},{"pct":2.5,"t":"churn"},{"cat":"*","from":1,"pct":-15,"t":"exp"},{"amount":5000000,"t":"goal"},{"months":24,"t":"horizon"}]`
	if b, _ := json.Marshal(o2); string(b) != want || d2 != 2 {
		t.Fatalf("got %s dropped %d", b, d2)
	}
}

func TestParseForecastAI(t *testing.T) {
	items, rest, dropped, err := parseForecastAI("```json\n{\"items\":[{\"t\":\"res\",\"n\":3,\"every\":3,\"price\":500000},{\"t\":\"tax\",\"rate\":3},{\"t\":\"hire\",\"title\":\"Менеджер\",\"salary\":400000,\"from\":3},{\"t\":\"bad\"}],\"rest\":[\"что-то\"]}\n```")
	if err != nil || len(items) != 3 || dropped != 1 || len(rest) != 1 || rest[0] != "что-то" {
		t.Fatalf("%v %v %d %v", items, rest, dropped, err)
	}
	for _, bad := range []string{"не знаю", `{"rest":[]}`} {
		if _, _, _, err := parseForecastAI(bad); err == nil {
			t.Fatalf("parsed garbage %q", bad)
		}
	}
	p := forecastPrompt(forecastReq{Text: "+3 резидента", Start: "2026-11", ExpLines: []string{"Офис"}})
	for _, w := range []string{"2026-11", "Офис", "«+3 резидента»", `"t":"res"`} {
		if !strings.Contains(p, w) {
			t.Fatalf("prompt lacks %q", w)
		}
	}
}

func TestPlatformForecastEndpoint(t *testing.T) {
	answer := `{"items":[{"t":"res","n":3,"every":3,"price":500000,"term":1,"from":1},{"t":"tax","rate":3},{"t":"hire","title":"Менеджер","salary":400000,"from":3},{"t":"res","n":500,"price":1}],"rest":[]}`
	var prompt string
	gm := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		prompt = string(b)
		_ = json.NewEncoder(w).Encode(map[string]any{"candidates": []any{map[string]any{
			"content": map[string]any{"parts": []any{map[string]any{"text": answer}}}}}})
	}))
	defer gm.Close()
	gin.SetMode(gin.TestMode)
	mk := func(c *ai.Client, role string) *gin.Engine {
		h := NewPlatformAI(nil, c)
		r := gin.New()
		r.Use(func(c *gin.Context) { c.Set("role", role); c.Set("userID", "tg:2") })
		r.POST("/ai/forecast", h.Forecast)
		return r
	}
	call := func(r *gin.Engine, body string) (int, map[string]any) {
		w := httptest.NewRecorder()
		req := httptest.NewRequest("POST", "/ai/forecast", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		r.ServeHTTP(w, req)
		var j map[string]any
		_ = json.Unmarshal(w.Body.Bytes(), &j)
		return w.Code, j
	}
	gem := &ai.Client{Gemini: "k", GeminiModel: "m", GeminiBase: gm.URL, HTTP: gm.Client()}
	body := `{"text":"+3 резидента по 500 000 каждые 3 месяца, налог 3%, с января найм менеджера 400 000","start":"2026-11","expLines":["Офис","SMM"],"horizon":12}`
	code, j := call(mk(gem, "admin"), body)
	if code != 200 {
		t.Fatalf("admin: %d %v", code, j)
	}
	items, _ := j["items"].([]any)
	if len(items) != 3 || j["dropped"] != 1.0 {
		t.Fatalf("items: %v", j)
	}
	if first, _ := items[0].(map[string]any); first["price"] != 500000.0 || first["every"] != 3.0 {
		t.Fatalf("first: %v", items[0])
	}
	if !strings.Contains(prompt, "2026-11") || !strings.Contains(prompt, "найм менеджера") {
		t.Fatalf("prompt: %.300s", prompt)
	}
	for _, role := range []string{"resident", "lead"} {
		if code, _ := call(mk(gem, role), body); code != http.StatusForbidden {
			t.Fatalf("%s: %d", role, code)
		}
	}
	if code, _ := call(mk(gem, "admin"), `{"text":"  "}`); code != http.StatusBadRequest {
		t.Fatalf("empty: %d", code)
	}
	if code, j := call(mk(&ai.Client{}, "admin"), body); code != http.StatusServiceUnavailable || j["code"] != "no_ai" {
		t.Fatalf("no ai: %d %v", code, j)
	}
}
