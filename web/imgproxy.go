package web

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"syscall"
	"time"

	"github.com/gin-gonic/gin"
)

// R52: «Не защищено» on app.bxclub.kz with a valid certificate is mixed
// content: an http:// picture that came with the data (an avatar from the
// sheet, an event cover, a link pasted into a card). The page asks the
// browser to upgrade such addresses (Content-Security-Policy), but a host
// without https then shows nothing. The page sends those pictures here
// instead: GET /img?u=http://… fetches the picture on the server and gives it
// back over https.
//
// Only for a signed-in session (not an open proxy), only http(s) addresses on
// public hosts (no loopback, private or link-local addresses: checked on the
// connection itself, so a redirect or DNS trick does not reach the inside),
// only raster pictures (png, jpeg, gif, webp, avif, ico, bmp: no svg, it can
// carry script), at most imgMax bytes, imgTimeout in all.

const (
	imgMax     = 6 << 20
	imgTimeout = 10 * time.Second
)

var imgTypes = map[string]bool{
	"image/png": true, "image/jpeg": true, "image/jpg": true, "image/gif": true, "image/webp": true,
	"image/avif": true, "image/x-icon": true, "image/vnd.microsoft.icon": true, "image/bmp": true,
}

var errImgPrivate = errors.New("private address")

// imgPrivateOK: the browser tests serve their http pictures from 127.0.0.1.
func imgPrivateOK() bool { return os.Getenv("IMG_PROXY_PRIVATE_OK") == "1" }

func imgBlockedIP(ip net.IP) bool {
	if imgPrivateOK() {
		return false
	}
	return ip == nil || ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() ||
		ip.IsMulticast() || ip.IsUnspecified() || ip.IsInterfaceLocalMulticast() ||
		(ip.To4() != nil && ip.To4()[0] == 100 && ip.To4()[1]&0xc0 == 64) // 100.64/10: carrier NAT, Railway's inside
}

var imgClient = &http.Client{
	Timeout: imgTimeout,
	Transport: &http.Transport{
		Proxy: nil, // straight to the host: the address check below is the one that counts
		DialContext: (&net.Dialer{Timeout: 5 * time.Second, Control: func(network, address string, _ syscall.RawConn) error {
			host, _, err := net.SplitHostPort(address)
			if err != nil {
				return err
			}
			if imgBlockedIP(net.ParseIP(host)) {
				return errImgPrivate
			}
			return nil
		}}).DialContext,
		TLSHandshakeTimeout:   5 * time.Second,
		ResponseHeaderTimeout: 8 * time.Second,
		MaxIdleConns:          8,
		IdleConnTimeout:       30 * time.Second,
	},
	CheckRedirect: func(req *http.Request, via []*http.Request) error {
		if len(via) >= 3 {
			return errors.New("too many redirects")
		}
		return imgURLOK(req.URL)
	},
}

func imgURLOK(u *url.URL) error {
	if u == nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.User != nil {
		return errors.New("bad address")
	}
	h := strings.ToLower(u.Hostname())
	if !imgPrivateOK() && (h == "localhost" || strings.HasSuffix(h, ".localhost") || strings.HasSuffix(h, ".internal") || strings.HasSuffix(h, ".local")) {
		return errImgPrivate
	}
	if ip := net.ParseIP(h); ip != nil && imgBlockedIP(ip) {
		return errImgPrivate
	}
	return nil
}

func serveImg(c *gin.Context, secret []byte, cookie string) {
	h := c.Writer.Header()
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("Content-Security-Policy", "default-src 'none'; sandbox")
	if !validSession(c, secret, cookie) {
		h.Set("Cache-Control", "no-store")
		c.String(http.StatusUnauthorized, "Войдите в платформу Business Surgery")
		return
	}
	raw := c.Query("u")
	u, err := url.Parse(raw)
	if err != nil || len(raw) > 2048 || imgURLOK(u) != nil {
		h.Set("Cache-Control", "no-store")
		c.String(http.StatusBadRequest, "Адрес картинки не подходит")
		return
	}
	ctx, cancel := context.WithTimeout(c.Request.Context(), imgTimeout)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	req.Header.Set("User-Agent", "BusinessSurgery-ImageProxy/1.0 (+https://app.bxclub.kz)")
	req.Header.Set("Accept", "image/avif,image/webp,image/png,image/jpeg,image/gif,image/*;q=0.8")
	res, err := imgClient.Do(req)
	if err != nil {
		h.Set("Cache-Control", "no-store")
		c.String(http.StatusBadGateway, "Картинка не загрузилась")
		return
	}
	defer res.Body.Close()
	ct := strings.ToLower(strings.TrimSpace(strings.Split(res.Header.Get("Content-Type"), ";")[0]))
	if res.StatusCode != http.StatusOK || !imgTypes[ct] {
		h.Set("Cache-Control", "no-store")
		c.String(http.StatusBadGateway, fmt.Sprintf("Это не картинка (%d)", res.StatusCode))
		return
	}
	if res.ContentLength > imgMax {
		h.Set("Cache-Control", "no-store")
		c.String(http.StatusRequestEntityTooLarge, "Картинка больше 6 МБ")
		return
	}
	data, err := io.ReadAll(io.LimitReader(res.Body, imgMax+1))
	if err != nil || len(data) > imgMax {
		h.Set("Cache-Control", "no-store")
		c.String(http.StatusRequestEntityTooLarge, "Картинка больше 6 МБ")
		return
	}
	h.Set("Cache-Control", "private, max-age=86400")
	c.Data(http.StatusOK, ct, data)
}
