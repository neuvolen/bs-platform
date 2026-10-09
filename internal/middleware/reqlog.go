package middleware

import (
	"fmt"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
)

// R70: the request log never carries Telegram login data (initData with its
// hash and signature), share links' signatures, board tokens, webhook secrets
// or OAuth codes. Query values are kept only for an allowlist of harmless
// parameters; every other value is written as "***". Path segments that are
// secrets by route, and any long token-like run, are masked the same way.

// logSafeQuery: parameters whose values say nothing about a person or a secret.
var logSafeQuery = map[string]bool{
	"action": true, "since": true, "limit": true, "offset": true, "off": true, "fresh": true,
	"_et": true, "year": true, "tab": true, "scope": true, "format": true, "days": true,
	"mode": true, "what": true, "status": true, "version": true, "dry": true, "full": true,
	"inline": true, "dl": true, "edit": true, "have": true, "upto": true, "own": true,
	"src": true, "categoryId": true, "organId": true, "kind": true, "page": true, "v": true,
	"date": true, "ts": true, "confirm": true, "error": true, "lang": true, "type": true,
}

// logSecretPath: route prefixes whose next N segments are secrets.
var logSecretPath = []struct {
	prefix string
	skip   int // segments after the prefix kept as they are (ids)
	mask   int // segments after those masked (-1: all the rest)
}{
	{"/b/", 0, 1},                          // client board link token
	{"/sum/", 0, 1},                        // shared summary id.sig
	{"/api/v1/platform/sum/", 0, 1},        // same, under the platform prefix
	{"/api/v1/public/sales/", 2, 1},        // /:kind/:id/:sig
	{"/api/v1/wa/open/", 1, 1},             // /:id/:sig
	{"/api/v1/wa/webhook/", 0, 1},          // /:secret
	{"/api/v1/platform/voicepipe/", 0, -1}, // nothing in it is for the log
}

// A run of 20+ token characters anywhere in the path (hex tokens, base64
// signatures, initData pasted into a path).
var logTokenRun = regexp.MustCompile(`[A-Za-z0-9_\-]{20,}`)

// RedactURL returns the path and query of a request as they may be logged.
func RedactURL(raw string) string {
	p, q, hasQ := strings.Cut(raw, "?")
	p = redactPath(p)
	if !hasQ {
		return p
	}
	return p + "?" + redactQuery(q)
}

func redactPath(p string) string {
	for _, r := range logSecretPath {
		if !strings.HasPrefix(p, r.prefix) {
			continue
		}
		segs := strings.Split(p[len(r.prefix):], "/")
		for i := range segs {
			if i < r.skip || segs[i] == "" {
				continue
			}
			if r.mask < 0 || i < r.skip+r.mask {
				ext := ""
				if strings.HasSuffix(segs[i], ".pdf") {
					ext = ".pdf"
				}
				segs[i] = "***" + ext
			}
		}
		p = r.prefix + strings.Join(segs, "/")
		break
	}
	return logTokenRun.ReplaceAllStringFunc(p, func(s string) string { return s[:4] + "***" })
}

func redactQuery(q string) string {
	parts := strings.Split(q, "&")
	for i, kv := range parts {
		k, v, hasV := strings.Cut(kv, "=")
		key, err := url.QueryUnescape(k)
		if err != nil {
			key = k
		}
		if !hasV || v == "" {
			continue
		}
		if logSafeQuery[key] && len(v) <= 64 && (key == "_et" || !logTokenRun.MatchString(v)) {
			continue // _et: the bundle's ETag, a content hash
		}
		parts[i] = k + "=***"
	}
	return strings.Join(parts, "&")
}

// RequestLog is gin's default request log line with the URL redacted.
func RequestLog() gin.HandlerFunc {
	return gin.LoggerWithConfig(gin.LoggerConfig{Formatter: func(p gin.LogFormatterParams) string {
		if p.Latency > time.Minute {
			p.Latency = p.Latency.Truncate(time.Second)
		}
		return fmt.Sprintf("[GIN] %v | %3d | %13v | %15s | %-7s %#v\n%s",
			p.TimeStamp.Format("2006/01/02 - 15:04:05"),
			p.StatusCode, p.Latency, p.ClientIP, p.Method,
			RedactURL(p.Path), p.ErrorMessage)
	}})
}
