package bookcovers

import (
	"bytes"
	"context"
	"encoding/json"
	"image"
	"image/color"
	"image/jpeg"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/bnursik/business_surgery_backend/internal/content"
)

// cover: a book-shaped JPEG, corner (top right) light or dark.
func cover(w, h int, light bool) []byte {
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			c := color.RGBA{20, 30, 60, 255}
			if light {
				c = color.RGBA{240, 236, 228, 255}
			}
			img.Set(x, y, c)
		}
	}
	var b bytes.Buffer
	_ = jpeg.Encode(&b, img, &jpeg.Options{Quality: 80})
	return b.Bytes()
}

// fixture: Google Books knows the Russian edition of «Принципы» (big and
// light), Open Library the original of «Zero to One» (dark), nobody knows
// the third book; a summary edition must not be taken.
func fixture(t *testing.T) (*httptest.Server, *int32) {
	var hits int32
	mux := http.NewServeMux()
	var base string
	mux.HandleFunc("/gb", func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		q := r.URL.Query().Get("q")
		items := []map[string]any{}
		if strings.Contains(q, "Принципы") && r.URL.Query().Get("langRestrict") == "ru" {
			items = append(items,
				map[string]any{"volumeInfo": map[string]any{"title": "Краткое содержание: Принципы", "authors": []string{"Smart Reading"}, "language": "ru",
					"imageLinks": map[string]any{"thumbnail": base + "/img/summary?zoom=1&edge=curl"}}},
				map[string]any{"volumeInfo": map[string]any{"title": "Принципы. Жизнь и работа", "authors": []string{"Рэй Далио"}, "language": "ru",
					"imageLinks": map[string]any{"thumbnail": strings.Replace(base, "https://", "http://", 1) + "/img/principles?zoom=1&edge=curl"}}})
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"items": items})
	})
	mux.HandleFunc("/ol/search.json", func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		docs := []map[string]any{}
		if r.URL.Query().Get("title") == "Zero to One" {
			docs = append(docs, map[string]any{"title": "Zero to One", "author_name": []string{"Peter Thiel", "Blake Masters"}, "cover_i": 777, "edition_count": 40},
				map[string]any{"title": "Zero to One summary", "author_name": []string{"Someone"}, "cover_i": 1, "edition_count": 2})
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"docs": docs})
	})
	mux.HandleFunc("/olc/b/id/777-L.jpg", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("default") != "false" {
			t.Error("Open Library cover without default=false")
		}
		w.Header().Set("Content-Type", "image/jpeg")
		_, _ = w.Write(cover(400, 600, false))
	})
	mux.HandleFunc("/img/principles", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("edge") != "" || r.URL.Query().Get("fife") != "w800" {
			t.Errorf("google image query %s", r.URL.RawQuery)
		}
		_, _ = w.Write(cover(512, 800, true))
	})
	mux.HandleFunc("/img/summary", func(w http.ResponseWriter, r *http.Request) {
		t.Error("summary edition fetched")
		_, _ = w.Write(cover(512, 800, true))
	})
	srv := httptest.NewTLSServer(mux)
	base = srv.URL
	return srv, &hits
}

func store(t *testing.T, srv *httptest.Server) *Store {
	s := New(t.TempDir())
	s.Client = srv.Client()
	s.GBooks, s.OLSearch, s.OLCovers = srv.URL+"/gb", srv.URL+"/ol/search.json", srv.URL+"/olc"
	s.Pause = 0
	return s
}

func TestRunFetchesRealCovers(t *testing.T) {
	srv, hits := fixture(t)
	defer srv.Close()
	s := store(t, srv)
	books := []content.Book{
		{ID: "bk_principles", RU: "Принципы", Author: "Рэй Далио", Orig: "Principles", AuthorEn: "Ray Dalio"},
		{ID: "bk_zero_to_one", RU: "От нуля к единице", Author: "Питер Тиль", Orig: "Zero to One", AuthorEn: "Peter Thiel"},
		{ID: "bk_nowhere", RU: "Нет такой книги", Author: "Никто", Orig: "No Such Book", AuthorEn: "Nobody"},
	}
	added, err := s.Run(context.Background(), books)
	if err != nil || added != 2 {
		t.Fatalf("added %d err %v", added, err)
	}
	p, ok := s.Get("bk_principles")
	if !ok || p.Src != "gb_ru" || p.W != 512 || p.Tone != "light" || p.File != "bk_principles.jpg" {
		t.Fatalf("principles %+v", p)
	}
	z, ok := s.Get("bk_zero_to_one")
	if !ok || z.Src != "ol" || z.Tone != "dark" || z.BG == "" {
		t.Fatalf("zero %+v", z)
	}
	// The file is stored exactly as downloaded (the publisher's artwork unaltered).
	b, _ := os.ReadFile(filepath.Join(s.Dir, z.File))
	if !bytes.Equal(b, cover(400, 600, false)) {
		t.Fatal("cover bytes changed")
	}
	if _, ok := s.Get("bk_nowhere"); ok {
		t.Fatal("missing book has a cover")
	}
	if s.Stats([]string{"bk_principles", "bk_zero_to_one", "bk_nowhere"}) != 2 {
		t.Fatal("stats")
	}
	if u := s.URL("bk_principles"); !strings.HasPrefix(u, "/covers/bk_principles.jpg?v=") {
		t.Fatalf("url %q", u)
	}
	// A second run asks nobody: found covers stay, the missing one waits for Retry.
	before := atomic.LoadInt32(hits)
	if added, _ = s.Run(context.Background(), books); added != 0 || atomic.LoadInt32(hits) != before {
		t.Fatalf("second run: added %d, hits %d → %d", added, before, atomic.LoadInt32(hits))
	}
	// The index survives a restart.
	s2 := New(s.Dir)
	if _, ok := s2.Get("bk_zero_to_one"); !ok {
		t.Fatal("index not reloaded")
	}
	// After Retry the missing book is searched again.
	s.Retry = time.Nanosecond
	time.Sleep(time.Millisecond)
	_, _ = s.Run(context.Background(), books)
	if atomic.LoadInt32(hits) == before {
		t.Fatal("missing book not retried")
	}
}

func TestDownloadRejectsNonCovers(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/wide", func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write(cover(600, 300, true)) })
	mux.HandleFunc("/tiny", func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write(cover(1, 1, true)) })
	mux.HandleFunc("/html", func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte("<html>")) })
	mux.HandleFunc("/big", func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write(make([]byte, 5<<20)) })
	srv := httptest.NewServer(mux)
	defer srv.Close()
	s := New(t.TempDir())
	for _, p := range []string{"/wide", "/tiny", "/html", "/big", "/404"} {
		if _, err := s.download(context.Background(), cand{"x", srv.URL + p}); err == nil {
			t.Errorf("%s accepted", p)
		}
	}
}

func TestPathOnlyServesCovers(t *testing.T) {
	s := New(t.TempDir())
	_ = os.WriteFile(filepath.Join(s.Dir, "bk_a.jpg"), []byte("x"), 0o644)
	_ = os.WriteFile(filepath.Join(s.Dir, "index.json"), []byte("{}"), 0o644)
	if s.Path("bk_a.jpg") == "" {
		t.Fatal("cover not served")
	}
	for _, n := range []string{"index.json", "../bk_a.jpg", "bk_a.jpg/..", "bk_b.jpg", "BK_A.JPG"} {
		if s.Path(n) != "" {
			t.Errorf("%s served", n)
		}
	}
}

func TestGBImage(t *testing.T) {
	got := gbImage("http://books.google.com/books/content?id=X&printsec=frontcover&img=1&zoom=1&edge=curl&source=gbs_api", true)
	if !strings.HasPrefix(got, "https://") || strings.Contains(got, "edge=") || !strings.Contains(got, "fife=w800") {
		t.Fatal(got)
	}
}
