package http

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/bnursik/business_surgery_backend/internal/bot"
	"github.com/bnursik/business_surgery_backend/internal/club"
	pg "github.com/bnursik/business_surgery_backend/internal/repository/pg"
	"github.com/bnursik/business_surgery_backend/migrations"
	"github.com/gin-gonic/gin"
)

// R40b: a fake Telegram that records every call (JSON and multipart) and
// can refuse a method, as the real one does on a bad markup or a block.
type tgRec struct {
	mu    sync.Mutex
	calls []tgCall
	fail  map[string]string // method → description
	next  int64
}

type tgCall struct {
	Method string
	P      map[string]any
}

func (t *tgRec) handler(w http.ResponseWriter, r *http.Request) {
	if !strings.HasPrefix(r.URL.Path, "/bot"+testBotToken+"/") {
		w.WriteHeader(404)
		return
	}
	method := r.URL.Path[strings.LastIndex(r.URL.Path, "/")+1:]
	p := map[string]any{}
	if strings.HasPrefix(r.Header.Get("Content-Type"), "multipart/") {
		_ = r.ParseMultipartForm(8 << 20)
		for k, v := range r.MultipartForm.Value {
			if len(v) > 0 {
				var x any
				if json.Unmarshal([]byte(v[0]), &x) == nil {
					if m, ok := x.(map[string]any); ok {
						p[k] = m
						continue
					}
				}
				p[k] = v[0]
			}
		}
	} else {
		b, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(b, &p)
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if d, ok := t.fail[method]; ok {
		_, _ = w.Write([]byte(fmt.Sprintf(`{"ok":false,"description":%q}`, d)))
		return
	}
	t.calls = append(t.calls, tgCall{method, p})
	t.next++
	switch method {
	case "getWebhookInfo":
		_, _ = w.Write([]byte(`{"ok":true,"result":{"url":""}}`))
	case "sendPhoto":
		_, _ = w.Write([]byte(fmt.Sprintf(`{"ok":true,"result":{"message_id":%d,"photo":[{"file_id":"ph1"}]}}`, t.next)))
	default:
		_, _ = w.Write([]byte(fmt.Sprintf(`{"ok":true,"result":{"message_id":%d}}`, t.next)))
	}
}

func chatOf(p map[string]any) int64 {
	switch v := p["chat_id"].(type) {
	case float64:
		return int64(v)
	case string:
		var n int64
		fmt.Sscan(v, &n)
		return n
	}
	return 0
}

// to: the messages (text or caption) the bot sent or edited for chat.
func (t *tgRec) to(chat int64) []tgCall {
	t.mu.Lock()
	defer t.mu.Unlock()
	var out []tgCall
	for _, c := range t.calls {
		if (c.Method == "sendMessage" || c.Method == "sendPhoto" || c.Method == "editMessageText") && chatOf(c.P) == chat {
			out = append(out, c)
		}
	}
	return out
}

func (c tgCall) text() string {
	if s, ok := c.P["text"].(string); ok {
		return s
	}
	s, _ := c.P["caption"].(string)
	return s
}

func (c tgCall) kb() string { b, _ := json.Marshal(c.P["reply_markup"]); return string(b) }

type r40Env struct {
	t      *testing.T
	db     *pg.DB
	svc    *bot.Service
	f      *LeadFunnel
	tg     *tgRec
	router *gin.Engine
	repo   *pg.PlatformRepo
}

func newR40Env(t *testing.T) *r40Env {
	dsn := os.Getenv("BS_TEST_DSN")
	if dsn == "" {
		t.Skip("BS_TEST_DSN not set")
	}
	restore := club.SetSheetMode(club.SheetModeOff) // production: the server is the bot
	ctx := context.Background()
	db, err := pg.NewDB(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	if err := pg.Migrate(ctx, db, migrations.FS); err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{`TRUNCATE bot_updates, bot_meta`,
		`DELETE FROM platform_docs WHERE key IN ('bs_crm','bs_ck_stats','bs_bot_inbox','bs_slots')`,
		`DELETE FROM club_residents WHERE tg_id IN (7001,7002,7003,7004)`} {
		if _, err := db.Pool.Exec(ctx, q); err != nil {
			t.Fatal(err)
		}
	}
	e := &r40Env{t: t, db: db, tg: &tgRec{fail: map[string]string{}}}
	srv := httptest.NewServer(http.HandlerFunc(e.tg.handler))
	e.svc = bot.New(pg.NewBotRepo(db), bot.Options{Token: testBotToken, APIBase: srv.URL, Admins: []int64{111}, Workers: 2, Timeout: 5 * time.Second})
	e.repo = pg.NewPlatformRepo(db)
	f := NewLeadFunnel(e.repo, e.svc.SendMessageKB, []int64{111})
	f.Photo, f.Edit, f.Meta = e.svc.SendPhotoKB, e.svc.EditMessageKB, pg.NewBotRepo(db)
	e.f = f
	// as app.go wires it
	e.svc.SetStartHook(f.StartHook(nil, map[int64]string{111: "Рустам"}))
	e.svc.SetCallbackHook(f.HandleCallback)
	e.svc.SetLeadTextHook(f.HandleLeadText)
	e.svc.SetInboundHook(f.NoteInbound)
	gin.SetMode(gin.TestMode)
	e.router = gin.New()
	NewBotModule(NewBotHandler(e.svc, "")).Register(e.router)
	c, cancel := context.WithCancel(ctx)
	e.svc.Start(c)
	t.Cleanup(func() { cancel(); srv.Close(); db.Pool.Close(); restore() })
	return e
}

func (e *r40Env) post(body string) {
	req := httptest.NewRequest("POST", "/api/v1/bot/webhook", strings.NewReader(body))
	req.Header.Set("X-Telegram-Bot-Api-Secret-Token", e.svc.WebhookSecret())
	w := httptest.NewRecorder()
	e.router.ServeHTTP(w, req)
	if w.Code != 200 {
		e.t.Fatalf("webhook: %d %s", w.Code, w.Body.String())
	}
}

var r40upd int64 = 91000

func (e *r40Env) msg(chat int64, text string, at time.Time) {
	r40upd++
	e.post(fmt.Sprintf(`{"update_id":%d,"message":{"message_id":%d,"date":%d,"chat":{"id":%d,"type":"private"},"from":{"id":%d,"first_name":"Айдар","username":"aidar"},"text":%q}}`,
		r40upd, r40upd, at.Unix(), chat, chat, text))
}

func (e *r40Env) press(chat, msgID int64, data string) {
	r40upd++
	e.post(fmt.Sprintf(`{"update_id":%d,"callback_query":{"id":"cb%d","data":%q,"from":{"id":%d,"first_name":"Айдар","username":"aidar"},"message":{"message_id":%d,"chat":{"id":%d,"type":"private"}}}}`,
		r40upd, r40upd, data, chat, msgID, chat))
}

func (e *r40Env) wait(chat int64, n int) []tgCall {
	e.t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if got := e.tg.to(chat); len(got) >= n {
			time.Sleep(150 * time.Millisecond) // the CRM writes after the send
			return e.tg.to(chat)
		}
		time.Sleep(20 * time.Millisecond)
	}
	got := e.tg.to(chat)
	var ts []string
	for _, c := range got {
		ts = append(ts, c.Method+": "+c.text())
	}
	e.t.Fatalf("chat %d: %d of %d messages: %v", chat, len(got), n, ts)
	return nil
}

func (e *r40Env) lead(tg int64) map[string]any {
	d, _ := e.repo.GetDoc(context.Background(), "club", "bs_crm")
	if d == nil {
		return nil
	}
	var crm map[string]any
	_ = json.Unmarshal([]byte(d.Value), &crm)
	return findLeadByTg(asList(crm["leads"]), tg)
}

func logHas(l map[string]any, s string) bool {
	b, _ := json.Marshal(l["log"])
	return strings.Contains(string(b), s)
}

// The whole path of a Threads lead: t.me/bsurgery_bot?start=th_99 → webhook →
// the server bot → the CRM card with the source → the welcome and the pain
// question → «Бот ответил» in the card → /status line.
func TestR40bThreadsStartAnswered(t *testing.T) {
	e := newR40Env(t)
	ctx := context.Background()
	// Telegram refuses the photo (as with a broken caption): the text fallback must still answer.
	e.tg.fail["sendPhoto"] = "Bad Request: can't parse entities"
	e.msg(7001, "/start th_99", time.Now())
	got := e.wait(7001, 2)
	if !strings.Contains(got[0].text(), "99 гайдов") || !strings.Contains(got[0].text(), "6 вопросов") || strings.Contains(got[0].text(), "<b>") {
		t.Fatalf("welcome: %q", got[0].text())
	}
	if !strings.Contains(got[1].text(), "болит сильнее") || !strings.Contains(got[1].kb(), "lm_pain_fin") {
		t.Fatalf("pain ask: %q %s", got[1].text(), got[1].kb())
	}
	l := e.lead(7001)
	if l == nil || l["source"] != "Threads: 99 чек-листов" || l["botReplyAt"] == nil || l["handledAt"] == nil || !logHas(l, "Бот ответил") {
		t.Fatalf("crm card: %v", l)
	}
	for _, c := range e.tg.to(111) {
		if strings.Contains(c.text(), "Новый лид") && strings.Contains(c.text(), "Threads") {
			goto adminOK
		}
	}
	t.Fatalf("team note missing: %v", e.tg.to(111))
adminOK:
	// the inbox has the message; the safety net sees it answered and stays quiet
	e.f.now = func() time.Time { return time.Now().Add(3 * time.Minute) }
	if n := e.f.SafetyOnce(ctx); n != 0 {
		t.Fatalf("safety net fired for an answered lead: %d", n)
	}
	e.f.now = time.Now
	h := ReadAutoHealth(ctx, e.f.Meta)
	if h.Last == "" || h.Replies < 1 {
		t.Fatalf("health: %+v", h)
	}
	st, txt := AutoReplyLine(h, time.Now())
	if st != "ok" || !strings.HasPrefix(txt, "последний ") || !strings.Contains(txt, "сбоев сегодня 0") {
		t.Fatalf("status line: %s %s", st, txt)
	}
	sc := &SysCheck{Meta: pg.NewBotRepo(e.db)}
	if it := sc.autoreply(ctx); it.Title != "Автоответ" || !strings.Contains(it.Text, "сбоев сегодня 0") {
		t.Fatalf("syscheck item: %+v", it)
	}
	// the same /start delivered again (a restart): not answered twice
	e.f.HandleStart(bot.WithRetry(ctx), bot.StartUpdate{ChatID: 7001, Param: "th_99", FirstName: "Айдар", Date: time.Now().Add(-5 * time.Second).Unix()})
	if n := len(e.tg.to(7001)); n != 2 {
		t.Fatalf("answered twice: %d", n)
	}
	delete(e.tg.fail, "sendPhoto")
}

// A lead's free text was dropped by the server bot; now it is answered,
// kept in the card and shown to the team, at night too.
func TestR40bLeadTextAnswered(t *testing.T) {
	e := newR40Env(t)
	e.msg(7002, "/start th_99", time.Now())
	e.wait(7002, 2)
	night := time.Date(2026, 10, 5, 23, 40, 0, 0, almaty)
	e.f.now = func() time.Time { return night }
	e.msg(7002, "Сколько стоит разбор?", time.Now())
	got := e.wait(7002, 3)
	if !strings.Contains(got[2].text(), "передал команде") || !strings.Contains(got[2].text(), "завтра после 10:00") || !strings.Contains(got[2].kb(), "lm_q_menu") {
		t.Fatalf("ack: %q %s", got[2].text(), got[2].kb())
	}
	l := e.lead(7002)
	if l["tgIn"] != "Сколько стоит разбор?" || !logHas(l, "Написал в бот") || !logHas(l, "ответ на сообщение") {
		t.Fatalf("card: %v", l)
	}
	found := false
	for _, c := range e.tg.to(111) {
		if strings.Contains(c.text(), "написал в бот") && strings.Contains(c.text(), "Сколько стоит") {
			found = true
		}
	}
	if !found {
		t.Fatal("the team did not see the message")
	}
	// a second message within 30 minutes: in the card, no second ack
	e.msg(7002, "Алло", time.Now())
	time.Sleep(1500 * time.Millisecond)
	if n := len(e.tg.to(7002)); n != 3 {
		t.Fatalf("second ack: %d", n)
	}
	if l := e.lead(7002); l["tgIn"] != "Алло" {
		t.Fatalf("second message not kept: %v", l["tgIn"])
	}
}

// The safety net: a message on record without an answer for 2 minutes gets
// the standard welcome once, the card says so, /status counts it.
func TestR40bSafetyNet(t *testing.T) {
	e := newR40Env(t)
	ctx := context.Background()
	at := time.Now().Add(-3 * time.Minute)
	// the message reached the server, the answer never went (a crash in between)
	e.f.NoteInbound(ctx, bot.Inbound{ChatID: 7003, Date: at.Unix(), FirstName: "Мадина", Start: true, Param: "th_99", Text: "/start th_99"})
	if n := e.f.SafetyOnce(ctx); n != 1 {
		t.Fatalf("safety net sent %d", n)
	}
	got := e.tg.to(7003)
	if len(got) < 1 || !strings.Contains(got[0].text(), "Мадина") {
		t.Fatalf("welcome: %v", got)
	}
	l := e.lead(7003)
	if l == nil || l["autoFix"] != true || l["source"] != "Threads: 99 чек-листов" || !logHas(l, "страховка") {
		t.Fatalf("card: %v", l)
	}
	if n := e.f.SafetyOnce(ctx); n != 0 {
		t.Fatalf("twice: %d", n)
	}
	e.f.NoteInbound(ctx, bot.Inbound{ChatID: 7003, Date: time.Now().Add(-3 * time.Minute).Unix(), Text: "ау"})
	if n := e.f.SafetyOnce(ctx); n != 0 {
		t.Fatalf("the welcome went once already: %d", n)
	}
	st, txt := AutoReplyLine(ReadAutoHealth(ctx, e.f.Meta), time.Now())
	if st != "warn" || !strings.Contains(txt, "страховка сработала 1") {
		t.Fatalf("status: %s %s", st, txt)
	}
	// a blocked bot: a failure with its reason
	e.tg.fail["sendPhoto"] = "Forbidden: bot was blocked by the user"
	e.tg.fail["sendMessage"] = "Forbidden: bot was blocked by the user"
	e.f.NoteInbound(ctx, bot.Inbound{ChatID: 7004, Date: time.Now().Add(-3 * time.Minute).Unix(), Start: true})
	e.f.SafetyOnce(ctx)
	if _, txt := AutoReplyLine(ReadAutoHealth(ctx, e.f.Meta), time.Now()); !strings.Contains(txt, "сбоев сегодня 1 (лид заблокировал бота)") {
		t.Fatalf("status: %s", txt)
	}
}

// The checklist right in the chat: pain → revenue → 6 yes/no → score, ₸,
// one action → one tap books the nearest slot; the CRM tracks it all.
func TestR40bChatChecklist(t *testing.T) {
	e := newR40Env(t)
	ctx := context.Background()
	start := time.Now().Add(48 * time.Hour).In(almaty)
	slots := fmt.Sprintf(`{"price":50000,"slots":[{"id":"s9","start":%q,"dur":60,"format":"онлайн","status":"free"}]}`, start.Format("2006-01-02T15:04:05-07:00"))
	if _, err := e.repo.PutDoc(ctx, "club", "bs_slots", 0, slots, false, "test"); err != nil {
		t.Fatal(err)
	}
	e.msg(7001, "/start th_99", time.Now())
	e.wait(7001, 2)
	e.press(7001, 2, "lm_pain_fin")
	got := e.wait(7001, 3)
	if got[2].Method != "editMessageText" || !strings.Contains(got[2].text(), "выручка") || !strings.Contains(got[2].kb(), "lm_q_fin_1") {
		t.Fatalf("revenue step: %s %q %s", got[2].Method, got[2].text(), got[2].kb())
	}
	if l := e.lead(7001); l["pain"] != "Финансы" || !logHas(l, "Начал проверку") {
		t.Fatalf("start not tracked: %v", l)
	}
	data := "lm_q_fin_1"
	e.press(7001, 2, data)
	for i, a := range "nyynyn" {
		got = e.wait(7001, 4+i)
		if !strings.Contains(got[len(got)-1].text(), fmt.Sprintf("вопрос %d из 6", i+1)) {
			t.Fatalf("question %d: %q", i+1, got[len(got)-1].text())
		}
		data += string(a)
		e.press(7001, 2, data)
	}
	got = e.wait(7001, 10)
	res := got[len(got)-1]
	// fin: n(bad 3) y y n(bad 3: «занимали» is BadYes, so n is fine) y n(bad 2)
	if !strings.Contains(res.text(), "из 6 в порядке") || !strings.Contains(res.text(), "₸ в месяц") || !strings.Contains(res.text(), "Один шаг") ||
		!strings.Contains(res.kb(), "lm_bk_s9") || strings.Contains(res.text(), "—") {
		t.Fatalf("result: %q %s", res.text(), res.kb())
	}
	l := e.lead(7001)
	qz, _ := l["qz"].(map[string]any)
	fin, _ := qz["fin"].(map[string]any)
	if fin == nil || fin["fin"] == nil || fin["score"] == nil || anyInt(fin["loss"]) <= 0 || !logHas(l, "Прошёл проверку") {
		t.Fatalf("result not tracked: %v", qz)
	}
	e.press(7001, 2, "lm_bk_s9")
	got = e.wait(7001, 11)
	if !strings.Contains(got[len(got)-1].text(), "Вы записаны на разбор") {
		t.Fatalf("booking: %q", got[len(got)-1].text())
	}
	if l := e.lead(7001); l["col"] != "meet" || l["razborSlot"] != "s9" || l["qzBooked"] == nil {
		t.Fatalf("booking in crm: col=%v slot=%v", l["col"], l["razborSlot"])
	}
}

func TestR40bQuizScoreAndNudge(t *testing.T) {
	q := quizByKey("sales")
	r := quizScore(q, 1, "nnnnnn") // every answer bad: 16% of 6 000 000
	if r.Score != 0 || r.Loss != 1_000_000 || !strings.Contains(r.Action, "15 минут") {
		t.Fatalf("score: %+v", r)
	}
	if r := quizScore(q, 0, "yyyyyy"); r.Score != 6 || r.Loss != 0 {
		t.Fatalf("all good: %+v", r)
	}
	if _, _, _, ok := quizState("lm_q_fin_9yy"); ok {
		t.Fatal("bad revenue accepted")
	}
	f := NewLeadFunnel(nil, nil, nil)
	text, keys, ok := f.quizNudge(context.Background(), 0, "Айдар", nil, "Продажи")
	kbs, _ := json.Marshal(keys)
	if !ok || !strings.Contains(text, "Айдар") || !strings.Contains(string(kbs), "lm_q_sales") {
		t.Fatalf("day 1: %q %s", text, kbs)
	}
	done := map[string]any{"sales": map[string]any{"st": "x", "fin": "y", "score": float64(2)}}
	text, keys, _ = f.quizNudge(context.Background(), 1, "", done, "Продажи")
	kbs, _ = json.Marshal(keys)
	if strings.Contains(string(kbs), "lm_q_sales") || !strings.Contains(text, "2 из 6") {
		t.Fatalf("day 3 offers another one: %q %s", text, kbs)
	}
	if _, _, ok := f.quizNudge(context.Background(), 2, "", nil, ""); ok {
		t.Fatal("day 7 keeps its own text")
	}
	for _, q := range quizzes {
		for _, x := range q.Qs {
			if strings.Contains(x.Text+x.Action, "—") {
				t.Fatalf("em dash in %s", q.Key)
			}
		}
	}
}
