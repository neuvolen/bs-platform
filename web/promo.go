package web

import (
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"net/http"
	"path"
	"strings"

	"github.com/gin-gonic/gin"
)

// R38a: real platform screens (demo data, made-up businesses) for the login
// page's showcase. Public: they hold no club data. web/promo/<name>.webp is
// served at /promo/<name>.webp.
//
//go:embed promo/*.webp
var promoFS embed.FS

// PromoFile returns one showcase picture by name (no folders, no hidden files).
func PromoFile(name string) ([]byte, bool) {
	if name == "" || strings.HasPrefix(name, ".") || strings.ContainsAny(name, "/\\") || path.Clean(name) != name || path.Ext(name) != ".webp" {
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
	if m := c.GetHeader("If-None-Match"); m != "" && strings.Contains(m, tag) {
		c.Status(http.StatusNotModified)
		return
	}
	c.Data(http.StatusOK, "image/webp", b)
}
