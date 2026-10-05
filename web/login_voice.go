package web

import (
	"embed"
	"encoding/json"
	"net/http"
	"regexp"
	"strings"
	"sync"

	"github.com/gin-gonic/gin"
)

// R40c: the guided demo of the login page («Посмотреть, как это работает»).
//
// Every step of the bsLoginTour block in web/login.html has a spoken line v.
// tools/voice/build.sh --login records it into web/voice/login/<key>.mp3 (the
// same Piper voice and processing as the platform tour, key = VoiceKey of the
// line). The files ship with the binary and are served at
// /voice/login/<key>.mp3; the login page gets the line → URL map in
// bsLoginVoice, so the demo plays without any API.

//go:embed voice/login/*.mp3
var loginVoiceFS embed.FS

var loginTourRe = regexp.MustCompile(`(?s)<script type="application/json" id="bsLoginTour">(.*?)</script>`)

// LoginTourLines: the spoken lines of the login demo, in step order, spaces collapsed.
func LoginTourLines(page []byte) []string {
	m := loginTourRe.FindSubmatch(page)
	if m == nil {
		return nil
	}
	var d struct {
		Steps []struct {
			V string `json:"v"`
		} `json:"steps"`
	}
	if json.Unmarshal(m[1], &d) != nil {
		return nil
	}
	var out []string
	seen := map[string]bool{}
	for _, s := range d.Steps {
		t := strings.Join(strings.Fields(s.V), " ")
		if t != "" && !seen[t] {
			seen[t] = true
			out = append(out, t)
		}
	}
	return out
}

// LoginVoice: the URL of a demo line's recording, "" when there is none.
func LoginVoice(text string) string {
	k := VoiceKey(text)
	if _, err := loginVoiceFS.Open("voice/login/" + k + ".mp3"); err != nil {
		return ""
	}
	return "/voice/login/" + k + ".mp3"
}

// LoginLines: the spoken lines of the login demo (the server voices them
// with the tour's ElevenLabs voice, R40d).
func LoginLines() []string { return LoginTourLines(loginHTML) }

// injectLoginVoice puts the line → URL map where the page has <!--LOGINVOICE-->.
func injectLoginVoice(page string) string { return injectLoginVoiceWith(page, nil) }

// injectLoginVoiceWith: bsLoginVoice is the map the demo plays (the server's
// ElevenLabs files when over has them, R40d), bsLoginVoiceBase the bundled
// Piper files: the page falls back to them when a file does not load.
func injectLoginVoiceWith(page string, over map[string]string) string {
	m, base := map[string]string{}, map[string]string{}
	for _, t := range LoginTourLines([]byte(page)) {
		u := LoginVoice(t)
		if u != "" {
			base[t] = u
		}
		if o := over[t]; o != "" {
			u = o
		}
		if u != "" {
			m[t] = u
		}
	}
	b, _ := json.Marshal(m) // json.Marshal escapes "<": no </script> inside
	tag := `<script type="application/json" id="bsLoginVoice">` + string(b) + `</script>`
	if len(over) > 0 {
		bb, _ := json.Marshal(base)
		tag += `<script type="application/json" id="bsLoginVoiceBase">` + string(bb) + `</script>`
	}
	return strings.Replace(page, "<!--LOGINVOICE-->", tag, 1)
}

// LoginVoiceOverlay (set by the server): line → URL of the login demo read
// with the tour's ElevenLabs voice, and its version; "" while the bundled
// recordings play. Like the platform page, the login page is built once per
// version, so its ETag changes with the voice.
var LoginVoiceOverlay func() (version string, urls map[string]string)

var loginHTMLBase string // the login page with icons, before the voice map

var loginOverlayPage struct {
	sync.Mutex
	ver string
	p   page
}

func currentLoginPage() page {
	if LoginVoiceOverlay == nil {
		return loginPage
	}
	ver, urls := LoginVoiceOverlay()
	if ver == "" || len(urls) == 0 {
		return loginPage
	}
	loginOverlayPage.Lock()
	defer loginOverlayPage.Unlock()
	if loginOverlayPage.ver != ver {
		loginOverlayPage.p = build(injectLoginVoiceWith(loginHTMLBase, urls))
		loginOverlayPage.ver = ver
	}
	return loginOverlayPage.p
}

func serveLoginVoice(c *gin.Context) {
	name := c.Param("file")
	if !voiceFileRe.MatchString(name) {
		c.Status(http.StatusNotFound)
		return
	}
	data, err := loginVoiceFS.ReadFile("voice/login/" + name)
	if err != nil {
		c.Header("Cache-Control", "no-store")
		c.Status(http.StatusNotFound)
		return
	}
	etag := `"` + strings.TrimSuffix(name, ".mp3") + `"`
	c.Header("Cache-Control", "public, max-age=31536000, immutable")
	c.Header("ETag", etag)
	if c.GetHeader("If-None-Match") == etag {
		c.Status(http.StatusNotModified)
		return
	}
	c.Data(http.StatusOK, "audio/mpeg", data)
}
