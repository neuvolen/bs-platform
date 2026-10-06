package http

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/andybalholm/brotli"
	"github.com/bnursik/business_surgery_backend/internal/content"
	"github.com/bnursik/business_surgery_backend/internal/middleware"
	"github.com/gin-gonic/gin"
)

// R46: бизнес-идеи.
//
//   GET /api/v1/platform/ideas   каталог для платформы: команда, резиденты и лиды
//                                (токен лида открывает этот адрес, как /lead/*)
//   GET /api/v1/app/ideas?_tg=   тот же каталог для приложения в Telegram
//
// Каталог один JSON (1000+ идей, около 1,5 МБ): brotli около 250 КБ, делается
// один раз в фоне (до того gzip). Страница берёт его, только когда открыли
// раздел «Бизнес-идеи»; повторно браузер получает 304 по ETag.

// Ideas serves the catalogue (the platform: lead, resident, team).
func Ideas(c *gin.Context) { sendIdeas(c) }

// AppIdeas: the same for the Telegram app (identified by initData).
func (g *AppGateway) AppIdeas(c *gin.Context) {
	if _, ok := g.identify(c, c.Query("_tg")); !ok {
		return
	}
	sendIdeas(c)
}

func sendIdeas(c *gin.Context) {
	plain, gz, etag, err := content.Ideas()
	if err != nil || len(plain) == 0 {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "ideas"})
		return
	}
	h := c.Writer.Header()
	h.Set("ETag", etag)
	h.Set("Cache-Control", "private, no-cache")
	h.Set("Vary", "Accept-Encoding")
	h.Set("X-Content-Type-Options", "nosniff")
	inm := c.GetHeader("If-None-Match")
	if inm == "" {
		inm = c.Query("_et") // приложение: другой источник, без лишнего preflight
	}
	if inm != "" && strings.Contains(inm, strings.Trim(etag, `"`)) {
		c.Status(http.StatusNotModified)
		return
	}
	ae := c.GetHeader("Accept-Encoding")
	if br := ideasBrotli(plain, etag); br != nil && middleware.AcceptsEncoding(ae, "br") {
		h.Set("Content-Encoding", "br")
		c.Data(http.StatusOK, "application/json; charset=utf-8", br)
		return
	}
	if middleware.AcceptsEncoding(ae, "gzip") {
		h.Set("Content-Encoding", "gzip")
		c.Data(http.StatusOK, "application/json; charset=utf-8", gz)
		return
	}
	c.Data(http.StatusOK, "application/json; charset=utf-8", plain)
}

var ideasBr struct {
	sync.Mutex
	etag, busy string
	b          []byte
	done       chan struct{}
}

func ideasBrotli(plain []byte, etag string) []byte {
	ideasBr.Lock()
	defer ideasBr.Unlock()
	if ideasBr.etag == etag {
		return ideasBr.b
	}
	if ideasBr.busy != etag {
		ideasBr.busy = etag
		done := make(chan struct{})
		ideasBr.done = done
		go func() {
			defer close(done)
			var buf bytes.Buffer
			bw := brotli.NewWriterOptions(&buf, brotli.WriterOptions{Quality: 11, LGWin: 22})
			_, _ = bw.Write(plain)
			_ = bw.Close()
			ideasBr.Lock()
			if ideasBr.busy == etag {
				ideasBr.etag, ideasBr.b = etag, buf.Bytes()
			}
			ideasBr.Unlock()
		}()
	}
	return nil
}

// WarmIdeas starts the brotli copy at start-up; wait (tests) blocks until it is made.
func WarmIdeas(wait bool) {
	plain, _, etag, err := content.Ideas()
	if err != nil || len(plain) == 0 {
		return
	}
	ideasBrotli(plain, etag)
	ideasBr.Lock()
	d := ideasBr.done
	ideasBr.Unlock()
	if wait && d != nil {
		<-d
	}
}

// LeadIdea godoc
// @Summary  A lead marks a business idea («Обсудить на разборе», «Взять в работу»)
// @Description  Body {id, kind: discuss|work}. The lead's CRM card gets the idea (ideas[]) and one line in its log, so the team sees it before the разбор. No message to the team: the card is the place.
// @Tags     platform
// @Security BearerAuth
// @Router   /api/v1/platform/lead/idea [post]
func (h *LeadHome) Idea(c *gin.Context) {
	var req struct {
		ID   string `json:"id"`
		Kind string `json:"kind"`
	}
	if err := c.ShouldBindJSON(&req); err != nil || req.ID == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "bad_body"})
		return
	}
	it := content.IdeaByID(req.ID)
	if it == nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "not_found"})
		return
	}
	if req.Kind != "work" {
		req.Kind = "discuss"
	}
	u, ok := h.leadUser(c)
	if !ok {
		return
	}
	found, err := markLeadIdea(h.f, u.ID, it, req.Kind)
	if err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": "storage"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"ok": true, "card": found})
}

func ideaBudget(b [2]int64) string {
	f := func(n int64) string {
		s := fmt.Sprint(n)
		var out []byte
		for i := range s {
			if i > 0 && (len(s)-i)%3 == 0 {
				out = append(out, ' ')
			}
			out = append(out, s[i])
		}
		return string(out)
	}
	if b[1] > b[0] {
		return f(b[0]) + "-" + f(b[1])
	}
	return f(b[0])
}

// markLeadIdea: the idea on the lead's CRM card (ideas[] and one log line), once per idea and kind.
func markLeadIdea(f *LeadFunnel, tg int64, it *content.IdeaItem, kind string) (bool, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	now := f.now()
	found := false
	err := f.mutate(ctx, "bs_crm", func(crm map[string]any) bool {
		leads, _ := crm["leads"].([]any)
		lead := findLeadByTg(leads, tg)
		if lead == nil {
			return false
		}
		found = true
		list, _ := lead["ideas"].([]any)
		for _, x := range list {
			if m, _ := x.(map[string]any); m != nil && lhStr(m, "id") == it.ID && lhStr(m, "kind") == kind {
				return false // уже отмечена: карточка не меняется
			}
		}
		lead["ideas"] = append(list, map[string]any{"id": it.ID, "title": it.Title, "cat": it.Cat, "kind": kind, "at": now.UTC().Format(time.RFC3339)})
		verb := "Хочет обсудить на разборе бизнес-идею"
		if kind == "work" {
			verb = "Взял в работу бизнес-идею"
		}
		addLog(lead, now, fmt.Sprintf("%s «%s» (бюджет %s ₸)", verb, it.Title, ideaBudget(it.Budget)))
		if s, _ := lead["niche"].(string); s == "" {
			lead["niche"] = "Ищет идею: " + it.Title
		}
		return true
	})
	return found, err
}

// AppIdea: the same mark from the Telegram app (POST /api/v1/app/idea?_tg=, body {id, kind}).
// A resident or the team gets ok without a CRM card: the mark is the lead's way to разбор.
func (g *AppGateway) AppIdea(c *gin.Context) {
	u, ok := g.identify(c, c.Query("_tg"))
	if !ok {
		return
	}
	var req struct {
		ID   string `json:"id"`
		Kind string `json:"kind"`
	}
	if err := c.ShouldBindJSON(&req); err != nil || req.ID == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "bad_body"})
		return
	}
	it := content.IdeaByID(req.ID)
	if it == nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "not_found"})
		return
	}
	if req.Kind != "work" {
		req.Kind = "discuss"
	}
	if g.Funnel == nil {
		c.JSON(http.StatusOK, gin.H{"ok": true, "card": false})
		return
	}
	found, err := markLeadIdea(g.Funnel, u.ID, it, req.Kind)
	if err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": "storage"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"ok": true, "card": found})
}
