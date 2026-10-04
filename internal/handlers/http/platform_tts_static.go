package http

import (
	"context"
	"log"
	"net/http"
	"os"
	"regexp"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/gin-gonic/gin"
)

// R32d: the tour's voice as static files.
//
// The tour never synthesises while someone listens. Every phrase is made
// once on the server (TourVoiceLoop, quota-aware) and kept in tts_audio;
// from there it is served as an immutable file under its content hash:
//
//	GET  /api/v1/platform/tts/a/<key>.wav   public, cached for a year
//	GET  /api/v1/platform/tts/manifest      phrase → file of the tour, and the state
//	POST /api/v1/platform/tts/voice {voice} the team picks the voice
//
// After login the page fetches the manifest, downloads every file into
// memory (and the browser's Cache API) and plays only those. A phrase
// without a file is shown as text with «озвучка появится позже».
//
// The voice is a setting (club doc bs_tts_voice). A new voice is made
// phrase by phrase as the quota allows; until a phrase exists in the new
// voice the manifest serves the earlier recording, so the tour never goes
// silent because of a change.

const ttsVoiceDoc = "bs_tts_voice"

var ttsFileRe = regexp.MustCompile(`^(tts_[0-9a-f]{48})\.wav$`)

// TTSVoiceList: the voices offered in settings, with Gemini's own word for each.
var TTSVoiceList = []map[string]string{
	{"id": "Iapetus", "note": "чёткий, ровный: ИИ-дворецкий"},
	{"id": "Schedar", "note": "ровный, спокойный"},
	{"id": "Charon", "note": "информативный, ниже"},
	{"id": "Orus", "note": "твёрдый"},
	{"id": "Sadaltager", "note": "знающий"},
	{"id": "Algenib", "note": "с хрипотцой"},
	{"id": "Alnilam", "note": "твёрдый, ниже"},
	{"id": "Rasalgethi", "note": "информативный"},
	{"id": "Fenrir", "note": "живой, быстрый"},
}

var ttsVoiceCache struct {
	sync.Mutex
	v  string
	at time.Time
}

// ttsVoice: the voice of the tour now: the team's setting, else AI_TTS_VOICE, else Iapetus.
func (h *PlatformAI) ttsVoice(ctx context.Context) string {
	ttsVoiceCache.Lock()
	if ttsVoiceCache.v != "" && time.Since(ttsVoiceCache.at) < 30*time.Second {
		v := ttsVoiceCache.v
		ttsVoiceCache.Unlock()
		return v
	}
	ttsVoiceCache.Unlock()
	v := ttsDefaultVoice()
	if h.repo != nil {
		if d, err := h.repo.GetDoc(ctx, "club", ttsVoiceDoc); err == nil && d != nil && !d.Deleted {
			if s := strings.Trim(strings.TrimSpace(d.Value), `"`); ttsVoices[s] {
				v = s
			}
		}
	}
	ttsVoiceCache.Lock()
	ttsVoiceCache.v, ttsVoiceCache.at = v, time.Now()
	ttsVoiceCache.Unlock()
	return v
}

func ttsForgetVoice() {
	ttsVoiceCache.Lock()
	ttsVoiceCache.v = ""
	ttsVoiceCache.Unlock()
}

// TTSFile: GET /tts/a/<key>.wav — a kept phrase, immutable (the key is the
// hash of style, voice and text). Public: the tour's phrases are no secret,
// and a plain URL is cached by the browser and any CDN for a year.
func (h *PlatformAI) TTSFile(c *gin.Context) {
	m := ttsFileRe.FindStringSubmatch(c.Param("file"))
	if m == nil || h.repo == nil {
		c.Status(http.StatusNotFound)
		return
	}
	if c.GetHeader("If-None-Match") == `"`+m[1]+`"` {
		c.Status(http.StatusNotModified)
		return
	}
	data, err := h.repo.GetTTS(c.Request.Context(), m[1])
	if err != nil || data == nil {
		c.Header("Cache-Control", "no-store")
		c.Status(http.StatusNotFound)
		return
	}
	c.Header("Cache-Control", "public, max-age=31536000, immutable")
	c.Header("ETag", `"`+m[1]+`"`)
	c.Header("Access-Control-Allow-Origin", "*")
	c.Data(http.StatusOK, "audio/wav", data)
}

// tourTexts: the tour's phrases (the page's bsTourTexts block).
func (h *PlatformAI) tourTexts() []string {
	if h.TourTexts == nil {
		return nil
	}
	var out []string
	for _, t := range h.TourTexts() {
		t = strings.Join(strings.Fields(t), " ")
		if t != "" && utf8.RuneCountInString(t) <= ttsMaxRunes {
			out = append(out, t)
		}
	}
	return out
}

// ttsState: what the settings line says.
//
//	ready  every phrase is there in the current voice
//	quota  Gemini's speech quota is used up until «until»
//	noKey  no server TTS (R34a: Gemini only with GEMINI_ENABLED=1): a phrase
//	       without a recording is shown as text
//	making phrases are being made
func (h *PlatformAI) ttsState(missing int) (string, time.Time) {
	if missing == 0 {
		return "ready", time.Time{}
	}
	if h.AI == nil || h.AI.Gemini == "" {
		return "noKey", time.Time{}
	}
	if u := h.AI.QuotaUntil("tts"); !u.IsZero() {
		return "quota", u
	}
	return "making", time.Time{}
}

// TTSManifest: GET /tts/manifest — every tour phrase with its file.
func (h *PlatformAI) TTSManifest(c *gin.Context) {
	ctx := c.Request.Context()
	voice := h.ttsVoice(ctx)
	texts := h.tourTexts()
	idx, err := h.repo.TTSIndex(ctx, ttsStyle)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "load_failed"})
		return
	}
	// text → voice → key
	have := map[string]map[string]string{}
	for _, e := range idx {
		if have[e.Text] == nil {
			have[e.Text] = map[string]string{}
		}
		have[e.Text][e.Voice] = e.Key
	}
	items := map[string]string{}
	ready, older, missing, static := 0, 0, 0, 0
	for _, t := range texts {
		// R32c: a phrase recorded into the binary (web/voice) needs no synthesis
		if h.StaticVoice != nil {
			if u := h.StaticVoice(t); u != "" {
				items[t] = u
				ready++
				static++
				continue
			}
		}
		if k, ok := have[t][voice]; ok {
			items[t] = "/api/v1/platform/tts/a/" + k + ".wav"
			ready++
			continue
		}
		// The new voice is not made yet: the earlier recording meanwhile.
		missing++
		for _, k := range have[t] {
			items[t] = "/api/v1/platform/tts/a/" + k + ".wav"
			older++
			break
		}
	}
	state, until := h.ttsState(missing)
	out := gin.H{"voice": voice, "total": len(texts), "ready": ready, "older": older, "missing": missing,
		"state": state, "items": items, "static": static}
	if !until.IsZero() {
		out["until"] = until.UTC().Format(time.RFC3339)
	}
	if !isResident(c) && !isLead(c) {
		out["voices"] = TTSVoiceList
	}
	c.Header("Cache-Control", "no-store")
	c.JSON(http.StatusOK, out)
}

// TTSSetVoice: POST /tts/voice {voice} — the team picks the tour's voice.
// The earlier recordings stay and play until the new ones are made.
func (h *PlatformAI) TTSSetVoice(c *gin.Context) {
	if !teamOnly(c) {
		return
	}
	var req struct {
		Voice string `json:"voice"`
	}
	if err := c.ShouldBindJSON(&req); err != nil || !ttsVoices[req.Voice] {
		c.JSON(http.StatusBadRequest, gin.H{"error": "unknown_voice"})
		return
	}
	if err := h.repo.PutServerDoc(c.Request.Context(), ttsVoiceDoc, req.Voice); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "store_failed"})
		return
	}
	ttsForgetVoice()
	queued, missing := h.PrewarmTour(context.Background(), h.tourTexts())
	log.Printf("tts: voice %s, %d phrases to make (%d queued)", req.Voice, missing, queued)
	c.JSON(http.StatusOK, gin.H{"voice": req.Voice, "missing": missing, "queued": queued})
}

// ttsStaticDir: TTS_STATIC_DIR=/path also writes every kept tour phrase there
// as <key>.wav (an ops path: a CDN or the image can serve them as files).
func (h *PlatformAI) ttsExport(ctx context.Context) {
	dir := strings.TrimSpace(os.Getenv("TTS_STATIC_DIR"))
	if dir == "" || h.repo == nil {
		return
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		log.Printf("tts export: %v", err)
		return
	}
	idx, err := h.repo.TTSIndex(ctx, ttsStyle)
	if err != nil {
		return
	}
	for _, e := range idx {
		p := dir + "/" + e.Key + ".wav"
		if _, err := os.Stat(p); err == nil {
			continue
		}
		if data, err := h.repo.GetTTS(ctx, e.Key); err == nil && data != nil {
			_ = os.WriteFile(p, data, 0o644)
		}
	}
}
