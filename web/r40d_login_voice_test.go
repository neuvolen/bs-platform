package web

import (
	"encoding/json"
	"regexp"
	"strings"
	"testing"
)

// R40d: when the server has the demo lines in the tour's ElevenLabs voice the
// login page plays them (bsLoginVoice) and keeps the bundled Piper files as
// the fallback (bsLoginVoiceBase); its ETag changes with the voice.
func TestR40dLoginVoiceOverlay(t *testing.T) {
	defer func() { LoginVoiceOverlay = nil }()
	lines := LoginLines()
	if len(lines) != 9 {
		t.Fatalf("demo lines: %d, want 9", len(lines))
	}
	read := func(p page, id string) map[string]string {
		m := regexp.MustCompile(`<script type="application/json" id="` + id + `">(.*?)</script>`).FindStringSubmatch(string(p.plain))
		if m == nil {
			return nil
		}
		var mp map[string]string
		if err := json.Unmarshal([]byte(m[1]), &mp); err != nil {
			t.Fatal(err)
		}
		return mp
	}
	// no overlay: the page built at start, bundled files only
	LoginVoiceOverlay = func() (string, map[string]string) { return "", nil }
	if p := currentLoginPage(); p.etag != loginPage.etag || read(p, "bsLoginVoiceBase") != nil {
		t.Fatal("without the overlay the login page must be the bundled one")
	}
	over := map[string]string{}
	for i, l := range lines {
		over[l] = "/api/v1/platform/tts/login/el_" + strings.Repeat(string(rune('a'+i)), 48) + ".mp3"
	}
	LoginVoiceOverlay = func() (string, map[string]string) { return "ell-1", over }
	p := currentLoginPage()
	if p.etag == loginPage.etag {
		t.Fatal("the overlay page has the bundled page's ETag")
	}
	m, base := read(p, "bsLoginVoice"), read(p, "bsLoginVoiceBase")
	for _, l := range lines {
		if m[l] != over[l] {
			t.Errorf("%q plays %q, want the ElevenLabs file", l, m[l])
		}
		if !strings.HasPrefix(base[l], "/voice/login/") {
			t.Errorf("%q: no bundled fallback (%q)", l, base[l])
		}
	}
	if q := currentLoginPage(); q.etag != p.etag {
		t.Fatal("same version, another page")
	}
	LoginVoiceOverlay = func() (string, map[string]string) { return "ell-2", map[string]string{lines[0]: "/x.mp3"} }
	if q := currentLoginPage(); q.etag == p.etag {
		t.Fatal("another version, same page")
	}
}
