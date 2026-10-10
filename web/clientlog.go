package web

import (
	"io"
	"log"
	"net/http"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/gin-gonic/gin"
)

// R83: the page's own reports in the server log ("clientlog: …"): the voice
// assistant (did the microphone give sound, how loud, was the wake word
// heard; never the sound or the words of a command) and script errors.
//
//	POST /clog   a short JSON or text body (navigator.sendBeacon)

const clogMax = 2048

var clogRate struct {
	sync.Mutex
	at   time.Time
	byIP map[string]int
}

// clogAllow: at most 40 reports a minute from one address.
func clogAllow(ip string) bool {
	clogRate.Lock()
	defer clogRate.Unlock()
	if time.Since(clogRate.at) > time.Minute || clogRate.byIP == nil {
		clogRate.at, clogRate.byIP = time.Now(), map[string]int{}
	}
	clogRate.byIP[ip]++
	return clogRate.byIP[ip] <= 40
}

// clogClean: one line, printable, short.
func clogClean(s string, n int) string {
	s = strings.Map(func(r rune) rune {
		if r == '\n' || r == '\r' || r == '\t' {
			return ' '
		}
		if !unicode.IsPrint(r) {
			return -1
		}
		return r
	}, s)
	if r := []rune(s); len(r) > n {
		s = string(r[:n]) + "…"
	}
	return s
}

func serveClientLog(c *gin.Context) {
	if !clogAllow(c.ClientIP()) {
		c.Status(http.StatusTooManyRequests)
		return
	}
	b, _ := io.ReadAll(io.LimitReader(c.Request.Body, clogMax))
	if len(b) == 0 {
		c.Status(http.StatusNoContent)
		return
	}
	ua := c.GetHeader("User-Agent")
	if i := strings.Index(ua, ") "); i > 0 && len(ua) > 120 {
		ua = ua[i+2:] // the browser and its version, not the long platform part
	}
	log.Printf("clientlog: ip=%s ua=%q %s", c.ClientIP(), clogClean(ua, 80), clogClean(string(b), 1500))
	c.Status(http.StatusNoContent)
}
