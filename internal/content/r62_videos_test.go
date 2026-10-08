package content

import (
	"encoding/json"
	"io/fs"
	"regexp"
	"strings"
	"testing"
)

// R62: YouTube links on the library cards and guides: canonical watch URLs,
// a title, at most two per card, no em dash; RichVideos finds them by title.
var r62WatchRe = regexp.MustCompile(`^https://www\.youtube\.com/watch\?v=[A-Za-z0-9_-]{11}$`)

func r62Check(t *testing.T, where string, vs []Video) {
	t.Helper()
	if len(vs) > 2 {
		t.Errorf("%s: %d videos, want at most 2", where, len(vs))
	}
	seen := map[string]bool{}
	for _, v := range vs {
		if !r62WatchRe.MatchString(v.URL) {
			t.Errorf("%s: bad url %q", where, v.URL)
		}
		if seen[v.URL] {
			t.Errorf("%s: duplicate %s", where, v.URL)
		}
		seen[v.URL] = true
		if strings.TrimSpace(v.Title) == "" {
			t.Errorf("%s: %s has no title", where, v.URL)
		}
		if strings.Contains(v.Title+v.Why+v.Channel, "—") {
			t.Errorf("%s: em dash in %q", where, v.Title)
		}
		if v.Lang != "ru" && v.Lang != "kk" && v.Lang != "en" {
			t.Errorf("%s: lang %q", where, v.Lang)
		}
	}
}

func TestR62LibraryVideos(t *testing.T) {
	plain, _, _, err := LibRich()
	if err != nil {
		t.Fatal(err)
	}
	var lib struct {
		Tools []struct {
			ID     string  `json:"id"`
			Title  string  `json:"title"`
			Videos []Video `json:"videos"`
		} `json:"tools"`
		Diag []struct {
			ID     string  `json:"id"`
			Title  string  `json:"title"`
			Videos []Video `json:"videos"`
		} `json:"diag"`
	}
	if err := json.Unmarshal(plain, &lib); err != nil {
		t.Fatal(err)
	}
	nt, nd := 0, 0
	for _, x := range lib.Tools {
		r62Check(t, x.ID, x.Videos)
		if len(x.Videos) > 0 {
			nt++
			if got := RichVideos("tool", x.Title); len(got) != len(x.Videos) || got[0].URL != x.Videos[0].URL {
				t.Errorf("RichVideos(tool, %q) = %v", x.Title, got)
			}
		}
	}
	for _, x := range lib.Diag {
		r62Check(t, x.ID, x.Videos)
		if len(x.Videos) > 0 {
			nd++
			if got := RichVideos("diag", strings.ToUpper(x.Title)); len(got) == 0 {
				t.Errorf("RichVideos(diag, %q) found nothing", x.Title)
			}
		}
	}
	// most cards carry videos (the rest wait for the next search round)
	if nt*2 < len(lib.Tools) || nd*2 < len(lib.Diag) {
		t.Errorf("videos on %d of %d tools, %d of %d diagnoses", nt, len(lib.Tools), nd, len(lib.Diag))
	}
	if RichVideos("tool", "нет такого инструмента") != nil {
		t.Error("unknown title must have no videos")
	}
}

func TestR62GuideVideos(t *testing.T) {
	files, err := fs.Glob(guidesFS, "guides/g*.json")
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, f := range files {
		b, _ := guidesFS.ReadFile(f)
		var g struct {
			Videos []Video `json:"videos"`
		}
		if err := json.Unmarshal(b, &g); err != nil {
			t.Fatalf("%s: %v", f, err)
		}
		r62Check(t, f, g.Videos)
		if len(g.Videos) > 0 {
			n++
		}
	}
	if n*2 < len(files) {
		t.Errorf("videos in %d of %d guides", n, len(files))
	}
}
