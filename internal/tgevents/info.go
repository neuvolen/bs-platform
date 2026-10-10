package tgevents

import (
	"context"
	"fmt"
	"html"
	"io"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// R77: a public channel's or group's card for Маркетинг → «Сообщества»:
// title, description, subscribers (members for a group), the last post.
// A channel has a preview t.me/s/<name> with posts; a group has none
// (t.me/s/ sends it to t.me/<name>, the card with "N members").

// Info: what the card of a public channel or group says.
type Info struct {
	Title string
	Desc  string
	Subs  int       // subscribers of a channel, members of a group
	Group bool      // a chat, not a channel
	Last  time.Time // the newest post (channels only)
	Posts []Post    // the posts of the preview page (channels only)
}

// InfoBaseURL: the plain card t.me/<name>; tests point it at a local server.
var InfoBaseURL = "https://t.me/"

var (
	ogDescRe   = regexp.MustCompile(`<meta property="og:description" content="([^"]*)"`)
	counterRe  = regexp.MustCompile(`<span class="counter_value">([^<]+)</span>\s*<span class="counter_type">([^<]+)</span>`)
	headCntRe  = regexp.MustCompile(`<div class="tgme_header_counter">([^<]+)</div>`)
	pageExtra  = regexp.MustCompile(`<div class="tgme_page_extra">([^<]+)</div>`)
	countWords = regexp.MustCompile(`(?i)(\d[\d\s\x{00a0},.]*[kmкм]?)\s*(subscribers?|members?|подписчик|участник)`)
	tmeRefRe   = regexp.MustCompile(`(?i)(?:https?://)?(?:www\.)?(?:t|telegram)\.me/(?:s/)?([A-Za-z][A-Za-z0-9_]{3,31})\b`)
)

// ParseCount reads "10.2K", "1,5M", "8 001", "12,345" as a number.
func ParseCount(s string) int {
	s = strings.TrimSpace(strings.ReplaceAll(s, " ", " "))
	if s == "" {
		return 0
	}
	mul := 1.0
	switch strings.ToLower(s[len(s)-1:]) {
	case "k":
		mul, s = 1e3, s[:len(s)-1]
	case "m":
		mul, s = 1e6, s[:len(s)-1]
	default:
		if strings.HasSuffix(s, "К") || strings.HasSuffix(s, "к") {
			mul, s = 1e3, strings.TrimSuffix(strings.TrimSuffix(s, "К"), "к")
		} else if strings.HasSuffix(s, "М") || strings.HasSuffix(s, "м") {
			mul, s = 1e6, strings.TrimSuffix(strings.TrimSuffix(s, "М"), "м")
		}
	}
	s = strings.ReplaceAll(strings.TrimSpace(s), " ", "")
	if mul > 1 {
		f, err := strconv.ParseFloat(strings.ReplaceAll(s, ",", "."), 64)
		if err != nil {
			return 0
		}
		return int(f*mul + 0.5)
	}
	n, _ := strconv.Atoi(strings.NewReplacer(",", "", ".", "").Replace(s))
	return n
}

// countIn: the subscribers or members in a counter text ("8 001 members, 120 online").
func countIn(s string) (int, bool) {
	m := countWords.FindStringSubmatch(html.UnescapeString(s))
	if m == nil {
		return 0, false
	}
	w := strings.ToLower(m[2])
	return ParseCount(m[1]), strings.HasPrefix(w, "member") || strings.HasPrefix(w, "участник")
}

// ParseInfo reads a t.me/s/<name> preview or a t.me/<name> card.
func ParseInfo(body string) Info {
	pg := ParsePage(body)
	in := Info{Title: pg.Title, Posts: pg.Posts}
	if m := ogDescRe.FindStringSubmatch(body); m != nil {
		in.Desc = strings.TrimSpace(html.UnescapeString(m[1]))
	}
	for _, m := range counterRe.FindAllStringSubmatch(body, -1) {
		if n, grp := countIn(m[1] + " " + m[2]); n > 0 {
			in.Subs, in.Group = n, grp
			break
		}
	}
	if in.Subs == 0 {
		for _, re := range []*regexp.Regexp{headCntRe, pageExtra} {
			if m := re.FindStringSubmatch(body); m != nil {
				if n, grp := countIn(m[1]); n > 0 {
					in.Subs, in.Group = n, grp
					break
				}
			}
		}
	}
	for _, p := range pg.Posts {
		if p.Time.After(in.Last) {
			in.Last = p.Time
		}
	}
	return in
}

// Mentions: the usernames that posts point to (t.me/<name> links and
// t.me/<name> written in the text), each once per post, without the channel
// itself and without bots.
func Mentions(posts []Post) []string {
	var out []string
	for _, p := range posts {
		seen := map[string]bool{strings.ToLower(p.Channel): true}
		var src []string
		src = append(src, p.Links...)
		src = append(src, p.Text)
		for _, s := range src {
			for _, m := range tmeRefRe.FindAllStringSubmatch(s, -1) {
				u := m[1]
				l := strings.ToLower(u)
				if seen[l] || tmeReserved[l] || strings.HasSuffix(l, "bot") {
					continue
				}
				seen[l] = true
				out = append(out, u)
			}
		}
	}
	return out
}

var tmeReserved = map[string]bool{"joinchat": true, "addstickers": true, "addemoji": true, "share": true, "proxy": true,
	"socks": true, "addlist": true, "boost": true, "contact": true, "login": true, "setlanguage": true, "addtheme": true,
	"invoice": true, "iv": true, "confirmphone": true, "telegram": true, "durov": true, "premium": true, "stories": true}

func getPage(ctx context.Context, hc *http.Client, u string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("User-Agent", UserAgent)
	req.Header.Set("Accept-Language", "en,ru;q=0.5") // "subscribers", "members"
	res, err := hc.Do(req)
	if err != nil {
		return "", err
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return "", fmt.Errorf("t.me ответил %d", res.StatusCode)
	}
	b, err := io.ReadAll(io.LimitReader(res.Body, 4<<20))
	return string(b), err
}

// FetchInfo reads the card of a public channel or group. A person, a bot
// or a missing name: ErrNoChannel.
func FetchInfo(ctx context.Context, hc *http.Client, username string) (Info, error) {
	body, err := getPage(ctx, hc, BaseURL+username)
	if err != nil {
		return Info{}, err
	}
	in := ParseInfo(body)
	if in.Subs > 0 || len(in.Posts) > 0 {
		return in, nil
	}
	// a group: the preview has no posts; the plain card says "N members"
	body, err = getPage(ctx, hc, InfoBaseURL+username)
	if err != nil {
		return Info{}, err
	}
	in2 := ParseInfo(body)
	if in2.Subs == 0 {
		return Info{}, ErrNoChannel
	}
	if in2.Title == "" {
		in2.Title = in.Title
	}
	return in2, nil
}
