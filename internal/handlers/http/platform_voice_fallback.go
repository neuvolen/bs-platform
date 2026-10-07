package http

import (
	"context"
	"errors"
	"log"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/bnursik/business_surgery_backend/internal/ai"
)

// R39: «ElevenLabs 402 paid_plan_required: Free users cannot use library
// voices via the API». The owner's voice (ogi2DyUAKJb7CEdqqvlU) is a Voice
// Library voice; on the free plan the API reads only the premade voices.
//
// The chosen voice stays the target (premiumCfg.Voice). When ElevenLabs
// answers paid_plan_required for it, the tour is read with a premade voice
// (ELEVENLABS_FALLBACK_VOICE_ID, default Daniel: deep, calm, middle-aged male,
// eleven_multilingual_v2 reads Russian with it), kept in premiumCfg.Fallback.
// Once an hour the target is probed («Тест», 4 characters): after a plan
// upgrade it reads, the fallback is dropped and the tour is voiced again
// with the chosen voice by itself.
//
// Quota: the free plan has about 10 000 characters a month; the 33 tour
// phrases are counted (PremiumState.Chars). A reading stopped by the quota
// or the plan is not started again by the background loop for
// premiumHoldQuota / premiumHoldPlan (only «Продолжить» or a new key does).

// Premade voices good for a calm Russian guide (ids from ElevenLabs' premade list).
var elevenPremade = map[string]string{
	"onwK4e9ZLuTAKqWW03F9": "Daniel",
	"JBFqnCBsd6RMkjVDRZzb": "George",
	"nPczCjzI2devNBz1zQrb": "Brian",
}

const defaultFallbackVoice = "onwK4e9ZLuTAKqWW03F9" // Daniel

var (
	premiumHoldQuota  = 6 * time.Hour
	premiumHoldPlan   = time.Hour
	premiumProbeEvery = time.Hour
)

type premiumFallback struct {
	For     string       `json:"for"` // the target voice id it stands in for
	Voice   premiumVoice `json:"voice"`
	Why     string       `json:"why"` // paid_plan_required
	At      string       `json:"at"`
	Checked string       `json:"checked,omitempty"` // last probe of the target
}

func elevenFallbackVoice() string {
	if v := strings.TrimSpace(os.Getenv("ELEVENLABS_FALLBACK_VOICE_ID")); v != "" && elevenIDRe.MatchString(v) {
		return v
	}
	return defaultFallbackVoice
}

// isPaidPlanVoice: the plan does not let the API read this voice.
func isPaidPlanVoice(err error) bool {
	var ee *ai.ElevenError
	if !errors.As(err, &ee) {
		return false
	}
	low := strings.ToLower(ee.Code + " " + ee.Legacy + " " + ee.Message)
	return strings.Contains(low, "paid_plan_required") || strings.Contains(low, "library voices")
}

// effective: the voice the tour is read with now (the fallback, if it stands in).
func (c premiumCfg) effective() premiumVoice {
	if c.Fallback != nil && c.Fallback.For == c.Voice.ID && c.Voice.ID != "" {
		return c.Fallback.Voice
	}
	return c.Voice
}

func (c premiumCfg) fallbackOn() bool {
	return c.Fallback != nil && c.Fallback.For == c.Voice.ID && c.Voice.ID != ""
}

// useFallback: the target cannot be read on this plan: the premade voice stands in.
func (p *PremiumVoice) useFallback(ctx context.Context, target premiumVoice, why error) bool {
	c := p.config()
	if c.Voice.ID != target.ID || c.fallbackOn() {
		return false
	}
	id := elevenFallbackVoice()
	if id == target.ID {
		return false
	}
	name := elevenPremade[id]
	if name == "" {
		name = id
		cctx, cancel := context.WithTimeout(ctx, 20*time.Second)
		if v, err := p.EL.Voice(cctx, id); err == nil && strings.TrimSpace(v.Name) != "" {
			name = strings.TrimSpace(v.Name)
		}
		cancel()
	}
	now := time.Now().UTC().Format(time.RFC3339)
	c.Fallback = &premiumFallback{For: target.ID, Why: "paid_plan_required", At: now, Checked: now,
		Voice: premiumVoice{ID: id, Name: name, Model: target.Model, Settings: target.Settings}}
	if c.Fallback.Voice.Model == "" {
		c.Fallback.Voice.Model = ai.ElevenModel()
	}
	if err := p.saveCfg(context.WithoutCancel(ctx), c); err != nil {
		return false
	}
	log.Printf("tts premium: %s needs a paid plan (%v): the tour is read with %s until then", target.Name, why, name)
	return true
}

// recheckTarget: once an hour the chosen voice is probed; when it reads
// (the plan was upgraded) the fallback is dropped. true: it was dropped.
func (p *PremiumVoice) recheckTarget(ctx context.Context) bool {
	c := p.config()
	if !c.fallbackOn() {
		return false
	}
	// R57: the first pass after a start probes at once (a plan bought while
	// the server ran is seen at the next deploy or restart, not an hour later)
	forced := p.probeNow.Swap(false)
	if t, err := time.Parse(time.RFC3339, c.Fallback.Checked); !forced && err == nil && time.Since(t) < premiumProbeEvery {
		return false
	}
	cctx, cancel := context.WithTimeout(ctx, 40*time.Second)
	_, err := p.EL.Probe(cctx, "", c.Voice.ID, c.Voice.Model)
	cancel()
	c = p.config()
	if !c.fallbackOn() {
		return false
	}
	if err != nil {
		if isPaidPlanVoice(err) {
			c.Fallback.Checked = time.Now().UTC().Format(time.RFC3339)
			_ = p.saveCfg(ctx, c)
		}
		if forced {
			log.Printf("tts premium: start probe: %s still not readable (%v): %s stands in", c.Voice.Name, err, c.Fallback.Voice.Name)
		}
		return false
	}
	c.Fallback = nil
	if err := p.saveCfg(ctx, c); err != nil {
		return false
	}
	log.Printf("tts premium: %s reads now (the plan allows it): the tour is voiced with it again", c.Voice.Name)
	return true
}

// maybeRunAuto: the background loop's maybeRun: a reading stopped by the
// quota, the plan or the key is not started again before its hold is over.
func (p *PremiumVoice) maybeRunAuto(ctx context.Context) {
	p.recheckTarget(ctx)
	c := p.config()
	v := c.effective()
	p.mu.Lock()
	job := p.job
	p.mu.Unlock()
	if job.VoiceID == v.ID && !job.Running && job.Stopped != "" {
		hold := time.Duration(0)
		switch job.Stopped {
		case "quota":
			hold = premiumHoldQuota
		case "plan", "key", "perm", "voice":
			hold = premiumHoldPlan
		}
		if at, err := time.Parse(time.RFC3339, job.StopAt); hold > 0 && err == nil && time.Since(at) < hold {
			return
		}
	}
	p.maybeRun(ctx)
}

// tourChars: the characters of the tour phrases (what a full reading costs).
func (p *PremiumVoice) tourChars() int {
	n := 0
	for _, t := range p.texts() {
		n += len([]rune(t))
	}
	return n
}

// elevenPlanNote: the plan's terms, as ElevenLabs' help center states them
// («Can I publish the content I generate on the platform?»).
const elevenPlanNote = "Бесплатный тариф ElevenLabs: около 10 000 символов в месяц, через API доступны только стандартные голоса (голоса из библиотеки требуют платного тарифа). " +
	"Бесплатный тариф не даёт коммерческой лицензии и требует указывать ElevenLabs (elevenlabs.io) при публикации. Коммерческая лицензия входит во все платные тарифы, начиная со Starter."

// spaced: 12345 → «12 345» (the brand's number format).
func spaced(n int) string {
	s := strconv.Itoa(n)
	neg := strings.HasPrefix(s, "-")
	s = strings.TrimPrefix(s, "-")
	var b strings.Builder
	for i, r := range s {
		if i > 0 && (len(s)-i)%3 == 0 {
			b.WriteByte(' ')
		}
		b.WriteRune(r)
	}
	if neg {
		return "-" + b.String()
	}
	return b.String()
}
