// Package web serves the BS platform page itself. The page is one HTML file
// kept in this folder; it is embedded into the server binary, so every push
// to the repository ships the platform and the API together.
package web

import (
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
)

//go:embed platform.html
var platformHTML []byte

// Marks the page as served by the server: the page then logs in and keeps
// its data on the server instead of only in this browser.
const serverMarker = `<script>window.BS_SERVER=1</script>`

type page struct {
	plain []byte
	gz    []byte
	etag  string
}

var platformPage = build(platformHTML)

func build(src []byte) page {
	html := string(src)
	if i := strings.Index(strings.ToLower(html), "<head>"); i >= 0 {
		html = html[:i+len("<head>")] + serverMarker + html[i+len("<head>"):]
	} else {
		html = serverMarker + html
	}
	plain := []byte(html)

	var buf bytes.Buffer
	zw, _ := gzip.NewWriterLevel(&buf, gzip.BestCompression)
	_, _ = zw.Write(plain)
	_ = zw.Close()

	sum := sha256.Sum256(plain)
	return page{plain: plain, gz: buf.Bytes(), etag: `"` + hex.EncodeToString(sum[:8]) + `"`}
}

// Platform serves the page: gzip when the browser accepts it, and an ETag so
// a repeat visit costs a 304 until the next deploy changes the page.
func Platform(c *gin.Context) {
	p := platformPage
	h := c.Writer.Header()
	h.Set("ETag", p.etag)
	h.Set("Cache-Control", "no-cache")
	h.Set("Vary", "Accept-Encoding")
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("Referrer-Policy", "strict-origin-when-cross-origin")
	if match := c.GetHeader("If-None-Match"); match != "" && strings.Contains(match, p.etag) {
		c.Status(http.StatusNotModified)
		return
	}
	if strings.Contains(c.GetHeader("Accept-Encoding"), "gzip") {
		h.Set("Content-Encoding", "gzip")
		c.Data(http.StatusOK, "text/html; charset=utf-8", p.gz)
		return
	}
	c.Data(http.StatusOK, "text/html; charset=utf-8", p.plain)
}

// Register mounts the page at / and /platform.
func Register(r *gin.Engine) {
	r.GET("/", Platform)
	r.HEAD("/", Platform)
	r.GET("/platform", Platform)
}
