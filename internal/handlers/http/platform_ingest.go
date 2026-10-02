package http

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"io"
	"log"
	"net/http"
	"net/url"
	"regexp"
	"strings"

	"github.com/bnursik/business_surgery_backend/internal/bsfiles"
	"github.com/bnursik/business_surgery_backend/internal/content"
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
	id, added, attached, err := h.storeClubFile(c.Request.Context(), kind, name, c.GetHeader("Content-Type"), data)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"id": id, "added": added, "attached": attached})
}

// storeClubFile keeps a file once (id = content hash) and registers it:
// books in bs_bookfiles and on their card, stickers in the club pack.
func (h *PlatformAI) storeClubFile(ctx context.Context, kind, name, mt string, data []byte) (id string, added bool, attached string, err error) {
	sum := sha256.Sum256(data)
	id = hex.EncodeToString(sum[:12])
	if !h.repo.FileExists(ctx, id) {
		if mt == "" || mt == "application/octet-stream" {
			mt = http.DetectContentType(data)
		}
		if err = h.repo.PutFile(ctx, pg.PlatformFile{ID: id, Name: name, Mime: mt, Data: data}, "ingest"); err != nil {
			return "", false, "", err
		}
	}
	key := "bs_bookfiles"
	if kind == "sticker" {
		key = "bs_stickerpack_srv"
	}
	if added, err = h.appendDocList(ctx, key, ingestItem{ID: id, Name: name}); err != nil {
		return id, false, "", err
	}
	if kind == "book" {
		attached = h.attachBook(ctx, id, name)
	}
	return id, added, attached, nil
}

// LoadEmbedded puts the club files carried inside the server into storage.
// Safe to run on every start: each file is stored and registered once. Books
// are re-attached later too, when the platform first saves the book cards.
func (h *PlatformAI) LoadEmbedded(ctx context.Context, botToken string) {
	if botToken == "" || h.repo == nil {
		return
	}
	items, err := bsfiles.Manifest(botToken)
	if err != nil {
		log.Printf("bsfiles: %v", err)
		return
	}
	n := 0
	for _, it := range items {
		b, err := bsfiles.Read(botToken, it)
		if err != nil {
			log.Printf("bsfiles %s: %v", it.Name, err)
			continue
		}
		if _, added, att, err := h.storeClubFile(ctx, it.Kind, it.Name, it.Mime, b); err != nil {
			log.Printf("bsfiles %s: %v", it.Name, err)
		} else if added || att != "" {
			n++
		}
	}
	log.Printf("bsfiles: %d files, %d new", len(items), n)
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

// SeedGuides writes the catalogue of the 99 guides into the club document
// bs_guides = {"version": "...", "items": [...]} once per shipped version, and
// removes the old short checklists (bs_checklists).
func (h *PlatformAI) SeedGuides(ctx context.Context) {
	if h.repo == nil {
		return
	}
	ver := content.GuidesVersion()
	if d, err := h.repo.GetDoc(ctx, "club", "bs_checklists"); err == nil && d != nil && !d.Deleted {
		_, _ = h.repo.PutDoc(ctx, "club", "bs_checklists", d.Version, "", true, "server:content")
	}
	for try := 0; try < 3; try++ {
		base := 0
		if d, err := h.repo.GetDoc(ctx, "club", "bs_guides"); err == nil && d != nil {
			base = d.Version
			var cur struct {
				Version string `json:"version"`
			}
			if json.Unmarshal([]byte(d.Value), &cur) == nil && cur.Version == ver && !d.Deleted {
				return
			}
		}
		if _, err := h.repo.PutDoc(ctx, "club", "bs_guides", base, string(content.GuidesIndex()), false, "server:content"); err == nil {
			log.Printf("guides: version %s stored", ver)
			return
		}
	}
}

// GuideForPlatform: GET /api/v1/platform/guide/:id (team and residents).
func (h *PlatformAI) GuideForPlatform(c *gin.Context) {
	b := content.Guide(c.Param("id"))
	if b == nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "not_found"})
		return
	}
	c.Header("Cache-Control", "private, max-age=3600")
	c.Data(http.StatusOK, "application/json; charset=utf-8", b)
}
