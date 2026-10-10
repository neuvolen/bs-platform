package http

import (
	"context"
	"encoding/json"
	"html"
	"io"
	"log"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/bnursik/business_surgery_backend/internal/tgevents"
)

// ── R83: WhatsApp in Маркетинг → «Сообщества» ──
// «Почему с WhatsApp их мало, я думаю их там наоборот гораздо больше».
//
// A WhatsApp channel (whatsapp.com/channel/…) or group invite
// (chat.whatsapp.com/…) has no catalogue of its own: the links live on
// pages. Once a day the server looks for them where they are published:
//   - the Telegram channels of the catalogue: the newest posts and the
//     channel's own search t.me/s/<name>?q=whatsapp;
//   - the pages the catalogue was researched on (associations, media,
//     clubs, accelerators) and business media and directories of groups
//     (waPages), one level deeper on the same site for links about business.
// Every link is opened: a live group shows its name on the invite page, a
// channel its name and followers. A link joins the catalogue only when it
// was seen on a real page (Src: that page or post) and is alive; from a
// directory or a general channel only when the name or the text next to it
// is about business. Nothing is typed in by hand.

type waSrc struct {
	URL string
	Biz bool // a business page: every live link on it counts
}

// waPages: where WhatsApp links of business communities are published.
// Biz pages (business media, associations, clubs) count every link; the
// directories of groups count only links about business.
var waPages = []waSrc{
	// directories of WhatsApp groups and channels in Kazakhstan (found by search 10.10.2026)
	{"https://akimshi.kz/", false},
	{"https://akimshi.kz/votsap/drygoe/ssylochnaya-whatsapp-grypp_i22", false},
	{"https://add-groups.com/whatsapp/kazakhstan/almaty/", false},
	{"https://add-groups.com/whatsapp_d090d0bbd0bcd0b0d182d18b-c1420", false},
	{"https://topmsg.ru/wgroup/category/kazaxstan/", false},
	{"https://topmsg.ru/wgroup/wcat-business/", false},
	{"https://topmsg.ru/wgroup/wcat-work/loc-kazakhstan/", false},
	{"https://topmsg.ru/wgroup/wcat-advertisements/loc-kazakhstan/", false},
	{"https://groupsru.com/groups/wagroups-JkfGJg400fXAmNJxdflNRT", false},
	{"https://russia-dropshipping.ru/raznoe/spisok-grupp-whatsapp-katalog-grupp-v-whatsapp-viber-i-telegram.html", false},
	// business media, associations, clubs and hubs: their own channels and groups
	{"https://bizpride.kz/blog/luchshie-soobshchestva-predprinimatelej/", true},
	{"https://weproject.media/articles/detail/gde-biznesmenu-nayti-edinomyshlennikov-v-tsentralnoy-azii-17-klubov-i-soobshchestv/", true},
	{"https://weproject.media/articles/detail/10-soobshchestv-dlya-predprinimateley-v-kazakhstane-kyrgyzstane-i-uzbekistane/", true},
	{"https://kapital.kz/", true},
	{"https://forbes.kz/", true},
	{"https://www.inbusiness.kz/ru", true},
	{"https://kz.kursiv.media/", true},
	{"https://lsm.kz/", true},
	{"https://digitalbusiness.kz/", true},
	{"https://profit.kz/", true},
	{"https://astanahub.com/ru/", true},
	{"https://atameken.kz/ru", true},
	{"https://damu.kz/", true},
	{"https://amcham.kz/", true},
	{"https://delovar.kz/", true},
	{"https://jd.expert/businessclub", true},
	{"https://terricon.kz/", true},
	{"https://www.uchet.kz/", true},
	{"https://dostykhub.kz/", true},
}

// waAggTG: Telegram channels that collect WhatsApp groups (any topic):
// their links count only when about business.
var waAggTG = []string{"whatsapp_group_ru"}

var (
	waGroupRe = regexp.MustCompile(`(?i)(?:https?://)?chat\.whatsapp\.com/(?:invite/)?([A-Za-z0-9]{18,26})`)
	waChanRe  = regexp.MustCompile(`(?i)(?:https?://)?(?:www\.)?whatsapp\.com/channel/([A-Za-z0-9]{18,30})`)
	waAnchor  = regexp.MustCompile(`(?is)<a\s[^>]*href="([^"]+)"[^>]*>(.*?)</a>`)
	waMetaRe  = regexp.MustCompile(`(?i)<meta\s+(?:property|name)="(og:title|og:description|twitter:title)"\s+content="([^"]*)"`)
	waTitleRe = regexp.MustCompile(`(?is)<title>(.*?)</title>`)
	waH3Re    = regexp.MustCompile(`(?is)<h3[^>]*>(.*?)</h3>`)
	waFollow  = regexp.MustCompile(`(?i)(\d[\d\s\x{00a0},.]*\s*[kmкм]?)\s*(followers|подписчик)`)
	waTagRe   = regexp.MustCompile(`<[^>]+>`)

	waEvery    = 24 * time.Hour
	waHTTP     = &http.Client{Timeout: 25 * time.Second}
	waPause    = 1500 * time.Millisecond
	waCheckMax = 120 // links opened per run
	waMaxItems = 400 // WhatsApp communities kept from the discovery
	waRun      sync.Mutex
	waTGPerRun = 60 // catalogue channels searched per run (round robin)
	waMaxCand  = 1500
	waAlmaty   = regexp.MustCompile(`(?i)алматы|almaty`)
)

// The links seen and read live in a server doc (the page does not need them).
const waCandKey = "comm_wa_cand"

func (h *PlatformAI) loadWACand(ctx context.Context) (map[string]*waCand, int) {
	m := map[string]*waCand{}
	d, err := h.repo.GetDoc(ctx, "server", waCandKey)
	if err != nil || d == nil {
		return m, 0
	}
	if !d.Deleted {
		_ = json.Unmarshal([]byte(d.Value), &m)
	}
	if m == nil {
		m = map[string]*waCand{}
	}
	return m, d.Version
}

func (h *PlatformAI) saveWACand(ctx context.Context, base int, m map[string]*waCand) error {
	val, _ := json.Marshal(m)
	_, err := h.repo.PutDoc(ctx, "server", waCandKey, base, string(val), false, "server:communities")
	return err
}

// waCand: a WhatsApp link seen on a page, read or waiting.
type waCand struct {
	Src     string `json:"src"`            // the page or post it was seen on
	Hint    string `json:"hint,omitempty"` // the text next to the link
	Biz     bool   `json:"biz,omitempty"`  // seen on a business page
	N       int    `json:"n"`
	Seen    string `json:"seen,omitempty"`
	Checked string `json:"checked,omitempty"`
	Why     string `json:"why,omitempty"` // why it was not taken
}

type waRunInfo struct {
	At      string `json:"at"`
	Took    string `json:"took,omitempty"`
	Pages   int    `json:"pages"`
	TG      int    `json:"tg"`
	Links   int    `json:"links"` // new links seen this run
	Opened  int    `json:"opened"`
	Added   int    `json:"added"`
	Total   int    `json:"total"`  // WhatsApp communities in the catalogue now
	Cursor  int    `json:"cursor"` // the next catalogue channel to search
	Failed  int    `json:"failed"` // pages that did not open
	Blocked int    `json:"blocked,omitempty"`
}

// waNorm: the canonical link and its kind ("группа" | "канал"), "" if none.
func waNorm(s string) (string, string) {
	if m := waChanRe.FindStringSubmatch(s); m != nil {
		return "https://www.whatsapp.com/channel/" + m[1], "канал"
	}
	if m := waGroupRe.FindStringSubmatch(s); m != nil {
		return "https://chat.whatsapp.com/" + m[1], "группа"
	}
	return "", ""
}

func waPlain(s string) string {
	s = html.UnescapeString(waTagRe.ReplaceAllString(s, " "))
	return strings.Join(strings.Fields(s), " ")
}

func waCut(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return strings.TrimSpace(string(r[:n-1])) + "…"
}

// waFromText: the links in a text with the words around each (the hint).
func waFromText(text string) map[string]string {
	out := map[string]string{}
	for _, re := range []*regexp.Regexp{waChanRe, waGroupRe} {
		for _, ix := range re.FindAllStringIndex(text, -1) {
			u, _ := waNorm(text[ix[0]:ix[1]])
			if u == "" {
				continue
			}
			a, b := ix[0]-160, ix[1]+60
			if a < 0 {
				a = 0
			}
			if b > len(text) {
				b = len(text)
			}
			for a > 0 && !utf8Start(text[a]) {
				a--
			}
			for b < len(text) && !utf8Start(text[b]) {
				b++
			}
			out[u] = waCut(waPlain(text[a:b]), 200)
		}
	}
	return out
}

func utf8Start(c byte) bool { return c&0xC0 != 0x80 }

// waFromHTML: the links of a page (href and plain text) with their anchor
// text, and the same-site links about business to read one level deeper.
func waFromHTML(page, body string) (map[string]string, []string) {
	found := map[string]string{}
	var deeper []string
	base, _ := url.Parse(page)
	seen := map[string]bool{}
	for _, m := range waAnchor.FindAllStringSubmatch(body, -1) {
		href, text := html.UnescapeString(m[1]), waPlain(m[2])
		if u, _ := waNorm(href); u != "" {
			if found[u] == "" {
				found[u] = waCut(text, 200)
			}
			continue
		}
		if base == nil || (!commTopicRe.MatchString(text) && !commTopicRe.MatchString(href)) {
			continue
		}
		ref, err := base.Parse(href)
		if err != nil || ref.Host != base.Host || (ref.Scheme != "http" && ref.Scheme != "https") {
			continue
		}
		ref.Fragment = ""
		if s := ref.String(); s != page && !seen[s] {
			seen[s] = true
			deeper = append(deeper, s)
		}
	}
	for u, h := range waFromText(body) {
		if _, ok := found[u]; !ok {
			found[u] = h
		}
	}
	return found, deeper
}

// waInfo: what a WhatsApp link's own page says.
type waInfo struct {
	Name, Desc string
	Followers  int
}

var waGeneric = regexp.MustCompile(`(?i)^(whatsapp( group invite| channel)?|whatsapp messenger|share on whatsapp|whatsapp web|приглашение в группу whatsapp|канал whatsapp)$`)

// parseWAPage reads the name of a group (invite page) or a channel.
func parseWAPage(body, kind string) waInfo {
	var in waInfo
	meta := map[string]string{}
	for _, m := range waMetaRe.FindAllStringSubmatch(body, -1) {
		if _, ok := meta[strings.ToLower(m[1])]; !ok {
			meta[strings.ToLower(m[1])] = strings.TrimSpace(html.UnescapeString(m[2]))
		}
	}
	name := meta["og:title"]
	if name == "" {
		name = meta["twitter:title"]
	}
	if name == "" || waGeneric.MatchString(name) {
		if m := waH3Re.FindStringSubmatch(body); m != nil && kind == "группа" {
			name = waPlain(m[1])
		}
	}
	if name == "" {
		if m := waTitleRe.FindStringSubmatch(body); m != nil {
			name = waPlain(m[1])
		}
	}
	for _, suf := range []string{" | WhatsApp Channel", " | WhatsApp", " - WhatsApp Channel", " WhatsApp Channel", " | Канал WhatsApp"} {
		name = strings.TrimSuffix(name, suf)
	}
	name = strings.TrimSpace(name)
	if waGeneric.MatchString(name) {
		name = ""
	}
	in.Name = waCut(name, 90)
	in.Desc = waCut(waPlain(meta["og:description"]), 160)
	if m := waFollow.FindStringSubmatch(body); m != nil {
		in.Followers = tgevents.ParseCount(m[1])
	}
	return in
}

func waGet(ctx context.Context, u string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/124.0 Safari/537.36 BSCommunities/1.0")
	req.Header.Set("Accept-Language", "ru,en;q=0.8")
	res, err := waHTTP.Do(req)
	if err != nil {
		return "", err
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return "", &waStatus{res.StatusCode}
	}
	b, err := io.ReadAll(io.LimitReader(res.Body, 3<<20))
	return string(b), err
}

type waStatus struct{ code int }

func (e *waStatus) Error() string { return "ответ " + strconv.Itoa(e.code) }

func waSleep(ctx context.Context) {
	if waPause <= 0 {
		return
	}
	t := time.NewTimer(waPause)
	defer t.Stop()
	select {
	case <-ctx.Done():
	case <-t.C:
	}
}

// waIDOf: the catalogue id of a WhatsApp link.
func waIDOf(u string) string {
	l := strings.ToLower(u)
	l = strings.TrimPrefix(strings.TrimPrefix(l, "https://"), "www.")
	return "wa-" + strings.NewReplacer("/", "-", ".", "-").Replace(l)
}

// waTake: a live link with a name joins when it is about business or was
// seen on a business page.
func waTake(in waInfo, cd *waCand) (bool, string) {
	if in.Name == "" {
		return false, "ссылка не открылась или группа закрыта"
	}
	text := in.Name + " " + in.Desc
	if commTopicRe.MatchString(text) || waSelfRe.MatchString(text) {
		return true, ""
	}
	if cd.Biz && (commTopicRe.MatchString(cd.Hint) || commLocalRe.MatchString(text+" "+cd.Hint)) {
		return true, ""
	}
	if cd.Biz && cd.Hint == "" {
		return true, ""
	}
	return false, "не про бизнес"
}

// waSelfRe: self-development and networking words for a WhatsApp community.
var waSelfRe = regexp.MustCompile(`(?i)клуб|club|community|сообществ|нетворк|ассоциац|палат|chamber|women in|женщин\w* бизнес|предпринимател|кәсіпкер|бизнес-?леди|акселератор|accelerator|hub\b|хаб`)

// refreshWhatsApp: one run of the WhatsApp discovery.
func (h *PlatformAI) refreshWhatsApp(ctx context.Context) (waRunInfo, error) {
	if !waRun.TryLock() {
		return waRunInfo{}, nil
	}
	defer waRun.Unlock()
	start := time.Now()
	day := start.In(tgevents.Almaty).Format("2006-01-02")
	cat, _ := h.loadComm(ctx)
	res := waRunInfo{At: start.UTC().Format(time.RFC3339)}
	cands, candBase := h.loadWACand(ctx)
	cat.WA = cands
	have := map[string]bool{}
	for _, it := range cat.Items {
		if u, _ := waNorm(it.URL); u != "" {
			have[u] = true
		}
	}
	note := func(u, src, hint string, biz bool) {
		if have[u] {
			return
		}
		cd := cat.WA[u]
		if cd == nil {
			cd = &waCand{Src: src}
			cat.WA[u] = cd
			res.Links++ // a link not seen before
		}
		cd.N++
		cd.Seen = day
		if hint != "" && (cd.Hint == "" || biz && !cd.Biz) {
			cd.Hint = hint
		}
		if biz && !cd.Biz {
			cd.Biz, cd.Src = true, src
		}
	}
	// 1) Telegram: the catalogue's channels (newest posts and their search), round robin
	var names []string
	seenTG := map[string]bool{}
	for _, it := range cat.Items {
		if it.Platform != "telegram" {
			continue
		}
		if u := tgName(it.URL); u != "" && !seenTG[strings.ToLower(u)] {
			seenTG[strings.ToLower(u)] = true
			names = append(names, u)
		}
	}
	sort.Strings(names)
	from := 0
	if cat.WARun != nil {
		from = cat.WARun.Cursor
	}
	readTG := func(name, q string, biz bool) {
		u := tgevents.BaseURL + name
		if q != "" {
			u += "?q=" + url.QueryEscape(q)
		}
		body, err := waGet(ctx, u)
		res.TG++
		if err != nil {
			res.Failed++
			return
		}
		for _, p := range tgevents.ParsePage(body).Posts {
			text := p.Text + "\n" + strings.Join(p.Links, "\n")
			for wu, hint := range waFromText(text) {
				note(wu, p.URL(), hint, biz)
			}
		}
	}
	n := len(names)
	for k := 0; k < waTGPerRun && k < n && ctx.Err() == nil; k++ {
		name := names[(from+k)%n]
		readTG(name, "", true)
		waSleep(ctx)
		readTG(name, "whatsapp", true)
		waSleep(ctx)
	}
	if n > 0 {
		res.Cursor = (from + waTGPerRun) % n
	}
	for _, name := range waAggTG {
		for _, q := range []string{"бизнес", "предприниматели", "Казахстан", "Алматы"} {
			if ctx.Err() != nil {
				break
			}
			readTG(name, q, false)
			waSleep(ctx)
		}
	}
	// 2) pages: the researched sources of the catalogue and waPages, one level deeper
	pages := append([]waSrc{}, waPages...)
	seenPg := map[string]bool{}
	for _, p := range pages {
		seenPg[p.URL] = true
	}
	for _, it := range cat.Items {
		for _, u := range []string{it.Src, it.URL} {
			if strings.HasPrefix(u, "http") && !strings.Contains(u, "t.me/") && !strings.Contains(u, "instagram.com") &&
				!strings.Contains(u, "facebook.com") && !strings.Contains(u, "linkedin.com") && !strings.Contains(u, "whatsapp.com") &&
				!strings.Contains(u, "youtube.com") && !strings.Contains(u, "threads.") && !seenPg[u] {
				seenPg[u] = true
				pages = append(pages, waSrc{u, true})
			}
		}
	}
	for _, p := range pages {
		if ctx.Err() != nil {
			break
		}
		body, err := waGet(ctx, p.URL)
		res.Pages++
		waSleep(ctx)
		if err != nil {
			res.Failed++
			continue
		}
		found, deeper := waFromHTML(p.URL, body)
		for u, hint := range found {
			note(u, p.URL, hint, p.Biz)
		}
		lim := 4 // a business page: its own channel is on the page or one step away
		if !p.Biz {
			lim = 15 // a directory: the groups are on their own pages
		}
		if len(deeper) > lim {
			deeper = deeper[:lim]
		}
		for _, d := range deeper {
			if ctx.Err() != nil || seenPg[d] {
				continue
			}
			seenPg[d] = true
			b2, err := waGet(ctx, d)
			res.Pages++
			waSleep(ctx)
			if err != nil {
				continue
			}
			f2, _ := waFromHTML(d, b2)
			for u, hint := range f2 {
				note(u, d, hint, p.Biz)
			}
		}
	}
	// 3) open the links: never read, or read more than 30 days ago (not taken)
	var todo []string
	for u, cd := range cat.WA {
		if have[u] {
			continue
		}
		if cd.Checked != "" {
			if t, err := time.Parse("2006-01-02", cd.Checked); err == nil && start.Sub(t) < 30*24*time.Hour {
				continue
			}
		}
		todo = append(todo, u)
	}
	sort.Slice(todo, func(i, j int) bool {
		a, b := cat.WA[todo[i]], cat.WA[todo[j]]
		if a.Biz != b.Biz {
			return a.Biz
		}
		if a.N != b.N {
			return a.N > b.N
		}
		return todo[i] < todo[j]
	})
	if len(todo) > waCheckMax {
		todo = todo[:waCheckMax]
	}
	var added []commItem
	for _, u := range todo {
		if ctx.Err() != nil {
			break
		}
		cd := cat.WA[u]
		_, kind := waNorm(u)
		body, err := waGet(ctx, u)
		res.Opened++
		waSleep(ctx)
		cd.Checked = day
		if err != nil {
			cd.Why = "не открылась: " + err.Error()
			if se, ok := err.(*waStatus); ok && (se.code == 403 || se.code == 429) {
				res.Blocked++
			}
			continue
		}
		in := parseWAPage(body, kind)
		ok, why := waTake(in, cd)
		if !ok {
			cd.Why = why
			continue
		}
		cd.Why = ""
		text := in.Name + " " + in.Desc + " " + cd.Hint
		city := "СНГ"
		if commLocalRe.MatchString(text) || strings.HasSuffix(hostOf(cd.Src), ".kz") {
			city = "Казахстан"
		}
		if waAlmaty.MatchString(text) {
			city = "Алматы"
		}
		topic := in.Desc
		if topic == "" {
			topic = cd.Hint
		}
		k := "чат"
		if kind == "канал" {
			k = "канал"
		}
		it := commItem{ID: waIDOf(u), Name: in.Name, URL: u, Platform: "whatsapp", Kind: k, Cat: "WhatsApp: найдено",
			Topic: waCut(topic, 140), Lang: "ru", City: city, Join: "открыто", Why: []string{"лиды", "партнёрства"},
			Src: cd.Src, Checked: day, Size: in.Followers, Origin: "found", Found: day}
		added = append(added, it)
		have[u] = true
		log.Printf("communities: whatsapp +%s %q %s (src %s)", k, in.Name, u, cd.Src)
	}
	// forget the oldest links not taken (keep the map small)
	if len(cat.WA) > waMaxCand {
		var ks []string
		for k := range cat.WA {
			ks = append(ks, k)
		}
		sort.Slice(ks, func(i, j int) bool { return cat.WA[ks[i]].Seen < cat.WA[ks[j]].Seen })
		for _, k := range ks[:len(cat.WA)-waMaxCand] {
			delete(cat.WA, k)
		}
	}
	res.Took = time.Since(start).Round(time.Second).String()
	err := h.mutateComm(ctx, func(c *commCat) bool {
		ids := map[string]bool{}
		nWA := 0
		for _, it := range c.Items {
			ids[it.ID] = true
			if it.Platform == "whatsapp" && it.Origin == "found" {
				nWA++
			}
		}
		for _, it := range added {
			if !ids[it.ID] && nWA < waMaxItems {
				c.Items = append(c.Items, it)
				ids[it.ID] = true
				nWA++
				res.Added++
			}
		}
		total := 0
		for _, it := range c.Items {
			if it.Platform == "whatsapp" {
				total++
			}
		}
		res.Total = total
		r := res
		c.WARun = &r
		c.Updated = res.At
		return true
	})
	if werr := h.saveWACand(ctx, candBase, cat.WA); werr != nil {
		log.Printf("communities: whatsapp candidates not saved: %v", werr)
	}
	log.Printf("communities: whatsapp: %d pages, %d telegram reads, %d links, %d opened (%d blocked), %d added, %d WhatsApp in the catalogue; failed %d; took %s; err=%v",
		res.Pages, res.TG, res.Links, res.Opened, res.Blocked, res.Added, res.Total, res.Failed, res.Took, err)
	return res, err
}

func hostOf(u string) string {
	p, err := url.Parse(u)
	if err != nil {
		return ""
	}
	return strings.ToLower(p.Host)
}

// WhatsAppCommLoop: the first run a few minutes after start (then daily,
// counting from the last run).
func (h *PlatformAI) WhatsAppCommLoop(ctx context.Context) {
	if h.repo == nil {
		return
	}
	wait := 6 * time.Minute
	if c, _ := h.loadComm(ctx); c.WARun != nil {
		if t, err := time.Parse(time.RFC3339, c.WARun.At); err == nil {
			if left := waEvery - time.Since(t); left > wait {
				wait = left
			}
		}
	}
	log.Printf("communities: whatsapp discovery in %s", wait.Round(time.Minute))
	timer := time.NewTimer(wait)
	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
		}
		rctx, cancel := context.WithTimeout(ctx, 50*time.Minute)
		_, _ = h.refreshWhatsApp(rctx)
		cancel()
		timer.Reset(waEvery)
	}
}
