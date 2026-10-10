package http

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"log"

	"github.com/bnursik/business_surgery_backend/internal/content"
	pg "github.com/bnursik/business_surgery_backend/internal/repository/pg"
)

// R82: the meme stickers go into the club's pack (bs_stickerpack_srv) once,
// in one write, each with its category («cat»): the page groups the pack by
// it. A sticker the team hid stays hidden (bs_stickerhide is not touched).

type memePackItem struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Cat  string `json:"cat,omitempty"`
}

// LoadMemeStickers stores the pack's files and adds the missing ones to the
// club's pack.
func (h *PlatformAI) LoadMemeStickers(ctx context.Context) {
	if h.repo == nil {
		return
	}
	list, files, err := content.MemeStickers()
	if err != nil {
		log.Printf("meme stickers: %v", err)
		return
	}
	var add []memePackItem
	for _, s := range list {
		data := files[s.ID]
		sum := sha256.Sum256(data)
		id := hex.EncodeToString(sum[:12])
		if !h.repo.FileExists(ctx, id) {
			if err := h.repo.PutFile(ctx, pg.PlatformFile{ID: id, Name: "BS мем " + s.ID + ".png", Mime: "image/png", Data: data}, "r82"); err != nil {
				log.Printf("meme sticker %s: %v", s.ID, err)
				return
			}
		}
		add = append(add, memePackItem{ID: id, Name: s.Name, Cat: s.Cat})
	}
	for try := 0; try < 4; try++ {
		var cur []json.RawMessage
		base := 0
		if d, err := h.repo.GetDoc(ctx, "club", "bs_stickerpack_srv"); err == nil && d != nil {
			base = d.Version
			if !d.Deleted {
				_ = json.Unmarshal([]byte(d.Value), &cur)
			}
		}
		have := map[string]bool{}
		for _, r := range cur {
			var x struct {
				ID string `json:"id"`
			}
			_ = json.Unmarshal(r, &x)
			have[x.ID] = true
		}
		n := 0
		for _, it := range add {
			if have[it.ID] {
				continue
			}
			b, _ := json.Marshal(it)
			cur = append(cur, b)
			n++
		}
		if n == 0 {
			return
		}
		val, _ := json.Marshal(cur)
		if _, err := h.repo.PutDoc(ctx, "club", "bs_stickerpack_srv", base, string(val), false, "server:stickers"); err != nil {
			continue
		}
		log.Printf("meme stickers: +%d in the club pack (%d in the set)", n, len(list))
		return
	}
	log.Printf("meme stickers: the pack was busy, next start")
}
