package web

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
)

// R32c: every phrase the tour speaks has a recorded file. A failure here
// means a phrase in bsTourTexts changed: run tools/voice/build.sh.
func TestR32cEveryTourPhraseHasVoiceFile(t *testing.T) {
	texts := TourTexts()
	if len(texts) < 20 {
		t.Fatalf("tour texts: %d", len(texts))
	}
	for _, s := range texts {
		u := StaticVoice(s)
		if u == "" {
			t.Errorf("no voice/%s.mp3 for %q: run tools/voice/build.sh", VoiceKey(s), s)
			continue
		}
		b, err := voiceFS.ReadFile("voice/" + VoiceKey(s) + ".mp3")
		if err != nil || len(b) < 4000 || len(b) > 200000 {
			t.Errorf("%s: %d bytes, %v", u, len(b), err)
		}
	}
	if n := len(TourTextsUnvoiced()); n != 0 {
		t.Errorf("%d phrases without a file", n)
	}
	// no stale files: each file belongs to a phrase
	want := map[string]bool{}
	for _, s := range texts {
		want[VoiceKey(s)+".mp3"] = true
	}
	ents, _ := voiceFS.ReadDir("voice")
	for _, e := range ents {
		if !want[e.Name()] {
			t.Errorf("voice/%s belongs to no phrase", e.Name())
		}
	}
}

func TestR32cVoiceMapInPageAndServed(t *testing.T) {
	m := regexp.MustCompile(`<script type="application/json" id="bsVoiceStatic">(.*?)</script>`).FindSubmatch(appPage.plain)
	if m == nil {
		t.Fatal("bsVoiceStatic not in the page")
	}
	var mp map[string]string
	if err := json.Unmarshal(m[1], &mp); err != nil {
		t.Fatal(err)
	}
	if len(mp) != len(TourTexts()) {
		t.Fatalf("map %d, texts %d", len(mp), len(TourTexts()))
	}
	if strings.Contains(string(loginPage.plain), "bsVoiceStatic") {
		t.Error("login page should not carry the voice map")
	}
	gin.SetMode(gin.TestMode)
	r := gin.New()
	Register(r, "x", "bs_session")
	var url string
	for _, u := range mp {
		url = u
		break
	}
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest("GET", url, nil))
	if w.Code != 200 || w.Header().Get("Content-Type") != "audio/mpeg" || !strings.Contains(w.Header().Get("Cache-Control"), "immutable") {
		t.Fatalf("GET %s: %d %v", url, w.Code, w.Header())
	}
	w2 := httptest.NewRecorder()
	req := httptest.NewRequest("GET", url, nil)
	req.Header.Set("If-None-Match", w.Header().Get("ETag"))
	r.ServeHTTP(w2, req)
	if w2.Code != http.StatusNotModified {
		t.Errorf("etag: %d", w2.Code)
	}
	for _, bad := range []string{"/voice/0000000000000000.mp3", "/voice/..%2Fplatform.html", "/voice/index.json"} {
		w := httptest.NewRecorder()
		r.ServeHTTP(w, httptest.NewRequest("GET", bad, nil))
		if w.Code != 404 {
			t.Errorf("%s: %d", bad, w.Code)
		}
	}
}
