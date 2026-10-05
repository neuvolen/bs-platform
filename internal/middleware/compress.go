package middleware

import (
	"bufio"
	"compress/gzip"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"

	"github.com/andybalholm/brotli"
	"github.com/gin-gonic/gin"
)

// R45: JSON and text answers of the API go compressed: brotli when the
// browser takes it (all current browsers over https), gzip otherwise.
// The first full sync of a resident is about 450 KB of JSON; compressed it
// is a tenth of that. The answer is streamed (nothing waits for the whole
// body), small answers (under 1 KB) and binary files go as they are, and an
// answer that already has a Content-Encoding (the app's gzip, the rich
// library) is left alone.

const compressMin = 1024

var (
	brPool = sync.Pool{New: func() any { return brotli.NewWriterLevel(nil, 5) }}
	gzPool = sync.Pool{New: func() any { w, _ := gzip.NewWriterLevel(nil, 5); return w }}
)

// Compress compresses the answers of /api/ routes.
func Compress() gin.HandlerFunc {
	return func(c *gin.Context) {
		if c.Request.Method == http.MethodHead || !strings.HasPrefix(c.Request.URL.Path, "/api/") {
			c.Next()
			return
		}
		enc := ""
		ae := c.GetHeader("Accept-Encoding")
		if AcceptsEncoding(ae, "br") {
			enc = "br"
		} else if AcceptsEncoding(ae, "gzip") {
			enc = "gzip"
		}
		if enc == "" {
			c.Next()
			return
		}
		cw := &compressWriter{ResponseWriter: c.Writer, enc: enc}
		c.Writer = cw
		defer func() {
			cw.finish()
			c.Writer = cw.ResponseWriter
		}()
		c.Next()
	}
}

// AcceptsEncoding: the encoding is listed in Accept-Encoding and not refused with q=0.
func AcceptsEncoding(header, enc string) bool {
	for _, p := range strings.Split(header, ",") {
		f := strings.Split(strings.TrimSpace(p), ";")
		if !strings.EqualFold(strings.TrimSpace(f[0]), enc) {
			continue
		}
		for _, x := range f[1:] {
			x = strings.ReplaceAll(strings.TrimSpace(x), " ", "")
			if strings.HasPrefix(x, "q=") && strings.Trim(strings.TrimPrefix(x, "q="), "0.") == "" {
				return false
			}
		}
		return true
	}
	return false
}

func compressibleType(ct string) bool {
	ct = strings.ToLower(ct)
	return strings.Contains(ct, "json") || strings.HasPrefix(ct, "text/") || strings.Contains(ct, "javascript") ||
		strings.Contains(ct, "xml") || strings.Contains(ct, "svg")
}

type compressWriter struct {
	gin.ResponseWriter
	enc     string
	status  int
	buf     []byte
	decided bool
	zw      io.WriteCloser // nil: passing through
	n       int
}

func (w *compressWriter) WriteHeader(code int) {
	if code > 0 && !w.decided {
		w.status = code
	}
}

func (w *compressWriter) WriteHeaderNow() {
	// headers go out with the first bytes (finish writes them at the latest)
}

func (w *compressWriter) Status() int {
	if w.decided {
		return w.ResponseWriter.Status()
	}
	if w.status == 0 {
		return http.StatusOK
	}
	return w.status
}

func (w *compressWriter) Size() int     { return w.n }
func (w *compressWriter) Written() bool { return w.decided || w.status != 0 || len(w.buf) > 0 }

func (w *compressWriter) Write(b []byte) (int, error) {
	w.n += len(b)
	if !w.decided {
		w.buf = append(w.buf, b...)
		if len(w.buf) >= compressMin {
			w.decide(true)
		}
		return len(b), nil
	}
	if w.zw != nil {
		return w.zw.Write(b)
	}
	return w.ResponseWriter.Write(b)
}

func (w *compressWriter) WriteString(s string) (int, error) { return w.Write([]byte(s)) }

// decide sends the headers: compressed when the body is big enough (or is
// being streamed) and of a text type.
func (w *compressWriter) decide(big bool) {
	w.decided = true
	h := w.ResponseWriter.Header()
	st := w.status
	if st == 0 {
		st = http.StatusOK
	}
	ct := h.Get("Content-Type")
	if ct == "" && len(w.buf) > 0 {
		ct = http.DetectContentType(w.buf)
	}
	if big && h.Get("Content-Encoding") == "" && compressibleType(ct) && st >= 200 && st != http.StatusNoContent && st != http.StatusNotModified {
		h.Set("Content-Encoding", w.enc)
		h.Del("Content-Length")
		h.Add("Vary", "Accept-Encoding")
		if et := h.Get("ETag"); et != "" && !strings.HasPrefix(et, "W/") {
			h.Set("ETag", "W/"+et) // a different representation of the same answer
		}
		w.ResponseWriter.WriteHeader(st)
		if w.enc == "br" {
			bw := brPool.Get().(*brotli.Writer)
			bw.Reset(w.ResponseWriter)
			w.zw = bw
		} else {
			gw := gzPool.Get().(*gzip.Writer)
			gw.Reset(w.ResponseWriter)
			w.zw = gw
		}
		if len(w.buf) > 0 {
			_, _ = w.zw.Write(w.buf)
		}
	} else {
		w.ResponseWriter.WriteHeader(st)
		if len(w.buf) > 0 {
			_, _ = w.ResponseWriter.Write(w.buf)
		} else {
			w.ResponseWriter.WriteHeaderNow()
		}
	}
	w.buf = nil
}

func (w *compressWriter) finish() {
	if !w.decided {
		if w.status == 0 && len(w.buf) == 0 {
			w.decided = true // nothing was written: gin writes its own default
			return
		}
		w.decide(false)
	}
	if w.zw != nil {
		_ = w.zw.Close()
		switch z := w.zw.(type) {
		case *brotli.Writer:
			brPool.Put(z)
		case *gzip.Writer:
			gzPool.Put(z)
		}
		w.zw = nil
	}
}

func (w *compressWriter) Flush() {
	if !w.decided {
		w.decide(true)
	}
	if f, ok := w.zw.(interface{ Flush() error }); ok {
		_ = f.Flush()
	}
	w.ResponseWriter.Flush()
}

func (w *compressWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	return w.ResponseWriter.Hijack()
}
