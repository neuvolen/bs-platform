package ai

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// R70: Claude without balance and the main model's search refused: the light
// Gemini model searches (Google counts the search quota per model).
func TestR70SearchLiteModelGrounding(t *testing.T) {
	SearchBackoff = []time.Duration{time.Millisecond}
	defer func() { SearchBackoff = []time.Duration{4 * time.Second, 12 * time.Second} }()
	var mu sync.Mutex
	searched := map[string]int{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		buf := new(strings.Builder)
		_, _ = io.Copy(buf, r.Body)
		model := strings.TrimPrefix(r.URL.Path, "/v1beta/models/")
		model = model[:strings.Index(model, ":")]
		if !strings.Contains(buf.String(), "google_search") {
			t.Errorf("a search without grounding: %s", model)
		}
		mu.Lock()
		searched[model]++
		mu.Unlock()
		if model == "gemini-3.8-flash" {
			w.WriteHeader(429)
			_, _ = w.Write([]byte(r56Bare))
			return
		}
		_, _ = w.Write([]byte(`{"candidates":[{"content":{"parts":[{"text":"найдено"}]}}]}`))
	}))
	defer srv.Close()
	c := r56Client(srv, "gemini-3.5-flash-lite")
	c.TextOrder = []string{"claude", "gemini"}
	c.Anthropic = "sk-test"
	c.SetQuotaUntil("claude", time.Now().Add(time.Hour)) // no balance
	for i := 0; i < 2; i++ {
		ans, err := c.Search(context.Background(), "events")
		if err != nil || ans != "найдено" {
			t.Fatalf("search %d: %q %v", i, ans, err)
		}
	}
	mu.Lock()
	defer mu.Unlock()
	if searched["gemini-3.8-flash"] != 1 || searched["gemini-3.5-flash-lite"] != 2 {
		t.Fatalf("searches: %v", searched)
	}

	// Both refused: SearchUnavailable, so the feed skips the web part quietly.
	c2 := r56Client(srv, "")
	c2.SetQuotaUntil("gemini-search", time.Now().Add(time.Hour))
	if _, err := c2.Search(context.Background(), "events"); !SearchUnavailable(err) {
		t.Fatalf("no model can search: %v", err)
	}
}

// R70: the pauses and the last self-test outlive a restart.
func TestR70QuotaPersists(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ai-quota.json")
	c := &Client{}
	c.PersistQuota(path)
	c.hold("claude", QuotaInfo{Service: "claude", Billing: true}, nil)
	c.SetQuotaUntil("gemini-search", time.Now().Add(-time.Minute)) // already open
	if !c.selfTestDue(time.Hour) || c.selfTestDue(time.Hour) {
		t.Fatal("self-test due twice")
	}

	c2 := &Client{}
	c2.PersistQuota(path)
	q := c2.quotaClosed("claude")
	if q == nil || !q.Billing || q.Until.Before(time.Now().Add(QuotaHold()-time.Minute)) {
		t.Fatalf("claude pause lost: %+v", q)
	}
	if c2.quotaClosed("gemini-search") != nil {
		t.Fatal("an ended pause came back")
	}
	if c2.selfTestDue(time.Hour) {
		t.Fatal("self-test again right after a restart")
	}
	// Saving the key again opens Claude, and that is kept too.
	c2.SetQuotaUntil("claude", time.Time{})
	c3 := &Client{}
	c3.PersistQuota(path)
	if c3.quotaClosed("claude") != nil {
		t.Fatal("opened pause came back")
	}
	// No file: nothing breaks.
	(&Client{}).PersistQuota(filepath.Join(t.TempDir(), "none", "x.json"))
}

// R70: the start's self-test leaves out a model under a kept daily pause.
func TestR70SelfTestSkipsPaused(t *testing.T) {
	f := &r56Fake{reply: func(m string, n int) (int, string) { return 200, r56OK }}
	srv := f.server(t)
	c := r56Client(srv, "gemini-3.5-flash-lite")
	c.hold("gemini", QuotaInfo{Service: "gemini", Daily: true}, nil)
	lines := c.geminiSelfTest(context.Background(), true)
	if f.n("gemini-3.8-flash") != 0 || f.n("gemini-3.5-flash-lite") != 1 || len(lines) != 2 || !strings.Contains(lines[0], "skipped") {
		t.Fatalf("lines %v, calls %v", lines, f.calls)
	}
	// By hand (ops) it asks past the pause, as before.
	c.GeminiSelfTest(context.Background())
	if f.n("gemini-3.8-flash") != 1 {
		t.Fatal("manual self-test skipped the model")
	}
}
