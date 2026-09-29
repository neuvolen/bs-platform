package http

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
)

// Avatars in the app are the person's Telegram profile photo, fetched by the
// bot. Only for the club (residents and the team), so the endpoint cannot be
// used to look up strangers. Kept in memory: a photo for 12 hours, "no photo"
// for 6 hours.

// AppAvatarSource knows whose photos may be shown.
type AppAvatarSource interface {
	ClubTgIDs(ctx context.Context) ([]int64, error)
}

type avatarEntry struct {
	data  []byte
	ctype string
	at    time.Time
}

const (
	avatarTTL     = 12 * time.Hour
	avatarMissTTL = 6 * time.Hour
	avatarMaxSize = 2 << 20
)

func (g *AppGateway) tgBase() string {
	if g.TGBase != "" {
		return strings.TrimRight(g.TGBase, "/")
	}
	if b := os.Getenv("TELEGRAM_API_BASE"); b != "" {
		return strings.TrimRight(b, "/")
	}
	return "https://api.telegram.org"
}

func (g *AppGateway) avatarAllowed(ctx context.Context, id int64) bool {
	if _, ok := g.Admins[id]; ok {
		return true
	}
	if g.Avatars == nil {
		return false
	}
	g.mu.Lock()
	fresh := g.avIDs != nil && g.now().Sub(g.avIDsAt) < 10*time.Minute
	ok := g.avIDs[id]
	g.mu.Unlock()
	if fresh {
		return ok
	}
	ids, err := g.Avatars.ClubTgIDs(ctx)
	if err != nil {
		return ok
	}
	m := map[int64]bool{}
	for _, x := range ids {
		m[x] = true
	}
	g.mu.Lock()
	g.avIDs, g.avIDsAt = m, g.now()
	g.mu.Unlock()
	return m[id]
}

// fetchAvatar asks Telegram for the person's current profile photo.
func (g *AppGateway) fetchAvatar(ctx context.Context, id int64) ([]byte, string, error) {
	base := g.tgBase() + "/bot" + g.token
	get := func(u string, out any) error {
		req, _ := http.NewRequestWithContext(ctx, "GET", u, nil)
		res, err := g.client.Do(req)
		if err != nil {
			return err
		}
		defer res.Body.Close()
		return json.NewDecoder(io.LimitReader(res.Body, 1<<20)).Decode(out)
	}
	var photos struct {
		OK     bool `json:"ok"`
		Result struct {
			Photos [][]struct {
				FileID string `json:"file_id"`
				Width  int    `json:"width"`
			} `json:"photos"`
		} `json:"result"`
	}
	if err := get(fmt.Sprintf("%s/getUserProfilePhotos?user_id=%d&limit=1", base, id), &photos); err != nil {
		return nil, "", err
	}
	if !photos.OK || len(photos.Result.Photos) == 0 || len(photos.Result.Photos[0]) == 0 {
		return nil, "", nil
	}
	sizes := photos.Result.Photos[0]
	pick := sizes[len(sizes)-1]
	for _, s := range sizes { // the smallest that is still sharp on a phone
		if s.Width >= 160 {
			pick = s
			break
		}
	}
	var file struct {
		OK     bool `json:"ok"`
		Result struct {
			FilePath string `json:"file_path"`
		} `json:"result"`
	}
	if err := get(base+"/getFile?file_id="+pick.FileID, &file); err != nil {
		return nil, "", err
	}
	if !file.OK || file.Result.FilePath == "" {
		return nil, "", nil
	}
	req, _ := http.NewRequestWithContext(ctx, "GET", g.tgBase()+"/file/bot"+g.token+"/"+file.Result.FilePath, nil)
	res, err := g.client.Do(req)
	if err != nil {
		return nil, "", err
	}
	defer res.Body.Close()
	if res.StatusCode != 200 {
		return nil, "", fmt.Errorf("telegram file: %d", res.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(res.Body, avatarMaxSize))
	if err != nil {
		return nil, "", err
	}
	ct := res.Header.Get("Content-Type")
	if !strings.HasPrefix(ct, "image/") {
		ct = "image/jpeg"
	}
	return data, ct, nil
}

// Avatar: GET /api/v1/app/avatar/:id
func (g *AppGateway) Avatar(c *gin.Context) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || id <= 0 || g.token == "" {
		c.Status(http.StatusNotFound)
		return
	}
	ctx := c.Request.Context()
	if !g.avatarAllowed(ctx, id) {
		c.Status(http.StatusNotFound)
		return
	}
	g.mu.Lock()
	if g.avatars == nil {
		g.avatars = map[int64]*avatarEntry{}
	}
	e := g.avatars[id]
	g.mu.Unlock()
	now := g.now()
	if e == nil || (e.data != nil && now.Sub(e.at) > avatarTTL) || (e.data == nil && now.Sub(e.at) > avatarMissTTL) {
		fc, cancel := context.WithTimeout(ctx, 15*time.Second)
		data, ct, err := g.fetchAvatar(fc, id)
		cancel()
		if err != nil && e != nil {
			// Telegram did not answer: the old photo is better than none
		} else {
			e = &avatarEntry{data: data, ctype: ct, at: now}
			g.mu.Lock()
			g.avatars[id] = e
			g.mu.Unlock()
		}
	}
	if e == nil || e.data == nil {
		c.Header("Cache-Control", "public, max-age=3600")
		c.Status(http.StatusNotFound)
		return
	}
	c.Header("Cache-Control", "public, max-age=43200")
	c.Data(http.StatusOK, e.ctype, e.data)
}
