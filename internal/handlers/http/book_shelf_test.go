package http

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bnursik/business_surgery_backend/internal/bookcovers"
	"github.com/bnursik/business_surgery_backend/internal/content"
	"github.com/gin-gonic/gin"
)

// The shelf answers with every book, the cover of a book that has one
// (its address on our domain, the tone of its corner), and serves the
// cover file with a year of cache; nothing else in the folder is served.
func TestBookShelfAndCovers(t *testing.T) {
	gin.SetMode(gin.TestMode)
	books, _ := content.Books()
	dir := t.TempDir()
	id := books[0].ID
	_ = os.WriteFile(filepath.Join(dir, id+".jpg"), []byte("\xff\xd8\xffjpeg"), 0o644)
	idx, _ := json.Marshal(map[string]bookcovers.Entry{id: {File: id + ".jpg", Src: "ol", W: 400, H: 600, Tone: "light", BG: "#eeeeee", Hash: "abc123", Size: 7}})
	_ = os.WriteFile(filepath.Join(dir, "index.json"), idx, 0o644)
	SetBookCovers(bookcovers.New(dir))

	r := gin.New()
	r.GET("/covers/:file", BookCoverFile)
	r.GET("/books", BookShelf)

	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/books", nil))
	var out struct {
		Books []struct {
			ID, Title, Cover, Tone string
			Diag                   []string
		}
		Cats          []string
		Covers, Total int
	}
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil || w.Code != 200 {
		t.Fatal(w.Code, err)
	}
	if out.Total != len(books) || len(out.Books) != len(books) || out.Covers != 1 || len(out.Cats) != len(content.BookCats) {
		t.Fatalf("shelf: %d books, %d covers, %d cats", len(out.Books), out.Covers, len(out.Cats))
	}
	if b := out.Books[0]; b.Cover != "/covers/"+id+".jpg?v=abc123" || b.Tone != "light" || !strings.HasPrefix(b.Title, "Книга: «") || len(b.Diag) == 0 {
		t.Fatalf("first book %+v", b)
	}
	if out.Books[1].Cover != "" {
		t.Fatal("cover without a file")
	}

	w = httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/covers/"+id+".jpg?v=abc123", nil))
	if w.Code != 200 || !strings.Contains(w.Header().Get("Cache-Control"), "max-age=31536000") || w.Body.String() != "\xff\xd8\xffjpeg" {
		t.Fatalf("cover: %d %q", w.Code, w.Header().Get("Cache-Control"))
	}
	for _, p := range []string{"/covers/index.json", "/covers/bk_none.jpg", "/covers/..%2Findex.json"} {
		w = httptest.NewRecorder()
		r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, p, nil))
		if w.Code != http.StatusNotFound {
			t.Errorf("%s: %d", p, w.Code)
		}
	}
}

// Residents keep «Читаю / Прочитал / Хочу» in their own scope.
func TestBookshelfIsPersonal(t *testing.T) {
	if !platformPersonalKeys["bs_bookshelf"] {
		t.Fatal("bs_bookshelf is not personal")
	}
}
