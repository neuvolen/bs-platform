package http

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
)

type fakeClubIDs struct{ ids []int64 }

func (f *fakeClubIDs) ClubTgIDs(context.Context) ([]int64, error) { return f.ids, nil }

func TestAppAvatar(t *testing.T) {
	var calls int32
	tg := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		switch {
		case strings.Contains(r.URL.Path, "getUserProfilePhotos"):
			if r.URL.Query().Get("user_id") == "222" {
				_, _ = w.Write([]byte(`{"ok":true,"result":{"total_count":0,"photos":[]}}`))
				return
			}
			_, _ = w.Write([]byte(`{"ok":true,"result":{"total_count":1,"photos":[[{"file_id":"s","width":160},{"file_id":"b","width":640}]]}}`))
		case strings.Contains(r.URL.Path, "getFile"):
			if r.URL.Query().Get("file_id") != "s" {
				t.Errorf("must take the 160px size, got %s", r.URL.Query().Get("file_id"))
			}
			_, _ = w.Write([]byte(`{"ok":true,"result":{"file_path":"photos/a.jpg"}}`))
		case strings.HasPrefix(r.URL.Path, "/file/"):
			w.Header().Set("Content-Type", "image/jpeg")
			_, _ = w.Write([]byte("JPEGDATA"))
		}
	}))
	defer tg.Close()
	g := NewAppGateway(testBotToken, "http://127.0.0.1:1/exec")
	g.TGBase = tg.URL
	g.Admins = map[int64]string{453800951: "Рустам"}
	g.Avatars = &fakeClubIDs{ids: []int64{111, 222}}
	now := time.Now()
	g.now = func() time.Time { return now }
	gin.SetMode(gin.TestMode)
	r := gin.New()
	NewAppGatewayModule(g).Register(r)
	get := func(id string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		r.ServeHTTP(w, httptest.NewRequest("GET", "/api/v1/app/avatar/"+id, nil))
		return w
	}
	if w := get("111"); w.Code != 200 || w.Body.String() != "JPEGDATA" || w.Header().Get("Content-Type") != "image/jpeg" {
		t.Fatalf("resident photo: %d %q", w.Code, w.Body.String())
	}
	n := atomic.LoadInt32(&calls)
	if w := get("111"); w.Code != 200 || atomic.LoadInt32(&calls) != n {
		t.Fatalf("second request must come from memory")
	}
	if w := get("453800951"); w.Code != 200 {
		t.Fatalf("team photo: %d", w.Code)
	}
	if w := get("222"); w.Code != 404 {
		t.Fatalf("no photo: want 404, got %d", w.Code)
	}
	before := atomic.LoadInt32(&calls)
	if w := get("999"); w.Code != 404 || atomic.LoadInt32(&calls) != before {
		t.Fatalf("a stranger is refused without asking Telegram")
	}
	if w := get("abc"); w.Code != 404 {
		t.Fatalf("bad id")
	}
}
