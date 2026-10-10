package http

import (
	"context"
	"log"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/bnursik/business_surgery_backend/internal/content"
	"github.com/bnursik/business_surgery_backend/internal/tgevents"
)

// R83: the external materials of the library (isExt, a public t.me post)
// are opened once after start: a post that is gone is written to the log
// with its card, a live one with the start of its text (the team checks the
// card's description against it).

var extPostRe = regexp.MustCompile(`^https://t\.me/([A-Za-z0-9_]{4,32})/(\d+)$`)

func (h *PlatformAI) CheckExtLinks(ctx context.Context) {
	ext, err := content.LibExt()
	if err != nil {
		return
	}
	pages := map[string]tgevents.Page{}
	for _, it := range ext.Tools {
		if it["isExt"] != true {
			continue
		}
		link, _ := it["link"].(string)
		m := extPostRe.FindStringSubmatch(link)
		if m == nil {
			continue
		}
		id, _ := strconv.Atoi(m[2])
		key := m[1] + "/" + m[2]
		pg, ok := pages[key]
		if !ok {
			pg, err = tgevents.Fetch(ctx, commHTTP, m[1], id+1)
			pages[key] = pg
			time.Sleep(time.Second)
		}
		var text string
		found := false
		for _, p := range pg.Posts {
			if p.ID == id {
				found, text = true, strings.Join(strings.Fields(p.Text), " ")
			}
		}
		title, _ := it["title"].(string)
		switch {
		case err != nil:
			log.Printf("library ext link: %s (%s): не открылся: %v", link, title, err)
		case !found:
			log.Printf("library ext link: %s (%s): поста нет на странице канала", link, title)
		default:
			r := []rune(text)
			if len(r) > 360 {
				r = r[:360]
			}
			log.Printf("library ext link: %s (%s) ok: %s", link, title, string(r))
		}
		if ctx.Err() != nil {
			return
		}
	}
}
