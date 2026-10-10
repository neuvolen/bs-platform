package web

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"net/http"
	"regexp"
	"sync"

	"github.com/gin-gonic/gin"
)

// R83: one icon on every page and file the server opens.
//
// The platform page carries its icon inline (a 64 px PNG and a 180 px Apple
// touch icon). Every other page (the public site, the client's board /b/,
// the call, the assistant's pages, Google Calendar setup) and every file
// opened in a tab (a PDF has no icon of its own: the browser asks the site
// for /favicon.ico) gets the same picture from these addresses:
//
//	/favicon.ico                   ICO with the 64 px PNG inside
//	/favicon.png                   64 px PNG
//	/apple-touch-icon.png          180 px PNG (also -precomposed)
//	/site.webmanifest              name and icons for «Добавить на экран»

// IconLinks: the <head> lines that point a page to the platform icon.
const IconLinks = `<link rel="icon" href="/favicon.ico" sizes="any"><link rel="icon" type="image/png" sizes="64x64" href="/favicon.png"><link rel="apple-touch-icon" href="/apple-touch-icon.png"><link rel="manifest" href="/site.webmanifest">`

type iconFile struct {
	b    []byte
	ct   string
	etag string
}

var (
	iconsOnce sync.Once
	icons     map[string]*iconFile
)

var iconRe = regexp.MustCompile(`<link rel="(icon|apple-touch-icon)"[^>]*href="data:image/png;base64,([^"]+)"`)

// platformIcons: the icons cut out of the platform page.
func platformIcons() map[string]*iconFile {
	iconsOnce.Do(func() {
		icons = map[string]*iconFile{}
		var fav, apple []byte
		for _, m := range iconRe.FindAllSubmatch(platformHTML, 4) {
			b, err := base64.StdEncoding.DecodeString(string(m[2]))
			if err != nil || len(b) == 0 {
				continue
			}
			if string(m[1]) == "icon" && fav == nil {
				fav = b
			} else if string(m[1]) == "apple-touch-icon" && apple == nil {
				apple = b
			}
		}
		if apple == nil {
			apple = fav
		}
		add := func(name, ct string, b []byte) {
			if b == nil {
				return
			}
			sum := sha256.Sum256(b)
			icons[name] = &iconFile{b: b, ct: ct, etag: `"ic-` + hex.EncodeToString(sum[:6]) + `"`}
		}
		add("favicon.png", "image/png", fav)
		add("favicon.ico", "image/x-icon", icoFromPNG(fav, 64))
		add("apple-touch-icon.png", "image/png", apple)
		add("site.webmanifest", "application/manifest+json", []byte(`{"name":"Business Surgery","short_name":"BS","start_url":"/","display":"browser","background_color":"#050505","theme_color":"#050505",`+
			`"icons":[{"src":"/favicon.png","sizes":"64x64","type":"image/png"},{"src":"/apple-touch-icon.png","sizes":"180x180","type":"image/png"}]}`))
	})
	return icons
}

// icoFromPNG: an .ico file with one PNG image inside (every browser reads it).
func icoFromPNG(png []byte, size int) []byte {
	if png == nil {
		return nil
	}
	var b bytes.Buffer
	w := func(v any) { _ = binary.Write(&b, binary.LittleEndian, v) }
	w(uint16(0)) // reserved
	w(uint16(1)) // type: icon
	w(uint16(1)) // one image
	d := uint8(size)
	if size >= 256 {
		d = 0
	}
	b.WriteByte(d) // width
	b.WriteByte(d) // height
	b.WriteByte(0) // palette
	b.WriteByte(0) // reserved
	w(uint16(1))   // planes
	w(uint16(32))  // bits per pixel
	w(uint32(len(png)))
	w(uint32(6 + 16)) // offset of the image
	b.Write(png)
	return b.Bytes()
}

func serveIcon(name string) gin.HandlerFunc {
	return func(c *gin.Context) {
		f := platformIcons()[name]
		if f == nil {
			c.Status(http.StatusNotFound)
			return
		}
		h := c.Writer.Header()
		h.Set("Cache-Control", "public, max-age=86400")
		h.Set("ETag", f.etag)
		if m := c.GetHeader("If-None-Match"); m == f.etag {
			c.Status(http.StatusNotModified)
			return
		}
		c.Data(http.StatusOK, f.ct, f.b)
	}
}

func registerIcons(r *gin.Engine) {
	for path, name := range map[string]string{
		"/favicon.ico": "favicon.ico", "/favicon.png": "favicon.png",
		"/apple-touch-icon.png": "apple-touch-icon.png", "/apple-touch-icon-precomposed.png": "apple-touch-icon.png",
		"/site.webmanifest": "site.webmanifest",
	} {
		r.GET(path, serveIcon(name))
		r.HEAD(path, serveIcon(name))
	}
}
