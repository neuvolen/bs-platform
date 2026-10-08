package http

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"regexp"
	"strings"
	"time"
)

// R62: short share links threads.com/share/<code> (what the Threads app's
// «Поделиться» copies). They carry no post code: the server asks Threads
// where the link leads (a redirect, or the page's og:url / canonical) and
// works with the canonical /@user/post/CODE from then on. One request per
// link, through the fetcher's gap and User-Agent.

var (
	thShareRe     = regexp.MustCompile(`(?i)(?:https?://)?(?:www\.)?threads\.(?:com|net)/share/([A-Za-z0-9_-]{4,40})/?`)
	thCanonicalRe = regexp.MustCompile(`(?is)<link\s+[^>]*rel="canonical"[^>]*href="([^"]+)"|<link\s+[^>]*href="([^"]+)"[^>]*rel="canonical"`)
	thCaptionRe   = regexp.MustCompile(`"caption"\s*:\s*\{[^{}]*?"text"\s*:\s*"((?:[^"\\]|\\.)*)"`)
)

// ThreadsShareLinks: every share link in a text, once each, as written.
func ThreadsShareLinks(s string) []string {
	var out []string
	seen := map[string]bool{}
	for _, m := range thShareRe.FindAllStringSubmatch(s, -1) {
		if !seen[m[1]] {
			seen[m[1]] = true
			out = append(out, m[0])
		}
	}
	return out
}

// ResolveShare: a share link → the canonical post link.
func (f *ThreadsFetcher) ResolveShare(ctx context.Context, share string) (ThreadsRef, error) {
	m := thShareRe.FindStringSubmatch(share)
	if m == nil {
		return ThreadsRef{}, errors.New("это не ссылка threads.com/share/…")
	}
	u := "https://www.threads.com/share/" + m[1] + "/"
	final, _, page, err := f.getPage(ctx, u)
	if err != nil {
		return ThreadsRef{}, fmt.Errorf("нет связи (%v)", shortErr(err))
	}
	if r, ok := ParseThreadsURL(final); ok {
		return r, nil
	}
	if r, ok := threadsRefInPage(page); ok {
		return r, nil
	}
	return ThreadsRef{}, fmt.Errorf("Threads не показал, на какой пост ведёт ссылка (%s, %d байт)", cutRunes(final, 120), len(page))
}

// threadsRefInPage: og:url, canonical, then the first post link on the page.
func threadsRefInPage(page string) (ThreadsRef, bool) {
	for _, tag := range thMetaRe.FindAllString(page, -1) {
		var k, v string
		for _, a := range thAttrRe.FindAllStringSubmatch(tag, -1) {
			switch strings.ToLower(a[1]) {
			case "property", "name":
				k = strings.ToLower(a[2])
			case "content":
				v = a[2]
			}
		}
		if k == "og:url" {
			if r, ok := ParseThreadsURL(v); ok {
				return r, true
			}
		}
	}
	if m := thCanonicalRe.FindStringSubmatch(page); m != nil {
		if r, ok := ParseThreadsURL(m[1] + m[2]); ok {
			return r, true
		}
	}
	return ParseThreadsURL(strings.ReplaceAll(page, `\/`, `/`))
}

// getPage: a GET that follows redirects and reports where it ended.
func (f *ThreadsFetcher) getPage(ctx context.Context, u string) (string, int, string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if w := f.Gap - f.now().Sub(f.last); w > 0 && !f.last.IsZero() {
		select {
		case <-ctx.Done():
			return "", 0, "", ctx.Err()
		case <-time.After(w):
		}
	}
	defer func() { f.last = f.now() }()
	req, err := http.NewRequestWithContext(ctx, "GET", u, nil)
	if err != nil {
		return "", 0, "", err
	}
	req.Header.Set("User-Agent", thFetchUA)
	req.Header.Set("Accept", "text/html")
	req.Header.Set("Accept-Language", "ru,en;q=0.8")
	res, err := f.HTTP.Do(req)
	if err != nil {
		return "", 0, "", err
	}
	defer res.Body.Close()
	b, err := io.ReadAll(io.LimitReader(res.Body, thFetchMaxB))
	final := u
	if res.Request != nil && res.Request.URL != nil {
		// the path is what matters: Threads may answer from another host
		final = "https://www.threads.com" + res.Request.URL.RequestURI()
	}
	return final, res.StatusCode, string(b), err
}

// ResolveShareLinks: the text with every share link replaced by its post
// link (a link that does not resolve stays as it was).
func (f *ThreadsFetcher) ResolveShareLinks(ctx context.Context, text string) string {
	if f == nil {
		return text
	}
	for _, s := range ThreadsShareLinks(text) {
		if r, err := f.ResolveShare(ctx, s); err == nil {
			text = strings.ReplaceAll(text, s, r.URL)
		} else {
			log.Printf("trends: share link: %v", err)
		}
	}
	return text
}

// threadsPageCaptions: the post texts on a public post page (the post and,
// for a chain, the author's next parts), in page order, once each.
func threadsPageCaptions(page string) []string {
	var out []string
	seen := map[string]bool{}
	for _, m := range thCaptionRe.FindAllStringSubmatch(page, -1) {
		var s string
		if json.Unmarshal([]byte(`"`+m[1]+`"`), &s) != nil {
			continue
		}
		if s = strings.TrimSpace(s); s != "" && !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	return out
}

// thOwnerShare: the link the owner sent on 08.10.2026 («посмотри это и
// примени»). The self-test resolves it once per start and logs the public
// post (author and text) so the team can read it without the app.
const thOwnerShare = "https://www.threads.com/share/BBr-UwA_Ka/"

func (m *Trends) shareSelfTest(ctx context.Context) {
	if m.Fetch == nil || m.Fetch.PageOff {
		return
	}
	ref, err := m.Fetch.ResolveShare(ctx, thOwnerShare)
	if err != nil {
		log.Printf("trends: self-test share link: fail (%v)", err)
		return
	}
	_, code, page, err := m.Fetch.getPage(ctx, ref.URL)
	if err != nil || code != 200 {
		log.Printf("trends: self-test share link: %s, page %d %v", ref.URL, code, err)
		return
	}
	pg, _ := parseThreadsPage(page)
	log.Printf("trends: self-test share link: %s author @%s, likes %d, replies %d: %s", ref.URL, pg.Author, pg.Likes, pg.Replies, oneLine(pg.Text, 1500))
	all := threadsPageCaptions(page)
	chain := threadsAuthorChain(page, pg.Author)
	log.Printf("trends: self-test share link: %d texts on the page, %d by the author", len(all), len(chain))
	for i, c := range threadsCaptionsWho(page) {
		if i >= 30 {
			break
		}
		log.Printf("trends: self-test share link text %d (@%s|@%s): %s", i+1, c.Before, c.After, oneLine(c.Text, 900))
	}
}

func oneLine(s string, n int) string {
	return cutRunes(strings.ReplaceAll(s, "\n", " / "), n)
}

var thUsernameRe = regexp.MustCompile(`"username"\s*:\s*"([A-Za-z0-9._]+)"`)

type thCaption struct{ Text, Before, After string }

// threadsCaptionsWho: every post text on the page with the closest
// "username" before and after it (Threads keeps the author in the post
// object next to its caption).
func threadsCaptionsWho(page string) []thCaption {
	users := thUsernameRe.FindAllStringSubmatchIndex(page, -1)
	var out []thCaption
	for _, m := range thCaptionRe.FindAllStringSubmatchIndex(page, -1) {
		var c thCaption
		if json.Unmarshal([]byte(`"`+page[m[2]:m[3]]+`"`), &c.Text) != nil {
			continue
		}
		for _, u := range users {
			if u[0] < m[0] {
				c.Before = page[u[2]:u[3]]
			} else if c.After == "" {
				c.After = page[u[2]:u[3]]
			}
		}
		if c.Text = strings.TrimSpace(c.Text); c.Text != "" {
			out = append(out, c)
		}
	}
	return out
}

// threadsAuthorChain: the author's own parts of a post (the post and its
// thread continuation), without other people's replies.
func threadsAuthorChain(page, author string) []string {
	if author == "" {
		return nil
	}
	var out []string
	seen := map[string]bool{}
	for _, c := range threadsCaptionsWho(page) {
		if strings.EqualFold(c.After, author) && !seen[c.Text] {
			seen[c.Text] = true
			out = append(out, c.Text)
		}
	}
	return out
}
