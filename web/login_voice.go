package web

import (
	"embed"
	"encoding/json"
	"net/http"
	"regexp"
	"strings"

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

// injectLoginVoice puts the line → URL map where the page has <!--LOGINVOICE-->.
func injectLoginVoice(page string) string {
	m := map[string]string{}
	for _, t := range LoginTourLines([]byte(page)) {
		if u := LoginVoice(t); u != "" {
			m[t] = u
		}
	}
	b, _ := json.Marshal(m) // json.Marshal escapes "<": no </script> inside
	return strings.Replace(page, "<!--LOGINVOICE-->", `<script type="application/json" id="bsLoginVoice">`+string(b)+`</script>`, 1)
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
