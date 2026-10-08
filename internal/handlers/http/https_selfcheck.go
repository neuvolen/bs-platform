package http

import (
	"context"
	"crypto/tls"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/bnursik/business_surgery_backend/internal/club"
	"github.com/bnursik/business_surgery_backend/internal/middleware"
)

// R62: «у меня теперь замок есть, а у других пользователей как? Нужно,
// чтобы так всегда было».
//
// Once a day (and a few minutes after every start) the server opens its own
// public address the way a visitor does, from outside:
//
//   - http://<host>/ must answer 301/308 to https://<host>/ (Railway passes
//     the scheme in X-Forwarded-Proto, middleware.HTTPS redirects);
//   - https://<host>/ must answer with Strict-Transport-Security max-age of a
//     year or more (on the custom domain), a CSP with
//     upgrade-insecure-requests, and a certificate valid 14 more days.
//
// Every run goes to the log («https self-check: ok …»). A problem reaches the
// owner through the bot once (the same problems are not repeated); when all
// is well again he hears that once too. /status shows the last result.
//
// HTTPS_SELFCHECK=off turns it off; PUBLIC_URL names the address
// (default https://app.bxclub.kz).

const (
	metaHTTPSSig  = "https_check:sig" // the problems last reported ("" = all well)
	httpsMinHSTS  = 31536000
	httpsCertDays = 14
)

// HTTPSSelfCheck: the daily check of the public address.
type HTTPSSelfCheck struct {
	Host      string // app.bxclub.kz
	HTTPBase  string // default http://<Host>
	HTTPSBase string // default https://<Host>
	Client    *http.Client
	Send      func(ctx context.Context, chatID int64, text string) error
	Owner     int64
	Meta      interface {
		GetMeta(ctx context.Context, key string) (string, error)
		SetMeta(ctx context.Context, key, value string) error
	}
	Now     func() time.Time
	NoQuiet bool

	mu   sync.Mutex
	last *HTTPSResult
}

// HTTPSResult: one run.
type HTTPSResult struct {
	At       time.Time
	Redirect string // where http:// went
	HSTS     string
	CertDays int
	Problems []string // in words, for the owner
	Codes    []string // stable: compared between runs
}

func (r HTTPSResult) OK() bool { return len(r.Problems) == 0 }

// NewHTTPSSelfCheck: the check of a public address ("https://host" or "host").
func NewHTTPSSelfCheck(public string) *HTTPSSelfCheck {
	u, err := url.Parse(middleware.HTTPSURL(public))
	if err != nil || u.Host == "" {
		return nil
	}
	return &HTTPSSelfCheck{Host: u.Host}
}

func (h *HTTPSSelfCheck) now() time.Time {
	if h.Now != nil {
		return h.Now()
	}
	return time.Now()
}

func (h *HTTPSSelfCheck) client() *http.Client {
	if h.Client != nil {
		return h.Client
	}
	return &http.Client{
		Timeout: 25 * time.Second,
		// the redirect itself is what is checked
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		Transport:     &http.Transport{Proxy: http.ProxyFromEnvironment, TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS12}, DisableKeepAlives: true},
	}
}

var hstsMaxAgeRe = regexp.MustCompile(`(?i)max-age\s*=\s*"?(\d+)`)

// Check: one run, nothing sent.
func (h *HTTPSSelfCheck) Check(ctx context.Context) HTTPSResult {
	r := HTTPSResult{At: h.now()}
	add := func(code, text string) {
		r.Codes = append(r.Codes, code)
		r.Problems = append(r.Problems, text)
	}
	plain, secure := h.HTTPBase, h.HTTPSBase
	if plain == "" {
		plain = "http://" + h.Host
	}
	if secure == "" {
		secure = "https://" + h.Host
	}
	c := h.client()
	get := func(u string) (*http.Response, error) {
		cctx, cancel := context.WithTimeout(ctx, 25*time.Second)
		defer cancel()
		req, _ := http.NewRequestWithContext(cctx, http.MethodGet, u, nil)
		req.Header.Set("User-Agent", "bs-https-selfcheck")
		resp, err := c.Do(req)
		if err == nil {
			resp.Body.Close()
		}
		return resp, err
	}

	// 1. http:// goes to https:// at once
	if resp, err := get(plain + "/"); err != nil {
		add("http_down", "http://"+h.Host+" не открывается: "+httpsErrText(err))
	} else {
		r.Redirect = resp.Header.Get("Location")
		switch {
		case resp.StatusCode != http.StatusMovedPermanently && resp.StatusCode != http.StatusPermanentRedirect:
			add("http_no_redirect", "http://"+h.Host+" не переводит на https (ответ "+strconv.Itoa(resp.StatusCode)+"): посетитель видит «Не защищено»")
		case !strings.HasPrefix(r.Redirect, "https://"+h.Host+"/"):
			add("http_bad_redirect", "http://"+h.Host+" переводит не на https://"+h.Host+": "+r.Redirect)
		}
	}

	// 2. https:// answers with HSTS and a valid certificate
	resp, err := get(secure + "/")
	if err != nil {
		add("https_down", "https://"+h.Host+" не открывается: "+httpsErrText(err))
		h.keep(r)
		return r
	}
	if resp.StatusCode >= 400 {
		add("https_status", "https://"+h.Host+" отвечает ошибкой "+strconv.Itoa(resp.StatusCode))
	}
	r.HSTS = resp.Header.Get("Strict-Transport-Security")
	if middleware.WantsHSTS(h.Host) {
		m := hstsMaxAgeRe.FindStringSubmatch(r.HSTS)
		if age, _ := strconv.Atoi(firstGroup(m)); age < httpsMinHSTS {
			add("hsts", "нет HSTS на год (заголовок Strict-Transport-Security: «"+r.HSTS+"»): браузер может открыть http")
		}
	}
	if !strings.Contains(resp.Header.Get("Content-Security-Policy"), "upgrade-insecure-requests") {
		add("csp", "страница не просит браузер грузить всё по https (нет upgrade-insecure-requests)")
	}
	if resp.TLS == nil || len(resp.TLS.PeerCertificates) == 0 {
		add("cert_none", "сертификат https не получен")
	} else {
		left := resp.TLS.PeerCertificates[0].NotAfter.Sub(h.now())
		r.CertDays = int(left.Hours() / 24)
		if left < httpsCertDays*24*time.Hour {
			add("cert_expiry", "сертификат https истекает через "+strconv.Itoa(r.CertDays)+" дн. ("+resp.TLS.PeerCertificates[0].NotAfter.In(club.Almaty).Format("02.01.2006")+"): Railway → Settings → Domains → обновить сертификат")
		}
	}
	h.keep(r)
	return r
}

func firstGroup(m []string) string {
	if len(m) > 1 {
		return m[1]
	}
	return ""
}

func httpsErrText(err error) string {
	s := err.Error()
	if r := []rune(s); len(r) > 160 {
		s = string(r[:160]) + "…"
	}
	return s
}

func (h *HTTPSSelfCheck) keep(r HTTPSResult) {
	h.mu.Lock()
	h.last = &r
	h.mu.Unlock()
}

// Last: the last run (nil before the first).
func (h *HTTPSSelfCheck) Last() *HTTPSResult {
	if h == nil {
		return nil
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.last
}

func (r HTTPSResult) sig() string {
	c := append([]string(nil), r.Codes...)
	sort.Strings(c)
	return strings.Join(c, ",")
}

// Run: check, log, tell the owner when the problems changed (once). The
// text sent is returned ("" when nothing was sent).
func (h *HTTPSSelfCheck) Run(ctx context.Context) (string, error) {
	r := h.Check(ctx)
	if r.OK() {
		log.Printf("https self-check: ok: http→%s, HSTS %q, certificate %d more days", r.Redirect, r.HSTS, r.CertDays)
	} else {
		log.Printf("https self-check: PROBLEM: %s", strings.Join(r.Problems, "; "))
	}
	if h.Meta == nil {
		return "", nil
	}
	prev, err := h.Meta.GetMeta(ctx, metaHTTPSSig)
	if err != nil {
		return "", err
	}
	sig := r.sig()
	if sig == prev {
		return "", nil
	}
	var text string
	if r.OK() {
		text = "🔒 HTTPS снова в порядке: " + h.Host + " открывается только по https, замок у всех посетителей."
	} else {
		text = "⚠️ HTTPS на " + h.Host + ": у посетителей может не быть замка.\n\n• " + strings.Join(r.Problems, "\n• ") + "\n\nПроверка повторится завтра; сообщу, когда всё будет в порядке."
	}
	if !h.NoQuiet {
		if now := h.now(); QuietUntil(now).After(now) {
			return "", nil // the night: the morning run tells
		}
	}
	if h.Send == nil || h.Owner == 0 {
		return "", h.Meta.SetMeta(ctx, metaHTTPSSig, sig)
	}
	if err := h.Send(ctx, h.Owner, text); err != nil {
		return "", fmt.Errorf("send: %w", err) // tried again at the next run
	}
	return text, h.Meta.SetMeta(ctx, metaHTTPSSig, sig)
}

// nextRun: 10:00 Almaty after t.
func nextHTTPSRun(t time.Time) time.Time {
	a := t.In(club.Almaty)
	n := time.Date(a.Year(), a.Month(), a.Day(), 10, 0, 0, 0, club.Almaty)
	if !n.After(t) {
		n = n.AddDate(0, 0, 1)
	}
	return n
}

// Loop: a run «first» after the start, then every day at 10:00 Almaty.
func (h *HTTPSSelfCheck) Loop(ctx context.Context, first time.Duration) {
	wait := first
	for {
		select {
		case <-ctx.Done():
			return
		case <-time.After(wait):
		}
		if _, err := h.Run(ctx); err != nil {
			log.Printf("https self-check: %v", err)
		}
		wait = time.Until(nextHTTPSRun(time.Now()))
	}
}

// httpsItem: the /status line of the last run.
func httpsItem(h *HTTPSSelfCheck) (CheckItem, bool) {
	r := h.Last()
	if r == nil {
		return CheckItem{}, false
	}
	it := CheckItem{Key: "https", Title: "HTTPS", Sig: r.sig()}
	if r.OK() {
		it.State, it.Text = "ok", "замок у всех: http→https, HSTS, сертификат ещё "+strconv.Itoa(r.CertDays)+" дн."
	} else {
		it.State, it.Text = "fail", strings.Join(r.Problems, "; ")
	}
	return it, true
}
