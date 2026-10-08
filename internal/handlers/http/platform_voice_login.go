package http

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"log"
	"net/http"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/bnursik/business_surgery_backend/internal/ai"
	"github.com/gin-gonic/gin"
)

// R40d: «Голос в "Посмотреть, как это работает" сделай как в озвучке
// обучения, побыстрее».
//
// The guided demo of the login page (web/login.html, bsLoginTour, 9 spoken
// lines) is read by the server with the ElevenLabs voice the tour plays
// (premiumCfg.Active: the chosen voice, or the premade fallback while the
// chosen one needs a paid plan), a bit faster (voice_settings.speed,
// LoginSpeed). The MP3s are kept in tts_audio (style el1l) next to the tour
// phrases; once all 9 are there the login page gets them in bsLoginVoice
// (web.LoginVoiceOverlay) instead of the bundled Piper files, so the demo
// never mixes two voices. The file name is a hash of voice, model, settings
// (speed included) and text: another voice is another URL.
//
// Public, read-only, only the files of the current demo map:
//
//	GET /api/v1/platform/tts/login/<key>.mp3   immutable

const (
	loginStyle   = "el1l" // tts_audio.style of the login demo lines
	loginURLPath = "/api/v1/platform/tts/login/"
)

// LoginSpeed: voice_settings.speed of the login demo (ElevenLabs: 0.7-1.2,
// 1.0 normal). ELEVENLABS_LOGIN_SPEED sets another one.
var LoginSpeed = 1.12

func init() {
	if v, err := strconv.ParseFloat(strings.TrimSpace(os.Getenv("ELEVENLABS_LOGIN_SPEED")), 64); err == nil && v >= 0.7 && v <= 1.2 {
		LoginSpeed = v
	}
}

type loginJob struct {
	Running bool
	VoiceID string // voice id + settings sig: what is being read
	Done    int
	Total   int
	Error   string
	Stopped string
	StopAt  time.Time
}

type loginOverlay struct {
	ver  string
	m    map[string]string
	keys map[string]bool
}

// SetLoginTexts: where the login demo lines come from (web.LoginLines).
func (p *PremiumVoice) SetLoginTexts(f func() []string) {
	p.mu.Lock()
	p.loginTexts = f
	p.mu.Unlock()
	p.Kick()
}

func (p *PremiumVoice) loginLines() []string {
	p.mu.Lock()
	f := p.loginTexts
	p.mu.Unlock()
	if f == nil {
		return nil
	}
	var out []string
	seen := map[string]bool{}
	for _, t := range f() {
		t = strings.Join(strings.Fields(t), " ")
		if t != "" && !seen[t] {
			seen[t] = true
			out = append(out, t)
		}
	}
	return out
}

// loginVoice: the voice the tour plays, at the demo's pace. Empty while the
// tour plays the built-in recordings.
func (p *PremiumVoice) loginVoice() premiumVoice {
	v := p.config().Active
	if v.ID == "" {
		return v
	}
	v.Settings.Speed = LoginSpeed
	return v
}

func loginSig(v premiumVoice) string { return v.ID + "|" + v.Model + "|" + v.Settings.Sig() }

// loginHave: which demo lines of voice v are kept (line → URL).
// R62: the normalized copy first (lookup); a line made before the
// pronunciation dictionary is not ready until it is read again.
func (p *PremiumVoice) loginHave(ctx context.Context, v premiumVoice, texts []string) (map[string]string, error) {
	m, _, err := p.lookup(ctx, v, texts, loginURLPath, false)
	return m, err
}

func loginChars(texts []string) int {
	n := 0
	for _, t := range texts {
		n += len([]rune(t))
	}
	return n
}

// refreshLoginOverlay: the demo map of the tour's voice, only when every
// line is there (else the previous map, or the bundled files, keep playing).
func (p *PremiumVoice) refreshLoginOverlay(ctx context.Context) {
	if p.repo == nil {
		return
	}
	v, texts := p.loginVoice(), p.loginLines()
	if v.ID == "" || len(texts) == 0 {
		p.loginOver.Store(nil)
		return
	}
	// R62: a line read again keeps its earlier file meanwhile (withOld)
	m, _, err := p.lookup(ctx, v, texts, loginURLPath, true)
	if err != nil || len(m) < len(texts) {
		return
	}
	urls := make([]string, 0, len(m))
	keys := map[string]bool{}
	for _, u := range m {
		urls = append(urls, u)
		keys[strings.TrimSuffix(strings.TrimPrefix(u, loginURLPath), ".mp3")] = true
	}
	sort.Strings(urls)
	h := sha256.Sum256([]byte(strings.Join(urls, "\n")))
	ver := "ell-" + hex.EncodeToString(h[:6])
	if o := p.loginOver.Load(); o != nil && o.ver == ver {
		return
	}
	p.loginOver.Store(&loginOverlay{ver: ver, m: m, keys: keys})
	log.Printf("tts premium: the login demo speaks with %s now (%d lines, speed %.2f)", v.Name, len(m), v.Settings.Speed)
}

// LoginOverlay: line → URL of the login demo and its version, for the login
// page (web.LoginVoiceOverlay). "" when the bundled recordings play.
func (p *PremiumVoice) LoginOverlay() (string, map[string]string) {
	o := p.loginOver.Load()
	if o == nil {
		return "", nil
	}
	return o.ver, o.m
}

// maybeRunLogin: the demo lines missing for the tour's voice are read in the
// background (after the tour itself: the tour's phrases go first).
func (p *PremiumVoice) maybeRunLogin(ctx context.Context) {
	if p.repo == nil {
		return
	}
	texts := p.loginLines()
	v := p.loginVoice()
	if len(texts) == 0 || v.ID == "" {
		p.refreshLoginOverlay(ctx)
		return
	}
	sig := loginSig(v)
	p.mu.Lock()
	job := p.login
	tourBusy := p.job.Running
	p.mu.Unlock()
	if job.VoiceID == sig && job.Running {
		return
	}
	m, err := p.loginHave(ctx, v, texts)
	if err != nil {
		return
	}
	if len(m) == len(texts) {
		p.mu.Lock()
		if p.login.VoiceID == sig || p.login.VoiceID == "" {
			p.login = loginJob{VoiceID: sig, Done: len(texts), Total: len(texts)}
		}
		p.mu.Unlock()
		p.refreshLoginOverlay(ctx)
		return
	}
	if p.key() == "" || tourBusy {
		return
	}
	if job.VoiceID == sig && job.Stopped != "" {
		hold := premiumHoldPlan
		if job.Stopped == "quota" {
			hold = premiumHoldQuota
		}
		if time.Since(job.StopAt) < hold {
			return
		}
	}
	p.mu.Lock()
	if p.login.Running && p.login.VoiceID == sig {
		p.mu.Unlock()
		return
	}
	p.login = loginJob{Running: true, VoiceID: sig, Done: len(m), Total: len(texts)}
	p.mu.Unlock()
	go p.readLogin(context.WithoutCancel(ctx), v, sig, texts, m)
}

func (p *PremiumVoice) readLogin(ctx context.Context, v premiumVoice, sig string, texts []string, have map[string]string) {
	var todo []string
	for _, t := range texts {
		if _, ok := have[t]; !ok {
			todo = append(todo, t)
		}
	}
	log.Printf("tts premium: login demo: reading %d of %d lines, %d characters (all %d: %d) with %s, speed %.2f",
		len(todo), len(texts), loginChars(todo), len(texts), loginChars(texts), v.Name, v.Settings.Speed)
	stop := func(why, msg string) {
		p.mu.Lock()
		if p.login.VoiceID == sig {
			p.login.Running, p.login.Stopped, p.login.Error, p.login.StopAt = false, why, msg, time.Now()
		}
		p.mu.Unlock()
	}
	for i, t := range todo {
		if loginSig(p.loginVoice()) != sig {
			stop("", "")
			return // the tour's voice changed: its own reading follows
		}
		if i > 0 {
			select {
			case <-ctx.Done():
				return
			case <-time.After(PremiumPace):
			}
		}
		actx, cancel := context.WithTimeout(ctx, 3*time.Minute)
		say := SpeakText(t) // R62: the stress marks go to the voice only
		audio, err := p.EL.Speak(actx, v.ID, v.Model, say, v.Settings)
		cancel()
		if err != nil {
			why := "net"
			switch ai.ElevenKind(err) {
			case ai.ElevenKindQuota:
				why = "quota"
			case ai.ElevenKindKey:
				why = "key"
			case ai.ElevenKindPerm:
				why = "perm"
			case ai.ElevenKindVoice:
				why = "voice"
			case ai.ElevenKindPlan, ai.ElevenKindAbuse:
				why = "plan"
			}
			log.Printf("tts premium: login demo: %s: stopped (%s): %v", v.Name, why, err)
			stop(why, ai.ElevenMessage(err))
			return
		}
		raw := premiumKey(v, say)
		if err := p.repo.PutTTSMime(ctx, raw, "elevenlabs:"+v.ID, loginStyle, t, "audio/mpeg", audio); err != nil {
			stop("net", "Запись не сохранилась в базе")
			return
		}
		if ff := p.ffBin(ctx); ff != "" {
			p.normalize(ctx, ff, raw, "elevenlabs:"+v.ID, loginStyle, t, "login line", audio)
		}
		p.mu.Lock()
		if p.login.VoiceID == sig {
			p.login.Done++
		}
		p.mu.Unlock()
	}
	p.mu.Lock()
	if p.login.VoiceID == sig {
		p.login = loginJob{VoiceID: sig, Done: len(texts), Total: len(texts)}
	}
	p.mu.Unlock()
	p.refreshLoginOverlay(ctx)
}

// loginState: the demo's readiness for the system check.
func (p *PremiumVoice) loginState(ctx context.Context) (ready, total int, on bool, running bool, stopped string) {
	texts := p.loginLines()
	total = len(texts)
	if total == 0 || p.repo == nil {
		return
	}
	_, m := p.LoginOverlay()
	on = len(m) > 0
	v := p.loginVoice()
	if v.ID == "" {
		return
	}
	if got, err := p.loginHave(ctx, v, texts); err == nil {
		ready = len(got)
	}
	p.mu.Lock()
	if p.login.VoiceID == loginSig(v) {
		running, stopped = p.login.Running, p.login.Stopped
	}
	p.mu.Unlock()
	return
}

// LoginFile: GET /tts/login/<key>.mp3: public, only the current demo's files.
func (p *PremiumVoice) LoginFile(c *gin.Context) {
	m := premiumFileRe.FindStringSubmatch(c.Param("file"))
	var o *loginOverlay
	if m != nil && p != nil && p.repo != nil {
		o = p.loginOver.Load()
	}
	if o == nil || !o.keys[m[1]] {
		c.Header("Cache-Control", "no-store")
		c.Status(http.StatusNotFound)
		return
	}
	etag := `"` + m[1] + `"`
	if c.GetHeader("If-None-Match") == etag {
		c.Status(http.StatusNotModified)
		return
	}
	data, mime, err := p.repo.GetTTSMime(c.Request.Context(), m[1])
	if err != nil || data == nil {
		c.Header("Cache-Control", "no-store")
		c.Status(http.StatusNotFound)
		return
	}
	if mime == "" {
		mime = "audio/mpeg"
	}
	c.Header("Cache-Control", "public, max-age=31536000, immutable")
	c.Header("ETag", etag)
	c.Header("X-Content-Type-Options", "nosniff")
	c.Data(http.StatusOK, mime, data)
}
