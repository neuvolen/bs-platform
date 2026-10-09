package http

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"

	"github.com/bnursik/business_surgery_backend/internal/ai"
	"github.com/bnursik/business_surgery_backend/internal/content"
	"github.com/bnursik/business_surgery_backend/internal/tplpdf"
	"github.com/gin-gonic/gin"
)

// R71: «Gallup резидентам понравился анализ, ещё его улучши ×2 и добавь
// отдельно, чем именно наш проект будет полезен этому резиденту».

const gallupClubFixture = `{"intro":"Вы быстро решаете и сами тянете результат, поэтому клуб вам нужен как внешняя проверка и фильтр. Каждые 10 дней трекеры оставляют 2-3 задачи, которые двигают выручку, а пятёрка показывает ваши цифры рядом с цифрами других собственников.",
"mechanics":[{"key":"cycle","why":"За 10 дней вы успеваете запустить много, на разборе остаётся то, что принесло деньги."},
{"key":"tracking","why":"С «Распорядителем» вам редко возражают, двое трекеров спорят с вами по фактам."},
{"key":"five","why":"«Конкуренция» включается от сравнения: в пятёрке ваши цифры видны рядом с чужими."},
{"key":"library","why":"Готовые шаблоны внедряются за дни, искать и изобретать не нужно."},
{"key":"tasks","why":"Задачи пишутся с чек-листом и ежедневной отметкой, это ваш формат."},
{"key":"report","why":"Отчёт до 22:00 держит ритм, замер показывает, что выросло."},
{"key":"nope","why":"лишний ключ выбрасывается"}],
"diags":[{"id":"dx4_en_04","why":"Темп «Достигатора» без пауз даёт работу рывками."},{"id":"seed_dx_0","why":"Нет в списке кандидатов, выбрасывается."}],
"first":[{"task":"Отдать 3 задачи из операционки сотрудникам с письменным сроком","talent":"achiever","result":"3 задачи закрыты без вас"},
{"task":"Рейтинг продавцов по конверсии в пятёрке","talent":"competition","result":"конверсия выше на 5 пунктов"},
{"task":"Совещание, где вы говорите последним","talent":"nope","result":"2 решения от команды"}]}`

func TestR71GallupKB(t *testing.T) {
	bad := regexp.MustCompile(`, а не |(^|[^а-яё])не [^,.;:!?]*, а |—|–|\d{5}`)
	check := func(where, s string) {
		if strings.TrimSpace(s) == "" {
			t.Errorf("%s: empty", where)
		}
		if bad.MatchString(s) {
			t.Errorf("%s: brand rule: %q", where, s)
		}
	}
	for _, th := range gallupThemes {
		kb, ok := gallupKB[th[0]]
		if !ok {
			t.Fatalf("no KB for %s", th[0])
		}
		for i, s := range []string{kb.Sales, kb.Team, kb.Money, kb.Hire, kb.Blind, kb.Burnout, kb.Talk, kb.Exp, kb.Task} {
			check(th[0]+"#"+string(rune('0'+i)), s)
		}
		if len(gallupDiagTop[th[0]]) == 0 || len(gallupDiagLow[th[0]]) == 0 {
			t.Errorf("%s: no diagnoses", th[0])
		}
	}
	for _, m := range []map[string][]diagLink{gallupDiagTop, gallupDiagLow} {
		for k, ls := range m {
			if gallupTheme(k)[0] == "" {
				t.Errorf("diag map: unknown talent %s", k)
			}
			for _, l := range ls {
				if _, ok := content.RichDiagByID(l.ID); !ok {
					t.Errorf("%s: diagnosis %s is not in the library", k, l.ID)
				}
				check(k+" "+l.ID, l.Why)
			}
		}
	}
	for _, p := range gallupPairs {
		if gallupTheme(p.A)[0] == "" || gallupTheme(p.B)[0] == "" || (p.Kind != "synergy" && p.Kind != "tension") {
			t.Errorf("pair %+v", p)
		}
		check(p.A+"+"+p.B, p.Title+" "+p.Text+" "+p.Rule)
	}
	for i, a := range gallupDomainOrder {
		for _, b := range gallupDomainOrder[i:] {
			x, y := a, b
			if x > y {
				x, y = y, x
			}
			g, ok := gallupDomainPair[x+"|"+y]
			if !ok {
				t.Errorf("no domain pair %s|%s", x, y)
			}
			check(x+"|"+y, strings.Join(g[:], " "))
			g2, ok := gallupDomainPair2[x+"|"+y]
			if !ok || g2[0] == g[0] {
				t.Errorf("no second domain pair %s|%s", x, y)
			}
			check(x+"|"+y+" 2", strings.Join(g2[:], " "))
		}
		d := gallupDomainKB[a]
		check(a, d.Role+d.Own+d.Weak+d.Seek+d.Ask[0]+d.Ask[1]+d.RedFlag+d.Cycle+d.Tracking+d.Five+d.Library)
	}
	for k, h := range gallupHook {
		if gallupTheme(k)[0] == "" {
			t.Errorf("hook: %s", k)
		}
		ok := false
		for _, m := range gallupMechKeys {
			ok = ok || m == h[0]
		}
		if !ok {
			t.Errorf("hook %s: mechanic %s", k, h[0])
		}
		check("hook "+k, h[1])
	}
}

func TestR71GallupPlus(t *testing.T) {
	x := gallupPlusFor(gallupR29Order, nil)
	if x == nil || x.N != 34 || x.AI {
		t.Fatalf("plus: %+v", x)
	}
	if len(x.Top) != 5 || x.Top[0].Key != "achiever" || len(x.Top[0].Rows) != 7 || x.Top[2].Ru != "Распорядитель" {
		t.Fatalf("top: %+v", x.Top)
	}
	if len(x.Pairs) != 10 {
		t.Fatalf("pairs: %d", len(x.Pairs))
	}
	titles := map[string]bool{}
	for _, p := range x.Pairs {
		if titles[p.Title] {
			t.Errorf("pair title twice: %s", p.Title)
		}
		titles[p.Title] = true
	}
	// named pairs first: «Достигатор + Сосредоточенность», «Конкуренция + Достигатор»
	named := 0
	for _, p := range x.Pairs[:3] {
		if p.Title == "Много и в одну сторону" || p.Title == "Обогнать и сделать" {
			named++
		}
	}
	if named != 2 {
		t.Fatalf("named pairs: %+v", x.Pairs[:3])
	}
	total := 0
	for _, s := range x.Strip {
		total += len(s.Ranks)
	}
	if len(x.Strip) != 4 || total != 34 || x.Strip[0].Ru != "Исполнение" || x.Strip[1].Top5 != 2 {
		t.Fatalf("strip: %+v", x.Strip)
	}
	// R29 order: influencing leads, relationship is the weakest
	if len(x.Split) != 4 || x.Split[0].Area[:len("Влияние")] != "Влияние" || x.Split[3].Who != "Нанятый специалист" ||
		!strings.HasPrefix(x.Split[3].Area, "Отношения") {
		t.Fatalf("split: %+v", x.Split)
	}
	if x.Hire == nil || len(x.Hire.Talents) != 3 || x.Hire.Talents[0] != "Гармония" || len(x.Hire.Ask) != 2 {
		t.Fatalf("hire: %+v", x.Hire)
	}
	if len(x.Talk) != 5 || len(x.Exp) != 6 || x.Exp[5].A != "День 10" {
		t.Fatalf("talk/exp: %d %+v", len(x.Talk), x.Exp)
	}
	c := x.Club
	if c == nil || len(c.Mechanics) != 6 || len(c.Diags) < 3 || len(c.Tools) == 0 || len(c.First) != 3 || !strings.Contains(c.Intro, "Влияние") {
		t.Fatalf("club: %+v", c)
	}
	// command → tracking hook, competition → five hook
	for _, m := range c.Mechanics {
		if m.Key == "tracking" && !strings.Contains(m.You, "Распорядител") || m.Key == "five" && !strings.Contains(m.You, "Конкуренция") {
			t.Errorf("mechanic %s: %s", m.Key, m.You)
		}
	}
	organs := map[string]int{}
	for _, d := range c.Diags {
		organs[d.Organ]++
		if organs[d.Organ] > 2 || d.Why == "" || d.Title == "" {
			t.Errorf("diag %+v", d)
		}
	}
	// a partial report (10 themes): no weak talents are guessed
	p := gallupPlusFor(gallupR29Order[:10], nil)
	if p == nil || p.N != 10 || len(p.Club.Diags) == 0 {
		t.Fatalf("partial: %+v", p)
	}
	for _, d := range p.Club.Diags {
		if strings.Contains(d.Why, "из 10") {
			t.Errorf("partial: a weak-talent reason %q", d.Why)
		}
	}
	if gallupPlusFor([]string{"achiever", "nope"}, nil) != nil {
		t.Fatal("2 talents: no plus")
	}
	// AI lines replace the rule ones they cover
	cai := gallupClubOf(json.RawMessage(gallupClubFixture), gallupR29Order)
	if cai == nil || len(cai.Mechanics) != 6 || len(cai.First) != 3 || cai.First[2].Talent != "" || len(cai.Diags) > 1 {
		t.Fatalf("club AI: %+v", cai)
	}
	y := gallupPlusFor(gallupR29Order, cai)
	if !y.AI || !strings.HasPrefix(y.Club.Intro, "Вы быстро решаете") || y.Club.Mechanics[0].You != "За 10 дней вы успеваете запустить много, на разборе остаётся то, что принесло деньги." ||
		y.Club.First[0].B != "Опора на «Достигатор»." {
		t.Fatalf("merged: %+v", y.Club)
	}
	if _, err := parseGallupClubAI(`{"intro":"коротко"}`, gallupR29Order, nil); err == nil {
		t.Fatal("a short answer passes")
	}
}

// The PDF with the second half; BS_GALLUP_PDF=dir keeps it for a look.
func TestR71GallupPlusPDF(t *testing.T) {
	var talents []tplpdf.GallupTalent
	for i, k := range gallupR29Order {
		th := gallupTheme(k)
		talents = append(talents, tplpdf.GallupTalent{Rank: i + 1, Name: th[1], Ru: th[2], DomainRu: gallupDomainRu[th[3]], Essence: "Суть таланта " + th[2] + ".",
			Business: "Как проявляется в бизнесе.", Blind: "Слепая зона.", Manage: "Как использовать."})
	}
	var top5 []tplpdf.GallupTop
	for _, t := range talents[:5] {
		top5 = append(top5, tplpdf.GallupTop{Rank: t.Rank, Name: t.Name, Ru: t.Ru, Domain: t.DomainRu, Line: "Как тема видна в работе собственника каждый день."})
	}
	g := tplpdf.GallupDoc{Name: "Альтаир Сейткали", Date: "10.10.2026", Headline: "Предприниматель, который выигрывает темпом", Plain: "Вы решаете быстро и доводите до результата.",
		Top5: top5, Talents: talents, Domains: []tplpdf.GallupDomain{{Ru: "Исполнение", Score: 58, Top10: 4}, {Ru: "Влияние", Score: 66, Top10: 4}, {Ru: "Построение отношений", Score: 22, Top10: 0}, {Ru: "Стратегическое мышление", Score: 52, Top10: 2}},
		Order: gallupR29Order, ClubAI: json.RawMessage(gallupClubFixture)}
	sanitizeGallupDoc(&g)
	if g.Plus == nil || !g.Plus.AI || g.Order != nil {
		t.Fatalf("plus: %+v", g.Plus)
	}
	b, err := tplpdf.RenderGallup(&g)
	if err != nil || len(b) < 20000 {
		t.Fatalf("render: %v %d", err, len(b))
	}
	// an older page: no order, the names of the talents give it
	g2 := tplpdf.GallupDoc{Talents: talents, Top5: top5}
	sanitizeGallupDoc(&g2)
	if g2.Plus == nil || g2.Plus.Top[0].Key != "achiever" || g2.Plus.AI {
		t.Fatalf("plus from names: %+v", g2.Plus)
	}
	if dir := os.Getenv("BS_GALLUP_PDF"); dir != "" {
		_ = os.WriteFile(filepath.Join(dir, "gallup_r71.pdf"), b, 0o644)
		b2, _ := tplpdf.RenderGallup(&g2)
		_ = os.WriteFile(filepath.Join(dir, "gallup_r71_rules.pdf"), b2, 0o644)
	}
}

func TestR71GallupPlusEndpoint(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.POST("/gallup/plus", GallupPlus)
	call := func(body string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		req := httptest.NewRequest("POST", "/gallup/plus", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		r.ServeHTTP(w, req)
		return w
	}
	ord, _ := json.Marshal(map[string]any{"order": gallupR29Order, "club": json.RawMessage(gallupClubFixture)})
	w := call(string(ord))
	var out struct {
		V    int                `json:"v"`
		Plus *tplpdf.GallupPlus `json:"plus"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &out)
	if w.Code != 200 || out.V != gallupDeepVer || out.Plus == nil || !out.Plus.AI || len(out.Plus.Club.Diags) == 0 {
		t.Fatalf("plus: %d %.300s", w.Code, w.Body.String())
	}
	// Russian names work too, without AI
	w = call(`{"order":["Стратегия","Ученик","Собиратель","Мышление","Будущее"]}`)
	_ = json.Unmarshal(w.Body.Bytes(), &out)
	if w.Code != 200 || out.Plus.AI || out.Plus.Top[0].Key != "strategic" || len(out.Plus.Pairs) != 10 {
		t.Fatalf("names: %d %.300s", w.Code, w.Body.String())
	}
	if w = call(`{"order":["x"]}`); w.Code != 400 {
		t.Fatalf("bad order: %d", w.Code)
	}
}

// A profile analysed before R71: POST {order, only:"club"} asks the light
// model once and keeps the answer.
func TestR71GallupClubOnly(t *testing.T) {
	repo, db := platformTestRepo(t)
	ctx := context.Background()
	_, _ = db.Pool.Exec(ctx, `DELETE FROM platform_files WHERE id LIKE 'gal3c_%'`)
	var mu sync.Mutex
	calls := 0
	gm := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		calls++
		mu.Unlock()
		_ = json.NewEncoder(w).Encode(map[string]any{"candidates": []any{map[string]any{
			"content": map[string]any{"parts": []any{map[string]any{"text": gallupClubFixture}}}}}})
	}))
	defer gm.Close()
	h := NewPlatformAI(repo, &ai.Client{Gemini: "k", GeminiModel: "m", GeminiBase: gm.URL, HTTP: gm.Client()})
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(func(c *gin.Context) { c.Set("role", "admin"); c.Set("userID", "tg:1") })
	r.POST("/ai/gallup", h.Gallup)
	body, _ := json.Marshal(map[string]any{"order": gallupR29Order, "only": "club"})
	for i := 0; i < 2; i++ {
		w := httptest.NewRecorder()
		req := httptest.NewRequest("POST", "/ai/gallup", strings.NewReader(string(body)))
		req.Header.Set("Content-Type", "application/json")
		r.ServeHTTP(w, req)
		var out struct {
			V    int           `json:"v"`
			Club *gallupClubAI `json:"club"`
		}
		_ = json.Unmarshal(w.Body.Bytes(), &out)
		if w.Code != 200 || out.V != gallupDeepVer || out.Club == nil || len(out.Club.First) != 3 {
			t.Fatalf("club only #%d: %d %.300s", i, w.Code, w.Body.String())
		}
		if i == 1 && w.Header().Get("X-Gallup-Cache") != "hit" {
			t.Fatal("club only: not cached")
		}
	}
	if calls != 1 {
		t.Fatalf("calls: %d", calls)
	}
}
