package http

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"

	pg "github.com/bnursik/business_surgery_backend/internal/repository/pg"
	"github.com/gin-gonic/gin"
)

// One set of data for the app and the platform.
//
//   POST /api/v1/app/mytests   a test passed in the app lands on the resident's
//                              current board (tests and their history), so the
//                              tracker sees it on the platform at once.
//   GET  /api/v1/app/mycal     the resident's work calendar, the same document
//   PUT  /api/v1/app/mycal     the platform's «Мой календарь» edits (bs_mycal in
//                              the resident's personal scope).

// AppSyncSource writes what the app changes into the platform storage.
type AppSyncSource interface {
	LiveBoards(ctx context.Context) ([]pg.PlatformBoard, error)
	PutBoard(ctx context.Context, id string, baseVersion int, data json.RawMessage, by string) (*pg.PlatformBoard, error)
	GetDoc(ctx context.Context, scope, key string) (*pg.PlatformDoc, error)
	PutDoc(ctx context.Context, scope, key string, baseVersion int, value string, deleted bool, by string) (*pg.PlatformDoc, error)
}

// Tests the app may write, and the history list each one keeps on the board.
var appTestKeys = map[string]string{"wheel": "wheelHist", "diag": "diagHist", "health": ""}

const appHistMax = 12

// syncWho: the resident behind the request (the team may act for a resident by ?name=).
func (g *AppGateway) syncWho(c *gin.Context) (name string, tgID int64, ok bool) {
	u, ok := g.identify(c, c.Query("_tg"))
	if !ok {
		return "", 0, false
	}
	if _, admin := g.Admins[u.ID]; admin && strings.TrimSpace(c.Query("name")) != "" {
		return strings.TrimSpace(c.Query("name")), u.ID, true
	}
	if g.Boards == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "not_configured"})
		return "", 0, false
	}
	n, active, err := g.Boards.ResidentByTg(c.Request.Context(), u.ID)
	if err != nil || !active || n == "" {
		c.JSON(http.StatusForbidden, gin.H{"error": "residents_only"})
		return "", 0, false
	}
	return n, u.ID, true
}

type myTestsReq struct {
	// tests: {"wheel": {"Здоровье": 7, ...}, "diag": {"Финансы": 4, ...}, "health": {...}}
	Tests map[string]map[string]float64 `json:"tests"`
	// date label for the history entry, e.g. "01 окт."
	Date string `json:"date"`
}

// MergeTests puts app results into a board's data. Returns the new data.
func MergeTests(data json.RawMessage, req myTestsReq) (json.RawMessage, error) {
	var b map[string]any
	if err := json.Unmarshal(data, &b); err != nil || b == nil {
		return nil, errors.New("board data unreadable")
	}
	tests, _ := b["tests"].(map[string]any)
	if tests == nil {
		tests = map[string]any{}
	}
	for key, vals := range req.Tests {
		histKey, allowed := appTestKeys[key]
		if !allowed || len(vals) == 0 {
			continue
		}
		clean := map[string]any{}
		sum, n := 0.0, 0
		for axis, v := range vals {
			if v < 0 {
				v = 0
			}
			if v > 10 {
				v = 10
			}
			clean[strings.TrimSpace(axis)] = v
			sum += v
			n++
		}
		if key == "health" {
			b["health"] = clean
			continue
		}
		tests[key] = clean
		if histKey != "" && n > 0 {
			hist, _ := b[histKey].([]any)
			vs := []float64{}
			for _, v := range clean {
				vs = append(vs, v.(float64))
			}
			avg := float64(int(sum/float64(n)*10+0.5)) / 10
			hist = append(hist, map[string]any{"date": req.Date, "avg": avg, "vals": vs, "src": "app"})
			if len(hist) > appHistMax {
				hist = hist[len(hist)-appHistMax:]
			}
			b[histKey] = hist
		}
	}
	b["tests"] = tests
	return json.Marshal(b)
}

// MyTests: POST /api/v1/app/mytests?_tg=
func (g *AppGateway) MyTests(c *gin.Context) {
	if g.Sync == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "not_configured"})
		return
	}
	name, tg, ok := g.syncWho(c)
	if !ok {
		return
	}
	var req myTestsReq
	body, _ := io.ReadAll(http.MaxBytesReader(c.Writer, c.Request.Body, 1<<20))
	if json.Unmarshal(body, &req) != nil || len(req.Tests) == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "tests_required"})
		return
	}
	ctx := c.Request.Context()
	for try := 0; try < 4; try++ {
		boards, err := g.Sync.LiveBoards(ctx)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "boards_failed"})
			return
		}
		b := latestBoardOf(boards, name)
		if b == nil {
			// No board yet: the result is kept in the resident's own scope; the
			// tracker's board picks it up on the first разбор (shown in Замеры).
			val, _ := json.Marshal(req)
			scope := fmt.Sprintf("user:tg:%d", tg)
			base := 0
			if d, _ := g.Sync.GetDoc(ctx, scope, "bs_apptests"); d != nil {
				base = d.Version
			}
			if _, err := g.Sync.PutDoc(ctx, scope, "bs_apptests", base, string(val), false, fmt.Sprintf("tg:%d", tg)); err != nil {
				continue
			}
			c.JSON(http.StatusOK, gin.H{"ok": true, "board": nil})
			return
		}
		data, err := MergeTests(b.Data, req)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		out, err := g.Sync.PutBoard(ctx, b.ID, b.Version, data, fmt.Sprintf("tg:%d", tg))
		if errors.Is(err, pg.ErrPlatformConflict) {
			continue // the board changed meanwhile: merge again
		}
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "save_failed"})
			return
		}
		c.JSON(http.StatusOK, gin.H{"ok": true, "board": out.ID, "version": out.Version})
		return
	}
	c.JSON(http.StatusConflict, gin.H{"error": "busy_try_again"})
}

// MyCal: GET/PUT /api/v1/app/mycal?_tg=  body for PUT: {"value": "<json>", "version": n}
func (g *AppGateway) MyCal(c *gin.Context) {
	if g.Sync == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "not_configured"})
		return
	}
	_, tg, ok := g.syncWho(c)
	if !ok {
		return
	}
	ctx := c.Request.Context()
	scope := fmt.Sprintf("user:tg:%d", tg)
	if c.Request.Method == http.MethodGet {
		d, err := g.Sync.GetDoc(ctx, scope, "bs_mycal")
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "load_failed"})
			return
		}
		if d == nil || d.Deleted {
			c.JSON(http.StatusOK, gin.H{"value": `{"slots":{}}`, "version": 0})
			return
		}
		c.JSON(http.StatusOK, gin.H{"value": d.Value, "version": d.Version})
		return
	}
	var req struct {
		Value   string `json:"value"`
		Version int    `json:"version"`
	}
	body, _ := io.ReadAll(http.MaxBytesReader(c.Writer, c.Request.Body, 1<<20))
	var probe map[string]any
	if json.Unmarshal(body, &req) != nil || json.Unmarshal([]byte(req.Value), &probe) != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "value_must_be_json"})
		return
	}
	d, err := g.Sync.PutDoc(ctx, scope, "bs_mycal", req.Version, req.Value, false, fmt.Sprintf("tg:%d", tg))
	if errors.Is(err, pg.ErrPlatformConflict) {
		cur := gin.H{"error": "conflict"}
		if d != nil {
			cur["value"], cur["version"] = d.Value, d.Version
		}
		c.JSON(http.StatusConflict, cur)
		return
	}
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "save_failed"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"ok": true, "version": d.Version})
}
