package http

import (
	"github.com/bnursik/business_surgery_backend/internal/middleware"

	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/bnursik/business_surgery_backend/internal/bot"
	"github.com/bnursik/business_surgery_backend/internal/content"
)

// Контент-движок: посты для продаж выходят сами, собственник только
// подтверждает в Telegram.
//
// Club doc bs_content (команда правит на платформе, сервер планирует и публикует):
//
//	{settings: {channels: {threads: {on, time}, telegram: {on, chat, time}, instagram: {on, time}},
//	            days: [1..7], approval: "auto"|"manual", previewHour: 9},
//	 queue:   [item...], history: [последние 200 опубликованных], stats: {total, weeks}}
//
// Планировщик держит 14 дней вперёд по каждому включённому каналу: органы
// чередуются, гайд не повторяется в канале 60 дней, каждый 4-й пост из рубрик
// (кейсы, возражения, формат). Пункты, которые команда правила (edited), он
// не трогает. Утром в previewHour собственник получает список постов дня с
// кнопками «Всё ок», «✏️» и «Пропустить». Threads и Telegram-канал сервер
// публикует сам, Instagram (карусели, reels) остаётся задачей «вручную».
// Раз в час к каждому опубликованному посту считаются лиды из CRM по ссылке
// поста, записи на разбор и ставшие резидентами.

const (
	contentKey      = "bs_content"
	contentStateKey = "content_state" // scope server: the morning preview's day
	contentDays     = 14
	contentNoRepeat = 60 * 24 * time.Hour
	contentLate     = 6 * time.Hour // a post this late is not published any more
	contentHistory  = 600           // 16 Threads posts a day: about a month
	contentBotLink  = "t.me/bsurgery_bot?start="
)

var contentChannels = []string{"threads", "telegram", "instagram"}

var contentChannelName = map[string]string{"threads": "Threads", "telegram": "Telegram-канал", "instagram": "Instagram"}

var contentChannelPrefix = map[string]string{"threads": "th", "telegram": "tg", "instagram": "ig"}

// ── The doc ──

type contentChan struct {
	On   bool   `json:"on"`
	Time string `json:"time"`
	Chat string `json:"chat,omitempty"`
	Days []int  `json:"days,omitempty"` // optional: the channel's own days
	// Threads only (content_threads.go): posts a day (1-25, 0: 16), the
	// window they are spread over (08:00-22:00) and when the batch is made.
	PerDay  int    `json:"perDay,omitempty"`
	From    string `json:"from,omitempty"`
	To      string `json:"to,omitempty"`
	BuildAt string `json:"buildAt,omitempty"`
	// ManualPerDay: posts a day in the manual mode (no THREADS_TOKEN): the
	// bot sends each one to the owner (content_threads_manual.go), 0: 4.
	ManualPerDay int `json:"manualPerDay,omitempty"`
}

type contentSettings struct {
	Channels struct {
		Threads   contentChan `json:"threads"`
		Telegram  contentChan `json:"telegram"`
		Instagram contentChan `json:"instagram"`
	} `json:"channels"`
	Days        []int  `json:"days"`
	Approval    string `json:"approval"`
	PreviewHour int    `json:"previewHour"`
	Rev         int    `json:"rev,omitempty"` // settings layout; 2: Telegram channel off by default
	// manual: Threads has no token, the owner publishes by hand (set by the engine, not stored)
	manual bool
}

// contentTelegramDefault: the Telegram channel is off unless the team turns it on.
var contentTelegramDefault = false

func defaultContentSettings() contentSettings {
	var s contentSettings
	s.Channels.Threads = contentChan{On: true, Time: "10:00", PerDay: contentThreadsPerDay}
	s.Channels.Telegram = contentChan{On: contentTelegramDefault, Chat: "@bsurgery_kz", Time: "19:00"}
	s.Channels.Instagram = contentChan{On: false, Time: "12:00"}
	s.Days = []int{1, 2, 3, 4, 5, 6}
	s.Approval = "auto"
	s.PreviewHour = 9
	s.Rev = 2
	return s
}

func parseContentSettings(raw json.RawMessage) contentSettings {
	s := defaultContentSettings()
	if len(raw) > 0 {
		_ = json.Unmarshal(raw, &s)
	}
	if s.Approval != "manual" {
		s.Approval = "auto"
	}
	if s.PreviewHour < 0 || s.PreviewHour > 23 {
		s.PreviewHour = 9
	}
	if strings.TrimSpace(s.Channels.Telegram.Chat) == "" {
		s.Channels.Telegram.Chat = "@bsurgery_kz"
	}
	if s.Channels.Threads.PerDay < 0 {
		s.Channels.Threads.PerDay = 0
	}
	if s.Channels.Threads.PerDay > threadsPerDayMax {
		s.Channels.Threads.PerDay = threadsPerDayMax
	}
	return s
}

func (s contentSettings) channel(name string) contentChan {
	switch name {
	case "threads":
		return s.Channels.Threads
	case "telegram":
		return s.Channels.Telegram
	case "instagram":
		return s.Channels.Instagram
	}
	return contentChan{}
}

// clock: the channel's HH:MM (the default for a bad value).
func (s contentSettings) clock(name string) (int, int) {
	def := map[string][2]int{"threads": {10, 0}, "telegram": {19, 0}, "instagram": {12, 0}}[name]
	h, m, ok := strings.Cut(strings.TrimSpace(s.channel(name).Time), ":")
	hh, e1 := strconv.Atoi(h)
	mm, e2 := strconv.Atoi(m)
	if !ok || e1 != nil || e2 != nil || hh < 0 || hh > 23 || mm < 0 || mm > 59 {
		return def[0], def[1]
	}
	return hh, mm
}

// dayOn: the channel posts on that day (1 = Monday … 7 or 0 = Sunday).
func (s contentSettings) dayOn(name string, t time.Time) bool {
	days := s.Days
	if c := s.channel(name); len(c.Days) > 0 {
		days = c.Days
	}
	wd := int(t.In(almaty).Weekday())
	for _, d := range days {
		if d == wd || (wd == 0 && d == 7) {
			return true
		}
	}
	return false
}

type contentStats struct {
	Leads     int `json:"leads"`
	Razbor    int `json:"razbor"`
	Residents int `json:"residents"`
}

type contentItemData struct {
	ID          string          `json:"id"`
	Src         string          `json:"src,omitempty"`
	Kind        string          `json:"kind"`
	Channel     string          `json:"channel"`
	V           int             `json:"v,omitempty"` // which of the library's texts (Threads has two)
	Title       string          `json:"title,omitempty"`
	Organ       string          `json:"organ,omitempty"`
	Rubric      string          `json:"rubric,omitempty"`
	Text        string          `json:"text"`
	Slides      json.RawMessage `json:"slides,omitempty"`
	Caption     string          `json:"caption,omitempty"`
	Reels       json.RawMessage `json:"reels,omitempty"`
	At          string          `json:"at"`
	Status      string          `json:"status"`
	Error       string          `json:"error,omitempty"`
	PostID      string          `json:"postId,omitempty"`
	URL         string          `json:"url,omitempty"`
	Link        string          `json:"link,omitempty"`
	Edited      bool            `json:"edited,omitempty"`
	Auto        bool            `json:"auto,omitempty"`   // planned by the server (may be re-planned while not edited)
	Manual      bool            `json:"manual,omitempty"` // Instagram: published by hand
	PublishedAt string          `json:"publishedAt,omitempty"`
	ApprovedBy  string          `json:"approvedBy,omitempty"`
	Stats       *contentStats   `json:"stats,omitempty"`
	// The Threads batch (content_threads.go)
	Format  string   `json:"format,omitempty"`  // tip, checklist, numbers, myth, question, case, symptom, series, library
	Parts   []string `json:"parts,omitempty"`   // a series: the replies after the first post
	Gen     string   `json:"gen,omitempty"`     // ai or lib: made by the daily batch
	CTA     bool     `json:"cta,omitempty"`     // ends with the link to the 99 checklists
	Tries   int      `json:"tries,omitempty"`   // failed attempts to publish
	RetryAt string   `json:"retryAt,omitempty"` // not before
	// The manual Threads mode (content_threads_manual.go): the bot sent the
	// post to the owner (status sent), he published it himself (byHand).
	SentAt string `json:"sentAt,omitempty"`
	MsgID  int64  `json:"msgId,omitempty"`
	ByHand bool   `json:"byHand,omitempty"`
}

// contentItem keeps the fields the platform adds that the server does not know.
type contentItem struct {
	contentItemData
	extra map[string]json.RawMessage
}

func (c *contentItem) UnmarshalJSON(b []byte) error {
	if err := json.Unmarshal(b, &c.contentItemData); err != nil {
		return err
	}
	c.extra = extraKeys(b, contentItemData{})
	return nil
}

func (c contentItem) MarshalJSON() ([]byte, error) { return withExtra(c.contentItemData, c.extra) }

type contentDocData struct {
	Settings json.RawMessage `json:"settings"`
	Queue    []*contentItem  `json:"queue"`
	History  []*contentItem  `json:"history"`
	Stats    json.RawMessage `json:"stats,omitempty"`
	// ThreadsMode: the server tells the platform whether Threads is manual
	ThreadsMode *threadsMode `json:"threadsMode,omitempty"`
}

type contentDoc struct {
	contentDocData
	extra map[string]json.RawMessage
}

func (d *contentDoc) UnmarshalJSON(b []byte) error {
	if err := json.Unmarshal(b, &d.contentDocData); err != nil {
		return err
	}
	d.extra = extraKeys(b, contentDocData{})
	return nil
}

func (d contentDoc) MarshalJSON() ([]byte, error) { return withExtra(d.contentDocData, d.extra) }

var jsonKeysCache sync.Map

func jsonKeys(v any) map[string]bool {
	t := reflect.TypeOf(v)
	if k, ok := jsonKeysCache.Load(t); ok {
		return k.(map[string]bool)
	}
	out := map[string]bool{}
	for i := 0; i < t.NumField(); i++ {
		name, _, _ := strings.Cut(t.Field(i).Tag.Get("json"), ",")
		if name != "" && name != "-" {
			out[name] = true
		}
	}
	jsonKeysCache.Store(t, out)
	return out
}

func extraKeys(b []byte, known any) map[string]json.RawMessage {
	var all map[string]json.RawMessage
	if json.Unmarshal(b, &all) != nil {
		return nil
	}
	for k := range jsonKeys(known) {
		delete(all, k)
	}
	if len(all) == 0 {
		return nil
	}
	return all
}

func withExtra(v any, extra map[string]json.RawMessage) ([]byte, error) {
	b, err := json.Marshal(v)
	if err != nil || len(extra) == 0 {
		return b, err
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(b, &m); err != nil {
		return b, nil
	}
	for k, x := range extra {
		if _, ok := m[k]; !ok {
			m[k] = x
		}
	}
	return json.Marshal(m)
}

func parseContentAt(s string) (time.Time, bool) {
	s = strings.TrimSpace(s)
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return t, true
	}
	for _, l := range []string{"2006-01-02T15:04:05", "2006-01-02T15:04", "2006-01-02 15:04"} {
		if t, err := time.ParseInLocation(l, s, almaty); err == nil {
			return t, true
		}
	}
	return time.Time{}, false
}

func contentDay(t time.Time) string { return t.In(almaty).Format("20060102") }

func (it *contentItem) day() string {
	if t, ok := parseContentAt(it.At); ok {
		return contentDay(t)
	}
	return ""
}

func (it *contentItem) used() bool {
	return it.Status == "planned" || it.Status == "approved" || it.Status == "published" || it.Status == "sent" || it.Status == ""
}

// ── The engine ──

// ContentEngine plans, previews, publishes and counts the club's content.
type ContentEngine struct {
	docs funnelDocs
	// Threads publishes a text (PlatformAI.PublishThreadsText).
	Threads func(ctx context.Context, title, text string) (postID, url string, err error)
	// Channel posts to the Telegram channel (bot.Service.SendChannel).
	Channel func(ctx context.Context, chat, text string) (int64, error)
	// Send and Edit talk to the owner (bot.Service.SendMessageID / EditMessageKB).
	Send func(ctx context.Context, chatID int64, text string, kb map[string]any) (int64, error)
	Edit func(ctx context.Context, chatID, msgID int64, text string, kb map[string]any) error
	// Owner gets the morning preview and the failures.
	Owner int64
	// PlatformURL opens the platform (the ✏️ button adds ?section=content&id=…).
	PlatformURL string
	// Lib is the content library (content.Library; tests give their own).
	Lib func() []*content.LibItem
	// AI writes the Threads batch (ai.Client.Text); nil: library posts only.
	AI func(ctx context.Context, system, prompt string) (string, error)
	// ThreadsReply publishes a reply (a series' next part).
	ThreadsReply func(ctx context.Context, replyTo, text string) (string, error)
	// Go runs the batch build in the background (tests run it inline).
	Go func(func())
	// ThreadsManual: true when Threads has no token (the owner publishes by
	// hand from the bot's message); nil: never.
	ThreadsManual func(ctx context.Context) bool

	now      func() time.Time
	pubMu    sync.Mutex
	building atomic.Bool
	thMu     sync.Mutex
	thHold   time.Time // Threads waits until then (token, limits)
	thLast   time.Time // the last Threads post
	manMu    sync.Mutex
	manAt    time.Time // when manual was checked
	manVal   bool
}

func NewContentEngine(docs funnelDocs) *ContentEngine {
	return &ContentEngine{docs: docs, Lib: content.Library, now: time.Now, PlatformURL: ContentPlatformURL(), Owner: 453800951}
}

// ContentPlatformURL: PLATFORM_URL, else PUBLIC_URL/platform.
func ContentPlatformURL() string {
	if u := strings.TrimSpace(os.Getenv("PLATFORM_URL")); u != "" {
		return middleware.HTTPSURL(u) // R38a
	}
	if u := middleware.HTTPSURL(os.Getenv("PUBLIC_URL")); u != "" {
		return u + "/platform"
	}
	return "https://bs-platform-production.up.railway.app/platform"
}

// FirstTeamID: the first id of PLATFORM_TEAM ("id:Name,id:Name"), the owner.
func FirstTeamID(spec string) int64 {
	if strings.TrimSpace(spec) == "" {
		spec = DefaultPlatformTeam
	}
	for _, part := range strings.Split(spec, ",") {
		id, _, _ := strings.Cut(strings.TrimSpace(part), ":")
		if n, err := strconv.ParseInt(strings.TrimSpace(id), 10, 64); err == nil && n > 0 {
			return n
		}
	}
	return 453800951
}

func (e *ContentEngine) load(ctx context.Context) (*contentDoc, int, error) {
	d := &contentDoc{}
	base := 0
	pd, err := e.docs.GetDoc(ctx, "club", contentKey)
	if err != nil {
		return nil, 0, err
	}
	if pd != nil {
		base = pd.Version
		if !pd.Deleted && strings.TrimSpace(pd.Value) != "" {
			if err := json.Unmarshal([]byte(pd.Value), d); err != nil {
				return nil, 0, fmt.Errorf("bs_content unreadable: %w", err)
			}
		}
	}
	if len(d.Settings) == 0 || string(d.Settings) == "null" {
		d.Settings, _ = json.Marshal(defaultContentSettings())
	}
	if d.Queue == nil {
		d.Queue = []*contentItem{}
	}
	// Rev 2: the owner publishes to Threads, not to the Telegram channel.
	// Earlier docs had Telegram on: switch it off and drop its untouched plan.
	var rv struct {
		Rev int `json:"rev"`
	}
	_ = json.Unmarshal(d.Settings, &rv)
	if st := parseContentSettings(d.Settings); rv.Rev < 2 {
		st.Channels.Telegram.On, st.Rev = false, 2
		d.Settings, _ = json.Marshal(st)
		q := d.Queue[:0]
		for _, it := range d.Queue {
			if it.Channel == "telegram" && it.Status != "published" && !it.Edited {
				continue
			}
			q = append(q, it)
		}
		d.Queue = q
	}
	if d.History == nil {
		d.History = []*contentItem{}
	}
	return d, base, nil
}

// update reads bs_content, lets fn change it and writes it back when it changed.
func (e *ContentEngine) update(ctx context.Context, fn func(d *contentDoc) bool) (*contentDoc, error) {
	var last error
	for try := 0; try < 6; try++ {
		d, base, err := e.load(ctx)
		if err != nil {
			return nil, err
		}
		before, _ := json.Marshal(d)
		if !fn(d) {
			return d, nil
		}
		after, err := json.Marshal(d)
		if err != nil {
			return nil, err
		}
		if base > 0 && bytes.Equal(before, after) {
			return d, nil
		}
		if _, last = e.docs.PutDoc(ctx, "club", contentKey, base, string(after), false, "server:content"); last == nil {
			return d, nil
		}
	}
	return nil, last
}

func (e *ContentEngine) lib() []*content.LibItem {
	if e.Lib == nil {
		return content.Library()
	}
	return e.Lib()
}

// ── Library texts ──

type contentText struct {
	text, caption string
	slides, reels json.RawMessage
}

func libContent(li *content.LibItem, kind string, v int) (contentText, bool) {
	switch kind {
	case "threads":
		if len(li.Threads) == 0 {
			return contentText{}, false
		}
		return contentText{text: li.Threads[v%len(li.Threads)]}, true
	case "telegram":
		return contentText{text: li.Telegram}, li.Telegram != ""
	case "carousel":
		var c struct {
			Slides  json.RawMessage `json:"slides"`
			Caption string          `json:"caption"`
		}
		if len(li.Carousel) == 0 || json.Unmarshal(li.Carousel, &c) != nil {
			return contentText{}, false
		}
		return contentText{text: strings.TrimSpace(c.Caption), caption: strings.TrimSpace(c.Caption), slides: c.Slides}, true
	case "reels":
		var r struct {
			Hook     string   `json:"hook"`
			Beats    []string `json:"beats"`
			CTA      string   `json:"cta"`
			Duration any      `json:"duration"`
		}
		if len(li.Reels) == 0 || json.Unmarshal(li.Reels, &r) != nil {
			return contentText{}, false
		}
		var b strings.Builder
		b.WriteString("Хук: " + strings.TrimSpace(r.Hook) + "\n")
		for i, x := range r.Beats {
			fmt.Fprintf(&b, "\n%d. %s", i+1, strings.TrimSpace(x))
		}
		if r.CTA != "" {
			b.WriteString("\n\nCTA: " + strings.TrimSpace(r.CTA))
		}
		if r.Duration != nil {
			fmt.Fprintf(&b, "\n\nДлительность: %v сек", r.Duration)
		}
		return contentText{text: b.String(), reels: li.Reels}, true
	}
	return contentText{}, false
}

// contentLink: the bot link in the text, else <channel>_<src>.
func contentLink(channel, src, text string) string {
	if p := content.StartParam(text); p != "" {
		return contentBotLink + p
	}
	if src == "" {
		return ""
	}
	return contentBotLink + contentChannelPrefix[channel] + "_" + src
}

func linkParam(link string) string {
	if i := strings.Index(link, "?start="); i >= 0 {
		return link[i+len("?start="):]
	}
	return ""
}

// ── Planning ──

type contentUse struct {
	at                             time.Time
	channel, src, organ, rub, kind string
}

func contentUses(d *contentDoc) []contentUse {
	var out []contentUse
	for _, list := range [][]*contentItem{d.History, d.Queue} {
		for _, it := range list {
			if !it.used() || it.Src == "" {
				continue
			}
			if t, ok := parseContentAt(it.At); ok {
				out = append(out, contentUse{at: t, channel: it.Channel, src: it.Src, organ: it.Organ, rub: it.Rubric, kind: it.Kind})
			}
		}
	}
	return out
}

func contentKinds(channel string, prior []contentUse) []string {
	switch channel {
	case "threads":
		return []string{"threads"}
	case "telegram":
		return []string{"telegram"}
	}
	// Instagram: carousel and reels take turns.
	if len(prior) > 0 && prior[0].kind == "carousel" {
		return []string{"reels", "carousel"}
	}
	return []string{"carousel", "reels"}
}

// pick chooses the source for a slot.
func (e *ContentEngine) pick(lib []*content.LibItem, uses []contentUse, channel string, slot time.Time) (*content.LibItem, string, int) {
	var prior []contentUse // the channel's posts before the slot, newest first
	srcLast := map[string]time.Time{}
	srcCount := map[string]int{}
	organLast := map[string]time.Time{}
	blocked := map[string]bool{}
	near := map[string]bool{}      // sources on the other channels these days
	nearOrgan := map[string]bool{} // organs on the other channels that day
	for _, u := range uses {
		if u.channel != channel {
			if d := u.at.Sub(slot); d < 72*time.Hour && d > -72*time.Hour {
				near[u.src] = true
			}
			if d := u.at.Sub(slot); d < 20*time.Hour && d > -20*time.Hour && u.organ != "" {
				nearOrgan[u.organ] = true
			}
			continue
		}
		if d := u.at.Sub(slot); d < contentNoRepeat && d > -contentNoRepeat {
			blocked[u.src] = true
		}
		srcCount[u.src]++
		if u.at.Before(slot) {
			prior = append(prior, u)
			if u.at.After(srcLast[u.src]) {
				srcLast[u.src] = u.at
			}
			if u.organ != "" && u.at.After(organLast[u.organ]) {
				organLast[u.organ] = u.at
			}
		}
	}
	sort.SliceStable(prior, func(i, j int) bool { return prior[i].at.After(prior[j].at) })
	kinds := contentKinds(channel, prior)

	// every 4th post: the three before were guides
	needRubric := len(prior) >= 3
	for i := 0; i < 3 && i < len(prior); i++ {
		if prior[i].rub != "" {
			needRubric = false
		}
	}
	lastOrgan, lastRubric := "", ""
	for _, u := range prior {
		if u.rub == "" && lastOrgan == "" {
			lastOrgan = u.organ
		}
		if u.rub != "" && lastRubric == "" {
			lastRubric = u.rub
		}
	}

	guide := func(kind string) *content.LibItem {
		var cands []*content.LibItem
		for _, li := range lib {
			if li.Rubric == "" && li.Has(kind) && !blocked[li.Src] {
				cands = append(cands, li)
			}
		}
		sort.SliceStable(cands, func(i, j int) bool {
			a, b := cands[i], cands[j]
			if (a.Organ == lastOrgan) != (b.Organ == lastOrgan) {
				return a.Organ != lastOrgan
			}
			if near[a.Src] != near[b.Src] {
				return !near[a.Src]
			}
			if nearOrgan[a.Organ] != nearOrgan[b.Organ] {
				return !nearOrgan[a.Organ]
			}
			if oa, ob := organLast[a.Organ], organLast[b.Organ]; !oa.Equal(ob) {
				return oa.Before(ob)
			}
			if sa, sb := srcLast[a.Src], srcLast[b.Src]; !sa.Equal(sb) {
				return sa.Before(sb)
			}
			return a.Src < b.Src
		})
		if len(cands) == 0 {
			return nil
		}
		return cands[0]
	}
	rubric := func(kind string) *content.LibItem {
		order := []string{"case", "objection", "format"}
		seen := map[string]bool{"case": true, "objection": true, "format": true}
		var extra []string
		for _, li := range lib {
			if li.Rubric != "" && !seen[li.Rubric] {
				seen[li.Rubric] = true
				extra = append(extra, li.Rubric)
			}
		}
		sort.Strings(extra)
		order = append(order, extra...)
		start := 0
		for i, r := range order {
			if r == lastRubric {
				start = i + 1
			}
		}
		for k := 0; k < len(order); k++ {
			r := order[(start+k)%len(order)]
			var best *content.LibItem
			for _, li := range lib {
				if li.Rubric != r || !li.Has(kind) || blocked[li.Src] {
					continue
				}
				better := best == nil
				if !better && near[li.Src] != near[best.Src] {
					better = !near[li.Src]
				} else if !better {
					better = srcLast[li.Src].Before(srcLast[best.Src]) || (srcLast[li.Src].Equal(srcLast[best.Src]) && li.Src < best.Src)
				}
				if better {
					best = li
				}
			}
			if best != nil {
				return best
			}
		}
		return nil
	}
	for _, kind := range kinds {
		var li *content.LibItem
		if needRubric {
			li = rubric(kind)
		}
		if li == nil {
			li = guide(kind)
		}
		if li == nil && !needRubric {
			li = rubric(kind)
		}
		if li != nil {
			v := 0
			if n := li.Variants(kind); n > 0 {
				v = srcCount[li.Src] % n
			}
			return li, kind, v
		}
	}
	return nil, "", 0
}

func newContentItem(li *content.LibItem, channel, kind string, v int, slot time.Time) *contentItem {
	tx, _ := libContent(li, kind, v)
	src := li.Src
	if len(src) > 32 {
		src = src[:32]
	}
	id := contentChannelPrefix[channel] + "-" + contentDay(slot) + "-" + src
	if v > 0 {
		id += fmt.Sprintf("-%d", v)
	}
	it := &contentItem{contentItemData: contentItemData{
		ID: id, Src: li.Src, Kind: kind, Channel: channel, V: v, Title: li.Title, Organ: li.Organ, Rubric: li.Rubric,
		Text: tx.text, Caption: tx.caption, Slides: tx.slides, Reels: tx.reels,
		At: slot.In(almaty).Format(time.RFC3339), Status: "planned", Auto: true, Manual: channel == "instagram",
	}}
	it.Link = contentLink(channel, li.Src, tx.text)
	return it
}

// fits: a server-planned item still matches the settings.
func (s contentSettings) fits(it *contentItem, at time.Time) bool {
	c := s.channel(it.Channel)
	if !c.On || !s.dayOn(it.Channel, at) {
		return false
	}
	if it.Channel == "threads" && (it.Gen != "" || s.threadsBatch()) {
		// a batch post fits while the batch is on; a classic one gives way to the batch
		if it.Gen == "" {
			return false
		}
		from, to := s.threadsWindow()
		a := at.In(almaty)
		m := a.Hour()*60 + a.Minute()
		return s.threadsBatch() && m >= from && m < to
	}
	hh, mm := s.clock(it.Channel)
	a := at.In(almaty)
	return a.Hour() == hh && a.Minute() == mm
}

// plan fills the next 14 days; reset re-plans the server's untouched items too.
// Returns how many items were added.
func (e *ContentEngine) plan(d *contentDoc, now time.Time, reset bool) int {
	st := e.stCached(d)
	lib := e.lib()
	bySrc := map[string]*content.LibItem{}
	for _, li := range lib {
		bySrc[li.Src] = li
	}
	// The team's edits: an item whose text differs from the library is theirs.
	for _, it := range d.Queue {
		if it.Status == "" {
			it.Status = "planned"
		}
		if it.ID == "" {
			it.ID = "x-" + newID()[:10]
		}
		if !it.Edited && it.Auto && it.Src != "" && it.Gen == "" {
			if li := bySrc[it.Src]; li != nil {
				if tx, ok := libContent(li, it.Kind, it.V); ok && strings.TrimSpace(tx.text) != strings.TrimSpace(it.Text) {
					it.Edited = true
				}
			}
		}
		if it.Link == "" {
			it.Link = contentLink(it.Channel, it.Src, it.Text)
		}
	}
	today := now.In(almaty)
	today = time.Date(today.Year(), today.Month(), today.Day(), 0, 0, 0, 0, almaty)
	// Old items: published ones to history, the rest away after two weeks.
	var keep []*contentItem
	for _, it := range d.Queue {
		at, ok := parseContentAt(it.At)
		switch {
		case ok && it.Status == "published" && at.Before(today.AddDate(0, 0, -2)):
			d.History = append(d.History, it)
			continue
		case ok && (it.Status == "skipped" || it.Status == "failed" || it.Status == "missed") && at.Before(today.AddDate(0, 0, -14)):
			continue
		case ok && it.Auto && !it.Edited && it.Status == "planned" && at.After(now) && ((reset && it.Gen == "") || !st.fits(it, at)):
			continue // re-planned below (the Threads batch stays: it is made once a day)
		case ok && it.Auto && !it.Edited && it.Status == "planned" && it.Src != "" && it.Gen == "" && bySrc[it.Src] == nil && at.After(now):
			continue // gone from the library
		}
		keep = append(keep, it)
	}
	d.Queue = threadsTrim(keep, st, now)
	sort.SliceStable(d.History, func(i, j int) bool { return d.History[i].At < d.History[j].At })
	var hist []*contentItem
	for _, it := range d.History {
		if it.Status == "published" {
			hist = append(hist, it)
		}
	}
	if len(hist) > contentHistory {
		hist = hist[len(hist)-contentHistory:]
	}
	d.History = hist

	taken := map[string]bool{}
	for _, it := range d.Queue {
		taken[it.Channel+"/"+it.day()] = true
	}
	uses := contentUses(d)
	added := 0
	// day by day, so the channels of one day see each other
	for i := 0; i < contentDays; i++ {
		day := today.AddDate(0, 0, i)
		for _, ch := range contentChannels {
			if !st.channel(ch).On || !st.dayOn(ch, day) || (ch == "threads" && st.threadsBatch()) {
				continue // several Threads posts a day come from the daily batch
			}
			hh, mm := st.clock(ch)
			slot := time.Date(day.Year(), day.Month(), day.Day(), hh, mm, 0, 0, almaty)
			if !slot.After(now) || taken[ch+"/"+contentDay(slot)] {
				continue
			}
			li, kind, v := e.pick(lib, uses, ch, slot)
			if li == nil {
				continue
			}
			it := newContentItem(li, ch, kind, v, slot)
			d.Queue = append(d.Queue, it)
			taken[ch+"/"+contentDay(slot)] = true
			uses = append(uses, contentUse{at: slot, channel: ch, src: li.Src, organ: li.Organ, rub: li.Rubric, kind: kind})
			added++
		}
	}
	sort.SliceStable(d.Queue, func(i, j int) bool {
		a, _ := parseContentAt(d.Queue[i].At)
		b, _ := parseContentAt(d.Queue[j].At)
		return a.Before(b)
	})
	return added
}

// Plan fills the queue (reset: re-plans every untouched future item).
func (e *ContentEngine) Plan(ctx context.Context, reset bool) (int, *contentDoc, error) {
	e.manual(ctx)
	added := 0
	d, err := e.update(ctx, func(d *contentDoc) bool {
		added = e.plan(d, e.now(), reset)
		return true
	})
	return added, d, err
}

// OwnsThreadsDay: the queue has a Threads post that day (the daily
// «Полезное» post then waits).
func (e *ContentEngine) OwnsThreadsDay(ctx context.Context, day time.Time) bool {
	d, _, err := e.load(ctx)
	if err != nil {
		return false
	}
	key := contentDay(day)
	for _, it := range d.Queue {
		if it.Channel == "threads" && it.Status != "skipped" && it.day() == key {
			return true
		}
	}
	return false
}

// ── Publishing ──

var errContentNotFound = errors.New("Пост не найден")

func findContent(d *contentDoc, id string) *contentItem {
	for _, it := range d.Queue {
		if it.ID == id {
			return it
		}
	}
	return nil
}

// tgChannelError: Telegram's refusal in words the team can act on.
func tgChannelError(err error, chat string) error {
	if err == nil {
		return nil
	}
	low := strings.ToLower(err.Error())
	for _, s := range []string{"chat not found", "not enough rights", "need administrator rights", "bot is not a member", "chat_write_forbidden", "have no rights", "bot was kicked"} {
		if strings.Contains(low, s) {
			return fmt.Errorf("Добавьте @bsurgery_bot администратором канала %s (право «Публикация сообщений»)", chat)
		}
	}
	msg := err.Error()
	if i := strings.Index(msg, ": "); strings.HasPrefix(msg, "telegram ") && i > 0 {
		msg = msg[i+2:]
	}
	return errors.New("Telegram: " + msg)
}

func channelPostURL(chat string, id int64) string {
	if id == 0 {
		return ""
	}
	if strings.HasPrefix(chat, "@") {
		return fmt.Sprintf("https://t.me/%s/%d", chat[1:], id)
	}
	if strings.HasPrefix(chat, "-100") {
		return fmt.Sprintf("https://t.me/c/%s/%d", chat[4:], id)
	}
	return ""
}

// Publish posts one item now. force: whatever its status and time (the
// team's «Опубликовать сейчас»); otherwise only when it is still due.
func (e *ContentEngine) Publish(ctx context.Context, id string, force bool) (*contentItem, error) {
	e.pubMu.Lock()
	defer e.pubMu.Unlock()
	d, _, err := e.load(ctx)
	if err != nil {
		return nil, err
	}
	it := findContent(d, id)
	if it == nil {
		return nil, errContentNotFound
	}
	st := e.settings(ctx, d)
	if it.Status == "published" {
		return it, errors.New("Уже опубликовано")
	}
	if it.Channel == "threads" && st.manual {
		// no token: the post goes to the owner's bot to publish by hand
		if !force {
			return it, nil
		}
		return e.sendManual(ctx, id, true)
	}
	if it.Manual || it.Channel == "instagram" {
		return it, errors.New("Instagram публикуется вручную: отметьте пост опубликованным на платформе")
	}
	if !force && !(it.Status == "approved" || (it.Status == "planned" && st.Approval != "manual")) {
		return it, nil
	}
	var postID, url, partErr string
	var perr error
	switch it.Channel {
	case "threads":
		if e.Threads == nil {
			perr = errors.New("Threads не подключён")
		} else {
			postID, url, perr = e.Threads(ctx, it.Title, it.Text)
			e.thSet(time.Time{}, e.now())
		}
		// a series: the next parts as replies, each to the one before
		prev := postID
		for i, part := range it.Parts {
			if perr != nil || strings.TrimSpace(part) == "" {
				break
			}
			if e.ThreadsReply == nil {
				partErr = "Продолжение серии не вышло: ответы Threads не подключены"
				break
			}
			id, err := e.ThreadsReply(ctx, prev, part)
			if err != nil {
				partErr = fmt.Sprintf("Часть %d серии не вышла: %v", i+2, err)
				break
			}
			prev = id
		}
	case "telegram":
		chat := st.Channels.Telegram.Chat
		if e.Channel == nil {
			perr = errors.New("Бот не настроен: нет TELEGRAM_BOT_TOKEN")
		} else if strings.TrimSpace(it.Text) == "" {
			perr = errors.New("Пустой текст")
		} else {
			mid, err := e.Channel(ctx, chat, it.Text)
			perr = tgChannelError(err, chat)
			if perr == nil {
				postID, url = strconv.FormatInt(mid, 10), channelPostURL(chat, mid)
			}
		}
	default:
		perr = fmt.Errorf("Неизвестный канал %q", it.Channel)
	}
	now := e.now().UTC().Format(time.RFC3339)
	// Threads, by itself: a token, limit or network error waits and tries again
	kind, retryAt := "", ""
	if perr != nil && it.Channel == "threads" {
		kind = threadsErrKind(perr)
		var wait time.Duration
		switch kind {
		case "auth":
			wait = 30 * time.Minute
			e.thSet(e.now().Add(wait), time.Time{})
		case "rate":
			wait = time.Hour
			e.thSet(e.now().Add(wait), time.Time{})
		case "temp":
			if it.Tries < 2 {
				wait = []time.Duration{3 * time.Minute, 10 * time.Minute}[it.Tries]
			}
		}
		if wait > 0 && !force {
			retryAt = e.now().Add(wait).In(almaty).Format(time.RFC3339)
		}
	}
	var out *contentItem
	_, uerr := e.update(ctx, func(d *contentDoc) bool {
		x := findContent(d, id)
		if x == nil {
			if perr != nil {
				return false
			}
			// deleted on the platform meanwhile: keep the published post in history
			cp := *it
			x = &cp
			d.History = append(d.History, x)
		}
		switch {
		case perr != nil && retryAt != "":
			x.Tries++
			x.RetryAt = retryAt
			x.Error = "Повтор в " + atClock(retryAt) + ": " + perr.Error()
		case perr != nil:
			x.Status, x.Error, x.RetryAt = "failed", perr.Error(), ""
		default:
			x.Status, x.Error, x.PostID, x.URL, x.PublishedAt, x.RetryAt = "published", partErr, postID, url, now, ""
		}
		out = x
		return true
	})
	if uerr != nil {
		log.Printf("content: save %s: %v", id, uerr)
	}
	if out == nil {
		out = it
	}
	if perr != nil {
		log.Printf("content: %s %s: %v", it.Channel, id, perr)
		if it.Channel != "threads" {
			e.tellOwner(ctx, fmt.Sprintf("⚠️ Пост не вышел\n%s · %s\n«%s»\n\n%s", contentChannelName[it.Channel], atClock(it.At), content.FirstLine(it.Text, 80), perr.Error()))
		} else if retryAt == "" || kind != "temp" {
			// once a day per kind: 16 posts a day must not bring 16 messages
			key := "th_" + kind
			if kind == "other" {
				key += ":" + content.FirstLine(perr.Error(), 40)
			}
			e.alertOnce(ctx, key, threadsAlertText(kind, it, perr))
		}
		return out, perr
	}
	if it.Channel == "threads" {
		e.threadsRecovered(ctx)
	}
	log.Printf("content: %s %s published %s", it.Channel, id, url)
	return out, nil
}

func (e *ContentEngine) tellOwner(ctx context.Context, text string) {
	if e.Send == nil || e.Owner == 0 {
		return
	}
	if _, err := e.Send(ctx, e.Owner, text, nil); err != nil {
		log.Printf("content: tell owner: %v", err)
	}
}

func atClock(at string) string {
	if t, ok := parseContentAt(at); ok {
		return t.In(almaty).Format("15:04")
	}
	return ""
}

// due: the items to publish now; too late ones are closed. Threads gets one
// post per call (the earliest), not while it waits after an error, not
// within 2 minutes of the last one and not over the 24-hour limit.
func (e *ContentEngine) due(ctx context.Context) []string {
	now := e.now()
	var ids []string
	quotaFull := false
	_, err := e.update(ctx, func(d *contentDoc) bool {
		ids = ids[:0]
		st := e.stCached(d)
		changed := false
		thOK := !e.thHeld(now)
		if n, _ := threads24h(d, now); n >= threadsQuotaSafe {
			thOK, quotaFull = false, true
		}
		var th *contentItem
		var thAt time.Time
		for _, it := range d.Queue {
			if it.Manual || (it.Channel != "threads" && it.Channel != "telegram") || !st.channel(it.Channel).On {
				continue
			}
			if it.Channel == "threads" && st.manual {
				continue // the manual mode sends them to the owner (manualDue)
			}
			at, ok := parseContentAt(it.At)
			if !ok || at.After(now) {
				continue
			}
			publish := it.Status == "approved" || (it.Status == "planned" && st.Approval != "manual")
			batch := it.Channel == "threads" && (it.Gen != "" || st.threadsBatch())
			lim := contentLate
			if batch {
				lim = threadsLateBatch
			}
			late := now.Sub(at) > lim
			waiting := false
			if r, ok := parseContentAt(it.RetryAt); ok && r.After(now) {
				waiting = true
			}
			switch {
			case publish && !late && it.Channel == "threads":
				if thOK && !waiting && (th == nil || at.Before(thAt)) {
					th, thAt = it, at
				}
			case publish && !late:
				ids = append(ids, it.ID)
			case publish && late && batch:
				// a batch post that missed its time is not published in a heap with the next ones
				it.Status, it.RetryAt = "skipped", ""
				if it.Error == "" {
					it.Error = "Не вышел вовремя: пропущен, чтобы посты не шли пачкой"
				} else {
					it.Error = "Не вышел вовремя. " + it.Error
				}
				changed = true
			case publish && late:
				it.Status, it.Error = "failed", "Не опубликовано вовремя: сервер был недоступен"
				changed = true
			case it.Status == "planned" && late:
				it.Status, it.Error = "skipped", "Не подтверждено в Telegram"
				changed = true
			}
		}
		if th != nil {
			ids = append([]string{th.ID}, ids...)
		}
		return changed
	})
	if err != nil {
		log.Printf("content: due: %v", err)
		return nil
	}
	if quotaFull {
		e.alertOnce(ctx, "th_quota", threadsAlertText("quota", nil, nil))
	}
	return ids
}

// Tick: publish what is due, make the day's Threads batch after 06:30 and
// send the morning preview (every minute).
func (e *ContentEngine) Tick(ctx context.Context) {
	if e.manual(ctx) {
		e.manualDue(ctx)
	}
	e.noteMode(ctx)
	for _, id := range e.due(ctx) {
		c, cancel := context.WithTimeout(ctx, 3*time.Minute)
		_, _ = e.Publish(c, id, false)
		cancel()
	}
	if d, _, err := e.load(ctx); err == nil {
		e.buildDue(ctx, d)
	}
	e.Preview(ctx, false)
}

// ── Morning preview ──

type contentState struct {
	Preview string `json:"preview,omitempty"` // the day (20261002) the preview went out
	Msg     int64  `json:"msg,omitempty"`
	// Threads (content_threads.go): the owner's alerts (kind → day told),
	// the daily batches and the posts of the last 45 days for dedupe.
	Alerts  map[string]string           `json:"alerts,omitempty"`
	Threads map[string]*threadsDayState `json:"threads,omitempty"`
	Recent  []threadsSig                `json:"recent,omitempty"`
}

func (e *ContentEngine) state(ctx context.Context) (contentState, int) {
	var s contentState
	d, err := e.docs.GetDoc(ctx, "server", contentStateKey)
	if err != nil || d == nil {
		return s, 0
	}
	_ = json.Unmarshal([]byte(d.Value), &s)
	return s, d.Version
}

func dayItems(d *contentDoc, day string) []*contentItem {
	var out []*contentItem
	for _, it := range d.Queue {
		if it.day() == day {
			out = append(out, it)
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		a, _ := parseContentAt(out[i].At)
		b, _ := parseContentAt(out[j].At)
		return a.Before(b)
	})
	return out
}

var ruMonthsGen = []string{"января", "февраля", "марта", "апреля", "мая", "июня", "июля", "августа", "сентября", "октября", "ноября", "декабря"}

var contentKindName = map[string]string{"carousel": "карусель", "reels": "reels"}

func previewText(items []*contentItem, approval, day string) string {
	var b strings.Builder
	title := day
	if t, err := time.ParseInLocation("20060102", day, almaty); err == nil {
		title = fmt.Sprintf("%d %s", t.Day(), ruMonthsGen[t.Month()-1])
	}
	b.WriteString("📅 Контент на сегодня, " + title + "\n")
	for i, it := range items {
		head := fmt.Sprintf("%d. %s", i+1, contentChannelName[it.Channel])
		if k := contentKindName[it.Kind]; k != "" {
			head += " · " + k
		}
		if c := atClock(it.At); c != "" {
			head += " · " + c
		}
		switch {
		case it.Status == "published":
			head += " · 📤 опубликован"
		case it.Status == "skipped":
			head += " · ⏭ пропущен"
		case it.Status == "failed":
			head += " · ⚠️ " + it.Error
		case it.Manual:
			head += " · вручную"
		case it.Status == "approved":
			head += " · ✅ подтверждён"
		case approval == "manual":
			head += " · ждёт подтверждения"
		}
		text := it.Text
		if it.Kind == "carousel" && it.Caption != "" {
			text = it.Caption
		}
		short := 120
		if len(items) > 8 { // 16 Threads posts a day still fit one message
			short = 70
		}
		b.WriteString("\n" + head + "\n" + content.FirstLine(strings.ReplaceAll(text, "\n", " "), short) + "\n")
	}
	if approval == "manual" {
		b.WriteString("\nРучной режим: выйдут только подтверждённые посты. Нажмите «Всё ок», если тексты в порядке.")
	} else {
		b.WriteString("\nПосты выйдут сами в своё время. Не подходит: «Пропустить» или «✏️» поправить на платформе.")
	}
	return b.String()
}

func (e *ContentEngine) previewKB(items []*contentItem, day string) map[string]any {
	var rows [][]map[string]any
	open := false
	for _, it := range items {
		if !it.Manual && it.Status == "planned" {
			open = true
		}
	}
	if open {
		rows = append(rows, row(map[string]any{"text": "✅ Всё ок", "callback_data": "cnt_ok_" + day}))
	}
	for i, it := range items {
		var r []map[string]any
		if e.PlatformURL != "" {
			sep := "?"
			if strings.Contains(e.PlatformURL, "?") {
				sep = "&"
			}
			r = append(r, map[string]any{"text": fmt.Sprintf("✏️ %d", i+1), "url": e.PlatformURL + sep + "section=content&id=" + it.ID})
		}
		if it.Status == "planned" || it.Status == "approved" {
			if data := "cnt_skip_" + it.ID; len(data) <= 64 {
				r = append(r, map[string]any{"text": fmt.Sprintf("⏭ Пропустить %d", i+1), "callback_data": data})
			}
		}
		if len(r) > 0 {
			rows = append(rows, r)
		}
	}
	if len(rows) == 0 {
		return nil
	}
	return map[string]any{"inline_keyboard": rows}
}

// Preview sends the owner today's posts once a day after previewHour.
// force sends it again (tests, the team).
func (e *ContentEngine) Preview(ctx context.Context, force bool) bool {
	now := e.now().In(almaty)
	d, _, err := e.load(ctx)
	if err != nil {
		return false
	}
	st := e.settings(ctx, d)
	if !force && now.Hour() < st.PreviewHour {
		return false
	}
	day := contentDay(now)
	s, base := e.state(ctx)
	if !force && s.Preview == day {
		return false
	}
	var items []*contentItem
	for _, it := range dayItems(d, day) {
		if st.manual && it.Channel == "threads" {
			continue // each comes by itself at its time
		}
		if it.Status == "planned" || it.Status == "approved" {
			items = append(items, it)
		}
	}
	var mid int64
	if len(items) > 0 && e.Send != nil && e.Owner != 0 {
		if mid, err = e.Send(ctx, e.Owner, previewText(items, st.Approval, day), e.previewKB(items, day)); err != nil {
			log.Printf("content: preview: %v", err)
		}
	}
	_ = base
	e.saveState(ctx, func(s *contentState) { s.Preview, s.Msg = day, mid })
	return len(items) > 0
}

// HandleCallback answers the preview's buttons (cnt_ok_<day>, cnt_skip_<id>);
// the bot service calls it for the team only.
func (e *ContentEngine) HandleCallback(ctx context.Context, cb bot.CallbackUpdate) (string, bool) {
	data := strings.TrimPrefix(cb.Data, "cnt_")
	if strings.HasPrefix(data, "th_") {
		return e.manualCallback(ctx, cb, strings.TrimPrefix(data, "th_")), true
	}
	by := fmt.Sprintf("tg:%d", cb.FromID)
	day, toast := "", ""
	var err error
	switch {
	case strings.HasPrefix(data, "ok_"):
		day = strings.TrimPrefix(data, "ok_")
		n := 0
		_, err = e.update(ctx, func(d *contentDoc) bool {
			n = 0
			for _, it := range d.Queue {
				if it.day() == day && !it.Manual && it.Status == "planned" {
					it.Status, it.ApprovedBy = "approved", by
					n++
				}
			}
			return n > 0
		})
		toast = fmt.Sprintf("Подтверждено постов: %d", n)
		if n == 0 {
			toast = "Подтверждать нечего"
		}
	case strings.HasPrefix(data, "skip_"):
		id := strings.TrimPrefix(data, "skip_")
		toast = "Пост уже не в очереди"
		_, err = e.update(ctx, func(d *contentDoc) bool {
			it := findContent(d, id)
			if it == nil {
				return false
			}
			day = it.day()
			if it.Status != "planned" && it.Status != "approved" {
				toast = "Пост уже " + map[string]string{"published": "опубликован", "skipped": "пропущен", "failed": "не вышел"}[it.Status]
				return false
			}
			it.Status, it.ApprovedBy = "skipped", by
			toast = "Пропущено"
			return true
		})
	default:
		return "", true
	}
	if err != nil {
		log.Printf("content: callback %s: %v", cb.Data, err)
		return "Не получилось, попробуйте ещё раз", true
	}
	if day != "" && e.Edit != nil && cb.MessageID != 0 {
		if d, _, err := e.load(ctx); err == nil {
			items := dayItems(d, day)
			st := parseContentSettings(d.Settings)
			if err := e.Edit(ctx, cb.ChatID, cb.MessageID, previewText(items, st.Approval, day), e.previewKB(items, day)); err != nil {
				log.Printf("content: preview edit: %v", err)
			}
		}
	}
	return toast, true
}

// ── Stats ──

type contentAgg struct {
	Posts     int `json:"posts"`
	Leads     int `json:"leads"`
	Razbor    int `json:"razbor"`
	Residents int `json:"residents"`
}

type contentWeek struct {
	Week      string                 `json:"week"` // Monday, 2026-09-28
	Channels  map[string]*contentAgg `json:"-"`
	Threads   *contentAgg            `json:"threads"`
	Telegram  *contentAgg            `json:"telegram"`
	Instagram *contentAgg            `json:"instagram"`
}

func leadTime(l map[string]any) (time.Time, bool) {
	if t, err := time.Parse(time.RFC3339, fmt.Sprint(l["startAt"])); err == nil {
		return t, true
	}
	if t, err := time.ParseInLocation("02.01.2006", fmt.Sprint(l["date"]), almaty); err == nil {
		return t.Add(23*time.Hour + 59*time.Minute), true
	}
	return time.Time{}, false
}

func weekOf(t time.Time) string {
	a := t.In(almaty)
	wd := int(a.Weekday())
	if wd == 0 {
		wd = 7
	}
	m := time.Date(a.Year(), a.Month(), a.Day(), 0, 0, 0, 0, almaty).AddDate(0, 0, 1-wd)
	return m.Format("2006-01-02")
}

// countContent writes each published item's leads, разборы and residents
// and the weekly totals per channel.
func countContent(d *contentDoc, leads []any) {
	type pub struct {
		it *contentItem
		at time.Time
	}
	byLabel := map[string][]pub{}
	var all []pub
	for _, list := range [][]*contentItem{d.History, d.Queue} {
		for _, it := range list {
			if it.Status != "published" {
				continue
			}
			at, ok := parseContentAt(it.PublishedAt)
			if !ok {
				if at, ok = parseContentAt(it.At); !ok {
					continue
				}
			}
			it.Stats = &contentStats{}
			p := pub{it, at}
			all = append(all, p)
			if param := linkParam(it.Link); param != "" {
				label := startSource(param)
				byLabel[label] = append(byLabel[label], p)
			}
		}
	}
	for _, ps := range byLabel {
		sort.SliceStable(ps, func(i, j int) bool { return ps[i].at.Before(ps[j].at) })
	}
	for _, x := range leads {
		l, _ := x.(map[string]any)
		if l == nil {
			continue
		}
		ps := byLabel[fmt.Sprint(l["source"])]
		if len(ps) == 0 {
			continue
		}
		var hit *contentItem
		if t, ok := leadTime(l); ok {
			for _, p := range ps { // the latest post with that link before the lead came
				if !p.at.After(t) {
					hit = p.it
				}
			}
		} else {
			hit = ps[len(ps)-1].it
		}
		if hit == nil {
			continue
		}
		hit.Stats.Leads++
		col := fmt.Sprint(l["col"])
		if col == "meet" || col == "diag" || col == "won" || (l["razborSlot"] != nil && fmt.Sprint(l["razborSlot"]) != "") {
			hit.Stats.Razbor++
		}
		if col == "won" {
			hit.Stats.Residents++
		}
	}
	weeks := map[string]*contentWeek{}
	total := map[string]*contentAgg{}
	for _, ch := range contentChannels {
		total[ch] = &contentAgg{}
	}
	for _, p := range all {
		k := weekOf(p.at)
		w := weeks[k]
		if w == nil {
			w = &contentWeek{Week: k, Channels: map[string]*contentAgg{}}
			for _, ch := range contentChannels {
				w.Channels[ch] = &contentAgg{}
			}
			weeks[k] = w
		}
		for _, a := range []*contentAgg{w.Channels[p.it.Channel], total[p.it.Channel]} {
			if a == nil {
				continue
			}
			a.Posts++
			a.Leads += p.it.Stats.Leads
			a.Razbor += p.it.Stats.Razbor
			a.Residents += p.it.Stats.Residents
		}
	}
	var list []*contentWeek
	for _, w := range weeks {
		w.Threads, w.Telegram, w.Instagram = w.Channels["threads"], w.Channels["telegram"], w.Channels["instagram"]
		w.Channels = nil
		list = append(list, w)
	}
	sort.Slice(list, func(i, j int) bool { return list[i].Week > list[j].Week })
	if len(list) > 26 {
		list = list[:26]
	}
	if list == nil {
		list = []*contentWeek{}
	}
	d.Stats, _ = json.Marshal(map[string]any{"total": total, "weeks": list})
}

// Stats counts the leads of the published posts (hourly).
func (e *ContentEngine) Stats(ctx context.Context) error {
	var leads []any
	if cd, err := e.docs.GetDoc(ctx, "club", "bs_crm"); err == nil && cd != nil && !cd.Deleted {
		var crm map[string]any
		_ = json.Unmarshal([]byte(cd.Value), &crm)
		leads, _ = crm["leads"].([]any)
	}
	_, err := e.update(ctx, func(d *contentDoc) bool {
		countContent(d, leads)
		return true
	})
	return err
}

// Loop: plan and count hourly, publish and preview every minute.
func (e *ContentEngine) Loop(ctx context.Context) {
	hourly := func() {
		c, cancel := context.WithTimeout(ctx, time.Minute)
		defer cancel()
		if n, _, err := e.Plan(c, false); err != nil {
			log.Printf("content: plan: %v", err)
		} else if n > 0 {
			log.Printf("content: planned %d posts", n)
		}
		if err := e.Stats(c); err != nil {
			log.Printf("content: stats: %v", err)
		}
	}
	hourly()
	last := time.Now()
	t := time.NewTicker(time.Minute)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		if time.Since(last) >= time.Hour {
			hourly()
			last = time.Now()
		}
		e.Tick(ctx)
	}
}
