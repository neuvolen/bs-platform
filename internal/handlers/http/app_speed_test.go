package http

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"io"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

// R22: the app opened slowly. An open never waits long for the script when
// the server has the data; the script's answer is kept for the next open.

func slowScript(e *clubEnv, d time.Duration) {
	e.f.mu.Lock()
	prev := e.f.bundle
	e.f.bundle = func() []byte { time.Sleep(d); return prev() }
	e.f.mu.Unlock()
}

func withBudget(t *testing.T, d time.Duration) {
	old := scriptBudget
	scriptBudget = d
	t.Cleanup(func() { scriptBudget = old })
}

func TestShadowOpenDoesNotWaitForTheScript(t *testing.T) {
	e := newClubEnv(t)
	withBudget(t, 150*time.Millisecond)
	slowScript(e, 900*time.Millisecond)

	t0 := time.Now()
	b, w := e.bundle(t, 490685605)
	if took := time.Since(t0); took > 700*time.Millisecond {
		t.Fatalf("waited %v for the script", took)
	}
	if w.Header().Get("X-BS-Bundle-Source") != "server-interim" || b["residents"] == nil || b["schedule"] == nil {
		t.Fatalf("the server's data answers: %s %s", w.Header().Get("X-BS-Bundle-Source"), w.Body.String()[:200])
	}
	// The script's answer arrives behind and is the next open's, at once.
	time.Sleep(1200 * time.Millisecond)
	t0 = time.Now()
	b, w = e.bundle(t, 490685605)
	if time.Since(t0) > 100*time.Millisecond || w.Header().Get("X-BS-Bundle-Source") != "" || w.Header().Get("X-BS-Bundle-Age") == "" {
		t.Fatalf("the kept script copy: %v %v", time.Since(t0), w.Header())
	}
	if b["residents"] == nil {
		t.Fatalf("the script's bundle: %v", b)
	}
	if n := e.scriptCalls("getBotCache"); n != 1 {
		t.Fatalf("the script was asked %d times", n)
	}

	// A change of data: the fetch that started before it is not kept.
	e.g.dropBundles()
	_, w = e.bundle(t, 490685605)
	if w.Header().Get("X-BS-Bundle-Source") != "server-interim" {
		t.Fatalf("after a change: %v", w.Header())
	}
}

func TestShadowFastScriptAnswersAsBefore(t *testing.T) {
	e := newClubEnv(t)
	_, w := e.bundle(t, 490685605)
	if w.Header().Get("X-BS-Bundle-Source") != "" || w.Header().Get("X-BS-Bundle-Age") != "0" {
		t.Fatalf("a quick script answers itself: %v", w.Header())
	}
}

func TestSheetStageStillWaitsForTheScript(t *testing.T) {
	e := newClubEnv(t)
	withBudget(t, 100*time.Millisecond)
	if err := e.mig.SetStage(context.Background(), StageSheet, "test"); err != nil {
		t.Fatal(err)
	}
	slowScript(e, 400*time.Millisecond)
	t0 := time.Now()
	_, w := e.bundle(t, 490685605)
	if time.Since(t0) < 350*time.Millisecond || w.Header().Get("X-BS-Bundle-Source") != "" {
		t.Fatalf("stage sheet: the script answers: %v %v", time.Since(t0), w.Header())
	}
}

func TestServerStageMarksMissingScriptSections(t *testing.T) {
	e := newClubEnv(t)
	withBudget(t, 100*time.Millisecond)
	ctx := context.Background()
	if err := e.mig.SetStage(ctx, StageServer, "test"); err != nil {
		t.Fatal(err)
	}
	slowScript(e, 800*time.Millisecond)
	t0 := time.Now()
	b, w := e.bundle(t, 490685605)
	if time.Since(t0) > 600*time.Millisecond || w.Header().Get("X-BS-Bundle-Source") != "server" {
		t.Fatalf("server stage: %v %v", time.Since(t0), w.Header())
	}
	p, _ := b["_partial"].([]any)
	if len(p) == 0 {
		t.Fatalf("the sections the script has are marked: %v", b["_partial"])
	}
	time.Sleep(1100 * time.Millisecond)
	b, _ = e.bundle(t, 490685605)
	if b["_partial"] != nil {
		t.Fatalf("with the script's part there: %v", b["_partial"])
	}
}

func TestRoleFromTheServer(t *testing.T) {
	e := newClubEnv(t)
	slowScript(e, 0)
	if r := e.call(490685605, "checkUserRole"); r["role"] != "resident" || r["source"] != "server" {
		t.Fatalf("resident: %v", r)
	}
	if r := e.call(999, "checkUserRole"); r["role"] != "lead" || r["source"] != "server" {
		t.Fatalf("lead: %v", r)
	}
	if e.scriptCalls("checkUserRole") != 0 {
		t.Fatal("the script is not asked")
	}
	// The team and stage sheet: the script, as before.
	e.call(453800951, "checkUserRole")
	if err := e.mig.SetStage(context.Background(), StageSheet, "test"); err != nil {
		t.Fatal(err)
	}
	e.mig.stageAt = time.Time{}
	e.call(490685605, "checkUserRole")
	if e.scriptCalls("checkUserRole") != 2 {
		t.Fatalf("script asked %d", e.scriptCalls("checkUserRole"))
	}
}

func TestSaveAvatarKeepsBundles(t *testing.T) {
	e := newClubEnv(t)
	e.bundle(t, 490685605)
	gen := e.g.gen
	e.call(490685605, "saveAvatar", "avatar", "https://t.me/i/userpic/1.jpg")
	if e.g.gen != gen || e.g.bundles["490685605"] == nil {
		t.Fatal("the photo saved on every open does not drop everyone's bundle")
	}
	e.call(490685605, "addFine", "name", "Альтаир", "amount", "1")
	if e.g.gen == gen {
		t.Fatal("a club change does")
	}
}

func TestAppAnswersGzipped(t *testing.T) {
	e := newClubEnv(t)
	q := url.Values{"action": {"getBotCache"}, "_tg": {makeInitData(testBotToken, 453800951, "Рустам", *e.now)}}
	plain := get(e.r, q)
	req := httptest.NewRequest("GET", "/api/v1/app/call?"+q.Encode(), nil)
	req.Header.Set("Accept-Encoding", "gzip, deflate, br")
	w := httptest.NewRecorder()
	e.r.ServeHTTP(w, req)
	if w.Header().Get("Content-Encoding") != "gzip" || !strings.Contains(w.Header().Get("Vary"), "Accept-Encoding") {
		t.Fatalf("headers %v", w.Header())
	}
	zr, err := gzip.NewReader(bytes.NewReader(w.Body.Bytes()))
	if err != nil {
		t.Fatal(err)
	}
	got, _ := io.ReadAll(zr)
	var a, b map[string]any
	if json.Unmarshal(got, &a) != nil || json.Unmarshal(plain.Body.Bytes(), &b) != nil || len(a) != len(b) || len(a) < 10 {
		t.Fatalf("same bundle: %d %d", len(a), len(b))
	}
	if w.Body.Len() >= plain.Body.Len()/2 || plain.Header().Get("Content-Encoding") != "" {
		t.Fatalf("gzipped %d of %d", w.Body.Len(), plain.Body.Len())
	}
	// Small answers and errors pass as they are.
	req = httptest.NewRequest("GET", "/api/v1/app/call?action=getBotCache", nil)
	req.Header.Set("Accept-Encoding", "gzip")
	w = httptest.NewRecorder()
	e.r.ServeHTTP(w, req)
	if w.Code != 401 || w.Header().Get("Content-Encoding") != "" || !strings.Contains(w.Body.String(), "not_telegram") {
		t.Fatalf("refusal %d %v %s", w.Code, w.Header(), w.Body.String())
	}
}
