package http

import (
	"net/http/httptest"
	"testing"
	"time"
)

// R70: the Mini App sends initData in the X-Tg-Init header, out of the URL;
// the _tg parameter of older copies still works.
func TestAppInitDataHeader(t *testing.T) {
	_, f, r, now := newGateway(t)
	call := func(header, query string) int {
		u := "/api/v1/app/call?action=getTodos"
		if query != "" {
			u += "&_tg=" + query
		}
		req := httptest.NewRequest("GET", u, nil)
		if header != "" {
			req.Header.Set(AppInitHeader, header)
		}
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		return w.Code
	}
	good := makeInitData(testBotToken, 478757502, "Асет", *now)
	if c := call(good, ""); c != 200 {
		t.Fatalf("header: %d", c)
	}
	if f.calls[0].Get("chatId") != "478757502" {
		t.Fatalf("caller from header: %v", f.calls[0])
	}
	if c := call(makeInitData("999:other", 478757502, "Асет", *now), ""); c != 401 {
		t.Fatalf("forged header: %d", c)
	}
	if c := call(makeInitData(testBotToken, 478757502, "Асет", now.Add(-8*24*time.Hour)), ""); c != 401 {
		t.Fatalf("old header: %d", c)
	}
	if c := call("", ""); c != 401 {
		t.Fatalf("nothing: %d", c)
	}
}
