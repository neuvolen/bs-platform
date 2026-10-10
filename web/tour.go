package web

import (
	"encoding/json"
	"regexp"
	"strings"
	"sync"
)

// The voice guide's phrases (onboarding tour) live in one place: the
// <script type="application/json" id="bsTourTexts"> block of platform.html.
// The page reads it for the tour, the server reads it to make every phrase
// ahead of time (platform_tts_warm.go), so a changed phrase is voiced
// before anyone opens the tour.

var tourRe = regexp.MustCompile(`(?s)<script type="application/json" id="bsTourTexts">(.*?)</script>`)

type tourItem struct {
	D string `json:"d"`
}

// TourDoc is the tour's texts as the page has them.
type TourDoc struct {
	Welcome map[string]tourItem `json:"welcome"`
	Nav     map[string]string   `json:"nav"`
	Actions []tourItem          `json:"actions"`
	Help    tourItem            `json:"help"`
	Bye     tourItem            `json:"bye"`
	Cta     map[string]string   `json:"cta"`
	RSteps  []tourItem          `json:"rsteps"` // R81: the resident's tour, every section and its tabs in order
}

var tourOnce struct {
	sync.Once
	texts []string
}

// ParseTourTexts returns every spoken phrase of the tour in the page, each once.
func ParseTourTexts(page []byte) []string {
	m := tourRe.FindSubmatch(page)
	if m == nil {
		return nil
	}
	var d TourDoc
	if err := json.Unmarshal(m[1], &d); err != nil {
		return nil
	}
	var out []string
	seen := map[string]bool{}
	add := func(s string) {
		s = strings.Join(strings.Fields(s), " ")
		if s != "" && !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	// The order a listener meets them: welcome, the call to start, the steps.
	for _, k := range []string{"admin", "res", "lead"} {
		add(d.Welcome[k].D)
	}
	for _, k := range []string{"admin", "res", "lead"} {
		add(d.Cta[k])
	}
	add(d.Actions0())
	for _, k := range []string{"track", "club", "fin", "sales", "mkt", "lib", "lhome", "lideas", "lbs", "ltrack", "lmeas", "lup"} {
		add(d.Nav[k])
	}
	for _, s := range d.RSteps {
		add(s.D)
	}
	for _, v := range d.Nav {
		add(v)
	}
	for _, a := range d.Actions {
		add(a.D)
	}
	add(d.Help.D)
	add(d.Bye.D)
	for _, v := range d.Welcome {
		add(v.D)
	}
	for _, v := range d.Cta {
		add(v)
	}
	return out
}

// Actions0: the first step (search) of every mode.
func (d TourDoc) Actions0() string {
	if len(d.Actions) == 0 {
		return ""
	}
	return d.Actions[0].D
}

// TourTexts: the tour's phrases of the embedded platform page.
func TourTexts() []string {
	tourOnce.Do(func() { tourOnce.texts = ParseTourTexts(platformHTML) })
	return tourOnce.texts
}
