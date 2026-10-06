package http

import (
	"bytes"
	"compress/gzip"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/andybalholm/brotli"
	"github.com/bnursik/business_surgery_backend/internal/content"
	"github.com/bnursik/business_surgery_backend/internal/middleware"
	"github.com/gin-gonic/gin"
)

type ideasCat struct {
	Version string `json:"version"`
	Total   int    `json:"total"`
	Cats    []struct {
		ID    string   `json:"id"`
		N     string   `json:"n"`
		Count int      `json:"count"`
		Subs  []string `json:"subs"`
	} `json:"cats"`
	Items []content.IdeaItem `json:"items"`
}

// R46: the catalogue: 12 categories in order, every idea complete, no em dash.
func TestR46IdeasCatalogue(t *testing.T) {
	plain, gz, etag, err := content.Ideas()
	if err != nil || len(plain) == 0 || len(gz) == 0 || etag == "" {
		t.Fatalf("ideas: %v", err)
	}
	var c ideasCat
	if err := json.Unmarshal(plain, &c); err != nil {
		t.Fatal(err)
	}
	if len(c.Cats) != 12 || c.Cats[0].ID != "serv" || c.Cats[11].ID != "tour" {
		t.Fatalf("categories: %+v", c.Cats)
	}
	if c.Total != len(c.Items) || c.Total < 12 || content.IdeasTotal() != c.Total {
		t.Fatalf("total %d items %d", c.Total, len(c.Items))
	}
	cats, sum, ids := map[string]bool{}, 0, map[string]bool{}
	for _, x := range c.Cats {
		cats[x.ID] = true
		sum += x.Count
		if x.Count > 0 && len(x.Subs) == 0 {
			t.Fatalf("%s: no subcategories", x.ID)
		}
	}
	if sum != c.Total {
		t.Fatalf("counts %d != %d", sum, c.Total)
	}
	for _, it := range c.Items {
		if !cats[it.Cat] || ids[it.ID] || it.Title == "" || it.Short == "" || len(it.FirstSteps) == 0 || it.Budget[0] > it.Budget[1] || it.Difficulty < 1 || it.Difficulty > 5 {
			t.Fatalf("bad idea %+v", it)
		}
		ids[it.ID] = true
		if content.IdeaByID(it.ID) == nil {
			t.Fatalf("by id %s", it.ID)
		}
	}
	if strings.Contains(string(plain), "—") {
		t.Fatal("em dash in ideas")
	}
	if ideaBudget([2]int64{150000, 1200000}) != "150 000-1 200 000" || ideaBudget([2]int64{5000, 5000}) != "5 000" {
		t.Fatal("budget format: " + ideaBudget([2]int64{150000, 1200000}))
	}
}

func ideasRouter() *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(middleware.Compress())
	r.GET("/i", Ideas)
	return r
}

// R46: brotli (once ready), gzip, plain, and 304 by ETag; the global compressor leaves it alone.
func TestR46IdeasEncodings(t *testing.T) {
	r := ideasRouter()
	plain, _, etag, _ := content.Ideas()
	get := func(ae, inm string) *httptest.ResponseRecorder {
		req := httptest.NewRequest("GET", "/i", nil)
		if ae != "" {
			req.Header.Set("Accept-Encoding", ae)
		}
		if inm != "" {
			req.Header.Set("If-None-Match", inm)
		}
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		return w
	}
	WarmIdeas(true)
	w := get("br, gzip", "")
	if w.Code != 200 || w.Header().Get("Content-Encoding") != "br" || w.Header().Get("ETag") != etag {
		t.Fatalf("br: %d %v", w.Code, w.Header())
	}
	b, err := io.ReadAll(brotli.NewReader(bytes.NewReader(w.Body.Bytes())))
	if err != nil || !bytes.Equal(b, plain) {
		t.Fatalf("br body: %v", err)
	}
	if len(w.Body.Bytes())*3 > len(plain) {
		t.Fatalf("brotli too big: %d of %d", len(w.Body.Bytes()), len(plain))
	}
	w = get("gzip", "")
	zr, err := gzip.NewReader(bytes.NewReader(w.Body.Bytes()))
	if err != nil || w.Header().Get("Content-Encoding") != "gzip" {
		t.Fatalf("gzip: %v %v", err, w.Header())
	}
	b, _ = io.ReadAll(zr)
	if !bytes.Equal(b, plain) {
		t.Fatal("gzip body")
	}
	if w = get("", ""); !bytes.Equal(w.Body.Bytes(), plain) || w.Header().Get("Content-Encoding") != "" {
		t.Fatal("plain")
	}
	if w = get("br", etag); w.Code != http.StatusNotModified || w.Body.Len() != 0 {
		t.Fatalf("304: %d", w.Code)
	}
}

// R46: who opens the catalogue: lead, resident, team; nobody without a token.
// «Обсудить на разборе» / «Взять в работу» of a lead land on its CRM card once.
func TestR46IdeasLeadAccess(t *testing.T) {
	le := newLeadEnv(t)
	if w := le.call("GET", "/api/v1/platform/ideas", "", nil); w.Code != http.StatusUnauthorized {
		t.Fatalf("no token: %d", w.Code)
	}
	for _, role := range []string{"lead", "resident", "admin", "moderator"} {
		w := le.call("GET", "/api/v1/platform/ideas", le.token(t, 777001, role), nil)
		var c ideasCat
		if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &c) != nil || len(c.Cats) != 12 || len(c.Items) == 0 {
			t.Fatalf("%s: %d %s", role, w.Code, w.Body.String()[:min(200, w.Body.Len())])
		}
	}
	// a lead's token still does not open the team's routes
	if w := le.call("GET", "/api/v1/platform/sync", le.token(t, 777001, "lead"), nil); w.Code != http.StatusForbidden {
		t.Fatalf("lead on /sync: %d", w.Code)
	}
	// the lead logs in (lands in the CRM), then marks an idea
	if w := le.call("POST", "/api/v1/platform/auth/telegram", "", map[string]any{"widget": widgetFor(777001, "Айгерим", "aigerim")}); w.Code != 200 {
		t.Fatalf("login: %d %s", w.Code, w.Body.String())
	}
	_, _, _, _ = content.Ideas()
	var first content.IdeaItem
	{
		plain, _, _, _ := content.Ideas()
		var c ideasCat
		_ = json.Unmarshal(plain, &c)
		first = c.Items[0]
	}
	lt := le.token(t, 777001, "lead")
	for i := 0; i < 2; i++ { // the second time changes nothing
		if w := le.call("POST", "/api/v1/platform/lead/idea", lt, map[string]any{"id": first.ID, "kind": "discuss"}); w.Code != 200 {
			t.Fatalf("idea: %d %s", w.Code, w.Body.String())
		}
	}
	if w := le.call("POST", "/api/v1/platform/lead/idea", lt, map[string]any{"id": first.ID, "kind": "work"}); w.Code != 200 {
		t.Fatalf("work: %d", w.Code)
	}
	card := crmCard(t, le.repo, 777001)
	ideas, _ := card["ideas"].([]any)
	if len(ideas) != 2 {
		t.Fatalf("ideas on the card: %v", card["ideas"])
	}
	logs, _ := json.Marshal(card["log"])
	if strings.Count(string(logs), first.Title) != 2 || !strings.Contains(string(logs), "обсудить на разборе") {
		t.Fatalf("log: %s", logs)
	}
	if w := le.call("POST", "/api/v1/platform/lead/idea", lt, map[string]any{"id": "idea_nope_001"}); w.Code != http.StatusNotFound {
		t.Fatalf("unknown idea: %d", w.Code)
	}
	// the team in the «Лид» preview: nothing is written
	if w := le.call("POST", "/api/v1/platform/lead/idea", le.token(t, 453800951, "admin"), map[string]any{"id": first.ID}); w.Code != http.StatusForbidden {
		t.Fatalf("preview: %d", w.Code)
	}
}

// R46: the Telegram app gets the same catalogue by initData; no initData, no catalogue.
func TestR46AppIdeas(t *testing.T) {
	_, _, r, now := newGateway(t)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest("GET", "/api/v1/app/ideas", nil))
	if w.Code == 200 {
		t.Fatal("ideas without initData")
	}
	q := "?_tg=" + url.QueryEscape(makeInitData(testBotToken, 777, "Айдар", *now))
	w = httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest("GET", "/api/v1/app/ideas"+q, nil))
	var c ideasCat
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &c) != nil || len(c.Cats) != 12 || len(c.Items) == 0 {
		t.Fatalf("app ideas: %d", w.Code)
	}
	_, _, etag, _ := content.Ideas()
	w = httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest("GET", "/api/v1/app/ideas"+q+"&_et="+url.QueryEscape(strings.Trim(etag, `"`)), nil))
	if w.Code != http.StatusNotModified {
		t.Fatalf("app 304: %d", w.Code)
	}
	w = httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest("POST", "/api/v1/app/idea"+q, strings.NewReader(`{"id":"`+c.Items[0].ID+`","kind":"discuss"}`)))
	if w.Code != 200 {
		t.Fatalf("app idea: %d %s", w.Code, w.Body.String())
	}
	w = httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest("POST", "/api/v1/app/idea"+q, strings.NewReader(`{"id":"idea_x_000"}`)))
	if w.Code != http.StatusNotFound {
		t.Fatalf("app unknown idea: %d", w.Code)
	}
}
