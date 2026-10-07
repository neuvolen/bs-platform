package http

import (
	"context"
	"testing"
	"testing/fstest"
)

// A new version of a shipped video ("replaces" in videos.json) takes the old
// seed's step and on/off state, and the old seed goes off; a team upload is
// never replaced this way.
func TestFunnelVideoSeedReplaces(t *testing.T) {
	ctx := context.Background()
	e := newFVEnv(t)
	v1 := fstest.MapFS{
		"videos.json": {Data: []byte(`{"videos":[
			{"file":"a_tg.mp4","title":"A","step":"d1","caption":"a"},
			{"file":"b_tg.mp4","title":"B","step":"d3","caption":"b"},
			{"file":"c_tg.mp4","title":"C","step":"","caption":"c"}]}`)},
		"a_tg.mp4": {Data: fakeMP4(1024)},
		"b_tg.mp4": {Data: fakeMP4(1100)},
		"c_tg.mp4": {Data: fakeMP4(1200)},
	}
	if n, err := e.v.Seed(ctx, v1); err != nil || n != 3 {
		t.Fatalf("seed v1 %d %v", n, err)
	}
	// the team moved A to d7 and switched B off; a team upload sits on d10
	if err := e.v.update(ctx, func(l *fvLib) bool {
		for _, x := range l.Videos {
			switch x.Source {
			case "seed:a_tg.mp4":
				x.Step = "d7"
			case "seed:b_tg.mp4":
				x.On = false
			}
		}
		l.Videos = append(l.Videos, &fvItem{ID: "up1", Title: "U", Step: "d10", On: true, File: "fv-up", Name: "u_tg.mp4", Source: "upload"})
		return true
	}); err != nil {
		t.Fatal(err)
	}
	v2 := fstest.MapFS{
		"videos.json": {Data: []byte(`{"videos":[
			{"file":"a_v2_tg.mp4","title":"A2","step":"d1","caption":"a2","replaces":"a_tg.mp4"},
			{"file":"b_v2_tg.mp4","title":"B2","step":"d3","caption":"b2","replaces":"b_tg.mp4"},
			{"file":"u_v2_tg.mp4","title":"U2","step":"d10","caption":"u2","replaces":"u_tg.mp4"},
			{"file":"n_tg.mp4","title":"N","step":"d14","caption":"n","replaces":"missing_tg.mp4"}]}`)},
		"a_v2_tg.mp4": {Data: fakeMP4(2048)},
		"b_v2_tg.mp4": {Data: fakeMP4(2100)},
		"u_v2_tg.mp4": {Data: fakeMP4(2200)},
		"n_tg.mp4":    {Data: fakeMP4(2300)},
	}
	if n, err := e.v.Seed(ctx, v2); err != nil || n != 4 {
		t.Fatalf("seed v2 %d %v", n, err)
	}
	lib, _ := e.v.load(ctx)
	by := map[string]*fvItem{}
	for _, x := range lib.Videos {
		by[x.Source] = x
	}
	check := func(src, step string, on bool) {
		t.Helper()
		x := by[src]
		if x == nil || x.Step != step || x.On != on {
			t.Fatalf("%s: %+v, want step %q on %v", src, x, step, on)
		}
	}
	check("seed:a_tg.mp4", "d7", false)    // replaced: off
	check("seed:a_v2_tg.mp4", "d7", true)  // took A's step (moved by the team) and its on state
	check("seed:b_tg.mp4", "d3", false)    // was off
	check("seed:b_v2_tg.mp4", "d3", false) // stays off like B
	check("upload", "d10", true)           // a team upload is not replaced
	check("seed:u_v2_tg.mp4", "d10", false)
	check("seed:n_tg.mp4", "d14", true) // nothing to replace: the manifest's step
	check("seed:c_tg.mp4", "", false)
	if it := e.v.ForStep(ctx, "d7"); it == nil || it.Title != "A2" {
		t.Fatalf("d7 sends %+v", it)
	}
	// a second start changes nothing
	if n, _ := e.v.Seed(ctx, v2); n != 0 {
		t.Fatalf("seeded again: %d", n)
	}
}
