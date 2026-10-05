package http

import (
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"sync"

	"github.com/gin-gonic/gin"
)

// The app's JSON answers go gzipped (R22: the app opened slowly). The bundle
// is 120 to 180 KB and the guides' catalogue 110 KB of JSON; gzipped they are
// about a fifth. Only what the browser accepts, only JSON and text, only
// above a kilobyte.
//
// R44: every 200 answer to a GET carries an ETag. A request that names the
// same tag (If-None-Match, or _et in the query: the Mini App calls another
// origin and a custom header would cost a preflight on every open) gets 304
// and no body: the app keeps the copy it has. The bundle's "ts" (the moment it
// was built) is left out of the tag, the rest of the bytes decide.

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

// appGzip compresses the answer of the handlers after it and tags it (ETag, 304).
func appGzip(c *gin.Context) {
	if c.Request.Method == http.MethodHead {
		c.Next()
		return
	}
	zip := strings.Contains(c.GetHeader("Accept-Encoding"), "gzip")
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
	if c.Request.Method == http.MethodGet && bw.Status() == http.StatusOK && compressible && h.Get("ETag") == "" && len(body) > 0 {
		tag := appETag(c, body)
		h.Set("ETag", tag)
		if appNotModified(c, tag) {
			h.Del("Content-Type")
			h.Del("Content-Length")
			orig.WriteHeader(http.StatusNotModified)
			return
		}
	}
	if !zip || !compressible || len(body) < gzipMin || h.Get("Content-Encoding") != "" {
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

// bundleTS: the moment the bundle was built (mergeBundle), not its data.
var bundleTS = regexp.MustCompile(`"ts":\d{10,14}`)

// appETag: a weak tag (the bytes go gzipped or not) of what the answer says.
func appETag(c *gin.Context, body []byte) string {
	b := body
	if c.Query("action") == "getBotCache" {
		b = bundleTS.ReplaceAll(body, nil)
	}
	sum := sha256.Sum256(b)
	return `W/"` + hex.EncodeToString(sum[:10]) + `"`
}

func bareTag(t string) string {
	t = strings.TrimSpace(t)
	t = strings.TrimPrefix(t, "W/")
	return strings.Trim(t, `"`)
}

// appNotModified: the caller already has this answer.
func appNotModified(c *gin.Context, tag string) bool {
	want := bareTag(tag)
	if et := c.Query("_et"); et != "" && bareTag(et) == want {
		return true
	}
	for _, t := range strings.Split(c.GetHeader("If-None-Match"), ",") {
		if bt := bareTag(t); bt == want || bt == "*" {
			return true
		}
	}
	return false
}
