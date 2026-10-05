package http

import (
	"archive/zip"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"unicode"

	"github.com/andybalholm/brotli"
	"github.com/bnursik/business_surgery_backend/internal/content"
	"github.com/bnursik/business_surgery_backend/internal/middleware"
	"github.com/bnursik/business_surgery_backend/internal/tplpdf"
	"github.com/gin-gonic/gin"
)

// R25: the rich library and the branded templates of the tools.
//
//   GET /api/v1/platform/library/rich            всё содержимое (gzip, ETag)
//   GET /api/v1/platform/library/template/<id>.pdf  шаблон инструмента (вход в платформу)
//   GET /api/v1/platform/library/templates.zip   все шаблоны одним архивом
//   GET /t/<id>.pdf                               тот же шаблон по открытой ссылке
//
// The page merges the rich cards into the club's own items by id or title and
// never writes them into bs_tools / bs_diag: team edits stay where they are.

// LibraryRich serves the whole rich library.
func LibraryRich(c *gin.Context) {
	plain, gz, etag, err := content.LibRich()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "library"})
		return
	}
	h := c.Writer.Header()
	h.Set("ETag", etag)
	h.Set("Cache-Control", "private, no-cache")
	h.Set("Vary", "Accept-Encoding")
	if m := c.GetHeader("If-None-Match"); m != "" && strings.Contains(m, etag) {
		c.Status(http.StatusNotModified)
		return
	}
	ae := c.GetHeader("Accept-Encoding")
	if br := libRichBrotli(plain, etag); br != nil && middleware.AcceptsEncoding(ae, "br") {
		h.Set("Content-Encoding", "br")
		c.Data(http.StatusOK, "application/json; charset=utf-8", br)
		return
	}
	if strings.Contains(ae, "gzip") {
		h.Set("Content-Encoding", "gzip")
		c.Data(http.StatusOK, "application/json; charset=utf-8", gz)
		return
	}
	c.Data(http.StatusOK, "application/json; charset=utf-8", plain)
}

// R45: the library's brotli copy (a quarter smaller than its gzip) is made
// once per version in the background; gzip goes until it is ready.
var libRichBr struct {
	sync.Mutex
	etag, busy string
	b          []byte
}

func libRichBrotli(plain []byte, etag string) []byte {
	libRichBr.Lock()
	defer libRichBr.Unlock()
	if libRichBr.etag == etag {
		return libRichBr.b
	}
	if libRichBr.busy != etag {
		libRichBr.busy = etag
		go func() {
			var buf bytes.Buffer
			bw := brotli.NewWriterOptions(&buf, brotli.WriterOptions{Quality: 11, LGWin: 22})
			_, _ = bw.Write(plain)
			_ = bw.Close()
			libRichBr.Lock()
			if libRichBr.busy == etag {
				libRichBr.etag, libRichBr.b = etag, buf.Bytes()
			}
			libRichBr.Unlock()
		}()
	}
	return nil
}

type tplPDF struct {
	b    []byte
	etag string
}

var tplCache sync.Map // id -> tplPDF

// TemplatePDF renders (once per process) the template of one tool.
func TemplatePDF(id string) (*content.RichTool, []byte, string, error) {
	t := content.RichToolByID(id)
	if t == nil || t.Template == nil {
		return nil, nil, "", nil
	}
	if v, ok := tplCache.Load(id); ok {
		p := v.(tplPDF)
		return t, p.b, p.etag, nil
	}
	b, err := tplpdf.Render(t)
	if err != nil {
		return t, nil, "", err
	}
	sum := sha256.Sum256(b)
	p := tplPDF{b: b, etag: `"t-` + hex.EncodeToString(sum[:8]) + `"`}
	if v, loaded := tplCache.LoadOrStore(id, p); loaded {
		p = v.(tplPDF)
	}
	return t, p.b, p.etag, nil
}

var translit = map[rune]string{
	'а': "a", 'б': "b", 'в': "v", 'г': "g", 'д': "d", 'е': "e", 'ё': "e", 'ж': "zh", 'з': "z", 'и': "i", 'й': "j",
	'к': "k", 'л': "l", 'м': "m", 'н': "n", 'о': "o", 'п': "p", 'р': "r", 'с': "s", 'т': "t", 'у': "u", 'ф': "f",
	'х': "h", 'ц': "c", 'ч': "ch", 'ш': "sh", 'щ': "sch", 'ъ': "", 'ы': "y", 'ь': "", 'э': "e", 'ю': "yu", 'я': "ya",
	'қ': "k", 'ғ': "g", 'ң': "n", 'ө': "o", 'ұ': "u", 'ү': "u", 'һ': "h", 'ә': "a", 'і': "i",
}

// tplFileNames: «BS_Platyozhnyj_kalendar.pdf» for old browsers and the
// Cyrillic name for the rest.
func tplFileNames(title string) (ascii, utf string) {
	var a, u strings.Builder
	sep := false
	for _, r := range title {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			if sep && u.Len() > 0 {
				a.WriteByte('_')
				u.WriteByte('_')
			}
			sep = false
			u.WriteRune(r)
			lr := unicode.ToLower(r)
			if s, ok := translit[lr]; ok {
				if unicode.IsUpper(r) && s != "" {
					s = strings.ToUpper(s[:1]) + s[1:]
				}
				a.WriteString(s)
			} else if r < 128 {
				a.WriteRune(r)
			}
		} else {
			sep = true
		}
	}
	as, us := a.String(), u.String()
	if len(as) > 80 {
		as = as[:80]
	}
	if r := []rune(us); len(r) > 80 {
		us = string(r[:80])
	}
	return "BS_" + as + ".pdf", "BS_" + us + ".pdf"
}

func serveTemplate(c *gin.Context, cache string) {
	file := c.Param("file")
	id := strings.TrimSuffix(file, ".pdf")
	if id == file || id == "" {
		c.JSON(http.StatusNotFound, gin.H{"error": "not_found"})
		return
	}
	t, b, etag, err := TemplatePDF(id)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "render"})
		return
	}
	if t == nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "not_found"})
		return
	}
	title := t.Title
	if t.Template != nil && t.Template.Title != "" {
		title = t.Template.Title
	}
	ascii, utf := tplFileNames(title)
	disp := "attachment"
	if c.Query("inline") == "1" {
		disp = "inline"
	}
	h := c.Writer.Header()
	h.Set("Content-Disposition", disp+`; filename="`+ascii+`"; filename*=UTF-8''`+url.PathEscape(utf))
	h.Set("ETag", etag)
	h.Set("Cache-Control", cache)
	h.Set("X-Content-Type-Options", "nosniff")
	if m := c.GetHeader("If-None-Match"); m != "" && strings.Contains(m, etag) {
		c.Status(http.StatusNotModified)
		return
	}
	c.Data(http.StatusOK, "application/pdf", b)
}

// LibraryTemplate: the template for the platform (team and residents).
func LibraryTemplate(c *gin.Context) {
	serveTemplate(c, "private, max-age=3600")
}

// PublicTemplate: the same file by an open link, for sharing.
func PublicTemplate(c *gin.Context) {
	c.Header("X-Robots-Tag", "noindex")
	serveTemplate(c, "public, max-age=3600")
}

var (
	tplZipMu   sync.Mutex
	tplZipEtag string // the library version the archive was built for
	tplZip     []byte
	tplZipErr  error
)

// LibraryTemplatesZip: every template in one archive (built once per library
// version: a tool added at runtime gets into it too).
func LibraryTemplatesZip(c *gin.Context) {
	_, _, ver, _ := content.LibRich()
	tplZipMu.Lock()
	defer tplZipMu.Unlock()
	if tplZipEtag != ver || (tplZip == nil && tplZipErr == nil) {
		tplZipEtag, tplZipErr = ver, nil
		func() {
			var buf bytes.Buffer
			zw := zip.NewWriter(&buf)
			seen := map[string]int{}
			for _, t := range content.RichTools() {
				if t.Template == nil {
					continue
				}
				_, b, _, err := TemplatePDF(t.ID)
				if err != nil {
					tplZipErr = err
					return
				}
				_, name := tplFileNames(t.Template.Title)
				if n := seen[name]; n > 0 {
					name = strings.TrimSuffix(name, ".pdf") + "_" + string(rune('1'+n)) + ".pdf"
				}
				seen[name]++
				f, err := zw.CreateHeader(&zip.FileHeader{Name: t.Organ + "/" + name, Method: zip.Store, Flags: 0x800})
				if err != nil {
					tplZipErr = err
					return
				}
				_, _ = f.Write(b)
			}
			tplZipErr = zw.Close()
			tplZip = buf.Bytes()
		}()
	}
	if tplZipErr != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "zip"})
		return
	}
	c.Header("Content-Disposition", `attachment; filename="BS_shablony_instrumentov.zip"; filename*=UTF-8''`+url.PathEscape("BS_Шаблоны_инструментов.zip"))
	c.Header("Cache-Control", "private, max-age=3600")
	c.Data(http.StatusOK, "application/zip", tplZip)
}
