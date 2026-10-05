package middleware

import (
	"bytes"
	"compress/gzip"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/andybalholm/brotli"
	"github.com/gin-gonic/gin"
)

// R45: the API's JSON goes compressed.

func compressRouter() *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(Compress())
	big := map[string]any{"text": strings.Repeat("Кассовый разрыв виден за 3-6 недель. ", 400)}
	r.GET("/api/big", func(c *gin.Context) { c.JSON(200, big) })
	r.GET("/api/small", func(c *gin.Context) { c.JSON(200, gin.H{"ok": true}) })
	r.GET("/api/pdf", func(c *gin.Context) { c.Data(200, "application/pdf", bytes.Repeat([]byte("%PDF-"), 1000)) })
	r.GET("/api/pre", func(c *gin.Context) {
		c.Header("Content-Encoding", "gzip")
		var b bytes.Buffer
		zw := gzip.NewWriter(&b)
		_, _ = zw.Write(bytes.Repeat([]byte(`{"a":1}`), 500))
		_ = zw.Close()
		c.Data(200, "application/json", b.Bytes())
	})
	r.GET("/api/etag", func(c *gin.Context) {
		c.Header("ETag", `"v1"`)
		if c.GetHeader("If-None-Match") != "" {
			c.Status(http.StatusNotModified)
			return
		}
		c.JSON(200, big)
	})
	r.GET("/api/stream", func(c *gin.Context) {
		c.Header("Content-Type", "text/plain")
		c.Status(200)
		_, _ = c.Writer.WriteString("first ")
		c.Writer.Flush()
		_, _ = c.Writer.WriteString(strings.Repeat("second ", 10))
	})
	r.GET("/page", func(c *gin.Context) { c.String(200, strings.Repeat("страница ", 500)) })
	r.GET("/api/none", func(c *gin.Context) {})
	return r
}

func get(r http.Handler, path, ae string, hdr ...string) *httptest.ResponseRecorder {
	w := httptest.NewRecorder()
	req := httptest.NewRequest("GET", path, nil)
	if ae != "" {
		req.Header.Set("Accept-Encoding", ae)
	}
	for i := 0; i+1 < len(hdr); i += 2 {
		req.Header.Set(hdr[i], hdr[i+1])
	}
	r.ServeHTTP(w, req)
	return w
}

func TestR45CompressJSON(t *testing.T) {
	r := compressRouter()
	plain := get(r, "/api/big", "")
	if plain.Header().Get("Content-Encoding") != "" || plain.Body.Len() < 10000 {
		t.Fatalf("no Accept-Encoding: plain answer, %d %v", plain.Body.Len(), plain.Header())
	}
	w := get(r, "/api/big", "gzip, deflate, br, zstd")
	if w.Header().Get("Content-Encoding") != "br" || w.Header().Get("Content-Length") != "" || !strings.Contains(w.Header().Get("Vary"), "Accept-Encoding") {
		t.Fatalf("br headers: %v", w.Header())
	}
	b, err := io.ReadAll(brotli.NewReader(w.Body))
	if err != nil || !bytes.Equal(b, plain.Body.Bytes()) || w.Body.Len() > plain.Body.Len()/10 {
		t.Fatalf("br body: %v, %d of %d", err, w.Body.Len(), plain.Body.Len())
	}
	w = get(r, "/api/big", "gzip")
	zr, err := gzip.NewReader(w.Body)
	if err != nil || w.Header().Get("Content-Encoding") != "gzip" {
		t.Fatalf("gzip: %v %v", err, w.Header())
	}
	if b, _ := io.ReadAll(zr); !bytes.Equal(b, plain.Body.Bytes()) {
		t.Fatalf("gzip body differs")
	}
	if w := get(r, "/api/big", "br;q=0, gzip"); w.Header().Get("Content-Encoding") != "gzip" {
		t.Fatalf("br refused with q=0: %v", w.Header())
	}
}

func TestR45CompressLeavesAlone(t *testing.T) {
	r := compressRouter()
	if w := get(r, "/api/small", "br"); w.Header().Get("Content-Encoding") != "" || w.Body.String() != `{"ok":true}` {
		t.Fatalf("small answer: %v %q", w.Header(), w.Body.String())
	}
	if w := get(r, "/api/pdf", "br"); w.Header().Get("Content-Encoding") != "" || w.Body.Len() != 5000 {
		t.Fatalf("binary answer: %v", w.Header())
	}
	if w := get(r, "/api/pre", "br, gzip"); w.Header().Get("Content-Encoding") != "gzip" {
		t.Fatalf("already gzipped: %v", w.Header())
	} else if zr, err := gzip.NewReader(w.Body); err != nil {
		t.Fatalf("double compressed: %v", err)
	} else if b, _ := io.ReadAll(zr); len(b) != 3500 {
		t.Fatalf("pre body: %d", len(b))
	}
	if w := get(r, "/page", "br"); w.Header().Get("Content-Encoding") != "" {
		t.Fatalf("not an API route: %v", w.Header())
	}
	if w := get(r, "/api/none", "br"); w.Code != 200 || w.Body.Len() != 0 {
		t.Fatalf("empty answer: %d %q", w.Code, w.Body.String())
	}
	w := get(r, "/api/etag", "br", "If-None-Match", `"v1"`)
	if w.Code != http.StatusNotModified || w.Body.Len() != 0 || w.Header().Get("Content-Encoding") != "" {
		t.Fatalf("304: %d %v", w.Code, w.Header())
	}
	if w := get(r, "/api/etag", "br"); w.Header().Get("ETag") != `W/"v1"` {
		t.Fatalf("a compressed copy gets a weak tag: %v", w.Header())
	}
}

func TestR45CompressStream(t *testing.T) {
	r := compressRouter()
	w := get(r, "/api/stream", "br")
	if w.Header().Get("Content-Encoding") != "br" || !w.Flushed {
		t.Fatalf("a flushed answer is compressed and streamed: %v %v", w.Header(), w.Flushed)
	}
	b, err := io.ReadAll(brotli.NewReader(w.Body))
	if err != nil || string(b) != "first "+strings.Repeat("second ", 10) {
		t.Fatalf("stream body: %v %q", err, b)
	}
}
