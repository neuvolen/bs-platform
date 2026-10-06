package http

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/bnursik/business_surgery_backend/internal/ai"
	pg "github.com/bnursik/business_surgery_backend/internal/repository/pg"
	"github.com/gin-gonic/gin"
)

// R36: «Голос ElevenLabs»: a premium voice for the tour, picked by the owner.
//
// The tour plays the recordings built into the app (web/voice) until the
// owner adds an ElevenLabs key and picks a voice. Then the server reads
// every tour phrase with that voice, one by one at a calm pace (a 429 is
// waited out), keeps the MP3s in tts_audio and, once every phrase is there,
// the page gets them in its phrase → file map (web.VoiceOverlay) instead of
// the built-in files. The file name is a hash of voice, model, settings and
// text: a new voice or a changed phrase is a new URL, so caches update.
// While a new voice is being made the previous one (or the built-in files)
// keeps playing: the tour never mixes two voices.
//
// Admin only:
//
//	GET    /tts/premium              state: key (last 4), voice, progress
//	PUT    /tts/premium/key {key}    saves the key (sealed like the Claude key)
//	DELETE /tts/premium/key
//	POST   /tts/premium/key/test {key?}
//	GET    /tts/premium/voices?q=    «Подобрать голос»: the account's voices and the library
//	POST   /tts/premium/voice {id, name, ownerId?, previewUrl?}   pick: the reading starts
//	DELETE /tts/premium/voice        back to the built-in recordings
//	POST   /tts/premium/run          go on after a stop (the plan was topped up)
//
// Public: GET /api/v1/platform/tts/p/<key>.mp3, immutable.

const (
	elevenKeyDoc   = "ai_eleven_key" // scope server, sealed
	premiumCfgDoc  = "tts_premium"   // scope server
	premiumStyle   = "el1"           // tts_audio.style of the premium phrases
	premiumURLPath = "/api/v1/platform/tts/p/"
)

var premiumFileRe = regexp.MustCompile(`^(el_[0-9a-f]{48})\.mp3$`)

// PremiumPace: the pause between two phrases (ElevenLabs' concurrency limit
// is small on the cheaper plans). Tests shorten it.
var PremiumPace = 1200 * time.Millisecond

func init() {
	// ELEVENLABS_PACE_MS: another pace (a plan with more concurrency, a test stand)
	if v, err := strconv.Atoi(strings.TrimSpace(os.Getenv("ELEVENLABS_PACE_MS"))); err == nil && v >= 0 {
		PremiumPace = time.Duration(v) * time.Millisecond
	}
}

// PremiumRetryEvery: a stopped reading (no key yet, network) is tried again.
var PremiumRetryEvery = 15 * time.Minute

type premiumVoice struct {
	ID         string            `json:"id"`                  // the id speech is made with
	LibraryID  string            `json:"libraryId,omitempty"` // the library voice it was added from
	OwnerID    string            `json:"ownerId,omitempty"`
	Name       string            `json:"name"`
	PreviewURL string            `json:"previewUrl,omitempty"`
	Model      string            `json:"model"`
	Settings   ai.ElevenSettings `json:"settings"`
}

type premiumCfg struct {
	Voice  premiumVoice `json:"voice"`  // picked: being read or ready
	Active premiumVoice `json:"active"` // every phrase there: the page plays it
	By     string       `json:"by,omitempty"`
	At     string       `json:"at,omitempty"`
	// EnvVoice: the ELEVENLABS_VOICE_ID already taken (R36c): a voice the
	// owner picks in the settings later is not overridden at every start.
	EnvVoice string `json:"envVoice,omitempty"`
	// Fallback: a premade voice reads the tour while the chosen one needs a
	// paid plan (R39, platform_voice_fallback.go).
	Fallback *premiumFallback `json:"fallback,omitempty"`
}

type premiumOverlay struct {
	ver string
	m   map[string]string
}

type premiumJob struct {
	Running bool   `json:"running"`
	VoiceID string `json:"voiceId,omitempty"`
	Done    int    `json:"done"`
	Total   int    `json:"total"`
	Error   string `json:"error,omitempty"`
	Stopped string `json:"stopped,omitempty"` // quota | key | voice | net
	At      string `json:"at,omitempty"`
	StopAt  string `json:"stopAt,omitempty"` // when it stopped (R39: the background loop waits)
}

// PremiumVoice: the ElevenLabs voice of the tour.
type PremiumVoice struct {
	repo   *pg.PlatformRepo
	EL     *ai.Eleven
	Keys   *ai.KeyBox
	Secret func() []byte
	Texts  func() []string
	// OnReady: the new voice is ready (the owner gets a note through the bot).
	OnReady func(ctx context.Context, name string)

	mu      sync.Mutex
	cfg     premiumCfg
	job     premiumJob
	cancel  context.CancelFunc
	overlay atomic.Pointer[premiumOverlay]
	kick    chan struct{}
	envErr  string // why ELEVENLABS_VOICE_ID is not used (R36c)
	envTry  struct {
		sig string // last4 of the key | voice id of the last try
		at  time.Time
	}
	// R40d: the login page demo, read with the tour's voice (platform_voice_login.go)
	loginTexts func() []string
	login      loginJob
	loginOver  atomic.Pointer[loginOverlay]
	// R52: when the tour's map was last looked for (Overlay finds a map the
	// start missed: the tour texts are wired after Load)
	overlayTry atomic.Int64
}

// NewPremiumVoice: the ElevenLabs client reads ELEVENLABS_API_KEY, else the saved key.
func NewPremiumVoice(repo *pg.PlatformRepo, secret func() []byte, texts func() []string) *PremiumVoice {
	p := &PremiumVoice{repo: repo, Keys: &ai.KeyBox{}, Secret: secret, Texts: texts, kick: make(chan struct{}, 1)}
	p.EL = ai.NewEleven(p.key)
	return p
}

func elevenEnvKey() string { return strings.TrimSpace(os.Getenv("ELEVENLABS_API_KEY")) }

// elevenEnvVoice: ELEVENLABS_VOICE_ID in Railway: the tour is read with this
// voice without picking it in the settings (R36c).
func elevenEnvVoice() string {
	if v := strings.TrimSpace(os.Getenv("ELEVENLABS_VOICE_ID")); v != "" {
		return v
	}
	return defaultElevenVoice
}

// defaultElevenVoice: the voice the owner chose in ElevenLabs (05.10.2026),
// used when ELEVENLABS_VOICE_ID is not set; only the API key is needed.
const defaultElevenVoice = "ogi2DyUAKJb7CEdqqvlU"

var voiceIDShape = regexp.MustCompile(`^[A-Za-z0-9]{20}$`)

// keyProblem: a voice id pasted where the key goes (keys start with sk_).
func keyProblem() string {
	if k := elevenEnvKey(); k != "" && voiceIDShape.MatchString(k) {
		return "в ELEVENLABS_API_KEY вставлен id голоса, а не ключ: ключ начинается с sk_"
	}
	return ""
}

func (p *PremiumVoice) setEnvErr(s string) {
	p.mu.Lock()
	p.envErr = s
	p.mu.Unlock()
}

// applyEnvVoice: ELEVENLABS_VOICE_ID becomes the picked voice once (a voice
// of the account, or a library voice added to it); the reading then starts
// by itself (maybeRun). The reason it cannot is kept for the system check.
func (p *PremiumVoice) applyEnvVoice(ctx context.Context) {
	id := elevenEnvVoice()
	if id == "" {
		p.setEnvErr("")
		return
	}
	if !elevenIDRe.MatchString(id) {
		p.setEnvErr("ELEVENLABS_VOICE_ID не похож на id голоса")
		return
	}
	if kp := keyProblem(); kp != "" {
		p.setEnvErr(kp)
		return
	}
	if p.key() == "" {
		if strings.TrimSpace(os.Getenv("ELEVENLABS_VOICE_ID")) == "" {
			p.setEnvErr("голос выбран, нужен ключ ElevenLabs: добавьте ELEVENLABS_API_KEY в Railway (или ключ в Настройках платформы)")
		} else {
			p.setEnvErr("в Railway указан ELEVENLABS_VOICE_ID, но нет ключа: добавьте ELEVENLABS_API_KEY (или ключ в Настройках платформы)")
		}
		return
	}
	c := p.config()
	if c.EnvVoice == id {
		p.setEnvErr("")
		return
	}
	if p.repo == nil {
		return
	}
	if c.Voice.ID == id || c.Voice.LibraryID == id {
		c.EnvVoice = id
		if err := p.saveCfg(ctx, c); err == nil {
			p.setEnvErr("")
		}
		return
	}
	// R38c: a failed try is not repeated every 15 minutes with the same key
	// and voice (each try reads «Тест»): once an hour, or at once after a new
	// key or «Проверить».
	sig := ai.Last4(p.key()) + "|" + id
	p.mu.Lock()
	if p.envTry.sig == sig && time.Since(p.envTry.at) < time.Hour {
		p.mu.Unlock()
		return
	}
	p.envTry.sig, p.envTry.at = sig, time.Now()
	p.mu.Unlock()
	cctx, cancel := context.WithTimeout(ctx, 40*time.Second)
	defer cancel()
	v, libID, ownerID, msg := p.resolveVoice(cctx, id)
	if msg != "" {
		p.setEnvErr(msg)
		return
	}
	name := strings.TrimSpace(v.Name)
	if r := []rune(name); len(r) > 60 {
		name = string(r[:60])
	}
	if name == "" {
		name = id
	}
	prev := v.PreviewURL
	if !strings.HasPrefix(prev, "https://") || len(prev) > 500 {
		prev = ""
	}
	c = p.config()
	c.Voice = premiumVoice{ID: v.ID, LibraryID: libID, OwnerID: ownerID, Name: name, PreviewURL: prev, Model: ai.ElevenModel(), Settings: ai.DefaultElevenSettings}
	c.EnvVoice, c.By, c.At = id, "ELEVENLABS_VOICE_ID", time.Now().UTC().Format(time.RFC3339)
	if err := p.saveCfg(ctx, c); err != nil {
		p.setEnvErr("выбор голоса не сохранился в базе")
		return
	}
	p.setEnvErr("")
	log.Printf("tts premium: voice %s (%s) taken from ELEVENLABS_VOICE_ID", name, v.ID)
}

// resolveVoice: the voice id made usable with the fewest permissions (R38c).
//
//  1. «Тест» is read with the id directly: only «Text to Speech» is needed,
//     and a library voice usually speaks so without being added.
//  2. Only when ElevenLabs answers voice_not_found the voice is looked up in
//     the library and added to the account (that needs «Voices: Write»).
//
// The name of the voice is a nicety: read when the key may («Voices: Read»),
// never required. msg is the reason in words when the voice cannot be used.
func (p *PremiumVoice) resolveVoice(ctx context.Context, id string) (v ai.ElevenVoice, libID, ownerID, msg string) {
	_, err := p.EL.Probe(ctx, "", id, ai.ElevenModel())
	// R39: paid_plan_required: the voice exists, the plan does not let the API
	// read it yet: it stays the chosen voice and a premade one stands in
	if err == nil || isPaidPlanVoice(err) {
		v = ai.ElevenVoice{ID: id}
		if got, verr := p.EL.Voice(ctx, id); verr == nil {
			v.Name, v.PreviewURL = got.Name, got.PreviewURL
		} else if id == defaultElevenVoice {
			v.Name = "голос владельца" // the key may not read voices: the name is not needed
		}
		return v, "", "", ""
	}
	if !ai.IsElevenVoiceMissing(err) {
		return v, "", "", ai.ElevenMessage(err)
	}
	sv, ok, ferr := p.EL.FindShared(ctx, id)
	if ferr != nil {
		return v, "", "", "голос " + id + " не в вашем аккаунте ElevenLabs, а найти его в библиотеке не вышло: " + ai.ElevenMessage(ferr) +
			". Проще всего: elevenlabs.io → Voices → Voice Library → найдите голос → «Add to my voices»"
	}
	if !ok || sv.OwnerID == "" {
		return v, "", "", "голос " + id + " не найден ни в вашем аккаунте ElevenLabs, ни в библиотеке: проверьте id (Voices → ⋯ → Copy voice ID)"
	}
	got, aerr := p.EL.AddShared(ctx, sv.OwnerID, sv.ID, "BS гид: "+sv.Name)
	if aerr != nil {
		return v, "", "", "голос «" + sv.Name + "» из библиотеки нужно добавить в аккаунт ElevenLabs, а ключ не смог: " + ai.ElevenMessage(aerr) +
			". Или добавьте его вручную: Voices → Voice Library → «" + sv.Name + "» → «Add to my voices»"
	}
	if _, err := p.EL.Probe(ctx, "", got, ai.ElevenModel()); err != nil {
		return v, "", "", "голос «" + sv.Name + "» добавлен, но не читает: " + ai.ElevenMessage(err)
	}
	return ai.ElevenVoice{ID: got, Name: sv.Name, PreviewURL: sv.PreviewURL}, sv.ID, sv.OwnerID, ""
}

func (p *PremiumVoice) key() string {
	if k := elevenEnvKey(); k != "" {
		return k
	}
	return p.Keys.Get()
}

// KeySource: env | settings | "".
func (p *PremiumVoice) KeySource() string {
	switch {
	case elevenEnvKey() != "":
		return "env"
	case p.Keys.Get() != "":
		return "settings"
	}
	return ""
}

func premiumKey(v premiumVoice, text string) string {
	s := sha256.Sum256([]byte(premiumStyle + "\x00" + v.ID + "\x00" + v.Model + "\x00" + v.Settings.Sig() + "\x00" + text))
	return "el_" + hex.EncodeToString(s[:])[:48]
}

func (p *PremiumVoice) texts() []string {
	if p.Texts == nil {
		return nil
	}
	var out []string
	seen := map[string]bool{}
	for _, t := range p.Texts() {
		t = strings.Join(strings.Fields(t), " ")
		if t != "" && !seen[t] {
			seen[t] = true
			out = append(out, t)
		}
	}
	return out
}

// Load reads the saved key and the choice (at start).
func (p *PremiumVoice) Load(ctx context.Context) {
	if p.repo == nil {
		return
	}
	if d, err := p.repo.GetDoc(ctx, "server", elevenKeyDoc); err == nil && d != nil && !d.Deleted && d.Value != "" {
		if k, err := ai.Open(p.Secret(), d.Value); err == nil {
			p.Keys.Set(k)
		} else {
			log.Printf("tts premium: the saved ElevenLabs key cannot be read: %v", err)
		}
	}
	if d, err := p.repo.GetDoc(ctx, "server", premiumCfgDoc); err == nil && d != nil && !d.Deleted && d.Value != "" {
		var c premiumCfg
		if json.Unmarshal([]byte(d.Value), &c) == nil {
			p.mu.Lock()
			p.cfg = c
			p.mu.Unlock()
		}
	}
	p.refreshOverlay(ctx)
	p.refreshLoginOverlay(ctx)
}

func (p *PremiumVoice) config() premiumCfg {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.cfg
}

func (p *PremiumVoice) saveCfg(ctx context.Context, c premiumCfg) error {
	b, _ := json.Marshal(c)
	if !putServerDoc(ctx, p.repo, premiumCfgDoc, string(b), false, c.By) {
		return errStore
	}
	p.mu.Lock()
	p.cfg = c
	p.mu.Unlock()
	return nil
}

var errStore = errors.New("store failed")

// putServerDoc: a scope "server" doc (never synced to a page).
func putServerDoc(ctx context.Context, repo *pg.PlatformRepo, key, val string, deleted bool, by string) bool {
	if repo == nil {
		return false
	}
	for try := 0; try < 4; try++ {
		ver := 0
		if d, err := repo.GetDoc(ctx, "server", key); err == nil && d != nil {
			ver = d.Version
		}
		if ver == 0 && deleted {
			return true
		}
		if _, err := repo.PutDoc(ctx, "server", key, ver, val, deleted, by); err == nil {
			return true
		}
	}
	return false
}

// have: which phrases of voice v are kept.
func (p *PremiumVoice) have(ctx context.Context, v premiumVoice, texts []string) (map[string]string, int, error) {
	if p.repo == nil {
		return map[string]string{}, 0, errors.New("no database")
	}
	keys := make([]string, len(texts))
	for i, t := range texts {
		keys[i] = premiumKey(v, t)
	}
	got, err := p.repo.TTSHave(ctx, keys)
	if err != nil {
		return nil, 0, err
	}
	m := map[string]string{}
	for i, t := range texts {
		if got[keys[i]] {
			m[t] = premiumURLPath + keys[i] + ".mp3"
		}
	}
	return m, len(m), nil
}

// refreshOverlay: the page's phrase → file map of the active voice.
func (p *PremiumVoice) refreshOverlay(ctx context.Context) {
	c := p.config()
	if c.Active.ID == "" || p.repo == nil {
		p.overlay.Store(nil)
		return
	}
	texts := p.texts()
	if len(texts) == 0 {
		// R52: the tour's phrases are not wired yet (Load runs before
		// app.go sets TourTexts): keep the last map, look again later
		return
	}
	m, n, err := p.have(ctx, c.Active, texts)
	if err != nil {
		return // keep the last map
	}
	if n == 0 {
		p.overlay.Store(nil)
		return
	}
	urls := make([]string, 0, len(m))
	for _, u := range m {
		urls = append(urls, u)
	}
	sort.Strings(urls)
	h := sha256.Sum256([]byte(strings.Join(urls, "\n")))
	p.overlay.Store(&premiumOverlay{ver: "el-" + hex.EncodeToString(h[:6]), m: m})
}

// Overlay: phrase → URL of the active premium voice and its version, for
// the page (web.VoiceOverlay). "" when the built-in recordings play.
func (p *PremiumVoice) Overlay() (string, map[string]string) {
	o := p.overlay.Load()
	if o == nil {
		// R52: the server started with the voice ready, but the map was
		// looked for before the tour's phrases were known: the login demo
		// spoke with ElevenLabs, the tour kept the built-in files. Look
		// again, at most every 20 s (a page request costs one query then).
		if c := p.config(); c.Active.ID != "" && p.repo != nil {
			now := time.Now().UnixNano()
			if last := p.overlayTry.Load(); now-last > int64(20*time.Second) && p.overlayTry.CompareAndSwap(last, now) {
				ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
				p.refreshOverlay(ctx)
				cancel()
				o = p.overlay.Load()
			}
		}
		if o == nil {
			return "", nil
		}
	}
	return o.ver, o.m
}

// State: the provider and how many tour phrases it has (the system check).
type PremiumState struct {
	On        bool   // a premium voice plays
	Voice     string // its name
	Picked    string // the voice being read now, if another
	Ready     int
	Total     int
	Running   bool
	Error     string
	Stopped   string // quota | key | voice | net
	KeySource string
	// EnvError: why ELEVENLABS_VOICE_ID / ELEVENLABS_API_KEY are not used.
	EnvError string
	// R39: a premade voice reads while the chosen one (Target) needs a paid plan
	Fallback bool
	Target   string
	Chars    int // characters of all tour phrases
	// R40d: the login page demo read with the tour's voice
	LoginReady, LoginTotal int
	LoginOn                bool // the page plays the ElevenLabs lines
	LoginRunning           bool
	LoginStopped           string
}

func (p *PremiumVoice) State(ctx context.Context) PremiumState {
	c := p.config()
	texts := p.texts()
	st := PremiumState{Total: len(texts), KeySource: p.KeySource()}
	p.mu.Lock()
	job, envErr := p.job, p.envErr
	p.mu.Unlock()
	st.Running, st.Error, st.Stopped, st.EnvError = job.Running, job.Error, job.Stopped, envErr
	if kp := keyProblem(); kp != "" {
		st.EnvError = kp
	}
	eff := c.effective()
	st.Chars = p.tourChars()
	st.LoginReady, st.LoginTotal, st.LoginOn, st.LoginRunning, st.LoginStopped = p.loginState(ctx)
	if c.fallbackOn() {
		st.Fallback, st.Target = true, c.Voice.Name
	}
	if job.VoiceID != eff.ID {
		st.Running, st.Error, st.Stopped = false, "", ""
	}
	if c.Active.ID != "" {
		_, n, _ := p.have(ctx, c.Active, texts)
		st.On, st.Voice, st.Ready = n > 0, c.Active.Name, n
	}
	if eff.ID != "" && eff.ID != c.Active.ID {
		st.Picked = eff.Name
		if !st.On {
			_, n, _ := p.have(ctx, eff, texts)
			st.Ready = n
		}
	}
	return st
}

// Start: resume an unfinished reading at start and every PremiumRetryEvery.
func (p *PremiumVoice) Start(ctx context.Context) {
	if p.repo == nil {
		return
	}
	go func() {
		t := time.NewTicker(PremiumRetryEvery)
		defer t.Stop()
		for {
			p.applyEnvVoice(ctx)
			if p.overlay.Load() == nil {
				p.refreshOverlay(ctx) // R52: the voice was ready before this start
			}
			p.maybeRunAuto(ctx) // R39: no new try right after a quota or plan stop
			p.maybeRunLogin(ctx) // R40d: the login demo after the tour
			select {
			case <-ctx.Done():
				return
			case <-t.C:
			case <-p.kick:
			}
		}
	}()
}

// maybeRun starts the reading when the picked voice is missing phrases.
func (p *PremiumVoice) maybeRun(ctx context.Context) {
	c := p.config()
	v := c.effective() // R39: the premade voice while the chosen one needs a paid plan
	if v.ID == "" || p.key() == "" {
		return
	}
	p.mu.Lock()
	running := p.job.Running && p.job.VoiceID == v.ID
	p.mu.Unlock()
	if running {
		return
	}
	texts := p.texts()
	if _, n, err := p.have(ctx, v, texts); err == nil && n == len(texts) {
		if c.Active.ID != v.ID {
			p.finish(ctx, v)
		} else if o := p.overlay.Load(); o == nil || len(o.m) < n {
			p.refreshOverlay(ctx) // R52: ready before this start: the page plays it too
		}
		return
	}
	p.run(ctx, v)
}

// run reads every missing phrase with v in the background.
func (p *PremiumVoice) run(parent context.Context, v premiumVoice) {
	p.mu.Lock()
	if p.cancel != nil {
		p.cancel()
	}
	ctx, cancel := context.WithCancel(context.WithoutCancel(parent))
	p.cancel = cancel
	texts := p.texts()
	p.job = premiumJob{Running: true, VoiceID: v.ID, Total: len(texts), At: time.Now().UTC().Format(time.RFC3339)}
	p.mu.Unlock()
	go func() {
		defer cancel()
		p.read(ctx, v, texts)
	}()
}

func (p *PremiumVoice) progress(f func(j *premiumJob)) {
	p.mu.Lock()
	f(&p.job)
	p.mu.Unlock()
}

func (p *PremiumVoice) read(ctx context.Context, v premiumVoice, texts []string) {
	have, n, err := p.have(ctx, v, texts)
	if err != nil {
		p.progress(func(j *premiumJob) { j.Running, j.Error, j.Stopped = false, "Нет связи с базой", "net" })
		return
	}
	p.progress(func(j *premiumJob) { j.Done = n })
	made := 0
	for _, t := range texts {
		if _, ok := have[t]; ok {
			continue
		}
		if ctx.Err() != nil || p.config().effective().ID != v.ID {
			p.progress(func(j *premiumJob) {
				if j.VoiceID == v.ID {
					j.Running = false
				}
			})
			return // another voice was picked: its own reading goes on
		}
		if made > 0 {
			select {
			case <-ctx.Done():
				return
			case <-time.After(PremiumPace):
			}
		}
		actx, acancel := context.WithTimeout(ctx, 3*time.Minute)
		audio, err := p.EL.Speak(actx, v.ID, v.Model, t, v.Settings)
		acancel()
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
			msg := ai.ElevenMessage(err)
			log.Printf("tts premium: %s: stopped at a phrase (%s): %v", v.Name, why, err)
			stop := time.Now().UTC().Format(time.RFC3339)
			p.progress(func(j *premiumJob) {
				if j.VoiceID == v.ID {
					j.Running, j.Error, j.Stopped, j.StopAt = false, msg, why, stop
				}
			})
			// R39: the chosen voice needs a paid plan: a premade voice reads the tour now
			if isPaidPlanVoice(err) && p.useFallback(ctx, v, err) {
				p.Kick()
			}
			return
		}
		if err := p.repo.PutTTSMime(context.WithoutCancel(ctx), premiumKey(v, t), "elevenlabs:"+v.ID, premiumStyle, t, "audio/mpeg", audio); err != nil {
			p.progress(func(j *premiumJob) {
				j.Running, j.Error, j.Stopped = false, "Запись не сохранилась в базе", "net"
			})
			return
		}
		made++
		p.progress(func(j *premiumJob) {
			if j.VoiceID == v.ID {
				j.Done++
			}
		})
	}
	p.finish(ctx, v)
}

// finish: every phrase of v is there: the page plays it from now on.
func (p *PremiumVoice) finish(ctx context.Context, v premiumVoice) {
	ctx = context.WithoutCancel(ctx)
	c := p.config()
	if c.effective().ID != v.ID {
		return
	}
	changed := c.Active.ID != v.ID || c.Active.Model != v.Model || c.Active.Settings != v.Settings
	if changed {
		c.Active = v
		if err := p.saveCfg(ctx, c); err != nil {
			log.Printf("tts premium: the ready voice not saved: %v", err)
		}
	}
	p.progress(func(j *premiumJob) {
		if j.VoiceID == v.ID {
			j.Running, j.Error, j.Stopped = false, "", ""
			j.Done = j.Total
		}
	})
	p.refreshOverlay(ctx)
	p.maybeRunLogin(ctx) // R40d: the login demo follows the tour's voice
	if changed {
		log.Printf("tts premium: the tour speaks with %s now", v.Name)
		if p.OnReady != nil {
			p.OnReady(ctx, v.Name)
		}
	}
}

// ── HTTP ──

func (p *PremiumVoice) view(ctx context.Context) gin.H {
	c := p.config()
	texts := p.texts()
	src := p.KeySource()
	out := gin.H{"source": src, "envSet": elevenEnvKey() != "", "saved": p.Keys.Get() != "", "model": ai.ElevenModel(), "total": len(texts)}
	switch src {
	case "env":
		out["last4"] = ai.Last4(elevenEnvKey())
	case "settings":
		out["last4"] = ai.Last4(p.Keys.Get())
	}
	p.mu.Lock()
	job, envErr := p.job, p.envErr
	p.mu.Unlock()
	if kp := keyProblem(); kp != "" {
		envErr = kp
	}
	if v := elevenEnvVoice(); v != "" {
		out["envVoice"] = v
	}
	if envErr != "" {
		out["envError"] = envErr
	}
	if c.Voice.ID != "" {
		_, n, _ := p.have(ctx, c.Voice, texts)
		out["voice"] = gin.H{"id": c.Voice.ID, "libraryId": c.Voice.LibraryID, "name": c.Voice.Name, "previewUrl": c.Voice.PreviewURL}
		out["ready"] = n
	}
	if c.Active.ID != "" {
		out["active"] = gin.H{"id": c.Active.ID, "name": c.Active.Name}
	}
	// R39: the premade voice that reads while the chosen one needs a paid plan,
	// the cost of a full reading and the plan's terms (ElevenLabs help center)
	out["chars"] = p.tourChars()
	out["planNote"] = elevenPlanNote
	if c.fallbackOn() {
		eff := c.effective()
		_, n, _ := p.have(ctx, eff, texts)
		out["fallback"] = gin.H{"id": eff.ID, "name": eff.Name, "why": c.Fallback.Why, "since": c.Fallback.At, "ready": n}
	}
	// R40d: the login page demo, read with the same voice
	if lr, lt, lon, lrun, _ := p.loginState(ctx); lt > 0 {
		out["login"] = gin.H{"ready": lr, "total": lt, "on": lon, "running": lrun, "chars": loginChars(p.loginLines()), "speed": LoginSpeed}
	}
	if ver, m := p.Overlay(); ver != "" {
		out["playing"], out["ver"] = "premium", ver
		// a file of the voice playing: the page compares it with its own map
		// and offers to reload when it still has the earlier one
		for _, t := range texts {
			if u := m[t]; u != "" {
				out["sample"] = u
				break
			}
		}
	} else {
		out["playing"] = "builtin"
	}
	if job.VoiceID != "" && job.VoiceID == c.effective().ID {
		out["job"] = job
		if job.Running && job.Done > 0 {
			out["ready"] = job.Done
		}
	}
	return out
}

// Get: GET /tts/premium
func (p *PremiumVoice) Get(c *gin.Context) {
	if !adminOnly(c) {
		return
	}
	c.Header("Cache-Control", "no-store")
	c.JSON(http.StatusOK, p.view(c.Request.Context()))
}

// PutKey: PUT /tts/premium/key {key}
func (p *PremiumVoice) PutKey(c *gin.Context) {
	if !adminOnly(c) {
		return
	}
	var in struct {
		Key string `json:"key"`
	}
	if err := c.ShouldBindJSON(&in); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "bad_json"})
		return
	}
	key := strings.TrimSpace(in.Key)
	if !ai.ValidKeyShape(key) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "bad_key", "message": "Это не похоже на ключ ElevenLabs: скопируйте его целиком из elevenlabs.io → Developers → API Keys"})
		return
	}
	sealed, err := ai.Seal(p.Secret(), key)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "seal_failed"})
		return
	}
	if !putServerDoc(c.Request.Context(), p.repo, elevenKeyDoc, sealed, false, platformUser(c)) {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "store_failed"})
		return
	}
	p.Keys.Set(key)
	log.Printf("tts premium: an ElevenLabs key %s saved by %s", ai.Last4(key), platformUser(c))
	p.Kick()
	c.JSON(http.StatusOK, p.view(c.Request.Context()))
}

// DeleteKey: DELETE /tts/premium/key
func (p *PremiumVoice) DeleteKey(c *gin.Context) {
	if !adminOnly(c) {
		return
	}
	if !putServerDoc(c.Request.Context(), p.repo, elevenKeyDoc, "", true, platformUser(c)) {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "store_failed"})
		return
	}
	p.Keys.Set("")
	log.Printf("tts premium: the saved ElevenLabs key removed by %s", platformUser(c))
	c.JSON(http.StatusOK, p.view(c.Request.Context()))
}

// TestKey: POST /tts/premium/key/test {key?}
func (p *PremiumVoice) TestKey(c *gin.Context) {
	if !adminOnly(c) {
		return
	}
	var in struct {
		Key string `json:"key"`
	}
	_ = c.ShouldBindJSON(&in)
	key := strings.TrimSpace(in.Key)
	if key != "" && !ai.ValidKeyShape(key) {
		c.JSON(http.StatusOK, gin.H{"ok": false, "message": "Это не похоже на ключ ElevenLabs"})
		return
	}
	ctx, cancel := context.WithTimeout(c.Request.Context(), 40*time.Second)
	defer cancel()
	// R38c: the check is the real thing: «Тест» read with the voice the tour
	// uses (needs only «Text to Speech»). The plan's characters are read
	// when the key may («User: Read»), never required.
	cfg := p.config()
	voice, vname := cfg.Voice.ID, cfg.Voice.Name
	if voice == "" {
		voice, vname = elevenEnvVoice(), ""
	}
	n, err := p.EL.Probe(ctx, key, voice, ai.ElevenModel())
	if err != nil {
		msg := ai.ElevenMessage(err)
		if ai.IsElevenVoiceMissing(err) {
			msg = "Ключ принят, но голоса " + voice + " нет в вашем аккаунте ElevenLabs. Сервер добавит его из библиотеки сам, если у ключа есть право «Voices: Write»; или добавьте голос вручную: Voices → Voice Library → «Add to my voices». " + msg
		}
		c.JSON(http.StatusOK, gin.H{"ok": false, "message": msg, "kind": ai.ElevenKind(err)})
		return
	}
	who := "голос " + voice
	if strings.HasPrefix(vname, "голос ") {
		who = vname
	} else if vname != "" {
		who = "голос «" + vname + "»"
	}
	msg := "Ключ работает: " + who + " прочитал «" + ai.ProbeText + "» (" + strconv.Itoa((n+1023)/1024) + " КБ звука)"
	out := gin.H{"ok": true, "voice": voice, "bytes": n}
	if plan, perr := p.EL.Subscription(ctx, key); perr == nil {
		if plan.Tier != "" {
			msg += ", тариф " + plan.Tier
			out["tier"] = plan.Tier
		}
		if plan.Limit > 0 {
			msg += ", осталось символов " + fmtThousands(float64(plan.Limit-plan.Used)) + " из " + fmtThousands(float64(plan.Limit))
			out["left"] = plan.Limit - plan.Used
		}
	}
	out["message"] = msg
	if key == "" || key == p.key() {
		// the env voice is tried again at once (not in an hour)
		p.mu.Lock()
		p.envTry.sig = ""
		p.mu.Unlock()
		p.Kick()
	}
	c.JSON(http.StatusOK, out)
}

// Voices: GET /tts/premium/voices?q=
func (p *PremiumVoice) Voices(c *gin.Context) {
	if !adminOnly(c) {
		return
	}
	ctx, cancel := context.WithTimeout(c.Request.Context(), 40*time.Second)
	defer cancel()
	q := strings.TrimSpace(c.Query("q"))
	if len([]rune(q)) > 60 {
		q = string([]rune(q)[:60])
	}
	mine, lib, err := p.EL.FindButlerVoices(ctx, q, 24)
	if err != nil {
		c.JSON(http.StatusOK, gin.H{"ok": false, "message": ai.ElevenMessage(err)})
		return
	}
	if mine == nil {
		mine = []ai.ElevenVoice{}
	}
	if lib == nil {
		lib = []ai.ElevenVoice{}
	}
	c.Header("Cache-Control", "no-store")
	c.JSON(http.StatusOK, gin.H{"ok": true, "mine": mine, "library": lib, "picked": p.config().Voice.LibraryID + "|" + p.config().Voice.ID})
}

var elevenIDRe = regexp.MustCompile(`^[A-Za-z0-9_-]{4,64}$`)

// Pick: POST /tts/premium/voice {id, name, ownerId?, previewUrl?}
func (p *PremiumVoice) Pick(c *gin.Context) {
	if !adminOnly(c) {
		return
	}
	var in struct {
		ID         string `json:"id"`
		Name       string `json:"name"`
		OwnerID    string `json:"ownerId"`
		PreviewURL string `json:"previewUrl"`
	}
	if err := c.ShouldBindJSON(&in); err != nil || !elevenIDRe.MatchString(in.ID) || (in.OwnerID != "" && !elevenIDRe.MatchString(in.OwnerID)) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "bad_voice"})
		return
	}
	if p.key() == "" {
		c.JSON(http.StatusOK, gin.H{"ok": false, "message": ai.ErrNoElevenKey.Error()})
		return
	}
	name := strings.TrimSpace(in.Name)
	if r := []rune(name); len(r) > 60 {
		name = string(r[:60])
	}
	if name == "" {
		name = in.ID
	}
	prev := strings.TrimSpace(in.PreviewURL)
	if !strings.HasPrefix(prev, "https://") || len(prev) > 500 {
		prev = ""
	}
	ctx, cancel := context.WithTimeout(c.Request.Context(), 40*time.Second)
	defer cancel()
	id := in.ID
	if in.OwnerID != "" {
		// a library voice goes into the account first: the API speaks only with those
		got, err := p.EL.AddShared(ctx, in.OwnerID, in.ID, "BS гид: "+name)
		if err != nil {
			c.JSON(http.StatusOK, gin.H{"ok": false, "message": ai.ElevenMessage(err)})
			return
		}
		id = got
	}
	v := premiumVoice{ID: id, Name: name, PreviewURL: prev, Model: ai.ElevenModel(), Settings: ai.DefaultElevenSettings}
	if in.OwnerID != "" {
		v.LibraryID, v.OwnerID = in.ID, in.OwnerID
	}
	cfg := p.config()
	cfg.Voice, cfg.By, cfg.At = v, platformUser(c), time.Now().UTC().Format(time.RFC3339)
	if err := p.saveCfg(c.Request.Context(), cfg); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "store_failed"})
		return
	}
	log.Printf("tts premium: voice %s (%s) picked by %s", name, id, platformUser(c))
	p.maybeRun(c.Request.Context())
	out := p.view(c.Request.Context())
	out["ok"] = true
	c.JSON(http.StatusOK, out)
}

// Off: DELETE /tts/premium/voice: the built-in recordings again.
func (p *PremiumVoice) Off(c *gin.Context) {
	if !adminOnly(c) {
		return
	}
	p.mu.Lock()
	if p.cancel != nil {
		p.cancel()
	}
	p.job = premiumJob{}
	p.mu.Unlock()
	cfg := premiumCfg{By: platformUser(c), At: time.Now().UTC().Format(time.RFC3339), EnvVoice: p.config().EnvVoice}
	if err := p.saveCfg(c.Request.Context(), cfg); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "store_failed"})
		return
	}
	p.refreshOverlay(c.Request.Context())
	p.refreshLoginOverlay(c.Request.Context())
	log.Printf("tts premium: off, the built-in recordings play (%s)", platformUser(c))
	c.JSON(http.StatusOK, p.view(c.Request.Context()))
}

// Run: POST /tts/premium/run: go on after a stop.
func (p *PremiumVoice) Run(c *gin.Context) {
	if !adminOnly(c) {
		return
	}
	p.maybeRun(c.Request.Context())
	c.JSON(http.StatusOK, p.view(c.Request.Context()))
}

// Kick: look for missing phrases now (a key was saved).
func (p *PremiumVoice) Kick() {
	select {
	case p.kick <- struct{}{}:
	default:
	}
}

// File: GET /tts/p/<key>.mp3, public and immutable.
func (p *PremiumVoice) File(c *gin.Context) {
	m := premiumFileRe.FindStringSubmatch(c.Param("file"))
	if m == nil || p.repo == nil {
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
	c.Header("Access-Control-Allow-Origin", "*")
	c.Data(http.StatusOK, mime, data)
}

// Register mounts the routes: pub is public, g is the team's group.
func (p *PremiumVoice) Register(pub, g *gin.RouterGroup) {
	pub.GET("/tts/p/:file", p.File)
	pub.HEAD("/tts/p/:file", p.File)
	pub.GET("/tts/login/:file", p.LoginFile) // R40d: the login demo's lines only
	pub.HEAD("/tts/login/:file", p.LoginFile)
	g.GET("/tts/premium", p.Get)
	g.PUT("/tts/premium/key", p.PutKey)
	g.DELETE("/tts/premium/key", p.DeleteKey)
	g.POST("/tts/premium/key/test", p.TestKey)
	g.GET("/tts/premium/voices", p.Voices)
	g.POST("/tts/premium/voice", p.Pick)
	g.DELETE("/tts/premium/voice", p.Off)
	g.POST("/tts/premium/run", p.Run)
}

// premiumReadyNote: the owner hears that the new voice is ready (not at night).
func (h *PlatformAI) premiumReadyNote(ctx context.Context, name string) {
	if h.Notify == nil || h.Owner == 0 {
		return
	}
	if now := time.Now(); QuietUntil(now).After(now) && os.Getenv("SYSCHECK_QUIET") != "off" {
		return
	}
	if err := h.Notify(ctx, h.Owner, "🎙 Голос гида готов: ElevenLabs «"+name+"». Все фразы обучения озвучены, платформа уже говорит новым голосом."); err != nil {
		log.Printf("tts premium: ready note: %v", err)
	}
}
