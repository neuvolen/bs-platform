package web

import (
	"bytes"
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"net/http"
	"path"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
)

// R38a: real platform screens (demo data, made-up businesses) for the login
// page's showcase. Public: they hold no club data. web/promo/<name>.webp is
// served at /promo/<name>.webp.
//
// R73: also the lead home's video about the platform (web/promo/*.mp4, H.264
// 720×1280 with faststart; *.webm, VP9, for browsers without H.264), served
// with byte ranges so phones stream it.
//
//go:embed promo/*.webp promo/*.mp4 promo/*.webm
var promoFS embed.FS

// PromoFile returns one showcase picture by name (no folders, no hidden files).
func PromoFile(name string) ([]byte, bool) {
	if name == "" || strings.HasPrefix(name, ".") || strings.ContainsAny(name, "/\\") || path.Clean(name) != name || (path.Ext(name) != ".webp" && path.Ext(name) != ".mp4" && path.Ext(name) != ".webm") {
		return nil, false
	}
	b, err := promoFS.ReadFile("promo/" + name)
	if err != nil {
		return nil, false
	}
	return b, true
}

func servePromo(c *gin.Context) {
	b, ok := PromoFile(c.Param("file"))
	if !ok {
		c.String(http.StatusNotFound, "Не найдено")
		return
	}
	sum := sha256.Sum256(b)
	tag := `"` + hex.EncodeToString(sum[:8]) + `"`
	h := c.Writer.Header()
	h.Set("ETag", tag)
	h.Set("Cache-Control", "public, max-age=86400")
	h.Set("X-Content-Type-Options", "nosniff")
	if ext := path.Ext(c.Param("file")); ext == ".mp4" || ext == ".webm" {
		h.Set("Content-Type", "video/"+ext[1:])
		http.ServeContent(c.Writer, c.Request, c.Param("file"), time.Time{}, bytes.NewReader(b)) // Range, If-Range, If-None-Match
		return
	}
	if m := c.GetHeader("If-None-Match"); m != "" && strings.Contains(m, tag) {
		c.Status(http.StatusNotModified)
		return
	}
	c.Data(http.StatusOK, "image/webp", b)
}
