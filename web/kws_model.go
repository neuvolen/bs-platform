package web

import (
	"archive/tar"
	"compress/bzip2"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/bnursik/business_surgery_backend/internal/datadir"
	"github.com/gin-gonic/gin"
)

// R79: the wake word model of the board's hands-free voice assistant.
//
// «Джарвис» is heard in the browser itself (web/vendor/kws-1.13.8/bskws.js,
// sherpa-onnx in WebAssembly): the small Russian streaming Zipformer of
// Vosk (alphacep, Apache-2.0, int8, about 28 MB) spots the wake word and
// writes a rough transcript, so no sound leaves the device before the wake
// word. The model is too big for the repository: the server downloads it
// once from the sherpa-onnx releases into the volume (bs-kws/) and serves it
// at /vendor/kws-model/<file>, gzipped, cached by the browser for a year.
//
//	KWS_MODEL_URL  another source of the model archive

const kwsModelName = "sherpa-onnx-streaming-zipformer-small-ru-vosk-int8-2025-08-16"

var kwsModelFiles = map[string]string{ // published name → file in the archive
	"encoder.onnx": "encoder.int8.onnx", "decoder.onnx": "decoder.onnx", "joiner.onnx": "joiner.int8.onnx", "tokens.txt": "tokens.txt",
}

var kws struct {
	sync.Mutex
	files map[string]*kwsFile
	err   error
	at    time.Time
}

type kwsFile struct {
	gz   []byte // the file gzipped (served to every browser: all accept gzip)
	raw  string // the plain file on disk, for a client without gzip
	hash string
}

func kwsDir() string { return datadir.Path("bs-kws") }

func kwsURL() string {
	if u := strings.TrimSpace(os.Getenv("KWS_MODEL_URL")); u != "" {
		return u
	}
	return "https://github.com/k2-fsa/sherpa-onnx/releases/download/asr-models/" + kwsModelName + ".tar.bz2"
}

// kwsLoad: the model's files, downloading them once. A failed download is
// tried again after a minute, not on every request.
func kwsLoad(ctx context.Context) (map[string]*kwsFile, error) {
	kws.Lock()
	defer kws.Unlock()
	if kws.files != nil {
		return kws.files, nil
	}
	if kws.err != nil && time.Since(kws.at) < time.Minute {
		return nil, kws.err
	}
	files, err := kwsPrepare(ctx)
	kws.files, kws.err, kws.at = files, err, time.Now()
	if err != nil {
		log.Printf("kws: %v", err)
	}
	return files, err
}

func kwsPrepare(ctx context.Context) (map[string]*kwsFile, error) {
	dir := filepath.Join(kwsDir(), kwsModelName)
	have := func() bool {
		for pub := range kwsModelFiles {
			if st, err := os.Stat(filepath.Join(dir, pub)); err != nil || st.Size() == 0 {
				return false
			}
		}
		return true
	}
	if !have() {
		if err := kwsDownload(ctx, dir); err != nil {
			return nil, err
		}
	}
	out := map[string]*kwsFile{}
	for pub := range kwsModelFiles {
		p := filepath.Join(dir, pub)
		gzp := p + ".gz"
		gz, err := os.ReadFile(gzp)
		if err != nil {
			b, err := os.ReadFile(p)
			if err != nil {
				return nil, err
			}
			gz = gzipBytes(b)
			_ = os.WriteFile(gzp, gz, 0o644)
		}
		sum := sha256.Sum256(gz)
		out[pub] = &kwsFile{gz: gz, raw: p, hash: hex.EncodeToString(sum[:8])}
	}
	return out, nil
}

func kwsDownload(ctx context.Context, dir string) error {
	ctx, cancel := context.WithTimeout(ctx, 4*time.Minute)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, kwsURL(), nil)
	if err != nil {
		return err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("загрузка модели «Джарвис»: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("загрузка модели «Джарвис»: %s", resp.Status)
	}
	tmp := dir + ".part"
	os.RemoveAll(tmp)
	if err := os.MkdirAll(tmp, 0o755); err != nil {
		return err
	}
	want := map[string]string{}
	for pub, src := range kwsModelFiles {
		want[src] = pub
	}
	tr := tar.NewReader(bzip2.NewReader(resp.Body))
	got := 0
	for {
		h, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return fmt.Errorf("архив модели «Джарвис»: %v", err)
		}
		pub, ok := want[filepath.Base(h.Name)]
		if !ok || h.Typeflag != tar.TypeReg {
			continue
		}
		f, err := os.Create(filepath.Join(tmp, pub))
		if err != nil {
			return err
		}
		_, err = io.Copy(f, io.LimitReader(tr, 200<<20))
		f.Close()
		if err != nil {
			return err
		}
		got++
	}
	if got != len(kwsModelFiles) {
		return fmt.Errorf("в архиве модели «Джарвис» %d файлов из %d", got, len(kwsModelFiles))
	}
	os.RemoveAll(dir)
	return os.Rename(tmp, dir)
}

// serveKWSModel: /vendor/kws-model/<file>.
func serveKWSModel(c *gin.Context, name string) {
	if _, ok := kwsModelFiles[name]; !ok {
		c.String(http.StatusNotFound, "not found")
		return
	}
	files, err := kwsLoad(c.Request.Context())
	if err != nil {
		c.Header("Retry-After", "60")
		c.String(http.StatusServiceUnavailable, "модель распознавания «Джарвис» ещё не загружена на сервер: %v", err)
		return
	}
	f := files[name]
	ct := "application/octet-stream"
	if strings.HasSuffix(name, ".txt") {
		ct = "text/plain; charset=utf-8"
	}
	c.Writer.Header().Set("Cross-Origin-Resource-Policy", "same-origin")
	if !strings.Contains(c.GetHeader("Accept-Encoding"), "gzip") {
		c.Header("Cache-Control", "public, max-age=31536000, immutable")
		c.Header("Content-Type", ct)
		c.File(f.raw)
		return
	}
	sendBytes(c, ct, "public, max-age=31536000, immutable", f.hash, []byte{}, f.gz, nil)
}

// KWSPreload: download the model in the background a while after the start,
// so the first «Слушать "Джарвис"» does not wait for it (KWS_PRELOAD=0: off).
func KWSPreload() {
	if os.Getenv("KWS_PRELOAD") == "0" || testing.Testing() {
		return
	}
	go func() {
		time.Sleep(90 * time.Second)
		if _, err := kwsLoad(context.Background()); err == nil {
			log.Printf("kws: model «Джарвис» ready in %s", kwsDir())
		}
	}()
}
