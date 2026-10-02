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

	pg "github.com/bnursik/business_surgery_backend/internal/repository/pg"
	"github.com/gin-gonic/gin"
)

// POST /api/v1/platform/tts {text, voice?} → audio/wav
// The voice guide of the platform (onboarding tour, welcome): a calm, confident,
// slightly ironic male butler-AI voice. Every phrase is synthesised once and kept
// in platform_files under a hash of style, voice and text, so the tour phrases
// cost nothing after the first listener. Team and residents may call it.

const ttsMaxRunes = 1200

// ttsStyle is the spoken-manner instruction given to the model.
const ttsStyle = "Говори как ИИ-ассистент дворецкий: спокойно, уверенно, чуть иронично, низким голосом. Произнеси по-русски только этот текст:"

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

func (h *PlatformAI) TTS(c *gin.Context) {
	var req struct {
		Text  string `json:"text"`
		Voice string `json:"voice"`
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
	if f, err := h.repo.GetFile(ctx, key); err == nil && f != nil {
		serve(f.Data, true)
		return
	}
	if h.AI == nil || h.AI.Gemini == "" {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "no_tts"})
		return
	}
	unlock := ttsLock(key)
	defer unlock()
	// Another request may have made it while this one waited.
	if f, err := h.repo.GetFile(ctx, key); err == nil && f != nil {
		serve(f.Data, true)
		return
	}
	sctx, cancel := context.WithTimeout(ctx, 90*time.Second)
	defer cancel()
	wav, err := h.AI.Speak(sctx, text, voice, ttsStyle)
	if err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": "tts_failed", "detail": err.Error()})
		return
	}
	_ = h.repo.PutFile(context.Background(), pg.PlatformFile{ID: key, Name: "voice.wav", Mime: "audio/wav", Data: wav}, "server:tts")
	serve(wav, false)
}
