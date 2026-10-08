package http

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/bnursik/business_surgery_backend/internal/middleware"
	"github.com/gin-gonic/gin"
)

// R62: «у меня теперь замок есть, а у других пользователей как?». The
// server opens its own address as a visitor would: http must go to https,
// https must carry HSTS for a year and a certificate valid 14 more days. A
// problem reaches the owner once, the recovery once; all well from the start
// sends nothing.
func TestR62HTTPSSelfCheck(t *testing.T) {
	gin.SetMode(gin.TestMode)
	t.Setenv("HSTS_HOSTS", "")
	const host = "app.bxclub.kz"
	var broken atomic.Bool
	r := gin.New()
	r.Use(middleware.HTTPS())
	r.GET("/", func(c *gin.Context) {
		if broken.Load() {
			c.Writer.Header().Del("Strict-Transport-Security")
		}
		c.String(200, "ok")
	})
	// Railway's edge: the visitor's host and scheme reach the app in headers
	edge := func(proto string) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
			if broken.Load() && proto == "http" {
				w.WriteHeader(200) // the redirect is lost
				return
			}
			req.Host = host
			req.Header.Set("X-Forwarded-Proto", proto)
			if broken.Load() {
				w = noHSTS{w}
			}
			r.ServeHTTP(w, req)
		})
	}
	plain := httptest.NewServer(edge("http"))
	defer plain.Close()
	secure := httptest.NewTLSServer(edge("https"))
	defer secure.Close()

	c := secure.Client()
	c.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	meta := newMemMeta()
	var sent []string
	h := NewHTTPSSelfCheck("http://" + host + "/")
	if h == nil || h.Host != host {
		t.Fatalf("self-check of PUBLIC_URL: %+v", h)
	}
	h.HTTPBase, h.HTTPSBase, h.Client, h.Meta, h.Owner, h.NoQuiet = plain.URL, secure.URL, c, meta, 42, true
	h.Send = func(_ context.Context, chat int64, text string) error {
		if chat != 42 {
			t.Fatalf("sent to %d", chat)
		}
		sent = append(sent, text)
		return nil
	}
	ctx := context.Background()

	res := h.Check(ctx)
	if !res.OK() || res.Redirect != "https://"+host+"/" || res.HSTS != middleware.HSTSValue || res.CertDays < 365 {
		t.Fatalf("healthy: %+v", res)
	}
	if txt, err := h.Run(ctx); err != nil || txt != "" || len(sent) != 0 {
		t.Fatalf("all well must send nothing: %q %v", txt, err)
	}
	if it, ok := httpsItem(h); !ok || it.State != "ok" || !strings.Contains(it.Text, "http→https") {
		t.Fatalf("/status line: %+v", it)
	}

	// the redirect and HSTS are lost: one message, not repeated
	broken.Store(true)
	txt, err := h.Run(ctx)
	if err != nil || !strings.Contains(txt, "не переводит на https") || !strings.Contains(txt, "HSTS") {
		t.Fatalf("problem message: %q %v", txt, err)
	}
	if txt, _ := h.Run(ctx); txt != "" || len(sent) != 1 {
		t.Fatalf("repeated: %q (%d sent)", txt, len(sent))
	}
	if it, _ := httpsItem(h); it.State != "fail" {
		t.Fatalf("/status line: %+v", it)
	}
	// fixed: the owner hears it once
	broken.Store(false)
	if txt, _ := h.Run(ctx); !strings.Contains(txt, "снова в порядке") || len(sent) != 2 {
		t.Fatalf("recovery: %q", txt)
	}

	// the certificate ends in 10 days
	h.Now = func() time.Time { return secure.Certificate().NotAfter.Add(-10 * 24 * time.Hour) }
	res = h.Check(ctx)
	if res.OK() || res.Codes[0] != "cert_expiry" || !strings.Contains(res.Problems[0], "через 10 дн.") {
		t.Fatalf("expiry: %+v", res)
	}
	h.Now = nil

	// Railway's own domain carries no HSTS and that is fine
	h2 := &HTTPSSelfCheck{Host: "bs-platform-production.up.railway.app", HTTPSBase: secure.URL, HTTPBase: plain.URL, Client: c}
	res = h2.Check(ctx)
	for _, code := range res.Codes {
		if code == "hsts" {
			t.Fatalf("railway domain: %+v", res)
		}
	}

	// unreachable
	h3 := &HTTPSSelfCheck{Host: host, HTTPBase: "http://127.0.0.1:1", HTTPSBase: "https://127.0.0.1:1", Client: c}
	if res := h3.Check(ctx); res.OK() || len(res.Codes) != 2 {
		t.Fatalf("down: %+v", res)
	}

	// the daily time: 10:00 Almaty
	at := time.Date(2026, 10, 8, 3, 0, 0, 0, time.UTC) // 08:00 Almaty
	if n := nextHTTPSRun(at); n.Sub(at) != 2*time.Hour {
		t.Fatalf("next run %v", n)
	}
}

type noHSTS struct{ http.ResponseWriter }

func (w noHSTS) WriteHeader(code int) {
	w.ResponseWriter.Header().Del("Strict-Transport-Security")
	w.ResponseWriter.WriteHeader(code)
}

func (w noHSTS) Write(b []byte) (int, error) {
	w.ResponseWriter.Header().Del("Strict-Transport-Security")
	return w.ResponseWriter.Write(b)
}
