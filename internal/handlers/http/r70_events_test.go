package http

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/bnursik/business_surgery_backend/internal/ai"
)

// R70: Claude without balance and no Gemini search: the morning refresh skips
// the web part quietly, and a deploy does not run the morning refresh again.
func TestR70EventsQuietAndOncePerMorning(t *testing.T) {
	repo, ctx := testPlatformDB(t, eventsFeedKey)
	if _, err := repo.PutDoc(ctx, "club", eventsFeedKey, 0, `{"items":[{"title":"Старое","date":"2099-10-20"}]}`, false, "test"); err != nil {
		t.Fatal(err)
	}
	c := &ai.Client{Anthropic: "sk-test"}
	c.SetQuotaUntil("claude", time.Now().Add(time.Hour))
	h := NewPlatformAI(repo, c)
	var told []string
	h.Notify = func(_ context.Context, _ int64, s string) error { told = append(told, s); return nil }
	now := time.Now()
	if !h.feedStale(ctx, now) {
		t.Fatal("a feed never refreshed is not stale")
	}
	<-h.startEvents("morning")
	st := h.eventsState()
	if st["error"] != nil || st["nosearch"] != true {
		t.Fatalf("state: %+v", st)
	}
	d, _ := repo.GetDoc(ctx, "club", eventsFeedKey)
	if !strings.Contains(d.Value, "Старое") || !strings.Contains(d.Value, `"tried"`) {
		t.Fatalf("feed: %s", d.Value)
	}
	if h.feedStale(ctx, now) {
		t.Fatal("refreshed this morning, still stale")
	}
	if !h.feedStale(ctx, now.Add(25*time.Hour)) {
		t.Fatal("not stale the next day")
	}
	if len(told) != 0 {
		t.Fatalf("bot told: %q", told)
	}
}
