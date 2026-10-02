package http

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/bnursik/business_surgery_backend/internal/ai"
	"github.com/gin-gonic/gin"
)

// gallupOrder: a full profile, ranks 1..34.
var gallupOrder = []string{"strategic", "focus", "achiever", "analytical", "context", "learner", "responsibility", "arranger", "futuristic", "self-assurance",
	"maximizer", "ideation", "input", "command", "relator", "individualization", "activator", "intellection", "discipline", "significance",
	"deliberative", "competition", "belief", "communication", "consistency", "restorative", "developer", "connectedness", "woo", "positivity",
	"empathy", "adaptability", "includer", "harmony"}

func gallupAnswer(order []string) string {
	var ts []string
	for i, k := range order {
		th := gallupTheme(k)
		name := th[1]
		switch i % 4 { // the model mixes spellings: English, Russian, key
		case 1:
			name = th[2]
		case 2:
			name = strings.ToUpper(th[0])
		}
		ts = append(ts, fmt.Sprintf(`{"rank":%d,"name":%q,"domain":"x","essence":"Суть %s — коротко","business":"В бизнесе %s","blind":"Риск %s","manage":"Управление %s"}`, i+1, name, k, k, k, k))
	}
	// a duplicate and an unknown theme are dropped
	ts = append(ts, `{"rank":35,"name":"Strategic","essence":"dup"}`, `{"rank":36,"name":"Telepathy","essence":"?"}`)
	return "```json\n{\"talents\":[" + strings.Join(ts, ",") + "],\"summary\":{\"headline\":\"Стратег — исполнитель\",\"business\":[\"Пункт 1\",\"\",\"Пункт 2\"],\"roles\":[\"Архитектор стратегии\"],\"partners\":[\"Человек с Woo для продаж\"],\"avoid\":[\"Холодные звонки\"]}}\n```"
}

func TestParseGallupAI(t *testing.T) {
	p, err := parseGallupAI(gallupAnswer(gallupOrder))
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Talents) != 34 || !p.Complete {
		t.Fatalf("talents: %d complete=%v", len(p.Talents), p.Complete)
	}
	doms := map[string]int{}
	for i, tl := range p.Talents {
		if tl.Key != gallupOrder[i] || tl.Rank != i+1 {
			t.Fatalf("order at %d: %+v", i, tl)
		}
		th := gallupTheme(tl.Key)
		if tl.Name != th[1] || tl.Ru != th[2] || tl.Domain != th[3] || tl.DomainRu != gallupDomainRu[th[3]] || tl.Manage == "" {
			t.Fatalf("talent %d: %+v", i, tl)
		}
		if strings.Contains(tl.Essence, "—") {
			t.Fatalf("long dash kept: %q", tl.Essence)
		}
		doms[tl.Domain]++
	}
	if doms["executing"] != 9 || doms["influencing"] != 8 || doms["relationship"] != 9 || doms["strategic"] != 8 {
		t.Fatalf("domains: %v", doms)
	}
	if p.Summary.Headline != "Стратег - исполнитель" || len(p.Summary.Business) != 2 || len(p.Summary.Partners) != 1 {
		t.Fatalf("summary: %+v", p.Summary)
	}
	// Russian spellings of other translations
	for in, want := range map[string]string{"Самоуверенность": "self-assurance", "Генератор идей": "ideation", "Включённость": "includer", "SELF ASSURANCE": "self-assurance", "Развитие других": "developer", "Стратегия": "strategic"} {
		if got := gallupKeyOf(in); got != want {
			t.Fatalf("%s → %s, want %s", in, got, want)
		}
	}
	// A top-10 report: what is there, not complete
	p, err = parseGallupAI(gallupAnswer(gallupOrder[:10]))
	if err != nil || len(p.Talents) != 10 || p.Complete {
		t.Fatalf("partial: %v %+v", err, p)
	}
	if _, err := parseGallupAI("не знаю"); err == nil {
		t.Fatal("garbage parsed")
	}
	// The prompt lists all 34 themes with domains and asks for every field
	pr := gallupPrompt("REPORT")
	for _, th := range gallupThemes {
		if !strings.Contains(pr, th[1]+" ("+th[2]+", "+th[3]+")") {
			t.Fatalf("prompt lacks %s", th[1])
		}
	}
	for _, w := range []string{"ВСЕХ 34", "essence", "business", "blind", "manage", "partners", "Построение отношений", "REPORT"} {
		if !strings.Contains(pr, w) {
			t.Fatalf("prompt lacks %q", w)
		}
	}
}

func TestPlatformGallupEndpoint(t *testing.T) {
	repo, db := platformTestRepo(t)
	ctx := context.Background()
	_, _ = db.Pool.Exec(ctx, `DELETE FROM platform_files WHERE id LIKE 'gal\_%'`)
	calls := 0
	var prompt string
	gm := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		calls++
		prompt = string(b)
		_ = json.NewEncoder(w).Encode(map[string]any{"candidates": []any{map[string]any{
			"content": map[string]any{"parts": []any{map[string]any{"text": gallupAnswer(gallupOrder)}}}}}})
	}))
	defer gm.Close()
	h := NewPlatformAI(repo, &ai.Client{Gemini: "k", GeminiModel: "m", GeminiBase: gm.URL, HTTP: gm.Client()})
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(func(c *gin.Context) { c.Set("role", "resident"); c.Set("userID", "tg:2") })
	r.POST("/ai/gallup", h.Gallup)
	call := func(body string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		req := httptest.NewRequest("POST", "/ai/gallup", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		r.ServeHTTP(w, req)
		return w
	}
	var names []string
	for i, k := range gallupOrder {
		names = append(names, fmt.Sprintf("%d. %s", i+1, gallupTheme(k)[1]))
	}
	report, _ := json.Marshal(map[string]string{"text": "CliftonStrengths 34 Рустам\n" + strings.Join(names, "\n")})
	w := call(string(report))
	if w.Code != 200 {
		t.Fatalf("gallup: %d %s", w.Code, w.Body.String())
	}
	var p gallupProfile
	_ = json.Unmarshal(w.Body.Bytes(), &p)
	if len(p.Talents) != 34 || !p.Complete || p.Talents[0].Key != "strategic" || p.Talents[33].Key != "harmony" || p.Talents[9].DomainRu != "Влияние" {
		t.Fatalf("profile: %d %+v", len(p.Talents), p.Talents[:1])
	}
	if !strings.Contains(prompt, "34. Harmony") || !strings.Contains(prompt, `"responseMimeType":"application/json"`) {
		t.Fatalf("prompt: %.300s", prompt)
	}
	// the same report again: from storage
	w = call(string(report))
	if w.Code != 200 || w.Header().Get("X-Gallup-Cache") != "hit" || calls != 1 {
		t.Fatalf("cache: %d %s calls=%d", w.Code, w.Header().Get("X-Gallup-Cache"), calls)
	}
	if w = call(`{"text":"мало"}`); w.Code != 400 {
		t.Fatalf("short: %d", w.Code)
	}
}
