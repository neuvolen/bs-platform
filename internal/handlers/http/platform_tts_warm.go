package http

import (
	"context"
	"log"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode/utf8"

	pg "github.com/bnursik/business_surgery_backend/internal/repository/pg"
	"github.com/gin-gonic/gin"
)

// POST /api/v1/platform/tts/warm {texts[], voice?}
//
// The voice guide's phrases are made ahead of time. The page sends every
// phrase of the tour right after login, long before anyone opens the tour;
// the server makes the missing ones one by one in the background and keeps
// them (platform_files), so the tour starts speaking at once and Gemini's
// TTS rate limit (a few requests a minute on the free tier) never cuts the
// voice off in the middle of the tour.
//
// All TTS calls of the server go through one gate: one synthesis at a time.
// A listener waiting for a phrase (POST /tts) goes before the background.

const (
	ttsWarmMaxTexts = 80
	ttsWarmMaxQueue = 400
	ttsWarmTries    = 4
)

var (
	// ttsWarmGap: pause between background syntheses (kind to the rate limit).
	ttsWarmGap = 1500 * time.Millisecond
	// ttsWarmRetry: wait before a failed phrase is tried again.
	ttsWarmRetry = 20 * time.Second
	// ttsWarmTimeout: one background synthesis, Speak's own 429 waits included.
	ttsWarmTimeout = 3 * time.Minute
)

var ttsGate = make(chan struct{}, 1)

// ttsUrgent: listeners waiting for a phrase; the background waits for them.
var ttsUrgent atomic.Int32

type ttsJob struct {
	key, text, voice string
	tries            int
	notBefore        time.Time
}

type ttsWarmer struct {
	mu      sync.Mutex
	queue   []ttsJob
	queued  map[string]bool
	running bool
	made    int
	failed  int
}

var ttsW = &ttsWarmer{queued: map[string]bool{}}

// speakCached returns the phrase, making it if it is not kept yet. urgent:
// someone is waiting for it now.
func (h *PlatformAI) speakCached(ctx context.Context, key, text, voice string, urgent bool) (data []byte, hit bool, err error) {
	if f, err := h.repo.GetFile(ctx, key); err == nil && f != nil {
		return f.Data, true, nil
	}
	if urgent {
		ttsUrgent.Add(1)
		defer ttsUrgent.Add(-1)
	}
	unlock := ttsLock(key)
	defer unlock()
	// Another request (or the background) may have made it meanwhile.
	if f, err := h.repo.GetFile(ctx, key); err == nil && f != nil {
		return f.Data, true, nil
	}
	select {
	case ttsGate <- struct{}{}:
	case <-ctx.Done():
		return nil, false, ctx.Err()
	}
	wav, err := h.AI.Speak(ctx, text, voice, "")
	<-ttsGate
	if err != nil {
		return nil, false, err
	}
	_ = h.repo.PutFile(context.Background(), pg.PlatformFile{ID: key, Name: "voice.wav", Mime: "audio/wav", Data: wav}, "server:tts")
	return wav, false, nil
}

func (w *ttsWarmer) add(h *PlatformAI, jobs []ttsJob) int {
	w.mu.Lock()
	defer w.mu.Unlock()
	n := 0
	for _, j := range jobs {
		if w.queued[j.key] || len(w.queue) >= ttsWarmMaxQueue {
			continue
		}
		w.queued[j.key] = true
		w.queue = append(w.queue, j)
		n++
	}
	if n > 0 && !w.running {
		w.running = true
		go w.run(h)
	}
	return n
}

func (w *ttsWarmer) run(h *PlatformAI) {
	for {
		w.mu.Lock()
		if len(w.queue) == 0 {
			w.running = false
			w.mu.Unlock()
			return
		}
		j := w.queue[0]
		w.queue = w.queue[1:]
		w.mu.Unlock()
		if d := time.Until(j.notBefore); d > 0 {
			time.Sleep(d)
		}
		// Listeners first.
		for i := 0; ttsUrgent.Load() > 0 && i < 600; i++ {
			time.Sleep(100 * time.Millisecond)
		}
		ctx, cancel := context.WithTimeout(context.Background(), ttsWarmTimeout)
		_, hit, err := h.speakCached(ctx, j.key, j.text, j.voice, false)
		cancel()
		w.mu.Lock()
		switch {
		case err != nil && j.tries+1 < ttsWarmTries:
			j.tries++
			j.notBefore = time.Now().Add(ttsWarmRetry)
			w.queue = append(w.queue, j) // after the others: one stubborn phrase does not hold the tour
		case err != nil:
			w.failed++
			delete(w.queued, j.key)
			log.Printf("tts warm: %q not made: %v", ttsShort(j.text), err)
		default:
			if !hit {
				w.made++
			}
			delete(w.queued, j.key)
		}
		w.mu.Unlock()
		if err == nil && !hit {
			time.Sleep(ttsWarmGap)
		}
	}
}

func ttsShort(s string) string {
	if utf8.RuneCountInString(s) > 40 {
		return string([]rune(s)[:40]) + "…"
	}
	return s
}

// TTSWarm: POST /tts/warm — make the phrases ahead of time; answers which are ready.
func (h *PlatformAI) TTSWarm(c *gin.Context) {
	var req struct {
		Texts []string `json:"texts"`
		Voice string   `json:"voice"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "bad_request"})
		return
	}
	if len(req.Texts) > ttsWarmMaxTexts {
		req.Texts = req.Texts[:ttsWarmMaxTexts]
	}
	voice := ttsDefaultVoice()
	if ttsVoices[req.Voice] {
		voice = req.Voice
	}
	ctx := c.Request.Context()
	ready := make([]bool, len(req.Texts))
	var jobs []ttsJob
	nReady := 0
	seen := map[string]bool{}
	for i, t := range req.Texts {
		text := strings.Join(strings.Fields(t), " ")
		if text == "" || utf8.RuneCountInString(text) > ttsMaxRunes {
			continue
		}
		key := ttsKey(voice, text)
		if h.repo.FileExists(ctx, key) {
			ready[i] = true
			nReady++
			continue
		}
		if !seen[key] {
			seen[key] = true
			jobs = append(jobs, ttsJob{key: key, text: text, voice: voice})
		}
	}
	noTTS := h.AI == nil || h.AI.Gemini == ""
	queued := 0
	if !noTTS {
		queued = ttsW.add(h, jobs)
	}
	ttsW.mu.Lock()
	waiting := len(ttsW.queue)
	ttsW.mu.Unlock()
	c.JSON(http.StatusOK, gin.H{"total": len(req.Texts), "ready": nReady, "items": ready, "queued": queued,
		"waiting": waiting, "noTTS": noTTS})
}
