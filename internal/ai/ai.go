// Package ai talks to the language and speech models the platform uses:
// the voice assistant (a spoken phrase becomes board actions) and the call
// summary (a recording becomes a transcript, a summary and a checklist).
//
// R34a: Claude does all the thinking: text, JSON, reasoning and web search
// (claude.go). The key is ANTHROPIC_API_KEY (or CLAUDE_API_KEY) in Railway,
// or the key the owner pastes in the platform settings (keys.go).
// Speech becomes text on the server itself (asr_local.go, Whisper through
// sherpa-onnx); OPENAI_API_KEY adds Whisper API as a fallback.
// R42: when Claude has no key or balance, the free tiers answer (free.go):
// Gemini (GEMINI_API_KEY, on by default; GEMINI_ENABLED=0 opts out), Groq
// (GROQ_API_KEY), OpenRouter (OPENROUTER_API_KEY). Gemini's speech
// (transcription fallback, tour TTS) stays behind GEMINI_ENABLED=1.
// AI_TEXT_ORDER (e.g. "claude,groq") changes which model is asked first.
package ai

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"mime/multipart"
	"net/http"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

type Client struct {
	Anthropic, Gemini, OpenAI             string // keys
	Groq, OpenRouter                      string // R42: free OpenAI-compatible providers (free.go)
	ClaudeModel, GeminiModel              string
	GeminiLightModel                      string // R42: short JSON tasks (AI_GEMINI_MODEL_LIGHT)
	GroqModel, GroqLightModel, GroqBase   string
	OpenRouterModel, OpenRouterLightModel string
	OpenRouterBase                        string
	// GeminiTextOnly: Gemini answers text and search only; its speech
	// (transcription, tour TTS) needs GEMINI_ENABLED=1 (FromEnv, R42).
	GeminiTextOnly                        bool
	HeavyModel                            string // AI_MODEL_HEAVY: Heavy(ctx) tasks
	OpenAIModel, OpenAISTTModel           string
	AnthropicBase, GeminiBase, OpenAIBase string
	HTTP                                  *http.Client
	// Keys: the Claude key saved in the settings, used when Anthropic is empty.
	Keys *KeyBox
	// Env: where Anthropic came from (FromEnv, R37): the variable's name and
	// the related names, for the system check. Never holds a printed value.
	Env *EnvKeyInfo
	// ASR: speech to text on the server (asr_local.go); nil: off.
	ASR *LocalASR

	modelMu  sync.Mutex // GeminiModel may be switched when Google retires a model
	liteOnce sync.Once  // R55: one log line when the light model stands in
	quota    quotaState // services whose quota is used up (quota.go)

	// TextOrder: the order Text, JSON and Search ask the models in
	// (AI_TEXT_ORDER; default gemini, claude, openai).
	TextOrder []string
	// OnQuota is told when a service closes for a long time (a daily or
	// billing quota); the platform tells the owner once a day (R32c).
	OnQuota func(*QuotaError)
	// OnSwitch is told when another provider durably answers text tasks
	// (R42: Claude ran out of balance, Gemini's day is over); from is "" at start.
	OnSwitch func(from, to string)

	budget budgets
	sw     switchState
}

// HTTPError is a non-2xx answer of a model API (Body is complete).
type HTTPError struct {
	Status     int
	Body       string
	RetryAfter time.Duration // the API's retry-after header, 0 when absent
}

// Error never carries the API's raw JSON (R32c: it used to reach the page):
// a quota refusal is QuotaMessage, anything else the status and the API's
// own one-line message. The full answer stays in Body for the logs.
func (e *HTTPError) Error() string {
	if q, ok := ParseQuota(e); ok {
		return quotaMessage(q.Service)
	}
	if m := apiMessage(e.Body); m != "" {
		return fmt.Sprintf("ИИ ответил ошибкой %d: %s", e.Status, m)
	}
	return fmt.Sprintf("ИИ ответил ошибкой %d", e.Status)
}

func (c *Client) geminiModel() string {
	c.modelMu.Lock()
	defer c.modelMu.Unlock()
	return c.GeminiModel
}

var suggestedModelRe = regexp.MustCompile(`models/(gemini-[\w.\-]+)`)

// retiredModel: Google answers 404 (or 400) when a model is switched off.
func retiredModel(err error) (*HTTPError, bool) {
	var he *HTTPError
	if !errors.As(err, &he) {
		return nil, false
	}
	low := strings.ToLower(he.Body)
	return he, (he.Status == 404 || he.Status == 400) && (strings.Contains(low, "no longer available") ||
		strings.Contains(low, "not found") || strings.Contains(low, "is not supported") || strings.Contains(low, "deprecated"))
}

// switchGeminiModel picks a working model: the one Google suggests in the
// error, otherwise the newest "flash" model the key can use.
func (c *Client) switchGeminiModel(ctx context.Context, he *HTTPError) bool {
	cur := c.geminiModel()
	pick := ""
	for _, m := range suggestedModelRe.FindAllStringSubmatch(he.Body, -1) {
		if name := strings.TrimRight(m[1], ".,"); name != cur {
			pick = name
			break
		}
	}
	if pick == "" {
		pick = c.newestFlash(ctx, cur)
	}
	if pick == "" {
		return false
	}
	c.modelMu.Lock()
	c.GeminiModel = pick
	c.modelMu.Unlock()
	return true
}

var verRe = regexp.MustCompile(`gemini-(\d+(?:\.\d+)?)`)

func (c *Client) newestFlash(ctx context.Context, cur string) string {
	r, _ := http.NewRequest("GET", c.GeminiBase+"/v1beta/models?pageSize=1000", nil)
	b, err := c.do(ctx, c.gemAuth(r))
	if err != nil {
		return ""
	}
	var out struct {
		Models []struct {
			Name    string   `json:"name"`
			Methods []string `json:"supportedGenerationMethods"`
		} `json:"models"`
	}
	_ = json.Unmarshal(b, &out)
	type cand struct {
		name string
		ver  float64
	}
	var cs []cand
	for _, m := range out.Models {
		n := strings.TrimPrefix(m.Name, "models/")
		ok := false
		for _, g := range m.Methods {
			if g == "generateContent" {
				ok = true
			}
		}
		low := strings.ToLower(n)
		if !ok || n == cur || !strings.Contains(low, "flash") {
			continue
		}
		bad := false
		for _, w := range []string{"lite", "image", "tts", "live", "audio", "embedding", "exp", "thinking"} {
			if strings.Contains(low, w) {
				bad = true
			}
		}
		if bad {
			continue
		}
		v := 0.0
		if mm := verRe.FindStringSubmatch(low); mm != nil {
			v, _ = strconv.ParseFloat(mm[1], 64)
		}
		if strings.Contains(low, "preview") {
			v -= 0.01 // a stable model of the same version wins
		}
		cs = append(cs, cand{n, v})
	}
	sort.Slice(cs, func(i, j int) bool { return cs[i].ver > cs[j].ver })
	if len(cs) == 0 {
		return ""
	}
	return cs[0].name
}

// geminiCall posts to models/<model>:<method>, switching to a live model
// once if the configured one was retired.
// While Gemini's quota is used up it is not called at all (quota.go).
func (c *Client) geminiCall(ctx context.Context, method string, body any) ([]byte, error) {
	return c.geminiCallModel(ctx, "", method, body)
}

// geminiCallModel: model "" is GeminiModel; another (the light model) has
// its own quota ("gemini-lite": Google counts per model) and on any refusal
// the main model answers instead.
func (c *Client) geminiCallModel(ctx context.Context, model, method string, body any) ([]byte, error) {
	if ke := c.keyClosed("gemini"); ke != nil {
		return nil, ke
	}
	if model != "" && model != c.geminiModel() {
		if c.quotaClosed("gemini-lite") == nil {
			b, err := c.gemPost(ctx, model, method, body)
			if err == nil {
				return b, nil
			}
			if ke := c.noteBadKey("gemini", err); ke != nil {
				return nil, ke
			}
			c.noteQuota("gemini-lite", err)
			if ctx.Err() != nil {
				return nil, err
			}
			if _, retired := retiredModel(err); !retired && !IsQuota(err) && !transient(err) {
				return nil, err
			}
		}
	}
	if q := c.quotaClosed("gemini"); q != nil {
		if b, ok := c.geminiLiteInstead(ctx, model, method, body); ok {
			return b, nil
		}
		return nil, q
	}
	waited := false
	for try := 0; ; try++ {
		b, err := c.gemPost(ctx, c.geminiModel(), method, body)
		if q := c.noteQuota("gemini", err); q != nil {
			if b, ok := c.geminiLiteInstead(ctx, model, method, body); ok {
				return b, nil
			}
			// R56: a per-minute limit: wait it out once, then ask again
			if !waited && waitMinute(ctx, q) {
				waited = true
				c.SetQuotaUntil("gemini", time.Time{})
				continue
			}
			return nil, q
		}
		if ke := c.noteBadKey("gemini", err); ke != nil {
			return nil, ke
		}
		// R56: still «high demand» after the retries: the light model, no pause
		if err != nil && overloaded(err) && ctx.Err() == nil {
			if b, ok := c.geminiLiteInstead(ctx, model, method, body); ok {
				return b, nil
			}
			return nil, err
		}
		if err == nil || try > 0 {
			return b, err
		}
		he, retired := retiredModel(err)
		if !retired || !c.switchGeminiModel(ctx, he) {
			return b, err
		}
	}
}

// provQuota: quotaClosed, except that Gemini stays open while its main
// model rests and the light model can stand in (R55, geminiLiteInstead).
func (c *Client) provQuota(name string) *QuotaError {
	q := c.quotaClosed(name)
	if q == nil || name != "gemini" {
		return q
	}
	if lite := strings.TrimSpace(c.GeminiLightModel); lite != "" && lite != c.geminiModel() && c.quotaClosed("gemini-lite") == nil {
		return nil
	}
	return q
}

// geminiLiteInstead (R55): the main model's quota is closed (a new key
// whose free tier gives that model «limit: 0», or its daily limit is used
// up), but Google counts quota per model: the light model answers instead
// of the whole of Gemini resting for hours. ok false: it did not answer
// (no light model, it was the one that failed, or its quota is closed too).
func (c *Client) geminiLiteInstead(ctx context.Context, tried, method string, body any) ([]byte, bool) {
	lite := strings.TrimSpace(c.GeminiLightModel)
	if lite == "" || lite == c.geminiModel() || tried == lite || c.quotaClosed("gemini-lite") != nil || ctx.Err() != nil {
		return nil, false
	}
	b, err := c.gemPost(ctx, lite, method, body)
	if err == nil {
		c.liteOnce.Do(func() {
			log.Printf("ai: gemini %s is closed (quota) or overloaded; %s answers instead", c.geminiModel(), lite)
		})
		return b, true
	}
	if c.noteBadKey("gemini", err) == nil {
		c.noteQuota("gemini-lite", err)
	}
	return nil, false
}

func FromEnv() *Client {
	env := func(k, def string) string {
		if v := strings.TrimSpace(os.Getenv(k)); v != "" {
			return v
		}
		return def
	}
	gem := ""
	if !GeminiDisabled() { // R42: the free fallback, on unless GEMINI_ENABLED=0
		gem = envClean("GEMINI_API_KEY", "GOOGLE_API_KEY", "GOOGLE_AI_API_KEY")
	}
	ek := EnvKey() // R37: any sensible name, a cleaned value (envkey.go)
	return &Client{
		Anthropic:            ek.Key,
		Env:                  &ek,
		Gemini:               gem,
		GeminiTextOnly:       !GeminiEnabled(),
		GeminiLightModel:     env("AI_GEMINI_MODEL_LIGHT", "gemini-3.5-flash-lite"),
		Groq:                 envClean("GROQ_API_KEY"),
		GroqModel:            env("AI_GROQ_MODEL", "openai/gpt-oss-120b"),
		GroqLightModel:       env("AI_GROQ_MODEL_LIGHT", "openai/gpt-oss-20b"),
		GroqBase:             env("GROQ_API_BASE", "https://api.groq.com/openai/v1"),
		OpenRouter:           envClean("OPENROUTER_API_KEY"),
		OpenRouterModel:      env("AI_OPENROUTER_MODEL", "openrouter/free"),
		OpenRouterLightModel: env("AI_OPENROUTER_MODEL_LIGHT", ""),
		OpenRouterBase:       env("OPENROUTER_API_BASE", "https://openrouter.ai/api/v1"),
		OpenAI:               env("OPENAI_API_KEY", ""),
		ClaudeModel:          env("AI_MODEL", env("AI_CLAUDE_MODEL", DefaultModel)),
		HeavyModel:           env("AI_MODEL_HEAVY", DefaultHeavyModel),
		Keys:                 SharedKeys,
		ASR:                  LocalASRFromEnv(),
		GeminiModel:          env("AI_GEMINI_MODEL", "gemini-3.8-flash"),
		OpenAIModel:          env("AI_OPENAI_MODEL", "gpt-4o-mini"),
		OpenAISTTModel:       env("AI_OPENAI_STT_MODEL", "whisper-1"),
		AnthropicBase:        env("ANTHROPIC_API_BASE", "https://api.anthropic.com"),
		GeminiBase:           env("GEMINI_API_BASE", "https://generativelanguage.googleapis.com"),
		OpenAIBase:           env("OPENAI_API_BASE", "https://api.openai.com"),
		HTTP:                 &http.Client{Timeout: 10 * time.Minute},
		TextOrder:            strings.FieldsFunc(strings.ToLower(env("AI_TEXT_ORDER", "")), func(r rune) bool { return r == ',' || r == ' ' }),
	}
}

// GeminiEnabled: GEMINI_ENABLED=1 also turns on Gemini's speech
// (transcription fallback, tour TTS). Text and search need no switch (R42).
func GeminiEnabled() bool {
	v := strings.ToLower(strings.TrimSpace(os.Getenv("GEMINI_ENABLED")))
	return v == "1" || v == "true" || v == "yes" || v == "on"
}

// SpeechModels: who turns a recording into text, in order (Transcribe).
func (c *Client) SpeechModels() []string {
	var out []string
	if c.ASR != nil {
		out = append(out, "local")
	}
	if c.OpenAI != "" {
		out = append(out, "openai")
	}
	if c.Gemini != "" && !c.GeminiTextOnly {
		out = append(out, "gemini")
	}
	return out
}

// HasTTS: Gemini may speak the tour's phrases (GEMINI_ENABLED=1, R42).
func (c *Client) HasTTS() bool { return c != nil && c.Gemini != "" && !c.GeminiTextOnly }

// Status says what is available, for the page to explain what is missing.
func (c *Client) Status() map[string]any {
	text, speech := "", ""
	if ms := c.TextModels(); len(ms) > 0 {
		text = ms[0]
	}
	if ms := c.SpeechModels(); len(ms) > 0 {
		speech = ms[0]
	}
	st := map[string]any{"text": text, "speech": speech, "textModels": c.TextModels(), "speechModels": c.SpeechModels()}
	if c.HasClaude() {
		ms := c.models(context.Background())
		st["model"] = ms[len(ms)-1]
		st["heavyModel"] = c.models(Heavy(context.Background()))[0]
		st["keySource"] = c.KeySource()
	}
	if speech == "local" {
		st["speech_model"] = c.ASR.Describe()
	}
	q := map[string]string{}
	for _, svc := range []string{"claude", "gemini", "tts"} {
		if u := c.QuotaUntil(svc); !u.IsZero() {
			q[svc] = u.UTC().Format(time.RFC3339)
		}
	}
	if len(q) > 0 {
		st["quota"] = q
	}
	st["providers"] = c.Providers()
	// R42: every provider of the chain, the one answering now, today's use
	st["chain"] = c.ProviderStates()
	st["answering"] = c.Answering()
	if si := c.LastSearch(); !si.At.IsZero() {
		st["search"] = si // R39: the last web search for «Состояние ИИ»
	}
	st["searchModels"] = c.SearchModels()
	if c.Paused() {
		msg := QuotaMessage
		if ms := c.TextModels(); len(ms) > 1 || len(ms) == 1 && ms[0] != "claude" {
			msg = AllPausedMessage
		}
		st["paused"], st["message"] = true, msg
	}
	return st
}

// Providers: one line per model for the admin's «Состояние ИИ» (R32c).
//
//	state: ok | quota (until) | none (no key)
func (c *Client) Providers() []map[string]any {
	var out []map[string]any
	add := func(name, key string, svc string) {
		p := map[string]any{"name": name, "state": "ok"}
		if key == "" {
			p["state"] = "none"
		} else if svc != "" {
			if q := c.quotaClosed(svc); q != nil {
				p["state"], p["until"] = "quota", q.Until.UTC().Format(time.RFC3339)
				p["billing"], p["daily"] = q.Billing, q.Daily
			}
		}
		out = append(out, p)
	}
	add("claude", c.claudeKey(), "claude")
	if c.Gemini != "" {
		add("gemini", c.Gemini, "gemini")
	}
	if c.Groq != "" {
		add("groq", c.Groq, "groq")
	}
	if c.OpenRouter != "" {
		add("openrouter", c.OpenRouter, "openrouter")
	}
	if c.OpenAI != "" {
		add("openai", c.OpenAI, "")
	}
	return out
}

// Paused: no model can answer a text task now (every model with a key has
// its quota or balance used up). The page shows QuotaMessage.
func (c *Client) Paused() bool {
	ms := c.TextModels()
	for _, m := range ms {
		if c.provQuota(m) == nil && c.budgetLeft(m) { // R66: Gemini stays open while its light model can answer
			return false
		}
	}
	return len(ms) > 0
}

// UserMessage: an AI error in words for the page or the bot, never raw JSON.
func UserMessage(err error) string {
	if err == nil {
		return ""
	}
	if IsQuota(err) {
		return quotaText(err)
	}
	var ke *KeyError
	if errors.As(err, &ke) {
		return ke.Error()
	}
	if errors.Is(err, ErrNoKey) {
		return ErrNoKey.Error()
	}
	var se *SearchError
	if errors.As(err, &se) {
		return se.Msg
	}
	if errors.Is(err, ErrNoSearch) {
		return ErrNoSearch.Error()
	}
	var pe *ProviderError
	var he *HTTPError
	if errors.As(err, &pe) && errors.As(err, &he) && (he.Status == 401 || he.Status == 403) {
		return "Ключ " + ProviderLabel(pe.Name) + " не принят: проверьте " + providerEnv[pe.Name] + " в переменных Railway"
	}
	if errors.As(err, &he) {
		if he.Status == 401 || he.Status == 403 {
			return keyRejected(he)
		}
		return he.Error()
	}
	msg := err.Error()
	if i := strings.Index(msg, "{\""); i >= 0 {
		msg = strings.TrimRight(strings.TrimSpace(msg[:i]), ":")
		if msg == "" {
			msg = "ИИ не ответил"
		}
	}
	return msg
}

var ErrNoKey = errors.New("Нет ключа ИИ: добавьте бесплатный ключ GEMINI_API_KEY (aistudio.google.com) или ANTHROPIC_API_KEY в переменные Railway, либо вставьте ключ Claude в Настройках платформы («Ключ Claude»)")

// KeyRejected: the API refused the key (401/403).
const KeyRejected = "Ключ Claude не принят: проверьте его в Настройках платформы («Ключ Claude») или ANTHROPIC_API_KEY в Railway"

// keyRejected: whose key it was. Google's errors carry "code" and no
// Anthropic error type (Gemini is there only with GEMINI_ENABLED=1).
func keyRejected(he *HTTPError) string {
	low := strings.ToLower(he.Body)
	if !strings.Contains(low, "authentication_error") && !strings.Contains(low, "permission_error") &&
		(strings.Contains(low, "api key not valid") || strings.Contains(low, `"code"`) || strings.Contains(low, "googleapis")) {
		return "Ключ Gemini не принят: проверьте GEMINI_API_KEY в переменных Railway"
	}
	return KeyRejected
}

// Text answers a prompt. system sets the role; the answer is plain text.
//
// The models with a key are asked in turn, Claude first (R34a), then Gemini
// (only with GEMINI_ENABLED=1), then OpenAI: when one fails the next answers.
// A model with a used-up quota is skipped without a call.
func (c *Client) Text(ctx context.Context, system, prompt string) (string, error) {
	return c.chain(ctx, system, prompt, false)
}

// JSON answers a prompt expecting a JSON object (Claude is told to answer
// with JSON only; Gemini is put into JSON mode).
func (c *Client) JSON(ctx context.Context, system, prompt string) (string, error) {
	return c.chain(ctx, system, prompt, true)
}

// TextModels: the models Text and JSON try, in order: TextOrder, by default
// Claude, then Gemini (emergency only), then OpenAI. Only models with a key;
// one missing from TextOrder goes last.
func (c *Client) TextModels() []string {
	has := map[string]bool{"gemini": c.Gemini != "", "claude": c.claudeKey() != "", "openai": c.OpenAI != "",
		"groq": c.Groq != "", "openrouter": c.OpenRouter != ""}
	var out []string
	seen := map[string]bool{}
	for _, m := range append(append([]string{}, c.TextOrder...), providerOrder...) {
		if has[m] && !seen[m] {
			seen[m] = true
			out = append(out, m)
		}
	}
	return out
}

func (c *Client) chain(ctx context.Context, system, prompt string, asJSON bool) (string, error) {
	models := c.TextModels()
	if len(models) == 0 {
		return "", ErrNoKey
	}
	light := isLight(ctx, asJSON, system, prompt)
	cps := c.compats()
	var first error
	quotaAll, tried := true, 0
	var soonest time.Time
	for _, m := range models {
		var ans string
		var err error
		if q := c.provQuota(m); q != nil {
			err = q
		} else if ke := c.keyClosed(m); ke != nil && m != "claude" { // R51: a refused key is not asked again at once
			err = ke
		} else if berr := c.spend(m); berr != nil {
			err = berr
		} else {
			tried++
			switch m {
			case "claude":
				ans, err = c.claude(ctx, system, prompt, asJSON)
			case "gemini":
				ans, err = c.geminiText(ctx, system, prompt, asJSON, light)
			default:
				if p := cps[m]; p != nil {
					ans, _, err = c.compatChat(ctx, p, system, prompt, asJSON, light)
				}
			}
		}
		if err == nil {
			c.noteAnswered(m, models)
			return ans, nil
		}
		if m != "claude" && !IsKeyRejected(err) {
			if ke := c.noteBadKey(m, err); ke != nil {
				err = ke
			}
		}
		var qe *QuotaError
		if errors.As(err, &qe) {
			if soonest.IsZero() || qe.Until.Before(soonest) {
				soonest = qe.Until
			}
		} else if !IsQuota(err) {
			quotaAll = false
		}
		if tried > 0 || !IsQuota(err) {
			log.Printf("ai: %s did not answer: %s", m, UserMessage(err))
		}
		// a paused provider says less than the fallback's own failure
		if first == nil || (IsQuota(first) && !IsQuota(err)) {
			first = err
		}
		if !fallbackWorthy(ctx, err) {
			break
		}
	}
	if quotaAll && len(models) > 1 && ctx.Err() == nil {
		return "", &QuotaError{Service: "all", Until: soonest, Daily: true, Err: first}
	}
	return "", first
}

// geminiText: a text or JSON task on Gemini, the light model for short tasks.
func (c *Client) geminiText(ctx context.Context, system, prompt string, asJSON, light bool) (string, error) {
	var cfg map[string]any
	if asJSON {
		cfg = map[string]any{"responseMimeType": "application/json", "temperature": 0.2}
	}
	model := ""
	if light {
		model = c.GeminiLightModel
	}
	return c.geminiCfgModel(ctx, model, system, []map[string]any{{"text": prompt}}, cfg)
}

// ErrNoSpeech: no way to turn a recording into text.
var ErrNoSpeech = errors.New("Расшифровка недоступна: включите распознавание на сервере (ASR_LOCAL, по умолчанию включено) или добавьте ключ OPENAI_API_KEY в Railway. Запись сохранена")

// Transcribe turns a recording into text (R34a): Whisper on the server
// (asr_local.go), then Whisper API (OPENAI_API_KEY), then Gemini (only with
// GEMINI_ENABLED=1). The first that works answers.
func (c *Client) Transcribe(ctx context.Context, audio []byte, mime string) (string, error) {
	if mime == "" {
		mime = "audio/webm"
	}
	ms := c.SpeechModels()
	if len(ms) == 0 {
		return "", ErrNoSpeech
	}
	var first error
	for _, m := range ms {
		var t string
		var err error
		switch m {
		case "local":
			t, err = c.ASR.Transcribe(ctx, audio, mime)
		case "openai":
			t, err = c.whisper(ctx, audio, mime)
		case "gemini":
			t, err = c.geminiTranscribe(ctx, audio, mime)
		}
		if err == nil {
			return t, nil
		}
		log.Printf("ai: transcription via %s failed: %s", m, UserMessage(err))
		if first == nil {
			first = err
		}
		if !fallbackWorthy(ctx, err) {
			break
		}
	}
	return "", first
}

func (c *Client) geminiTranscribe(ctx context.Context, audio []byte, mime string) (string, error) {
	if q := c.provQuota("gemini"); q != nil {
		return "", q
	}
	part, err := c.geminiMedia(ctx, audio, mime)
	if err != nil {
		return "", err
	}
	return c.gemini(ctx, "Ты точно расшифровываешь деловые созвоны на русском языке.",
		[]map[string]any{part, {"text": "Дословно расшифруй запись созвона. Это разбор бизнеса: трекер (Рустам или Береке) и резидент. " +
			"Раздели реплики по говорящим: «Трекер:» и «Резидент:», каждая с новой строки. Без комментариев от себя."}})
}

func (c *Client) do(ctx context.Context, req *http.Request) ([]byte, error) {
	res, err := c.HTTP.Do(req.WithContext(ctx))
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(res.Body, 32<<20))
	if res.StatusCode >= 300 {
		he := &HTTPError{Status: res.StatusCode, Body: string(b)}
		if v, err := strconv.Atoi(strings.TrimSpace(res.Header.Get("retry-after"))); err == nil && v > 0 {
			he.RetryAfter = time.Duration(v) * time.Second
		}
		return nil, he
	}
	return b, nil
}

// gemAuth: the Gemini key goes in the x-goog-api-key header, as Google's
// docs show for every key type (the AI Studio auth keys «AQ.…» included),
// never in the URL: no escaping trouble and no key in a logged URL.
func (c *Client) gemAuth(r *http.Request) *http.Request {
	r.Header.Set("x-goog-api-key", c.Gemini)
	return r
}

func jsonReq(method, url string, body any) *http.Request {
	b, _ := json.Marshal(body)
	r, _ := http.NewRequest(method, url, bytes.NewReader(b))
	r.Header.Set("Content-Type", "application/json")
	return r
}

func (c *Client) gemini(ctx context.Context, system string, parts []map[string]any) (string, error) {
	return c.geminiCfg(ctx, system, parts, nil)
}

func (c *Client) geminiCfg(ctx context.Context, system string, parts []map[string]any, cfg map[string]any) (string, error) {
	return c.geminiCfgModel(ctx, "", system, parts, cfg)
}

func (c *Client) geminiCfgModel(ctx context.Context, model, system string, parts []map[string]any, cfg map[string]any) (string, error) {
	body := map[string]any{
		"system_instruction": map[string]any{"parts": []map[string]any{{"text": system}}},
		"contents":           []map[string]any{{"role": "user", "parts": parts}},
	}
	if cfg != nil {
		body["generationConfig"] = cfg
	}
	b, err := c.geminiCallModel(ctx, model, "generateContent", body)
	if err != nil {
		return "", err
	}
	var out struct {
		Candidates []struct {
			Content struct {
				Parts []struct {
					Text    string `json:"text"`
					Thought bool   `json:"thought"`
				} `json:"parts"`
			} `json:"content"`
		} `json:"candidates"`
	}
	if err := json.Unmarshal(b, &out); err != nil {
		return "", err
	}
	var sb strings.Builder
	for _, cnd := range out.Candidates {
		for _, p := range cnd.Content.Parts {
			if !p.Thought {
				sb.WriteString(p.Text)
			}
		}
		break
	}
	if sb.Len() == 0 {
		return "", errors.New("ИИ вернул пустой ответ")
	}
	return sb.String(), nil
}

// geminiMedia: small files go inline, large ones through the Files API.
func (c *Client) geminiMedia(ctx context.Context, data []byte, mime string) (map[string]any, error) {
	if len(data) < 14<<20 {
		return map[string]any{"inline_data": map[string]any{"mime_type": mime, "data": base64.StdEncoding.EncodeToString(data)}}, nil
	}
	start := c.gemAuth(jsonReq("POST", c.GeminiBase+"/upload/v1beta/files", map[string]any{"file": map[string]any{"display_name": "call"}}))
	start.Header.Set("X-Goog-Upload-Protocol", "resumable")
	start.Header.Set("X-Goog-Upload-Command", "start")
	start.Header.Set("X-Goog-Upload-Header-Content-Length", fmt.Sprint(len(data)))
	start.Header.Set("X-Goog-Upload-Header-Content-Type", mime)
	res, err := c.HTTP.Do(start.WithContext(ctx))
	if err != nil {
		return nil, err
	}
	res.Body.Close()
	up := res.Header.Get("X-Goog-Upload-URL")
	if up == "" {
		return nil, fmt.Errorf("загрузка записи в ИИ: %d", res.StatusCode)
	}
	r, _ := http.NewRequest("POST", up, bytes.NewReader(data))
	r.Header.Set("X-Goog-Upload-Command", "upload, finalize")
	r.Header.Set("X-Goog-Upload-Offset", "0")
	b, err := c.do(ctx, r)
	if err != nil {
		return nil, err
	}
	var f struct {
		File struct {
			Name, URI, State string
		} `json:"file"`
	}
	_ = json.Unmarshal(b, &f)
	for i := 0; i < 120 && f.File.State == "PROCESSING"; i++ {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(3 * time.Second):
		}
		g, _ := http.NewRequest("GET", c.GeminiBase+"/v1beta/"+f.File.Name, nil)
		if b2, err := c.do(ctx, c.gemAuth(g)); err == nil {
			_ = json.Unmarshal(b2, &f.File)
		}
	}
	if f.File.URI == "" {
		return nil, errors.New("ИИ не принял запись")
	}
	return map[string]any{"file_data": map[string]any{"mime_type": mime, "file_uri": f.File.URI}}, nil
}

func (c *Client) whisper(ctx context.Context, audio []byte, mime string) (string, error) {
	var body bytes.Buffer
	w := multipart.NewWriter(&body)
	_ = w.WriteField("model", c.OpenAISTTModel)
	_ = w.WriteField("language", "ru")
	_ = w.WriteField("response_format", "text")
	ext := "webm"
	if strings.Contains(mime, "mp4") || strings.Contains(mime, "m4a") {
		ext = "m4a"
	} else if strings.Contains(mime, "mpeg") || strings.Contains(mime, "mp3") {
		ext = "mp3"
	} else if strings.Contains(mime, "wav") {
		ext = "wav"
	}
	fw, _ := w.CreateFormFile("file", "call."+ext)
	_, _ = fw.Write(audio)
	_ = w.Close()
	r, _ := http.NewRequest("POST", c.OpenAIBase+"/v1/audio/transcriptions", &body)
	r.Header.Set("Content-Type", w.FormDataContentType())
	r.Header.Set("Authorization", "Bearer "+c.OpenAI)
	b, err := c.do(ctx, r)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(b)), nil
}

// JSONFrom pulls the first JSON object out of a model answer (models like ```json fences).
func JSONFrom(s string) string {
	s = strings.TrimSpace(s)
	i, j := strings.Index(s, "{"), strings.LastIndex(s, "}")
	if i < 0 || j <= i {
		return ""
	}
	return s[i : j+1]
}

// Event is one business event found on the web.
type Event struct {
	Title  string   `json:"title"`
	Date   string   `json:"date"` // YYYY-MM-DD
	Time   string   `json:"time,omitempty"`
	Place  string   `json:"place,omitempty"`
	URL    string   `json:"url,omitempty"`
	Price  string   `json:"price,omitempty"`
	Source string   `json:"source,omitempty"`
	Tags   []string `json:"tags,omitempty"`
	// Telegram channels (tgevents): an opportunity (grant, programme, contest)
	// has Kind "возможность" and its Date is the application deadline.
	Kind     string `json:"kind,omitempty"`
	Deadline string `json:"deadline,omitempty"` // YYYY-MM-DD, applications close
	Org      string `json:"org,omitempty"`
	Desc     string `json:"desc,omitempty"`
	Online   bool   `json:"online,omitempty"`
	Post     string `json:"post,omitempty"`   // the channel post it came from
	Origin   string `json:"origin,omitempty"` // "tg" for a Telegram channel
}

const eventsPrompt = `Найди в интернете бизнес-мероприятия в Алматы на ближайшие %d дней, начиная с %s: конференции, форумы, нетворкинги, бизнес-завтраки, мастер-классы и лекции для предпринимателей, выставки.
Источники: ticketon.kz, sxodim.com, afisha, сайты организаторов, Telegram-каналы с анонсами, Astana Hub, Atameken, Forbes Kazakhstan, бизнес-клубы.
Верни ТОЛЬКО JSON: {"items":[{"title":"...","date":"YYYY-MM-DD","time":"HH:MM","place":"...","url":"ссылка на страницу события","price":"бесплатно или цена","source":"домен","tags":["нетворкинг|конференция|обучение|выставка|завтрак|IT|маркетинг|финансы|продажи"]}]}
Только реальные события с датой и ссылкой, которые ты нашёл в поиске. Не выдумывай. До 30 событий.`

// Search asks a model that can search the web: Claude with the web search
// server tool, then Gemini with Google Search grounding (R42: a free key, its
// own daily budget AI_BUDGET_GEMINI_SEARCH) and returns its text answer. An
// overloaded or rate-limited model is asked again (SearchBackoff). With no
// model able to search, the error is ErrNoSearch (SearchUnavailable): the
// callers degrade (events keep the feed, AI recs go without search).
func (c *Client) Search(ctx context.Context, prompt string) (string, error) {
	ms := c.SearchModels()
	if len(ms) == 0 {
		if c.HasText() {
			return "", ErrNoSearch
		}
		return "", ErrNoKey
	}
	try := func(f func(context.Context, string) (string, error)) (string, error) {
		var ans string
		var err error
		for i := 0; ; i++ {
			ans, err = f(ctx, prompt)
			if err == nil || !transient(err) || i >= len(SearchBackoff) || ctx.Err() != nil {
				return ans, err
			}
			if serr := sleepCtx(ctx, SearchBackoff[i]); serr != nil {
				return ans, err
			}
		}
	}
	// The same order as Text (Claude first). A model with a used-up quota
	// answers at once without a call.
	var first error
	for _, m := range ms {
		var ans string
		var err error
		if q := c.quotaClosed("gemini-search"); q != nil && m == "gemini" {
			err = q
		} else if q := c.quotaClosed(m); q != nil {
			err = q
		} else if ke := c.keyClosed(m); ke != nil && m != "claude" {
			err = ke
		} else {
			switch m {
			case "gemini":
				if err = c.spend("gemini_search"); err == nil {
					if err = c.spend("gemini"); err == nil {
						ans, err = try(c.geminiSearch)
						c.noteGeminiSearch(err)
					}
				}
			case "claude":
				if err = c.spend("claude"); err == nil {
					ans, err = try(c.claudeSearch)
				}
			}
		}
		if err == nil {
			return ans, nil
		}
		if first == nil || (IsQuota(first) && !IsQuota(err)) {
			first = err
		}
		if ctx.Err() != nil {
			break
		}
	}
	return "", first
}

// SearchModels: the models that can search the web now or later (a key).
func (c *Client) SearchModels() []string {
	var out []string
	for _, m := range c.TextModels() {
		if m == "claude" || m == "gemini" {
			out = append(out, m)
		}
	}
	return out
}

func (c *Client) noteGeminiSearch(err error) {
	si := SearchInfo{At: time.Now(), OK: err == nil, Model: c.geminiModel(), Tool: "google_search"}
	if err != nil {
		si.Error = UserMessage(err)
	}
	c.noteSearch(si)
}

// geminiSearchCall (R56, prod 07.10.2026): a request with the google_search
// tool has its own quota at Google (grounding): the morning events search got
// a bare 429 «check your plan and billing» on both models while a plain
// request a minute earlier was answered. That refusal used to pause all of
// Gemini for 6 hours; now it closes only "gemini-search" (the search goes on
// with Claude or says so), and text tasks keep Gemini.
func (c *Client) geminiSearchCall(ctx context.Context, body any) ([]byte, error) {
	if ke := c.keyClosed("gemini"); ke != nil {
		return nil, ke
	}
	if q := c.quotaClosed("gemini-search"); q != nil {
		return nil, q
	}
	for try := 0; ; try++ {
		b, err := c.gemPost(ctx, c.geminiModel(), "generateContent", body)
		if q := c.noteQuota("gemini-search", err); q != nil {
			return nil, q
		}
		if ke := c.noteBadKey("gemini", err); ke != nil {
			return nil, ke
		}
		if err == nil || try > 0 {
			return b, err
		}
		he, retired := retiredModel(err)
		if !retired || !c.switchGeminiModel(ctx, he) {
			return b, err
		}
	}
}

func (c *Client) geminiSearch(ctx context.Context, prompt string) (string, error) {
	b, err := c.geminiSearchCall(ctx, map[string]any{
		"contents": []map[string]any{{"role": "user", "parts": []map[string]any{{"text": prompt}}}},
		"tools":    []map[string]any{{"google_search": map[string]any{}}},
	})
	if err != nil {
		return "", err
	}
	var out struct {
		Candidates []struct {
			FinishReason string `json:"finishReason"`
			Content      struct {
				Parts []struct {
					Text    string `json:"text"`
					Thought bool   `json:"thought"`
				} `json:"parts"`
			} `json:"content"`
		} `json:"candidates"`
		PromptFeedback struct {
			BlockReason string `json:"blockReason"`
		} `json:"promptFeedback"`
	}
	if err := json.Unmarshal(b, &out); err != nil {
		return "", fmt.Errorf("ответ ИИ не читается: %v", err)
	}
	ans, reason := "", out.PromptFeedback.BlockReason
	for _, cnd := range out.Candidates {
		for _, p := range cnd.Content.Parts {
			if !p.Thought {
				ans += p.Text
			}
		}
		if reason == "" {
			reason = cnd.FinishReason
		}
		break
	}
	if strings.TrimSpace(ans) == "" {
		return "", &ErrEmptyAnswer{Reason: reason}
	}
	return ans, nil
}

// FindEvents searches the web for Almaty business events (needs a model with web search).
func (c *Client) FindEvents(ctx context.Context, days int, from time.Time) ([]Event, error) {
	ans, err := c.Search(ctx, fmt.Sprintf(eventsPrompt, days, from.Format("2006-01-02")))
	if err != nil {
		return nil, err
	}
	return ParseEvents(ans, from)
}
