package http

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/bnursik/business_surgery_backend/internal/content"
	"github.com/bnursik/business_surgery_backend/internal/tgevents"
	"github.com/gin-gonic/gin"
)

// ── R77: Маркетинг → «Сообщества» ──
// «Добавь парсинг различных сообществ в соцсетях по бизнесу или
// саморазвитию, чтобы у меня было очень много ссылок и мог вступить».
//
// The catalogue lives in the club doc bs_comm_cat, written only by the
// server: the researched seed (content/communities.json, every link seen on
// a page on 10.10.2026), the Telegram numbers (subscribers or members, the
// last post) refreshed once a day from the public pages t.me/s/<name>, and
// the communities found by the daily discovery: t.me/<name> links in the
// posts of the channels already read; a name mentioned in them is read
// once, and a public channel or group about business with enough people
// joins the catalogue as «новое». The owner's statuses and notes live in
// his own doc bs_communities (the page writes it), so the two never collide.

const (
	commCatKey  = "bs_comm_cat"
	commMaxCand = 600 // names remembered from mentions
	commMaxNew  = 400 // found communities kept in the catalogue
)

var (
	commEvery     = 24 * time.Hour
	commPause     = 2 * time.Second
	commHTTP      = &http.Client{Timeout: 25 * time.Second}
	commCheckNew  = 30  // mentioned names read per run
	commMinPeople = 300 // a found community needs at least that many
	commRun       sync.Mutex
	commBusy      sync.Mutex // guards commRunning
	commRunning   bool
)

type commItem struct {
	ID       string   `json:"id"`
	Name     string   `json:"name"`
	URL      string   `json:"url"`
	Platform string   `json:"platform"`
	Kind     string   `json:"kind,omitempty"`
	Cat      string   `json:"cat,omitempty"`
	Topic    string   `json:"topic,omitempty"`
	Lang     string   `json:"lang,omitempty"`
	City     string   `json:"city,omitempty"`
	Join     string   `json:"join,omitempty"`
	Why      []string `json:"why,omitempty"`
	Src      string   `json:"src,omitempty"`
	Checked  string   `json:"checked,omitempty"`
	Size     int      `json:"size,omitempty"`
	Origin   string   `json:"origin,omitempty"` // seed | found
	Found    string   `json:"found,omitempty"`  // the day discovery added it
	From     []string `json:"from,omitempty"`   // channels that mentioned it
}

// commTG: the numbers of one Telegram community from its public page.
type commTG struct {
	Title string `json:"title,omitempty"`
	Subs  int    `json:"subs,omitempty"`
	Group bool   `json:"group,omitempty"`
	Last  string `json:"last,omitempty"` // the newest post, RFC 3339 (channels)
	At    string `json:"at,omitempty"`
	Err   string `json:"err,omitempty"`
}

// commCand: a name mentioned in the channels, not read yet or rejected.
type commCand struct {
	N       int      `json:"n"`
	From    []string `json:"from,omitempty"`
	Seen    string   `json:"seen,omitempty"`    // the last day it was mentioned
	Checked string   `json:"checked,omitempty"` // the day it was read
	Why     string   `json:"why,omitempty"`     // why it was not taken
}

type commRunInfo struct {
	At      string `json:"at"`
	Took    string `json:"took,omitempty"`
	Checked int    `json:"checked"`
	OK      int    `json:"ok"`
	Failed  int    `json:"failed"`
	Mention int    `json:"mentions"`
	Read    int    `json:"read"`  // mentioned names read
	Added   int    `json:"added"` // of them added as «новые»
}

type commCat struct {
	Updated string               `json:"updated,omitempty"`
	Items   []commItem           `json:"items"`
	TG      map[string]*commTG   `json:"tg,omitempty"`
	Cand    map[string]*commCand `json:"cand,omitempty"`
	Run     *commRunInfo         `json:"run,omitempty"`
	WARun   *waRunInfo           `json:"waRun,omitempty"` // R83: the WhatsApp discovery (r83_whatsapp.go)
	WA      map[string]*waCand   `json:"-"`               // R83: kept in the server doc comm_wa_cand
}

func commSeed() []commItem {
	var list []commItem
	if err := json.Unmarshal(content.Communities, &list); err != nil {
		log.Printf("communities: seed: %v", err)
	}
	for i := range list {
		list[i].Origin = "seed"
	}
	return list
}

// tgName: the public username of a t.me link ("" for an invite link).
func tgName(u string) string {
	if !strings.Contains(u, "t.me/") && !strings.Contains(u, "telegram.me/") {
		return ""
	}
	return cleanTGUser(u)
}

func (h *PlatformAI) loadComm(ctx context.Context) (commCat, int) {
	var c commCat
	d, err := h.repo.GetDoc(ctx, "club", commCatKey)
	if err != nil || d == nil {
		return c, 0
	}
	if !d.Deleted {
		_ = json.Unmarshal([]byte(d.Value), &c)
	}
	return c, d.Version
}

// mutateComm applies fn and writes, again on a version conflict; fn says
// whether anything changed.
func (h *PlatformAI) mutateComm(ctx context.Context, fn func(c *commCat) bool) error {
	var err error
	for try := 0; try < 4; try++ {
		c, base := h.loadComm(ctx)
		if c.TG == nil {
			c.TG = map[string]*commTG{}
		}
		if c.Cand == nil {
			c.Cand = map[string]*commCand{}
		}
		if !fn(&c) && base > 0 {
			return nil
		}
		if c.Items == nil {
			c.Items = []commItem{}
		}
		val, _ := json.Marshal(c)
		if _, err = h.repo.PutDoc(ctx, "club", commCatKey, base, string(val), false, "server:communities"); err == nil {
			return nil
		}
	}
	return err
}

// mergeCommSeed puts the seed into the catalogue: a new seed row is added, a
// seed row already there takes the new text; found rows stay as they are.
func mergeCommSeed(c *commCat, seed []commItem) bool {
	at := map[string]int{}
	for i, it := range c.Items {
		at[it.ID] = i
	}
	changed := false
	for _, s := range seed {
		if i, ok := at[s.ID]; ok {
			if c.Items[i].Origin == "found" {
				continue
			}
			old, _ := json.Marshal(c.Items[i])
			now, _ := json.Marshal(s)
			if string(old) != string(now) {
				c.Items[i], changed = s, true
			}
			continue
		}
		c.Items = append(c.Items, s)
		at[s.ID] = len(c.Items) - 1
		changed = true
	}
	return changed
}

// SeedCommunities: the catalogue on start (the seed may grow between versions).
func (h *PlatformAI) SeedCommunities(ctx context.Context) {
	if h.repo == nil {
		return
	}
	seed := commSeed()
	if len(seed) == 0 {
		return
	}
	n := 0
	err := h.mutateComm(ctx, func(c *commCat) bool {
		before := len(c.Items)
		ch := mergeCommSeed(c, seed)
		n = len(c.Items) - before
		return ch
	})
	if err != nil {
		log.Printf("communities: seed not written: %v", err)
	} else if n > 0 {
		log.Printf("communities: %d seed communities added", n)
	}
}

// commTopicRe: a found channel must be about business, money, marketing,
// startups or growth (title or description).
var commTopicRe = regexp.MustCompile(`(?i)бизнес|предприним|собственник|стартап|startup|business|founder|фаундер|маркетинг|marketing|продаж|sales|smm|инвест|invest|венчур|venture|финанс|финанс|деньг|карьер|саморазвит|нетворкинг|networking|менедж|управлен|e-?com|маркетплейс|kaspi|франшиз|tender|тендер|экономик|кәсіп|кәсіпкер|ақша`)

// commLocalRe: a hint that it is about Kazakhstan or Central Asia.
var commLocalRe = regexp.MustCompile(`(?i)казахстан|kazakhstan|qazaq|қазақ|алматы|almaty|астан|astana|шымкент|караганд|kz\b|\.kz|центральн\w* ази|central asia`)

// noteMentions counts the names a channel's posts point to.
func noteMentions(c *commCat, from string, names []string, day string, known map[string]bool) int {
	n := 0
	for _, u := range names {
		l := strings.ToLower(u)
		if known[l] {
			continue
		}
		cd := c.Cand[l]
		if cd == nil {
			cd = &commCand{}
			c.Cand[l] = cd
		}
		cd.N++
		cd.Seen = day
		if len(cd.From) < 5 && !containsStr(cd.From, from) {
			cd.From = append(cd.From, from)
		}
		n++
	}
	return n
}

func containsStr(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

// trimCand keeps the names mentioned most (and most recently).
func trimCand(c *commCat) {
	if len(c.Cand) <= commMaxCand {
		return
	}
	type kv struct {
		k string
		v *commCand
	}
	var all []kv
	for k, v := range c.Cand {
		all = append(all, kv{k, v})
	}
	sort.Slice(all, func(i, j int) bool {
		if all[i].v.N != all[j].v.N {
			return all[i].v.N > all[j].v.N
		}
		return all[i].v.Seen > all[j].v.Seen
	})
	keep := map[string]*commCand{}
	for _, x := range all[:commMaxCand] {
		keep[x.k] = x.v
	}
	c.Cand = keep
}

// pickCand: the names to read this run: mentioned most, never read or read
// more than 30 days ago.
func pickCand(c *commCat, now time.Time, limit int) []string {
	var out []string
	for k, v := range c.Cand {
		if v.Checked != "" {
			if t, err := time.Parse("2006-01-02", v.Checked); err == nil && now.Sub(t) < 30*24*time.Hour {
				continue
			}
		}
		out = append(out, k)
	}
	sort.Slice(out, func(i, j int) bool {
		a, b := c.Cand[out[i]], c.Cand[out[j]]
		if a.N != b.N {
			return a.N > b.N
		}
		return out[i] < out[j]
	})
	if len(out) > limit {
		out = out[:limit]
	}
	return out
}

// foundItem: a mentioned name that turned out a public community about business.
func foundItem(u string, in tgevents.Info, cd *commCand, day string) (commItem, string) {
	if in.Subs < commMinPeople {
		return commItem{}, "мало участников"
	}
	text := in.Title + " " + in.Desc
	if !commTopicRe.MatchString(text) {
		return commItem{}, "не про бизнес"
	}
	kind, cat := "канал", "Найдено в каналах"
	if in.Group {
		kind = "чат"
	}
	city := "СНГ"
	if commLocalRe.MatchString(text) {
		city = "Казахстан"
	}
	desc := []rune(strings.Join(strings.Fields(in.Desc), " "))
	if len(desc) > 140 {
		desc = append(desc[:139], '…')
	}
	return commItem{
		ID: "tg-" + strings.ToLower(u), Name: in.Title, URL: "https://t.me/" + u, Platform: "telegram",
		Kind: kind, Cat: cat, Topic: string(desc), Lang: "ru", City: city, Join: "открыто",
		Why: []string{"лиды"}, Src: "https://t.me/" + cd.firstFrom(), Checked: day, Size: in.Subs,
		Origin: "found", Found: day, From: cd.From,
	}, ""
}

func (cd *commCand) firstFrom() string {
	if cd == nil || len(cd.From) == 0 {
		return ""
	}
	return cd.From[0]
}

func commSleep(ctx context.Context) {
	if commPause <= 0 {
		return
	}
	t := time.NewTimer(commPause)
	defer t.Stop()
	select {
	case <-ctx.Done():
	case <-t.C:
	}
}

// refreshCommunities reads every Telegram community of the catalogue, then
// the names they mention most, and writes the numbers and the new finds.
func (h *PlatformAI) refreshCommunities(ctx context.Context) (commRunInfo, error) {
	if !commRun.TryLock() {
		return commRunInfo{}, errors.New("обновление уже идёт")
	}
	defer commRun.Unlock()
	start := time.Now()
	day := start.In(tgevents.Almaty).Format("2006-01-02")
	cat, _ := h.loadComm(ctx)
	if cat.Cand == nil {
		cat.Cand = map[string]*commCand{}
	}
	known := map[string]bool{}
	var names []string
	for _, it := range cat.Items {
		if it.Platform != "telegram" {
			continue
		}
		if u := tgName(it.URL); u != "" && !known[strings.ToLower(u)] {
			known[strings.ToLower(u)] = true
			names = append(names, u)
		}
	}
	res := commRunInfo{At: start.UTC().Format(time.RFC3339)}
	tg := map[string]*commTG{}
	var failed []string
	for i, u := range names {
		if ctx.Err() != nil {
			break
		}
		if i > 0 {
			commSleep(ctx)
		}
		res.Checked++
		st := &commTG{At: time.Now().UTC().Format(time.RFC3339)}
		in, err := tgevents.FetchInfo(ctx, commHTTP, u)
		if err != nil {
			st.Err = err.Error()
			if prev := cat.TG[strings.ToLower(u)]; prev != nil { // keep the last good numbers
				st.Title, st.Subs, st.Group, st.Last = prev.Title, prev.Subs, prev.Group, prev.Last
			}
			res.Failed++
			failed = append(failed, "@"+u)
		} else {
			res.OK++
			st.Title, st.Subs, st.Group = in.Title, in.Subs, in.Group
			if !in.Last.IsZero() {
				st.Last = in.Last.UTC().Format(time.RFC3339)
			}
			res.Mention += noteMentions(&cat, u, tgevents.Mentions(in.Posts), day, known)
		}
		tg[strings.ToLower(u)] = st
	}
	// discovery: read the names mentioned most
	var added []commItem
	for i, u := range pickCand(&cat, start, commCheckNew) {
		if ctx.Err() != nil {
			break
		}
		if i > 0 || len(names) > 0 {
			commSleep(ctx)
		}
		cd := cat.Cand[u]
		cd.Checked = day
		res.Read++
		in, err := tgevents.FetchInfo(ctx, commHTTP, u)
		if err != nil {
			cd.Why = "не публичный канал или группа"
			continue
		}
		it, why := foundItem(u, in, cd, day)
		if why != "" {
			cd.Why = why
			continue
		}
		added = append(added, it)
		tg[strings.ToLower(u)] = &commTG{Title: in.Title, Subs: in.Subs, Group: in.Group, At: time.Now().UTC().Format(time.RFC3339),
			Last: func() string {
				if in.Last.IsZero() {
					return ""
				}
				return in.Last.UTC().Format(time.RFC3339)
			}()}
	}
	res.Added = len(added)
	trimCand(&cat)
	res.Took = time.Since(start).Round(time.Second).String()
	err := h.mutateComm(ctx, func(c *commCat) bool {
		for k, v := range tg {
			c.TG[k] = v
		}
		c.Cand = cat.Cand
		have := map[string]bool{}
		for _, it := range c.Items {
			have[it.ID] = true
		}
		nFound := 0
		for _, it := range c.Items {
			if it.Origin == "found" {
				nFound++
			}
		}
		for _, it := range added {
			if !have[it.ID] && nFound < commMaxNew {
				c.Items = append(c.Items, it)
				have[it.ID] = true
				nFound++
			}
		}
		r := res
		c.Run = &r
		c.Updated = res.At
		return true
	})
	log.Printf("communities: refresh: %d telegram checked, %d ok, %d failed %v; %d mentions, %d names read, %d added as new; took %s; err=%v",
		res.Checked, res.OK, res.Failed, failed, res.Mention, res.Read, res.Added, res.Took, err)
	return res, err
}

// CommunitiesLoop: the seed at once, then the refresh once a day (after a
// restart it waits for the rest of the day since the last run).
func (h *PlatformAI) CommunitiesLoop(ctx context.Context) {
	if h.repo == nil {
		return
	}
	h.SeedCommunities(ctx)
	wait := 4 * time.Minute
	if c, _ := h.loadComm(ctx); c.Run != nil {
		if t, err := time.Parse(time.RFC3339, c.Run.At); err == nil {
			if left := commEvery - time.Since(t); left > wait {
				wait = left
			}
		}
	}
	log.Printf("communities: next refresh in %s", wait.Round(time.Minute))
	timer := time.NewTimer(wait)
	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
		}
		h.runCommunities(ctx)
		timer.Reset(commEvery)
	}
}

func (h *PlatformAI) runCommunities(ctx context.Context) {
	commBusy.Lock()
	if commRunning {
		commBusy.Unlock()
		return
	}
	commRunning = true
	commBusy.Unlock()
	defer func() { commBusy.Lock(); commRunning = false; commBusy.Unlock() }()
	rctx, cancel := context.WithTimeout(ctx, 40*time.Minute)
	defer cancel()
	_, _ = h.refreshCommunities(rctx)
}

// CommunitiesRefresh: «Обновить сейчас» (the team): the run goes on in the
// background, the catalogue comes back by the sync.
func (h *PlatformAI) CommunitiesRefresh(c *gin.Context) {
	if isResident(c) {
		forbidden(c, "team_only")
		return
	}
	if h.repo == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "no storage"})
		return
	}
	commBusy.Lock()
	busy := commRunning
	commBusy.Unlock()
	if busy {
		c.JSON(http.StatusOK, gin.H{"started": false, "busy": true})
		return
	}
	go h.runCommunities(context.WithoutCancel(c.Request.Context()))
	c.JSON(http.StatusOK, gin.H{"started": true})
}
