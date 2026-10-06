package http

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/bnursik/business_surgery_backend/internal/ai"
	"github.com/bnursik/business_surgery_backend/internal/middleware"
	"github.com/gin-gonic/gin"
)

// R53: «Тренды Threads» (Маркетинг → Тренды Threads). Залетевшие посты рынка
// на нашу тему (бизнес, собственники, Казахстан и СНГ), не наши:
//
//  1. Сбор: ссылки (одна или пачка) → сервер читает пост (threads_fetch.go),
//     не вышло: текст вставляют руками; «Подобрать залетевшие»: поиск ИИ по
//     запросам из настройки, кандидаты ждут проверки; ссылка, присланная
//     владельцем боту, попадает во «Входящие».
//  2. Разбор каждого поста правилами (и ИИ при генерации): тип крючка,
//     строки, длина, абзацы, призыв, эмоция, тема, почему залетел,
//     вовлечённость, если известны лайки. Сводка недели: что повторяется.
//  3. «Сделать наш пост по мотивам»: 3 варианта голосом BS, только приём
//     источника, примеры и цифры из нашей библиотеки, проверка качества как у
//     генератора Threads и проверка на повтор фраз источника. «В план SMM»
//     ставит пост в ближайшее свободное окно ручного Threads с пометкой,
//     по мотивам какого приёма он сделан.
//  4. В понедельничном отчёте строка про новые тренды и ссылка.
//
// Хранение: документ club/bs_trends (до 300 постов).
//
//	GET    /api/v1/platform/trends               {posts, summary, queries, canSearch, ai, pageFetch}
//	POST   /api/v1/platform/trends/add           {links, text?, author?, likes?, replies?, reposts?, followers?}
//	PUT    /api/v1/platform/trends/queries       {queries: [...]}
//	POST   /api/v1/platform/trends/search        {queries?: [...]} кандидаты от поиска ИИ
//	PUT    /api/v1/platform/trends/:id           {text?, author?, likes?, replies?, reposts?, followers?, status?}
//	POST   /api/v1/platform/trends/:id/fetch     прочитать пост ещё раз
//	POST   /api/v1/platform/trends/:id/make      3 варианта нашего поста
//	POST   /api/v1/platform/trends/:id/plan      {text, v?} в план SMM
//	DELETE /api/v1/platform/trends/:id

const (
	trendsKey     = "bs_trends"
	trendsMax     = 300
	trendsInline  = 3 // links read while the request waits; the rest in the background
	trendsAddMax  = 20
	trendsVarN    = 3
	trendsSearchN = 12
)

var trendsDefaultQueries = []string{"бизнес", "предприниматель", "собственник бизнеса", "продажи", "найм", "финансы бизнеса", "Алматы бизнес", "бизнес Казахстан"}

// ── The doc ──

type TrendAnalysis struct {
	Hook     string   `json:"hook"`     // question, number, provocation, story, list, confession, statement
	HookName string   `json:"hookName"` // по-русски
	First    string   `json:"first"`    // the first line (for the card)
	Lines    int      `json:"lines"`
	Chars    int      `json:"chars"`
	Paras    int      `json:"paras"`
	ListN    int      `json:"listN,omitempty"`
	CTA      string   `json:"cta,omitempty"` // question, save, comment, follow, link
	Emotion  string   `json:"emotion"`
	Topic    string   `json:"topic"`
	Local    bool     `json:"local,omitempty"` // Kazakhstan / CIS context
	Why      []string `json:"why"`
	Pattern  string   `json:"pattern"`
	ER       float64  `json:"er,omitempty"` // % of followers, when known
	Score    int      `json:"score,omitempty"`
	By       string   `json:"by"` // rules, ai
}

type TrendVariant struct {
	Text      string   `json:"text"`
	Idea      string   `json:"idea,omitempty"` // the library material it stands on
	Problems  []string `json:"problems,omitempty"`
	By        string   `json:"by"` // ai, rules
	Planned   string   `json:"planned,omitempty"`
	PlannedAt string   `json:"plannedAt,omitempty"`
}

type TrendPost struct {
	ID        string         `json:"id"` // the post's code
	URL       string         `json:"url"`
	Author    string         `json:"author,omitempty"`
	Name      string         `json:"name,omitempty"`
	Text      string         `json:"text,omitempty"`
	Snippet   string         `json:"snippet,omitempty"` // the search's retelling (a candidate)
	PostedAt  string         `json:"postedAt,omitempty"`
	Likes     int            `json:"likes,omitempty"`
	Replies   int            `json:"replies,omitempty"`
	Reposts   int            `json:"reposts,omitempty"`
	Followers int            `json:"followers,omitempty"`
	Source    string         `json:"source"`          // link, bot, search
	Query     string         `json:"query,omitempty"` // the search query
	Status    string         `json:"status"`          // cand, inbox, done, hidden
	AddedAt   string         `json:"addedAt"`
	AddedBy   string         `json:"addedBy,omitempty"`
	Via       string         `json:"via,omitempty"` // oembed, page, manual
	Note      string         `json:"note,omitempty"`
	FetchErr  string         `json:"fetchErr,omitempty"`
	FetchedAt string         `json:"fetchedAt,omitempty"`
	Verified  bool           `json:"verified,omitempty"` // a candidate oEmbed knows
	Why       string         `json:"why,omitempty"`      // the search's guess why it spread
	An        *TrendAnalysis `json:"an,omitempty"`
	Vars      []TrendVariant `json:"vars,omitempty"`
	MadeAt    string         `json:"madeAt,omitempty"`
	MadeN     int            `json:"madeN,omitempty"`
	MadeNote  string         `json:"madeNote,omitempty"`
}

type trendsDoc struct {
	Posts     []*TrendPost `json:"posts"`
	Queries   []string     `json:"queries,omitempty"`
	SearchAt  string       `json:"searchAt,omitempty"`
	SearchErr string       `json:"searchErr,omitempty"`
}

func (d *trendsDoc) find(id string) *TrendPost {
	for _, p := range d.Posts {
		if p.ID == id {
			return p
		}
	}
	return nil
}

func (d *trendsDoc) queries() []string {
	if len(d.Queries) == 0 {
		return append([]string{}, trendsDefaultQueries...)
	}
	return d.Queries
}

// trim keeps trendsMax posts: hidden ones go first, then the oldest.
func (d *trendsDoc) trim() {
	if len(d.Posts) <= trendsMax {
		return
	}
	sort.SliceStable(d.Posts, func(i, j int) bool {
		hi, hj := d.Posts[i].Status == "hidden", d.Posts[j].Status == "hidden"
		if hi != hj {
			return !hi
		}
		return d.Posts[i].AddedAt > d.Posts[j].AddedAt
	})
	d.Posts = d.Posts[:trendsMax]
}

// ── The module ──

type Trends struct {
	docs  funnelDocs
	Fetch *ThreadsFetcher
	// AI writes the variants and refines the analysis (nil: rules and templates).
	AI func(ctx context.Context, system, prompt string) (string, error)
	// Search: a model with web search (ai.Client.Search); nil: no «Подобрать».
	Search func(ctx context.Context, prompt string) (string, error)
	// Content: the content engine «В план SMM» adds to.
	Content *ContentEngine
	// Seeds: the library's material for our posts (threadsSeedPool).
	Seeds       func() []*threadsSeed
	PlatformURL string
	Go          func(func())
	now         func() time.Time
	secret      []byte
	searching   sync.Mutex
}

func NewTrends(docs funnelDocs, secret []byte) *Trends {
	return &Trends{docs: docs, Fetch: NewThreadsFetcher(), Seeds: threadsSeedPool, PlatformURL: ContentPlatformURL(),
		Go: func(f func()) { go f() }, now: time.Now, secret: secret}
}

func (m *Trends) Register(r *gin.Engine) {
	g := r.Group("/api/v1/platform/trends")
	g.Use(middleware.AuthJWT(m.secret))
	g.Use(middleware.RequireRole("admin", "moderator"))
	g.GET("", m.list)
	g.POST("/add", m.add)
	g.PUT("/queries", m.setQueries)
	g.POST("/search", m.search)
	g.PUT("/:id", m.edit)
	g.DELETE("/:id", m.remove)
	g.POST("/:id/fetch", m.refetch)
	g.POST("/:id/make", m.make)
	g.POST("/:id/plan", m.plan)
}

func (m *Trends) load(ctx context.Context) (*trendsDoc, int, error) {
	d := &trendsDoc{}
	pd, err := m.docs.GetDoc(ctx, "club", trendsKey)
	if err != nil {
		return nil, 0, err
	}
	base := 0
	if pd != nil {
		base = pd.Version
		if !pd.Deleted && strings.TrimSpace(pd.Value) != "" {
			if err := json.Unmarshal([]byte(pd.Value), d); err != nil {
				return nil, 0, fmt.Errorf("bs_trends unreadable: %w", err)
			}
		}
	}
	if d.Posts == nil {
		d.Posts = []*TrendPost{}
	}
	return d, base, nil
}

func (m *Trends) update(ctx context.Context, fn func(d *trendsDoc) bool) (*trendsDoc, error) {
	var last error
	for try := 0; try < 6; try++ {
		d, base, err := m.load(ctx)
		if err != nil {
			return nil, err
		}
		before, _ := json.Marshal(d)
		if !fn(d) {
			return d, nil
		}
		d.trim()
		after, err := json.Marshal(d)
		if err != nil {
			return nil, err
		}
		if base > 0 && bytes.Equal(before, after) {
			return d, nil
		}
		if _, last = m.docs.PutDoc(ctx, "club", trendsKey, base, string(after), false, "server:trends"); last == nil {
			return d, nil
		}
	}
	return nil, last
}

func (m *Trends) ready(c *gin.Context) bool {
	if !teamOnly(c) {
		return false
	}
	if m.docs == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "no storage"})
		return false
	}
	return true
}

func (m *Trends) stamp() string { return m.now().In(almaty).Format(time.RFC3339) }

func trendWho(c *gin.Context) string {
	if v, ok := c.Get("userID"); ok {
		return fmt.Sprint(v)
	}
	return ""
}

// ── Collection ──

// AddLinks puts the links into the inbox (a known one is brought back from
// hidden); returns the new refs to read.
func (m *Trends) AddLinks(ctx context.Context, refs []ThreadsRef, source, by string, manual *TrendPost) ([]ThreadsRef, error) {
	var fresh []ThreadsRef
	_, err := m.update(ctx, func(d *trendsDoc) bool {
		fresh = fresh[:0]
		for _, r := range refs {
			if p := d.find(r.Code); p != nil {
				if p.Status == "hidden" || p.Status == "cand" {
					p.Status = "inbox"
				}
				if manual != nil {
					mergeManual(p, manual)
					analyzeInto(p)
				}
				if p.Text == "" {
					fresh = append(fresh, r)
				}
				continue
			}
			p := &TrendPost{ID: r.Code, URL: r.URL, Author: r.User, Source: source, Status: "inbox", AddedAt: m.stamp(), AddedBy: by}
			if manual != nil {
				mergeManual(p, manual)
				analyzeInto(p)
			}
			d.Posts = append([]*TrendPost{p}, d.Posts...)
			if p.Text == "" {
				fresh = append(fresh, r)
			}
		}
		return true
	})
	return fresh, err
}

func mergeManual(p, in *TrendPost) {
	if t := strings.TrimSpace(in.Text); t != "" {
		p.Text, p.Via = t, "manual"
		p.FetchErr = ""
	}
	if a := strings.TrimPrefix(strings.TrimSpace(in.Author), "@"); a != "" {
		p.Author = a
	}
	for _, x := range []struct {
		dst *int
		v   int
	}{{&p.Likes, in.Likes}, {&p.Replies, in.Replies}, {&p.Reposts, in.Reposts}, {&p.Followers, in.Followers}} {
		if x.v > 0 {
			*x.dst = x.v
		}
	}
}

// FetchOne reads a post and keeps what it learned (a text typed by hand stays).
func (m *Trends) FetchOne(ctx context.Context, ref ThreadsRef) error {
	if m.Fetch == nil {
		return errors.New("чтение Threads выключено")
	}
	info, ferr := m.Fetch.Fetch(ctx, ref)
	_, err := m.update(ctx, func(d *trendsDoc) bool {
		p := d.find(ref.Code)
		if p == nil {
			return false
		}
		p.FetchedAt = m.stamp()
		if ferr != nil {
			p.FetchErr = ferr.Error()
			if errors.Is(ferr, ErrThreadsNotFound) && p.Status == "cand" {
				p.Status = "hidden"
			}
			return true
		}
		p.FetchErr, p.Note = "", info.Note
		if info.Exists {
			p.Verified = true
		}
		if info.Author != "" {
			p.Author = info.Author
		}
		if info.Name != "" {
			p.Name = info.Name
		}
		if info.PostedAt != "" {
			p.PostedAt = info.PostedAt
		}
		if info.Text != "" && p.Via != "manual" {
			p.Text, p.Via = info.Text, info.Via
		} else if p.Via == "" {
			p.Via = info.Via
		}
		if info.Likes > 0 && p.Likes == 0 {
			p.Likes = info.Likes
		}
		if info.Replies > 0 && p.Replies == 0 {
			p.Replies = info.Replies
		}
		if info.Reposts > 0 && p.Reposts == 0 {
			p.Reposts = info.Reposts
		}
		analyzeInto(p)
		return true
	})
	if err != nil {
		return err
	}
	return ferr
}

func (m *Trends) list(c *gin.Context) {
	if !m.ready(c) {
		return
	}
	d, _, err := m.load(c.Request.Context())
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, m.view(d))
}

func (m *Trends) view(d *trendsDoc) gin.H {
	return gin.H{"posts": d.Posts, "summary": TrendSummary(d.Posts, m.now()), "queries": d.queries(),
		"canSearch": m.Search != nil, "ai": m.AI != nil, "pageFetch": m.Fetch != nil && !m.Fetch.PageOff,
		"searchAt": d.SearchAt, "searchErr": d.SearchErr}
}

type trendIn struct {
	Links     string `json:"links"`
	Text      string `json:"text"`
	Author    string `json:"author"`
	Likes     int    `json:"likes"`
	Replies   int    `json:"replies"`
	Reposts   int    `json:"reposts"`
	Followers int    `json:"followers"`
	Status    string `json:"status"`
}

func (in trendIn) manual() *TrendPost {
	return &TrendPost{Text: in.Text, Author: in.Author, Likes: in.Likes, Replies: in.Replies, Reposts: in.Reposts, Followers: in.Followers}
}

func (m *Trends) add(c *gin.Context) {
	if !m.ready(c) {
		return
	}
	var in trendIn
	_ = c.ShouldBindJSON(&in)
	refs := ThreadsLinks(in.Links)
	if len(refs) == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Не вижу ссылок на посты Threads: нужна ссылка вида threads.com/@автор/post/…"})
		return
	}
	if len(refs) > trendsAddMax {
		refs = refs[:trendsAddMax]
	}
	var man *TrendPost
	if len(refs) == 1 && (strings.TrimSpace(in.Text) != "" || in.Likes+in.Replies+in.Reposts+in.Followers > 0 || in.Author != "") {
		man = in.manual()
	}
	ctx := c.Request.Context()
	fresh, err := m.AddLinks(ctx, refs, "link", trendWho(c), man)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	inline, rest := fresh, []ThreadsRef(nil)
	if len(inline) > trendsInline {
		inline, rest = fresh[:trendsInline], fresh[trendsInline:]
	}
	fctx, cancel := context.WithTimeout(ctx, 45*time.Second)
	defer cancel()
	failed := 0
	for _, r := range inline {
		if m.FetchOne(fctx, r) != nil {
			failed++
		}
	}
	if len(rest) > 0 {
		m.fetchLater(rest)
	}
	d, _, _ := m.load(ctx)
	out := m.view(d)
	out["ok"], out["added"], out["failed"], out["later"] = true, len(refs), failed, len(rest)
	c.JSON(http.StatusOK, out)
}

func (m *Trends) fetchLater(refs []ThreadsRef) {
	m.Go(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Duration(len(refs))*20*time.Second)
		defer cancel()
		for _, r := range refs {
			if err := m.FetchOne(ctx, r); err != nil && ctx.Err() != nil {
				return
			}
		}
	})
}

func (m *Trends) edit(c *gin.Context) {
	if !m.ready(c) {
		return
	}
	var in trendIn
	_ = c.ShouldBindJSON(&in)
	id := c.Param("id")
	found := false
	d, err := m.update(c.Request.Context(), func(d *trendsDoc) bool {
		p := d.find(id)
		if found = p != nil; !found {
			return false
		}
		mergeManual(p, in.manual())
		switch in.Status {
		case "inbox", "done", "hidden":
			p.Status = in.Status
		}
		analyzeInto(p)
		return true
	})
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	if !found {
		c.JSON(http.StatusNotFound, gin.H{"error": "Пост не найден"})
		return
	}
	out := m.view(d)
	out["ok"] = true
	c.JSON(http.StatusOK, out)
}

func (m *Trends) remove(c *gin.Context) {
	if !m.ready(c) {
		return
	}
	id := c.Param("id")
	d, err := m.update(c.Request.Context(), func(d *trendsDoc) bool {
		for i, p := range d.Posts {
			if p.ID == id {
				d.Posts = append(d.Posts[:i], d.Posts[i+1:]...)
				return true
			}
		}
		return false
	})
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	out := m.view(d)
	out["ok"] = true
	c.JSON(http.StatusOK, out)
}

func (m *Trends) refetch(c *gin.Context) {
	if !m.ready(c) {
		return
	}
	ctx := c.Request.Context()
	d, _, err := m.load(ctx)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	p := d.find(c.Param("id"))
	if p == nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Пост не найден"})
		return
	}
	ref, ok := ParseThreadsURL(p.URL)
	if !ok {
		c.JSON(http.StatusBadRequest, gin.H{"error": "У поста нет ссылки Threads"})
		return
	}
	if m.Fetch != nil {
		m.Fetch.forget(ref.Code)
	}
	if p.Status == "cand" {
		_, _ = m.update(ctx, func(d *trendsDoc) bool {
			if x := d.find(ref.Code); x != nil && x.Status == "cand" {
				x.Status = "inbox"
				return true
			}
			return false
		})
	}
	fctx, cancel := context.WithTimeout(ctx, 40*time.Second)
	defer cancel()
	ferr := m.FetchOne(fctx, ref)
	d, _, _ = m.load(ctx)
	out := m.view(d)
	out["ok"] = ferr == nil
	if ferr != nil {
		out["error"] = ferr.Error()
	}
	c.JSON(http.StatusOK, out)
}

func (f *ThreadsFetcher) forget(code string) {
	f.cmu.Lock()
	delete(f.cache, code)
	f.cmu.Unlock()
}

func (m *Trends) setQueries(c *gin.Context) {
	if !m.ready(c) {
		return
	}
	var in struct {
		Queries []string `json:"queries"`
	}
	_ = c.ShouldBindJSON(&in)
	qs := cleanQueries(in.Queries)
	d, err := m.update(c.Request.Context(), func(d *trendsDoc) bool { d.Queries = qs; return true })
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	out := m.view(d)
	out["ok"] = true
	c.JSON(http.StatusOK, out)
}

func cleanQueries(in []string) []string {
	var out []string
	seen := map[string]bool{}
	for _, q := range in {
		q = strings.Join(strings.Fields(q), " ")
		if r := []rune(q); len(r) > 60 {
			q = string(r[:60])
		}
		if q == "" || seen[strings.ToLower(q)] {
			continue
		}
		seen[strings.ToLower(q)] = true
		out = append(out, q)
		if len(out) == 20 {
			break
		}
	}
	return out
}

// ── «Подобрать залетевшие»: the AI's web search ──

const trendsSearchPrompt = `Найди в интернете популярные посты в Threads (threads.com или threads.net) за последние 14 дней на русском языке по теме бизнеса для собственников и предпринимателей Казахстана и СНГ.
Запросы: %s.
Нужны посты, которые залетели: много лайков, ответов или репостов, их обсуждают. Только посты других авторов, не Business Surgery.
Верни ТОЛЬКО JSON: {"items":[{"url":"https://www.threads.com/@автор/post/КОД","author":"автор без @","snippet":"о чём пост, своими словами, до 200 знаков","likes":0,"replies":0,"query":"по какому запросу","why":"почему залетел, одной фразой"}]}
Только реальные ссылки на конкретные посты, которые ты видел в поиске. Не выдумывай ссылки и цифры: не знаешь лайков, ставь 0. До %d постов.`

var ErrTrendsNoSearch = errors.New("Поиск в интернете сейчас недоступен (нужен ключ Gemini или баланс Claude): вставьте ссылки на посты вручную")

func (m *Trends) search(c *gin.Context) {
	if !m.ready(c) {
		return
	}
	var in struct {
		Queries []string `json:"queries"`
	}
	_ = c.ShouldBindJSON(&in)
	if !m.searching.TryLock() {
		c.JSON(http.StatusOK, gin.H{"ok": false, "error": "Подбор уже идёт, обновите через минуту"})
		return
	}
	defer m.searching.Unlock()
	ctx, cancel := context.WithTimeout(c.Request.Context(), 4*time.Minute)
	defer cancel()
	n, err := m.SearchTrends(ctx, cleanQueries(in.Queries))
	d, _, _ := m.load(ctx)
	out := gin.H{}
	if d != nil {
		out = m.view(d)
	}
	out["ok"], out["found"] = err == nil, n
	if err != nil {
		out["error"] = err.Error()
	}
	c.JSON(http.StatusOK, out)
}

type trendFound struct {
	URL     string `json:"url"`
	Author  string `json:"author"`
	Snippet string `json:"snippet"`
	Likes   int    `json:"likes"`
	Replies int    `json:"replies"`
	Query   string `json:"query"`
	Why     string `json:"why"`
}

// SearchTrends asks the web-search model for popular posts and keeps the
// new ones as candidates; a link oEmbed does not know is dropped (the model
// made it up). Returns how many candidates were added.
func (m *Trends) SearchTrends(ctx context.Context, queries []string) (int, error) {
	if m.Search == nil {
		return 0, ErrTrendsNoSearch
	}
	d, _, err := m.load(ctx)
	if err != nil {
		return 0, err
	}
	if len(queries) == 0 {
		queries = d.queries()
	}
	q := make([]string, len(queries))
	for i, x := range queries {
		q[i] = "«" + x + "»"
	}
	ans, serr := m.Search(ctx, fmt.Sprintf(trendsSearchPrompt, strings.Join(q, ", "), trendsSearchN))
	if serr != nil {
		msg := serr.Error()
		if errors.Is(serr, ai.ErrNoSearch) || errors.Is(serr, ai.ErrNoKey) {
			msg = ErrTrendsNoSearch.Error()
		} else {
			msg = "Поиск не ответил: " + ai.UserMessage(serr)
		}
		_, _ = m.update(ctx, func(d *trendsDoc) bool { d.SearchAt, d.SearchErr = m.stamp(), msg; return true })
		return 0, errors.New(msg)
	}
	found := parseTrendFound(ans)
	var cands []*TrendPost
	seen := map[string]bool{}
	for _, f := range found {
		r, ok := ParseThreadsURL(f.URL)
		if !ok || seen[r.Code] || d.find(r.Code) != nil || strings.Contains(strings.ToLower(r.User), "bsurgery") || strings.Contains(strings.ToLower(r.User), "business.surgery") {
			continue
		}
		seen[r.Code] = true
		p := &TrendPost{ID: r.Code, URL: r.URL, Author: r.User, Snippet: cutRunes(noLongDash(f.Snippet), 240), Likes: maxInt(f.Likes, 0), Replies: maxInt(f.Replies, 0),
			Source: "search", Query: cutRunes(f.Query, 60), Status: "cand", AddedAt: m.stamp(), AddedBy: "search", Why: cutRunes(noLongDash(f.Why), 160)}
		if p.Author == "" {
			p.Author = strings.TrimPrefix(f.Author, "@")
		}
		cands = append(cands, p)
	}
	// oEmbed knows the real ones (no token, one request per 3 s)
	var keep []*TrendPost
	for i, p := range cands {
		if m.Fetch == nil || i >= 8 || ctx.Err() != nil {
			keep = append(keep, p)
			continue
		}
		info, ferr := m.Fetch.OEmbed(ctx, ThreadsRef{URL: p.URL, User: p.Author, Code: p.ID})
		if errors.Is(ferr, ErrThreadsNotFound) {
			continue
		}
		if ferr == nil {
			p.Verified = true
			if info.Author != "" {
				p.Author = info.Author
			}
			if info.Text != "" {
				p.Text, p.Via = info.Text, "oembed"
				analyzeInto(p)
			}
		}
		keep = append(keep, p)
	}
	_, err = m.update(ctx, func(d *trendsDoc) bool {
		for i := len(keep) - 1; i >= 0; i-- {
			if d.find(keep[i].ID) == nil {
				d.Posts = append([]*TrendPost{keep[i]}, d.Posts...)
			}
		}
		d.SearchAt, d.SearchErr = m.stamp(), ""
		if len(keep) == 0 {
			d.SearchErr = "Поиск не нашёл новых постов с настоящими ссылками: попробуйте другие запросы или вставьте ссылки вручную"
		}
		return true
	})
	return len(keep), err
}

func parseTrendFound(ans string) []trendFound {
	js := ai.JSONFrom(ans)
	var out struct {
		Items []trendFound `json:"items"`
	}
	if js != "" && json.Unmarshal([]byte(js), &out) == nil && len(out.Items) > 0 {
		return out.Items
	}
	// no JSON: the links in the text
	var items []trendFound
	for _, r := range ThreadsLinks(ans) {
		items = append(items, trendFound{URL: r.URL, Author: r.User})
	}
	return items
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

// ── Analysis (rules) ──

var trendHookNames = map[string]string{"question": "Вопрос", "number": "Цифра", "provocation": "Провокация", "story": "История",
	"list": "Список", "confession": "Признание", "statement": "Утверждение"}

var (
	trListLineRe = regexp.MustCompile(`^\s*(?:\d{1,2}[.)]|[-•▪✅❌👉✔→]|\p{So})\s*\S`)
	trDigitRe    = regexp.MustCompile(`\d`)
	trConfess    = []string{"признаюсь", "честно", "я ошибал", "я ошиблась", "мне стыдно", "я потерял", "я потеряла", "я уволил", "мой провал", "моя ошибка", "я долго", "я боялся", "я боялась", "я думал", "я думала", "раньше я"}
	trStory      = []string{"однажды", "вчера", "сегодня", "на прошлой неделе", "когда я", "пришёл", "пришел", "пришла", "лет назад", "года назад", "мой клиент", "у меня был", "у нас был", "история", "в 2019", "в 2020", "в 2021", "в 2022", "в 2023", "в 2024", "в 2025", "в 2026"}
	trProvoke    = []string{"никогда", "хватит", "перестань", "перестаньте", "не нужно", "не надо", "миф", "врут", "неправда", "стоп", "забудь", "бред", "глупо", "ошибка", "не работает", "никто не", "все врут", "унпопуляр", "непопулярное мнение"}
	trCTAQ       = []string{"а у тебя", "а у вас", "а ты", "а вы", "согласны", "согласен", "как думаете", "как считаете", "что скажете", "было такое", "знакомо"}
	trCTASave    = []string{"сохрани", "сохраняй", "сохраните", "в закладки"}
	trCTAComment = []string{"пиши в комментар", "напиши в комментар", "пишите в комментар", "в комментариях", "напиши «", "пиши «", "напишите «", "в ответах"}
	trCTAFollow  = []string{"подпишись", "подпишитесь", "подписывайся", "подписывайтесь"}
	trEmotions   = []struct {
		name  string
		words []string
	}{
		{"страх потерь", []string{"потер", "теря", "долг", "кассов", "банкрот", "закрыл", "закрыть бизнес", "убыт", "штраф", "выгор", "провал", "минус", "сгорел"}},
		{"выгода", []string{"заработ", "прибыл", "млн", "выручк", "вырос", "рост", "x2", "х2", "вдвое", "окупил", "сэконом"}},
		{"несогласие", []string{"миф", "врут", "неправда", "хватит", "перестань", "не нужно", "не надо", "глупо", "бред", "переоцен"}},
		{"любопытство", []string{"секрет", "никто не", "мало кто", "почему", "оказалось", "правда о", "что будет", "как так"}},
		{"узнавание", []string{"знакомо", "каждый", "все мы", "у всех", "ты тоже", "бывает", "узнаешь", "узнали"}},
		{"гордость", []string{"построил", "построила", "лет в бизнесе", "добился", "добилась", "запустил", "запустила", "открыл", "открыла"}},
		{"ирония", []string{"смешно", "ахах", "хаха", "))", "ирони", "шутк"}},
	}
	trTopics = []struct {
		name  string
		words []string
	}{
		{"Продажи", []string{"продаж", "продав", "клиент", "заявк", "менеджер", "сделк", "чек", "воронк", "конверс"}},
		{"Команда", []string{"найм", "наня", "нанима", "сотрудник", "команд", "уволи", "hr", "зарплат", "персонал", "кадр", "делегир"}},
		{"Финансы", []string{"деньг", "прибыл", "финанс", "налог", "кассов", "марж", "учёт", "учет", "выручк", "расход", "кредит", "долг", "инвест"}},
		{"Маркетинг", []string{"маркетинг", "реклам", "контент", "smm", "threads", "инстаграм", "instagram", "бренд", "таргет", "блог", "охват"}},
		{"Процессы", []string{"процесс", "систем", "операционк", "регламент", "автоматиз", "crm", "срм"}},
		{"Стратегия", []string{"стратег", "масштаб", "рынок", "конкурент", "ниша", "франшиз", "партнёр", "партнер"}},
		{"Мышление", []string{"мышлен", "страх", "решени", "выгоран", "энерги", "цель", "привычк", "мотивац", "собственник", "предпринимател"}},
	}
	trLocal = []string{"казахстан", "алмат", "астан", "шымкент", "kaspi", "каспи", "тенге", "₸", "снг", "в рк", "по рк", "бишкек", "ташкент", "kz"}
)

func hasAny(s string, words []string) bool {
	for _, w := range words {
		if strings.Contains(s, w) {
			return true
		}
	}
	return false
}

func countAny(s string, words []string) int {
	n := 0
	for _, w := range words {
		n += strings.Count(s, w)
	}
	return n
}

func trendLines(text string) (lines []string, paras int) {
	text = strings.ReplaceAll(strings.TrimSpace(text), "\r", "")
	inPara := false
	for _, l := range strings.Split(text, "\n") {
		if l = strings.TrimSpace(l); l == "" {
			inPara = false
			continue
		}
		lines = append(lines, l)
		if !inPara {
			paras++
			inPara = true
		}
	}
	return
}

// AnalyzeTrend: the rules' reading of a post (no AI).
func AnalyzeTrend(p *TrendPost) *TrendAnalysis {
	text := strings.TrimSpace(p.Text)
	if text == "" {
		text = strings.TrimSpace(p.Snippet)
	}
	if text == "" {
		return nil
	}
	lines, paras := trendLines(text)
	a := &TrendAnalysis{Lines: len(lines), Paras: paras, Chars: utf8.RuneCountInString(text), By: "rules"}
	first := ""
	if len(lines) > 0 {
		first = lines[0]
	}
	a.First = cutRunes(first, 140)
	fl := strings.ToLower(first)
	low := strings.ToLower(text)
	for _, l := range lines {
		if trListLineRe.MatchString(l) {
			a.ListN++
		}
	}
	switch {
	case hasAny(fl, trConfess):
		a.Hook = "confession"
	case strings.Contains(fl, "?"):
		a.Hook = "question"
	case hasAny(fl, trProvoke) || strings.HasSuffix(strings.TrimSpace(fl), "!") && utf8.RuneCountInString(fl) < 80:
		a.Hook = "provocation"
	case trDigitRe.MatchString(fl) && a.ListN < 3:
		a.Hook = "number"
	case a.ListN >= 3 || strings.HasSuffix(fl, ":"):
		a.Hook = "list"
	case hasAny(fl, trStory):
		a.Hook = "story"
	case trDigitRe.MatchString(fl):
		a.Hook = "number"
	default:
		a.Hook = "statement"
	}
	a.HookName = trendHookNames[a.Hook]
	last := ""
	if len(lines) > 0 {
		last = strings.ToLower(lines[len(lines)-1])
	}
	switch {
	case hasAny(low, trCTASave):
		a.CTA = "save"
	case hasAny(low, trCTAComment):
		a.CTA = "comment"
	case hasAny(low, trCTAFollow):
		a.CTA = "follow"
	case strings.Contains(last, "?") && len(lines) > 1 || hasAny(last, trCTAQ):
		a.CTA = "question"
	case thURLRe.MatchString(text):
		a.CTA = "link"
	}
	best, bestN := "", 0
	for _, e := range trEmotions {
		if n := countAny(low, e.words); n > bestN {
			best, bestN = e.name, n
		}
	}
	if best == "" {
		best = "интерес"
		if a.ListN >= 3 {
			best = "польза"
		}
	}
	a.Emotion = best
	best, bestN = "", 0
	for _, t := range trTopics {
		if n := countAny(low, t.words); n > bestN {
			best, bestN = t.name, n
		}
	}
	if best == "" {
		best = "Бизнес"
	}
	a.Topic = best
	a.Local = hasAny(low, trLocal)
	a.Score = p.Likes + 2*p.Replies + 3*p.Reposts
	if p.Followers > 0 && a.Score > 0 {
		a.ER = float64(int(float64(p.Likes+p.Replies+p.Reposts)*1000/float64(p.Followers))) / 10
	}
	// why it likely spread
	var why []string
	switch a.Hook {
	case "question":
		why = append(why, "Вопрос в первой строке зовёт ответить: ответы поднимают пост в ленте")
	case "number":
		why = append(why, "Цифра в первой строке обещает конкретику")
	case "provocation":
		why = append(why, "Спорное утверждение: люди спорят в ответах, и пост показывают дальше")
	case "story":
		why = append(why, "История: хочется дочитать, чем кончилось")
	case "confession":
		why = append(why, "Признание от первого лица: честность вызывает доверие и ответы")
	case "list":
		why = append(why, "Список: такие посты сохраняют и пересылают")
	}
	if fr := utf8.RuneCountInString(first); fr > 0 && fr <= 60 {
		why = append(why, "Короткий крючок: первая строка читается за секунду")
	}
	switch a.CTA {
	case "question":
		why = append(why, "Вопрос в конце собирает ответы")
	case "comment":
		why = append(why, "Прямая просьба написать в ответах")
	case "save":
		why = append(why, "Просьба сохранить: сохранения ценятся лентой")
	}
	if a.Chars <= 220 {
		why = append(why, "Коротко: дочитывают до конца")
	} else if a.Paras >= 3 {
		why = append(why, "Длинный, но разбит на абзацы: легко читать с телефона")
	}
	if a.Local {
		why = append(why, "Местный контекст: свои узнают себя")
	}
	if p.Likes > 0 && float64(p.Replies)/float64(p.Likes) >= 0.15 {
		why = append(why, fmt.Sprintf("Много ответов на лайк (%d на %d): тема задевает", p.Replies, p.Likes))
	}
	if a.ER >= 5 {
		why = append(why, fmt.Sprintf("Высокая вовлечённость: %.1f%% от подписчиков", a.ER))
	}
	switch a.Emotion {
	case "страх потерь":
		why = append(why, "Давит на страх потерять деньги: это читают внимательнее всего")
	case "выгода":
		why = append(why, "Обещает выгоду в деньгах")
	case "узнавание":
		why = append(why, "Узнаваемая ситуация: «это про меня»")
	}
	if len(why) > 5 {
		why = why[:5]
	}
	a.Why = why
	a.Pattern = trendPattern(a)
	return a
}

func trendPattern(a *TrendAnalysis) string {
	parts := []string{a.HookName}
	switch {
	case a.ListN >= 3:
		parts = append(parts, fmt.Sprintf("список из %d", a.ListN))
	case a.Lines > 0:
		parts = append(parts, fmt.Sprintf("%d %s, %d знаков", a.Lines, plural(a.Lines, "строка", "строки", "строк"), a.Chars))
	}
	parts = append(parts, a.Emotion)
	if n := trendCTAName[a.CTA]; n != "" {
		parts = append(parts, n)
	}
	return strings.Join(parts, " · ")
}

var trendCTAName = map[string]string{"question": "вопрос в конце", "comment": "просьба написать", "save": "просьба сохранить", "follow": "просьба подписаться", "link": "ссылка"}

func analyzeInto(p *TrendPost) {
	prevAI := p.An != nil && p.An.By == "ai"
	var keep *TrendAnalysis
	if prevAI {
		keep = p.An
	}
	a := AnalyzeTrend(p)
	if a != nil && keep != nil && strings.TrimSpace(p.Text) != "" {
		// the AI's reasons stay while the text is the same length
		if keep.Chars == a.Chars {
			a.Why, a.By = keep.Why, "ai"
			if keep.Pattern != "" {
				a.Pattern = keep.Pattern
			}
		}
	}
	p.An = a
}

// ── Summary of the week ──

type TrendCount struct {
	Name string `json:"name"`
	N    int    `json:"n"`
}

type TrendsSummary struct {
	Period  string       `json:"period"` // «7 дней» or «30 дней»
	Posts   int          `json:"posts"`
	Hooks   []TrendCount `json:"hooks"`
	Emotion []TrendCount `json:"emotion"`
	Topics  []TrendCount `json:"topics"`
	AvgLen  int          `json:"avgLen"`
	CTAPct  int          `json:"ctaPct"`
	ListPct int          `json:"listPct"`
	Local   int          `json:"local"`
	Best    string       `json:"best,omitempty"` // the id of the post with the best score
	Tips    []string     `json:"tips"`
	New     int          `json:"new"` // added in 7 days (any status but hidden)
}

func countTop(m map[string]int) []TrendCount {
	var out []TrendCount
	for k, v := range m {
		out = append(out, TrendCount{k, v})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].N != out[j].N {
			return out[i].N > out[j].N
		}
		return out[i].Name < out[j].Name
	})
	return out
}

// TrendSummary: the patterns of the week's analysed posts (30 days when the
// week has fewer than 3).
func TrendSummary(posts []*TrendPost, now time.Time) TrendsSummary {
	pick := func(days int) []*TrendPost {
		var out []*TrendPost
		from := now.AddDate(0, 0, -days)
		for _, p := range posts {
			if p.An == nil || p.Status == "hidden" || p.Status == "cand" || strings.TrimSpace(p.Text) == "" {
				continue
			}
			if t, ok := parseContentAt(p.AddedAt); ok && t.After(from) {
				out = append(out, p)
			}
		}
		return out
	}
	s := TrendsSummary{Period: "7 дней"}
	for _, p := range posts {
		if t, ok := parseContentAt(p.AddedAt); ok && t.After(now.AddDate(0, 0, -7)) && p.Status != "hidden" {
			s.New++
		}
	}
	list := pick(7)
	if len(list) < 3 {
		list, s.Period = pick(30), "30 дней"
	}
	s.Posts = len(list)
	if len(list) == 0 {
		s.Tips = []string{}
		return s
	}
	hooks, emo, top := map[string]int{}, map[string]int{}, map[string]int{}
	total, cta, lst, best := 0, 0, 0, -1
	for _, p := range list {
		a := p.An
		hooks[a.HookName]++
		emo[a.Emotion]++
		top[a.Topic]++
		total += a.Chars
		if a.CTA != "" {
			cta++
		}
		if a.ListN >= 3 {
			lst++
		}
		if a.Local {
			s.Local++
		}
		if a.Score > best {
			best, s.Best = a.Score, p.ID
		}
	}
	if best <= 0 {
		s.Best = ""
	}
	s.Hooks, s.Emotion, s.Topics = countTop(hooks), countTop(emo), countTop(top)
	s.AvgLen = total / len(list)
	s.CTAPct = cta * 100 / len(list)
	s.ListPct = lst * 100 / len(list)
	var tips []string
	if len(s.Hooks) > 0 {
		tips = append(tips, fmt.Sprintf("Чаще всего крючок «%s» (%d из %d): начинайте так же, но со своей темой", strings.ToLower(s.Hooks[0].Name), s.Hooks[0].N, s.Posts))
	}
	if len(s.Emotion) > 0 {
		tips = append(tips, fmt.Sprintf("Главная эмоция: %s", s.Emotion[0].Name))
	}
	switch {
	case s.AvgLen <= 220:
		tips = append(tips, fmt.Sprintf("Залетает коротко: в среднем %d знаков", s.AvgLen))
	default:
		tips = append(tips, fmt.Sprintf("Длина в среднем %d знаков: длинные посты разбивайте на абзацы", s.AvgLen))
	}
	if s.CTAPct >= 50 {
		tips = append(tips, fmt.Sprintf("%d%% постов заканчиваются призывом или вопросом", s.CTAPct))
	}
	if len(s.Topics) > 0 {
		tips = append(tips, "Тема недели: "+s.Topics[0].Name)
	}
	s.Tips = tips
	return s
}

// WeeklyLine: one line for the Monday report (empty without new trends).
func (m *Trends) WeeklyLine(ctx context.Context, from, to time.Time) string {
	d, _, err := m.load(ctx)
	if err != nil || d == nil {
		return ""
	}
	n := 0
	hooks := map[string]int{}
	for _, p := range d.Posts {
		t, ok := parseContentAt(p.AddedAt)
		if !ok || t.Before(from) || !t.Before(to) || p.Status == "hidden" {
			continue
		}
		n++
		if p.An != nil {
			hooks[p.An.HookName]++
		}
	}
	if n == 0 {
		return ""
	}
	line := fmt.Sprintf("🧵 Тренды Threads: %d %s за неделю", n, plural(n, "новый пост", "новых поста", "новых постов"))
	if h := countTop(hooks); len(h) > 0 {
		line += ", чаще всего крючок «" + strings.ToLower(h[0].Name) + "»"
	}
	return line + ". Разбор и наши посты по мотивам: " + m.PlatformURL + "?section=mTrends"
}

// ── «Сделать наш пост по мотивам» ──

const trendsSystem = `Ты пишешь посты для Threads от имени Business Surgery (BS), клуба собственников бизнеса в Алматы.
Задача: сделать НАШ пост по мотивам чужого залетевшего поста. Берёшь у источника только приём: тип крючка, структуру, ритм, длину, эмоцию, вид призыва. Тему раскрываешь через наш материал.

Правила:
1. Нельзя копировать источник: ни одной фразы длиннее трёх слов подряд, ни его примеров, ни его цифр, ни его историй.
2. Примеры, цифры, имена и бизнесы только из нашего материала. Контекст Казахстана: тенге, Алматы, Астана, Kaspi, где это естественно.
3. Русский язык, на «ты», тон практика: конкретно, спокойно, без пафоса.
4. Первая строка отдельной строкой, до 80 знаков, крючок того же типа, что у источника.
5. Никакой статистики вроде «80% собственников», «исследования показывают».
6. Деньги со знаком ₸, разряды через пробел: 10 000 ₸.
7. Не используй тире (— и –) как знак препинания. Дефис только в диапазонах: 3-5 дней.
8. Без хэштегов, ссылок и эмодзи. Без штампов: «друзья», «давайте разберёмся», «важно отметить», «ключ к успеху», «в современном мире», «лайфхак».
9. Длина как у источника, но не больше 420 знаков.
10. Три варианта должны отличаться материалом и зачином.

Ответ: только JSON {"analysis":{"why":["почему источник залетел, 2-4 коротких пункта"],"pattern":"приём одной строкой"},"variants":[{"text":"...","seed":1}]}`

type trendAIOut struct {
	Analysis struct {
		Why     []string `json:"why"`
		Pattern string   `json:"pattern"`
	} `json:"analysis"`
	Variants []struct {
		Text string `json:"text"`
		Seed int    `json:"seed"`
	} `json:"variants"`
}

var trendTopicOrgan = map[string]string{"Продажи": "Продажи", "Команда": "Команда", "Финансы": "Финансы", "Маркетинг": "Маркетинг",
	"Процессы": "Процессы", "Стратегия": "Стратегия", "Мышление": "Мышление"}

// trendSeeds: 3+ pieces of our library close to the post's topic (other
// roots each time the owner asks again).
func (m *Trends) trendSeeds(p *TrendPost, n int) []*threadsSeed {
	if m.Seeds == nil {
		return nil
	}
	pool := m.Seeds()
	if len(pool) == 0 {
		return nil
	}
	organ := ""
	if p.An != nil {
		organ = trendTopicOrgan[p.An.Topic]
	}
	keys := thKeys(p.Text + " " + p.Snippet)
	good := map[string]int{"example": 3, "hero": 3, "case": 3, "mistake": 2, "step": 2, "cost": 2, "sign": 1, "metric": 1, "cause": 1}
	type sc struct {
		s *threadsSeed
		v float64
	}
	var all []sc
	for _, s := range pool {
		v := float64(good[s.Kind])
		if v == 0 {
			continue
		}
		if organ != "" && s.Organ == organ {
			v += 4
		}
		v += 10 * jaccard(keys, thKeys(s.Title+" "+s.Text))
		if p.An != nil && trendKindFit[p.An.Hook][s.Kind] {
			v += 3
		}
		v += float64(hashN(p.ID+"/"+strconv.Itoa(p.MadeN)+"/"+s.ID, 100)) / 50 // a different pick each time
		all = append(all, sc{s, v})
	}
	sort.Slice(all, func(i, j int) bool { return all[i].v > all[j].v })
	var out []*threadsSeed
	roots := map[string]bool{}
	for _, x := range all {
		if roots[x.s.Root] {
			continue
		}
		roots[x.s.Root] = true
		out = append(out, x.s)
		if len(out) == n {
			break
		}
	}
	return out
}

// trendKindFit: the library pieces that suit a hook type.
var trendKindFit = map[string]map[string]bool{
	"list":       {"step": true},
	"story":      {"hero": true, "example": true, "case": true},
	"confession": {"hero": true, "example": true, "case": true},
	"number":     {"example": true, "metric": true, "cost": true},
	"question":   {"mistake": true, "sign": true, "hero": true},
}

var trWordsRe = regexp.MustCompile(`[\p{L}\p{N}]+`)

// copiedPhrase: the first 4-word run of the source the text repeats ("" none);
// runs are taken inside a sentence or a line, not across them.
func copiedPhrase(src, text string) string {
	runs := func(s string) [][]string {
		var out [][]string
		for _, seg := range trSegRe.Split(strings.ToLower(s), -1) {
			if w := trWordsRe.FindAllString(seg, -1); len(w) >= 4 {
				out = append(out, w)
			}
		}
		return out
	}
	grams := map[string]bool{}
	for _, w := range runs(src) {
		for i := 0; i+4 <= len(w); i++ {
			grams[strings.Join(w[i:i+4], " ")] = true
		}
	}
	for _, w := range runs(text) {
		for i := 0; i+4 <= len(w); i++ {
			if g := strings.Join(w[i:i+4], " "); grams[g] {
				return g
			}
		}
	}
	return ""
}

var trSegRe = regexp.MustCompile(`[.!?\n:;]+`)

// trendGate: the Threads generator's quality gate plus «not a copy».
func trendGate(src, text string) (string, []string) {
	text = threadsClean(noLongDashSoft(text))
	probs := threadsQuality(text, text, nil)
	if c := copiedPhrase(src, text); c != "" {
		probs = append(probs, "повтор фразы источника: «"+c+"»")
	}
	if src != "" && jaccard(thKeys(src), thKeys(text)) > 0.45 {
		probs = append(probs, "слишком близко к источнику")
	}
	return text, probs
}

// noLongDashSoft: « — » in a sentence becomes a colon or a comma the way the
// AI tends to mean it; ranges are fixed by threadsClean.
func noLongDashSoft(s string) string {
	s = strings.ReplaceAll(s, " — ", ": ")
	s = strings.ReplaceAll(s, " – ", ", ")
	return s
}

func (m *Trends) make(c *gin.Context) {
	if !m.ready(c) {
		return
	}
	ctx, cancel := context.WithTimeout(c.Request.Context(), 3*time.Minute)
	defer cancel()
	p, err := m.MakeVariants(ctx, c.Param("id"))
	if err != nil {
		code := http.StatusOK
		if err == errTrendNotFound {
			code = http.StatusNotFound
		}
		c.JSON(code, gin.H{"ok": false, "error": err.Error()})
		return
	}
	d, _, _ := m.load(ctx)
	out := gin.H{"ok": true, "post": p}
	if d != nil {
		out["summary"] = TrendSummary(d.Posts, m.now())
	}
	c.JSON(http.StatusOK, out)
}

var errTrendNotFound = errors.New("Пост не найден")

// MakeVariants writes 3 posts in BS voice after the post's pattern: the AI
// when it answers, else templates on the library's material. Only variants
// that pass the gate are kept; planned ones from before stay.
func (m *Trends) MakeVariants(ctx context.Context, id string) (*TrendPost, error) {
	d, _, err := m.load(ctx)
	if err != nil {
		return nil, err
	}
	p := d.find(id)
	if p == nil {
		return nil, errTrendNotFound
	}
	src := strings.TrimSpace(p.Text)
	if src == "" {
		src = strings.TrimSpace(p.Snippet)
	}
	if src == "" {
		return nil, errors.New("Нет текста поста: вставьте его, чтобы разобрать приём")
	}
	if p.An == nil {
		analyzeInto(p)
	}
	a := p.An
	seeds := m.trendSeeds(p, trendsVarN+3)
	if len(seeds) == 0 {
		return nil, errors.New("Библиотека не загрузилась: попробуйте позже")
	}
	var vars []TrendVariant
	var aiWhy []string
	aiPattern, note := "", ""
	if m.AI != nil {
		ans, aerr := m.AI(ctx, trendsSystem, trendPrompt(p, a, seeds[:minInt(len(seeds), trendsVarN)]))
		if aerr == nil {
			var out trendAIOut
			if js := ai.JSONFrom(ans); js != "" && json.Unmarshal([]byte(js), &out) == nil {
				aiWhy, aiPattern = out.Analysis.Why, strings.TrimSpace(out.Analysis.Pattern)
				for _, v := range out.Variants {
					text, probs := trendGate(src, v.Text)
					if len(probs) > 0 || text == "" {
						continue
					}
					idea := ""
					if v.Seed >= 1 && v.Seed <= len(seeds) {
						idea = seeds[v.Seed-1].Organ + ": " + seeds[v.Seed-1].Title
					}
					vars = append(vars, TrendVariant{Text: text, Idea: idea, By: "ai"})
					if len(vars) == trendsVarN {
						break
					}
				}
				if len(vars) < trendsVarN {
					note = "Часть вариантов ИИ не прошла проверку качества: добавлены варианты по шаблону"
				}
			} else {
				note = "ИИ ответил неразборчиво: варианты по шаблону"
			}
		} else {
			note = "ИИ не ответил (" + ai.UserMessage(aerr) + "): варианты по шаблону"
		}
	} else {
		note = "ИИ не подключён: варианты по шаблону на материале библиотеки"
	}
	// templates fill up to 3
	for i := 0; len(vars) < trendsVarN && i < len(seeds); i++ {
		text, idea := trendTemplate(a, seeds[i], i)
		text, probs := trendGate(src, text)
		if len(probs) > 0 || text == "" {
			continue
		}
		dup := false
		for _, v := range vars {
			if v.Text == text {
				dup = true
			}
		}
		if !dup {
			vars = append(vars, TrendVariant{Text: text, Idea: idea, By: "rules"})
		}
	}
	if len(vars) == 0 {
		return nil, errors.New("Не получилось собрать вариант, который проходит проверку качества: попробуйте ещё раз")
	}
	var res *TrendPost
	_, err = m.update(ctx, func(d *trendsDoc) bool {
		x := d.find(id)
		if x == nil {
			return false
		}
		keep := []TrendVariant{}
		for _, v := range x.Vars {
			if v.Planned != "" {
				keep = append(keep, v)
			}
		}
		x.Vars = append(keep, vars...)
		x.MadeAt, x.MadeN, x.MadeNote = m.stamp(), x.MadeN+1, note
		if x.An == nil {
			analyzeInto(x)
		}
		if x.An != nil && len(aiWhy) > 0 {
			var why []string
			for _, w := range aiWhy {
				if w = cutRunes(noLongDash(strings.TrimSpace(w)), 160); w != "" {
					why = append(why, w)
				}
			}
			if len(why) > 0 {
				x.An.Why, x.An.By = why, "ai"
			}
			if aiPattern != "" {
				x.An.Pattern = cutRunes(noLongDash(aiPattern), 160)
			}
		}
		if x.Status == "cand" {
			x.Status = "inbox"
		}
		res = x
		return true
	})
	if err != nil {
		return nil, err
	}
	return res, nil
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func trendPrompt(p *TrendPost, a *TrendAnalysis, seeds []*threadsSeed) string {
	var b strings.Builder
	src := p.Text
	if src == "" {
		src = p.Snippet
	}
	b.WriteString("Источник (чужой пост, только для приёма, не копировать):\n«" + cutRunes(src, 900) + "»\n")
	if p.Likes+p.Replies > 0 {
		fmt.Fprintf(&b, "Лайки: %d, ответы: %d, репосты: %d.\n", p.Likes, p.Replies, p.Reposts)
	}
	if a != nil {
		fmt.Fprintf(&b, "Разбор правилами: крючок %s; %d строк, %d знаков, абзацев %d; эмоция: %s; тема: %s", strings.ToLower(a.HookName), a.Lines, a.Chars, a.Paras, a.Emotion, a.Topic)
		if n := trendCTAName[a.CTA]; n != "" {
			b.WriteString("; в конце " + n)
		}
		b.WriteString(".\n")
	}
	b.WriteString("\nНаш материал (бери примеры и цифры только отсюда):\n")
	for i, s := range seeds {
		fmt.Fprintf(&b, "%d. %s · %s: %s", i+1, s.Organ, s.Title, s.Text)
		if s.Extra != "" {
			b.WriteString(" Подробности: " + s.Extra)
		}
		b.WriteString("\n")
	}
	fmt.Fprintf(&b, "\nСделай %d варианта, каждый на своём материале (seed: номер материала).", trendsVarN)
	return b.String()
}

// trendTemplate: our post on a library piece with the source's hook type
// (no AI). Returns the text and its material.
func trendTemplate(a *TrendAnalysis, s *threadsSeed, i int) (string, string) {
	hook := "statement"
	cta := ""
	if a != nil {
		hook, cta = a.Hook, a.CTA
	}
	title := strings.TrimSpace(s.Title)
	lowTitle := lowerFirst(title)
	body := strings.TrimSpace(s.Text)
	extra := strings.TrimSpace(s.Extra)
	if strings.HasPrefix(extra, "Было:") {
		extra = "" // a before/after table, not a sentence
	}
	if hook == "list" && s.Kind != "step" {
		hook = "statement"
	}
	sentences := splitSentences(body)
	var first string
	switch hook {
	case "question":
		first = []string{"Как понять, что тебе пора заняться темой «" + lowTitle + "»?", "Ты точно знаешь, сколько тебе стоит бардак в теме «" + lowTitle + "»?", "Что будет, если навести порядок в теме «" + lowTitle + "»?"}[i%3]
	case "number":
		if n := firstWithDigit(extra); n != "" {
			first = n
		} else if n := firstWithDigit(body); n != "" {
			first = n
		} else {
			first = "Одна вещь про «" + lowTitle + "», которую стоит сделать на этой неделе."
		}
	case "provocation":
		first = []string{"«" + title + "» не про таблицы. Это про деньги.", "Без темы «" + lowTitle + "» бизнес работает вслепую.", "Хватит откладывать «" + lowTitle + "» на потом."}[i%3]
	case "story", "confession":
		if n := firstWithName(extra); n != "" {
			first = n
		} else if n := firstWithName(body); n != "" {
			first = n
		} else {
			first = "История с разбора: «" + lowTitle + "»."
		}
	case "list":
		first = title + ": что сделать на этой неделе"
	default:
		first = []string{"Тема, которую собственники откладывают: «" + lowTitle + "».", "«" + title + "»: одна мысль, которая экономит деньги.", "Про «" + lowTitle + "» коротко."}[i%3]
	}
	first = cutRunes(first, 110)
	var mid []string
	if hook == "list" && len(sentences) >= 2 {
		for k, x := range sentences {
			if k == 3 {
				break
			}
			mid = append(mid, fmt.Sprintf("%d. %s", k+1, x))
		}
	} else {
		for _, x := range sentences {
			if strings.HasPrefix(x, strings.TrimSuffix(strings.TrimSuffix(first, "…"), ".")) || strings.Contains(first, strings.TrimSuffix(x, ".")) {
				continue
			}
			mid = append(mid, x)
			if len(mid) == 2 {
				break
			}
		}
	}
	if x := firstSentence(extra); x != "" && hook != "list" && !strings.Contains(first, x) {
		x = cutRunes(x, 200)
		if !strings.ContainsAny(x[len(x)-1:], ".!?…") {
			x += "."
		}
		mid = append(mid, x)
	}
	end := ""
	switch cta {
	case "question", "comment":
		end = "А у тебя как с этим?"
	case "save":
		end = "Сохрани, чтобы вернуться к этому в понедельник."
	}
	text := first + "\n\n" + strings.Join(mid, "\n\n")
	if end != "" {
		text += "\n\n" + end
	}
	for utf8.RuneCountInString(text) > 420 && len(mid) > 1 {
		mid = mid[:len(mid)-1]
		text = first + "\n\n" + strings.Join(mid, "\n\n")
		if end != "" {
			text += "\n\n" + end
		}
	}
	if utf8.RuneCountInString(text) > 420 {
		text = cutRunes(text, 420)
	}
	return text, s.Organ + ": " + s.Title
}

func lowerFirst(s string) string {
	r := []rune(s)
	if len(r) > 1 && unicode.IsUpper(r[0]) && !unicode.IsUpper(r[1]) {
		r[0] = unicode.ToLower(r[0])
	}
	return string(r)
}

var trSentRe = regexp.MustCompile(`[^.!?]+[.!?]+`)

func splitSentences(s string) []string {
	var out []string
	for _, x := range trSentRe.FindAllString(s, -1) {
		if x = strings.TrimSpace(x); utf8.RuneCountInString(x) >= 12 {
			out = append(out, x)
		}
	}
	if len(out) == 0 && strings.TrimSpace(s) != "" {
		out = []string{strings.TrimSpace(s)}
	}
	return out
}

func firstWithDigit(s string) string {
	for _, x := range splitSentences(s) {
		if trDigitRe.MatchString(x) && utf8.RuneCountInString(x) <= 105 {
			return x
		}
	}
	return ""
}

var trNameRe = regexp.MustCompile(`(^|[\s«(])[А-ЯЁ][а-яё]{2,}(?:[\s,]|$)`)

func firstWithName(s string) string {
	for i, x := range splitSentences(s) {
		// a capital word not at the start: a name
		w := strings.Fields(x)
		if len(w) > 1 && trNameRe.MatchString(strings.Join(w[1:], " ")) && utf8.RuneCountInString(x) <= 105 {
			return x
		}
		if i == 0 && len(w) > 0 && trNameRe.MatchString(w[0]+" ") && utf8.RuneCountInString(x) <= 105 && strings.Contains(x, ",") {
			return x
		}
	}
	return ""
}

// ── «В план SMM» ──

func (m *Trends) plan(c *gin.Context) {
	if !m.ready(c) {
		return
	}
	var in struct {
		Text string `json:"text"`
		V    *int   `json:"v"`
	}
	_ = c.ShouldBindJSON(&in)
	ctx, cancel := context.WithTimeout(c.Request.Context(), 30*time.Second)
	defer cancel()
	it, p, err := m.PlanVariant(ctx, c.Param("id"), in.Text, in.V)
	if err != nil {
		code := http.StatusBadRequest
		if err == errTrendNotFound {
			code = http.StatusNotFound
		}
		c.JSON(code, gin.H{"ok": false, "error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"ok": true, "item": it, "post": p})
}

// PlanVariant checks the text again and puts it into the SMM plan.
func (m *Trends) PlanVariant(ctx context.Context, id, text string, v *int) (*contentItem, *TrendPost, error) {
	if m.Content == nil {
		return nil, nil, errors.New("Контент-движок не подключён")
	}
	d, _, err := m.load(ctx)
	if err != nil {
		return nil, nil, err
	}
	p := d.find(id)
	if p == nil {
		return nil, nil, errTrendNotFound
	}
	if strings.TrimSpace(text) == "" && v != nil && *v >= 0 && *v < len(p.Vars) {
		text = p.Vars[*v].Text
	}
	src := p.Text
	if src == "" {
		src = p.Snippet
	}
	clean, probs := trendGate(src, text)
	if len(probs) > 0 {
		return nil, nil, errors.New("Не проходит проверку: " + strings.Join(probs, ", "))
	}
	pattern := ""
	if p.An != nil {
		pattern = p.An.Pattern
	}
	who := "@" + p.Author
	if p.Author == "" {
		who = "чужого поста"
	}
	it, err := m.Content.AddTrendPost(ctx, clean, "По мотивам "+who, map[string]any{"url": p.URL, "author": p.Author, "pattern": pattern, "id": p.ID})
	if err != nil {
		return nil, nil, err
	}
	var res *TrendPost
	_, err = m.update(ctx, func(d *trendsDoc) bool {
		x := d.find(id)
		if x == nil {
			return false
		}
		hit := false
		for i := range x.Vars {
			if (v != nil && i == *v) || x.Vars[i].Text == clean {
				x.Vars[i].Text, x.Vars[i].Planned, x.Vars[i].PlannedAt = clean, it.ID, it.At
				hit = true
				break
			}
		}
		if !hit {
			x.Vars = append(x.Vars, TrendVariant{Text: clean, By: "edit", Planned: it.ID, PlannedAt: it.At})
		}
		x.Status = "done"
		res = x
		return true
	})
	return it, res, err
}

// AddTrendPost puts our post into the nearest free Threads slot (the manual
// mode's times when Threads has no token) within 2 weeks, as an edited item
// the daily batch keeps and works around. trend notes the source pattern.
func (e *ContentEngine) AddTrendPost(ctx context.Context, text, title string, trend map[string]any) (*contentItem, error) {
	var out *contentItem
	_, err := e.update(ctx, func(d *contentDoc) bool {
		out = nil
		st := e.settings(ctx, d)
		if !st.Channels.Threads.On {
			return false
		}
		now := e.now()
		from, to := st.threadsWindow()
		for k := 0; k <= contentDays && out == nil; k++ {
			day := dayStart(now.In(almaty)).AddDate(0, 0, k)
			if !st.dayOn("threads", day) {
				continue
			}
			var slots []time.Time
			if st.manual {
				slots = threadsManualSlots(day, st.manualPerDay())
			} else {
				slots = threadsSlots(day, st.threadsPerDay(), from, to)
			}
			for _, s := range slots {
				if !s.After(now.Add(20 * time.Minute)) {
					continue
				}
				taken := false
				for _, it := range d.Queue {
					if it.Channel != "threads" || it.Status == "skipped" {
						continue
					}
					if at, ok := parseContentAt(it.At); ok {
						if dd := at.Sub(s); dd < 12*time.Minute && dd > -12*time.Minute {
							taken = true
							break
						}
					}
				}
				if taken {
					continue
				}
				id := "th-" + s.In(almaty).Format("20060102-1504")
				for findContent(d, id) != nil {
					id += "t"
				}
				it := &contentItem{contentItemData: contentItemData{ID: id, Kind: "threads", Channel: "threads", Title: title, Text: text,
					At: s.In(almaty).Format(time.RFC3339), Status: "planned", Edited: true, Gen: "trend", Format: "trend"}}
				tr, _ := json.Marshal(trend)
				it.extra = map[string]json.RawMessage{"trend": tr}
				if st.manual {
					manualize(it)
				}
				d.Queue = append(d.Queue, it)
				out = it
				break
			}
		}
		return out != nil
	})
	if err != nil {
		return nil, err
	}
	if out == nil {
		return nil, errors.New("Нет свободного окна Threads на 2 недели вперёд (или Threads выключен в настройках)")
	}
	return out, nil
}

// ── The bot: a Threads link from the owner lands in the inbox ──

// BotLinks answers the team's message with Threads links (false: none).
func (m *Trends) BotLinks(ctx context.Context, chatID int64, text string) (string, bool) {
	refs := ThreadsLinks(text)
	if len(refs) == 0 {
		return "", false
	}
	if len(refs) > trendsAddMax {
		refs = refs[:trendsAddMax]
	}
	fresh, err := m.AddLinks(ctx, refs, "bot", fmt.Sprintf("tg:%d", chatID), nil)
	if err != nil {
		log.Printf("trends: bot: %v", err)
		return "Не получилось сохранить ссылку, попробуйте ещё раз через минуту", true
	}
	if len(fresh) > 0 {
		m.fetchLater(fresh)
	}
	return fmt.Sprintf("📥 В «Тренды Threads»: %d %s. Разбор и «Сделать наш пост по мотивам» на платформе: Маркетинг → Тренды Threads",
		len(refs), plural(len(refs), "ссылка", "ссылки", "ссылок")), true
}

// SelfTest reads the owner's example post once after a deploy and logs only
// what worked (no content).
func (m *Trends) SelfTest(ctx context.Context) {
	if m.Fetch == nil {
		return
	}
	ref, _ := ParseThreadsURL(thExampleLink)
	_, oerr := m.Fetch.OEmbed(ctx, ref)
	pageState := "off"
	text, likes := false, false
	if !m.Fetch.PageOff {
		pg, perr := m.Fetch.Page(ctx, ref)
		pageState = "ok"
		if perr != nil {
			pageState = "fail (" + perr.Error() + ")"
		}
		text, likes = pg.Text != "", pg.Likes > 0
	}
	oe := "ok"
	if oerr != nil {
		oe = "fail (" + oerr.Error() + ")"
	}
	log.Printf("trends: self-test threads fetch: oembed %s, page %s, text %v, likes %v", oe, pageState, text, likes)
}
