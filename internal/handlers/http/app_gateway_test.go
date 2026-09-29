package http

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
)

// initData as Telegram signs it for a Mini App.
func makeInitData(token string, id int64, first string, at time.Time) string {
	vals := map[string]string{
		"auth_date": fmt.Sprint(at.Unix()),
		"query_id":  "AAH",
		"user":      fmt.Sprintf(`{"id":%d,"first_name":%q,"username":"u%d"}`, id, first, id),
	}
	keys := make([]string, 0, len(vals))
	for k := range vals {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var lines []string
	for _, k := range keys {
		lines = append(lines, k+"="+vals[k])
	}
	sec := hmac.New(sha256.New, []byte("WebAppData"))
	sec.Write([]byte(token))
	m := hmac.New(sha256.New, sec.Sum(nil))
	m.Write([]byte(strings.Join(lines, "\n")))
	q := url.Values{}
	for k, v := range vals {
		q.Set(k, v)
	}
	q.Set("hash", hex.EncodeToString(m.Sum(nil)))
	return q.Encode()
}

type fakeAppScript struct {
	mu    sync.Mutex
	calls []url.Values
	posts []map[string]any
	down  bool
	n     int
}

func (f *fakeAppScript) handler(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.down {
		w.WriteHeader(500)
		return
	}
	if r.Method == http.MethodPost {
		var b map[string]any
		_ = json.NewDecoder(r.Body).Decode(&b)
		f.posts = append(f.posts, b)
		// Apps Script answers a POST with a redirect to the result
		http.Redirect(w, r, "/result?x=1", http.StatusFound)
		return
	}
	if r.URL.Path == "/result" {
		_, _ = w.Write([]byte(`{"ok":true,"url":"https://x/y.png"}`))
		return
	}
	f.calls = append(f.calls, r.URL.Query())
	f.n++
	_, _ = fmt.Fprintf(w, `{"n":%d,"chatId":%q,"residents":[]}`, f.n, r.URL.Query().Get("chatId"))
}

func newGateway(t *testing.T) (*AppGateway, *fakeAppScript, *gin.Engine, *time.Time) {
	f := &fakeAppScript{}
	srv := httptest.NewServer(http.HandlerFunc(f.handler))
	t.Cleanup(srv.Close)
	g := NewAppGateway(testBotToken, srv.URL+"/exec")
	g.Admins = map[int64]string{453800951: "Рустам"}
	now := time.Now()
	g.now = func() time.Time { return now }
	gin.SetMode(gin.TestMode)
	r := gin.New()
	NewAppGatewayModule(g).Register(r)
	return g, f, r, &now
}

func get(r *gin.Engine, q url.Values) *httptest.ResponseRecorder {
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest("GET", "/api/v1/app/call?"+q.Encode(), nil))
	return w
}

func TestAppGatewayIdentity(t *testing.T) {
	_, f, r, now := newGateway(t)
	good := makeInitData(testBotToken, 478757502, "Асет", *now)

	for name, tg := range map[string]string{
		"no initData":    "",
		"forged":         makeInitData("999:other-bot", 478757502, "Асет", *now),
		"eight days old": makeInitData(testBotToken, 478757502, "Асет", now.Add(-8*24*time.Hour)),
	} {
		if w := get(r, url.Values{"action": {"getTodos"}, "_tg": {tg}}); w.Code != 401 {
			t.Fatalf("%s: %d", name, w.Code)
		}
	}
	if len(f.calls) != 0 {
		t.Fatal("refused calls reached the script")
	}

	// The page claims to be the owner: the script hears who really called.
	w := get(r, url.Values{"action": {"addFine"}, "chatId": {"453800951"}, "name": {"Альтаир"}, "_tg": {good}})
	if w.Code != 200 {
		t.Fatalf("call: %d %s", w.Code, w.Body.String())
	}
	q := f.calls[0]
	if q.Get("chatId") != "478757502" || q.Get("userName") != "Асет" || q.Get("name") != "Альтаир" || q.Get("_tg") != "" {
		t.Fatalf("forwarded %v", q)
	}
	if q.Get("_srv_sig") != AppSign(testBotToken, "addFine", "478757502", q.Get("_srv_ts")) {
		t.Fatal("bad server signature")
	}
	// Admin acting on a subscriber keeps the subscriber's id; a resident cannot.
	get(r, url.Values{"action": {"banFromChannel"}, "chatId": {"777"}, "_tg": {makeInitData(testBotToken, 453800951, "Рустам", *now)}})
	get(r, url.Values{"action": {"banFromChannel"}, "chatId": {"777"}, "_tg": {good}})
	if f.calls[1].Get("chatId") != "777" || f.calls[2].Get("chatId") != "478757502" {
		t.Fatalf("target: admin %s, resident %s", f.calls[1].Get("chatId"), f.calls[2].Get("chatId"))
	}
}

func TestAppGatewayBundleCache(t *testing.T) {
	_, f, r, now := newGateway(t)
	tg := makeInitData(testBotToken, 478757502, "Асет", *now)
	bundle := func(extra ...string) (int, string) {
		q := url.Values{"action": {"getBotCache"}, "chatId": {"1"}, "_tg": {tg}}
		for i := 0; i+1 < len(extra); i += 2 {
			q.Set(extra[i], extra[i+1])
		}
		w := get(r, q)
		var b struct{ N int }
		_ = json.Unmarshal(w.Body.Bytes(), &b)
		return b.N, w.Header().Get("X-BS-Bundle-Age")
	}
	if n, _ := bundle(); n != 1 {
		t.Fatalf("first %d", n)
	}
	if n, age := bundle(); n != 1 || age != "0" || len(f.calls) != 1 {
		t.Fatalf("second: n %d age %s calls %d", n, age, len(f.calls))
	}
	*now = now.Add(25 * time.Second)
	if n, age := bundle(); n != 1 || age != "25" {
		t.Fatalf("stale served at once: n %d age %s", n, age)
	}
	time.Sleep(200 * time.Millisecond) // background refresh
	if n, _ := bundle(); n != 2 {
		t.Fatalf("refreshed %d", n)
	}
	if n, _ := bundle("fresh", "1"); n != 3 {
		t.Fatalf("fresh=1 goes to the script: %d", n)
	}
	// A change drops every bundle.
	get(r, url.Values{"action": {"addPayment"}, "_tg": {tg}})
	if n, _ := bundle(); n != 5 {
		t.Fatalf("after a change: %d", n)
	}
	// Script down: the last bundle still opens the app.
	f.mu.Lock()
	f.down = true
	f.mu.Unlock()
	if n, _ := bundle("fresh", "1"); n != 5 {
		t.Fatalf("script down: %d", n)
	}
	if w := get(r, url.Values{"action": {"getTodos"}, "_tg": {tg}}); w.Code != 502 {
		t.Fatalf("script down, other call: %d", w.Code)
	}
}

func TestAppGatewayPost(t *testing.T) {
	_, f, r, now := newGateway(t)
	tg := makeInitData(testBotToken, 478757502, "Асет", *now)
	post := func(body map[string]any) *httptest.ResponseRecorder {
		b, _ := json.Marshal(body)
		w := httptest.NewRecorder()
		req := httptest.NewRequest("POST", "/api/v1/app/post", bytes.NewReader(b))
		req.Header.Set("Content-Type", "text/plain;charset=utf-8")
		r.ServeHTTP(w, req)
		return w
	}
	w := post(map[string]any{"bsAction": "sendImage", "chatId": "453800951", "image": "x", "_tg": tg})
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"ok":true`) {
		t.Fatalf("sendImage %d %s", w.Code, w.Body.String())
	}
	p := f.posts[0]
	if p["chatId"] != "478757502" || p["_tg"] != nil || p["_srv_sig"] != AppSign(testBotToken, "sendImage", "478757502", p["_srv_ts"].(string)) {
		t.Fatalf("forwarded %v", p)
	}
	if w := post(map[string]any{"bsAction": "lead", "_tg": tg}); w.Code != 400 {
		t.Fatalf("other bsAction %d", w.Code)
	}
	if w := post(map[string]any{"bsAction": "uploadImage"}); w.Code != 401 {
		t.Fatalf("no initData %d", w.Code)
	}
	_ = io.Discard
}
