package http

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"io"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

// R53: «Тренды Threads»: чужие залетевшие посты рынка, их разбор и наши посты
// по мотивам (threads_trends.go). Здесь: ссылка на пост и вежливое чтение.
//
// Что можно без токена Threads API (проверено в октябре 2026):
//   - oEmbed graph.threads.com/oembed?url=… с 03.03.2026 работает без токена
//     (Threads changelog). Отвечает кодом встраивания: автор есть, текста
//     поста и лайков в нём обычно нет. Это официальный путь, он первый.
//   - Публичная страница поста (threads.com/@user/post/CODE) отдаёт meta
//     og:description (текст поста) и og:title (автор). robots.txt закрывает
//     её для поисковых роботов, поэтому страницу читаем только как превью
//     ссылки, которую человек вставил сам: одна страница, не чаще раза в 3
//     секунды, ответ кешируется на 6 часов, честный User-Agent. Не обходим
//     вход и не листаем ленты. THREADS_PAGE_FETCH=off выключает.
//   - Поиск по ключевым словам (keyword_search) требует токен и разрешение
//     threads_keyword_search: недоступно. Подбор идёт через поиск ИИ.
//   - Лайки и ответы публично не отдаются: их можно вписать руками.

const (
	thOEmbedURL   = "https://graph.threads.com/oembed"
	thFetchGap    = 3 * time.Second
	thFetchCache  = 6 * time.Hour
	thFetchMaxB   = 2 << 20
	thFetchUA     = "Mozilla/5.0 (compatible; BSPlatformLinkPreview/1.0; +https://app.bxclub.kz)"
	thExampleLink = "https://www.threads.com/@my.twinkles/post/DcObaXSjPe_"
)

// ThreadsRef: a post's link taken apart.
type ThreadsRef struct {
	URL  string // https://www.threads.com/@user/post/CODE (or /t/CODE)
	User string
	Code string
}

var (
	thPostURLRe = regexp.MustCompile(`(?i)(?:https?://)?(?:www\.)?threads\.(?:com|net)/(?:@([A-Za-z0-9._]{1,40})/post/|t/)([A-Za-z0-9_-]{6,20})`)
)

// ParseThreadsURL: a Threads post link (threads.com or .net, with @user or
// /t/, with or without query) → the canonical link; ok false for others.
func ParseThreadsURL(s string) (ThreadsRef, bool) {
	m := thPostURLRe.FindStringSubmatch(strings.TrimSpace(s))
	if m == nil {
		return ThreadsRef{}, false
	}
	r := ThreadsRef{User: m[1], Code: m[2]}
	if r.User != "" {
		r.URL = "https://www.threads.com/@" + r.User + "/post/" + r.Code
	} else {
		r.URL = "https://www.threads.com/t/" + r.Code
	}
	return r, true
}

// ThreadsLinks: every Threads post link in a text, once each, in order.
func ThreadsLinks(s string) []ThreadsRef {
	var out []ThreadsRef
	seen := map[string]bool{}
	for _, m := range thPostURLRe.FindAllString(s, -1) {
		if r, ok := ParseThreadsURL(m); ok && !seen[r.Code] {
			seen[r.Code] = true
			out = append(out, r)
		}
	}
	return out
}

// ThreadsPostInfo: what the public sources tell about a post.
type ThreadsPostInfo struct {
	Author   string `json:"author,omitempty"` // username without @
	Name     string `json:"name,omitempty"`   // display name
	Text     string `json:"text,omitempty"`
	PostedAt string `json:"postedAt,omitempty"`
	Likes    int    `json:"likes,omitempty"`
	Replies  int    `json:"replies,omitempty"`
	Reposts  int    `json:"reposts,omitempty"`
	Via      string `json:"via,omitempty"`    // oembed, page, oembed+page
	Exists   bool   `json:"exists,omitempty"` // oEmbed knows the post
	Note     string `json:"note,omitempty"`   // why something is missing
}

// ThreadsFetcher reads public post data politely (one request at a time,
// a gap between requests, a cache).
type ThreadsFetcher struct {
	HTTP      *http.Client
	OEmbedURL string
	PageOff   bool
	Gap       time.Duration
	now       func() time.Time

	mu    sync.Mutex // one request at a time
	last  time.Time
	cmu   sync.Mutex
	cache map[string]thCached
}

type thCached struct {
	at   time.Time
	info ThreadsPostInfo
	err  error
}

func NewThreadsFetcher() *ThreadsFetcher {
	return &ThreadsFetcher{HTTP: &http.Client{Timeout: 15 * time.Second}, OEmbedURL: thOEmbedURL,
		PageOff: strings.EqualFold(os.Getenv("THREADS_PAGE_FETCH"), "off"), Gap: thFetchGap, now: time.Now}
}

// ErrThreadsNotFound: oEmbed says there is no such public post.
var ErrThreadsNotFound = errors.New("Пост не найден или закрыт: проверьте ссылку")

func (f *ThreadsFetcher) get(ctx context.Context, u string, accept string) (int, []byte, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if w := f.Gap - f.now().Sub(f.last); w > 0 && !f.last.IsZero() {
		t := time.NewTimer(w)
		select {
		case <-ctx.Done():
			t.Stop()
			return 0, nil, ctx.Err()
		case <-t.C:
		}
	}
	defer func() { f.last = f.now() }()
	req, err := http.NewRequestWithContext(ctx, "GET", u, nil)
	if err != nil {
		return 0, nil, err
	}
	req.Header.Set("User-Agent", thFetchUA)
	req.Header.Set("Accept", accept)
	req.Header.Set("Accept-Language", "ru,en;q=0.8")
	res, err := f.HTTP.Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer res.Body.Close()
	b, err := io.ReadAll(io.LimitReader(res.Body, thFetchMaxB))
	return res.StatusCode, b, err
}

// Fetch: oEmbed first (official, no token), then the public page's meta
// tags. The result says which worked; an error only when neither did.
func (f *ThreadsFetcher) Fetch(ctx context.Context, ref ThreadsRef) (ThreadsPostInfo, error) {
	f.cmu.Lock()
	if c, ok := f.cache[ref.Code]; ok && f.now().Sub(c.at) < thFetchCache {
		f.cmu.Unlock()
		return c.info, c.err
	}
	f.cmu.Unlock()
	info, err := f.fetch(ctx, ref)
	if ctx.Err() == nil {
		f.cmu.Lock()
		if f.cache == nil {
			f.cache = map[string]thCached{}
		}
		if len(f.cache) > 500 {
			f.cache = map[string]thCached{}
		}
		f.cache[ref.Code] = thCached{at: f.now(), info: info, err: err}
		f.cmu.Unlock()
	}
	return info, err
}

func (f *ThreadsFetcher) fetch(ctx context.Context, ref ThreadsRef) (ThreadsPostInfo, error) {
	info := ThreadsPostInfo{Author: ref.User}
	var notes []string
	oe, oerr := f.OEmbed(ctx, ref)
	if oerr == nil {
		info.Exists, info.Via = true, "oembed"
		if oe.Author != "" {
			info.Author = oe.Author
		}
		info.Text = oe.Text
	} else if errors.Is(oerr, ErrThreadsNotFound) {
		return info, oerr
	} else {
		notes = append(notes, "oEmbed: "+oerr.Error())
	}
	if !f.PageOff && info.Text == "" {
		pg, perr := f.Page(ctx, ref)
		if perr == nil {
			if info.Via == "" {
				info.Via = "page"
			} else {
				info.Via += "+page"
			}
			if pg.Author != "" {
				info.Author = pg.Author
			}
			info.Name, info.Text, info.PostedAt = pg.Name, pg.Text, pg.PostedAt
			info.Likes, info.Replies, info.Reposts = pg.Likes, pg.Replies, pg.Reposts
		} else {
			notes = append(notes, "страница: "+perr.Error())
		}
	}
	if info.Via == "" {
		return info, fmt.Errorf("Threads не ответил (%s): вставьте текст поста вручную", strings.Join(notes, "; "))
	}
	if info.Text == "" {
		notes = append(notes, "текст поста Threads публично не отдал: вставьте его вручную")
	}
	if info.Likes == 0 && info.Replies == 0 {
		notes = append(notes, "лайки и ответы публично не видны: впишите их, если важно")
	}
	info.Note = strings.Join(notes, "; ")
	return info, nil
}

// OEmbed: graph.threads.com/oembed?url=… (no token since 03.03.2026).
func (f *ThreadsFetcher) OEmbed(ctx context.Context, ref ThreadsRef) (ThreadsPostInfo, error) {
	base := f.OEmbedURL
	if base == "" {
		base = thOEmbedURL
	}
	code, b, err := f.get(ctx, base+"?url="+url.QueryEscape(ref.URL), "application/json")
	if err != nil {
		return ThreadsPostInfo{}, fmt.Errorf("нет связи (%v)", shortErr(err))
	}
	switch {
	case code == 404 || code == 400 && strings.Contains(strings.ToLower(string(b)), "not found"):
		return ThreadsPostInfo{}, ErrThreadsNotFound
	case code == 401 || code == 403:
		return ThreadsPostInfo{}, fmt.Errorf("доступ закрыт (%d)", code)
	case code == 429:
		return ThreadsPostInfo{}, errors.New("слишком часто, попробуйте позже (429)")
	case code != 200:
		return ThreadsPostInfo{}, fmt.Errorf("ответ %d", code)
	}
	var oe struct {
		HTML       string `json:"html"`
		AuthorName string `json:"author_name"`
		Title      string `json:"title"`
	}
	if err := json.Unmarshal(b, &oe); err != nil || oe.HTML == "" {
		return ThreadsPostInfo{}, errors.New("ответ без кода встраивания")
	}
	out := ThreadsPostInfo{Author: strings.TrimPrefix(oe.AuthorName, "@")}
	if m := thPostByRe.FindStringSubmatch(oe.HTML); m != nil && out.Author == "" {
		out.Author = m[1]
	}
	if m := thPermalinkRe.FindStringSubmatch(oe.HTML); m != nil && out.Author == "" {
		if r, ok := ParseThreadsURL(m[1]); ok {
			out.Author = r.User
		}
	}
	out.Text = embedText(oe.HTML)
	if out.Text == "" && len([]rune(oe.Title)) > 20 {
		out.Text = strings.TrimSpace(oe.Title)
	}
	return out, nil
}

var (
	thPostByRe    = regexp.MustCompile(`(?i)Post by @([A-Za-z0-9._]+)`)
	thPermalinkRe = regexp.MustCompile(`data-text-post-permalink="([^"]+)"`)
	thTagRe       = regexp.MustCompile(`(?s)<script.*?</script>|<style.*?</style>|<svg.*?</svg>|<[^>]+>`)
	thEmbedJunk   = regexp.MustCompile(`(?i)(post by @[A-Za-z0-9._]+|view on threads|посмотреть в threads|смотреть в threads|публикация от @[A-Za-z0-9._]+)`)
	thMetaRe      = regexp.MustCompile(`(?is)<meta\s+[^>]*>`)
	thAttrRe      = regexp.MustCompile(`(?is)(property|name|content)\s*=\s*"([^"]*)"`)
	thTitleUserRe = regexp.MustCompile(`^(.*?)\s*\(@([A-Za-z0-9._]+)\)`)
	thCountRe     = map[string]*regexp.Regexp{
		"like":   regexp.MustCompile(`"like_count"\s*:\s*(\d+)`),
		"reply":  regexp.MustCompile(`"direct_reply_count"\s*:\s*(\d+)`),
		"repost": regexp.MustCompile(`"repost_count"\s*:\s*(\d+)`),
	}
	thTakenAtRe = regexp.MustCompile(`"taken_at"\s*:\s*(\d{9,11})`)
)

// embedText: the readable text of an embed code, without «Post by», «View on Threads».
func embedText(h string) string {
	t := thTagRe.ReplaceAllString(h, "\n")
	t = html.UnescapeString(t)
	t = thEmbedJunk.ReplaceAllString(t, "")
	var lines []string
	for _, l := range strings.Split(t, "\n") {
		if l = strings.TrimSpace(l); l != "" {
			lines = append(lines, l)
		}
	}
	out := strings.TrimSpace(strings.Join(lines, "\n"))
	if len([]rune(out)) < 20 {
		return ""
	}
	return out
}

// Page reads the public post page's meta tags (og:description: the text,
// og:title: «Name (@user) on Threads»), and counts when the page has them.
func (f *ThreadsFetcher) Page(ctx context.Context, ref ThreadsRef) (ThreadsPostInfo, error) {
	code, b, err := f.get(ctx, ref.URL, "text/html")
	if err != nil {
		return ThreadsPostInfo{}, fmt.Errorf("нет связи (%v)", shortErr(err))
	}
	if code == 404 {
		return ThreadsPostInfo{}, ErrThreadsNotFound
	}
	if code != 200 {
		return ThreadsPostInfo{}, fmt.Errorf("ответ %d", code)
	}
	return parseThreadsPage(string(b))
}

func parseThreadsPage(page string) (ThreadsPostInfo, error) {
	meta := map[string]string{}
	for _, tag := range thMetaRe.FindAllString(page, -1) {
		var k, v string
		for _, a := range thAttrRe.FindAllStringSubmatch(tag, -1) {
			switch strings.ToLower(a[1]) {
			case "property", "name":
				k = strings.ToLower(a[2])
			case "content":
				v = html.UnescapeString(a[2])
			}
		}
		if k != "" && meta[k] == "" {
			meta[k] = strings.TrimSpace(v)
		}
	}
	var out ThreadsPostInfo
	if m := thTitleUserRe.FindStringSubmatch(meta["og:title"]); m != nil {
		out.Name, out.Author = strings.TrimSpace(m[1]), m[2]
	}
	desc := meta["og:description"]
	if desc == "" {
		desc = meta["description"]
	}
	low := strings.ToLower(desc)
	if strings.Contains(low, "join threads") || strings.Contains(low, "log in") || strings.Contains(low, "присоединяйтесь к threads") {
		desc = "" // a login wall, not the post
	}
	out.Text = desc
	n := func(k string) int {
		if m := thCountRe[k].FindStringSubmatch(page); m != nil {
			v, _ := strconv.Atoi(m[1])
			return v
		}
		return 0
	}
	out.Likes, out.Replies, out.Reposts = n("like"), n("reply"), n("repost")
	if m := thTakenAtRe.FindStringSubmatch(page); m != nil {
		if s, err := strconv.ParseInt(m[1], 10, 64); err == nil {
			out.PostedAt = time.Unix(s, 0).In(almaty).Format(time.RFC3339)
		}
	} else if t := meta["article:published_time"]; t != "" {
		out.PostedAt = t
	}
	if out.Text == "" && out.Author == "" {
		return out, errors.New("на странице нет данных поста (возможно, нужен вход)")
	}
	return out, nil
}

func shortErr(err error) string {
	s := err.Error()
	if i := strings.LastIndex(s, ": "); i > 0 && len(s) > 80 {
		s = s[i+2:]
	}
	if len(s) > 80 {
		s = s[:80]
	}
	return s
}
