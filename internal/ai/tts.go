package ai

import (
	"context"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Speech synthesis for the platform's voice guide (onboarding tour).
// Gemini TTS answers raw PCM (s16le, mono, 24 kHz); Speak wraps it into WAV.

// DefaultTTSModel is used when the models list cannot be read.
const DefaultTTSModel = "gemini-2.5-flash-preview-tts"

// ttsModels remembers the TTS model picked for each client.
var ttsModels sync.Map // *Client -> string

// TTSModel is the speech model in use: AI_GEMINI_TTS_MODEL, otherwise the
// newest model whose name contains "tts" (picked once from the models list).
func (c *Client) TTSModel(ctx context.Context) string {
	if v := strings.TrimSpace(os.Getenv("AI_GEMINI_TTS_MODEL")); v != "" {
		return v
	}
	if v, ok := ttsModels.Load(c); ok {
		return v.(string)
	}
	if m := c.newestTTS(ctx, ""); m != "" {
		ttsModels.Store(c, m)
		return m
	}
	return DefaultTTSModel
}

var ttsVerRe = regexp.MustCompile(`gemini-(\d+(?:\.\d+)?)`)

// newestTTS: the newest "tts" model the key can call; flash before pro at
// the same version (faster), a stable model before a preview.
func (c *Client) newestTTS(ctx context.Context, skip string) string {
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
		name  string
		score float64
	}
	var cs []cand
	for _, m := range out.Models {
		n := strings.TrimPrefix(m.Name, "models/")
		low := strings.ToLower(n)
		if !strings.Contains(low, "tts") || n == skip {
			continue
		}
		ok := len(m.Methods) == 0
		for _, g := range m.Methods {
			if g == "generateContent" {
				ok = true
			}
		}
		if !ok {
			continue
		}
		v := 0.0
		if mm := ttsVerRe.FindStringSubmatch(low); mm != nil {
			v, _ = strconv.ParseFloat(mm[1], 64)
		}
		if strings.Contains(low, "flash") {
			v += 0.002
		}
		if strings.Contains(low, "preview") || strings.Contains(low, "exp") {
			v -= 0.001
		}
		cs = append(cs, cand{n, v})
	}
	sort.SliceStable(cs, func(i, j int) bool { return cs[i].score > cs[j].score })
	if len(cs) == 0 {
		return ""
	}
	return cs[0].name
}

// Speak turns text into a WAV recording read by a prebuilt Gemini voice.
// style is a short spoken-manner instruction put in front of the text.
// TTSBackoff is shortened in tests.
var TTSBackoff = func(d time.Duration) time.Duration { return d }

// TTSAttemptTimeout: one generateContent call of Speak (a phrase takes a few
// seconds; a call that hangs is cut and the phrase tried again later).
var TTSAttemptTimeout = 45 * time.Second

func (c *Client) Speak(ctx context.Context, text, voice, style string) ([]byte, error) {
	if c.Gemini == "" {
		return nil, errors.New("нет ключа для озвучки: добавьте GEMINI_API_KEY")
	}
	prompt := text
	if style = strings.TrimSpace(style); style != "" {
		prompt = style + "\n\n" + text
	}
	body := map[string]any{
		"contents": []map[string]any{{"role": "user", "parts": []map[string]any{{"text": prompt}}}},
		"generationConfig": map[string]any{
			"responseModalities": []string{"AUDIO"},
			"speechConfig": map[string]any{"voiceConfig": map[string]any{
				"prebuiltVoiceConfig": map[string]any{"voiceName": voice}}},
		},
	}
	model := c.TTSModel(ctx)
	rl := 0
	for try := 0; ; try++ {
		url := fmt.Sprintf("%s/v1beta/models/%s:generateContent?key=%s", c.GeminiBase, model, c.Gemini)
		// One call that hangs does not hold the server's only TTS slot.
		actx, cancel := context.WithTimeout(ctx, TTSAttemptTimeout)
		b, err := c.do(actx, jsonReq("POST", url, body))
		cancel()
		if err == nil {
			return speechWAV(b)
		}
		// Rate limit: the free TTS quota is a few requests a minute. Wait and
		// retry rather than fail, so the tour keeps one voice.
		var he *HTTPError
		if errors.As(err, &he) && (he.Status == 429 || he.Status == 503) && rl < 6 {
			rl++
			wait := time.Duration(4+rl*5) * time.Second
			select {
			case <-ctx.Done():
				return nil, err
			case <-time.After(TTSBackoff(wait)):
			}
			try--
			continue
		}
		_, retired := retiredModel(err)
		if try > 0 || !retired || os.Getenv("AI_GEMINI_TTS_MODEL") != "" {
			return nil, err
		}
		next := c.newestTTS(ctx, model)
		if next == "" || next == model {
			return nil, err
		}
		ttsModels.Store(c, next)
		model = next
	}
}

// speechWAV pulls the audio out of a generateContent answer.
func speechWAV(b []byte) ([]byte, error) {
	type inline struct {
		MimeType string `json:"mimeType"`
		Mime2    string `json:"mime_type"`
		Data     string `json:"data"`
	}
	var out struct {
		Candidates []struct {
			Content struct {
				Parts []struct {
					InlineData  *inline `json:"inlineData"`
					InlineData2 *inline `json:"inline_data"`
				} `json:"parts"`
			} `json:"content"`
		} `json:"candidates"`
	}
	if err := json.Unmarshal(b, &out); err != nil {
		return nil, err
	}
	var pcm []byte
	mt := ""
	for _, cnd := range out.Candidates {
		for _, p := range cnd.Content.Parts {
			in := p.InlineData
			if in == nil {
				in = p.InlineData2
			}
			if in == nil || in.Data == "" {
				continue
			}
			d, err := base64.StdEncoding.DecodeString(in.Data)
			if err != nil {
				return nil, err
			}
			if mt == "" {
				mt = in.MimeType + in.Mime2
			}
			pcm = append(pcm, d...)
		}
		break
	}
	if len(pcm) == 0 {
		return nil, errors.New("ИИ не вернул звук")
	}
	low := strings.ToLower(mt)
	if strings.Contains(low, "wav") || (len(pcm) > 12 && string(pcm[:4]) == "RIFF") {
		return pcm, nil
	}
	rate := 24000
	if m := regexp.MustCompile(`rate=(\d+)`).FindStringSubmatch(low); m != nil {
		if v, _ := strconv.Atoi(m[1]); v > 0 {
			rate = v
		}
	}
	return PCMToWAV(pcm, rate, 1, 16), nil
}

// PCMToWAV adds a RIFF/WAVE header to little-endian PCM samples.
func PCMToWAV(pcm []byte, rate, channels, bits int) []byte {
	block := channels * bits / 8
	h := make([]byte, 44, 44+len(pcm))
	copy(h[0:], "RIFF")
	binary.LittleEndian.PutUint32(h[4:], uint32(36+len(pcm)))
	copy(h[8:], "WAVE")
	copy(h[12:], "fmt ")
	binary.LittleEndian.PutUint32(h[16:], 16)
	binary.LittleEndian.PutUint16(h[20:], 1) // PCM
	binary.LittleEndian.PutUint16(h[22:], uint16(channels))
	binary.LittleEndian.PutUint32(h[24:], uint32(rate))
	binary.LittleEndian.PutUint32(h[28:], uint32(rate*block))
	binary.LittleEndian.PutUint16(h[32:], uint16(block))
	binary.LittleEndian.PutUint16(h[34:], uint16(bits))
	copy(h[36:], "data")
	binary.LittleEndian.PutUint32(h[40:], uint32(len(pcm)))
	return append(h, pcm...)
}
