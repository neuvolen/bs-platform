package http

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/bnursik/business_surgery_backend/internal/bot"
	"github.com/gin-gonic/gin"
)

// thTG: a fake Telegram: what the bot sent and edited.
type thTG struct {
	mu     sync.Mutex
	sent   []thMsg
	edits  []thMsg
	refuse func(kb map[string]any) error // Telegram refuses a button
}

type thMsg struct {
	chat, id int64
	text     string
	kb       map[string]any
}

func (f *thTG) send(_ context.Context, chat int64, text string, kb map[string]any) (int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.refuse != nil {
		if err := f.refuse(kb); err != nil {
			return 0, err
		}
	}
	f.sent = append(f.sent, thMsg{chat: chat, id: int64(100 + len(f.sent)), text: text, kb: kb})
	return int64(100 + len(f.sent) - 1), nil
}

func (f *thTG) edit(_ context.Context, chat, id int64, text string, kb map[string]any) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.edits = append(f.edits, thMsg{chat: chat, id: id, text: text, kb: kb})
	return nil
}

// kbButtons: the keyboard's buttons as text → url or callback data.
func kbButtons(kb map[string]any) map[string]string {
	out := map[string]string{}
	rows, _ := kb["inline_keyboard"].([][]map[string]any)
	for _, r := range rows {
		for _, b := range r {
			v, _ := b["url"].(string)
			if v == "" {
				v, _ = b["callback_data"].(string)
			}
			out[b["text"].(string)] = v
		}
	}
	return out
}

func thManualEngine(t *testing.T, now *time.Time) (*ContentEngine, *cntDocs, *thTG) {
	t.Helper()
	docs := &cntDocs{}
	thDoc(t, docs, 16)
	e := cntEngine(docs, now)
	e.Lib = thLib
	e.AI = (&thFakeAI{}).call
	e.Go = func(f func()) { f() }
	e.ThreadsManual = func(context.Context) bool { return true }
	e.Threads = func(context.Context, string, string) (string, string, error) {
		t.Fatal("the manual mode must not call the Threads API")
		return "", "", nil
	}
	tg := &thTG{}
	e.Send, e.Edit, e.Owner = tg.send, tg.edit, 453800951
	return e, docs, tg
}

func TestThreadsManualSlots(t *testing.T) {
	day := time.Date(2026, 10, 5, 0, 0, 0, 0, almaty)
	for k := 0; k < 30; k++ {
		d := day.AddDate(0, 0, k)
		s := threadsManualSlots(d, 4)
		if len(s) != 4 {
			t.Fatalf("%d slots", len(s))
		}
		for i, x := range s {
			a := x.In(almaty)
			m := a.Hour()*60 + a.Minute()
			anchor := []int{9*60 + 30, 12*60 + 30, 16*60 + 30, 19*60 + 30}[i]
			if m < anchor-13 || m > anchor+13 || m%15 == 0 || !inManualHours(x) || contentDay(x) != contentDay(d) {
				t.Fatalf("day %d slot %d: %s", k, i, a.Format("15:04"))
			}
		}
	}
	if a, b := threadsManualSlots(day, 4), threadsManualSlots(day, 4); !a[0].Equal(b[0]) {
		t.Fatal("the same day must give the same times")
	}
	for _, n := range []int{1, 2, 3, 6, 12} {
		s := threadsManualSlots(day, n)
		if len(s) != n {
			t.Fatalf("n=%d: %d", n, len(s))
		}
		for _, x := range s {
			if !inManualHours(x) {
				t.Fatalf("n=%d outside 09:00-21:00: %s", n, x.In(almaty).Format("15:04"))
			}
		}
	}
}

func TestThreadsManualFlow(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 10, 5, 6, 30, 0, 0, almaty)
	e, docs, tg := thManualEngine(t, &now)

	// the batch: 4 posts at the manual times, each with its own bot link
	res, err := e.BuildThreadsDay(ctx, now, false)
	if err != nil {
		t.Fatal(err)
	}
	items := thItems(docs.doc(t), "20261005")
	if res.PerDay != 4 || len(items) != 4 {
		t.Fatalf("manual batch: %+v, %d items", res, len(items))
	}
	slots := threadsManualSlots(now, 4)
	for i, it := range items {
		at, _ := parseContentAt(it.At)
		p := thPostParam(it)
		if !at.Equal(slots[i]) || it.Format == "series" || len(it.Parts) > 0 || utf8.RuneCountInString(it.Text) > 500 || strings.ContainsAny(it.Text, "—") {
			t.Fatalf("item %d: %+v", i, it.contentItemData)
		}
		if !strings.HasPrefix(p, "th_p261005") || it.Link != contentBotLink+p || !strings.Contains(it.Text, "t.me/bsurgery_bot?start="+p) || strings.Contains(it.Text, "start=th_99") {
			t.Fatalf("item %d without its own link %s: %q", i, p, it.Text)
		}
	}
	// the mode for the platform
	e.Tick(ctx)
	if m := docs.doc(t).ThreadsMode; m == nil || !m.Manual || m.PerDay != 4 {
		t.Fatalf("threadsMode: %+v", m)
	}
	if len(tg.sent) != 0 {
		t.Fatalf("nothing before the first post's time: %+v", tg.sent)
	}

	// 1st post: one message, the exact text, the buttons
	now = slots[0].Add(-time.Minute)
	e.Tick(ctx)
	if len(tg.sent) != 0 {
		t.Fatal("a minute early")
	}
	now = slots[0]
	e.Tick(ctx)
	e.Tick(ctx)
	if len(tg.sent) != 1 {
		t.Fatalf("one message per post: %d", len(tg.sent))
	}
	m := tg.sent[0]
	first := findContent(docs.doc(t), items[0].ID)
	if m.chat != 453800951 || m.text != first.Text || first.Status != "sent" || first.MsgID != m.id || first.SentAt == "" {
		t.Fatalf("message %+v item %+v", m, first.contentItemData)
	}
	bt := kbButtons(m.kb)
	u := bt["Опубликовать в Threads"]
	pu, err := url.Parse(u)
	if err != nil || !strings.HasPrefix(u, "https://www.threads.net/intent/post?text=") || pu.Query().Get("text") != first.Text || strings.Contains(u, "+") {
		t.Fatalf("intent link %q", u)
	}
	if bt["✅ Опубликовал"] != "cnt_th_ok_"+first.ID || bt["Другой пост"] != "cnt_th_next_"+first.ID || bt["Пропустить"] != "cnt_th_skip_"+first.ID {
		t.Fatalf("buttons %v", bt)
	}

	// ✅: published by hand, the keyboard says so
	cb := func(data string, msg int64) string {
		toast, ok := e.HandleCallback(ctx, bot.CallbackUpdate{ChatID: 453800951, MessageID: msg, FromID: 453800951, Data: data})
		if !ok {
			t.Fatalf("%s not taken", data)
		}
		return toast
	}
	now = slots[0].Add(20 * time.Minute)
	if toast := cb("cnt_th_ok_"+first.ID, m.id); toast != "Отмечено: опубликован" {
		t.Fatalf("toast %q", toast)
	}
	first = findContent(docs.doc(t), first.ID)
	if first.Status != "published" || !first.ByHand || first.PublishedAt == "" || first.ApprovedBy != "tg:453800951" {
		t.Fatalf("after ✅: %+v", first.contentItemData)
	}
	if len(tg.edits) != 1 || tg.edits[0].id != m.id || tg.edits[0].text != first.Text || !strings.HasPrefix(firstKey(kbButtons(tg.edits[0].kb)), "✅ Опубликован") {
		t.Fatalf("edit %+v", tg.edits)
	}
	if toast := cb("cnt_th_ok_"+first.ID, m.id); toast != "Пост уже отмечен опубликованным" {
		t.Fatalf("second ✅: %q", toast)
	}

	// /status
	if st, text, sig := e.ThreadsStatus(ctx); st != "ok" || text != "ручной режим, сегодня опубликовано 1 из 4" || sig != "manual" {
		t.Fatalf("status %q %q %q", st, text, sig)
	}

	// 2nd post: «Другой пост» swaps with the 3rd
	now = slots[1]
	e.Tick(ctx)
	if len(tg.sent) != 2 {
		t.Fatalf("2nd post: %d messages", len(tg.sent))
	}
	d := docs.doc(t)
	second, third := findContent(d, items[1].ID), findContent(d, items[2].ID)
	oldSecond, oldThird := second.Text, third.Text
	if toast := cb("cnt_th_next_"+second.ID, tg.sent[1].id); toast != "Другой пост" {
		t.Fatalf("next: %q", toast)
	}
	d = docs.doc(t)
	second, third = findContent(d, items[1].ID), findContent(d, items[2].ID)
	strip := func(s string) string { return thStartRe.ReplaceAllString(s, "") }
	if second.Status != "sent" || strip(second.Text) != strip(oldThird) || strip(third.Text) != strip(oldSecond) ||
		!strings.Contains(second.Text, thPostParam(second)) || !strings.Contains(third.Text, thPostParam(third)) || third.Status != "planned" {
		t.Fatalf("swap:\n%q\n%q", second.Text, third.Text)
	}
	last := tg.edits[len(tg.edits)-1]
	if last.text != second.Text || kbButtons(last.kb)["✅ Опубликовал"] != "cnt_th_ok_"+second.ID {
		t.Fatalf("the message shows the new post: %+v", last)
	}
	if q, _ := url.Parse(kbButtons(last.kb)["Опубликовать в Threads"]); q.Query().Get("text") != second.Text {
		t.Fatal("the intent carries the new text")
	}

	// no reaction for 3 hours: «не опубликован», no reminder (the 3rd slot is 3.5+ hours later)
	n := len(tg.sent)
	sentAt, _ := parseContentAt(second.SentAt)
	now = sentAt.Add(thManualWait - time.Minute)
	e.Tick(ctx)
	if findContent(docs.doc(t), items[1].ID).Status != "sent" {
		t.Fatal("missed too early")
	}
	now = sentAt.Add(thManualWait)
	if !now.Before(slots[2]) {
		t.Fatalf("slots too close: %s %s", now, slots[2])
	}
	e.Tick(ctx)
	if second = findContent(docs.doc(t), items[1].ID); second.Status != "missed" || second.Error == "" || len(tg.sent) != n {
		t.Fatalf("after 3 hours, no nagging: %+v, %d messages", second.contentItemData, len(tg.sent))
	}
	// ✅ still works on a missed post
	if toast := cb("cnt_th_ok_"+second.ID, tg.sent[1].id); toast != "Отмечено: опубликован" {
		t.Fatalf("✅ on missed: %q", toast)
	}

	// 3rd: «Пропустить»
	now = slots[2]
	e.Tick(ctx)
	third = findContent(docs.doc(t), items[2].ID)
	if third.Status != "sent" {
		t.Fatalf("3rd: %+v", third.contentItemData)
	}
	if toast := cb("cnt_th_skip_"+third.ID, tg.sent[len(tg.sent)-1].id); toast != "Пропущено" || findContent(docs.doc(t), third.ID).Status != "skipped" {
		t.Fatalf("skip: %q", toast)
	}
	if b := kbButtons(tg.edits[len(tg.edits)-1].kb); b["⏭ Пропущен"] == "" {
		t.Fatalf("skip keyboard %v", b)
	}
	if toast := cb("cnt_th_done_"+third.ID, 1); toast != "Уже отмечено" {
		t.Fatalf("done: %q", toast)
	}

	// /status: 2 of 4
	if _, text, _ := e.ThreadsStatus(ctx); text != "ручной режим, сегодня опубликовано 2 из 4" {
		t.Fatalf("status %q", text)
	}
	// the morning preview is not sent in the manual mode (the posts come one by one)
	for _, x := range tg.sent {
		if strings.HasPrefix(x.text, "📅") {
			t.Fatalf("preview sent: %q", x.text)
		}
	}
}

func firstKey(m map[string]string) string {
	for k := range m {
		return k
	}
	return ""
}

func TestThreadsManualQuietHours(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 10, 5, 8, 50, 0, 0, almaty)
	e, docs, tg := thManualEngine(t, &now)
	add := func(id, at string) {
		_, err := e.update(ctx, func(d *contentDoc) bool {
			d.Queue = append(d.Queue, &contentItem{contentItemData: contentItemData{ID: id, Kind: "threads", Channel: "threads", Gen: "lib", Auto: true,
				Text: "Платёжный календарь собирается за вечер: выпиши все платежи месяца и остаток на каждый день.", At: at, Status: "planned"}})
			return true
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	add("th-20261005-0850", "2026-10-05T08:50:00+05:00")
	add("th-20261005-2110", "2026-10-05T21:10:00+05:00")
	e.manualDue(ctx)
	now = time.Date(2026, 10, 5, 21, 10, 0, 0, almaty)
	e.manualDue(ctx)
	d := docs.doc(t)
	for _, id := range []string{"th-20261005-0850", "th-20261005-2110"} {
		if it := findContent(d, id); it.Status != "skipped" || !strings.Contains(it.Error, "09:00-21:00") {
			t.Fatalf("%s: %+v", id, it.contentItemData)
		}
	}
	// 14:02 and 14:04 both due after a pause: only the later goes, no heap
	add("th-20261005-1402", "2026-10-05T14:02:00+05:00")
	add("th-20261005-1404", "2026-10-05T14:04:00+05:00")
	now = time.Date(2026, 10, 5, 14, 5, 0, 0, almaty)
	e.manualDue(ctx)
	e.manualDue(ctx)
	d = docs.doc(t)
	if len(tg.sent) != 1 || findContent(d, "th-20261005-1404").Status != "sent" || findContent(d, "th-20261005-1402").Status != "skipped" {
		t.Fatalf("heap: %d sent, %+v", len(tg.sent), findContent(d, "th-20261005-1402").contentItemData)
	}
	// a post 10 minutes after the last one waits for nothing: it is skipped
	add("th-20261005-1414", "2026-10-05T14:14:00+05:00")
	now = time.Date(2026, 10, 5, 14, 14, 0, 0, almaty)
	e.manualDue(ctx)
	if len(tg.sent) != 1 || findContent(docs.doc(t), "th-20261005-1414").Status != "skipped" {
		t.Fatal("two posts within 30 minutes")
	}
}

func TestThreadsManualLongLink(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 10, 5, 12, 0, 0, 0, almaty)
	e, docs, tg := thManualEngine(t, &now)
	long := strings.Repeat("Выручка растёт, а денег нет: проверь дебиторку и склад. ", 8)
	_, _ = e.update(ctx, func(d *contentDoc) bool {
		d.Queue = append(d.Queue,
			&contentItem{contentItemData: contentItemData{ID: "th-20261005-1201", Kind: "threads", Channel: "threads", Gen: "lib", Text: strings.TrimSpace(long), At: "2026-10-05T12:01:00+05:00", Status: "planned"}},
			&contentItem{contentItemData: contentItemData{ID: "th-20261005-1301", Kind: "threads", Channel: "threads", Gen: "lib", Text: "Короткий пост про кассу и остаток денег на неделю вперёд.", At: "2026-10-05T13:01:00+05:00", Status: "planned"}})
		return true
	})
	now = time.Date(2026, 10, 5, 12, 1, 0, 0, almaty)
	e.manualDue(ctx)
	if len(tg.sent) != 1 {
		t.Fatal("not sent")
	}
	it := findContent(docs.doc(t), "th-20261005-1201")
	if len(thIntentURL(it.Text)) <= thIntentMax {
		t.Fatalf("the test text must be long: %d", len(thIntentURL(it.Text)))
	}
	if u := kbButtons(tg.sent[0].kb)["Опубликовать в Threads"]; u != "https://app.test/go/th/th-20261005-1201" {
		t.Fatalf("long post: the button goes through the server: %q", u)
	}
	if tg.sent[0].text != it.Text || utf8.RuneCountInString(it.Text) > 500 {
		t.Fatalf("the message is the post itself, for long-press copy: %q", tg.sent[0].text)
	}
	// the server's link opens the intent
	gin.SetMode(gin.TestMode)
	r := gin.New()
	NewContentModule(e, []byte("x")).Register(r)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest("GET", "/go/th/th-20261005-1201", nil))
	loc, _ := url.Parse(w.Header().Get("Location"))
	if w.Code != http.StatusFound || loc.Host != "www.threads.net" || loc.Path != "/intent/post" || loc.Query().Get("text") != it.Text {
		t.Fatalf("redirect %d %q", w.Code, w.Header().Get("Location"))
	}
	w = httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest("GET", "/go/th/nope", nil))
	if w.Code != http.StatusNotFound {
		t.Fatalf("unknown post: %d", w.Code)
	}
	// Telegram refuses the intent link: the message goes with the server's link
	tg.refuse = func(kb map[string]any) error {
		if strings.Contains(kbButtons(kb)["Опубликовать в Threads"], "threads.net") {
			return errors.New("telegram sendMessage: Bad Request: BUTTON_URL_INVALID")
		}
		return nil
	}
	now = time.Date(2026, 10, 5, 13, 1, 0, 0, almaty)
	e.manualDue(ctx)
	if len(tg.sent) != 2 || !strings.HasSuffix(kbButtons(tg.sent[1].kb)["Опубликовать в Threads"], "/go/th/th-20261005-1301") || findContent(docs.doc(t), "th-20261005-1301").Status != "sent" {
		t.Fatalf("refused button: %+v", tg.sent)
	}
}

func TestThreadsManualAttribution(t *testing.T) {
	if l := startSource("th_p2610050934"); l != "Threads: пост 05.10 09:34" {
		t.Fatalf("label %q", l)
	}
	if l := startSource("th_99"); l != "Threads: 99 чек-листов" {
		t.Fatalf("th_99 %q", l)
	}
	it := &contentItem{contentItemData: contentItemData{ID: "th-20261005-0934", Channel: "threads", At: "2026-10-05T09:34:00+05:00",
		Text: "Кассовый разрыв видно за месяц, если вести платёжный календарь.\n\nЕщё 99 чек-листов в боте: t.me/bsurgery_bot?start=th_99"}}
	manualize(it)
	if !strings.HasSuffix(it.Text, "t.me/bsurgery_bot?start=th_p2610050934") || it.Link != "t.me/bsurgery_bot?start=th_p2610050934" {
		t.Fatalf("manualize: %q %q", it.Text, it.Link)
	}
	again := *it
	manualize(&again)
	if again.Text != it.Text {
		t.Fatal("manualize must be idempotent")
	}
	// a text without a link gets a short line; one that would pass 500 signs does not
	x := &contentItem{contentItemData: contentItemData{Channel: "threads", At: "2026-10-05T12:31:00+05:00", Text: "Короткий пост без ссылки про маржу."}}
	manualize(x)
	if !strings.Contains(x.Text, "\n\nЧек-листы для собственника, бесплатно: t.me/bsurgery_bot?start=th_p2610051231") {
		t.Fatalf("short line: %q", x.Text)
	}
	y := &contentItem{contentItemData: contentItemData{Channel: "threads", At: "2026-10-05T12:31:00+05:00", Text: strings.Repeat("а", 495)}}
	manualize(y)
	if y.Link != "" || utf8.RuneCountInString(y.Text) != 495 {
		t.Fatalf("too long for a link: %d %q", utf8.RuneCountInString(y.Text), y.Link)
	}
	// leads per post: each manual post counts its own
	it.Status, it.ByHand, it.PublishedAt = "published", true, "2026-10-05T04:40:00Z"
	other := &contentItem{contentItemData: contentItemData{ID: "th-20261005-1231", Channel: "threads", At: "2026-10-05T12:31:00+05:00", Status: "published",
		PublishedAt: "2026-10-05T07:35:00Z", Link: "t.me/bsurgery_bot?start=th_p2610051231"}}
	d := &contentDoc{contentDocData: contentDocData{Queue: []*contentItem{it, other}}}
	leads := []any{
		map[string]any{"source": "Threads: пост 05.10 09:34", "startAt": "2026-10-05T05:00:00Z", "col": "new"},
		map[string]any{"source": "Threads: пост 05.10 09:34", "startAt": "2026-10-05T06:00:00Z", "col": "meet"},
		map[string]any{"source": "Threads: пост 05.10 12:31", "startAt": "2026-10-05T08:00:00Z", "col": "new"},
	}
	countContent(d, leads)
	if it.Stats.Leads != 2 || it.Stats.Razbor != 1 || other.Stats.Leads != 1 {
		t.Fatalf("per post: %+v %+v", it.Stats, other.Stats)
	}
	var st struct {
		Total map[string]contentAgg `json:"total"`
	}
	_ = json.Unmarshal(d.Stats, &st)
	if st.Total["threads"].Posts != 2 || st.Total["threads"].Leads != 3 {
		t.Fatalf("totals %+v", st.Total)
	}
}

// The auto mode stays as it was when a token appears: 16 a day, the API publishes.
func TestThreadsManualOffWithToken(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 10, 5, 6, 30, 0, 0, almaty)
	docs := &cntDocs{}
	thDoc(t, docs, 0)
	e := cntEngine(docs, &now)
	e.Lib = thLib
	e.AI = (&thFakeAI{}).call
	tok := false
	e.ThreadsManual = func(context.Context) bool { return !tok }
	st := e.settings(ctx, docs.doc(t))
	if !st.manual || st.threadsPerDay() != 4 {
		t.Fatalf("no token: manual 4, got %v %d", st.manual, st.threadsPerDay())
	}
	tok = true
	now = now.Add(2 * time.Minute) // the cache lives a minute
	st = e.settings(ctx, docs.doc(t))
	if st.manual || st.threadsPerDay() != contentThreadsPerDay {
		t.Fatalf("with a token: auto %d, got %v %d", contentThreadsPerDay, st.manual, st.threadsPerDay())
	}
	// the owner's own manual cadence
	b, _ := json.Marshal(map[string]any{"channels": map[string]any{"threads": map[string]any{"on": true, "manualPerDay": 6}}})
	s := parseContentSettings(b)
	s.manual = true
	if s.threadsPerDay() != 6 {
		t.Fatalf("manualPerDay %d", s.threadsPerDay())
	}
}

func TestSysCheckThreadsLine(t *testing.T) {
	s := &SysCheck{Threads: func(context.Context) (string, string, string) {
		return "ok", "ручной режим, сегодня опубликовано 1 из 4", "manual"
	}}
	r := s.Run(context.Background())
	if !strings.Contains(r.Text(), "✅ Threads: ручной режим, сегодня опубликовано 1 из 4") {
		t.Fatalf("status:\n%s", r.Text())
	}
}
