package http

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/bnursik/business_surgery_backend/internal/ai"
	pg "github.com/bnursik/business_surgery_backend/internal/repository/pg"
	"github.com/bnursik/business_surgery_backend/migrations"
	"github.com/gin-gonic/gin"
)

// The events refresh outlives one HTTP answer: the button gets pending, the
// page asks until the search is done; errors come readable.
func TestEventsRefreshInBackground(t *testing.T) {
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
	_, _ = db.Pool.Exec(ctx, `DELETE FROM platform_docs WHERE key = 'bs_events_feed'`)
	oldWait, oldBack := eventsWait, ai.SearchBackoff
	eventsWait, ai.SearchBackoff = 100*time.Millisecond, []time.Duration{20 * time.Millisecond}
	defer func() { eventsWait, ai.SearchBackoff = oldWait, oldBack }()

	var mode atomic.Value
	mode.Store("slow")
	var calls atomic.Int32
	gm := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		switch mode.Load().(string) {
		case "slow":
			time.Sleep(400 * time.Millisecond)
			ans := "```json\n{\"items\":[{\"title\":\"Форум предпринимателей\",\"date\":\"2099-01-10\",\"url\":\"https://ex.kz/f\"},]}\n```"
			_ = json.NewEncoder(w).Encode(map[string]any{"candidates": []any{map[string]any{"content": map[string]any{"parts": []any{map[string]any{"text": ans}}}}}})
		case "down":
			w.WriteHeader(503)
			_, _ = w.Write([]byte(`{"error":{"code":503,"message":"The model is overloaded.","status":"UNAVAILABLE"}}`))
		case "nothing":
			_ = json.NewEncoder(w).Encode(map[string]any{"candidates": []any{map[string]any{"content": map[string]any{"parts": []any{map[string]any{"text": `{"items":[]}`}}}}}})
		}
	}))
	defer gm.Close()
	repo := pg.NewPlatformRepo(db)
	h := NewPlatformAI(repo, &ai.Client{Gemini: "k", GeminiModel: "gemini-3.8-flash", GeminiBase: gm.URL, HTTP: gm.Client()})
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(func(c *gin.Context) { c.Set("role", "admin"); c.Set("userID", "tg:453800951") })
	r.POST("/ai/events", h.RefreshEvents)
	r.GET("/ai/events", h.EventsStatus)
	call := func(method string) map[string]any {
		w := httptest.NewRecorder()
		r.ServeHTTP(w, httptest.NewRequest(method, "/ai/events", nil))
		var out map[string]any
		_ = json.Unmarshal(w.Body.Bytes(), &out)
		return out
	}
	wait := func() map[string]any {
		for i := 0; i < 100; i++ {
			if s := call("GET"); s["busy"] != true {
				return s
			}
			time.Sleep(30 * time.Millisecond)
		}
		t.Fatal("still busy")
		return nil
	}

	// Slow search: pending at first, a second click joins the same search.
	if s := call("POST"); s["pending"] != true {
		t.Fatalf("pending: %v", s)
	}
	if s := call("POST"); s["pending"] != true {
		t.Fatalf("joined: %v", s)
	}
	if s := wait(); s["found"] != float64(1) || s["error"] != nil {
		t.Fatalf("done: %v", s)
	}
	if calls.Load() != 1 {
		t.Fatalf("one search, not %d", calls.Load())
	}
	d, _ := repo.GetDoc(ctx, "club", "bs_events_feed")
	if d == nil || !strings.Contains(d.Value, "Форум предпринимателей") {
		t.Fatalf("feed: %+v", d)
	}

	// The model is overloaded: retried, then a readable error and the raw cause; the feed stays.
	mode.Store("down")
	calls.Store(0)
	s := call("POST")
	if s["busy"] == true {
		s = wait()
	}
	if e, _ := s["error"].(string); !strings.Contains(e, "перегружен") || !strings.Contains(s["detail"].(string), "503") || calls.Load() != 2 {
		t.Fatalf("overloaded: %v calls=%d", s, calls.Load())
	}
	// Nothing found: the old feed is kept.
	mode.Store("nothing")
	s = call("POST")
	if s["busy"] == true {
		s = wait()
	}
	if e, _ := s["error"].(string); !strings.Contains(e, "лента осталась прежней") {
		t.Fatalf("nothing: %v", s)
	}
	if d, _ := repo.GetDoc(ctx, "club", "bs_events_feed"); d == nil || !strings.Contains(d.Value, "Форум предпринимателей") {
		t.Fatal("the feed was wiped")
	}
}
