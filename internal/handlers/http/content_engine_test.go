package http

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/bnursik/business_surgery_backend/internal/ai"
	"github.com/bnursik/business_surgery_backend/internal/bot"
	"github.com/bnursik/business_surgery_backend/internal/content"
	pg "github.com/bnursik/business_surgery_backend/internal/repository/pg"
	"github.com/bnursik/business_surgery_backend/migrations"
	"github.com/bnursik/business_surgery_backend/pkg/auth"
	"github.com/gin-gonic/gin"
)

// cntDocs: platform docs in memory, with versions and conflicts.
type cntDocs struct {
	mu   sync.Mutex
	docs map[string]*pg.PlatformDoc
}

func (m *cntDocs) GetDoc(_ context.Context, scope, key string) (*pg.PlatformDoc, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if d := m.docs[scope+"/"+key]; d != nil {
		cp := *d
		return &cp, nil
	}
	return nil, nil
}

func (m *cntDocs) PutDoc(_ context.Context, scope, key string, base int, value string, deleted bool, by string) (*pg.PlatformDoc, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.docs == nil {
		m.docs = map[string]*pg.PlatformDoc{}
	}
	cur := m.docs[scope+"/"+key]
	v := 0
	if cur != nil {
		v = cur.Version
	}
	if base != v {
		return cur, pg.ErrPlatformConflict
	}
	d := &pg.PlatformDoc{Scope: scope, Key: key, Value: value, Version: v + 1, Deleted: deleted}
	m.docs[scope+"/"+key] = d
	return d, nil
}

func (m *cntDocs) doc(t *testing.T) *contentDoc {
	t.Helper()
	d := m.docs["club/"+contentKey]
	if d == nil {
		t.Fatal("no bs_content")
	}
	var out contentDoc
	if err := json.Unmarshal([]byte(d.Value), &out); err != nil {
		t.Fatal(err)
	}
	return &out
}

// cntLib: 16 guides in 4 organs, two cases, two objections, one format item.
func cntLib() []*content.LibItem {
	organs := []string{"Финансы", "Продажи", "Команда", "Маркетинг"}
	var out []*content.LibItem
	for i := 1; i <= 16; i++ {
		src := fmt.Sprintf("g%03d", i)
		out = append(out, &content.LibItem{Src: src, Organ: organs[(i-1)/4], Title: "Гайд " + src,
			Threads:  []string{"Пост A " + src + "\n\nГайд бесплатно: t.me/bsurgery_bot?start=th_" + src, "Пост B " + src + "\n\nГайд бесплатно: t.me/bsurgery_bot?start=th_" + src},
			Telegram: "Канал " + src + "\n\nt.me/bsurgery_bot?start=tg_" + src,
			Carousel: json.RawMessage(`{"slides":[{"kind":"cover","title":"` + src + `"}],"caption":"Подпись ` + src + `"}`),
			Reels:    json.RawMessage(`{"hook":"Хук ` + src + `","beats":["раз","два"],"cta":"Пиши","duration":40}`)})
	}
	for _, r := range []struct{ src, rub string }{{"case_a", "case"}, {"case_b", "case"}, {"obj_a", "objection"}, {"obj_b", "objection"}, {"fmt_a", "format"}} {
		out = append(out, &content.LibItem{Src: r.src, Rubric: r.rub, Title: "Рубрика " + r.src,
			Threads:  []string{"Рубрика " + r.src + " t.me/bsurgery_bot?start=th_" + r.rub},
			Telegram: "Рубрика канал " + r.src + " t.me/bsurgery_bot?start=tg_" + r.rub})
	}
	return out
}

func cntEngine(docs funnelDocs, now *time.Time) *ContentEngine {
	e := NewContentEngine(docs)
	e.Lib = cntLib
	e.now = func() time.Time { return *now }
	e.PlatformURL = "https://app.test/platform"
	return e
}

func cntChannel(d *contentDoc, ch string) []*contentItem {
	var out []*contentItem
	for _, it := range d.Queue {
		if it.Channel == ch {
			out = append(out, it)
		}
	}
	return out
}

func TestContentPlanning(t *testing.T) {
	ctx := context.Background()
	docs := &cntDocs{}
	now := time.Date(2026, 10, 5, 8, 0, 0, 0, almaty) // Monday
	e := cntEngine(docs, &now)
	// A guide published in Threads 30 days ago is not repeated there; Telegram may use it.
	hist, _ := json.Marshal(map[string]any{"history": []map[string]any{{"id": "old", "src": "g001", "kind": "threads", "channel": "threads", "organ": "Финансы",
		"text": "x", "at": now.AddDate(0, 0, -30).Format(time.RFC3339), "status": "published"}}, "teamNote": "keep me"})
	_, _ = docs.PutDoc(ctx, "club", contentKey, 0, string(hist), false, "t")

	added, _, err := e.Plan(ctx, false)
	if err != nil || added != 24 { // 12 days (no Sundays) × Threads and Telegram
		t.Fatalf("plan: %d %v", added, err)
	}
	d := docs.doc(t)
	if string(d.extra["teamNote"]) != `"keep me"` || len(d.History) != 1 {
		t.Fatalf("the team's fields and the history are kept: %v %d", d.extra, len(d.History))
	}
	st := parseContentSettings(d.Settings)
	if !st.Channels.Threads.On || st.Channels.Telegram.Chat != "@bsurgery_kz" || st.Channels.Instagram.On || st.Approval != "auto" || st.PreviewHour != 9 {
		t.Fatalf("default settings %s", d.Settings)
	}
	ids := map[string]bool{}
	for _, ch := range []string{"threads", "telegram"} {
		items := cntChannel(d, ch)
		if len(items) != 12 {
			t.Fatalf("%s: %d items", ch, len(items))
		}
		seen := map[string]bool{}
		var seq []*contentItem // the channel's posts in order, history first
		for _, h := range d.History {
			if h.Channel == ch {
				seq = append(seq, h)
			}
		}
		seq = append(seq, items...)
		lastRub := ""
		for i, it := range items {
			at, _ := parseContentAt(it.At)
			if at.In(almaty).Weekday() == time.Sunday || ids[it.ID] || it.Status != "planned" || !it.Auto {
				t.Fatalf("%s item %d: %+v", ch, i, it.contentItemData)
			}
			ids[it.ID] = true
			want := "10:00"
			if ch == "telegram" {
				want = "19:00"
			}
			if atClock(it.At) != want || !strings.HasSuffix(it.At, "+05:00") {
				t.Fatalf("%s time %s", ch, it.At)
			}
			k := len(seq) - len(items) + i
			threeGuides := k >= 3 && seq[k-1].Rubric == "" && seq[k-2].Rubric == "" && seq[k-3].Rubric == ""
			if threeGuides != (it.Rubric != "") {
				t.Fatalf("%s: post %d rubric %q after three guides %v", ch, i+1, it.Rubric, threeGuides)
			}
			if it.Rubric != "" {
				if it.Rubric == lastRub {
					t.Fatalf("%s: rubrics must rotate, %s twice", ch, it.Rubric)
				}
				lastRub = it.Rubric
				continue
			}
			for j := k - 1; j >= 0; j-- { // the guide before
				if seq[j].Rubric == "" {
					if seq[j].Organ == it.Organ {
						t.Fatalf("%s: organ %s twice in a row (%d)", ch, it.Organ, i+1)
					}
					break
				}
			}
			if seen[it.Src] {
				t.Fatalf("%s: guide %s repeated", ch, it.Src)
			}
			seen[it.Src] = true
			prefix := map[string]string{"threads": "th_", "telegram": "tg_"}[ch]
			if it.Link != contentBotLink+prefix+it.Src || it.Title != "Гайд "+it.Src {
				t.Fatalf("%s link %q title %q", ch, it.Link, it.Title)
			}
		}
		rub := 0
		for _, it := range items {
			if it.Rubric != "" {
				rub++
			}
		}
		if rub != 3 {
			t.Fatalf("%s: %d rubric posts in 12", ch, rub)
		}
		if ch == "threads" && seen["g001"] {
			t.Fatal("g001 was in Threads 30 days ago")
		}
	}
	if items := cntChannel(d, "telegram"); items[0].Src != "g001" {
		t.Fatalf("Telegram may take g001: %s", items[0].Src)
	}
	// the same day's two channels prefer different guides
	for _, a := range cntChannel(d, "threads") {
		for _, b := range cntChannel(d, "telegram") {
			if a.day() == b.day() && a.Src == b.Src && a.Rubric == "" {
				t.Fatalf("same guide %s on both channels on %s", a.Src, a.day())
			}
		}
	}

	// Planning again changes nothing.
	ver := docs.docs["club/"+contentKey].Version
	if n, _, _ := e.Plan(ctx, false); n != 0 || docs.docs["club/"+contentKey].Version != ver {
		t.Fatalf("second plan: %d, version %d → %d", n, ver, docs.docs["club/"+contentKey].Version)
	}

	// The team edits a post's text, adds its own field and turns Telegram off.
	raw := docs.docs["club/"+contentKey]
	var m map[string]any
	_ = json.Unmarshal([]byte(raw.Value), &m)
	q := m["queue"].([]any)
	var editedID, tgEdited string
	for _, x := range q {
		it := x.(map[string]any)
		if it["channel"] == "threads" && editedID == "" && strings.HasPrefix(it["id"].(string), "th-20261007") {
			it["text"] = "Свой текст команды t.me/bsurgery_bot?start=th_g999"
			it["note"] = "проверить цифры"
			editedID = it["id"].(string)
		}
		if it["channel"] == "telegram" && tgEdited == "" && strings.HasPrefix(it["id"].(string), "tg-20261008") {
			it["edited"] = true
			tgEdited = it["id"].(string)
		}
	}
	m["settings"].(map[string]any)["channels"].(map[string]any)["telegram"].(map[string]any)["on"] = false
	b, _ := json.Marshal(m)
	if _, err := docs.PutDoc(ctx, "club", contentKey, raw.Version, string(b), false, "tg:1"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := e.Plan(ctx, true); err != nil {
		t.Fatal(err)
	}
	d = docs.doc(t)
	var kept *contentItem
	for _, it := range d.Queue {
		if it.ID == editedID {
			kept = it
		}
	}
	if kept == nil || !kept.Edited || !strings.HasPrefix(kept.Text, "Свой текст") || string(kept.extra["note"]) != `"проверить цифры"` {
		t.Fatalf("the team's post must stay as edited: %+v", kept)
	}
	tg := cntChannel(d, "telegram")
	if len(tg) != 1 || tg[0].ID != tgEdited {
		t.Fatalf("Telegram off: only the edited post stays, got %d", len(tg))
	}
	if len(cntChannel(d, "threads")) != 12 {
		t.Fatalf("threads re-planned: %d", len(cntChannel(d, "threads")))
	}

	// Instagram on: carousel and reels take turns, published by hand.
	_ = json.Unmarshal([]byte(docs.docs["club/"+contentKey].Value), &m)
	m["settings"].(map[string]any)["channels"].(map[string]any)["instagram"] = map[string]any{"on": true, "time": "12:30"}
	b, _ = json.Marshal(m)
	_, _ = docs.PutDoc(ctx, "club", contentKey, docs.docs["club/"+contentKey].Version, string(b), false, "tg:1")
	_, _, _ = e.Plan(ctx, false)
	ig := cntChannel(docs.doc(t), "instagram")
	if len(ig) != 12 || ig[0].Kind != "carousel" || ig[1].Kind != "reels" || ig[2].Kind != "carousel" || !ig[0].Manual ||
		ig[0].Caption == "" || len(ig[0].Slides) == 0 || !strings.Contains(ig[1].Text, "Хук:") || atClock(ig[0].At) != "12:30" ||
		!strings.HasPrefix(ig[0].Link, contentBotLink+"ig_") {
		t.Fatalf("instagram %+v / %+v", ig[0].contentItemData, ig[1].contentItemData)
	}
	// rubric items without Instagram content are passed over
	for _, it := range ig {
		if it.Rubric != "" {
			t.Fatalf("no rubric item has Instagram content: %s", it.Src)
		}
	}
}

// cntTelegram: a fake Telegram Bot API.
type cntTelegram struct {
	mu      sync.Mutex
	calls   []map[string]any
	noAdmin bool
}

func (f *cntTelegram) handler(w http.ResponseWriter, r *http.Request) {
	method := r.URL.Path[strings.LastIndex(r.URL.Path, "/")+1:]
	var p map[string]any
	_ = json.NewDecoder(r.Body).Decode(&p)
	f.mu.Lock()
	defer f.mu.Unlock()
	p["_method"] = method
	f.calls = append(f.calls, p)
	if method == "sendMessage" && p["chat_id"] == "@bsurgery_kz" && f.noAdmin {
		_, _ = w.Write([]byte(`{"ok":false,"description":"Bad Request: chat not found"}`))
		return
	}
	_, _ = w.Write([]byte(`{"ok":true,"result":{"message_id":77}}`))
}

func (f *cntTelegram) find(method string, match func(p map[string]any) bool) []map[string]any {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []map[string]any
	for _, c := range f.calls {
		if c["_method"] == method && (match == nil || match(c)) {
			out = append(out, c)
		}
	}
	return out
}

func TestContentPreviewPublish(t *testing.T) {
	dsn := os.Getenv("BS_TEST_DSN")
	if dsn == "" {
		t.Skip("BS_TEST_DSN not set")
	}
	ctx := context.Background()
	db, err := pg.NewDB(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Pool.Close()
	_ = pg.Migrate(ctx, db, migrations.FS)
	_, _ = db.Pool.Exec(ctx, `DELETE FROM platform_docs WHERE key IN ('bs_content','content_state','bs_threads_status','threads')`)
	threadsWait = time.Millisecond

	var posted []string
	th := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/refresh_access_token":
			_, _ = w.Write([]byte(`{"access_token":"tok1"}`))
		case r.URL.Path == "/v1.0/me":
			_, _ = w.Write([]byte(`{"id":"42","username":"bsurgery"}`))
		case strings.HasSuffix(r.URL.Path, "/threads"):
			posted = append(posted, r.URL.Query().Get("text"))
			_, _ = w.Write([]byte(`{"id":"c1"}`))
		case strings.HasSuffix(r.URL.Path, "/threads_publish"):
			_, _ = w.Write([]byte(fmt.Sprintf(`{"id":"p%d"}`, len(posted))))
		case strings.HasPrefix(r.URL.Path, "/v1.0/p"):
			_, _ = w.Write([]byte(`{"permalink":"https://www.threads.net/@bsurgery/post/X` + r.URL.Path[6:] + `"}`))
		}
	}))
	defer th.Close()
	t.Setenv("THREADS_API_BASE", th.URL)
	t.Setenv("THREADS_TOKEN", "tok1")
	ftg := &cntTelegram{}
	tgs := httptest.NewServer(http.HandlerFunc(ftg.handler))
	defer tgs.Close()

	repo := pg.NewPlatformRepo(db)
	pa := NewPlatformAI(repo, &ai.Client{HTTP: th.Client()})
	svc := bot.New(nil, bot.Options{Token: "T", APIBase: tgs.URL, Admins: []int64{453800951}})
	now := time.Date(2026, 10, 5, 8, 0, 0, 0, almaty)
	e := cntEngine(repo, &now)
	e.Threads = pa.PublishThreadsText
	e.Channel, e.Send, e.Edit = svc.SendChannel, svc.SendMessageID, svc.EditMessageKB
	e.Owner = 453800951

	if _, _, err := e.Plan(ctx, false); err != nil {
		t.Fatal(err)
	}
	load := func() *contentDoc {
		d, _, err := e.load(ctx)
		if err != nil {
			t.Fatal(err)
		}
		return d
	}
	today := dayItems(load(), "20261005")
	if len(today) != 2 || today[0].Channel != "threads" || today[1].Channel != "telegram" {
		t.Fatalf("today %d", len(today))
	}

	// The morning preview: not before 09:00, then once.
	e.Tick(ctx)
	if len(ftg.find("sendMessage", nil)) != 0 {
		t.Fatal("preview before 09:00")
	}
	now = now.Add(65 * time.Minute)
	e.Tick(ctx)
	e.Tick(ctx)
	pv := ftg.find("sendMessage", func(p map[string]any) bool { return p["chat_id"] == float64(453800951) })
	if len(pv) != 1 {
		t.Fatalf("preview sent %d times", len(pv))
	}
	text := pv[0]["text"].(string)
	kbj, _ := json.Marshal(pv[0]["reply_markup"])
	if !strings.Contains(text, "5 октября") || !strings.Contains(text, "1. Threads · 10:00") || !strings.Contains(text, "2. Telegram-канал · 19:00") ||
		!strings.Contains(text, content.FirstLine(today[0].Text, 120)) ||
		!strings.Contains(string(kbj), `"cnt_ok_20261005"`) || !strings.Contains(string(kbj), `"cnt_skip_`+today[1].ID+`"`) ||
		!strings.Contains(string(kbj), "section=content\\u0026id="+today[0].ID) {
		t.Fatalf("preview:\n%s\n%s", text, kbj)
	}

	// «Пропустить» the Telegram post: the message shows it at once.
	toast, ok := e.HandleCallback(ctx, bot.CallbackUpdate{ChatID: 453800951, MessageID: 77, FromID: 453800951, Data: "cnt_skip_" + today[1].ID})
	ed := ftg.find("editMessageText", nil)
	if !ok || toast != "Пропущено" || len(ed) != 1 || !strings.Contains(ed[0]["text"].(string), "⏭ пропущен") {
		t.Fatalf("skip: %q %v %v", toast, ok, ed)
	}

	// Auto mode: the Threads post goes out at 10:00 by itself, the skipped one does not.
	if !e.OwnsThreadsDay(ctx, now) {
		t.Fatal("the queue owns today's Threads post")
	}
	now = time.Date(2026, 10, 5, 10, 1, 0, 0, almaty)
	e.Tick(ctx)
	d := load()
	it := findContent(d, today[0].ID)
	if len(posted) != 1 || posted[0] != today[0].Text || it.Status != "published" || it.PostID != "p1" || !strings.Contains(it.URL, "threads.net/@bsurgery/post/") || it.PublishedAt == "" {
		t.Fatalf("threads: %q %+v", posted, it.contentItemData)
	}
	now = time.Date(2026, 10, 5, 19, 1, 0, 0, almaty)
	e.Tick(ctx)
	if n := len(ftg.find("sendMessage", func(p map[string]any) bool { return p["chat_id"] == "@bsurgery_kz" })); n != 0 {
		t.Fatalf("a skipped post was published: %d", n)
	}

	// Manual mode: nothing goes out until «Всё ок».
	var m map[string]any
	pd, _ := repo.GetDoc(ctx, "club", contentKey)
	_ = json.Unmarshal([]byte(pd.Value), &m)
	m["settings"].(map[string]any)["approval"] = "manual"
	b, _ := json.Marshal(m)
	if _, err := repo.PutDoc(ctx, "club", contentKey, pd.Version, string(b), false, "tg:1"); err != nil {
		t.Fatal(err)
	}
	now = time.Date(2026, 10, 6, 9, 0, 0, 0, almaty)
	e.Tick(ctx)
	pv = ftg.find("sendMessage", func(p map[string]any) bool { return p["chat_id"] == float64(453800951) })
	if len(pv) != 2 || !strings.Contains(pv[1]["text"].(string), "ждёт подтверждения") || !strings.Contains(pv[1]["text"].(string), "Ручной режим") {
		t.Fatalf("manual preview: %v", pv)
	}
	now = time.Date(2026, 10, 6, 10, 1, 0, 0, almaty)
	e.Tick(ctx)
	if len(posted) != 1 {
		t.Fatal("manual mode published without approval")
	}
	toast, _ = e.HandleCallback(ctx, bot.CallbackUpdate{ChatID: 453800951, MessageID: 77, FromID: 453800951, Data: "cnt_ok_20261006"})
	if toast != "Подтверждено постов: 2" {
		t.Fatalf("ok: %q", toast)
	}
	e.Tick(ctx)
	tue := dayItems(load(), "20261006")
	if len(posted) != 2 || tue[0].Status != "published" || tue[0].ApprovedBy != "tg:453800951" {
		t.Fatalf("approved threads: %d %+v", len(posted), tue[0].contentItemData)
	}

	// Telegram channel without the bot as admin: failed with a clear error, the owner is told.
	ftg.noAdmin = true
	now = time.Date(2026, 10, 6, 19, 0, 30, 0, almaty)
	e.Tick(ctx)
	tue = dayItems(load(), "20261006")
	if tue[1].Status != "failed" || !strings.HasPrefix(tue[1].Error, "Добавьте @bsurgery_bot администратором канала") {
		t.Fatalf("channel error: %+v", tue[1].contentItemData)
	}
	warn := ftg.find("sendMessage", func(p map[string]any) bool {
		return p["chat_id"] == float64(453800951) && strings.Contains(fmt.Sprint(p["text"]), "Пост не вышел")
	})
	if len(warn) != 1 || !strings.Contains(warn[0]["text"].(string), "администратором канала") {
		t.Fatalf("owner warning: %v", warn)
	}

	// Back to auto; the channel works; a manual late post is closed.
	ftg.noAdmin = false
	pd, _ = repo.GetDoc(ctx, "club", contentKey)
	_ = json.Unmarshal([]byte(pd.Value), &m)
	m["settings"].(map[string]any)["approval"] = "auto"
	b, _ = json.Marshal(m)
	_, _ = repo.PutDoc(ctx, "club", contentKey, pd.Version, string(b), false, "tg:1")
	now = time.Date(2026, 10, 7, 19, 2, 0, 0, almaty)
	e.Tick(ctx) // the Threads post of 10:00 is 9 hours late: failed, not published
	wed := dayItems(load(), "20261007")
	if wed[0].Status != "failed" || !strings.Contains(wed[0].Error, "вовремя") || wed[1].Status != "published" || wed[1].URL != "https://t.me/bsurgery_kz/77" {
		t.Fatalf("wednesday: %+v / %+v", wed[0].contentItemData, wed[1].contentItemData)
	}
	ch := ftg.find("sendMessage", func(p map[string]any) bool { return p["chat_id"] == "@bsurgery_kz" })
	if len(ch) != 2 || ch[1]["text"] != wed[1].Text {
		t.Fatalf("channel posts: %v", ch)
	}

	// «Опубликовать сейчас» from the platform; twice is refused.
	thu := dayItems(load(), "20261008")
	if _, err := e.Publish(ctx, thu[0].ID, true); err != nil {
		t.Fatal(err)
	}
	if _, err := e.Publish(ctx, thu[0].ID, true); err == nil || err.Error() != "Уже опубликовано" {
		t.Fatalf("twice: %v", err)
	}
	if _, err := e.Publish(ctx, "nope", true); err != errContentNotFound {
		t.Fatalf("unknown: %v", err)
	}
	if !e.OwnsThreadsDay(ctx, time.Date(2026, 10, 8, 10, 0, 0, 0, almaty)) || e.OwnsThreadsDay(ctx, time.Date(2026, 10, 11, 10, 0, 0, 0, almaty)) {
		t.Fatal("Sunday has no Threads post in the queue: «Полезное» posts then")
	}
	st, _ := repo.GetDoc(ctx, "club", "bs_threads_status")
	if st == nil || !strings.Contains(st.Value, `"username":"bsurgery"`) {
		t.Fatalf("threads status %v", st)
	}
}

func TestContentAttribution(t *testing.T) {
	g := content.GuideTitle("g001")
	cases := map[string]string{
		"th_g001":      "Threads: " + g,
		"tg_g001":      "Telegram-канал: " + g,
		"ig_g001":      "Instagram: " + g,
		"th_case":      "Threads: Кейсы",
		"tg_razbor":    "Telegram-канал: Разбор",
		"ig_some_post": "Instagram: some post",
		"threads":      "Threads",
		"threads_x":    "Threads: x",
	}
	for p, want := range cases {
		if got := startSource(p); got != want {
			t.Errorf("%s: %q, want %q", p, got, want)
		}
	}
	if content.StartParam("Гайд бесплатно: t.me/bsurgery_bot?start=th_g076") != "th_g076" {
		t.Error("start param")
	}
}

func TestContentStats(t *testing.T) {
	ctx := context.Background()
	docs := &cntDocs{}
	g := content.GuideTitle("g001")
	pub := func(id, ch, prefix, at string) map[string]any {
		return map[string]any{"id": id, "src": "g001", "kind": ch, "channel": ch, "text": "x", "at": at, "publishedAt": at,
			"status": "published", "link": contentBotLink + prefix + "_g001"}
	}
	doc := map[string]any{
		"history": []any{pub("a", "threads", "th", "2026-09-01T10:00:00+05:00")},
		"queue": []any{pub("b", "threads", "th", "2026-10-05T10:00:00+05:00"), pub("c", "telegram", "tg", "2026-10-05T19:00:00+05:00"),
			map[string]any{"id": "d", "src": "g002", "kind": "threads", "channel": "threads", "text": "y", "at": "2026-10-06T10:00:00+05:00", "status": "planned", "link": contentBotLink + "th_g002"}},
	}
	b, _ := json.Marshal(doc)
	_, _ = docs.PutDoc(ctx, "club", contentKey, 0, string(b), false, "t")
	crm := map[string]any{"leads": []any{
		map[string]any{"source": "Threads: " + g, "startAt": "2026-09-02T08:00:00Z", "col": "won"},            // a
		map[string]any{"source": "Threads: " + g, "startAt": "2026-10-05T12:00:00Z", "col": "new"},            // b
		map[string]any{"source": "Threads: " + g, "startAt": "2026-10-06T12:00:00Z", "col": "meet"},           // b
		map[string]any{"source": "Threads: " + g, "date": "07.10.2026", "col": "contact", "razborSlot": "s1"}, // b
		map[string]any{"source": "Telegram-канал: " + g, "startAt": "2026-10-05T15:00:00Z", "col": "won"},     // c
		map[string]any{"source": "Telegram-канал: " + g, "startAt": "2026-10-01T15:00:00Z", "col": "won"},     // before the post
		map[string]any{"source": "Instagram профиль", "startAt": "2026-10-05T15:00:00Z", "col": "won"},        // other
		map[string]any{"source": "Threads: " + content.GuideTitle("g002"), "startAt": "2026-10-06T15:00:00Z"}, // not published
	}}
	b, _ = json.Marshal(crm)
	_, _ = docs.PutDoc(ctx, "club", "bs_crm", 0, string(b), false, "t")
	now := time.Date(2026, 10, 7, 12, 0, 0, 0, almaty)
	e := cntEngine(docs, &now)
	if err := e.Stats(ctx); err != nil {
		t.Fatal(err)
	}
	d := docs.doc(t)
	want := map[string]contentStats{"a": {1, 1, 1}, "b": {3, 2, 0}, "c": {1, 1, 1}}
	for _, list := range [][]*contentItem{d.History, d.Queue} {
		for _, it := range list {
			if w, ok := want[it.ID]; ok && (it.Stats == nil || *it.Stats != w) {
				t.Errorf("%s: %+v, want %+v", it.ID, it.Stats, w)
			}
			if it.ID == "d" && it.Stats != nil {
				t.Error("a planned post has no stats")
			}
		}
	}
	var s struct {
		Total map[string]contentAgg `json:"total"`
		Weeks []struct {
			Week     string     `json:"week"`
			Threads  contentAgg `json:"threads"`
			Telegram contentAgg `json:"telegram"`
		} `json:"weeks"`
	}
	_ = json.Unmarshal(d.Stats, &s)
	if len(s.Weeks) != 2 || s.Weeks[0].Week != "2026-10-05" || s.Weeks[0].Threads != (contentAgg{1, 3, 2, 0}) || s.Weeks[0].Telegram != (contentAgg{1, 1, 1, 1}) ||
		s.Weeks[1].Week != "2026-08-31" || s.Total["threads"] != (contentAgg{2, 4, 3, 1}) {
		t.Fatalf("weekly: %s", d.Stats)
	}
}

func TestContentEndpoints(t *testing.T) {
	gin.SetMode(gin.TestMode)
	docs := &cntDocs{}
	now := time.Date(2026, 10, 5, 8, 0, 0, 0, almaty)
	e := cntEngine(docs, &now)
	r := gin.New()
	NewContentModule(e, []byte("cnt-secret")).Register(r)
	tok := func(sub, role string) string {
		acc, _, _ := auth.NewManager("cnt-secret", time.Hour, time.Hour).GenerateTokens(sub, role, nil)
		return acc
	}
	body := `{}`
	do := func(method, path, tk string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, path, bytes.NewBufferString(body))
		req.Header.Set("Authorization", "Bearer "+tk)
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		return w
	}
	team := tok("tg:453800951", "admin")
	if w := do("GET", "/api/v1/platform/content/library", tok("tg:490685605", "resident")); w.Code != http.StatusForbidden {
		t.Fatalf("resident: %d", w.Code)
	}
	w := do("GET", "/api/v1/platform/content/library", team)
	var lib struct {
		Items  []map[string]any `json:"items"`
		Counts map[string]int   `json:"counts"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &lib)
	if w.Code != 200 || lib.Counts["guides"] != 16 || lib.Counts["rubric"] != 5 || lib.Counts["threads"] != 37 || lib.Items[0]["first"] != "Пост A g001" || lib.Items[0]["text"] != nil {
		t.Fatalf("library %d %v %v", w.Code, lib.Counts, lib.Items[0])
	}
	w = do("GET", "/api/v1/platform/content/library?src=g002", team)
	if !strings.Contains(w.Body.String(), `"slides":[{"kind":"cover"`) || !strings.Contains(w.Body.String(), `"text":"Канал g002`) {
		t.Fatalf("one source: %s", w.Body.String())
	}
	w = do("POST", "/api/v1/platform/content/plan", team)
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"added":24`) {
		t.Fatalf("plan: %d %s", w.Code, w.Body.String())
	}
	if w = do("POST", "/api/v1/platform/content/publish/nope", team); w.Code != 404 {
		t.Fatalf("publish unknown: %d", w.Code)
	}
	id := docs.doc(t).Queue[1].ID // Telegram, no bot here
	w = do("POST", "/api/v1/platform/content/publish/"+id, team)
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"ok":false`) || !strings.Contains(w.Body.String(), "Бот не настроен") || docs.doc(t).Queue[1].Status != "failed" {
		t.Fatalf("publish without bot: %s", w.Body.String())
	}
	// The editor's text wins over the doc, and a not-yet-synced item is created.
	body = `{"text":"Правка из редактора","channel":"telegram","kind":"telegram"}`
	id2 := docs.doc(t).Queue[3].ID
	_ = do("POST", "/api/v1/platform/content/publish/"+id2, team)
	if it := findContent(docs.doc(t), id2); it == nil || it.Text != "Правка из редактора" || !it.Edited {
		t.Fatalf("override: %+v", it)
	}
	_ = do("POST", "/api/v1/platform/content/publish/tg-new-1", team)
	if it := findContent(docs.doc(t), "tg-new-1"); it == nil || it.Channel != "telegram" || it.Status != "failed" {
		t.Fatalf("new item: %+v", it)
	}
	body = `{}`
	if w = do("POST", "/api/v1/platform/content/plan", team); !strings.Contains(w.Body.String(), `"queue":[{`) {
		t.Fatalf("plan returns the queue: %s", w.Body.String()[:200])
	}
}
