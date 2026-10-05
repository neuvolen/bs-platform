package http

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/bnursik/business_surgery_backend/internal/content"
	"github.com/bnursik/business_surgery_backend/internal/middleware"
	"github.com/gin-gonic/gin"
)

// The team's content endpoints (platform JWT, team only):
//
//	POST /api/v1/platform/content/plan          {reset?: bool} re-plan the queue
//	POST /api/v1/platform/content/publish/:id   publish a queue item now
//	GET  /api/v1/platform/content/library       the library index (?src=g003 with texts, ?full=1 all texts)
//	GET  /api/v1/platform/content/threads       Threads per day for the planner: {perDay, from, to, buildAt, days:[{day, on, count, build}]}
//	POST /api/v1/platform/content/threads/build {day: "2026-10-05", force?: bool} make that day's Threads batch now
type ContentModule struct {
	E      *ContentEngine
	secret []byte
}

func NewContentModule(e *ContentEngine, secret []byte) *ContentModule {
	return &ContentModule{E: e, secret: secret}
}

func (m *ContentModule) Register(r *gin.Engine) {
	r.GET("/go/th/:id", m.goThreads) // the manual Threads mode's long-post button (content_threads_manual.go)
	g := r.Group("/api/v1/platform/content")
	g.Use(middleware.AuthJWT(m.secret))
	g.Use(middleware.RequireRole("admin", "moderator"))
	g.POST("/plan", m.plan)
	g.POST("/publish/:id", m.publish)
	g.GET("/library", m.library)
	g.GET("/threads", m.threadsDays)
	g.POST("/threads/build", m.threadsBuild)
}

func (m *ContentModule) threadsDays(c *gin.Context) {
	if !m.ready(c) {
		return
	}
	out, err := m.E.ThreadsDays(c.Request.Context())
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, out)
}

func (m *ContentModule) threadsBuild(c *gin.Context) {
	if !m.ready(c) {
		return
	}
	var req struct {
		Day   string `json:"day"`
		Force bool   `json:"force"`
	}
	_ = c.ShouldBindJSON(&req)
	day := m.E.now().In(almaty)
	if req.Day != "" {
		t, err := time.ParseInLocation("2006-01-02", req.Day, almaty)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "day: YYYY-MM-DD"})
			return
		}
		if t.Before(dayStart(day)) || t.After(dayStart(day).AddDate(0, 0, contentDays)) {
			c.JSON(http.StatusBadRequest, gin.H{"error": "Собрать можно сегодня и на 2 недели вперёд"})
			return
		}
		day = t
	}
	if !m.E.building.CompareAndSwap(false, true) {
		c.JSON(http.StatusOK, gin.H{"ok": false, "error": "Пачка уже собирается, обновите через минуту"})
		return
	}
	defer m.E.building.Store(false)
	ctx, cancel := context.WithTimeout(c.Request.Context(), 6*time.Minute)
	defer cancel()
	res, err := m.E.BuildThreadsDay(ctx, day, req.Force)
	if err != nil {
		c.JSON(http.StatusOK, gin.H{"ok": false, "error": err.Error()})
		return
	}
	d, _, _ := m.E.load(ctx)
	out := gin.H{"ok": true, "build": res}
	if d != nil {
		out["queue"] = d.Queue
	}
	c.JSON(http.StatusOK, out)
}

func (m *ContentModule) ready(c *gin.Context) bool {
	if !teamOnly(c) {
		return false
	}
	if m.E == nil || m.E.docs == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "no storage"})
		return false
	}
	return true
}

func (m *ContentModule) plan(c *gin.Context) {
	if !m.ready(c) {
		return
	}
	var req struct {
		Reset bool `json:"reset"`
	}
	_ = c.ShouldBindJSON(&req)
	ctx, cancel := context.WithTimeout(c.Request.Context(), 30*time.Second)
	defer cancel()
	n, d, err := m.E.Plan(ctx, req.Reset)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"ok": true, "added": n, "queue": d.Queue, "library": len(m.E.lib())})
}

func (m *ContentModule) publish(c *gin.Context) {
	if !m.ready(c) {
		return
	}
	ctx, cancel := context.WithTimeout(c.Request.Context(), 2*time.Minute)
	defer cancel()
	// The editor sends what it shows: its text wins over a doc copy the
	// sync has not delivered yet, and an item added on the platform a
	// moment ago is created here.
	var body contentItemData
	_ = c.ShouldBindJSON(&body)
	id := c.Param("id")
	if strings.TrimSpace(body.Text) != "" {
		if _, err := m.E.update(ctx, func(d *contentDoc) bool {
			it := findContent(d, id)
			if it == nil {
				if body.Channel == "" {
					return false
				}
				body.ID, body.Status, body.Edited = id, "planned", true
				if body.At == "" {
					body.At = time.Now().In(almaty).Format(time.RFC3339)
				}
				d.Queue = append(d.Queue, &contentItem{contentItemData: body})
				return true
			}
			if it.Text == body.Text && (body.Caption == "" || it.Caption == body.Caption) {
				return false
			}
			it.Text, it.Edited = body.Text, true
			if body.Caption != "" {
				it.Caption = body.Caption
			}
			return true
		}); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
	}
	it, err := m.E.Publish(ctx, id, true)
	if errors.Is(err, errContentNotFound) {
		c.JSON(http.StatusNotFound, gin.H{"error": err.Error()})
		return
	}
	if err != nil {
		c.JSON(http.StatusOK, gin.H{"ok": false, "error": err.Error(), "item": it})
		return
	}
	c.JSON(http.StatusOK, gin.H{"ok": true, "item": it})
}

func (m *ContentModule) library(c *gin.Context) {
	if !teamOnly(c) {
		return
	}
	only, full := c.Query("src"), c.Query("full") == "1"
	items := []gin.H{}
	counts := map[string]int{"guides": 0, "rubric": 0, "threads": 0, "telegram": 0, "carousel": 0, "reels": 0}
	for _, li := range m.E.lib() {
		if only != "" && li.Src != only {
			continue
		}
		if li.Rubric == "" {
			counts["guides"]++
		} else {
			counts["rubric"]++
		}
		for _, kind := range []string{"threads", "telegram", "carousel", "reels"} {
			for v := 0; v < li.Variants(kind); v++ {
				tx, ok := libContent(li, kind, v)
				if !ok {
					continue
				}
				counts[kind]++
				ch := kind
				if kind == "carousel" || kind == "reels" {
					ch = "instagram"
				}
				x := gin.H{"src": li.Src, "kind": kind, "v": v, "channel": ch, "title": li.Title, "organ": li.Organ,
					"rubric": li.Rubric, "first": content.FirstLine(tx.text, 140), "link": contentLink(ch, li.Src, tx.text)}
				if full || only != "" {
					x["text"] = tx.text
					if tx.caption != "" {
						x["caption"] = tx.caption
					}
					if len(tx.slides) > 0 {
						x["slides"] = tx.slides
					}
					if len(tx.reels) > 0 {
						x["reels"] = tx.reels
					}
				}
				items = append(items, x)
			}
		}
	}
	c.JSON(http.StatusOK, gin.H{"items": items, "counts": counts})
}
