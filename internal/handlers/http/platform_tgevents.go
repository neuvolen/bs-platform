package http

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"os"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/bnursik/business_surgery_backend/internal/ai"
	"github.com/bnursik/business_surgery_backend/internal/tgevents"
)

// ── Telegram channels in the events feed ──
// Besides the morning web search, the feed reads public Telegram channels
// with announcements through their web preview t.me/s/<username> (no login),
// every 2–3 hours, only the posts after the last one seen. Rules read the
// date, time, place, link, organizer and application deadline; the free text
// model reads what the rules cannot (digests, unclear dates). Every run also
// removes what has passed, so the feed holds only current events: an event
// goes after its day, an opportunity (grant, programme, contest; kind
// «возможность», dated by its deadline) after the deadline.

type tgSource struct {
	Key      string   // the state key in the feed doc
	Name     string   // as the owner calls it
	Username string   // known username
	Guess    []string // usernames to try when it is not known
	Match    string   // the channel title must contain it (lower case)
}

// tgSources: the owner's channels (R70). The Zula channel was not found by a
// web search, so the server tries likely usernames once a day and keeps the
// one whose title says Zula; the team can also add a channel in Мероприятия.
var tgSources = []tgSource{
	{Key: "startup_course_com", Name: "Возможности Startup course", Username: "startup_course_com"},
	// found on the first run on the server by its title: @opportunities_zula
	{Key: "zula", Name: "Opportunities with Zula", Username: "opportunities_zula", Match: "zula", Guess: []string{
		"opportunitieswithzula", "opportunities_with_zula", "zulaopportunities", "zula_opportunities",
		"oppswithzula", "opps_with_zula", "zulaopps", "zula_opps", "opportunitieszula", "opportunities_zula",
		"zula_opportunity", "zulaopportunity", "withzula", "zula_kz",
	}},
}

// eventsTGKey: club doc {channels:["username"]}: channels the team added.
const eventsTGKey = "bs_events_tg"

var (
	tgEvery    = 150 * time.Minute
	tgPause    = 3 * time.Second // between two requests to t.me
	tgHTTP     = &http.Client{Timeout: 25 * time.Second}
	tgMaxItems = 200
	tgUserRe   = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_]{3,31}$`)
	// tgRun: one run at a time (the loop, the morning refresh and the button
	// may meet right after a start)
	tgRun sync.Mutex
)

type tgState struct {
	Name     string `json:"name,omitempty"`
	Username string `json:"username,omitempty"`
	Title    string `json:"title,omitempty"`
	Last     int    `json:"last,omitempty"`
	At       string `json:"at,omitempty"`
	Found    int    `json:"found,omitempty"` // events taken in the last run
	Total    int    `json:"total,omitempty"` // in the feed now
	Err      string `json:"err,omitempty"`
	Tried    string `json:"tried,omitempty"` // the day the usernames were guessed
}

// cleanTGUser: "https://t.me/s/name", "@name", "t.me/name/12" → "name".
func cleanTGUser(s string) string {
	s = strings.TrimSpace(s)
	for _, p := range []string{"https://", "http://", "www.", "t.me/", "telegram.me/", "s/", "@"} {
		s = strings.TrimPrefix(s, p)
	}
	if i := strings.IndexAny(s, "/?#"); i >= 0 {
		s = s[:i]
	}
	if !tgUserRe.MatchString(s) {
		return ""
	}
	return s
}

func (h *PlatformAI) tgSourceList(ctx context.Context) []tgSource {
	out := append([]tgSource{}, tgSources...)
	seen := map[string]bool{}
	for _, s := range out {
		seen[strings.ToLower(s.Username)] = true
	}
	var extra []string
	for _, s := range strings.Split(os.Getenv("EVENTS_TG_CHANNELS"), ",") {
		extra = append(extra, s)
	}
	if h.repo != nil {
		if d, err := h.repo.GetDoc(ctx, "club", eventsTGKey); err == nil && d != nil && !d.Deleted {
			var v struct {
				Channels []string `json:"channels"`
			}
			_ = json.Unmarshal([]byte(d.Value), &v)
			extra = append(extra, v.Channels...)
		}
	}
	for _, s := range extra {
		u := cleanTGUser(s)
		if u == "" || seen[strings.ToLower(u)] {
			continue
		}
		seen[strings.ToLower(u)] = true
		out = append(out, tgSource{Key: strings.ToLower(u), Name: "@" + u, Username: u})
	}
	return out
}

// ── the feed doc ──

type eventsFeed struct {
	Updated string              `json:"updated,omitempty"`
	Tried   string              `json:"tried,omitempty"` // R70: the last refresh run, whatever it found
	Items   []ai.Event          `json:"items"`
	TG      map[string]*tgState `json:"tg,omitempty"`
	Cleaned *feedCleaned        `json:"cleaned,omitempty"`
}

// feedCleaned: the daily removal of passed events (counts only).
type feedCleaned struct {
	Day     string `json:"day"`
	Removed int    `json:"removed"` // on that day
	Total   int    `json:"total"`   // since counting began
}

func (h *PlatformAI) loadFeed(ctx context.Context) (eventsFeed, int) {
	var f eventsFeed
	d, err := h.repo.GetDoc(ctx, "club", eventsFeedKey)
	if err != nil || d == nil {
		return f, 0
	}
	if !d.Deleted {
		_ = json.Unmarshal([]byte(d.Value), &f)
	}
	return f, d.Version
}

// mutateFeed applies fn to the feed and writes it, again on a version conflict.
func (h *PlatformAI) mutateFeed(ctx context.Context, fn func(f *eventsFeed)) error {
	var err error
	for try := 0; try < 4; try++ {
		f, base := h.loadFeed(ctx)
		if f.TG == nil {
			f.TG = map[string]*tgState{}
		}
		fn(&f)
		if f.Items == nil {
			f.Items = []ai.Event{}
		}
		val, _ := json.Marshal(f)
		if _, err = h.repo.PutDoc(ctx, "club", eventsFeedKey, base, string(val), false, "server:events"); err == nil {
			return nil
		}
	}
	return err
}

// pruneFeed removes what has passed (an opportunity is dated by its
// deadline) and counts it for the day.
func pruneFeed(f *eventsFeed, today string) int {
	keep := f.Items[:0]
	n := 0
	for _, e := range f.Items {
		d := e.Date
		if e.Kind == tgevents.KindOpportunity && e.Deadline != "" {
			d = e.Deadline
		}
		if e.Title == "" || len(d) != 10 || d < today {
			n++
			continue
		}
		keep = append(keep, e)
	}
	f.Items = keep
	if f.Cleaned == nil || f.Cleaned.Day != today {
		prev := 0
		if f.Cleaned != nil {
			prev = f.Cleaned.Total
		}
		f.Cleaned = &feedCleaned{Day: today, Total: prev}
	}
	f.Cleaned.Removed += n
	f.Cleaned.Total += n
	return n
}

// evKey: the same event from two sources: the title's letters and the date.
func evKey(e ai.Event) string {
	var b strings.Builder
	n := 0
	for _, r := range strings.ToLower(e.Title) {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			b.WriteRune(r)
			if n++; n >= 40 {
				break
			}
		}
	}
	return b.String() + "|" + e.Date
}

// mergeEvents adds add to base without doubles: the same title and date, or
// the same link outside Telegram, is one event; the Telegram one wins and
// takes the fields it lacks from the other.
func mergeEvents(base, add []ai.Event) []ai.Event {
	out := append([]ai.Event{}, base...)
	byKey, byURL := map[string]int{}, map[string]int{}
	index := func(i int) {
		byKey[evKey(out[i])] = i
		if u := out[i].URL; u != "" && !strings.Contains(u, "t.me/") {
			byURL[u] = i
		}
	}
	for i := range out {
		index(i)
	}
	for _, e := range add {
		i, ok := byKey[evKey(e)]
		if !ok && e.URL != "" && !strings.Contains(e.URL, "t.me/") {
			i, ok = byURL[e.URL]
			ok = ok && out[i].Date == e.Date
		}
		if !ok {
			out = append(out, e)
			index(len(out) - 1)
			continue
		}
		win, lose := out[i], e
		if lose.Origin == "tg" && win.Origin != "tg" {
			win, lose = lose, win
		}
		fill := func(a *string, b string) {
			if *a == "" {
				*a = b
			}
		}
		fill(&win.Time, lose.Time)
		fill(&win.Place, lose.Place)
		fill(&win.Price, lose.Price)
		fill(&win.Org, lose.Org)
		fill(&win.Desc, lose.Desc)
		fill(&win.Deadline, lose.Deadline)
		if strings.Contains(win.URL, "t.me/") && lose.URL != "" && !strings.Contains(lose.URL, "t.me/") {
			win.URL = lose.URL
		}
		if len(win.Tags) == 0 {
			win.Tags = lose.Tags
		}
		out[i] = win
	}
	return out
}

func sortFeed(items []ai.Event) []ai.Event {
	sort.SliceStable(items, func(i, j int) bool { return items[i].Date+items[i].Time < items[j].Date+items[j].Time })
	if len(items) > tgMaxItems {
		items = items[:tgMaxItems]
	}
	return items
}

// ── reading the channels ──

func tgSleep(ctx context.Context) {
	t := time.NewTimer(tgPause)
	defer t.Stop()
	select {
	case <-ctx.Done():
	case <-t.C:
	}
}

// resolve: the username of a source; for a guessed one, once a day.
func resolveTG(ctx context.Context, s tgSource, st *tgState, today string) (string, tgevents.Page, error) {
	if s.Username != "" {
		pg, err := tgevents.Fetch(ctx, tgHTTP, s.Username, 0)
		return s.Username, pg, err
	}
	if st.Username != "" {
		pg, err := tgevents.Fetch(ctx, tgHTTP, st.Username, 0)
		if !errors.Is(err, tgevents.ErrNoChannel) {
			return st.Username, pg, err
		}
		st.Username = ""
	}
	if st.Tried == today {
		return "", tgevents.Page{}, errors.New("канал не найден, следующая попытка завтра; добавьте ссылку на канал в Мероприятиях")
	}
	st.Tried = today
	for i, u := range s.Guess {
		if i > 0 {
			tgSleep(ctx)
		}
		pg, err := tgevents.Fetch(ctx, tgHTTP, u, 0)
		if err == nil && len(pg.Posts) > 0 && strings.Contains(strings.ToLower(pg.Title), s.Match) {
			log.Printf("events tg: %q found as @%s (%s)", s.Name, u, pg.Title)
			return u, pg, nil
		}
		if ctx.Err() != nil {
			break
		}
	}
	return "", tgevents.Page{}, errors.New("канал не найден по вероятным именам; добавьте ссылку на канал в Мероприятиях")
}

// readTG: the new posts of one channel turned into events.
func (h *PlatformAI) readTG(ctx context.Context, s tgSource, st *tgState, now time.Time) []ai.Event {
	today := now.In(tgevents.Almaty).Format("2006-01-02")
	u, pg, err := resolveTG(ctx, s, st, today)
	st.Name = s.Name
	st.At = now.UTC().Format(time.RFC3339)
	st.Found = 0
	if err != nil {
		st.Err = err.Error()
		log.Printf("events tg: %q: %v", s.Name, err)
		return nil
	}
	st.Username, st.Err = u, ""
	if pg.Title != "" {
		st.Title = pg.Title
	}
	posts := pg.Posts
	// The first run reads two more pages back (opportunities stay open for
	// weeks); later runs one more page only when the newest one skipped posts.
	more := 0
	if st.Last == 0 {
		more = 2
	} else if len(posts) > 0 && posts[0].ID > st.Last+1 {
		more = 1
	}
	before := pg.Before
	for ; more > 0 && before > 0 && ctx.Err() == nil; more-- {
		tgSleep(ctx)
		older, err := tgevents.Fetch(ctx, tgHTTP, u, before)
		if err != nil {
			break
		}
		posts = append(older.Posts, posts...)
		before = older.Before
	}
	var fresh []tgevents.Post
	last := st.Last
	seenID := map[int]bool{}
	for _, p := range posts {
		if seenID[p.ID] {
			continue
		}
		seenID[p.ID] = true
		if p.ID > st.Last {
			fresh = append(fresh, p)
		}
		if p.ID > last {
			last = p.ID
		}
	}
	var out []ai.Event
	var hard []tgevents.Post
	for _, p := range fresh {
		r := tgevents.Extract(p, today)
		switch {
		case r.OK:
			out = append(out, r.Event)
		case r.NeedAI:
			hard = append(hard, p)
		}
	}
	rules := len(out)
	nAI := 0
	if len(hard) > 0 && h.AI != nil && h.AI.HasText() {
		if len(hard) > 5 {
			hard = hard[len(hard)-5:] // a short answer is not cut off
		}
		actx, cancel := context.WithTimeout(ctx, 2*time.Minute)
		evs, err := tgevents.AIExtract(actx, h.AI.JSON, hard, now)
		cancel()
		if err != nil {
			log.Printf("events tg: @%s: ИИ не разобрал %d постов: %v", u, len(hard), err)
		}
		nAI = len(evs)
		out = append(out, evs...)
	}
	st.Last, st.Found = last, len(out)
	log.Printf("events tg: @%s fetched: %d posts on the pages, %d new, %d events (rules %d, ai %d of %d posts), last=%d",
		u, len(seenID), len(fresh), len(out), rules, nAI, len(hard), last)
	return out
}

// refreshTG reads every channel and writes the feed (also removing what has
// passed, even when no channel answers).
func (h *PlatformAI) refreshTG(ctx context.Context) (int, error) {
	if !tgRun.TryLock() {
		log.Printf("events tg: a run is going on, skipped")
		return 0, nil
	}
	defer tgRun.Unlock()
	now := time.Now()
	today := now.In(tgevents.Almaty).Format("2006-01-02")
	f, _ := h.loadFeed(ctx)
	states := map[string]*tgState{}
	for k, v := range f.TG {
		states[k] = v
	}
	var got []ai.Event
	var errs []string
	for i, s := range h.tgSourceList(ctx) {
		if i > 0 {
			tgSleep(ctx)
		}
		st := states[s.Key]
		if st == nil {
			st = &tgState{}
			states[s.Key] = st
		}
		got = append(got, h.readTG(ctx, s, st, now)...)
		if st.Err != "" {
			errs = append(errs, s.Name+": "+st.Err)
		}
	}
	removed := 0
	err := h.mutateFeed(ctx, func(f *eventsFeed) {
		removed = pruneFeed(f, today)
		f.Items = sortFeed(mergeEvents(f.Items, got))
		for k, v := range states {
			v.Total = 0
			f.TG[k] = v
		}
		for _, e := range f.Items {
			for _, v := range f.TG {
				if v.Username != "" && e.Source == "t.me/"+v.Username {
					v.Total++
				}
			}
		}
		f.Updated = now.UTC().Format(time.RFC3339)
	})
	log.Printf("events tg: %d events taken, %d passed removed, err=%v", len(got), removed, err)
	if err != nil {
		return len(got), err
	}
	if len(errs) > 0 && len(got) == 0 {
		return 0, errors.New(strings.Join(errs, "; "))
	}
	return len(got), nil
}

// TGEventsLoop reads the channels every tgEvery; after a restart it waits
// for the rest of the interval since the last run.
func (h *PlatformAI) TGEventsLoop(ctx context.Context) {
	wait := 3 * time.Minute
	if f, _ := h.loadFeed(ctx); f.TG != nil {
		var lastAt time.Time
		for _, s := range f.TG {
			if t, err := time.Parse(time.RFC3339, s.At); err == nil && t.After(lastAt) {
				lastAt = t
			}
		}
		if left := tgEvery - time.Since(lastAt); left > wait {
			wait = left
		}
	}
	t := time.NewTimer(wait)
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		rctx, cancel := context.WithTimeout(ctx, 10*time.Minute)
		_, _ = h.refreshTG(rctx)
		cancel()
		t.Reset(tgEvery)
	}
}
