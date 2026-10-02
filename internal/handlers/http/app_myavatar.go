package http

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sync"
	"time"
)

// myAvatar is the bundle's myAvatar, as the script's _getUserAvatarBase64
// makes it: the caller's Telegram photo (the largest size) as a data URL, ""
// without a photo. ok is false when the server cannot tell (no bot token);
// the script's value is kept then. Kept for 6 hours, "no photo" for an hour,
// a failed call for 5 minutes.
func (g *AppGateway) myAvatar(ctx context.Context, id int64) (string, bool) {
	if id <= 0 {
		return "", true
	}
	if g.token == "" {
		return "", false
	}
	key := fmt.Sprintf("%p:%d", g, id)
	myAvatars.mu.Lock()
	e, ok := myAvatars.m[key]
	myAvatars.mu.Unlock()
	if ok && g.now().Before(e.until) {
		return e.data, true
	}
	fc, cancel := context.WithTimeout(ctx, 4*time.Second)
	data, ttl := g.fetchAvatarB64(fc, id)
	cancel()
	myAvatars.mu.Lock()
	if myAvatars.m == nil {
		myAvatars.m = map[string]myAvatarEntry{}
	}
	myAvatars.m[key] = myAvatarEntry{data: data, until: g.now().Add(ttl)}
	myAvatars.mu.Unlock()
	return data, true
}

type myAvatarEntry struct {
	data  string
	until time.Time
}

var myAvatars struct {
	mu sync.Mutex
	m  map[string]myAvatarEntry
}

func (g *AppGateway) fetchAvatarB64(ctx context.Context, id int64) (string, time.Duration) {
	base := g.tgBase() + "/bot" + g.token
	get := func(u string, out any) error {
		req, err := http.NewRequestWithContext(ctx, "GET", u, nil)
		if err != nil {
			return err
		}
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
			} `json:"photos"`
		} `json:"result"`
	}
	if err := get(fmt.Sprintf("%s/getUserProfilePhotos?user_id=%d&limit=1", base, id), &photos); err != nil {
		return "", 5 * time.Minute
	}
	if !photos.OK || len(photos.Result.Photos) == 0 || len(photos.Result.Photos[0]) == 0 {
		return "", time.Hour
	}
	sizes := photos.Result.Photos[0]
	var file struct {
		OK     bool `json:"ok"`
		Result struct {
			FilePath string `json:"file_path"`
		} `json:"result"`
	}
	if err := get(base+"/getFile?file_id="+sizes[len(sizes)-1].FileID, &file); err != nil {
		return "", 5 * time.Minute
	}
	if !file.OK || file.Result.FilePath == "" {
		return "", time.Hour
	}
	req, err := http.NewRequestWithContext(ctx, "GET", g.tgBase()+"/file/bot"+g.token+"/"+file.Result.FilePath, nil)
	if err != nil {
		return "", 5 * time.Minute
	}
	res, err := g.client.Do(req)
	if err != nil {
		return "", 5 * time.Minute
	}
	defer res.Body.Close()
	if res.StatusCode != 200 {
		return "", 5 * time.Minute
	}
	b, err := io.ReadAll(io.LimitReader(res.Body, avatarMaxSize))
	if err != nil {
		return "", 5 * time.Minute
	}
	return "data:image/jpeg;base64," + base64.StdEncoding.EncodeToString(b), 6 * time.Hour
}
