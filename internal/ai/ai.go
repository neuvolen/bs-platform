// Package ai talks to the language and speech models the platform uses:
// the voice assistant (a spoken phrase becomes board actions) and the call
// summary (a recording becomes a transcript, a summary and a checklist).
//
// R34a: Claude does all the thinking: text, JSON, reasoning and web search
// (claude.go). The key is ANTHROPIC_API_KEY (or CLAUDE_API_KEY) in Railway,
// or the key the owner pastes in the platform settings (keys.go).
// Speech becomes text on the server itself (asr_local.go, Whisper through
// sherpa-onnx); OPENAI_API_KEY adds Whisper API as a fallback.
// Gemini stays only as an emergency switch: GEMINI_ENABLED=1 with
// GEMINI_API_KEY puts it after Claude (text, search, transcription, tour TTS).
// AI_TEXT_ORDER (e.g. "claude,openai") changes which model is asked first.
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
	Anthropic, Gemini, OpenAI             string // keys (Gemini only with GEMINI_ENABLED=1, FromEnv)
	ClaudeModel, GeminiModel              string
	HeavyModel                            string // AI_MODEL_HEAVY: Heavy(ctx) tasks
	OpenAIModel, OpenAISTTModel           string
	AnthropicBase, GeminiBase, OpenAIBase string
	HTTP                                  *http.Client
	// Keys: the Claude key saved in the settings, used when Anthropic is empty.
	Keys *KeyBox
	// ASR: speech to text on the server (asr_local.go); nil: off.
	ASR *LocalASR

	modelMu sync.Mutex // GeminiModel may be switched when Google retires a model
	quota   quotaState // services whose quota is used up (quota.go)

	// TextOrder: the order Text, JSON and Search ask the models in
	// (AI_TEXT_ORDER; default gemini, claude, openai).
	TextOrder []string
	// OnQuota is told when a service closes for a long time (a daily or
	// billing quota); the platform tells the owner once a day (R32c).
	OnQuota func(*QuotaError)
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
	r, _ := http.NewRequest("GET", c.GeminiBase+"/v1beta/models?pageSize=1000&key="+c.Gemini, nil)
	b, err := c.do(ctx, r)
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
	if q := c.quotaClosed("gemini"); q != nil {
		return nil, q
	}
	for try := 0; ; try++ {
		url := fmt.Sprintf("%s/v1beta/models/%s:%s?key=%s", c.GeminiBase, c.geminiModel(), method, c.Gemini)
		b, err := c.do(ctx, jsonReq("POST", url, body))
		if q := c.noteQuota("gemini", err); q != nil {
			return nil, q
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

func FromEnv() *Client {
	env := func(k, def string) string {
		if v := strings.TrimSpace(os.Getenv(k)); v != "" {
			return v
		}
		return def
	}
	gem := ""
	if GeminiEnabled() {
		gem = env("GEMINI_API_KEY", "")
	}
	return &Client{
		Anthropic:      env("ANTHROPIC_API_KEY", env("CLAUDE_API_KEY", "")),
		Gemini:         gem,
		OpenAI:         env("OPENAI_API_KEY", ""),
		ClaudeModel:    env("AI_MODEL", env("AI_CLAUDE_MODEL", DefaultModel)),
		HeavyModel:     env("AI_MODEL_HEAVY", DefaultHeavyModel),
		Keys:           SharedKeys,
		ASR:            LocalASRFromEnv(),
		GeminiModel:    env("AI_GEMINI_MODEL", "gemini-3.8-flash"),
		OpenAIModel:    env("AI_OPENAI_MODEL", "gpt-4o-mini"),
		OpenAISTTModel: env("AI_OPENAI_STT_MODEL", "whisper-1"),
		AnthropicBase:  env("ANTHROPIC_API_BASE", "https://api.anthropic.com"),
		GeminiBase:     env("GEMINI_API_BASE", "https://generativelanguage.googleapis.com"),
		OpenAIBase:     env("OPENAI_API_BASE", "https://api.openai.com"),
		HTTP:           &http.Client{Timeout: 10 * time.Minute},
		TextOrder:      strings.FieldsFunc(strings.ToLower(env("AI_TEXT_ORDER", "")), func(r rune) bool { return r == ',' || r == ' ' }),
	}
}

// GeminiEnabled: the emergency switch GEMINI_ENABLED=1 (default off, R34a).
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
	if c.Gemini != "" {
		out = append(out, "gemini")
	}
	return out
}

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
	if c.Paused() {
		st["paused"], st["message"] = true, QuotaMessage
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
		switch m {
		case "claude", "gemini":
			if c.quotaClosed(m) == nil {
				return false
			}
		default:
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
	if errors.Is(err, ErrNoKey) {
		return ErrNoKey.Error()
	}
	var he *HTTPError
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

var ErrNoKey = errors.New("Нет ключа Claude: вставьте его в Настройках платформы («Ключ Claude») или в переменную ANTHROPIC_API_KEY в Railway")

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
	has := map[string]bool{"gemini": c.Gemini != "", "claude": c.claudeKey() != "", "openai": c.OpenAI != ""}
	var out []string
	seen := map[string]bool{}
	for _, m := range append(append([]string{}, c.TextOrder...), "claude", "gemini", "openai") {
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
	var first error
	for _, m := range models {
		var ans string
		var err error
		switch m {
		case "claude":
			ans, err = c.claude(ctx, system, prompt, asJSON)
		case "gemini":
			if asJSON {
				ans, err = c.geminiCfg(ctx, system, []map[string]any{{"text": prompt}}, map[string]any{"responseMimeType": "application/json", "temperature": 0.2})
			} else {
				ans, err = c.gemini(ctx, system, []map[string]any{{"text": prompt}})
			}
		case "openai":
			ans, err = c.openaiChat(ctx, system, prompt)
		}
		if err == nil {
			return ans, nil
		}
		// a paused Gemini says less than the fallback's own failure
		if first == nil || (IsQuota(first) && !IsQuota(err)) {
			first = err
		}
		if !fallbackWorthy(ctx, err) {
			break
		}
	}
	return "", first
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
	if q := c.quotaClosed("gemini"); q != nil {
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
	body := map[string]any{
		"system_instruction": map[string]any{"parts": []map[string]any{{"text": system}}},
		"contents":           []map[string]any{{"role": "user", "parts": parts}},
	}
	if cfg != nil {
		body["generationConfig"] = cfg
	}
	b, err := c.geminiCall(ctx, "generateContent", body)
	if err != nil {
		return "", err
	}
	var out struct {
		Candidates []struct {
			Content struct {
				Parts []struct {
					Text string `json:"text"`
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
			sb.WriteString(p.Text)
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
	start := jsonReq("POST", c.GeminiBase+"/upload/v1beta/files?key="+c.Gemini, map[string]any{"file": map[string]any{"display_name": "call"}})
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
		g, _ := http.NewRequest("GET", c.GeminiBase+"/v1beta/"+f.File.Name+"?key="+c.Gemini, nil)
		if b2, err := c.do(ctx, g); err == nil {
			_ = json.Unmarshal(b2, &f.File)
		}
	}
	if f.File.URI == "" {
		return nil, errors.New("ИИ не принял запись")
	}
	return map[string]any{"file_data": map[string]any{"mime_type": mime, "file_uri": f.File.URI}}, nil
}

func (c *Client) openaiChat(ctx context.Context, system, prompt string) (string, error) {
	r := jsonReq("POST", c.OpenAIBase+"/v1/chat/completions", map[string]any{
		"model":    c.OpenAIModel,
		"messages": []map[string]any{{"role": "system", "content": system}, {"role": "user", "content": prompt}},
	})
	r.Header.Set("Authorization", "Bearer "+c.OpenAI)
	b, err := c.do(ctx, r)
	if err != nil {
		return "", err
	}
	var out struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(b, &out); err != nil || len(out.Choices) == 0 {
		return "", errors.New("ИИ вернул пустой ответ")
	}
	return out.Choices[0].Message.Content, nil
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
}

const eventsPrompt = `Найди в интернете бизнес-мероприятия в Алматы на ближайшие %d дней, начиная с %s: конференции, форумы, нетворкинги, бизнес-завтраки, мастер-классы и лекции для предпринимателей, выставки.
Источники: ticketon.kz, sxodim.com, afisha, сайты организаторов, Telegram-каналы с анонсами, Astana Hub, Atameken, Forbes Kazakhstan, бизнес-клубы.
Верни ТОЛЬКО JSON: {"items":[{"title":"...","date":"YYYY-MM-DD","time":"HH:MM","place":"...","url":"ссылка на страницу события","price":"бесплатно или цена","source":"домен","tags":["нетворкинг|конференция|обучение|выставка|завтрак|IT|маркетинг|финансы|продажи"]}]}
Только реальные события с датой и ссылкой, которые ты нашёл в поиске. Не выдумывай. До 30 событий.`

// Search asks a model that can search the web (Claude with the web search
// server tool; Gemini with google_search only with GEMINI_ENABLED=1) and
// returns its text answer. An overloaded or rate-limited model is asked
// again (SearchBackoff).
func (c *Client) Search(ctx context.Context, prompt string) (string, error) {
	if c.Gemini == "" && c.claudeKey() == "" {
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
	for _, m := range c.TextModels() {
		var ans string
		var err error
		switch m {
		case "gemini":
			ans, err = try(c.geminiSearch)
		case "claude":
			ans, err = try(c.claudeSearch)
		default:
			continue
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

func (c *Client) geminiSearch(ctx context.Context, prompt string) (string, error) {
	b, err := c.geminiCall(ctx, "generateContent", map[string]any{
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
