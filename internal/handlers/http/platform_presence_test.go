package http

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
)

func f64(v float64) *float64 { return &v }

func TestPresenceColorStable(t *testing.T) {
	a, b := presenceColor("tg:453800951"), presenceColor("tg:453800951")
	if a != b || a == "" {
		t.Fatalf("colour must be stable for one id: %q %q", a, b)
	}
	seen := map[string]bool{}
	for i := 0; i < 40; i++ {
		seen[presenceColor("tg:"+strings.Repeat("7", i+1))] = true
	}
	if len(seen) < 5 {
		t.Fatalf("colours must spread over the palette, got %d", len(seen))
	}
	for _, c := range presenceColors {
		if strings.EqualFold(c, "#FF6B6F") {
			t.Fatal("red is for errors and diagnoses only")
		}
	}
}

func TestPresenceHubRoomsExpireAndNotify(t *testing.T) {
	h := newPresenceHub()
	now := time.Unix(1_700_000_000, 0)
	h.now = func() time.Time { return now }
	wake, stop := h.Subscribe("b1")
	defer stop()

	h.Update("b1", presencePeer{ID: "tg:1", Tab: "t1", Name: "Рустам", X: f64(10), Y: f64(20)})
	select {
	case <-wake:
	default:
		t.Fatal("a new person on the board must wake its streams")
	}
	h.Update("b1", presencePeer{ID: "tg:2", Tab: "t9", Name: "Береке"})
	<-wake
	// Same state again: no wake (heartbeats do not flood the streams)
	h.Update("b1", presencePeer{ID: "tg:2", Tab: "t9", Name: "Береке"})
	select {
	case <-wake:
		t.Fatal("an unchanged heartbeat must not wake the streams")
	default:
	}
	// Cursor move wakes
	h.Update("b1", presencePeer{ID: "tg:2", Tab: "t9", Name: "Береке", X: f64(1), Y: f64(2)})
	<-wake

	p := h.Peers("b1", "tg:1|t1")
	if len(p) != 1 || p[0].Name != "Береке" || p[0].X == nil || *p[0].X != 1 {
		t.Fatalf("the asking tab must see the others only: %+v", p)
	}
	if len(h.Peers("b2", "")) != 0 {
		t.Fatal("rooms must not leak into each other")
	}
	// Moving to another board takes the tab off the first one
	h.Update("b2", presencePeer{ID: "tg:2", Tab: "t9", Name: "Береке"})
	<-wake
	if len(h.Peers("b1", "")) != 1 || len(h.Peers("b2", "")) != 1 {
		t.Fatalf("one tab is on one board: b1 %v b2 %v", h.Peers("b1", ""), h.Peers("b2", ""))
	}
	// 31 s of silence: gone, and the sweep wakes the board
	now = now.Add(31 * time.Second)
	h.Sweep()
	select {
	case <-wake:
	default:
		t.Fatal("expiry must wake the board")
	}
	if len(h.Peers("b1", "")) != 0 {
		t.Fatal("a silent person must disappear after 30 s")
	}
	// Leave is immediate
	h.Update("b1", presencePeer{ID: "tg:3", Tab: "x", Name: "А"})
	<-wake
	h.Leave("b1", "tg:3|x")
	<-wake
	if len(h.Peers("b1", "")) != 0 {
		t.Fatal("leave must take the tab off at once")
	}
}

func presenceRouter(pp *PlatformPresence, uid, role string) *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	auth := func(c *gin.Context) { c.Set("userID", uid); c.Set("role", role); c.Next() }
	r.POST("/presence", auth, pp.Post)
	r.GET("/presence/stream", auth, pp.Stream)
	return r
}

func presencePost(t *testing.T, r *gin.Engine, body string) (int, map[string]any) {
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/presence", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(w, req)
	var j map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &j)
	return w.Code, j
}

func testPresence() *PlatformPresence {
	return NewPlatformPresence(
		func(tg int64) string {
			if tg == 1 {
				return "Рустам"
			}
			return ""
		},
		func(c *gin.Context) string {
			if platformUser(c) == "tg:5" {
				return "Асет"
			}
			return ""
		},
		func(_ context.Context, id string) (string, error) {
			switch id {
			case "bAset":
				return "асет ", nil
			case "bOther":
				return "Дамил", nil
			}
			return "", errors.New("no board")
		})
}

func TestPresenceHandlerRolesAndNames(t *testing.T) {
	pp := testPresence()
	admin := presenceRouter(pp, "tg:1", "admin")
	res := presenceRouter(pp, "tg:5", "resident")
	gone := presenceRouter(pp, "tg:6", "resident")

	// The team: name from the login list, not from the page
	code, j := presencePost(t, admin, `{"board":"bAset","tab":"a1","x":100.25,"y":50,"name":"Кто-то другой","photo":"javascript:alert(1)"}`)
	if code != 200 {
		t.Fatalf("admin heartbeat: %d %v", code, j)
	}
	you := j["you"].(map[string]any)
	if you["name"] != "Рустам" || you["color"] != presenceColor("tg:1") {
		t.Fatalf("server name and colour: %v", you)
	}
	// A resident joins their own board and sees the tracker there
	code, j = presencePost(t, res, `{"board":"bAset","tab":"r1","sel":"12","edit":"12","name":"x","photo":"https://t.me/i/userpic/320/a.jpg"}`)
	if code != 200 {
		t.Fatalf("resident on own board: %d %v", code, j)
	}
	peers := j["peers"].([]any)
	if len(peers) != 1 {
		t.Fatalf("resident must see the tracker: %v", peers)
	}
	pr := peers[0].(map[string]any)
	if pr["name"] != "Рустам" || pr["x"].(float64) != 100.2 || pr["photo"] != nil {
		t.Fatalf("peer as the resident sees it (cursor rounded, unsafe photo dropped): %v", pr)
	}
	// …and not someone else's board
	if code, _ = presencePost(t, res, `{"board":"bOther","tab":"r1"}`); code != 403 {
		t.Fatalf("resident on another resident's board must be refused, got %d", code)
	}
	if code, _ = presencePost(t, res, `{"board":"nope","tab":"r1"}`); code != 403 {
		t.Fatalf("resident on a missing board must be refused, got %d", code)
	}
	// A resident who lost access sees nothing
	if code, _ = presencePost(t, gone, `{"board":"bAset","tab":"g1"}`); code != 403 {
		t.Fatalf("former resident must be refused, got %d", code)
	}
	// The tracker now sees the resident with their name, photo, selection and typing
	_, j = presencePost(t, admin, `{"board":"bAset","tab":"a1"}`)
	peers = j["peers"].([]any)
	if len(peers) != 1 {
		t.Fatalf("admin must see the resident: %v", peers)
	}
	pr = peers[0].(map[string]any)
	if pr["name"] != "Асет" || pr["edit"] != "12" || pr["sel"] != "12" || pr["photo"] != "https://t.me/i/userpic/320/a.jpg" || pr["role"] != "resident" {
		t.Fatalf("resident as the admin sees them: %v", pr)
	}
	// Bad input
	for _, b := range []string{`{"board":"","tab":"a"}`, `{"board":"b 1","tab":"a"}`, `{"board":"b1","tab":""}`, `nope`} {
		if code, _ = presencePost(t, admin, b); code != 400 {
			t.Fatalf("%s must be refused, got %d", b, code)
		}
	}
	// Leave
	if code, _ = presencePost(t, res, `{"board":"bAset","tab":"r1","leave":true}`); code != 200 {
		t.Fatal("leave")
	}
	_, j = presencePost(t, admin, `{"board":"bAset","tab":"a1"}`)
	if len(j["peers"].([]any)) != 0 {
		t.Fatalf("after leave the board is empty: %v", j["peers"])
	}
}

func TestPresenceStream(t *testing.T) {
	pp := testPresence()
	admin := presenceRouter(pp, "tg:1", "admin")
	srv := httptest.NewServer(presenceRouter(pp, "tg:5", "resident"))
	defer srv.Close()

	// A resident cannot listen to another board
	resp, err := http.Get(srv.URL + "/presence/stream?board=bOther&tab=r1")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 403 {
		t.Fatalf("stream of someone else's board: %d", resp.StatusCode)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, srv.URL+"/presence/stream?board=bAset&tab=r1", nil)
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if !strings.HasPrefix(resp.Header.Get("Content-Type"), "text/event-stream") {
		t.Fatalf("content type %q", resp.Header.Get("Content-Type"))
	}
	rd := bufio.NewReader(resp.Body)
	next := func() map[string]any {
		for {
			line, err := rd.ReadString('\n')
			if err != nil {
				t.Fatalf("stream ended: %v", err)
			}
			if strings.HasPrefix(line, "data: ") {
				var j map[string]any
				if err := json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &j); err != nil {
					t.Fatal(err)
				}
				return j
			}
		}
	}
	first := next()
	if len(first["peers"].([]any)) != 0 {
		t.Fatalf("empty board first: %v", first)
	}
	// The tracker moves the cursor: the resident's stream gets it at once
	start := time.Now()
	presencePost(t, admin, `{"board":"bAset","tab":"a1","x":5,"y":6}`)
	ev := next()
	peers := ev["peers"].([]any)
	if len(peers) != 1 || peers[0].(map[string]any)["name"] != "Рустам" || peers[0].(map[string]any)["x"].(float64) != 5 {
		t.Fatalf("pushed peers: %v", ev)
	}
	if d := time.Since(start); d > time.Second {
		t.Fatalf("push took %v", d)
	}
}
