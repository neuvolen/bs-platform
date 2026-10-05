package http

import (
	"context"
	"log"
	"net/http"
	"sync"
	"time"

	"github.com/bnursik/business_surgery_backend/internal/ai"
	"github.com/gin-gonic/gin"
)

// Refreshing the events feed runs on the server on its own: a search with a
// web-searching model can take a few minutes, longer than a browser or a
// proxy in front of the server waits for one answer (that was the error the
// team saw). The button starts the search (or joins the one running), waits
// up to eventsWait for it, and otherwise answers {pending:true}; the page
// then asks GET /ai/events until the search is done. An error comes as
// {error: what to tell the team, detail: the raw cause}.

// eventsWait: how long POST /ai/events waits before answering pending.
var eventsWait = 40 * time.Second

// eventsTimeout: the whole search, retries included.
var eventsTimeout = 6 * time.Minute

type eventsRun struct {
	mu       sync.Mutex
	busy     bool
	done     chan struct{}
	started  time.Time
	finished time.Time
	by       string
	found    int
	err      error
}

// startEvents starts a refresh unless one runs; it returns the run's done channel.
func (h *PlatformAI) startEvents(by string) chan struct{} {
	r := &h.ev
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.busy {
		return r.done
	}
	r.busy, r.done, r.started, r.by = true, make(chan struct{}), time.Now(), by
	done := r.done
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), eventsTimeout)
		n, err := h.refreshEvents(ctx)
		cancel()
		log.Printf("platform events (%s): %d found, err=%v", by, n, err)
		r.mu.Lock()
		r.busy, r.finished, r.found, r.err = false, time.Now(), n, err
		r.mu.Unlock()
		close(done)
	}()
	return done
}

func (h *PlatformAI) eventsState() gin.H {
	r := &h.ev
	r.mu.Lock()
	defer r.mu.Unlock()
	out := gin.H{"busy": r.busy}
	if r.busy {
		out["pending"] = true
		out["started"] = r.started.UTC().Format(time.RFC3339)
		out["seconds"] = int(time.Since(r.started).Seconds())
		return out
	}
	if r.finished.IsZero() {
		return out
	}
	out["finished"] = r.finished.UTC().Format(time.RFC3339)
	if r.err != nil {
		out["error"] = ai.FriendlyError(r.err)
		out["detail"] = ai.UserMessage(r.err)
		out["quota"] = ai.IsQuota(r.err)
		// R42: no model can search now: the feed stays, the page says why
		if ai.SearchUnavailable(r.err) && h.AI != nil && h.AI.HasText() {
			out["nosearch"] = true
			out["error"] = EventsNoSearch
		}
	} else {
		out["found"] = r.found
	}
	return out
}

// RefreshEvents: POST /ai/events (team) refreshes the feed now.
func (h *PlatformAI) RefreshEvents(c *gin.Context) {
	if !teamOnly(c) {
		return
	}
	if h.AI.Status()["text"] == "" {
		c.JSON(http.StatusOK, gin.H{"error": ai.ErrNoKey.Error()})
		return
	}
	done := h.startEvents("button")
	t := time.NewTimer(eventsWait)
	defer t.Stop()
	select {
	case <-done:
	case <-t.C:
	case <-c.Request.Context().Done():
	}
	c.JSON(http.StatusOK, h.eventsState())
}

// EventsStatus: GET /ai/events (team) — whether a refresh runs and how the last one ended.
func (h *PlatformAI) EventsStatus(c *gin.Context) {
	if !teamOnly(c) {
		return
	}
	c.JSON(http.StatusOK, h.eventsState())
}

// EventsNoSearch: the events feed without a model that can search (R42).
const EventsNoSearch = "Поиск в интернете сейчас недоступен (у Claude нет баланса, бесплатный лимит поиска Gemini исчерпан или нет ключа). Лента осталась прежней, обновится сама, когда поиск вернётся"
