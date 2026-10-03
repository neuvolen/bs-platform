package http

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
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

// gallupDeepFixture: answers to the real R29 prompts for gallupR29Order (a
// dry run of the schema: what a model writes for that profile).
func gallupDeepFixture(t *testing.T, part string) string {
	b, err := os.ReadFile("testdata/gallup_deep_" + part + ".json")
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// A profile with strong Competition and Command and weak Self-Assurance.
var gallupR29Order = []string{"achiever", "competition", "command", "strategic", "focus", "activator", "analytical", "responsibility", "maximizer", "futuristic",
	"arranger", "significance", "learner", "ideation", "discipline", "communication", "input", "context", "belief", "relator",
	"individualization", "deliberative", "intellection", "restorative", "woo", "consistency", "developer", "positivity", "adaptability", "self-assurance",
	"connectedness", "includer", "empathy", "harmony"}

func TestParseGallupDeep(t *testing.T) {
	o := gallupR29Order
	a, err := parseGallupDeepA(gallupDeepFixture(t, "a"), o)
	if err != nil {
		t.Fatal(err)
	}
	if a.Portrait.Headline == "" || len(a.Portrait.Top5) != 5 || a.Portrait.Top5[0].Key != "achiever" || a.Portrait.Top10 == "" {
		t.Fatalf("portrait: %+v", a.Portrait)
	}
	if len(a.Amplify) != 3 || len(a.Amplify[1].Keys) != 3 || len(a.Conflict) != 2 || len(a.Anchors) != 3 {
		t.Fatalf("links: %d %d %d", len(a.Amplify), len(a.Conflict), len(a.Anchors))
	}
	if an := a.Anchors[0]; an.Weak != "self-assurance" || strings.Join(an.Strong, ",") != "competition,command" || !strings.Contains(an.Scene, "дистрибьютором") {
		t.Fatalf("anchor: %+v", an)
	}
	if !strings.Contains(a.Amplify[2].Scene, "4 500 000 ₸") || len(a.Best) != 4 || len(a.Stress) != 4 || len(a.Reset) != 3 || len(a.Blind) != 3 {
		t.Fatalf("a: %+v", a)
	}
	b, err := parseGallupDeepB(gallupDeepFixture(t, "b"), o)
	if err != nil {
		t.Fatal(err)
	}
	if len(b.Areas) != 5 || b.Areas[0].Area != "sales" || b.Areas[4].Area != "money" || len(b.Risks) != 4 || len(b.Plan) != 5 || len(b.Hires) != 3 || len(b.WorkWith) != 5 {
		t.Fatalf("b: %d %d %d %d %d", len(b.Areas), len(b.Risks), len(b.Plan), len(b.Hires), len(b.WorkWith))
	}
	if strings.Join(b.Hires[2].Talents, ",") != "self-assurance,woo,communication" {
		t.Fatalf("hire: %+v", b.Hires[2])
	}
	all, _ := json.Marshal([]any{a, b})
	if strings.Contains(string(all), "—") || strings.Contains(string(all), "–") {
		t.Fatal("long dash in the answer")
	}

	// Brand rules and the rank checks: a dash and a long number are fixed; a
	// «weak» talent from the top, a strong one from the bottom, a conflict
	// with the bottom and a scene of three words are dropped.
	raw := `{"portrait":{"headline":"Лидер — исполнитель","plain":"` + strings.Repeat("Вы решаете быстро и доводите до конца. ", 3) + `","top5":[{"key":"Achiever","line":"x"},{"key":"Конкуренция","line":"y"},{"key":"command","line":"z"},{"key":"harmony","line":"low"}],"top10":"t"},
	"amplify":[{"keys":["achiever","Сосредоточенность"],"title":"A","effect":"e","scene":"Кредит в банке на 25000000 тенге вы закрываете досрочно и сразу берёте новый на склад."},{"keys":["achiever","harmony"],"title":"B","effect":"e","scene":"` + strings.Repeat("с", 60) + `"},{"keys":["command","focus"],"title":"C","effect":"e","scene":"коротко"},{"keys":["strategic","focus"],"title":"D","effect":"e","scene":"` + strings.Repeat("д", 60) + `"}],
	"conflict":[{"keys":["command","empathy"],"title":"x","effect":"e","scene":"` + strings.Repeat("к", 60) + `"},{"keys":["command","analytical"],"title":"y","effect":"e","scene":"` + strings.Repeat("к", 60) + `","fix":"f"}],
	"anchors":[{"weak":"achiever","strong":["command"],"title":"x","effect":"e","scene":"` + strings.Repeat("я", 60) + `","fix":"f"},{"weak":"Уверенность","strong":["competition","harmony"],"title":"y","effect":"e","scene":"` + strings.Repeat("я", 60) + `","fix":"f"},{"weak":"empathy","strong":["achiever"],"title":"z","effect":"e","scene":"` + strings.Repeat("я", 60) + `","fix":"f"}],
	"best":["a","b"],"stress":["c","d"],"blind":[{"title":"t","text":"x"},{"title":"t2","text":"x2"}]}`
	d, err := parseGallupDeepA(raw, o)
	if err != nil {
		t.Fatal(err)
	}
	if d.Portrait.Headline != "Лидер - исполнитель" || len(d.Portrait.Top5) != 3 || d.Portrait.Top5[1].Key != "competition" {
		t.Fatalf("portrait: %+v", d.Portrait)
	}
	if len(d.Amplify) != 2 || d.Amplify[0].Keys[1] != "focus" || !strings.Contains(d.Amplify[0].Scene, "25 000 000 тенге") || d.Amplify[1].Title != "D" {
		t.Fatalf("amplify: %+v", d.Amplify)
	}
	if len(d.Conflict) != 1 || d.Conflict[0].Title != "y" || len(d.Anchors) != 2 || d.Anchors[0].Weak != "self-assurance" || len(d.Anchors[0].Strong) != 1 {
		t.Fatalf("conflict/anchors: %+v %+v", d.Conflict, d.Anchors)
	}
	// too little left: an error that names what to fix, and the retry prompt carries it
	raw = `{"portrait":{"headline":"h","plain":"коротко"},"amplify":[],"best":["a"],"stress":[]}`
	if _, err := parseGallupDeepA(raw, o); err == nil || !strings.Contains(err.Error(), "anchors") || !strings.Contains(err.Error(), "amplify") {
		t.Fatalf("thin answer: %v", err)
	}
	if _, err := parseGallupDeepB(`{"areas":[{"area":"sales","how":"h","action":"a"}]}`, o); err == nil || !strings.Contains(err.Error(), "areas") {
		t.Fatalf("thin b: %v", err)
	}
	if _, err := parseGallupDeepA("не JSON", o); err == nil {
		t.Fatal("garbage parsed")
	}
	// a top-10 report: no anchors are asked for
	if _, err := parseGallupDeepA(strings.Replace(gallupDeepFixture(t, "a"), `"anchors":[`, `"anchorsX":[`, 1), o[:10]); err != nil {
		t.Fatalf("top-10: %v", err)
	}
	pa, pb := gallupDeepPromptA(o), gallupDeepPromptB(o)
	for _, w := range []string{"30. Self-Assurance / Уверенность / ключ self-assurance", "anchors", "прогибается и молчит", "Казахстане", "10 000 000 ₸", "длинного тире", "гороскоп"} {
		if !strings.Contains(pa, w) {
			t.Fatalf("prompt A lacks %q", w)
		}
	}
	for _, w := range []string{"sales, negotiate, team, decisions, money", "risks", "trigger", "signs", "rule", "plan", "workWithMe", "hires"} {
		if !strings.Contains(pb, w) {
			t.Fatalf("prompt B lacks %q", w)
		}
	}
}

func TestGallupScanOrder(t *testing.T) {
	var lines []string
	for i, k := range gallupR29Order {
		lines = append(lines, fmt.Sprintf("%d. %s", i+1, gallupTheme(k)[1]))
	}
	got := gallupScanOrder("CliftonStrengths 34 Results\nYour talent themes:\n" + strings.Join(lines, "\n") + "\nWhat is Achiever? ...")
	if !sameOrder(got, gallupR29Order) {
		t.Fatalf("en: %v", got)
	}
	got = gallupScanOrder("Ваши таланты: 1. Стратегия 2. Сосредоточенность 3. Достигатор 4. Самоуверенность 5. Генератор идей")
	if strings.Join(got, ",") != "strategic,focus,achiever,self-assurance,ideation" {
		t.Fatalf("ru: %v", got)
	}
}

func TestPlatformGallupEndpoint(t *testing.T) {
	repo, db := platformTestRepo(t)
	ctx := context.Background()
	_, _ = db.Pool.Exec(ctx, `DELETE FROM platform_files WHERE id LIKE 'gal%'`)
	var mu sync.Mutex
	calls := map[string]int{}
	var prompt string
	failA := 1 // the first answer to part A is not JSON: the call is asked again
	gm := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		body := string(b)
		ans := gallupAnswer(gallupR29Order)
		mu.Lock()
		switch {
		case strings.Contains(body, "Сделай первую часть разбора"):
			calls["a"]++
			ans = gallupDeepFixture(t, "a")
			if failA > 0 {
				failA--
				ans = "Извините, вот разбор: ..."
			}
		case strings.Contains(body, "Сделай вторую часть разбора"):
			calls["b"]++
			ans = gallupDeepFixture(t, "b")
		default:
			calls["t"]++
			prompt = body
		}
		mu.Unlock()
		_ = json.NewEncoder(w).Encode(map[string]any{"candidates": []any{map[string]any{
			"content": map[string]any{"parts": []any{map[string]any{"text": ans}}}}}})
	}))
	defer gm.Close()
	h := NewPlatformAI(repo, &ai.Client{Gemini: "k", GeminiModel: "m", GeminiBase: gm.URL, HTTP: gm.Client()})
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(func(c *gin.Context) { c.Set("role", "resident"); c.Set("userID", "tg:2") })
	r.POST("/ai/gallup", h.Gallup)
	r.POST("/gallup/pdf", GallupPDF)
	call := func(path, body string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		req := httptest.NewRequest("POST", path, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		r.ServeHTTP(w, req)
		return w
	}
	var names []string
	for i, k := range gallupR29Order {
		names = append(names, fmt.Sprintf("%d. %s", i+1, gallupTheme(k)[1]))
	}
	report, _ := json.Marshal(map[string]string{"text": "CliftonStrengths 34 Рустам\n" + strings.Join(names, "\n")})
	w := call("/ai/gallup", string(report))
	if w.Code != 200 {
		t.Fatalf("gallup: %d %s", w.Code, w.Body.String())
	}
	var p gallupProfile
	_ = json.Unmarshal(w.Body.Bytes(), &p)
	if len(p.Talents) != 34 || !p.Complete || p.Talents[0].Key != "achiever" || p.Talents[33].Key != "harmony" || p.Talents[29].DomainRu != "Влияние" {
		t.Fatalf("profile: %d %+v", len(p.Talents), p.Talents[:1])
	}
	if p.V != 2 || p.Deep == nil || p.Deep.Partial || len(p.Deep.Anchors) != 3 || len(p.Deep.Areas) != 5 || len(p.Deep.Plan) != 5 {
		t.Fatalf("deep: %+v", p.Deep)
	}
	// the order was in the text: talents and both parts ran at once (3 calls + 1 retry of A)
	if calls["t"] != 1 || calls["a"] != 2 || calls["b"] != 1 {
		t.Fatalf("calls: %v", calls)
	}
	if !strings.Contains(prompt, "34. Harmony") || !strings.Contains(prompt, `"responseMimeType":"application/json"`) {
		t.Fatalf("prompt: %.300s", prompt)
	}
	// the same report again: from storage
	w = call("/ai/gallup", string(report))
	if w.Code != 200 || w.Header().Get("X-Gallup-Cache") != "hit" || calls["t"] != 1 {
		t.Fatalf("cache: %d %s calls=%v", w.Code, w.Header().Get("X-Gallup-Cache"), calls)
	}
	if w = call("/ai/gallup", `{"text":"мало"}`); w.Code != 400 {
		t.Fatalf("short: %d", w.Code)
	}
	// A profile saved before R29: the deep part by its order only
	ord, _ := json.Marshal(map[string]any{"order": gallupR29Order})
	w = call("/ai/gallup", string(ord))
	var od struct {
		V     int         `json:"v"`
		Order []string    `json:"order"`
		Deep  *gallupDeep `json:"deep"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &od)
	if w.Code != 200 || od.V != 2 || od.Deep == nil || len(od.Deep.Anchors) != 3 || len(od.Order) != 34 || calls["t"] != 1 || calls["a"] != 3 {
		t.Fatalf("order: %d %s %v", w.Code, w.Body.String()[:min(200, w.Body.Len())], calls)
	}
	if w = call("/ai/gallup", string(ord)); w.Header().Get("X-Gallup-Cache") != "hit" || calls["a"] != 3 {
		t.Fatalf("order cache: %v", calls)
	}
	if w = call("/ai/gallup", `{"order":["achiever","nope"]}`); w.Code != 400 {
		t.Fatalf("short order: %d", w.Code)
	}
	// PDF of what the platform shows
	doc := map[string]any{"name": "Альтаир Сейткали", "date": "03.10.2026", "headline": p.Deep.Portrait.Headline, "plain": p.Deep.Portrait.Plain,
		"top5":    []map[string]any{{"rank": 1, "name": "Achiever", "ru": "Достигатор", "line": "x — y"}},
		"domains": []map[string]any{{"ru": "Исполнение", "score": 140, "top10": 4, "note": "доводите до результата"}},
		"map": map[string]any{"nodes": []map[string]any{{"ru": "Достигатор", "rank": 1, "x": 0.5, "y": 0.2, "r": 22}, {"ru": "Уверенность", "rank": 30, "x": 0.5, "y": 0.9, "r": 13, "kind": "anchor"}},
			"edges": []map[string]any{{"a": 0, "b": 1, "type": "anchor"}, {"a": 0, "b": 7, "type": "amp"}}, "line": "Тормоз"},
		"talents": []map[string]any{{"rank": 1, "name": "Achiever", "ru": "Достигатор", "domainRu": "Исполнение", "essence": "Суть"}}}
	db2, _ := json.Marshal(doc)
	w = call("/gallup/pdf", string(db2))
	if w.Code != 200 || w.Header().Get("Content-Type") != "application/pdf" || !strings.HasPrefix(w.Body.String(), "%PDF-") ||
		!strings.Contains(w.Header().Get("Content-Disposition"), "BS_Gallup_Altair_Sejtkali.pdf") {
		t.Fatalf("pdf: %d %s %s", w.Code, w.Header().Get("Content-Type"), w.Header().Get("Content-Disposition"))
	}
	if w = call("/gallup/pdf", `{"name":"x"}`); w.Code != 400 {
		t.Fatalf("empty pdf: %d", w.Code)
	}
}
