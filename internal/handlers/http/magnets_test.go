package http

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/bnursik/business_surgery_backend/internal/bot"
	"github.com/bnursik/business_surgery_backend/internal/content"
	"github.com/bnursik/business_surgery_backend/web"
	"github.com/gin-gonic/gin"
)

// thMagnetPct: the classic Threads tests run without the magnets (R76).
var thMagnetPct any = 0

// thNotButRe: the brand avoids «не X, а Y» (checked on the hand-written texts).
var thNotButRe = regexp.MustCompile(`(?i)(?:^|[\s(«"])не [^.,!?;:\n]{1,40}, а `)

var mgBanned = []string{"хирург", "операция", "операцию", "операцией", "операции ", "прокача", "гарантированн", "—", "–", "{", "}"}

// Every hand-written magnet post passes the gate with its real link, keeps
// the brand rules and makes the offer.
func TestMagnetPostsPassTheGate(t *testing.T) {
	bigNum := regexp.MustCompile(`\d{4,}`)
	seen := map[string]bool{}
	for i := range leadMagnetList {
		m := &leadMagnetList[i]
		if strings.Contains(m.title(), "{") || m.Icon == "" || m.CTA == "" || m.Weight < 1 {
			t.Fatalf("%s: %+v", m.ID, m)
		}
		for v := 0; v < 3; v++ {
			body, _, probs := magnetPost(m, v)
			if len(probs) > 0 {
				t.Errorf("%s/%d refused: %v\n%s", m.ID, v, probs, body)
			}
			it := &contentItem{contentItemData: contentItemData{Channel: "threads", Text: body, Magnet: m.ID, At: "2026-10-12T12:34:00+05:00"}}
			manualize(it)
			if n := utf8.RuneCountInString(it.Text); n > threadsMaxText || n < 250 {
				t.Errorf("%s/%d: %d signs", m.ID, v, n)
			}
			if !strings.HasSuffix(it.Text, m.CTA+"app.bxclub.kz/m/"+m.ID+"-p2610121234") || it.Link != "t.me/bsurgery_bot?start=mg_"+m.ID+"_p2610121234" || !it.CTA {
				t.Errorf("%s/%d link: %q %q", m.ID, v, it.Text[len(it.Text)-80:], it.Link)
			}
			low := strings.ToLower(body + mgFill(m.Give+m.Warm+m.Title+m.Short))
			for _, w := range mgBanned {
				if strings.Contains(low, w) {
					t.Errorf("%s/%d: %q", m.ID, v, w)
				}
			}
			if thNotButRe.MatchString(low) {
				t.Errorf("%s/%d: «не X, а Y»: %q", m.ID, v, thNotButRe.FindString(low))
			}
			if x := bigNum.FindString(body); x != "" {
				t.Errorf("%s/%d: number without spaces %s", m.ID, v, x)
			}
			if !strings.Contains(low, "бесплат") {
				t.Errorf("%s/%d: no offer", m.ID, v)
			}
			f := thFirst(body)
			if seen[f] {
				t.Errorf("%s/%d: the same opening as another post", m.ID, v)
			}
			seen[f] = true
		}
	}
	if len(leadMagnetList) < 10 {
		t.Fatalf("%d magnets", len(leadMagnetList))
	}
}

// The numbers in the posts are the catalogue's and the library's own.
func TestMagnetFacts(t *testing.T) {
	plain, _, _, err := content.Ideas()
	if err != nil {
		t.Fatal(err)
	}
	var cat struct {
		Items []content.IdeaItem `json:"items"`
	}
	_ = json.Unmarshal(plain, &cat)
	find := func(prefix string) *content.IdeaItem {
		for i := range cat.Items {
			if strings.HasPrefix(cat.Items[i].Title, prefix) {
				return &cat.Items[i]
			}
		}
		t.Fatalf("no idea %q", prefix)
		return nil
	}
	if p := find("Цех пельменей"); p.Budget[0] != 3_500_000 || p.Payback != "10-18 мес" {
		t.Errorf("пельмени: %v %s", p.Budget, p.Payback)
	}
	if p := find("Мастер на час"); p.Budget[0] != 150_000 {
		t.Errorf("мастер на час: %v", p.Budget)
	}
	if p := find("Сборка мебели"); p.Budget[0] != 100_000 {
		t.Errorf("сборка мебели: %v", p.Budget)
	}
	if p := find("Травяные чаи"); p.Budget[0] != 600_000 || !strings.Contains(p.Desc+p.Short, "Алмат") {
		t.Errorf("травяные чаи: %v", p.Budget)
	}
	if n := web.IdeaPackCount("1m"); n < 400 || !strings.Contains(mgFill(leadMagnetByID["ideas1m"].Posts[0]), content.Num(n)+" бизнес-идей") {
		t.Errorf("1m: %d", n)
	}
	if n := web.IdeaPackCount("almaty"); n < 80 {
		t.Errorf("almaty: %d", n)
	}
	d, tl, i := content.Scale()
	if d != 278 || tl != 310 || i != 1059 {
		t.Logf("scale now %d/%d/%d (texts take it from the files)", d, tl, i)
	}
	for _, id := range []string{"paycal", "script", "org"} {
		m := leadMagnetByID[id]
		tool, pdf, _, err := TemplatePDF(m.Path)
		if err != nil || tool == nil || len(pdf) < 1000 || !strings.HasPrefix(tool.Template.Title, m.Title[:10]) {
			t.Errorf("%s: %v %v", id, err, tool)
		}
	}
	if !strings.Contains(gallupKB["achiever"].Blind, "30 задач") || !strings.Contains(gallupDomainKB["executing"].Seek, "операционный директор") ||
		!strings.Contains(gallupDomainKB["strategic"].Seek, "аналитик") {
		t.Error("the Gallup posts quote the knowledge base")
	}
}

// 4 posts a day: 1 or 2 magnets (never the morning post), about 1,5 on
// average, the reach pyramid in the rest, no other CTA on a magnet day.
func TestMagnetDayPlan(t *testing.T) {
	day := time.Date(2026, 10, 12, 0, 0, 0, 0, almaty)
	total, twos := 0, 0
	for k := 0; k < 60; k++ {
		key := contentDay(day.AddDate(0, 0, k))
		f, cta, isMg := threadsDayPlanMg(4, key, 65, 38)
		m, reach := 0, 0
		for i := range f {
			if isMg[i] != (f[i] == "magnet") || cta[i] {
				t.Fatalf("%s: %v %v %v", key, f, cta, isMg)
			}
			if isMg[i] {
				m++
				if i == 0 {
					t.Fatalf("%s: the morning post is a magnet: %v", key, f)
				}
			}
			if IsThreadsReach(f[i]) {
				reach++
			}
		}
		if m < 1 || m > 2 || reach < 1 || 4-m-reach < 1 {
			t.Fatalf("%s: %v", key, f)
		}
		if m == 2 {
			twos++
		}
		total += m
	}
	if total < 75 || total > 105 || twos < 10 {
		t.Fatalf("60 days: %d magnets, %d days with two", total, twos)
	}
	if f, _, _ := threadsDayPlanMg(16, "20261012", 65, 38); strings.Count(strings.Join(f, " "), "magnet") > threadsMagnetCap {
		t.Fatalf("16 a day: %v", f)
	}
	if _, _, mg := threadsDayPlanMg(4, "20261012", 65, 0); strings.Contains(fmt.Sprint(mg), "true") {
		t.Fatal("0% means no magnets")
	}
	var s contentSettings
	if s.threadsMagnet() != 38 {
		t.Fatal("default 38%")
	}
	ms := parseContentSettings(nil)
	ms.manual = true
	if line := magnetPlanLine(ms, day); !strings.Contains(line, "magnet") {
		t.Fatalf("plan line %q", line)
	}
}

// The manual batch: magnet posts with their own counted link; «Другой пост»
// moves the link with the post; the rotation does not repeat a magnet a day.
func TestMagnetManualBatch(t *testing.T) {
	old := thMagnetPct
	thMagnetPct = nil // the default 38%
	defer func() { thMagnetPct = old }()
	ctx := context.Background()
	used := map[string]int{}
	var mgDays int
	for k := 0; k < 6; k++ {
		now := time.Date(2026, 10, 12+k, 6, 30, 0, 0, almaty)
		e, docs, _ := thManualEngine(t, &now)
		res, err := e.BuildThreadsDay(ctx, now, false)
		if err != nil {
			t.Fatal(err)
		}
		items := thItems(docs.doc(t), contentDay(now))
		if len(items) != 4 || res.Magnets < 1 || res.Magnets > 2 {
			t.Fatalf("day %d: %+v", k, res)
		}
		day := map[string]bool{}
		for _, it := range items {
			if it.Magnet == "" {
				if strings.Contains(it.Text, "/m/") {
					t.Fatalf("a magnet link in a usual post: %q", it.Text)
				}
				continue
			}
			mgDays++
			code := strings.TrimPrefix(thPostParam(it), "th_")
			if it.Format != "magnet" || it.Gen != "mg" || day[it.Magnet] || strings.Count(it.Text, "/m/") != 1 ||
				!strings.HasSuffix(it.Text, "app.bxclub.kz/m/"+it.Magnet+"-"+code) || it.Link != contentBotLink+"mg_"+it.Magnet+"_"+code {
				t.Fatalf("magnet post: %+v", it.contentItemData)
			}
			day[it.Magnet] = true
			used[it.Magnet]++
			// moved to another slot (Другой пост): the link follows
			cp := *it
			cp.At = "2026-10-20T19:31:00+05:00"
			manualize(&cp)
			if !strings.HasSuffix(cp.Text, "/m/"+it.Magnet+"-p2610201931") || strings.Count(cp.Text, "/m/") != 1 || cp.Link != contentBotLink+"mg_"+it.Magnet+"_p2610201931" {
				t.Fatalf("after the swap: %q %q", cp.Text, cp.Link)
			}
		}
	}
	if mgDays < 6 || used["ideas"] == 0 {
		t.Fatalf("magnets over 6 days: %v", used)
	}
}

// AI posts with a long first sentence keep it on a line of its own.
func TestSplitHook(t *testing.T) {
	long := "Собственник часто путает выручку и деньги на счёте, и из-за этого принимает решения, которые съедают прибыль. Проверь три цифры каждую неделю."
	got := splitHook(long)
	if first := strings.Split(got, "\n")[0]; utf8.RuneCountInString(first) > 120 || !strings.HasSuffix(first, "прибыль.") {
		t.Fatalf("%q", got)
	}
	if s := "Короткая строка.\nДальше текст."; splitHook(s) != s {
		t.Fatal("a short first line stays")
	}
}

type mgTG struct {
	mu   sync.Mutex
	msgs []thMsg
	docs []string
}

func (g *mgTG) send(_ context.Context, chat int64, text string, kb map[string]any) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.msgs = append(g.msgs, thMsg{chat: chat, text: text, kb: kb})
	return nil
}

func (g *mgTG) doc(_ context.Context, chat int64, key, name string, data []byte, fileID, caption string, kb map[string]any) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	if len(data) < 1000 || !strings.HasSuffix(name, ".pdf") {
		return fmt.Errorf("bad pdf %s", name)
	}
	g.docs = append(g.docs, key)
	g.msgs = append(g.msgs, thMsg{chat: chat, text: caption, kb: kb})
	return nil
}

func mgLead(t *testing.T, docs *cntDocs, tg int64) map[string]any {
	t.Helper()
	d, _ := docs.GetDoc(context.Background(), "club", "bs_crm")
	if d == nil {
		t.Fatal("no crm")
	}
	var crm map[string]any
	_ = json.Unmarshal([]byte(d.Value), &crm)
	l := findLeadByTg(asList(crm["leads"]), tg)
	if l == nil {
		t.Fatalf("no lead %d", tg)
	}
	return l
}

// The bot gives the material at once, the CRM knows the magnet and the post,
// the click is counted and Маркетинг shows posts → clicks → starts → leads.
func TestMagnetStartClickStats(t *testing.T) {
	gin.SetMode(gin.TestMode)
	ctx := context.Background()
	docs := &cntDocs{}
	g := &mgTG{}
	now := time.Date(2026, 10, 12, 12, 40, 0, 0, almaty)
	f := NewLeadFunnel(docs, g.send, nil)
	f.now = func() time.Time { return now }
	f.Doc = g.doc

	// the click on the post's link
	r := gin.New()
	r.GET("/m/:code", f.MagnetClick)
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/m/ideas-p2610121234", nil)
	req.Header.Set("User-Agent", "Mozilla/5.0 (iPhone; CPU iPhone OS 18_0 like Mac OS X) Barcelona 350.0")
	r.ServeHTTP(w, req)
	if w.Code != http.StatusFound || w.Header().Get("Location") != "https://t.me/bsurgery_bot?start=mg_ideas_p2610121234" {
		t.Fatalf("click: %d %q", w.Code, w.Header().Get("Location"))
	}
	for _, ua := range []string{"facebookexternalhit/1.1", "Mozilla/5.0 (iPhone; CPU iPhone OS 18_0 like Mac OS X) Barcelona 350.0"} {
		w = httptest.NewRecorder()
		req = httptest.NewRequest(http.MethodGet, "/m/ideas-p2610121234", nil)
		req.Header.Set("User-Agent", ua) // the preview robot and the same person again: not counted
		r.ServeHTTP(w, req)
	}
	w = httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/m/nope", nil))
	if w.Code != http.StatusFound || !strings.HasSuffix(w.Header().Get("Location"), "start=threads") {
		t.Fatalf("unknown magnet: %q", w.Header().Get("Location"))
	}
	var clicks int64
	for i := 0; i < 100 && clicks == 0; i++ {
		time.Sleep(10 * time.Millisecond)
		if d, _ := docs.GetDoc(ctx, "club", magnetDoc); d != nil {
			var doc map[string]map[string]map[string]any
			_ = json.Unmarshal([]byte(d.Value), &doc)
			clicks = anyInt(doc["clicks"]["ideas"]["n"])
		}
	}
	time.Sleep(50 * time.Millisecond)
	if d, _ := docs.GetDoc(ctx, "club", magnetDoc); d == nil || !strings.Contains(d.Value, `"n":1`) || !strings.Contains(d.Value, `"p2610121234":1`) || !strings.Contains(d.Value, `"20261012":1`) {
		t.Fatalf("clicks doc: %v", d)
	}

	// the /start from the post: the catalogue at once, then the 2-minute check
	if !f.HandleStart(ctx, bot.StartUpdate{ChatID: 501, Param: "mg_ideas_p2610121234", FirstName: "Айдар", Username: "aidar"}) {
		t.Fatal("start not handled")
	}
	l := mgLead(t, docs, 501)
	if l["source"] != "Threads: магнит «"+mgFill("{ideas} бизнес-идей")+"», пост 12.10 12:34" || l["magnet"] != "ideas" || l["funnel"] != "bot" {
		t.Fatalf("lead %v", l)
	}
	if mp, _ := l["mgPost"].(map[string]any); mp["ideas"] != "p2610121234" {
		t.Fatalf("mgPost %v", l["mgPost"])
	}
	if len(g.msgs) != 2 || !strings.Contains(g.msgs[0].text, "бизнес-идей") || kbButtons(g.msgs[0].kb)["💡 Открыть: "+mgFill("{ideas} бизнес-идей")] != web.SiteURL+"/ideas" ||
		!strings.Contains(g.msgs[1].text, "болит") {
		t.Fatalf("messages %+v", g.msgs)
	}
	if bt := kbButtons(g.msgs[0].kb); bt["До 1 млн ₸"] != web.SiteURL+"/ideas?pack=1m" || bt["🎁 Ещё бесплатные материалы"] != "mg_menu" {
		t.Fatalf("buttons %v", bt)
	}
	if startSource("mg_ideas") != "Магнит: «"+mgFill("{ideas} бизнес-идей")+"»" || sourceGroup(startSource("mg_ideas_p2610121234")) != "Threads" {
		t.Fatal("labels")
	}

	// a PDF magnet from the bot's menu: the template as a file
	g.msgs = nil
	if !f.HandleCallback(ctx, bot.CallbackUpdate{ChatID: 501, FromID: 501, FirstName: "Айдар", Data: "mg_get_paycal"}) {
		t.Fatal("menu pick")
	}
	if len(g.docs) != 1 || g.docs[0] != "tpl_seed_tl_0" {
		t.Fatalf("pdf %v %+v", g.docs, g.msgs)
	}
	g.msgs = nil
	if !f.HandleCallback(ctx, bot.CallbackUpdate{ChatID: 501, FromID: 501, Data: "mg_menu"}) || len(g.msgs) != 1 || len(kbButtons(g.msgs[0].kb)) != len(leadMagnetList) {
		t.Fatalf("menu %+v", g.msgs)
	}
	// the diagnoses: the check in the chat at once, for a known lead too
	g.msgs = nil
	now = now.Add(2 * time.Minute)
	if !f.HandleStart(ctx, bot.StartUpdate{ChatID: 501, Param: "mg_diag"}) || len(g.msgs) != 2 || !strings.Contains(g.msgs[1].text, "болит") {
		t.Fatalf("diag %+v", g.msgs)
	}
	// the welcome menu has the magnets
	g.msgs = nil
	if err := f.sendWelcome(ctx, 502, "", ""); err != nil || kbButtons(g.msgs[0].kb)["🎁 Ещё бесплатно: шаблоны, карта диагнозов, Gallup"] != "mg_menu" {
		t.Fatalf("welcome %v %+v", err, g.msgs)
	}

	// the warm-up's first touch remembers the magnet
	if !strings.HasPrefix(magnetWarm("ideas"), "Вы забрали каталог") || magnetWarm("guides") != "" {
		t.Fatal("warm lines")
	}

	// Маркетинг: the published magnet post of 12.10 12:34 (variant 1)
	cd := &contentDoc{}
	cd.Queue = []*contentItem{{contentItemData: contentItemData{ID: "th-20261012-1234", Channel: "threads", Kind: "threads", At: "2026-10-12T12:34:00+05:00",
		Status: "published", ByHand: true, Magnet: "ideas", V: 1, Format: "magnet", Src: "mg:ideas/1"}},
		{contentItemData: contentItemData{ID: "th-20261013-1234", Channel: "threads", Kind: "threads", At: "2026-10-13T12:34:00+05:00", Status: "planned", Magnet: "ideas", V: 2}}}
	b, _ := json.Marshal(cd)
	if _, err := docs.PutDoc(ctx, "club", contentKey, 0, string(b), false, "t"); err != nil {
		t.Fatal(err)
	}
	st, err := f.MagnetStats(ctx, 30)
	if err != nil {
		t.Fatal(err)
	}
	var row MagnetRow
	for _, x := range st.Rows {
		if x.ID == "ideas" {
			row = x
		}
	}
	if row.Posts != 1 || row.Pub != 1 || row.Clicks != 1 || row.Starts != 1 || row.Leads != 1 || row.Next != "2026-10-13T12:34:00+05:00" ||
		row.Variants[1].Posts != 1 || row.Variants[1].Clicks != 1 || row.Variants[1].Starts != 1 {
		t.Fatalf("ideas row %+v", row)
	}
	if st.Total.Starts != 3 || st.Total.Leads != 1 { // ideas, paycal and diag were taken; one lead
		t.Fatalf("total %+v", st.Total)
	}
	fs, _ := f.FunnelStats(ctx, 14)
	if fs.Magnets != 1 || fs.ThreadPost != 1 || !strings.Contains(fs.Line(), "magnets 1") {
		t.Fatalf("funnel stats %+v", fs)
	}
}

// The open pages of the magnets.
func TestMagnetPages(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	web.RegisterPublic(r)
	for path, want := range map[string]string{
		"/free/gallup":         "Достигатор",
		"/ideas":               content.Num(content.IdeasTotal()) + " бизнес-идей",
		"/ideas?pack=1m":       "со стартом до 1 млн ₸",
		"/ideas/idea_prod_001": "3,5 млн ₸",
		"/free/map":            "Карта диагнозов",
		"/free/plan10":         "План на 10 дней",
	} {
		w := httptest.NewRecorder()
		r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, path, nil))
		body := w.Body.String()
		if w.Code != 200 || !strings.Contains(body, want) || strings.Contains(body, "—") {
			t.Errorf("%s: %d, has %q: %v, em dash: %v", path, w.Code, want, strings.Contains(body, want), strings.Contains(body, "—"))
		}
	}
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/free/gallup", nil))
	if n := strings.Count(w.Body.String(), "<details>"); n != 34 {
		t.Errorf("gallup: %d talents", n)
	}
	for _, m := range LeadMagnets("bsurgery_bot") {
		if u := fmt.Sprint(m["url"]); !strings.HasPrefix(u, "https://") || m["title"] == "" {
			t.Errorf("lead home magnet %v", m)
		}
	}
}
