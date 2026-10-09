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

// R59: «Мой календарь» is one per resident: the document bs_mycal in the
// resident's own scope (user:tg:<id>), the same for the app and the platform.
// Before, the team's view kept one calendar for everybody: the platform kept
// it in the club's shared bs_mycal (one document, whichever resident was
// open), and the app's admin preview read the admin's own calendar for any
// resident. Now the team reads and edits the calendar of the resident it is
// looking at.

// residentTgFinder finds an active resident's Telegram id by name.
type residentTgFinder interface {
	ResidentTgByName(ctx context.Context, name string) (int64, string, error)
}

var errNoResidentTg = errors.New("no_resident_tg")

// residentCalScope: the scope of a resident's calendar by name.
func residentCalScope(ctx context.Context, f residentTgFinder, name string) (string, error) {
	if f == nil || strings.TrimSpace(name) == "" {
		return "", errNoResidentTg
	}
	tg, _, err := f.ResidentTgByName(ctx, name)
	if err != nil {
		return "", err
	}
	if tg <= 0 {
		return "", errNoResidentTg
	}
	return fmt.Sprintf("user:tg:%d", tg), nil
}

type calDocStore interface {
	GetDoc(ctx context.Context, scope, key string) (*pg.PlatformDoc, error)
	PutDoc(ctx context.Context, scope, key string, baseVersion int, value string, deleted bool, by string) (*pg.PlatformDoc, error)
}

// serveCal answers GET (the calendar) and PUT ({value, version}) for a scope.
// owner: the viewer is the calendar's person (R71: Google titles, the button).
func serveCal(c *gin.Context, store calDocStore, scope, by string, owner bool) {
	ctx := c.Request.Context()
	if c.Request.Method == http.MethodGet {
		d, err := store.GetDoc(ctx, scope, "bs_mycal")
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "load_failed"})
			return
		}
		out := gin.H{"value": `{"slots":{}}`, "version": 0}
		if d != nil && !d.Deleted {
			out["value"], out["version"] = d.Value, d.Version
		} else if d != nil {
			out["version"] = d.Version
		}
		for k, v := range gcalCalExtra(ctx, scope, owner) {
			out[k] = v
		}
		c.JSON(http.StatusOK, out)
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
	d, err := store.PutDoc(ctx, scope, "bs_mycal", req.Version, req.Value, false, by)
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
	gcalTouch(scope)
	c.JSON(http.StatusOK, gin.H{"ok": true, "version": d.Version})
}

// ResidentCal: GET/PUT /api/v1/platform/mycal?name=<resident>. The team
// opens the calendar of the resident it looks at; a resident gets their own
// (the name is ignored).
func (h *PlatformHandler) ResidentCal(c *gin.Context) {
	scope := "user:" + platformUser(c)
	if !isResident(c) && c.Query("own") == "1" {
		// R71: a team member's own work calendar (their Google Calendar too)
		serveCal(c, h.repo, scope, platformUser(c), true)
		return
	}
	if !isResident(c) {
		s, err := residentCalScope(c.Request.Context(), h.repo, c.Query("name"))
		if err != nil {
			if errors.Is(err, errNoResidentTg) {
				c.JSON(http.StatusNotFound, gin.H{"error": "no_resident", "detail": "У резидента нет входа через Telegram: календарь появится, когда он войдёт"})
				return
			}
			c.JSON(http.StatusInternalServerError, gin.H{"error": "internal server error"})
			return
		}
		scope = s
	} else if h.residentOf(c) == "" {
		forbidden(c, "not_resident")
		return
	}
	serveCal(c, h.repo, scope, platformUser(c), isResident(c))
}
