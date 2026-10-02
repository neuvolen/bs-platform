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

func TestParseHealthAI(t *testing.T) {
	p, err := parseHealthAI("```json\n{\"scores\":{\"brain\":7.4,\"heart\":\"5\",\"hands\":12,\"blood\":-3,\"liver\":9},\"why\":{\"blood\":\"кассовые разрывы — нет календаря\",\"brain\":[\"нет фокуса\",\"\",\"навязанные цели\",\"лишнее\"]}}\n```")
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]int{"brain": 7, "heart": 5, "hands": 10, "blood": 0}
	if len(p.Scores) != len(want) {
		t.Fatalf("scores: %v", p.Scores)
	}
	for k, v := range want {
		if p.Scores[k] != v {
			t.Fatalf("%s = %d, want %d (%v)", k, p.Scores[k], v, p.Scores)
		}
	}
	if p.Why["blood"][0] != "кассовые разрывы - нет календаря" || len(p.Why["brain"]) != 2 || p.Why["brain"][1] != "навязанные цели" {
		t.Fatalf("why: %v", p.Why)
	}
	for _, bad := range []string{"не знаю", `{"scores":{}}`, `{"scores":{"liver":5}}`} {
		if _, err := parseHealthAI(bad); err == nil {
			t.Fatalf("parsed garbage %q", bad)
		}
	}
	pr := healthPrompt(healthReq{Resident: "Альтаир", Diagnoses: []healthItem{{Title: "Кассовые разрывы", Organ: "Финансы"}}})
	for _, w := range []string{"brain: Стратегия", "eyes: Аналитика", "Кассовые разрывы (Финансы)", "Альтаир", "от 0 до 10"} {
		if !strings.Contains(pr, w) {
			t.Fatalf("prompt lacks %q", w)
		}
	}
}

func TestPlatformHealthEndpoint(t *testing.T) {
	answer := `{"scores":{"brain":6,"heart":5,"hands":4,"spine":6,"blood":3,"dna":5,"eyes":7},"why":{"blood":"кассовые разрывы"}}`
	status := 200
	var prompt string
	gm := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		prompt = string(b)
		if status != 200 {
			w.WriteHeader(status)
			_, _ = w.Write([]byte(`{"error":{"message":"boom"}}`))
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"candidates": []any{map[string]any{
			"content": map[string]any{"parts": []any{map[string]any{"text": answer}}}}}})
	}))
	defer gm.Close()
	gin.SetMode(gin.TestMode)
	mk := func(c *ai.Client, role string) *gin.Engine {
		h := NewPlatformAI(nil, c)
		r := gin.New()
		r.Use(func(c *gin.Context) { c.Set("role", role); c.Set("userID", "tg:2") })
		r.POST("/ai/health", h.Health)
		return r
	}
	call := func(r *gin.Engine, body string) (int, map[string]any) {
		w := httptest.NewRecorder()
		req := httptest.NewRequest("POST", "/ai/health", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		r.ServeHTTP(w, req)
		var j map[string]any
		_ = json.Unmarshal(w.Body.Bytes(), &j)
		return w.Code, j
	}
	gem := &ai.Client{Gemini: "k", GeminiModel: "m", GeminiBase: gm.URL, HTTP: gm.Client()}
	body := `{"board":"b1","resident":"Альтаир","diagnoses":[{"title":"Кассовые разрывы","organ":"Финансы"}],"tools":[{"title":"Платёжный календарь","organ":"Финансы"}],"tasks":{"total":3,"done":1},"tests":{"Финансы":4},"local":{"blood":4}}`
	for _, role := range []string{"admin", "resident"} {
		code, j := call(mk(gem, role), body)
		if code != 200 {
			t.Fatalf("%s: %d %v", role, code, j)
		}
		sc, _ := j["scores"].(map[string]any)
		why, _ := j["why"].(map[string]any)
		if len(sc) != 7 || sc["blood"] != float64(3) || sc["eyes"] != float64(7) || why["blood"].([]any)[0] != "кассовые разрывы" {
			t.Fatalf("%s: answer %v", role, j)
		}
	}
	if !strings.Contains(prompt, "Кассовые разрывы (Финансы)") || !strings.Contains(prompt, "закрыто 1 из 3") || !strings.Contains(prompt, `"responseMimeType":"application/json"`) {
		t.Fatalf("prompt: %.400s", prompt)
	}
	r := mk(gem, "admin")
	if code, j := call(r, `{"diagnoses":[]}`); code != 400 || j["code"] != "empty" || j["error"] == "" {
		t.Fatalf("empty: %d %v", code, j)
	}
	if code, j := call(r, `not json`); code != 400 || j["code"] != "bad_request" {
		t.Fatalf("bad json: %d %v", code, j)
	}
	if code, j := call(mk(&ai.Client{}, "admin"), body); code != 503 || j["code"] != "no_ai" {
		t.Fatalf("no key: %d %v", code, j)
	}
	answer = "не знаю"
	if code, j := call(r, body); code != 502 || j["code"] != "ai_parse" {
		t.Fatalf("garbage: %d %v", code, j)
	}
	status = 500
	if code, j := call(r, body); code != 502 || j["code"] != "ai_failed" {
		t.Fatalf("gemini down: %d %v", code, j)
	}
}
