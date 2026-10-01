// Package ai talks to the language and speech models the platform uses:
// the voice assistant (a spoken phrase becomes board actions) and the call
// summary (a recording becomes a transcript, a summary and a checklist).
//
// One key is enough. GEMINI_API_KEY covers both speech and text;
// ANTHROPIC_API_KEY (Claude) is used for text when present; OPENAI_API_KEY
// works for speech (Whisper) and text as a fallback.
package ai

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"os"
	"strings"
	"time"
)

type Client struct {
	Anthropic, Gemini, OpenAI             string // keys
	ClaudeModel, GeminiModel              string
	OpenAIModel, OpenAISTTModel           string
	AnthropicBase, GeminiBase, OpenAIBase string
	HTTP                                  *http.Client
}

func FromEnv() *Client {
	env := func(k, def string) string {
		if v := strings.TrimSpace(os.Getenv(k)); v != "" {
			return v
		}
		return def
	}
	return &Client{
		Anthropic:      env("ANTHROPIC_API_KEY", ""),
		Gemini:         env("GEMINI_API_KEY", ""),
		OpenAI:         env("OPENAI_API_KEY", ""),
		ClaudeModel:    env("AI_CLAUDE_MODEL", "claude-sonnet-5"),
		GeminiModel:    env("AI_GEMINI_MODEL", "gemini-2.5-flash"),
		OpenAIModel:    env("AI_OPENAI_MODEL", "gpt-4o-mini"),
		OpenAISTTModel: env("AI_OPENAI_STT_MODEL", "whisper-1"),
		AnthropicBase:  env("ANTHROPIC_API_BASE", "https://api.anthropic.com"),
		GeminiBase:     env("GEMINI_API_BASE", "https://generativelanguage.googleapis.com"),
		OpenAIBase:     env("OPENAI_API_BASE", "https://api.openai.com"),
		HTTP:           &http.Client{Timeout: 15 * time.Minute},
	}
}

// Status says what is available, for the page to explain what is missing.
func (c *Client) Status() map[string]any {
	text, speech := "", ""
	switch {
	case c.Anthropic != "":
		text = "claude"
	case c.Gemini != "":
		text = "gemini"
	case c.OpenAI != "":
		text = "openai"
	}
	switch {
	case c.Gemini != "":
		speech = "gemini"
	case c.OpenAI != "":
		speech = "openai"
	}
	return map[string]any{"text": text, "speech": speech}
}

var ErrNoKey = errors.New("нет ключа ИИ: добавьте GEMINI_API_KEY в переменные Railway")

// Text answers a prompt. system sets the role; the answer is plain text.
func (c *Client) Text(ctx context.Context, system, prompt string) (string, error) {
	switch {
	case c.Anthropic != "":
		return c.claude(ctx, system, prompt)
	case c.Gemini != "":
		return c.gemini(ctx, system, []map[string]any{{"text": prompt}})
	case c.OpenAI != "":
		return c.openaiChat(ctx, system, prompt)
	}
	return "", ErrNoKey
}

// JSON answers a prompt expecting a JSON object (Gemini is put into JSON mode).
func (c *Client) JSON(ctx context.Context, system, prompt string) (string, error) {
	if c.Anthropic == "" && c.Gemini != "" {
		return c.geminiCfg(ctx, system, []map[string]any{{"text": prompt}}, map[string]any{"responseMimeType": "application/json", "temperature": 0.2})
	}
	return c.Text(ctx, system, prompt)
}

// Transcribe turns a recording into text with speakers where the model can tell them.
func (c *Client) Transcribe(ctx context.Context, audio []byte, mime string) (string, error) {
	if mime == "" {
		mime = "audio/webm"
	}
	switch {
	case c.Gemini != "":
		part, err := c.geminiMedia(ctx, audio, mime)
		if err != nil {
			return "", err
		}
		return c.gemini(ctx, "Ты точно расшифровываешь деловые созвоны на русском языке.",
			[]map[string]any{part, {"text": "Дословно расшифруй запись созвона. Это разбор бизнеса: трекер (Рустам или Береке) и резидент. " +
				"Раздели реплики по говорящим: «Трекер:» и «Резидент:», каждая с новой строки. Без комментариев от себя."}})
	case c.OpenAI != "":
		return c.whisper(ctx, audio, mime)
	}
	return "", errors.New("нет ключа для расшифровки речи: добавьте GEMINI_API_KEY в переменные Railway")
}

func (c *Client) do(ctx context.Context, req *http.Request) ([]byte, error) {
	res, err := c.HTTP.Do(req.WithContext(ctx))
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(res.Body, 32<<20))
	if res.StatusCode >= 300 {
		msg := string(b)
		if len(msg) > 300 {
			msg = msg[:300]
		}
		return nil, fmt.Errorf("ИИ ответил %d: %s", res.StatusCode, msg)
	}
	return b, nil
}

func jsonReq(method, url string, body any) *http.Request {
	b, _ := json.Marshal(body)
	r, _ := http.NewRequest(method, url, bytes.NewReader(b))
	r.Header.Set("Content-Type", "application/json")
	return r
}

func (c *Client) claude(ctx context.Context, system, prompt string) (string, error) {
	r := jsonReq("POST", c.AnthropicBase+"/v1/messages", map[string]any{
		"model": c.ClaudeModel, "max_tokens": 8000, "system": system,
		"messages": []map[string]any{{"role": "user", "content": prompt}},
	})
	r.Header.Set("x-api-key", c.Anthropic)
	r.Header.Set("anthropic-version", "2023-06-01")
	b, err := c.do(ctx, r)
	if err != nil {
		return "", err
	}
	var out struct {
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
	}
	if err := json.Unmarshal(b, &out); err != nil {
		return "", err
	}
	var sb strings.Builder
	for _, p := range out.Content {
		if p.Type == "text" {
			sb.WriteString(p.Text)
		}
	}
	return sb.String(), nil
}

func (c *Client) gemini(ctx context.Context, system string, parts []map[string]any) (string, error) {
	return c.geminiCfg(ctx, system, parts, nil)
}

func (c *Client) geminiCfg(ctx context.Context, system string, parts []map[string]any, cfg map[string]any) (string, error) {
	url := fmt.Sprintf("%s/v1beta/models/%s:generateContent?key=%s", c.GeminiBase, c.GeminiModel, c.Gemini)
	body := map[string]any{
		"system_instruction": map[string]any{"parts": []map[string]any{{"text": system}}},
		"contents":           []map[string]any{{"role": "user", "parts": parts}},
	}
	if cfg != nil {
		body["generationConfig"] = cfg
	}
	r := jsonReq("POST", url, body)
	b, err := c.do(ctx, r)
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

// FindEvents searches the web for Almaty business events (needs a model with web search).
func (c *Client) FindEvents(ctx context.Context, days int, from time.Time) ([]Event, error) {
	prompt := fmt.Sprintf(eventsPrompt, days, from.Format("2006-01-02"))
	var ans string
	var err error
	switch {
	case c.Gemini != "":
		url := fmt.Sprintf("%s/v1beta/models/%s:generateContent?key=%s", c.GeminiBase, c.GeminiModel, c.Gemini)
		r := jsonReq("POST", url, map[string]any{
			"contents": []map[string]any{{"role": "user", "parts": []map[string]any{{"text": prompt}}}},
			"tools":    []map[string]any{{"google_search": map[string]any{}}},
		})
		var b []byte
		if b, err = c.do(ctx, r); err == nil {
			var out struct {
				Candidates []struct {
					Content struct {
						Parts []struct {
							Text string `json:"text"`
						} `json:"parts"`
					} `json:"content"`
				} `json:"candidates"`
			}
			_ = json.Unmarshal(b, &out)
			for _, cnd := range out.Candidates {
				for _, p := range cnd.Content.Parts {
					ans += p.Text
				}
				break
			}
		}
	case c.Anthropic != "":
		r := jsonReq("POST", c.AnthropicBase+"/v1/messages", map[string]any{
			"model": c.ClaudeModel, "max_tokens": 8000,
			"tools":    []map[string]any{{"type": "web_search_20250305", "name": "web_search", "max_uses": 8}},
			"messages": []map[string]any{{"role": "user", "content": prompt}},
		})
		r.Header.Set("x-api-key", c.Anthropic)
		r.Header.Set("anthropic-version", "2023-06-01")
		var b []byte
		if b, err = c.do(ctx, r); err == nil {
			var out struct {
				Content []struct {
					Type string `json:"type"`
					Text string `json:"text"`
				} `json:"content"`
			}
			_ = json.Unmarshal(b, &out)
			for _, p := range out.Content {
				if p.Type == "text" {
					ans += p.Text
				}
			}
		}
	default:
		return nil, errors.New("поиск мероприятий работает с GEMINI_API_KEY или ANTHROPIC_API_KEY")
	}
	if err != nil {
		return nil, err
	}
	var out struct {
		Items []Event `json:"items"`
	}
	if js := JSONFrom(ans); js == "" || json.Unmarshal([]byte(js), &out) != nil {
		return nil, errors.New("ИИ не вернул список мероприятий")
	}
	today := from.Format("2006-01-02")
	var keep []Event
	seen := map[string]bool{}
	for _, e := range out.Items {
		e.Title, e.URL = strings.TrimSpace(e.Title), strings.TrimSpace(e.URL)
		if e.Title == "" || len(e.Date) != 10 || e.Date < today || !strings.HasPrefix(e.URL, "http") {
			continue
		}
		k := strings.ToLower(e.Title) + e.Date
		if seen[k] {
			continue
		}
		seen[k] = true
		keep = append(keep, e)
	}
	return keep, nil
}
