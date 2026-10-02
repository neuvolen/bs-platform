package http

import (
	"archive/zip"
	"bytes"
	"compress/gzip"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/bnursik/business_surgery_backend/internal/content"
	"github.com/gin-gonic/gin"
)

func richRouter() *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.GET("/rich", LibraryRich)
	r.GET("/tpl/:file", LibraryTemplate)
	r.GET("/t/:file", PublicTemplate)
	r.GET("/zip", LibraryTemplatesZip)
	return r
}

// The rich library: every library item has a card, cures point to tools,
// the answer is gzipped and cached by ETag.
func TestLibraryRich(t *testing.T) {
	r := richRouter()
	req := httptest.NewRequest(http.MethodGet, "/rich", nil)
	req.Header.Set("Accept-Encoding", "gzip")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != 200 || w.Header().Get("Content-Encoding") != "gzip" || w.Header().Get("ETag") == "" {
		t.Fatalf("%d %v", w.Code, w.Header())
	}
	zr, err := gzip.NewReader(w.Body)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := io.ReadAll(zr)
	var lib struct {
		Version string           `json:"version"`
		Tools   []map[string]any `json:"tools"`
		Diag    []map[string]any `json:"diag"`
	}
	if err := json.Unmarshal(raw, &lib); err != nil {
		t.Fatal(err)
	}
	if len(lib.Tools) < 110 || len(lib.Diag) < 72 || lib.Version == "" {
		t.Fatalf("tools %d diag %d", len(lib.Tools), len(lib.Diag))
	}
	if strings.Contains(string(raw), "—") {
		t.Error("em dash in the library")
	}
	titles := map[string]bool{}
	ids := map[string]bool{}
	for _, x := range lib.Tools {
		titles[x["title"].(string)] = true
		id := x["id"].(string)
		if ids[id] {
			t.Errorf("repeated id %s", id)
		}
		ids[id] = true
		if x["template"] == nil || x["steps"] == nil || x["hero"] == nil {
			t.Errorf("%s: card incomplete", id)
		}
	}
	for _, d := range lib.Diag {
		test, _ := d["test"].(map[string]any)
		if qs, _ := test["questions"].([]any); len(qs) < 6 {
			t.Errorf("%v: test", d["id"])
		}
		for _, c := range d["cure"].([]any) {
			if !titles[c.(string)] {
				t.Errorf("%v: cure %q is not a tool", d["id"], c)
			}
		}
	}
	// the library's own new tool carries its file
	fp := content.RichToolByID("tl_fin_plan")
	if fp == nil {
		t.Fatal("no tl_fin_plan")
	}
	req = httptest.NewRequest(http.MethodGet, "/rich", nil)
	req.Header.Set("If-None-Match", w.Header().Get("ETag"))
	w2 := httptest.NewRecorder()
	r.ServeHTTP(w2, req)
	if w2.Code != http.StatusNotModified {
		t.Fatalf("etag: %d", w2.Code)
	}
}

// Templates: a PDF with a readable file name, 404 for unknown ids.
func TestLibraryTemplate(t *testing.T) {
	r := richRouter()
	for _, path := range []string{"/tpl/seed_tl_0.pdf", "/t/tl_fin_plan.pdf"} {
		w := httptest.NewRecorder()
		r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, path, nil))
		b := w.Body.Bytes()
		cd := w.Header().Get("Content-Disposition")
		if w.Code != 200 || w.Header().Get("Content-Type") != "application/pdf" || !bytes.HasPrefix(b, []byte("%PDF-")) {
			t.Fatalf("%s: %d %s", path, w.Code, w.Header().Get("Content-Type"))
		}
		if !strings.HasPrefix(cd, "attachment;") || !strings.Contains(cd, `filename="BS_`) || !strings.Contains(cd, "filename*=UTF-8''BS_") {
			t.Errorf("%s: disposition %q", path, cd)
		}
		et := w.Header().Get("ETag")
		req := httptest.NewRequest(http.MethodGet, path, nil)
		req.Header.Set("If-None-Match", et)
		w2 := httptest.NewRecorder()
		r.ServeHTTP(w2, req)
		if w2.Code != http.StatusNotModified {
			t.Errorf("%s: etag %d", path, w2.Code)
		}
	}
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/tpl/seed_tl_0.pdf?inline=1", nil))
	if !strings.HasPrefix(w.Header().Get("Content-Disposition"), "inline;") {
		t.Error("inline")
	}
	for _, path := range []string{"/tpl/nope.pdf", "/t/seed_dx_0.pdf", "/t/seed_tl_0"} {
		w := httptest.NewRecorder()
		r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, path, nil))
		if w.Code != http.StatusNotFound {
			t.Errorf("%s: %d", path, w.Code)
		}
	}
	a, u := tplFileNames("Платёжный календарь на 8 недель")
	if a != "BS_Platezhnyj_kalendar_na_8_nedel.pdf" || u != "BS_Платёжный_календарь_на_8_недель.pdf" {
		t.Errorf("names %q %q", a, u)
	}
}

// All templates in one archive, one PDF per tool, in organ folders.
func TestLibraryTemplatesZip(t *testing.T) {
	r := richRouter()
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/zip", nil))
	if w.Code != 200 || w.Header().Get("Content-Type") != "application/zip" {
		t.Fatalf("%d", w.Code)
	}
	zr, err := zip.NewReader(bytes.NewReader(w.Body.Bytes()), int64(w.Body.Len()))
	if err != nil {
		t.Fatal(err)
	}
	if len(zr.File) != len(content.RichTools()) {
		t.Fatalf("files %d", len(zr.File))
	}
	names := map[string]bool{}
	for _, f := range zr.File {
		if names[f.Name] || !strings.HasSuffix(f.Name, ".pdf") || !strings.Contains(f.Name, "/BS_") {
			t.Errorf("name %q", f.Name)
		}
		names[f.Name] = true
	}
}
