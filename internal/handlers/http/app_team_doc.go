package http

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"

	pg "github.com/bnursik/business_surgery_backend/internal/repository/pg"
	"github.com/gin-gonic/gin"
)

// The team's platform sections the Telegram app shows as they are (R30: one
// source for the platform and the app). «Цели и задачи» in the app is the
// platform's board bs_kanban: a card added or closed in one place is there
// in the other at once, no copy in the script's sheet.
//
// GET  /api/v1/app/team/doc?key=bs_kanban&_tg=…  → {value, version}
// PUT  same, body {value, version}: written with the version check of the
//      platform (409 with the current value when someone wrote first).
var appTeamDocs = map[string]bool{"bs_kanban": true}

func (g *AppGateway) TeamDoc(c *gin.Context) {
	if g.Sync == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "not_configured"})
		return
	}
	key := strings.TrimSpace(c.Query("key"))
	if !appTeamDocs[key] {
		c.JSON(http.StatusBadRequest, gin.H{"error": "unknown_key"})
		return
	}
	u, ok := g.identify(c, c.Query("_tg"))
	if !ok {
		return
	}
	if _, team := g.Admins[u.ID]; !team {
		c.JSON(http.StatusForbidden, gin.H{"error": "team_only"})
		return
	}
	ctx := c.Request.Context()
	if c.Request.Method == http.MethodGet {
		d, err := g.Sync.GetDoc(ctx, "club", key)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "load_failed"})
			return
		}
		if d == nil || d.Deleted {
			c.JSON(http.StatusOK, gin.H{"value": "", "version": 0})
			return
		}
		c.JSON(http.StatusOK, gin.H{"value": d.Value, "version": d.Version})
		return
	}
	var req struct {
		Value   string `json:"value"`
		Version int    `json:"version"`
	}
	body, _ := io.ReadAll(http.MaxBytesReader(c.Writer, c.Request.Body, 4<<20))
	var probe map[string]any
	if json.Unmarshal(body, &req) != nil || json.Unmarshal([]byte(req.Value), &probe) != nil || probe == nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "value_must_be_json_object"})
		return
	}
	if key == "bs_kanban" {
		if _, ok := probe["cards"].([]any); !ok {
			c.JSON(http.StatusBadRequest, gin.H{"error": "cards_missing"})
			return
		}
	}
	d, err := g.Sync.PutDoc(ctx, "club", key, req.Version, req.Value, false, fmt.Sprintf("tg:%d", u.ID))
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
