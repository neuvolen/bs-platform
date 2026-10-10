package web

import (
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"io"
	"mime"
	"net/http"
	"path"
	"strings"
	"sync"

	"github.com/gin-gonic/gin"
)

// R75 call: files of the call's background effects, served by the platform
// itself (no CDN while a call runs): MediaPipe Tasks Vision (npm
// @mediapipe/tasks-vision 0.10.21: the module, its wasm loader and the wasm,
// kept gzipped), the selfie segmentation model, bsfx.js (blur and the BS
// background on a canvas) and the BS background picture. The versioned
// folder is cached for a year; bsfx.js and the picture are revalidated.
//
//go:embed all:vendor
var vendorFS embed.FS

//go:embed call.html
var callHTML []byte

var (
	callPageOnce sync.Once
	callPage     []byte
)

// CallPage: the guest's call page (/call/<id>, callroom_r75.go), with the
// platform's own fonts.
func CallPage() []byte {
	callPageOnce.Do(func() { callPage = []byte(useOwnFonts(string(callHTML))) })
	return callPage
}

type vendorFile struct {
	plain, gz []byte
	hash      string
	ctype     string
	once      sync.Once // plain of a file kept only gzipped, made on first need
}

var (
	vendorMu    sync.Mutex
	vendorFiles = map[string]*vendorFile{}
)

var vendorTypes = map[string]string{
	".mjs": "text/javascript; charset=utf-8", ".js": "text/javascript; charset=utf-8", ".wasm": "application/wasm",
	".tflite": "application/octet-stream", ".jpg": "image/jpeg", ".txt": "text/plain; charset=utf-8",
}

func vendorGet(name string) *vendorFile {
	if name == "" || strings.Contains(name, "..") || strings.HasPrefix(name, "/") || path.Clean(name) != name {
		return nil
	}
	vendorMu.Lock()
	defer vendorMu.Unlock()
	if f, ok := vendorFiles[name]; ok {
		return f
	}
	ext := path.Ext(name)
	ct := vendorTypes[ext]
	if ct == "" {
		ct = mime.TypeByExtension(ext)
	}
	f := &vendorFile{ctype: ct}
	if b, err := vendorFS.ReadFile("vendor/" + name); err == nil {
		f.plain = b
		if strings.HasPrefix(ct, "text/") || ext == ".tflite" {
			f.gz = gzipBytes(b)
		}
		sum := sha256.Sum256(b)
		f.hash = hex.EncodeToString(sum[:8])
	} else if gz, err := vendorFS.ReadFile("vendor/" + name + ".gz"); err == nil {
		f.gz = gz
		sum := sha256.Sum256(gz)
		f.hash = hex.EncodeToString(sum[:8])
	} else {
		f = nil
	}
	vendorFiles[name] = f
	return f
}

func (f *vendorFile) plainBytes() []byte {
	f.once.Do(func() {
		if f.plain != nil || f.gz == nil {
			return
		}
		if r, err := gzip.NewReader(bytes.NewReader(f.gz)); err == nil {
			f.plain, _ = io.ReadAll(r)
		}
	})
	return f.plain
}

func serveVendor(c *gin.Context) {
	name := strings.TrimPrefix(c.Param("path"), "/")
	if strings.HasSuffix(name, ".gz") {
		c.String(http.StatusNotFound, "not found")
		return
	}
	f := vendorGet(name)
	if f == nil {
		c.String(http.StatusNotFound, "not found")
		return
	}
	cc := "public, no-cache"
	if strings.HasPrefix(name, "mp-") {
		cc = "public, max-age=31536000, immutable"
	}
	c.Writer.Header().Set("Cross-Origin-Resource-Policy", "same-origin")
	plain := f.plain
	if plain == nil && !strings.Contains(c.GetHeader("Accept-Encoding"), "gzip") {
		plain = f.plainBytes()
	}
	if plain == nil {
		plain = []byte{} // the gzipped body goes out; sendBytes needs a plain copy only for clients without gzip
	}
	sendBytes(c, f.ctype, cc, f.hash, plain, f.gz, nil)
}

// VendorNames lists the embedded vendor files (tests).
func VendorNames() []string {
	var out []string
	_ = fsWalk(vendorFS, "vendor", func(p string) { out = append(out, strings.TrimSuffix(strings.TrimPrefix(p, "vendor/"), ".gz")) })
	return out
}

func fsWalk(fs embed.FS, dir string, fn func(string)) error {
	ents, err := fs.ReadDir(dir)
	if err != nil {
		return err
	}
	for _, e := range ents {
		p := dir + "/" + e.Name()
		if e.IsDir() {
			_ = fsWalk(fs, p, fn)
			continue
		}
		fn(p)
	}
	return nil
}
