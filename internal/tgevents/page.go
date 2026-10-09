// Package tgevents reads public Telegram channels through their web preview
// (https://t.me/s/<username>, no login) and turns posts into feed events.
package tgevents

import (
	"context"
	"errors"
	"fmt"
	"html"
	"io"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// Post: one channel message from the preview page.
type Post struct {
	Channel string
	ID      int
	Time    time.Time
	Text    string   // plain text, lines kept
	Links   []string // hrefs inside the text, in order
}

// URL of the post on t.me.
func (p Post) URL() string { return "https://t.me/" + p.Channel + "/" + strconv.Itoa(p.ID) }

// Page: what one preview page holds.
type Page struct {
	Title  string // channel title (og:title)
	Posts  []Post // oldest first, as on the page
	Before int    // the "before" id of the previous (older) page, 0 if none
}

var (
	ogTitleRe = regexp.MustCompile(`<meta property="og:title" content="([^"]*)"`)
	prevRe    = regexp.MustCompile(`<link rel="prev" href="[^"]*[?&]before=(\d+)"`)
	dataPost  = regexp.MustCompile(`data-post="([A-Za-z0-9_]+)/(\d+)"`)
	timeRe    = regexp.MustCompile(`<time[^>]*datetime="([^"]+)"`)
	hrefRe    = regexp.MustCompile(`<a\s[^>]*href="([^"]+)"`)
	brRe      = regexp.MustCompile(`(?i)<br\s*/?>`)
	tagRe     = regexp.MustCompile(`<[^>]+>`)
	divTagRe  = regexp.MustCompile(`(?i)<(/?)div\b`)
	spaceRe   = regexp.MustCompile(`[ \t\x{00a0}]+`)
)

const textOpen = `<div class="tgme_widget_message_text js-message_text"`

// ParsePage reads a t.me/s/<username> page.
func ParsePage(body string) Page {
	var pg Page
	if m := ogTitleRe.FindStringSubmatch(body); m != nil {
		pg.Title = html.UnescapeString(m[1])
	}
	if m := prevRe.FindStringSubmatch(body); m != nil {
		pg.Before, _ = strconv.Atoi(m[1])
	}
	chunks := strings.Split(body, `class="tgme_widget_message_wrap`)
	for _, ch := range chunks[1:] {
		m := dataPost.FindStringSubmatch(ch)
		if m == nil {
			continue
		}
		p := Post{Channel: m[1]}
		p.ID, _ = strconv.Atoi(m[2])
		if ts := timeRe.FindAllStringSubmatch(ch, -1); len(ts) > 0 {
			p.Time, _ = time.Parse(time.RFC3339, ts[len(ts)-1][1])
		}
		if i := strings.Index(ch, textOpen); i >= 0 {
			inner := innerDiv(ch[i:])
			for _, h := range hrefRe.FindAllStringSubmatch(inner, -1) {
				p.Links = append(p.Links, html.UnescapeString(h[1]))
			}
			p.Text = plain(inner)
		}
		pg.Posts = append(pg.Posts, p)
	}
	return pg
}

// innerDiv: the content of the <div …> that s starts with, up to its own </div>.
func innerDiv(s string) string {
	start := strings.Index(s, ">")
	if start < 0 {
		return ""
	}
	depth := 1
	for _, ix := range divTagRe.FindAllStringSubmatchIndex(s[start:], -1) {
		if ix[0] == 0 {
			continue
		}
		if s[start+ix[2]:start+ix[3]] == "/" {
			depth--
			if depth == 0 {
				return s[start+1 : start+ix[0]]
			}
		} else {
			depth++
		}
	}
	return s[start+1:]
}

func plain(h string) string {
	h = brRe.ReplaceAllString(h, "\n")
	h = tagRe.ReplaceAllString(h, "")
	h = html.UnescapeString(h)
	lines := strings.Split(h, "\n")
	out := lines[:0]
	for _, l := range lines {
		out = append(out, strings.TrimSpace(spaceRe.ReplaceAllString(l, " ")))
	}
	return strings.TrimSpace(strings.Join(out, "\n"))
}

// ── fetching ──

// UserAgent: polite and identifiable.
var UserAgent = "Mozilla/5.0 (compatible; BSEventsBot/1.0; +https://bxclub.kz)"

// BaseURL: tests point it at a local server.
var BaseURL = "https://t.me/s/"

// ErrNoChannel: the username has no public channel preview.
var ErrNoChannel = errors.New("канал не найден или закрыт")

// Fetch loads one preview page (before = 0: the newest posts).
func Fetch(ctx context.Context, hc *http.Client, username string, before int) (Page, error) {
	u := BaseURL + username
	if before > 0 {
		u += "?before=" + strconv.Itoa(before)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return Page{}, err
	}
	req.Header.Set("User-Agent", UserAgent)
	req.Header.Set("Accept-Language", "ru,en;q=0.8")
	res, err := hc.Do(req)
	if err != nil {
		return Page{}, err
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return Page{}, fmt.Errorf("t.me ответил %d", res.StatusCode)
	}
	b, err := io.ReadAll(io.LimitReader(res.Body, 4<<20))
	if err != nil {
		return Page{}, err
	}
	pg := ParsePage(string(b))
	// A missing or private channel: t.me shows the channel card without posts
	// (or redirects to t.me/<username>, a page with no data-post).
	if len(pg.Posts) == 0 && !strings.Contains(string(b), "tgme_channel_info") {
		return pg, ErrNoChannel
	}
	return pg, nil
}
