package http

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/bnursik/business_surgery_backend/internal/bot"
	pg "github.com/bnursik/business_surgery_backend/internal/repository/pg"
	"github.com/bnursik/business_surgery_backend/migrations"
	"github.com/gin-gonic/gin"
)

// These tests need a throwaway Postgres: BS_TEST_DSN=postgres://…/bs_bot_test

type fakeScript struct {
	mu       sync.Mutex
	got      []map[string]any // bodies in arrival order
	failFor  map[int64]int    // update_id -> how many times to answer 500
	delay    time.Duration
	version  string
	inflight map[string]bool
	overlap  []string // order keys that arrived while an earlier one of theirs was in flight
}

func (f *fakeScript) handler(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodGet {
		_ = json.NewEncoder(w).Encode(map[string]string{"version": f.version})
		return
	}
	b, _ := io.ReadAll(r.Body)
	var u map[string]any
	_ = json.Unmarshal(b, &u)
	id := int64(u["update_id"].(float64))
	p, _ := bot.Parse(b)
	f.mu.Lock()
	if f.failFor[id] > 0 {
		f.failFor[id]--
		f.mu.Unlock()
		w.WriteHeader(500)
		return
	}
	if f.inflight[p.OrderKey] {
		f.overlap = append(f.overlap, p.OrderKey)
	}
	f.inflight[p.OrderKey] = true
	f.mu.Unlock()
	time.Sleep(f.delay)
	f.mu.Lock()
	f.inflight[p.OrderKey] = false
	f.got = append(f.got, u)
	f.mu.Unlock()
	// Apps Script with ContentService answers a redirect once doPost has run.
	w.Header().Set("Location", "https://script.googleusercontent.com/echo")
	w.WriteHeader(302)
}

type fakeTelegram struct {
	mu      sync.Mutex
	hook    map[string]any
	sent    []map[string]any
	pending int
}

func (t *fakeTelegram) handler(w http.ResponseWriter, r *http.Request) {
	if !strings.HasPrefix(r.URL.Path, "/bot"+testBotToken+"/") {
		w.WriteHeader(404)
		_, _ = w.Write([]byte(`{"ok":false,"description":"Not Found"}`))
		return
	}
	method := r.URL.Path[strings.LastIndex(r.URL.Path, "/")+1:]
	var p map[string]any
	_ = json.NewDecoder(r.Body).Decode(&p)
	t.mu.Lock()
	defer t.mu.Unlock()
	switch method {
	case "setWebhook":
		t.hook = p
		_, _ = w.Write([]byte(`{"ok":true,"result":true}`))
	case "getWebhookInfo":
		url, _ := t.hook["url"].(string)
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "result": map[string]any{"url": url, "pending_update_count": t.pending}})
	case "sendMessage":
		t.sent = append(t.sent, p)
		_, _ = w.Write([]byte(`{"ok":true,"result":{}}`))
	case "createChatInviteLink": // R70: the personal club group link
		_, _ = w.Write([]byte(`{"ok":true,"result":{"invite_link":"https://t.me/+personal1"}}`))
	default:
		_, _ = w.Write([]byte(`{"ok":false,"description":"unknown"}`))
	}
}

type botEnv struct {
	t      *testing.T
	db     *pg.DB
	svc    *bot.Service
	router *gin.Engine
	script *fakeScript
	scrSrv *httptest.Server
	tg     *fakeTelegram
	cancel context.CancelFunc
}

func newBotEnv(t *testing.T) *botEnv {
	dsn := os.Getenv("BS_TEST_DSN")
	if dsn == "" {
		t.Skip("BS_TEST_DSN not set")
	}
	ctx := context.Background()
	db, err := pg.NewDB(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	if err := pg.Migrate(ctx, db, migrations.FS); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Pool.Exec(ctx, `TRUNCATE bot_updates, bot_meta`); err != nil {
		t.Fatal(err)
	}
	e := &botEnv{t: t, db: db, script: &fakeScript{failFor: map[int64]int{}, version: "2026-09-28-4", inflight: map[string]bool{}}, tg: &fakeTelegram{}}
	e.scrSrv = httptest.NewServer(http.HandlerFunc(e.script.handler))
	tgSrv := httptest.NewServer(http.HandlerFunc(e.tg.handler))
	e.svc = bot.New(pg.NewBotRepo(db), bot.Options{Token: testBotToken, APIBase: tgSrv.URL, Admins: []int64{111, 222}, Workers: 4, Timeout: 5 * time.Second})
	gin.SetMode(gin.TestMode)
	e.router = gin.New()
	NewBotModule(NewBotHandler(e.svc, "")).Register(e.router)
	c, cancel := context.WithCancel(ctx)
	e.cancel = cancel
	e.svc.Start(c)
	t.Cleanup(func() { cancel(); e.scrSrv.Close(); tgSrv.Close(); db.Pool.Close() })
	return e
}

func sign(body []byte) string {
	m := hmac.New(sha256.New, []byte(testBotToken))
	m.Write(body)
	return hex.EncodeToString(m.Sum(nil))
}

func (e *botEnv) do(method, path string, body []byte, hdr map[string]string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, bytes.NewReader(body))
	req.Host = "app.example.kz"
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	w := httptest.NewRecorder()
	e.router.ServeHTTP(w, req)
	return w
}

func (e *botEnv) signedPost(path string, v map[string]any) *httptest.ResponseRecorder {
	if _, ok := v["ts"]; !ok {
		v["ts"] = time.Now().Unix()
	}
	b, _ := json.Marshal(v)
	return e.do("POST", path, b, map[string]string{"X-BS-Signature": sign(b), "Content-Type": "application/json"})
}

// The script is only reachable at a script.google.com address in production;
// the test server sits on 127.0.0.1, so the relay is set directly.
func (e *botEnv) useTestRelay() {
	if err := e.svc.SetRelayURL(context.Background(), e.scrSrv.URL); err != nil {
		e.t.Fatal(err)
	}
}

func (e *botEnv) hook(body string) *httptest.ResponseRecorder {
	return e.do("POST", "/api/v1/bot/webhook", []byte(body), map[string]string{"X-Telegram-Bot-Api-Secret-Token": e.svc.WebhookSecret()})
}

func (e *botEnv) waitRelayed(n int, within time.Duration) {
	deadline := time.Now().Add(within)
	for time.Now().Before(deadline) {
		e.script.mu.Lock()
		got := len(e.script.got)
		e.script.mu.Unlock()
		if got >= n {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	e.script.mu.Lock()
	defer e.script.mu.Unlock()
	e.t.Fatalf("relayed %d of %d in %s", len(e.script.got), n, within)
}

func msg(id int64, chat int64, chatType string, from int64, text string) string {
	return fmt.Sprintf(`{"update_id":%d,"message":{"message_id":%d,"date":%d,"chat":{"id":%d,"type":"%s"},"from":{"id":%d,"first_name":"Тест"},"text":%q}}`,
		id, id, time.Now().Unix(), chat, chatType, from, text)
}

func TestBotWebhookRejectsWithoutSecret(t *testing.T) {
	e := newBotEnv(t)
	w := e.do("POST", "/api/v1/bot/webhook", []byte(msg(1, 5, "private", 5, "hi")), nil)
	if w.Code != 401 {
		t.Fatalf("no secret: %d", w.Code)
	}
	w = e.do("POST", "/api/v1/bot/webhook", []byte(msg(1, 5, "private", 5, "hi")), map[string]string{"X-Telegram-Bot-Api-Secret-Token": "bs2026secret"})
	if w.Code != 401 {
		t.Fatalf("old script secret accepted: %d", w.Code)
	}
	var n int
	_ = e.db.Pool.QueryRow(context.Background(), `SELECT count(*) FROM bot_updates`).Scan(&n)
	if n != 0 {
		t.Fatalf("stored %d updates from a stranger", n)
	}
}

func TestBotRelaysEverythingOnceInOrderAndAnswersAtOnce(t *testing.T) {
	e := newBotEnv(t)
	e.useTestRelay()
	e.script.delay = 150 * time.Millisecond // the script is slow; Telegram must not wait on it

	const group = -1002494126345
	var id int64 = 1000
	var slowest time.Duration
	for round := 0; round < 6; round++ {
		for user := int64(1); user <= 4; user++ {
			id++
			start := time.Now()
			w := e.hook(msg(id, user, "private", user, fmt.Sprintf("u%d-%d", user, round)))
			if d := time.Since(start); d > slowest {
				slowest = d
			}
			if w.Code != 200 {
				t.Fatalf("webhook %d", w.Code)
			}
			id++
			w = e.hook(msg(id, group, "supergroup", user, fmt.Sprintf("отчёт u%d-%d", user, round)))
			if w.Code != 200 {
				t.Fatalf("webhook %d", w.Code)
			}
		}
	}
	// Telegram sends the same update again: must not reach the script twice.
	if w := e.hook(msg(1001, 1, "private", 1, "u1-0")); w.Code != 200 {
		t.Fatalf("repeat: %d", w.Code)
	}
	if slowest > 100*time.Millisecond {
		t.Fatalf("webhook waited %s on something", slowest)
	}
	e.waitRelayed(48, 20*time.Second)
	time.Sleep(300 * time.Millisecond)

	e.script.mu.Lock()
	defer e.script.mu.Unlock()
	if len(e.script.got) != 48 {
		t.Fatalf("script got %d updates, want 48", len(e.script.got))
	}
	seen := map[int64]bool{}
	last := map[string]int64{}
	for _, u := range e.script.got {
		uid := int64(u["update_id"].(float64))
		if seen[uid] {
			t.Fatalf("update %d relayed twice", uid)
		}
		seen[uid] = true
		b, _ := json.Marshal(u)
		p, _ := bot.Parse(b)
		if uid < last[p.OrderKey] {
			t.Fatalf("order broken in %s: %d after %d", p.OrderKey, uid, last[p.OrderKey])
		}
		last[p.OrderKey] = uid
	}
	if len(e.script.overlap) > 0 {
		t.Fatalf("two updates of one conversation at the script at once: %v", e.script.overlap)
	}
	if len(last) != 8 {
		t.Fatalf("conversations: %d, want 8 (4 private + 4 members of the group)", len(last))
	}
	t.Logf("48 updates, slowest webhook answer %s", slowest)
}

func TestBotRetriesAndKeepsOrderBehindAFailure(t *testing.T) {
	e := newBotEnv(t)
	e.useTestRelay()
	e.script.failFor[2001] = 2 // the script refuses the first update twice
	e.hook(msg(2001, 7, "private", 7, "/start"))
	e.hook(msg(2002, 7, "private", 7, "второе"))
	e.hook(msg(2003, 8, "private", 8, "другой человек"))

	// Someone else is not held up by the failure.
	e.waitRelayed(1, 3*time.Second)
	e.script.mu.Lock()
	first := int64(e.script.got[0]["update_id"].(float64))
	e.script.mu.Unlock()
	if first != 2003 {
		t.Fatalf("first delivered %d, want 2003", first)
	}
	// 5s + 15s backoff would be slow for a test: pull the retries forward.
	for i := 0; i < 2; i++ {
		time.Sleep(200 * time.Millisecond)
		_, _ = e.db.Pool.Exec(context.Background(), `UPDATE bot_updates SET next_try_at = now() WHERE relayed_at IS NULL`)
		e.svc.Wake()
	}
	e.waitRelayed(3, 10*time.Second)
	e.script.mu.Lock()
	order := []int64{}
	for _, u := range e.script.got {
		order = append(order, int64(u["update_id"].(float64)))
	}
	e.script.mu.Unlock()
	if fmt.Sprint(order) != "[2003 2001 2002]" {
		t.Fatalf("order %v, want [2003 2001 2002]", order)
	}
	var tries int
	var status int
	_ = e.db.Pool.QueryRow(context.Background(), `SELECT relay_tries, relay_status FROM bot_updates WHERE update_id = 2001`).Scan(&tries, &status)
	if tries != 3 || status != 302 {
		t.Fatalf("tries %d status %d, want 3 and 302", tries, status)
	}
}

func TestBotIgnoresNonUpdates(t *testing.T) {
	e := newBotEnv(t)
	for _, b := range []string{`{}`, `not json`, `{"update_id":0}`} {
		if w := e.hook(b); w.Code != 200 {
			t.Fatalf("%q: %d", b, w.Code)
		}
	}
	var n int
	_ = e.db.Pool.QueryRow(context.Background(), `SELECT count(*) FROM bot_updates`).Scan(&n)
	if n != 0 {
		t.Fatalf("stored %d", n)
	}
}

func TestBotConnect(t *testing.T) {
	e := newBotEnv(t)
	good := "https://script.google.com/macros/s/AKfyc-TEST_1/exec"

	// Not signed, stale, badly signed, wrong address.
	b, _ := json.Marshal(map[string]any{"ts": time.Now().Unix(), "relay": good})
	if w := e.do("POST", "/api/v1/bot/connect", b, nil); w.Code != 401 {
		t.Fatalf("unsigned: %d", w.Code)
	}
	if w := e.do("POST", "/api/v1/bot/connect", b, map[string]string{"X-BS-Signature": sign([]byte("other"))}); w.Code != 401 {
		t.Fatalf("bad signature: %d", w.Code)
	}
	if w := e.signedPost("/api/v1/bot/connect", map[string]any{"ts": time.Now().Add(-2 * time.Hour).Unix(), "relay": good}); w.Code != 401 {
		t.Fatalf("stale: %d", w.Code)
	}
	if w := e.signedPost("/api/v1/bot/connect", map[string]any{"relay": "https://evil.example/exec"}); w.Code != 400 {
		t.Fatalf("foreign relay: %d", w.Code)
	}
	// The script cannot be reached from the test (it is not really on
	// script.google.com): connect must refuse rather than point Telegram at
	// a relay that does not answer.
	if w := e.signedPost("/api/v1/bot/connect", map[string]any{"relay": good, "version": "2026-09-28-4"}); w.Code != 409 {
		t.Fatalf("unreachable relay: %d %s", w.Code, w.Body.String())
	}
	if e.tg.hook != nil {
		t.Fatal("webhook was set although the relay did not answer")
	}
	if e.svc.RelayURL() != "" {
		t.Fatal("relay saved although it did not answer")
	}
}

func TestBotConnectSetsWebhook(t *testing.T) {
	e := newBotEnv(t)
	// Wrong version: refused.
	e.script.version = "2026-09-01-1"
	w := e.signedPostRelay(e.scrSrv.URL, "2026-09-28-4")
	if w.Code != 409 {
		t.Fatalf("old script version accepted: %d %s", w.Code, w.Body.String())
	}
	e.script.version = "2026-09-28-4"
	w = e.signedPostRelay(e.scrSrv.URL, "2026-09-28-4")
	if w.Code != 200 {
		t.Fatalf("connect: %d %s", w.Code, w.Body.String())
	}
	if e.tg.hook["url"] != "https://app.example.kz/api/v1/bot/webhook" {
		t.Fatalf("webhook url %v", e.tg.hook["url"])
	}
	if e.tg.hook["secret_token"] != e.svc.WebhookSecret() {
		t.Fatal("secret not set")
	}
	if e.tg.hook["drop_pending_updates"] != false {
		t.Fatal("pending updates would be dropped")
	}
	au, _ := json.Marshal(e.tg.hook["allowed_updates"])
	if string(au) != `["message","edited_message","callback_query","message_reaction"]` {
		t.Fatalf("allowed_updates %s", au)
	}
	if e.svc.RelayURL() != e.scrSrv.URL {
		t.Fatal("relay not saved")
	}
	// Survives a restart.
	v, _ := pg.NewBotRepo(e.db).GetMeta(context.Background(), "relay_url")
	if v != e.scrSrv.URL {
		t.Fatalf("relay in db %q", v)
	}

	// The sheet's own version check through the webhook address.
	w = e.do("GET", "/api/v1/bot/webhook?action=bsVersion", nil, nil)
	var ver map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &ver)
	if ver["version"] != "2026-09-28-4" {
		t.Fatalf("version through server: %s", w.Body.String())
	}

	// Status.
	e.hook(msg(3001, 9, "private", 9, "hi"))
	e.waitRelayed(1, 3*time.Second)
	var st struct {
		ViaServer bool           `json:"viaServer"`
		Version   string         `json:"version"`
		Queue     map[string]any `json:"queue"`
	}
	// the script got the update; the server marks it relayed right after its answer
	for end := time.Now().Add(3 * time.Second); ; time.Sleep(20 * time.Millisecond) {
		w = e.signedPost("/api/v1/bot/status", map[string]any{})
		st.Queue = nil
		_ = json.Unmarshal(w.Body.Bytes(), &st)
		if r, _ := st.Queue["relayed24h"].(float64); r >= 1 || time.Now().After(end) {
			break
		}
	}
	if w.Code != 200 || !st.ViaServer || st.Version != "2026-09-28-4" || st.Queue["received24h"].(float64) != 1 || st.Queue["relayed24h"].(float64) != 1 {
		t.Fatalf("status: %d %s", w.Code, w.Body.String())
	}
	if w := e.do("POST", "/api/v1/bot/status", []byte(`{"ts":1}`), nil); w.Code != 401 {
		t.Fatalf("status without signature: %d", w.Code)
	}
}

// signedPostRelay connects with a relay on the test server by widening the
// address check for the duration of the call.
func (e *botEnv) signedPostRelay(relay, version string) *httptest.ResponseRecorder {
	orig := bot.RelayURLPattern
	bot.RelayURLPattern = anyHTTP
	defer func() { bot.RelayURLPattern = orig }()
	return e.signedPost("/api/v1/bot/connect", map[string]any{"relay": relay, "version": version})
}

var anyHTTP = regexp.MustCompile(`^http://127\.0\.0\.1:\d+$`)
