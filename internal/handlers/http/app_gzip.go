package http

import (
	"bytes"
	"compress/gzip"
	"net/http"
	"strconv"
	"strings"
	"sync"

	"github.com/gin-gonic/gin"
)

// The app's JSON answers go gzipped (R22: the app opened slowly). The bundle
// is 120 to 180 KB and the guides' catalogue 110 KB of JSON; gzipped they are
// about a fifth. Only what the browser accepts, only JSON and text, only
// above a kilobyte.

const gzipMin = 1024

var gzipPool = sync.Pool{New: func() any { w, _ := gzip.NewWriterLevel(nil, gzip.BestSpeed); return w }}

// bufferedWriter keeps the handler's body so it can be compressed at the end.
type bufferedWriter struct {
	gin.ResponseWriter
	buf    bytes.Buffer
	status int
}

func (w *bufferedWriter) WriteHeader(code int) { w.status = code }
func (w *bufferedWriter) WriteHeaderNow()      {}
func (w *bufferedWriter) Write(b []byte) (int, error) {
	return w.buf.Write(b)
}
func (w *bufferedWriter) WriteString(s string) (int, error) { return w.buf.WriteString(s) }
func (w *bufferedWriter) Status() int {
	if w.status == 0 {
		return http.StatusOK
	}
	return w.status
}
func (w *bufferedWriter) Size() int     { return w.buf.Len() }
func (w *bufferedWriter) Written() bool { return w.buf.Len() > 0 || w.status != 0 }

// appGzip compresses the answer of the handlers after it.
func appGzip(c *gin.Context) {
	if !strings.Contains(c.GetHeader("Accept-Encoding"), "gzip") || c.Request.Method == http.MethodHead {
		c.Next()
		return
	}
	orig := c.Writer
	bw := &bufferedWriter{ResponseWriter: orig}
	c.Writer = bw
	c.Next()
	c.Writer = orig

	body := bw.buf.Bytes()
	h := orig.Header()
	ct := h.Get("Content-Type")
	compressible := strings.Contains(ct, "json") || strings.HasPrefix(ct, "text/")
	h.Add("Vary", "Accept-Encoding")
	if !compressible || len(body) < gzipMin || h.Get("Content-Encoding") != "" {
		h.Set("Content-Length", strconv.Itoa(len(body)))
		orig.WriteHeader(bw.Status())
		_, _ = orig.Write(body)
		return
	}
	var out bytes.Buffer
	zw := gzipPool.Get().(*gzip.Writer)
	zw.Reset(&out)
	_, _ = zw.Write(body)
	_ = zw.Close()
	gzipPool.Put(zw)
	h.Set("Content-Encoding", "gzip")
	h.Set("Content-Length", strconv.Itoa(out.Len()))
	orig.WriteHeader(bw.Status())
	_, _ = orig.Write(out.Bytes())
}
