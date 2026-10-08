package middleware

import (
	"net"
	"net/http"
	"os"
	"strings"

	"github.com/gin-gonic/gin"
)

// HSTSValue is sent on HTTPS answers of the custom domain. No includeSubDomains:
// bxclub.kz itself lives on Tilda and must not be forced to HTTPS by this app.
const HSTSValue = "max-age=31536000"

// CSPValue: the policy on every answer (R52). Only the upgrade: the pages
// load their scripts from the CDNs they need, so nothing else is restricted.
const CSPValue = "upgrade-insecure-requests"

// HTTPS keeps the platform on https:// only.
//
// Railway ends TLS at its proxy and passes the original scheme in
// X-Forwarded-Proto. A request that came in over plain http (someone typed
// http://app.bxclub.kz or an old link) gets 301 (GET, HEAD) or 308 (other
// methods, keeps the body) to the same address on https. Requests without the
// header (Railway health checks, local runs, tests) are served as they are.
//
// HSTS is added only to HTTPS answers of the custom domain (HSTS_HOSTS, comma
// separated suffixes, default "bxclub.kz"), so the browser never tries http
// again; *.up.railway.app and localhost do not get it.
func HTTPS() gin.HandlerFunc {
	hosts := hstsHosts(os.Getenv("HSTS_HOSTS"))
	return func(c *gin.Context) {
		host := c.Request.Host
		proto := forwardedProto(c.Request)
		if proto == "http" && !isLocal(host) {
			code := http.StatusMovedPermanently
			if c.Request.Method != http.MethodGet && c.Request.Method != http.MethodHead {
				code = http.StatusPermanentRedirect
			}
			c.Redirect(code, "https://"+host+c.Request.URL.RequestURI())
			c.Abort()
			return
		}
		if (proto == "https" || c.Request.TLS != nil) && hostMatches(host, hosts) {
			c.Header("Strict-Transport-Security", HSTSValue)
		}
		// R52: every answer (the platform and login pages, the open pages
		// /about and /library, error pages, files opened in a tab) asks the
		// browser to fetch any http:// resource over https: no «Не защищено»
		// from a picture or form address that came with the data. A handler
		// with its own policy sets the header again.
		c.Header("Content-Security-Policy", CSPValue)
		c.Next()
	}
}

// WantsHSTS: whether answers for this host carry HSTS (R62: the server's
// own https self-check expects it only there).
func WantsHSTS(host string) bool { return hostMatches(host, hstsHosts(os.Getenv("HSTS_HOSTS"))) }

// forwardedProto: the first value of X-Forwarded-Proto (proxies may chain them), lower case.
func forwardedProto(r *http.Request) string {
	v := r.Header.Get("X-Forwarded-Proto")
	if i := strings.IndexByte(v, ','); i >= 0 {
		v = v[:i]
	}
	return strings.ToLower(strings.TrimSpace(v))
}

func hostOnly(host string) string {
	if h, _, err := net.SplitHostPort(host); err == nil {
		return strings.ToLower(h)
	}
	return strings.ToLower(strings.Trim(host, "[]"))
}

func isLocal(host string) bool {
	h := hostOnly(host)
	if h == "" || h == "localhost" || strings.HasSuffix(h, ".localhost") || strings.HasSuffix(h, ".internal") {
		return true
	}
	ip := net.ParseIP(h)
	return ip != nil && (ip.IsLoopback() || ip.IsPrivate())
}

func hstsHosts(spec string) []string {
	if strings.TrimSpace(spec) == "" {
		spec = "bxclub.kz"
	}
	var out []string
	for _, s := range strings.Split(spec, ",") {
		if s = strings.ToLower(strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(s), "."))); s != "" {
			out = append(out, s)
		}
	}
	return out
}

func hostMatches(host string, suffixes []string) bool {
	h := hostOnly(host)
	for _, s := range suffixes {
		if h == s || strings.HasSuffix(h, "."+s) {
			return true
		}
	}
	return false
}

// HTTPSURL turns a configured public address into an https one: "http://x" and
// "x" become "https://x"; local addresses keep http. Trailing "/" is dropped.
func HTTPSURL(u string) string {
	u = strings.TrimRight(strings.TrimSpace(u), "/")
	if u == "" {
		return ""
	}
	low := strings.ToLower(u)
	switch {
	case strings.HasPrefix(low, "https://"):
		return "https://" + u[len("https://"):]
	case strings.HasPrefix(low, "http://"):
		rest := u[len("http://"):]
		host := rest
		if i := strings.IndexAny(rest, "/?#"); i >= 0 {
			host = rest[:i]
		}
		if isLocal(host) {
			return u
		}
		return "https://" + rest
	case strings.Contains(u, "://"):
		return u
	}
	return "https://" + u
}
