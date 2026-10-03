package http

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/gin-gonic/gin"
)

// POST /api/v1/platform/tts {text, voice?} → audio/wav
// The voice guide of the platform (onboarding tour, welcome): a calm, confident,
// slightly ironic male butler-AI voice. Every phrase is synthesised once and kept
// under a hash of style, voice and text, so the tour phrases
// cost nothing after the first listener. Team and residents may call it.
// The phrases are kept in tts_audio (migration 0021): they survive redeploys.

const ttsMaxRunes = 1200

// ttsStyle: no spoken-manner instruction is sent. The TTS model read the
// instruction aloud instead of following it; the manner comes from the voice.
// "v2" also retires every phrase cached with the old instruction.
const ttsStyle = "v2"

// Deep male prebuilt voices of Gemini TTS.
var ttsVoices = map[string]bool{"Charon": true, "Orus": true, "Fenrir": true, "Iapetus": true, "Algenib": true, "Alnilam": true, "Rasalgethi": true, "Schedar": true}

func ttsDefaultVoice() string {
	if v := strings.TrimSpace(os.Getenv("AI_TTS_VOICE")); v != "" {
		return v
	}
	return "Charon"
}

// ttsKey: the file id of a phrase (fits platformIDRe).
func ttsKey(voice, text string) string {
	s := sha256.Sum256([]byte(ttsStyle + "\x00" + voice + "\x00" + text))
	return "tts_" + hex.EncodeToString(s[:])[:48]
}

// ttsFlight makes concurrent requests for one phrase wait for a single synthesis.
var ttsFlight = struct {
	sync.Mutex
	m map[string]*sync.Mutex
}{m: map[string]*sync.Mutex{}}

func ttsLock(key string) func() {
	ttsFlight.Lock()
	mu := ttsFlight.m[key]
	if mu == nil {
		mu = &sync.Mutex{}
		ttsFlight.m[key] = mu
	}
	ttsFlight.Unlock()
	mu.Lock()
	return func() {
		mu.Unlock()
		ttsFlight.Lock()
		if ttsFlight.m[key] == mu {
			delete(ttsFlight.m, key)
		}
		ttsFlight.Unlock()
	}
}

// ttsListenWait: how long POST /tts waits for a phrase that is not kept yet
// (the client's "wait", seconds, up to ttsListenMax). The synthesis goes on
// after that and the phrase is kept: the next request gets it at once.
var (
	ttsListenWait = 25 * time.Second
	ttsListenMax  = 60 * time.Second
)

func (h *PlatformAI) TTS(c *gin.Context) {
	var req struct {
		Text  string  `json:"text"`
		Voice string  `json:"voice"`
		Wait  float64 `json:"wait"` // seconds; <0: do not wait, only make it (202)
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "bad_request"})
		return
	}
	text := strings.Join(strings.Fields(req.Text), " ")
	if text == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "empty"})
		return
	}
	if utf8.RuneCountInString(text) > ttsMaxRunes {
		c.JSON(http.StatusRequestEntityTooLarge, gin.H{"error": "too_long", "max": ttsMaxRunes})
		return
	}
	voice := ttsDefaultVoice()
	if ttsVoices[req.Voice] {
		voice = req.Voice
	}
	key := ttsKey(voice, text)
	ctx := c.Request.Context()
	serve := func(data []byte, hit bool) {
		c.Header("Cache-Control", "private, max-age=2592000")
		c.Header("X-TTS-Cache", map[bool]string{true: "hit", false: "miss"}[hit])
		c.Data(http.StatusOK, "audio/wav", data)
	}
	if data, err := h.repo.GetTTS(ctx, key); err == nil && data != nil {
		serve(data, true)
		return
	}
	if h.AI == nil || h.AI.Gemini == "" {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "no_tts"})
		return
	}
	wait := ttsListenWait
	if req.Wait > 0 {
		wait = time.Duration(req.Wait * float64(time.Second))
	} else if req.Wait < 0 {
		wait = 0
	}
	if wait > ttsListenMax {
		wait = ttsListenMax
	}
	// The synthesis does not depend on this request: a listener who gives up
	// (or a slow, rate-limited model) does not lose the phrase, it is kept.
	type res struct {
		wav []byte
		hit bool
		err error
	}
	done := make(chan res, 1)
	go func() {
		sctx, cancel := context.WithTimeout(context.Background(), ttsWarmTimeout)
		defer cancel()
		// One synthesis at a time on the server, a listener before the warm-up (platform_tts_warm.go).
		wav, hit, err := h.speakCached(sctx, key, text, voice, true)
		done <- res{wav, hit, err}
	}()
	t := time.NewTimer(wait)
	defer t.Stop()
	select {
	case r := <-done:
		if r.err != nil {
			c.JSON(http.StatusBadGateway, gin.H{"error": "tts_failed", "detail": r.err.Error()})
			return
		}
		serve(r.wav, r.hit)
	case <-t.C:
		c.JSON(http.StatusAccepted, gin.H{"pending": true})
	case <-ctx.Done():
	}
}
