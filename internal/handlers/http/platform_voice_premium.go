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
func elevenEnvVoice() string { return strings.TrimSpace(os.Getenv("ELEVENLABS_VOICE_ID")) }

var voiceIDShape = regexp.MustCompile(`^[A-Za-z0-9]{20}$`)

// keyProblem: a voice id pasted where the key goes (keys start with sk_).
func keyProblem() string {
	if k := elevenEnvKey(); k != "" && voiceIDShape.MatchString(k) {
		return "в ELEVENLABS_API_KEY вставлен id голоса, а не ключ: ключ начинается с sk_, id голоса нужно положить в ELEVENLABS_VOICE_ID"
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
		p.setEnvErr("в Railway указан ELEVENLABS_VOICE_ID, но нет ключа: добавьте ELEVENLABS_API_KEY (или ключ в Настройках платформы)")
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
	cctx, cancel := context.WithTimeout(ctx, 40*time.Second)
	defer cancel()
	v, err := p.EL.Voice(cctx, id)
	libID, ownerID := "", ""
	if err != nil {
		var ee *ai.ElevenError
		if !errors.As(err, &ee) || (ee.Status != 404 && ee.Status != 400 && ee.Code != "voice_not_found") {
			p.setEnvErr(ai.ElevenMessage(err))
			return
		}
		sv, ok, ferr := p.EL.FindShared(cctx, id)
		if ferr != nil || !ok || sv.OwnerID == "" {
			p.setEnvErr("голос " + id + " из ELEVENLABS_VOICE_ID не найден в ElevenLabs: проверьте id (Voices → ⋯ → Copy voice ID)")
			return
		}
		got, aerr := p.EL.AddShared(cctx, sv.OwnerID, sv.ID, "BS гид: "+sv.Name)
		if aerr != nil {
			p.setEnvErr("голос из библиотеки не добавился: " + ai.ElevenMessage(aerr))
			return
		}
		v = ai.ElevenVoice{ID: got, Name: sv.Name, PreviewURL: sv.PreviewURL}
		libID, ownerID = sv.ID, sv.OwnerID
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
	m, n, err := p.have(ctx, c.Active, p.texts())
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
		return "", nil
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
	if job.VoiceID != c.Voice.ID {
		st.Running, st.Error, st.Stopped = false, "", ""
	}
	if c.Active.ID != "" {
		_, n, _ := p.have(ctx, c.Active, texts)
		st.On, st.Voice, st.Ready = n > 0, c.Active.Name, n
	}
	if c.Voice.ID != "" && c.Voice.ID != c.Active.ID {
		st.Picked = c.Voice.Name
		if !st.On {
			_, n, _ := p.have(ctx, c.Voice, texts)
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
			p.maybeRun(ctx)
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
	if c.Voice.ID == "" || p.key() == "" {
		return
	}
	p.mu.Lock()
	running := p.job.Running && p.job.VoiceID == c.Voice.ID
	p.mu.Unlock()
	if running {
		return
	}
	texts := p.texts()
	if _, n, err := p.have(ctx, c.Voice, texts); err == nil && n == len(texts) {
		if c.Active.ID != c.Voice.ID {
			p.finish(ctx, c.Voice)
		}
		return
	}
	p.run(ctx, c.Voice)
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
		if ctx.Err() != nil || p.config().Voice.ID != v.ID {
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
			switch {
			case ai.IsElevenQuota(err):
				why = "quota"
			case ai.IsElevenKey(err):
				why = "key"
			default:
				var ee *ai.ElevenError
				if errors.As(err, &ee) && ee.Status == 404 {
					why = "voice"
				}
			}
			msg := ai.ElevenMessage(err)
			log.Printf("tts premium: %s: stopped at a phrase (%s): %v", v.Name, why, err)
			p.progress(func(j *premiumJob) {
				if j.VoiceID == v.ID {
					j.Running, j.Error, j.Stopped = false, msg, why
				}
			})
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
	if c.Voice.ID != v.ID {
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
	if job.VoiceID != "" && job.VoiceID == c.Voice.ID {
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
	ctx, cancel := context.WithTimeout(c.Request.Context(), 30*time.Second)
	defer cancel()
	plan, err := p.EL.Subscription(ctx, key)
	if err != nil {
		c.JSON(http.StatusOK, gin.H{"ok": false, "message": ai.ElevenMessage(err)})
		return
	}
	msg := "Ключ работает"
	if plan.Tier != "" {
		msg += ": тариф " + plan.Tier
	}
	if plan.Limit > 0 {
		msg += ", осталось символов " + fmtThousands(float64(plan.Limit-plan.Used)) + " из " + fmtThousands(float64(plan.Limit))
	}
	c.JSON(http.StatusOK, gin.H{"ok": true, "message": msg, "tier": plan.Tier, "left": plan.Limit - plan.Used})
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
