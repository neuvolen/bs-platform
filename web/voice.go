package web

import (
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"regexp"
	"strings"
	"sync"

	"github.com/gin-gonic/gin"
)

// R32c: the tour's voice is recorded ahead of time and ships with the binary.
//
// Every phrase of bsTourTexts has a file web/voice/<key>.mp3, where key is
// the first 16 hex digits of sha256 of the phrase (spaces collapsed). The
// files are made by tools/voice/build.sh (Piper, Russian male voice, with a
// «digital assistant» colour) and served at /voice/<key>.mp3 with a
// one-year cache. The page gets the phrase → file map in its <head>
// (bsVoiceStatic), so the guide starts speaking without asking any API; the
// server's own speech synthesis stays only for a phrase without a file.

//go:embed voice/*.mp3
var voiceFS embed.FS

var voiceFileRe = regexp.MustCompile(`^[0-9a-f]{16}\.mp3$`)

// VoiceKey: the file name (without .mp3) of a phrase.
func VoiceKey(text string) string {
	sum := sha256.Sum256([]byte(strings.Join(strings.Fields(text), " ")))
	return hex.EncodeToString(sum[:])[:16]
}

// StaticVoice: the URL of the phrase's recorded file, "" when there is none.
func StaticVoice(text string) string {
	k := VoiceKey(text)
	if _, err := voiceFS.Open("voice/" + k + ".mp3"); err != nil {
		return ""
	}
	return "/voice/" + k + ".mp3"
}

// TourTextsUnvoiced: the tour's phrases that have no recorded file (the
// server synthesises only these).
func TourTextsUnvoiced() []string {
	var out []string
	for _, t := range TourTexts() {
		if StaticVoice(t) == "" {
			out = append(out, t)
		}
	}
	return out
}

// voiceMapScript: <script type="application/json" id="bsVoiceStatic"> with
// phrase → URL for every recorded phrase of the page.
func voiceMapScript(page []byte) string {
	m := map[string]string{}
	for _, t := range ParseTourTexts(page) {
		if u := StaticVoice(t); u != "" {
			m[t] = u
		}
	}
	b, _ := json.Marshal(m)
	// </script> can not occur in phrases, but a "<" is escaped anyway
	s := strings.ReplaceAll(string(b), "<", `<`)
	return `<script type="application/json" id="bsVoiceStatic">` + s + `</script>`
}

func injectVoice(page string) string {
	tag := voiceMapScript([]byte(page))
	if i := strings.Index(strings.ToLower(page), "<head>"); i >= 0 {
		return page[:i+len("<head>")] + tag + page[i+len("<head>"):]
	}
	return tag + page
}

// serveVoice: GET /voice/<key>.mp3, public and immutable (the name is the
// hash of the phrase).
func serveVoice(c *gin.Context) {
	name := c.Param("file")
	if !voiceFileRe.MatchString(name) {
		c.Status(http.StatusNotFound)
		return
	}
	data, err := voiceFS.ReadFile("voice/" + name)
	if err != nil {
		c.Header("Cache-Control", "no-store")
		c.Status(http.StatusNotFound)
		return
	}
	etag := `"` + strings.TrimSuffix(name, ".mp3") + `"`
	c.Header("Cache-Control", "public, max-age=31536000, immutable")
	c.Header("ETag", etag)
	c.Header("Access-Control-Allow-Origin", "*")
	if c.GetHeader("If-None-Match") == etag {
		c.Status(http.StatusNotModified)
		return
	}
	c.Data(http.StatusOK, "audio/mpeg", data)
}

// ── R36: the premium voice (ElevenLabs) over the built-in recordings ──
//
// VoiceOverlay (set by the server when the owner picked an ElevenLabs voice
// and every phrase is read) gives phrase → URL and a version. The page is
// built once per version with those URLs in bsVoiceStatic instead of the
// built-in files, so its ETag changes and browsers take the new map; the
// file URLs are content hashes, so the browser's caches update too. Without
// an overlay the page is the one built at start.

// VoiceOverlay: "" version when the built-in recordings play.
var VoiceOverlay func() (version string, urls map[string]string)

var appHTML string // the platform page without the seed, before the voice map

var overlayPage struct {
	sync.Mutex
	ver string
	p   page
}

func currentAppPage() page {
	if VoiceOverlay == nil {
		return appPage
	}
	ver, urls := VoiceOverlay()
	if ver == "" || len(urls) == 0 {
		return appPage
	}
	overlayPage.Lock()
	defer overlayPage.Unlock()
	if overlayPage.ver != ver {
		overlayPage.p = build(injectMarker(injectVoiceWith(appHTML, urls)))
		overlayPage.ver = ver
	}
	return overlayPage.p
}

// injectVoiceWith: the voice map with the overlay's URLs over the built-in ones.
func injectVoiceWith(page string, over map[string]string) string {
	m := map[string]string{}
	for _, t := range ParseTourTexts([]byte(page)) {
		if u := over[t]; u != "" {
			m[t] = u
		} else if u := StaticVoice(t); u != "" {
			m[t] = u
		}
	}
	b, _ := json.Marshal(m)
	s := string(b) // json.Marshal escapes "<": no </script> inside
	tag := `<script type="application/json" id="bsVoiceStatic">` + s + `</script>`
	if i := strings.Index(strings.ToLower(page), "<head>"); i >= 0 {
		return page[:i+len("<head>")] + tag + page[i+len("<head>"):]
	}
	return tag + page
}
