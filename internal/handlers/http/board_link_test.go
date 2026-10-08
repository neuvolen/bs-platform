package http

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	pg "github.com/bnursik/business_surgery_backend/internal/repository/pg"
	"github.com/bnursik/business_surgery_backend/migrations"
)

func r57Setup(t *testing.T) (*gin.Engine, *BoardLinks, *pg.BoardLinksRepo, *[]string, *time.Time) {
	dsn := os.Getenv("BS_TEST_DSN")
	if dsn == "" {
		t.Skip("BS_TEST_DSN not set")
	}
	ctx := context.Background()
	db, err := pg.NewDB(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(db.Pool.Close)
	if err := pg.Migrate(ctx, db, migrations.FS); err != nil {
		t.Fatal(err)
	}
	_, _ = db.Pool.Exec(ctx, `DELETE FROM board_links WHERE board_id IN ('r57a','r57b'); DELETE FROM platform_boards WHERE id IN ('r57a','r57b');
		DELETE FROM platform_docs WHERE scope='club' AND key='bs_crm'`)
	repo := pg.NewPlatformRepo(db)
	put := func(id, res, diag, note string) {
		data := `{"id":"` + id + `","name":"` + res + `","info":{"name":"` + res + `","a":"Оборот 3 000 000 ₸","b":"Оборот 6 000 000 ₸"},
			"history":[{"x":1}],"tests":{"gallup":[1]},"health":{"fin":3},
			"nodes":[{"id":1,"type":"root","title":"` + res + `"},
			{"id":2,"type":"diag","title":"` + diag + `","desc":"Почему так","organ":"Финансы"},
			{"id":3,"type":"note","title":"` + note + `","author":"admin"},
			{"id":4,"type":"fine","title":"Штраф 10 000 ₸ секрет"},
			{"id":5,"type":"task","title":"Внедрить ДДС","task":{"date":"2026-10-12","completed":false}},
			{"id":6,"type":"tool","title":"ДДС","desc":"Шаблон"},
			{"id":7,"type":"dna","title":"Нет учёта","dna":{"effects":["Кассовый разрыв"]}}]}`
		if _, err := repo.PutBoard(ctx, id, 0, json.RawMessage(data), "tg:1"); err != nil {
			t.Fatal(err)
		}
	}
	put("r57a", "Алия Клиентова", "Нет управленческого учёта", "ВНУТРЕННЯЯ заметка трекера")
	put("r57b", "Чужой Резидент", "Чужой диагноз", "Чужая заметка")
	_, _ = repo.PutDoc(ctx, "club", "bs_crm", 0, `{"leads":[{"id":"L1","name":"Алия Клиентова","col":"diag"}]}`, false, "tg:1")

	now := time.Date(2026, 10, 8, 10, 0, 0, 0, time.UTC)
	links := pg.NewBoardLinksRepo(db)
	s := NewClubSales(repo, []byte("x"))
	s.Now = func() time.Time { return now }
	h := NewBoardLinks(links, repo, s, []byte("x"))
	h.Now = func() time.Time { return now }
	var mu sync.Mutex
	notes := []string{}
	h.Notify = func(_ context.Context, text string) { mu.Lock(); notes = append(notes, text); mu.Unlock() }
	gin.SetMode(gin.TestMode)
	r := gin.New()
	pub := r.Group("/b/")
	pub.Use(h.publicGuard)
	pub.GET(":token", h.Page)
	pub.GET(":token/data", h.Data)
	pub.POST(":token/ev", h.Event)
	pub.POST(":token/intent", h.IntentHTTP)
	a := r.Group("/cl", func(c *gin.Context) { c.Set("userID", "tg:453800951"); c.Next() })
	a.GET("/:board", h.AdminGet)
	a.POST("/:board", h.AdminCreate)
	a.DELETE("/:board", h.AdminRevoke)
	return r, h, links, &notes, &now
}

func r57Do(r http.Handler, method, path, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w
}

func r57Link(t *testing.T, w *httptest.ResponseRecorder) boardLinkView {
	t.Helper()
	var out struct{ Link *boardLinkView }
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil || out.Link == nil {
		t.Fatalf("link: %d %s", w.Code, w.Body.String())
	}
	return *out.Link
}

func r57Token(u string) string { return u[strings.LastIndex(u, "/")+1:] }

func TestR57LinkCreateReuseRevoke(t *testing.T) {
	r, _, _, _, _ := r57Setup(t)
	l1 := r57Link(t, r57Do(r, "POST", "/cl/r57a", ""))
	if !l1.Live || !boardLinkIDRe.MatchString(r57Token(l1.URL)) || !strings.Contains(l1.URL, "/b/") {
		t.Fatalf("bad link %+v", l1)
	}
	if !strings.HasPrefix(l1.WA, "https://wa.me/?text=") || !strings.HasPrefix(l1.TG, "https://t.me/share/url?url=") {
		t.Fatalf("share links %q %q", l1.WA, l1.TG)
	}
	l2 := r57Link(t, r57Do(r, "POST", "/cl/r57a", ""))
	if l2.URL != l1.URL {
		t.Fatal("a live link must be reused")
	}
	if w := r57Do(r, "POST", "/cl/nope", ""); w.Code != http.StatusNotFound {
		t.Fatalf("unknown board: %d", w.Code)
	}
	rv := r57Link(t, r57Do(r, "DELETE", "/cl/r57a", ""))
	if rv.Live || rv.State != "revoked" {
		t.Fatalf("revoke: %+v", rv)
	}
	w := r57Do(r, "GET", "/b/"+r57Token(l1.URL), "")
	if w.Code != http.StatusNotFound || !strings.Contains(w.Body.String(), "больше не действует") {
		t.Fatalf("revoked page: %d", w.Code)
	}
	l3 := r57Link(t, r57Do(r, "POST", "/cl/r57a", ""))
	if l3.URL == l1.URL || !l3.Live {
		t.Fatal("after a revoke a new link is made")
	}
}

func TestR57ExpiredAndBadToken(t *testing.T) {
	r, _, _, _, now := r57Setup(t)
	l := r57Link(t, r57Do(r, "POST", "/cl/r57a", ""))
	if w := r57Do(r, "GET", "/b/"+r57Token(l.URL), ""); w.Code != 200 {
		t.Fatalf("live page %d", w.Code)
	}
	*now = now.Add(31 * 24 * time.Hour)
	w := r57Do(r, "GET", "/b/"+r57Token(l.URL), "")
	if w.Code != http.StatusNotFound || !strings.Contains(w.Body.String(), "Срок ссылки закончился") {
		t.Fatalf("expired page: %d", w.Code)
	}
	l2 := r57Link(t, r57Do(r, "POST", "/cl/r57a", ""))
	if l2.URL == l.URL {
		t.Fatal("an expired link is not reused")
	}
	for _, p := range []string{"/b/abc", "/b/" + strings.Repeat("0", 32), "/b/r57a"} {
		if w := r57Do(r, "GET", p, ""); w.Code != http.StatusNotFound || strings.Contains(w.Body.String(), "Нет управленческого") {
			t.Fatalf("%s: %d", p, w.Code)
		}
	}
}

func TestR57ScopeOnlyThisBoard(t *testing.T) {
	r, _, _, _, _ := r57Setup(t)
	l := r57Link(t, r57Do(r, "POST", "/cl/r57a", ""))
	tok := r57Token(l.URL)
	for _, p := range []string{"/b/" + tok, "/b/" + tok + "/data"} {
		w := r57Do(r, "GET", p, "")
		b := w.Body.String()
		if w.Code != 200 || !strings.Contains(b, "Нет управленческого учёта") || !strings.Contains(b, "Внедрить ДДС") || !strings.Contains(b, "Кассовый разрыв") {
			t.Fatalf("%s: %d %.300s", p, w.Code, b)
		}
		for _, bad := range []string{"Чужой", "ВНУТРЕННЯЯ", "секрет", "gallup", "history", "author"} {
			if strings.Contains(b, bad) {
				t.Fatalf("%s leaks %q", p, bad)
			}
		}
		h := w.Header()
		if h.Get("Cache-Control") != "private, no-store" || !strings.Contains(h.Get("X-Robots-Tag"), "noindex") || h.Get("Referrer-Policy") != "no-referrer" {
			t.Fatalf("%s headers %v", p, h)
		}
	}
	page := r57Do(r, "GET", "/b/"+tok, "")
	if csp := page.Header().Get("Content-Security-Policy"); !strings.Contains(csp, "script-src 'nonce-") || !strings.Contains(csp, "frame-ancestors 'none'") {
		t.Fatalf("csp %q", csp)
	}
	body := page.Body.String()
	for _, want := range []string{"Ваш разбор · Business Surgery", "Алия Клиентова", "1 500 000 ₸", "500 000 ₸", "Хочу в клуб", "Есть вопрос", "Пока подумаю"} {
		if !strings.Contains(body, want) {
			t.Fatalf("page lacks %q", want)
		}
	}
	if strings.Contains(body, "—") {
		t.Fatal("em dash on the page")
	}
}

func TestR57StatsAndNotifyOnce(t *testing.T) {
	r, h, links, notes, now := r57Setup(t)
	l := r57Link(t, r57Do(r, "POST", "/cl/r57a", ""))
	tok := r57Token(l.URL)
	for i := 0; i < 3; i++ {
		if w := r57Do(r, "POST", "/b/"+tok+"/ev", `{"open":true}`); w.Code != 200 {
			t.Fatalf("open %d", w.Code)
		}
	}
	r57Do(r, "POST", "/b/"+tok+"/ev", `{"s":40,"sec":["diag","plan","evil"],"click":["wa","x"]}`)
	r57Do(r, "POST", "/b/"+tok+"/ev", `{"s":9999,"sec":["diag"]}`)
	if len(*notes) != 1 || !strings.Contains((*notes)[0], "Клиент открыл доску: Алия Клиентова") {
		t.Fatalf("open notes: %q", *notes)
	}
	*now = now.Add(24 * time.Hour)
	r57Do(r, "POST", "/b/"+tok+"/ev", `{"open":true}`)
	if len(*notes) != 2 {
		t.Fatalf("next day: one more note, got %q", *notes)
	}
	for i := 0; i < 2; i++ {
		if w := r57Do(r, "POST", "/b/"+tok+"/intent", `{"intent":"join"}`); w.Code != 200 {
			t.Fatalf("intent %d", w.Code)
		}
	}
	r57Do(r, "POST", "/b/"+tok+"/intent", `{"intent":"think"}`)
	if w := r57Do(r, "POST", "/b/"+tok+"/intent", `{"intent":"hack"}`); w.Code != 400 {
		t.Fatalf("bad intent %d", w.Code)
	}
	joins := 0
	for _, n := range *notes {
		if strings.Contains(n, "Хочу в клуб") {
			joins++
			if !strings.Contains(n, "Алия Клиентова") || !strings.Contains(n, "/b/"+tok) {
				t.Fatalf("join note %q", n)
			}
		}
	}
	if joins != 1 {
		t.Fatalf("join must notify once, got %d: %q", joins, *notes)
	}
	got, _ := links.Get(context.Background(), tok)
	if got.Opens != 4 || got.Seconds != 160 || got.Sections["diag"] != 2 || got.Sections["plan"] != 1 || got.Sections["evil"] != 0 ||
		got.Clicks["wa"] != 1 || got.Clicks["join"] != 2 || got.Clicks["x"] != 0 || got.Intent != "think" || got.LastOpen == nil {
		t.Fatalf("stats %+v", got)
	}
	v := r57Link(t, r57Do(r, "GET", "/cl/r57a", ""))
	if v.Opens != 4 || v.Seconds != 160 {
		t.Fatalf("panel stats %+v", v)
	}
	// the CRM lead: link on the card, «Решение», log
	ld := h.S.leadByID(context.Background(), "L1")
	if ld == nil || sStr(ld, "boardLink") != l.URL || sStr(ld, "col") != "decide" || ld["hot"] != true {
		t.Fatalf("lead %v", ld)
	}
	// the day-2 reminder carries the board link
	if txt := h.S.stepText(context.Background(), h.S.Settings(context.Background()), ld, "d2", *now); !strings.Contains(txt, l.URL) {
		t.Fatalf("d2 text lacks the link: %q", txt)
	}
}
