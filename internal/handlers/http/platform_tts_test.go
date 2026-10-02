package http

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/bnursik/business_surgery_backend/internal/ai"
	pg "github.com/bnursik/business_surgery_backend/internal/repository/pg"
	"github.com/bnursik/business_surgery_backend/migrations"
	"github.com/gin-gonic/gin"
)

func platformTestRepo(t *testing.T) (*pg.PlatformRepo, *pg.DB) {
	t.Helper()
	dsn := os.Getenv("BS_TEST_DSN")
	if dsn == "" {
		t.Skip("BS_TEST_DSN not set")
	}
	ctx := context.Background()
	db, err := pg.NewDB(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	if err := pg.Migrate(ctx, db, migrations.FS); err != nil {
		t.Fatal(err)
	}
	return pg.NewPlatformRepo(db), db
}

// fakeGeminiTTS lists models and answers generateContent with raw PCM.
type fakeTTS struct {
	mu     sync.Mutex
	models []string
	gens   []string // model of each generateContent call
	bodies []string
	pcm    []byte
	limit  int // answer 429 this many times first
}

func (f *fakeTTS) server() *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "GET" && strings.HasSuffix(r.URL.Path, "/v1beta/models") {
			var ms []any
			for _, m := range f.models {
				ms = append(ms, map[string]any{"name": "models/" + m, "supportedGenerationMethods": []string{"generateContent", "countTokens"}})
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"models": ms})
			return
		}
		b, _ := io.ReadAll(r.Body)
		model := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/v1beta/models/"), ":generateContent")
		f.mu.Lock()
		f.gens = append(f.gens, model)
		f.bodies = append(f.bodies, string(b))
		f.mu.Unlock()
		f.mu.Lock()
		lim := f.limit
		if lim > 0 {
			f.limit--
		}
		f.mu.Unlock()
		if lim > 0 {
			w.WriteHeader(429)
			_, _ = w.Write([]byte(`{"error":{"code":429,"message":"quota"}}`))
			return
		}
		if !strings.Contains(model, "tts") {
			w.WriteHeader(400)
			_, _ = w.Write([]byte(`{"error":{"message":"model does not support audio"}}`))
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"candidates": []any{map[string]any{"content": map[string]any{"parts": []any{
			map[string]any{"inlineData": map[string]any{"mimeType": "audio/L16;codec=pcm;rate=24000", "data": base64.StdEncoding.EncodeToString(f.pcm)}}}}}}})
	}))
}

func TestPlatformTTS(t *testing.T) {
	repo, db := platformTestRepo(t)
	ctx := context.Background()
	_, _ = db.Pool.Exec(ctx, `DELETE FROM platform_files WHERE id LIKE 'tts\_%'`)
	t.Setenv("AI_GEMINI_TTS_MODEL", "")
	t.Setenv("AI_TTS_VOICE", "")
	pcm := make([]byte, 4800) // 0.1 s of s16le mono 24 kHz
	for i := range pcm {
		pcm[i] = byte(i * 7)
	}
	f := &fakeTTS{pcm: pcm, models: []string{"gemini-3.5-flash", "gemini-2.5-flash-preview-tts", "gemini-2.5-pro-preview-tts", "gemini-3.1-pro-tts", "gemini-3.1-flash-tts", "gemini-3.1-flash-live"}}
	srv := f.server()
	defer srv.Close()
	cl := &ai.Client{Gemini: "k", GeminiModel: "gemini-3.5-flash", GeminiBase: srv.URL, HTTP: srv.Client()}
	h := NewPlatformAI(repo, cl)

	text := "Добро пожаловать на платформу. Проверка голоса."

	gin.SetMode(gin.TestMode)
	role := "resident"
	r := gin.New()
	r.Use(func(c *gin.Context) { c.Set("role", role); c.Set("userID", "tg:1") })
	r.POST("/tts", h.TTS)
	call := func(body string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		req := httptest.NewRequest("POST", "/tts", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		r.ServeHTTP(w, req)
		return w
	}

	// 1. A resident asks for a phrase: the newest flash TTS model reads it, WAV comes back.
	jb, _ := json.Marshal(map[string]string{"text": "  " + text + "\n"})
	w := call(string(jb))
	if w.Code != 200 || w.Header().Get("Content-Type") != "audio/wav" || w.Header().Get("X-TTS-Cache") != "miss" {
		t.Fatalf("first: %d %v %s", w.Code, w.Header(), w.Body.String())
	}
	wav := w.Body.Bytes()
	if len(wav) != 44+len(pcm) || string(wav[0:4]) != "RIFF" || string(wav[8:12]) != "WAVE" || string(wav[12:16]) != "fmt " || string(wav[36:40]) != "data" {
		t.Fatalf("bad WAV header: % x", wav[:44])
	}
	if binary.LittleEndian.Uint32(wav[24:]) != 24000 || binary.LittleEndian.Uint16(wav[22:]) != 1 || binary.LittleEndian.Uint16(wav[34:]) != 16 ||
		binary.LittleEndian.Uint32(wav[40:]) != uint32(len(pcm)) || binary.LittleEndian.Uint32(wav[4:]) != uint32(36+len(pcm)) || !bytes.Equal(wav[44:], pcm) {
		t.Fatalf("bad WAV fields: % x", wav[:44])
	}
	if len(f.gens) != 1 || f.gens[0] != "gemini-3.1-flash-tts" {
		t.Fatalf("model: %v", f.gens)
	}
	body := f.bodies[0]
	for _, want := range []string{`"responseModalities":["AUDIO"]`, `"voiceName":"Charon"`, `"text":"` + text + `"`} {
		if !strings.Contains(body, want) {
			t.Fatalf("request lacks %q: %s", want, body)
		}
	}
	// Only the phrase itself goes to the model: an instruction would be read aloud.
	if strings.Contains(body, "дворецкий") || strings.Contains(body, "Говори") {
		t.Fatalf("instruction sent to TTS: %s", body)
	}

	// 2. The same phrase again: served from storage, the model is not called.
	w = call(string(jb))
	if w.Code != 200 || w.Header().Get("X-TTS-Cache") != "hit" || !bytes.Equal(w.Body.Bytes(), wav) || len(f.gens) != 1 {
		t.Fatalf("second: %d %s gens=%v", w.Code, w.Header().Get("X-TTS-Cache"), f.gens)
	}

	// 3. Another allowed voice is a separate recording; an unknown voice falls back to the default.
	jb2, _ := json.Marshal(map[string]string{"text": text, "voice": "Orus"})
	if w = call(string(jb2)); w.Code != 200 || w.Header().Get("X-TTS-Cache") != "miss" || len(f.gens) != 2 || !strings.Contains(f.bodies[1], `"voiceName":"Orus"`) {
		t.Fatalf("voice: %d %v", w.Code, f.gens)
	}
	jb3, _ := json.Marshal(map[string]string{"text": text, "voice": "<script>"})
	if w = call(string(jb3)); w.Code != 200 || w.Header().Get("X-TTS-Cache") != "hit" || len(f.gens) != 2 {
		t.Fatalf("unknown voice: %d %s %v", w.Code, w.Header().Get("X-TTS-Cache"), f.gens)
	}

	// 3b. Rate limit: the server waits and retries instead of failing.
	ai.TTSBackoff = func(time.Duration) time.Duration { return 10 * time.Millisecond }
	defer func() { ai.TTSBackoff = func(d time.Duration) time.Duration { return d } }()
	f.limit = 3
	jb4, _ := json.Marshal(map[string]string{"text": "Шаг девять после лимита."})
	if w = call(string(jb4)); w.Code != 200 || w.Header().Get("X-TTS-Cache") != "miss" {
		t.Fatalf("429 retry: %d %s", w.Code, w.Body.String())
	}

	// 4. Bad input.
	if w = call(`{"text":"   "}`); w.Code != 400 {
		t.Fatalf("empty: %d", w.Code)
	}
	long, _ := json.Marshal(map[string]string{"text": strings.Repeat("я", ttsMaxRunes+1)})
	if w = call(string(long)); w.Code != 413 {
		t.Fatalf("long: %d", w.Code)
	}

	// 5. Without a key: 503 for a new phrase, the client falls back to the browser voice.
	h2 := NewPlatformAI(repo, &ai.Client{})
	r2 := gin.New()
	r2.POST("/tts", h2.TTS)
	w = httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/tts", strings.NewReader(`{"text":"Новая фраза без ключа"}`))
	r2.ServeHTTP(w, req)
	if w.Code != 503 {
		t.Fatalf("no key: %d", w.Code)
	}
}

func TestPCMToWAVAndModelPick(t *testing.T) {
	w := ai.PCMToWAV([]byte{1, 2, 3, 4}, 16000, 1, 16)
	if len(w) != 48 || binary.LittleEndian.Uint32(w[28:]) != 32000 || binary.LittleEndian.Uint16(w[32:]) != 2 {
		t.Fatalf("wav: % x", w)
	}
	f := &fakeTTS{models: []string{"gemini-2.5-pro-preview-tts", "gemini-2.5-flash-preview-tts", "gemini-3.5-flash"}}
	srv := f.server()
	defer srv.Close()
	t.Setenv("AI_GEMINI_TTS_MODEL", "")
	cl := &ai.Client{Gemini: "k", GeminiBase: srv.URL, HTTP: srv.Client()}
	if m := cl.TTSModel(context.Background()); m != "gemini-2.5-flash-preview-tts" {
		t.Fatalf("pick: %s", m)
	}
	t.Setenv("AI_GEMINI_TTS_MODEL", "custom-tts")
	if m := cl.TTSModel(context.Background()); m != "custom-tts" {
		t.Fatalf("env: %s", m)
	}
}
