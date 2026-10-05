// Package web serves the BS platform page. The page is one HTML file kept in
// this folder and embedded into the server binary, so every push ships the
// platform and the API together.
//
// Two rules keep club data private:
//   - the platform itself is served only with a valid session cookie; anyone
//     else gets the small login page;
//   - business data that used to be baked into the page (P&L, residents'
//     payments and debts, fines, schedule, NPS) is cut out when the server
//     starts. It is handed to the storage as the "bs_seed" section instead,
//     where the team sees all of it and a resident only their own part.
package web

import (
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"encoding/json"
	"log"
	"mime"
	"net/http"
	"net/url"
	"path"
	"regexp"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/golang-jwt/jwt/v5"
)

//go:embed platform.html
var platformHTML []byte

//go:embed login.html
var loginHTML []byte

// Files the library hands out as they are (Excel plans, PDF strategies):
// web/dl/<name> is served at /dl/<name> to anyone signed in to the platform.
//
//go:embed all:dl
var dlFS embed.FS

var dlTypes = map[string]string{
	".xlsx": "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet",
	".xls":  "application/vnd.ms-excel",
	".docx": "application/vnd.openxmlformats-officedocument.wordprocessingml.document",
	".pptx": "application/vnd.openxmlformats-officedocument.presentationml.presentation",
	".pdf":  "application/pdf",
	".csv":  "text/csv; charset=utf-8",
	".zip":  "application/zip",
}

// DLFile returns one downloadable file by name (no folders, no hidden files).
func DLFile(name string) ([]byte, string, bool) {
	if name == "" || strings.HasPrefix(name, ".") || strings.ContainsAny(name, "/\\") || path.Clean(name) != name {
		return nil, "", false
	}
	b, err := dlFS.ReadFile("dl/" + name)
	if err != nil {
		return nil, "", false
	}
	ext := strings.ToLower(path.Ext(name))
	ct := dlTypes[ext]
	if ct == "" {
		ct = mime.TypeByExtension(ext)
	}
	if ct == "" {
		ct = "application/octet-stream"
	}
	return b, ct, true
}

func serveDL(c *gin.Context, secret []byte, cookie string) {
	name := c.Param("name")
	b, ct, ok := DLFile(name)
	if !ok {
		c.String(http.StatusNotFound, "Файл не найден")
		return
	}
	ok, role := session(c, secret, cookie)
	if !ok {
		c.String(http.StatusUnauthorized, "Войдите в платформу Business Surgery, чтобы скачать файл")
		return
	}
	if role == "lead" {
		c.String(http.StatusForbidden, "Файлы библиотеки доступны резидентам Business Surgery")
		return
	}
	sum := sha256.Sum256(b)
	etag := `"dl-` + hex.EncodeToString(sum[:8]) + `"`
	h := c.Writer.Header()
	disp := "attachment"
	if c.Query("inline") == "1" {
		disp = "inline"
	}
	h.Set("Content-Disposition", disp+`; filename="`+name+`"; filename*=UTF-8''`+url.PathEscape(name))
	h.Set("ETag", etag)
	h.Set("Cache-Control", "private, max-age=600")
	h.Set("X-Content-Type-Options", "nosniff")
	if m := c.GetHeader("If-None-Match"); m != "" && strings.Contains(m, etag) {
		c.Status(http.StatusNotModified)
		return
	}
	c.Data(http.StatusOK, ct, b)
}

// Marks the page as served by the server: the page then logs in and keeps
// its data on the server instead of only in this browser.
const serverMarker = `<script>window.BS_SERVER=1</script>`

// Variables whose values are business data, not code.
var seedVars = []string{"PL_ROWS", "RESIDENTS", "FINES", "SDATA"}

type page struct {
	plain []byte
	gz    []byte
	etag  string
}

var (
	appPage   page
	loginPage page
	seedJSON  string
)

func init() {
	html, seed := stripSeed(string(platformHTML))
	seedJSON = seed
	appHTML = html
	appPage = build(injectMarker(injectVoice(html)))
	loginPage = build(loginWithIcons(string(loginHTML), html))
}

// stripSeed replaces `var NAME = <json>;` lines with empty values and returns
// the removed values as one JSON object. A value that cannot be read is still
// removed from the page: nothing leaks even if the seed is lost.
func stripSeed(html string) (string, string) {
	seed := map[string]json.RawMessage{}
	for _, name := range seedVars {
		re := regexp.MustCompile(`(?m)^var ` + name + ` = (.*);[ \t]*\r?$`)
		m := re.FindStringSubmatchIndex(html)
		if m == nil {
			continue
		}
		raw := html[m[2]:m[3]]
		empty := "[]"
		if strings.HasPrefix(strings.TrimSpace(raw), "{") {
			empty = "{}"
		}
		if json.Valid([]byte(raw)) {
			seed[name] = json.RawMessage(raw)
		} else {
			log.Printf("web: %s is not plain JSON, removed from the page without seeding", name)
		}
		html = html[:m[0]] + "var " + name + " = " + empty + ";" + html[m[1]:]
	}
	buf, _ := json.Marshal(seed)
	return html, string(buf)
}

func injectMarker(html string) string {
	if i := strings.Index(strings.ToLower(html), "<head>"); i >= 0 {
		return html[:i+len("<head>")] + serverMarker + html[i+len("<head>"):]
	}
	return serverMarker + html
}

// loginWithIcons reuses the favicon and the logo mark from the platform page.
func loginWithIcons(login, app string) string {
	var icons []string
	var mark string
	for _, line := range strings.Split(app, "\n") {
		t := strings.TrimSpace(line)
		if strings.HasPrefix(t, `<link rel="icon"`) || strings.HasPrefix(t, `<link rel="apple-touch-icon"`) {
			icons = append(icons, t)
			if strings.HasPrefix(t, `<link rel="apple-touch-icon"`) {
				if i := strings.Index(t, `href="`); i >= 0 {
					src := t[i+6:]
					if j := strings.IndexByte(src, '"'); j > 0 {
						mark = `<img class="lg" alt="" src="` + src[:j] + `">`
					}
				}
			}
		}
	}
	login = strings.Replace(login, "<!--ICONS-->", strings.Join(icons, "\n"), 1)
	return strings.Replace(login, "<!--LOGO-->", mark, 1)
}

func build(html string) page {
	plain := []byte(html)
	var buf bytes.Buffer
	zw, _ := gzip.NewWriterLevel(&buf, gzip.BestCompression)
	_, _ = zw.Write(plain)
	_ = zw.Close()
	sum := sha256.Sum256(plain)
	return page{plain: plain, gz: buf.Bytes(), etag: `"` + hex.EncodeToString(sum[:8]) + `"`}
}

// Seed returns the business data cut out of the page, as JSON.
func Seed() string { return seedJSON }

func serve(c *gin.Context, p page) {
	h := c.Writer.Header()
	h.Set("ETag", p.etag)
	h.Set("Cache-Control", "no-cache, private")
	h.Set("Vary", "Accept-Encoding, Cookie")
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("X-Robots-Tag", "noindex, nofollow")
	h.Set("Referrer-Policy", "strict-origin-when-cross-origin")
	// R38a: a stray http:// picture or script on the page is fetched over https (no "not secure" warning)
	h.Set("Content-Security-Policy", "upgrade-insecure-requests")
	if match := c.GetHeader("If-None-Match"); match != "" && strings.Contains(match, p.etag) {
		c.Status(http.StatusNotModified)
		return
	}
	if strings.Contains(c.GetHeader("Accept-Encoding"), "gzip") {
		h.Set("Content-Encoding", "gzip")
		c.Data(http.StatusOK, "text/html; charset=utf-8", p.gz)
		return
	}
	c.Data(http.StatusOK, "text/html; charset=utf-8", p.plain)
}

// validSession checks the session cookie the same way the API checks tokens.
func validSession(c *gin.Context, secret []byte, cookie string) bool {
	ok, _ := session(c, secret, cookie)
	return ok
}

// session: whether the cookie holds a valid access token, and its role
// (admin, resident or lead: a lead gets the platform page with the lead home
// only, the API decides what each role may read).
func session(c *gin.Context, secret []byte, cookie string) (bool, string) {
	raw, err := c.Cookie(cookie)
	if err != nil || raw == "" {
		return false, ""
	}
	tkn, err := jwt.Parse(raw, func(t *jwt.Token) (any, error) {
		if _, ok := t.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, jwt.ErrSignatureInvalid
		}
		return secret, nil
	})
	if err != nil || !tkn.Valid {
		return false, ""
	}
	claims, ok := tkn.Claims.(jwt.MapClaims)
	if !ok {
		return false, ""
	}
	typ, _ := claims["typ"].(string)
	role, _ := claims["role"].(string)
	return typ == "access", role
}

// Register mounts the platform at / and /platform.
func Register(r *gin.Engine, jwtSecret, sessionCookie string) {
	secret := []byte(jwtSecret)
	h := func(c *gin.Context) {
		if validSession(c, secret, sessionCookie) {
			serve(c, currentAppPage()) // R36: with the premium voice's files when it is on (voice.go)
			return
		}
		serve(c, loginPage)
	}
	r.GET("/", h)
	r.HEAD("/", h)
	r.GET("/platform", h)
	r.GET("/dl/:name", func(c *gin.Context) { serveDL(c, secret, sessionCookie) })
	r.GET("/voice/:file", serveVoice) // voice.go: the tour's recorded phrases
	r.GET("/promo/:file", servePromo) // promo.go: screens for the login page (R38a)
	r.HEAD("/promo/:file", servePromo)
	r.HEAD("/voice/:file", serveVoice)
}
