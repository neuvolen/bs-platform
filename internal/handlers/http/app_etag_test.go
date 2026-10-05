package http

import (
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

// R44: the app keeps its bundle and asks again with the bundle's tag; an
// unchanged bundle costs a 304 without a body, a changed one comes whole.

func etagGet(e *clubEnv, q url.Values, inm string) *httptest.ResponseRecorder {
	req := httptest.NewRequest("GET", "/api/v1/app/call?"+q.Encode(), nil)
	req.Header.Set("Accept-Encoding", "gzip")
	if inm != "" {
		req.Header.Set("If-None-Match", inm)
	}
	w := httptest.NewRecorder()
	e.r.ServeHTTP(w, req)
	return w
}

func TestAppBundleETag(t *testing.T) {
	e := newClubEnv(t)
	q := url.Values{"action": {"getBotCache"}, "_tg": {makeInitData(testBotToken, 453800951, "Рустам", *e.now)}}
	w := etagGet(e, q, "")
	tag := w.Header().Get("ETag")
	if w.Code != 200 || !strings.HasPrefix(tag, `W/"`) || w.Header().Get("Content-Encoding") != "gzip" || w.Body.Len() < 1000 {
		t.Fatalf("first answer %d tag %q enc %q len %d", w.Code, tag, w.Header().Get("Content-Encoding"), w.Body.Len())
	}

	// the bundle is built again a second later: its "ts" moves, the tag does not
	*e.now = e.now.Add(time.Second)
	q2 := url.Values{"action": {"getBotCache"}, "_tg": {makeInitData(testBotToken, 453800951, "Рустам", *e.now)}, "_et": {tag}}
	w = etagGet(e, q2, "")
	if w.Code != 304 || w.Body.Len() != 0 || w.Header().Get("ETag") != tag {
		t.Fatalf("same data by _et: %d len %d tag %q", w.Code, w.Body.Len(), w.Header().Get("ETag"))
	}
	// the standard header works the same; a bare tag without W/ and quotes too
	w = etagGet(e, q, tag)
	if w.Code != 304 || w.Body.Len() != 0 {
		t.Fatalf("same data by If-None-Match: %d", w.Code)
	}
	q3 := url.Values{"action": {"getBotCache"}, "_tg": q2["_tg"], "_et": {strings.Trim(strings.TrimPrefix(tag, "W/"), `"`)}}
	if w = etagGet(e, q3, ""); w.Code != 304 {
		t.Fatalf("bare tag: %d", w.Code)
	}

	// someone else's tag: the whole bundle
	q4 := url.Values{"action": {"getBotCache"}, "_tg": q2["_tg"], "_et": {`W/"0000"`}}
	if w = etagGet(e, q4, ""); w.Code != 200 || w.Body.Len() < 1000 {
		t.Fatalf("other tag: %d len %d", w.Code, w.Body.Len())
	}

	// the data changes: the old tag no longer matches
	if r := e.call(453800951, "addFine", "name", "Альтаир", "type", "Опоздание", "amount", "5000"); r["ok"] != true {
		t.Fatalf("addFine %v", r)
	}
	w = etagGet(e, q2, "")
	if w.Code != 200 || w.Header().Get("ETag") == tag || w.Body.Len() < 1000 {
		t.Fatalf("changed data: %d tag %q (old %q)", w.Code, w.Header().Get("ETag"), tag)
	}
}

func TestAppETagOnlyForGoodGets(t *testing.T) {
	e := newClubEnv(t)
	// refusals carry no tag and never turn into 304
	req := httptest.NewRequest("GET", "/api/v1/app/call?action=getBotCache&_et=x", nil)
	req.Header.Set("If-None-Match", "*")
	w := httptest.NewRecorder()
	e.r.ServeHTTP(w, req)
	if w.Code != 401 || w.Header().Get("ETag") != "" {
		t.Fatalf("refusal %d %q", w.Code, w.Header().Get("ETag"))
	}
	// without gzip the tag is the same and 304 works too
	q := url.Values{"action": {"getBotCache"}, "_tg": {makeInitData(testBotToken, 490685605, "Альтаир", *e.now)}}
	plain := get(e.r, q)
	zipped := etagGet(e, q, "")
	if plain.Code != 200 || plain.Header().Get("ETag") == "" || plain.Header().Get("ETag") != zipped.Header().Get("ETag") {
		t.Fatalf("tags %q %q", plain.Header().Get("ETag"), zipped.Header().Get("ETag"))
	}
	q.Set("_et", plain.Header().Get("ETag"))
	if w := get(e.r, q); w.Code != 304 {
		t.Fatalf("plain 304: %d", w.Code)
	}
}
