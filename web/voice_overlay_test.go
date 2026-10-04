package web

import (
	"encoding/json"
	"regexp"
	"testing"
)

// R36: with a premium voice ready the page's phrase → file map points at its
// files (a new page version: new ETag), the rest stays on the built-in files.
func TestR36VoiceOverlay(t *testing.T) {
	defer func() { VoiceOverlay = nil }()
	texts := TourTexts()
	if len(texts) < 3 {
		t.Fatal("tour texts")
	}
	mapOf := func(p page) map[string]string {
		m := regexp.MustCompile(`<script type="application/json" id="bsVoiceStatic">(.*?)</script>`).FindSubmatch(p.plain)
		if m == nil {
			t.Fatal("no voice map")
		}
		var out map[string]string
		if err := json.Unmarshal(m[1], &out); err != nil {
			t.Fatal(err)
		}
		return out
	}
	VoiceOverlay = nil
	if currentAppPage().etag != appPage.etag {
		t.Fatal("no overlay: the start page")
	}
	VoiceOverlay = func() (string, map[string]string) { return "", nil }
	if currentAppPage().etag != appPage.etag {
		t.Fatal("empty overlay: the start page")
	}
	u0 := "/api/v1/platform/tts/p/el_" + "0123456789abcdef0123456789abcdef0123456789abcdef" + ".mp3"
	VoiceOverlay = func() (string, map[string]string) { return "el-1", map[string]string{texts[0]: u0} }
	p1 := currentAppPage()
	m := mapOf(p1)
	if p1.etag == appPage.etag || m[texts[0]] != u0 || m[texts[1]] != StaticVoice(texts[1]) || len(m) != len(mapOf(appPage)) {
		t.Fatalf("overlay map: %s %s", m[texts[0]], m[texts[1]])
	}
	if currentAppPage().etag != p1.etag {
		t.Fatal("same version must reuse the page")
	}
	VoiceOverlay = func() (string, map[string]string) { return "el-2", map[string]string{texts[0]: u0, texts[1]: u0} }
	p2 := currentAppPage()
	if p2.etag == p1.etag || mapOf(p2)[texts[1]] != u0 {
		t.Fatal("a new version: a new page")
	}
	if len(p2.gz) == 0 || len(p2.gz) >= len(p2.plain) {
		t.Fatal("gzip")
	}
}
