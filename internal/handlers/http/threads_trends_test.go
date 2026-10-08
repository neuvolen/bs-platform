package http

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/bnursik/business_surgery_backend/internal/ai"
	"github.com/bnursik/business_surgery_backend/pkg/auth"
	"github.com/gin-gonic/gin"
)

// fakeThreads: graph.threads.com/oembed and www.threads.com post pages in one
// test server; the fetcher's client is pointed at it (the sandbox cannot reach
// Threads). The owner's example DcObaXSjPe_ is served the way Threads answers.
type fakeThreads struct {
	srv   *httptest.Server
	calls atomic.Int32
	wall  bool // the page answers with a login wall
}

const fakeOEmbedHTML = `<blockquote class="text-post-media" data-text-post-permalink="https://www.threads.com/@my.twinkles/post/DcObaXSjPe_" data-text-post-version="0" id="ig-tp-DcObaXSjPe_"><a href="https://www.threads.com/@my.twinkles/post/DcObaXSjPe_" target="_blank"><div><svg><path d="x"/></svg></div><div>Post by @my.twinkles</div><div>View on Threads</div></a></blockquote><script async src="https://www.threads.com/embed.js"></script>`

const fakePostText = "Почему твои менеджеры не продают?\n\nПотому что ты нанял их и ушёл.\nБез скрипта, без плана, без разбора звонков.\n\nА у тебя кто слушает звонки?"

func newFakeThreads(t *testing.T) *fakeThreads {
	f := &fakeThreads{}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.calls.Add(1)
		if r.Header.Get("User-Agent") != thFetchUA {
			w.WriteHeader(400)
			return
		}
		switch {
		case r.URL.Path == "/oembed":
			u := r.URL.Query().Get("url")
			ref, ok := ParseThreadsURL(u)
			if !ok || strings.HasPrefix(ref.Code, "Gone") {
				w.WriteHeader(404)
				_, _ = w.Write([]byte(`{"error":{"message":"Post not found"}}`))
				return
			}
			h := strings.ReplaceAll(fakeOEmbedHTML, "DcObaXSjPe_", ref.Code)
			if ref.User != "" {
				h = strings.ReplaceAll(h, "my.twinkles", ref.User)
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"version": "1.0", "type": "rich", "provider_name": "Threads", "provider_url": "https://www.threads.com/", "width": 658, "html": h})
		case r.URL.Path == "/share/BBr-UwA_Ka/":
			http.Redirect(w, r, "https://www.threads.com/@my.twinkles/post/DcObaXSjPe_?xmt=abc", http.StatusFound)
		case r.URL.Path == "/share/OgOnly1/":
			_, _ = w.Write([]byte(`<html><head><meta property="og:url" content="https://www.threads.com/@a.b/post/XYZ123abc_"></head></html>`))
		case strings.HasPrefix(r.URL.Path, "/share/"):
			_, _ = w.Write([]byte(`<html><head><title>Threads</title></head></html>`))
		case strings.Contains(r.URL.Path, "/post/"):
			if strings.Contains(r.URL.Path, "Gone") {
				w.WriteHeader(404)
				return
			}
			if f.wall {
				_, _ = w.Write([]byte(`<html><head><meta property="og:description" content="Join Threads to share ideas, ask questions, post random thoughts and more."></head></html>`))
				return
			}
			user := strings.TrimPrefix(strings.Split(r.URL.Path, "/")[1], "@")
			txt := strings.ReplaceAll(fakePostText, "\n", "&#x0a;")
			_, _ = fmt.Fprintf(w, `<html><head><meta property="og:title" content="Twinkles (@%s) on Threads"><meta property="og:description" content="%s"></head>`+
				`<body><script type="application/json">{"post":{"like_count":1840,"text_post_app_info":{"direct_reply_count":312,"repost_count":95},"taken_at":1790000000}}</script></body></html>`, user, txt)
		default:
			w.WriteHeader(404)
		}
	}))
	t.Cleanup(f.srv.Close)
	return f
}

// to: every request goes to the fake server, whatever the host.
type toFake struct{ base *url.URL }

func (tf toFake) RoundTrip(r *http.Request) (*http.Response, error) {
	r2 := r.Clone(r.Context())
	r2.URL.Scheme, r2.URL.Host = tf.base.Scheme, tf.base.Host
	r2.Host = tf.base.Host
	return http.DefaultTransport.RoundTrip(r2)
}

func (f *fakeThreads) fetcher() *ThreadsFetcher {
	u, _ := url.Parse(f.srv.URL)
	tf := NewThreadsFetcher()
	tf.HTTP = &http.Client{Transport: toFake{u}, Timeout: 5 * time.Second}
	tf.Gap = 5 * time.Millisecond
	tf.PageOff = false
	return tf
}

func TestParseThreadsURL(t *testing.T) {
	cases := map[string]string{
		thExampleLink: "https://www.threads.com/@my.twinkles/post/DcObaXSjPe_",
		"https://www.threads.com/@my.twinkles/post/DcObaXSjPe_?xmt=AQF0abc&slof=1": "https://www.threads.com/@my.twinkles/post/DcObaXSjPe_",
		"смотри threads.net/@biz.kz/post/C9xYz-12AbC это огонь":                    "https://www.threads.com/@biz.kz/post/C9xYz-12AbC",
		"https://threads.com/t/DAbcdEF1234":                                        "https://www.threads.com/t/DAbcdEF1234",
	}
	for in, want := range cases {
		r, ok := ParseThreadsURL(in)
		if !ok || r.URL != want {
			t.Fatalf("%q → %+v %v", in, r, ok)
		}
	}
	for _, bad := range []string{"https://instagram.com/p/abc", "https://www.threads.com/@user", "t.me/bsurgery_bot?start=x"} {
		if _, ok := ParseThreadsURL(bad); ok {
			t.Fatalf("accepted %q", bad)
		}
	}
	refs := ThreadsLinks("1) " + thExampleLink + "\n2) https://www.threads.net/@a.b/post/XYZ123abc_\n3) " + thExampleLink + "?x=1")
	if len(refs) != 2 || refs[0].Code != "DcObaXSjPe_" || refs[1].User != "a.b" {
		t.Fatalf("links: %+v", refs)
	}
}

// The owner's example through the server's fetcher: oEmbed knows the post
// (author, no text), the public page gives the text and the counts. In
// production graph.threads.com and www.threads.com answer the same way, or
// the page is closed: then the post keeps oEmbed's author and asks for the
// text by hand (the second half of the test).
func TestThreadsFetchOwnerExample(t *testing.T) {
	f := newFakeThreads(t)
	tf := f.fetcher()
	tf.OEmbedURL = "https://graph.threads.com/oembed"
	ref, _ := ParseThreadsURL(thExampleLink)
	ctx := context.Background()
	info, err := tf.Fetch(ctx, ref)
	if err != nil {
		t.Fatal(err)
	}
	if !info.Exists || info.Via != "oembed+page" || info.Author != "my.twinkles" || info.Name != "Twinkles" || info.Text != fakePostText ||
		info.Likes != 1840 || info.Replies != 312 || info.Reposts != 95 || info.PostedAt == "" {
		t.Fatalf("info: %+v", info)
	}
	n := f.calls.Load()
	if _, err := tf.Fetch(ctx, ref); err != nil || f.calls.Load() != n {
		t.Fatalf("cache: %v %d→%d", err, n, f.calls.Load())
	}
	// oEmbed alone: the author, no text
	oe, err := tf.OEmbed(ctx, ref)
	if err != nil || oe.Author != "my.twinkles" || oe.Text != "" {
		t.Fatalf("oembed: %+v %v", oe, err)
	}
	// a login wall: the post exists, the text is asked for
	f.wall = true
	tf2 := f.fetcher()
	info, err = tf2.Fetch(ctx, ref)
	if err != nil || !info.Exists || info.Text != "" || !strings.Contains(info.Note, "вставьте его вручную") {
		t.Fatalf("wall: %+v %v", info, err)
	}
	// a removed post
	gone, _ := ParseThreadsURL("https://www.threads.com/@x/post/GoneAbc123")
	if _, err := tf2.Fetch(ctx, gone); !errors.Is(err, ErrThreadsNotFound) {
		t.Fatalf("gone: %v", err)
	}
	// no network at all (the sandbox): a clear error, the text by hand
	tf3 := NewThreadsFetcher()
	tf3.HTTP = &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) { return nil, errors.New("connect: refused") })}
	tf3.Gap = time.Millisecond
	if _, err := tf3.Fetch(ctx, ref); err == nil || !strings.Contains(err.Error(), "вставьте текст поста вручную") {
		t.Fatalf("offline: %v", err)
	}
	// the gap between requests
	tf4 := f.fetcher()
	tf4.Gap = 120 * time.Millisecond
	t0 := time.Now()
	_, _ = tf4.OEmbed(ctx, ref)
	_, _ = tf4.OEmbed(ctx, ref)
	if time.Since(t0) < 120*time.Millisecond {
		t.Fatalf("no gap: %v", time.Since(t0))
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestAnalyzeTrend(t *testing.T) {
	cases := []struct {
		text, hook, cta, emotion, topic string
	}{
		{fakePostText, "question", "question", "", "Продажи"},
		{"5 ошибок при найме, которые стоили мне 3 млн ₸:\n1. Нанимал друзей\n2. Не давал испытательный срок\n3. Платил без KPI\n4. Не делал онбординг\n5. Терпел до последнего\n\nСохрани, пригодится", "list", "save", "", "Команда"},
		{"Хватит считать выручку.\nСчитай деньги, которые остаются.\nПрибыль, а не оборот.", "provocation", "", "", "Финансы"},
		{"Признаюсь: я 3 года не платил себе зарплату.\nДумал, что так правильно.\nОказалось, это убивало бизнес.", "confession", "", "", ""},
		{"Вчера ко мне пришёл собственник кофейни из Алматы.\nВыручка 12 млн ₸, а денег нет.\nМы открыли Kaspi выписку и нашли дыру.", "story", "", "", ""},
	}
	for i, c := range cases {
		a := AnalyzeTrend(&TrendPost{Text: c.text, Likes: 100, Replies: 30, Followers: 1000})
		if a == nil || a.Hook != c.hook || a.CTA != c.cta || (c.topic != "" && a.Topic != c.topic) || len(a.Why) == 0 || a.Pattern == "" || a.HookName == "" {
			t.Fatalf("%d: %+v", i, a)
		}
		if a.ER != 13 || a.Score != 160 {
			t.Fatalf("%d: er %v score %d", i, a.ER, a.Score)
		}
	}
	a := AnalyzeTrend(&TrendPost{Text: cases[4].text})
	if !a.Local || a.Lines != 3 || a.Paras != 1 {
		t.Fatalf("local: %+v", a)
	}
	if AnalyzeTrend(&TrendPost{}) != nil {
		t.Fatal("empty analysed")
	}
	if strings.ContainsAny(strings.Join(a.Why, " ")+a.Pattern, "—–") {
		t.Fatal("long dash in the analysis")
	}
}

func TestTrendTemplatesPassGate(t *testing.T) {
	pool := threadsSeedPool()
	if len(pool) < 100 {
		t.Fatalf("pool %d", len(pool))
	}
	tr := &Trends{Seeds: threadsSeedPool}
	for _, hook := range []string{"question", "number", "provocation", "story", "list", "confession", "statement"} {
		p := &TrendPost{ID: "X" + hook, Text: fakePostText}
		analyzeInto(p)
		p.An.Hook = hook
		seeds := tr.trendSeeds(p, 6)
		if len(seeds) != 6 {
			t.Fatalf("seeds %d", len(seeds))
		}
		ok := 0
		for i, s := range seeds {
			text, _ := trendTemplate(p.An, s, i)
			if clean, probs := trendGate(p.Text, text); len(probs) == 0 && clean != "" {
				ok++
			} else {
				t.Logf("%s: %v\n%s", hook, probs, clean)
			}
		}
		if ok < 3 {
			t.Fatalf("%s: only %d of 6 templates pass", hook, ok)
		}
	}
	if c := copiedPhrase(fakePostText, "Слушай: потому что ты нанял их и ушёл, вот и всё"); c != "потому что ты нанял" {
		t.Fatalf("copy: %q", c)
	}
}

func trendsTestEnv(t *testing.T) (*Trends, *ContentEngine, *cntDocs, *fakeThreads, *time.Time) {
	gin.SetMode(gin.TestMode)
	docs := &cntDocs{}
	now := time.Date(2026, 10, 5, 8, 0, 0, 0, almaty) // Monday
	e := cntEngine(docs, &now)
	e.ThreadsManual = func(context.Context) bool { return true }
	f := newFakeThreads(t)
	tr := NewTrends(docs, []byte("cnt-secret"))
	tr.Fetch = f.fetcher()
	tr.Content = e
	tr.now = func() time.Time { return now }
	tr.Go = func(fn func()) { fn() }
	tr.PlatformURL = "https://app.test/platform"
	return tr, e, docs, f, &now
}

func TestTrendsFlow(t *testing.T) {
	tr, _, docs, _, now := trendsTestEnv(t)
	var aiCalls int
	tr.AI = func(ctx context.Context, system, prompt string) (string, error) {
		aiCalls++
		if !strings.Contains(system, "Нельзя копировать источник") || !strings.Contains(prompt, "Источник") || !strings.Contains(prompt, "Наш материал") {
			t.Fatalf("prompt: %s", prompt)
		}
		return `{"analysis":{"why":["Вопрос бьёт в боль собственника","Короткие строки"],"pattern":"Вопрос · боль · вопрос в конце"},"variants":[` +
			`{"text":"Почему касса пустая при хорошей выручке?\n\nПотому что платежи никто не расписал по датам. Выпиши все обязательные платежи на 3 месяца вперёд: зарплата, аренда, налоги, лизинг.\n\nА у тебя есть такой календарь?","seed":1},` +
			`{"text":"Почему твои менеджеры не продают? Потому что ты нанял их и ушёл, вот и всё.","seed":2},` +
			`{"text":"Деньги — это кровь бизнеса. Без календаря платежей ты слепой.","seed":3}]}`, nil
	}
	r := gin.New()
	tr.Register(r)
	acc, _, _ := auth.NewManager("cnt-secret", time.Hour, time.Hour).GenerateTokens("tg:453800951", "admin", nil)
	res, _, _ := auth.NewManager("cnt-secret", time.Hour, time.Hour).GenerateTokens("tg:490685605", "resident", nil)
	do := func(method, path, tk string, body any) (int, map[string]any) {
		b, _ := json.Marshal(body)
		req := httptest.NewRequest(method, path, bytes.NewReader(b))
		req.Header.Set("Authorization", "Bearer "+tk)
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		var out map[string]any
		_ = json.Unmarshal(w.Body.Bytes(), &out)
		return w.Code, out
	}
	if c, _ := do("GET", "/api/v1/platform/trends", res, nil); c != http.StatusForbidden {
		t.Fatalf("resident: %d", c)
	}
	c, out := do("GET", "/api/v1/platform/trends", acc, nil)
	if c != 200 || len(out["posts"].([]any)) != 0 || out["canSearch"] != false || out["ai"] != true || len(out["queries"].([]any)) != len(trendsDefaultQueries) {
		t.Fatalf("empty: %d %v", c, out)
	}
	if c, out = do("POST", "/api/v1/platform/trends/add", acc, map[string]any{"links": "просто текст"}); c != 400 {
		t.Fatalf("no links: %d %v", c, out)
	}
	// the owner's example and one more: both read at once
	c, out = do("POST", "/api/v1/platform/trends/add", acc, map[string]any{"links": thExampleLink + "?xmt=1\nhttps://www.threads.net/@biz.almaty/post/C1abcDEF234"})
	if c != 200 || out["added"] != 2.0 || out["failed"] != 0.0 {
		t.Fatalf("add: %d %v", c, out)
	}
	d, _, _ := tr.load(context.Background())
	p := d.find("DcObaXSjPe_")
	if p == nil || p.Status != "inbox" || p.Text != fakePostText || p.Likes != 1840 || p.Via != "oembed+page" || p.An == nil || p.An.Hook != "question" || p.Source != "link" || p.AddedBy != "tg:453800951" {
		t.Fatalf("post: %+v", p)
	}
	if d.Posts[0].ID != "C1abcDEF234" || d.Posts[0].Author != "biz.almaty" {
		t.Fatalf("order: %+v", d.Posts[0])
	}
	// a text by hand wins over a later fetch; followers give the ER
	c, out = do("PUT", "/api/v1/platform/trends/C1abcDEF234", acc, map[string]any{"text": "Хватит нанимать «звёзд».\nНанимай тех, кто учится.\nЗвезда уходит через полгода.", "followers": 20000})
	if c != 200 {
		t.Fatalf("edit: %d %v", c, out)
	}
	c, _ = do("POST", "/api/v1/platform/trends/C1abcDEF234/fetch", acc, nil)
	d, _, _ = tr.load(context.Background())
	p2 := d.find("C1abcDEF234")
	if c != 200 || p2.Via != "manual" || !strings.HasPrefix(p2.Text, "Хватит") || p2.An.Hook != "provocation" || p2.An.ER == 0 {
		t.Fatalf("manual: %d %+v %+v", c, p2, p2.An)
	}
	// the summary of the week
	c, out = do("GET", "/api/v1/platform/trends", acc, nil)
	sum := out["summary"].(map[string]any)
	if sum["posts"] != 2.0 || sum["new"] != 2.0 || sum["best"] == "" || len(sum["tips"].([]any)) < 3 {
		t.Fatalf("summary: %v", sum)
	}
	// three variants: one of the AI's passes, the copy and the dash do not; templates fill up
	c, out = do("POST", "/api/v1/platform/trends/DcObaXSjPe_/make", acc, nil)
	if c != 200 || out["ok"] != true || aiCalls != 1 {
		t.Fatalf("make: %d %v", c, out)
	}
	d, _, _ = tr.load(context.Background())
	p = d.find("DcObaXSjPe_")
	if len(p.Vars) != 3 || p.Vars[0].By != "ai" || p.Vars[1].By != "rules" || p.An.By != "ai" || p.An.Why[0] != "Вопрос бьёт в боль собственника" || !strings.Contains(p.MadeNote, "по шаблону") {
		t.Fatalf("vars: %+v %+v", p.Vars, p.An)
	}
	for _, v := range p.Vars {
		if _, probs := trendGate(p.Text, v.Text); len(probs) > 0 || copiedPhrase(p.Text, v.Text) != "" || strings.ContainsAny(v.Text, "—–") {
			t.Fatalf("variant fails: %v %q", probs, v.Text)
		}
	}
	// a bad text is not planned
	if c, out = do("POST", "/api/v1/platform/trends/DcObaXSjPe_/plan", acc, map[string]any{"text": "Почему твои менеджеры не продают? Потому что ты нанял их и ушёл. Короче вот."}); c != 400 || !strings.Contains(out["error"].(string), "повтор фразы источника") {
		t.Fatalf("plan bad: %d %v", c, out)
	}
	// «В план SMM»: the nearest free manual slot today (08:00 now: 09:30±12)
	v0 := 0
	c, out = do("POST", "/api/v1/platform/trends/DcObaXSjPe_/plan", acc, map[string]any{"v": v0})
	if c != 200 || out["ok"] != true {
		t.Fatalf("plan: %d %v", c, out)
	}
	cd := docs.doc(t)
	var it *contentItem
	for _, x := range cd.Queue {
		if x.Gen == "trend" {
			it = x
		}
	}
	if it == nil || it.Channel != "threads" || it.Status != "planned" || !it.Edited || it.Auto || !strings.HasPrefix(it.At, "2026-10-05T09:") ||
		!strings.Contains(string(it.extra["trend"]), `"pattern":"Вопрос · боль · вопрос в конце"`) || !strings.Contains(it.Text, "t.me/bsurgery_bot?start=th_p261005") || it.Title != "По мотивам @my.twinkles" {
		t.Fatalf("item: %+v %s", it, it.extra["trend"])
	}
	d, _, _ = tr.load(context.Background())
	p = d.find("DcObaXSjPe_")
	if p.Status != "done" || p.Vars[0].Planned != it.ID {
		t.Fatalf("planned mark: %+v", p)
	}
	// the next one goes to the next free slot, and the day's batch keeps both
	c, out = do("POST", "/api/v1/platform/trends/DcObaXSjPe_/plan", acc, map[string]any{"v": 1})
	cd = docs.doc(t)
	n := 0
	for _, x := range cd.Queue {
		if x.Gen == "trend" {
			n++
			if x.ID != it.ID && !strings.HasPrefix(x.At, "2026-10-05T12:") {
				t.Fatalf("second slot: %s", x.At)
			}
		}
	}
	if c != 200 || n != 2 {
		t.Fatalf("second: %d %d %v", c, n, out)
	}
	b, err := tr.Content.BuildThreadsDay(context.Background(), *now, false)
	if err != nil {
		t.Fatal(err)
	}
	if b.Slots != 2 {
		t.Fatalf("batch slots around ours: %+v", b)
	}
	// make again: the planned variants stay, 3 new ones come
	tr.AI = nil
	if c, out = do("POST", "/api/v1/platform/trends/DcObaXSjPe_/make", acc, nil); c != 200 {
		t.Fatalf("remake: %d %v", c, out)
	}
	d, _, _ = tr.load(context.Background())
	p = d.find("DcObaXSjPe_")
	if len(p.Vars) != 5 || p.Vars[0].Planned == "" || p.Vars[1].Planned == "" || p.Vars[2].By != "rules" || !strings.Contains(p.MadeNote, "ИИ не подключён") {
		t.Fatalf("remake vars: %+v", p.Vars)
	}
	// search without a model: a clear answer
	c, out = do("POST", "/api/v1/platform/trends/search", acc, nil)
	if c != 200 || out["ok"] != false || !strings.Contains(out["error"].(string), "вставьте ссылки") {
		t.Fatalf("no search: %v", out)
	}
	// queries
	c, out = do("PUT", "/api/v1/platform/trends/queries", acc, map[string]any{"queries": []string{" найм ", "найм", "кассовый разрыв", ""}})
	if c != 200 || len(out["queries"].([]any)) != 2 {
		t.Fatalf("queries: %v", out)
	}
	// delete
	if c, out = do("DELETE", "/api/v1/platform/trends/C1abcDEF234", acc, nil); c != 200 || len(out["posts"].([]any)) != 1 {
		t.Fatalf("delete: %d", c)
	}
}

func TestTrendsSearch(t *testing.T) {
	tr, _, _, _, _ := trendsTestEnv(t)
	var prompt string
	tr.Search = func(ctx context.Context, p string) (string, error) {
		prompt = p
		return "Нашёл:\n```json\n" + `{"items":[` +
			`{"url":"https://www.threads.com/@owner.kz/post/DReal12345a","author":"owner.kz","snippet":"Про найм — без иллюзий","likes":900,"replies":120,"query":"найм","why":"боль найма"},` +
			`{"url":"https://www.threads.com/@ghost/post/GoneFake999","author":"ghost","snippet":"выдумка","likes":5},` +
			`{"url":"https://example.com/x","snippet":"не threads"},` +
			`{"url":"https://www.threads.com/@bsurgery/post/DOurOwn1234","snippet":"наш"}]}` + "\n```", nil
	}
	ctx := context.Background()
	if _, err := tr.update(ctx, func(d *trendsDoc) bool { d.Queries = []string{"найм", "Алматы бизнес"}; return true }); err != nil {
		t.Fatal(err)
	}
	n, err := tr.SearchTrends(ctx, nil)
	if err != nil || n != 1 {
		t.Fatalf("search: %d %v", n, err)
	}
	if !strings.Contains(prompt, "«найм», «Алматы бизнес»") || !strings.Contains(prompt, "Не выдумывай ссылки") {
		t.Fatalf("prompt: %s", prompt)
	}
	d, _, _ := tr.load(ctx)
	if len(d.Posts) != 1 {
		t.Fatalf("posts: %+v", d.Posts)
	}
	p := d.Posts[0]
	if p.Status != "cand" || !p.Verified || p.Source != "search" || p.Query != "найм" || p.Likes != 900 || strings.Contains(p.Snippet, "—") || p.Text != "" {
		t.Fatalf("cand: %+v", p)
	}
	// a candidate taken into the inbox is read
	if err := tr.FetchOne(ctx, ThreadsRef{URL: p.URL, User: p.Author, Code: p.ID}); err != nil {
		t.Fatal(err)
	}
	d, _, _ = tr.load(ctx)
	if d.Posts[0].Text == "" || d.Posts[0].An == nil {
		t.Fatalf("read cand: %+v", d.Posts[0])
	}
	// a model that cannot search
	tr.Search = func(ctx context.Context, p string) (string, error) { return "", ai.ErrNoSearch }
	if _, err := tr.SearchTrends(ctx, nil); err == nil || err.Error() != ErrTrendsNoSearch.Error() {
		t.Fatalf("no search: %v", err)
	}
	d, _, _ = tr.load(ctx)
	if d.SearchErr != ErrTrendsNoSearch.Error() {
		t.Fatalf("search error kept: %q", d.SearchErr)
	}
}

func TestTrendsBotAndWeekly(t *testing.T) {
	tr, _, _, _, now := trendsTestEnv(t)
	ctx := context.Background()
	if _, ok := tr.BotLinks(ctx, 453800951, "привет, как дела"); ok {
		t.Fatal("no links taken")
	}
	reply, ok := tr.BotLinks(ctx, 453800951, "глянь "+thExampleLink+" и https://www.threads.com/@x.y/post/DzzzYYY1234")
	if !ok || !strings.Contains(reply, "2 ссылки") || strings.ContainsAny(reply, "—–") {
		t.Fatalf("bot: %v %q", ok, reply)
	}
	d, _, _ := tr.load(ctx)
	if len(d.Posts) != 2 || d.Posts[0].Source != "bot" || d.find("DcObaXSjPe_").Text == "" {
		t.Fatalf("bot posts: %+v", d.Posts)
	}
	line := tr.WeeklyLine(ctx, now.AddDate(0, 0, -7), now.Add(time.Hour))
	if !strings.HasPrefix(line, "🧵 Тренды Threads: 2 новых поста за неделю, чаще всего крючок «вопрос»") || !strings.HasSuffix(line, "https://app.test/platform?section=mTrends") {
		t.Fatalf("weekly: %q", line)
	}
	if l := tr.WeeklyLine(ctx, now.AddDate(0, 0, -14), now.AddDate(0, 0, -7)); l != "" {
		t.Fatalf("old week: %q", l)
	}
	// the Monday report carries the line
	WeeklyExtra = append(WeeklyExtra, tr.WeeklyLine)
	defer func() { WeeklyExtra = nil }()
	s := NewClubSales(&cntDocs{}, nil)
	text, _ := s.WeeklyReport(ctx, now.AddDate(0, 0, 1))
	if !strings.Contains(text, "🧵 Тренды Threads: 2") {
		t.Fatalf("report: %s", text)
	}
}
