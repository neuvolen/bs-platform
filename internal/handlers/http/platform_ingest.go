package http

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"

	pg "github.com/bnursik/business_surgery_backend/internal/repository/pg"
	"github.com/gin-gonic/gin"
)

// Ingest: POST /api/v1/platform/ingest
//
// The club's private file store (a private GitHub repository with books and
// stickers) pushes files here from its workflow. The key itself lives only in
// that repository's secrets; the server knows just its SHA-256.
//
//	headers: X-Ingest-Key, X-File-Name (URL-encoded), X-Kind: book | sticker
//
// Files are stored once (id = content hash); books are attached to their tool
// cards, stickers join the club sticker pack.
var ingestKeySHA256 = "51edf8bfd5c601f1af680db5d4c5fb91cec53c8d940d5444ff898d7a0c262cf4"

func ingestKeyOK(k string) bool {
	sum := sha256.Sum256([]byte(strings.TrimSpace(k)))
	want, _ := hex.DecodeString(ingestKeySHA256)
	return len(k) > 20 && subtle.ConstantTimeCompare(sum[:], want) == 1
}

type ingestItem struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	W    int    `json:"w,omitempty"`
	H    int    `json:"h,omitempty"`
}

func (h *PlatformAI) Ingest(c *gin.Context) {
	if !ingestKeyOK(c.GetHeader("X-Ingest-Key")) {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "bad_key"})
		return
	}
	kind := c.GetHeader("X-Kind")
	if kind != "book" && kind != "sticker" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "kind_must_be_book_or_sticker"})
		return
	}
	data, err := io.ReadAll(http.MaxBytesReader(c.Writer, c.Request.Body, platformFileMax))
	if err != nil || len(data) == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "file_required"})
		return
	}
	name, _ := url.QueryUnescape(c.GetHeader("X-File-Name"))
	if name = strings.TrimSpace(name); name == "" {
		name = "file"
	}
	sum := sha256.Sum256(data)
	id := hex.EncodeToString(sum[:12])
	ctx := c.Request.Context()
	if f, _ := h.repo.GetFile(ctx, id); f == nil {
		mt := c.GetHeader("Content-Type")
		if mt == "" || mt == "application/octet-stream" {
			mt = http.DetectContentType(data)
		}
		if err := h.repo.PutFile(ctx, pg.PlatformFile{ID: id, Name: name, Mime: mt, Data: data}, "ingest"); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "store_failed"})
			return
		}
	}
	key := "bs_bookfiles"
	if kind == "sticker" {
		key = "bs_stickerpack_srv"
	}
	added, err := h.appendDocList(ctx, key, ingestItem{ID: id, Name: name})
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "doc_failed"})
		return
	}
	attached := ""
	if kind == "book" {
		attached = h.attachBook(ctx, id, name)
	}
	c.JSON(http.StatusOK, gin.H{"id": id, "added": added, "attached": attached})
}

// appendDocList adds an item to a club document holding a JSON list (once).
func (h *PlatformAI) appendDocList(ctx context.Context, key string, it ingestItem) (bool, error) {
	for try := 0; try < 4; try++ {
		var list []ingestItem
		base := 0
		if d, err := h.repo.GetDoc(ctx, "club", key); err == nil && d != nil {
			base = d.Version
			if !d.Deleted {
				_ = json.Unmarshal([]byte(d.Value), &list)
			}
		}
		for _, x := range list {
			if x.ID == it.ID {
				return false, nil
			}
		}
		list = append(list, it)
		val, _ := json.Marshal(list)
		if _, err := h.repo.PutDoc(ctx, "club", key, base, string(val), false, "server:ingest"); err == nil {
			return true, nil
		}
	}
	return false, pg.ErrPlatformConflict
}

// attachBook puts the file on the book card whose match rule fits the file name.
func (h *PlatformAI) attachBook(ctx context.Context, id, name string) string {
	low := strings.ToLower(name)
	for try := 0; try < 4; try++ {
		d, err := h.repo.GetDoc(ctx, "club", "bs_tools")
		if err != nil || d == nil || d.Deleted {
			return ""
		}
		var tools []map[string]any
		if json.Unmarshal([]byte(d.Value), &tools) != nil {
			return ""
		}
		hit := ""
		for _, t := range tools {
			if b, _ := t["isBook"].(bool); !b {
				continue
			}
			m, _ := t["match"].(string)
			f, _ := t["file"].(string)
			if m == "" || f != "" {
				continue
			}
			if re, err := regexp.Compile("(?i)" + m); err == nil && re.MatchString(low) {
				t["file"], t["fileName"] = id, name
				hit, _ = t["title"].(string)
				break
			}
		}
		if hit == "" {
			return ""
		}
		val, _ := json.Marshal(tools)
		if _, err := h.repo.PutDoc(ctx, "club", "bs_tools", d.Version, string(val), false, "server:ingest"); err == nil {
			return hit
		}
	}
	return ""
}
