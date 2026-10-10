package http

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"regexp"
	"strconv"

	pg "github.com/bnursik/business_surgery_backend/internal/repository/pg"
	"github.com/gin-gonic/gin"
)

// R81: one person's settings, the same in the Telegram app and on the
// platform: the personal doc «bs_prefs» (scope user:<id>; a Telegram login is
// user tg:<telegram id>, so the app and the platform share it). A JSON object;
// "hide_events": true hides «Мероприятия» under «Расписание».
//
//	GET /api/v1/app/prefs             (Telegram initData)  → {prefs:{…}}
//	PUT /api/v1/app/prefs  {k: v}     merges the keys      → {prefs:{…}}
//	GET|PUT /api/v1/platform/club/prefs  the same with the platform's login

const prefsKey = "bs_prefs"

var prefsKeyRe = regexp.MustCompile(`^[a-z][a-z0-9_]{0,40}$`)

type prefsStore interface {
	GetDoc(ctx context.Context, scope, key string) (*pg.PlatformDoc, error)
	PutDoc(ctx context.Context, scope, key string, baseVersion int, value string, deleted bool, by string) (*pg.PlatformDoc, error)
}

func readPrefs(ctx context.Context, st prefsStore, scope string) (map[string]any, int, error) {
	d, err := st.GetDoc(ctx, scope, prefsKey)
	if err != nil {
		return nil, 0, err
	}
	out := map[string]any{}
	if d == nil {
		return out, 0, nil
	}
	if !d.Deleted {
		_ = json.Unmarshal([]byte(d.Value), &out)
		if out == nil {
			out = map[string]any{}
		}
	}
	return out, d.Version, nil
}

// mergePrefs writes the given keys over the doc (null removes one).
func mergePrefs(ctx context.Context, st prefsStore, scope, by string, in map[string]any) (map[string]any, error) {
	for try := 0; ; try++ {
		cur, ver, err := readPrefs(ctx, st, scope)
		if err != nil {
			return nil, err
		}
		for k, v := range in {
			if v == nil {
				delete(cur, k)
			} else {
				cur[k] = v
			}
		}
		b, _ := json.Marshal(cur)
		_, err = st.PutDoc(ctx, scope, prefsKey, ver, string(b), false, by)
		if errors.Is(err, pg.ErrPlatformConflict) && try < 2 {
			continue
		}
		if err != nil {
			return nil, err
		}
		return cur, nil
	}
}

func prefsBody(c *gin.Context) (map[string]any, bool) {
	var in map[string]any
	if err := json.NewDecoder(http.MaxBytesReader(c.Writer, c.Request.Body, 4096)).Decode(&in); err != nil || len(in) == 0 || len(in) > 20 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "bad_params"})
		return nil, false
	}
	for k, v := range in {
		if !prefsKeyRe.MatchString(k) {
			c.JSON(http.StatusBadRequest, gin.H{"error": "bad_params", "detail": "ключ " + k})
			return nil, false
		}
		switch x := v.(type) {
		case nil, bool, float64:
		case string:
			if len(x) > 200 {
				c.JSON(http.StatusBadRequest, gin.H{"error": "bad_params"})
				return nil, false
			}
		default:
			c.JSON(http.StatusBadRequest, gin.H{"error": "bad_params"})
			return nil, false
		}
	}
	return in, true
}

func (p *ClubPeople) prefsScopeApp(c *gin.Context) (string, bool) {
	if p.G == nil || p.Docs == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "unavailable"})
		return "", false
	}
	u, ok := p.G.identify(c, c.Query("_tg"))
	if !ok {
		return "", false
	}
	return "user:tg:" + strconv.FormatInt(u.ID, 10), true
}

func (p *ClubPeople) prefsScopePlatform(c *gin.Context) (string, bool) {
	who := platformUser(c)
	if who == "" || p.Docs == nil {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
		return "", false
	}
	return "user:" + who, true
}

func (p *ClubPeople) prefsGet(scope func(*gin.Context) (string, bool)) gin.HandlerFunc {
	return func(c *gin.Context) {
		sc, ok := scope(c)
		if !ok {
			return
		}
		pr, _, err := readPrefs(c.Request.Context(), p.Docs, sc)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "internal server error"})
			return
		}
		c.Header("Cache-Control", "private, no-store")
		c.JSON(http.StatusOK, gin.H{"prefs": pr})
	}
}

func (p *ClubPeople) prefsPut(scope func(*gin.Context) (string, bool), src string) gin.HandlerFunc {
	return func(c *gin.Context) {
		sc, ok := scope(c)
		if !ok {
			return
		}
		in, ok := prefsBody(c)
		if !ok {
			return
		}
		pr, err := mergePrefs(c.Request.Context(), p.Docs, sc, src, in)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "internal server error"})
			return
		}
		c.Header("Cache-Control", "private, no-store")
		c.JSON(http.StatusOK, gin.H{"ok": true, "prefs": pr})
	}
}
