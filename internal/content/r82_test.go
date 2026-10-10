package content

import (
	"strings"
	"testing"
)

// R82: every shelf book has «Конспект BS» of 5 ideas in the brand's text
// rules; the free links point at books of the shelf; the meme stickers and
// the Gallup tool are shipped.
func TestR82BooksStickersGallup(t *testing.T) {
	books, err := Books()
	if err != nil {
		t.Fatal(err)
	}
	ids := map[string]bool{}
	for _, b := range books {
		ids[b.ID] = true
		k, err := BookKonspekt(b.ID)
		if err != nil || len(k) != 5 {
			t.Fatalf("%s: konspekt %d %v", b.ID, len(k), err)
		}
		for _, x := range k {
			s := x.Idea + " " + x.Apply
			if x.Idea == "" || x.Apply == "" || strings.ContainsAny(s, "—–") || strings.Contains(s, ", а не ") {
				t.Fatalf("%s: %q", b.ID, s)
			}
		}
	}
	for id, l := range BookFree {
		if !ids[id] || !strings.HasPrefix(l.URL, "https://") {
			t.Fatalf("free %s %v", id, l)
		}
	}
	list, files, err := MemeStickers()
	if err != nil || len(list) < 40 {
		t.Fatalf("stickers %d %v", len(list), err)
	}
	for _, s := range list {
		if s.Cat == "" || s.Name == "" || len(files[s.ID]) < 1000 || string(files[s.ID][1:4]) != "PNG" {
			t.Fatalf("sticker %+v", s)
		}
	}
	ext, err := LibExt()
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, x := range ext.Tools {
		if x["id"] == "tl_r82_gallup" {
			found = true
		}
	}
	if !found || RichToolByID("tl_r82_gallup") == nil || RichToolByID("tl_r82_gallup").Template == nil {
		t.Fatal("no Gallup tool in the library")
	}
}
