package http

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/bnursik/business_surgery_backend/internal/ai"
	"github.com/gin-gonic/gin"
)

// The common text of every Gallup report: an intro and the domain legend where
// theme words stand in the same places for everyone (the R68 bug).
const gallupBoilerplate = "CliftonStrengths 34 Results. Congratulations! Your talents are your natural patterns. " +
	"Focus on what you do best. This report gives context and input for your belief in your potential: a developer of people, " +
	"with discipline and responsibility. Domains: Executing, Influencing, Relationship Building, Strategic Thinking. " +
	"Ваши таланты: развитие, ответственность, фокус, контекст. Стратегическое мышление, Построение отношений.\n"

func gallupReport(name string, order []string) string {
	var b strings.Builder
	b.WriteString(name + "\n" + gallupBoilerplate + "Your CliftonStrengths 34 order:\n")
	for i, k := range order {
		fmt.Fprintf(&b, "%d. %s\n", i+1, gallupTheme(k)[1])
	}
	b.WriteString("What is Achiever? People exceptionally talented in the Achiever theme work hard.")
	return b.String()
}

// gallupOrderB: another person, the reverse of gallupOrder.
func gallupOrderB() []string {
	out := make([]string, len(gallupOrder))
	for i, k := range gallupOrder {
		out[len(out)-1-i] = k
	}
	return out
}

func TestGallupRankedOrderPerPerson(t *testing.T) {
	a, b := gallupReport("Асет", gallupOrder), gallupReport("Мади", gallupOrderB())
	// the old reading (first mention) gives both people one and the same order
	if fa, fb := gallupFirstSeen(strings.ToLower(a)), gallupFirstSeen(strings.ToLower(b)); !sameOrder(gallupTop(fa, 5), gallupTop(fb, 5)) {
		t.Fatalf("fixture must reproduce the bug: %v vs %v", fa[:5], fb[:5])
	}
	oa, okA := gallupRankedOrder(a)
	ob, okB := gallupRankedOrder(b)
	if !okA || !okB || !sameOrder(oa, gallupOrder) || !sameOrder(ob, gallupOrderB()) {
		t.Fatalf("ranked: %v %v\n%v\n%v", okA, okB, oa, ob)
	}
	// Russian report, numbers after names, «1) …»
	ru := "Отчёт Gallup. Развитие и фокус важны. 1) Стратегия 2) Сосредоточенность 3) Достигатор 4) Самоуверенность 5) Генератор идей 6) Ученик"
	if o, ok := gallupRankedOrder(ru); !ok || strings.Join(o, ",") != "strategic,focus,achiever,self-assurance,ideation,learner" {
		t.Fatalf("ru: %v %v", ok, o)
	}
	if o, ok := gallupRankedOrder("Strategic 1\nLearner 2\nInput 3\nFocus 4\nAchiever 5\nRelator 6"); !ok || o[0] != "strategic" || o[5] != "relator" {
		t.Fatalf("after: %v %v", ok, o)
	}
	// the common text alone: not recognised, no default analysis
	if o, ok := gallupRankedOrder(gallupBoilerplate + strings.Repeat("Achiever Arranger Belief Focus Context Input. ", 30)); ok {
		t.Fatalf("boilerplate read as a ranking: %v", o)
	}
	// the theme legend in its own order is not a person
	var legend []string
	for i, th := range gallupThemes {
		legend = append(legend, fmt.Sprintf("%d %s", i+1, th[1]))
	}
	if _, ok := gallupRankedOrder(strings.Join(legend, " ")); ok {
		t.Fatal("canonical legend accepted")
	}
	// a short pasted list without numbers is the tracker's own input
	if o, ok := gallupRankedOrder("Стратегия, Ученик, Собиратель, Сосредоточенность, Достигатор, Отношения"); !ok || o[0] != "strategic" || o[5] != "relator" {
		t.Fatalf("short list: %v %v", ok, o)
	}
}

// Two residents upload different reports to the platform (resident cabinet
// and the board's «Замеры» use the same endpoint): each gets their own order,
// even when the model answers both with the same list.
func TestGallupTwoResidentsDifferentResults(t *testing.T) {
	repo, db := platformTestRepo(t)
	ctx := context.Background()
	_, _ = db.Pool.Exec(ctx, `DELETE FROM platform_files WHERE id LIKE 'gal%'`)
	var mu sync.Mutex
	texts := 0
	gm := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		body := string(raw)
		ans := gallupAnswer(gallupR29Order) // the same (wrong) list for everyone
		switch {
		case strings.Contains(body, "Сделай первую часть разбора"):
			ans = gallupDeepFixture(t, "a")
		case strings.Contains(body, "Сделай вторую часть разбора"):
			ans = gallupDeepFixture(t, "b")
		default:
			mu.Lock()
			texts++
			mu.Unlock()
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"candidates": []any{map[string]any{
			"content": map[string]any{"parts": []any{map[string]any{"text": ans}}}}}})
	}))
	defer gm.Close()
	h := NewPlatformAI(repo, &ai.Client{Gemini: "k", GeminiModel: "m", GeminiBase: gm.URL, HTTP: gm.Client()})
	gin.SetMode(gin.TestMode)
	post := func(uid, body string) *httptest.ResponseRecorder {
		r := gin.New()
		r.Use(func(c *gin.Context) { c.Set("role", "resident"); c.Set("userID", uid) })
		r.POST("/ai/gallup", h.Gallup)
		w := httptest.NewRecorder()
		req := httptest.NewRequest("POST", "/ai/gallup", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		r.ServeHTTP(w, req)
		return w
	}
	order := func(w *httptest.ResponseRecorder) []string {
		var p gallupProfile
		_ = json.Unmarshal(w.Body.Bytes(), &p)
		var o []string
		for _, t := range p.Talents {
			o = append(o, t.Key)
		}
		return o
	}
	ja, _ := json.Marshal(map[string]string{"text": gallupReport("Асет", gallupOrder)})
	jb, _ := json.Marshal(map[string]string{"text": gallupReport("Мади", gallupOrderB())})
	wa, wb := post("tg:11", string(ja)), post("tg:12", string(jb))
	if wa.Code != 200 || wb.Code != 200 {
		t.Fatalf("codes %d %d %s", wa.Code, wb.Code, wb.Body.String())
	}
	if oa, ob := order(wa), order(wb); !sameOrder(oa, gallupOrder) || !sameOrder(ob, gallupOrderB()) || sameOrder(oa, ob) {
		t.Fatalf("one result for both:\n%v\n%v", oa, ob)
	}
	// again, in the other order: each still gets their own
	if w := post("tg:12", string(jb)); !sameOrder(order(w), gallupOrderB()) {
		t.Fatalf("again B: %v", order(w))
	}
	if w := post("tg:11", string(ja)); !sameOrder(order(w), gallupOrder) {
		t.Fatalf("again A: %v", order(w))
	}
	if texts < 2 {
		t.Fatalf("model calls: %d", texts)
	}
	// no AI and no ranked list: the explicit message, never a default analysis
	h2 := NewPlatformAI(repo, &ai.Client{Gemini: "k", GeminiModel: "m", GeminiBase: "http://127.0.0.1:9", HTTP: &http.Client{}})
	r := gin.New()
	r.POST("/ai/gallup", h2.Gallup)
	w := httptest.NewRecorder()
	jc, _ := json.Marshal(map[string]string{"text": gallupBoilerplate + strings.Repeat(" Focus Context Input.", 20)})
	req := httptest.NewRequest("POST", "/ai/gallup", strings.NewReader(string(jc)))
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(w, req)
	if w.Code == 200 || !strings.Contains(w.Body.String(), "Отправьте PDF из Gallup") {
		t.Fatalf("unrecognised: %d %s", w.Code, w.Body.String())
	}
}

// The audit finds who got someone else's analysis; the team sends each the
// corrected one with one click; residents are never messaged by the audit.
func TestGallupAuditAndFixSend(t *testing.T) {
	repo, db := platformTestRepo(t)
	ctx := context.Background()
	for _, q := range []string{
		`DELETE FROM platform_boards WHERE id LIKE 'r68%'`,
		`DELETE FROM platform_docs WHERE key IN ('bs_gallup', 'bs_gallup_txt', 'bs_gallup_fix')`,
		`DELETE FROM platform_residents WHERE tg_id IN (680001, 680002, 680003)`,
		`INSERT INTO platform_residents (tg_id, name, active) VALUES (680001, 'Асет Тестов', true), (680002, 'Мади Тестов', true), (680003, 'Алия Тестова', true)`,
	} {
		if _, err := db.Pool.Exec(ctx, q); err != nil {
			t.Fatal(err)
		}
	}
	tmpl := gallupFirstSeen(strings.ToLower(gallupReport("x", gallupOrder))) // what the old parser gave everyone
	board := func(id, res string, g any) {
		d, _ := json.Marshal(map[string]any{"name": "Разбор · " + res, "info": map[string]string{"res": res}, "gallup": g})
		if _, err := repo.PutBoard(ctx, id, 0, d, "u1"); err != nil {
			t.Fatal(err)
		}
	}
	board("r68a", "Асет Тестов", map[string]any{"date": "2026-10-01T10:00:00Z", "themes": tmpl})
	board("r68b", "Мади Тестов", nil)
	board("r68c", "Алия Тестова", map[string]any{"date": "2026-10-01T10:00:00Z", "themes": gallupR29Order}) // own, fine
	// Мади uploaded in the cabinet: the profile and the text in his own scope
	gb, _ := json.Marshal(map[string]any{"r68b": map[string]any{"date": "2026-10-02T10:00:00Z", "themes": tmpl}})
	tb, _ := json.Marshal(map[string]string{"r68b": gallupReport("Мади", gallupOrderB())})
	for k, v := range map[string]string{"bs_gallup": string(gb), "bs_gallup_txt": string(tb)} {
		if _, err := repo.PutDoc(ctx, "user:tg:680002", k, 0, v, false, "tg:680002"); err != nil {
			t.Fatal(err)
		}
	}
	h := NewPlatformAI(repo, nil)
	type sent struct {
		chat int64
		name string
		data []byte
	}
	var out []sent
	h.SendDoc = func(_ context.Context, chat int64, name string, data []byte, _ string) error {
		out = append(out, sent{chat, name, data})
		return nil
	}
	doc, err := h.GallupAudit(ctx)
	if err != nil {
		t.Fatal(err)
	}
	a, b := doc.Items["r68a"], doc.Items["r68b"]
	if len(doc.Items) != 2 || a == nil || b == nil || doc.Items["r68c"] != nil {
		t.Fatalf("items: %+v", doc.Items)
	}
	if a.Status != "upload" || b.Status != "ready" || !sameOrder(b.Themes, gallupOrderB()) || a.Name != "Асет Тестов" {
		t.Fatalf("a=%+v b=%+v", a, b)
	}
	if len(out) != 0 {
		t.Fatal("the audit must not message residents")
	}
	gin.SetMode(gin.TestMode)
	send := func(role, body string) *httptest.ResponseRecorder {
		r := gin.New()
		r.Use(func(c *gin.Context) { c.Set("role", role); c.Set("userID", "u1") })
		r.POST("/gallup/fix/send", h.GallupFixSend)
		w := httptest.NewRecorder()
		req := httptest.NewRequest("POST", "/gallup/fix/send", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		r.ServeHTTP(w, req)
		return w
	}
	pdfDoc := func(name string, order []string) string {
		var tal []map[string]any
		for i, k := range order {
			th := gallupTheme(k)
			tal = append(tal, map[string]any{"rank": i + 1, "name": th[1], "ru": th[2], "domainRu": gallupDomainRu[th[3]], "essence": "Суть " + th[2]})
		}
		j, _ := json.Marshal(map[string]any{"board": map[string]string{"Мади Тестов": "r68b", "Асет Тестов": "r68a", "Алия Тестова": "r68c"}[name],
			"doc": map[string]any{"name": name, "talents": tal}})
		return string(j)
	}
	if w := send("resident", pdfDoc("Мади Тестов", gallupOrderB())); w.Code != 403 {
		t.Fatalf("resident may not send: %d", w.Code)
	}
	if w := send("", pdfDoc("Алия Тестова", gallupR29Order)); w.Code != 404 {
		t.Fatalf("not affected: %d", w.Code)
	}
	if w := send("", pdfDoc("Мади Тестов", gallupOrderB())); w.Code != 200 {
		t.Fatalf("send b: %d %s", w.Code, w.Body.String())
	}
	if w := send("", pdfDoc("Асет Тестов", gallupOrder)); w.Code != 200 {
		t.Fatalf("send a: %d %s", w.Code, w.Body.String())
	}
	// bot path: each resident got their own file in their own chat
	if len(out) != 2 || out[0].chat != 680002 || out[1].chat != 680001 || string(out[0].data) == string(out[1].data) ||
		!strings.HasPrefix(string(out[0].data), "%PDF-") || out[0].name == out[1].name || !strings.Contains(out[0].name, "Madi") && !strings.Contains(out[0].name, "Мади") {
		t.Fatalf("sent: %d %v", len(out), func() []string {
			var s []string
			for _, x := range out {
				s = append(s, fmt.Sprint(x.chat, " ", x.name))
			}
			return s
		}())
	}
	// the next audit keeps them as sent and lists nobody to fix
	doc, _ = h.GallupAudit(ctx)
	for id, it := range doc.Items {
		if it.SentAt == "" {
			t.Fatalf("%s still to fix: %+v", id, it)
		}
	}
	// a client may not overwrite the server's list
	ph := NewPlatformHandler(repo)
	r := gin.New()
	r.Use(func(c *gin.Context) { c.Set("userID", "u1") })
	r.PUT("/docs/:key", ph.PutDoc)
	w := httptest.NewRecorder()
	req := httptest.NewRequest("PUT", "/docs/bs_gallup_fix", strings.NewReader(`{"version":0,"value":"{}"}`))
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(w, req)
	if w.Code != 400 {
		t.Fatalf("client write of bs_gallup_fix: %d", w.Code)
	}
}
