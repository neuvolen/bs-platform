package ai

import (
	"context"
	"encoding/json"

	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
)

// fakeGoogle: generativelanguage.googleapis.com as it answers an AI Studio
// key: the key must come in the x-goog-api-key header; a key in the URL is
// refused (and must never be sent). Grounded calls must carry
// tools:[{"google_search":{}}].
type fakeGoogle struct {
	key      string
	calls    atomic.Int32
	searches atomic.Int32
	mu       sync.Mutex
	paths    []string
}

func (f *fakeGoogle) server(t *testing.T) *httptest.Server {
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.calls.Add(1)
		b, _ := io.ReadAll(r.Body)
		f.mu.Lock()
		f.paths = append(f.paths, r.URL.String())
		f.mu.Unlock()
		if r.URL.Query().Get("key") != "" {
			t.Errorf("key in the URL: %s", r.URL.Path)
		}
		if r.Header.Get("x-goog-api-key") != f.key {
			w.WriteHeader(400)
			_, _ = w.Write([]byte(`{"error":{"code":400,"message":"API key not valid. Please pass a valid API key.","status":"INVALID_ARGUMENT","details":[{"@type":"type.googleapis.com/google.rpc.ErrorInfo","reason":"API_KEY_INVALID"}]}}`))
			return
		}
		var body struct {
			Tools []map[string]any `json:"tools"`
		}
		_ = json.Unmarshal(b, &body)
		text := "ок"
		if len(body.Tools) > 0 {
			if _, ok := body.Tools[0]["google_search"]; !ok {
				t.Errorf("grounding tool: %s", b)
			}
			f.searches.Add(1)
			text = "akimat.almaty.gov.kz"
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"candidates": []any{map[string]any{"content": map[string]any{"parts": []any{map[string]any{"text": text}}}}}})
	}))
	t.Cleanup(s.Close)
	return s
}

// R51, prod 06.10.2026: the owner's new free key from AI Studio («AQ.…»),
// Claude without balance: the search was asked with the key in the URL and
// Google answered 400 «API key not valid». The key now goes in the header:
// text, grounded search and the /status ping all work with it.
func TestR51GeminiKeyInHeaderTextAndSearch(t *testing.T) {
	const key = "AQ.Ab8RN6Ltest-key_with.dots-0123456789abcdefXYZ"
	f := &fakeGoogle{key: key}
	srv := f.server(t)
	c := &Client{Gemini: key, GeminiModel: "gemini-3.8-flash", GeminiLightModel: "gemini-3.5-flash-lite", GeminiBase: srv.URL, HTTP: srv.Client()}
	if ans, err := c.Text(context.Background(), "s", "p"); err != nil || ans != "ок" {
		t.Fatalf("text: %q %v", ans, err)
	}
	ans, err := c.Search(context.Background(), "найди сайт акимата")
	if err != nil || !strings.Contains(ans, "akimat") || f.searches.Load() != 1 {
		t.Fatalf("search: %q %v searches=%d", ans, err, f.searches.Load())
	}
	si, err := c.SearchPing(context.Background())
	if err != nil || !si.OK || si.Tool != "google_search" {
		t.Fatalf("ping: %+v %v", si, err)
	}
	if _, err := c.PingProvider(context.Background(), "gemini"); err != nil {
		t.Fatal(err)
	}
	if st := c.state("gemini"); st.State != "ok" {
		t.Fatalf("state: %+v", st)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, p := range f.paths {
		if strings.Contains(p, key) {
			t.Fatalf("the key is in a URL: %s", p)
		}
	}
}

// A key Google refuses: one call, then the provider rests (no more calls),
// /status says «ключ не принят» instead of «работает», the chain and the
// search go on with the next provider, and the message is one plain action.
func TestR51RefusedKeyRestsAndIsShown(t *testing.T) {
	f := &fakeGoogle{key: "AIzaRIGHTkey-0000000000000000000000000"}
	srv := f.server(t)
	compat, _ := fakeCompatAI(t)
	c := &Client{Gemini: "AIzaWRONG", GeminiModel: "gemini-3.8-flash", GeminiBase: srv.URL, HTTP: srv.Client(),
		Groq: "gsk_x", GroqBase: compat.URL, GroqModel: "g"}
	// the /status ping
	if _, err := c.PingProvider(context.Background(), "gemini"); !IsKeyRejected(err) {
		t.Fatalf("ping: %v", err)
	}
	st := c.state("gemini")
	if st.State != "badkey" || !strings.Contains(st.Action, "aistudio.google.com") || !strings.Contains(st.Problem, "39") {
		t.Fatalf("state: %+v", st)
	}
	if a := c.Answering(); a != "groq" {
		t.Fatalf("answering: %q", a)
	}
	n := f.calls.Load()
	// text goes to Groq without asking Google again
	if ans, err := c.Text(context.Background(), "s", "p"); err != nil || ans != "ок" || f.calls.Load() != n {
		t.Fatalf("text: %q %v calls %d→%d", ans, err, n, f.calls.Load())
	}
	// the search has no other provider: one clean sentence, Google not called
	_, err := c.Search(context.Background(), "q")
	if !IsKeyRejected(err) || !SearchUnavailable(err) || f.calls.Load() != n {
		t.Fatalf("search: %v calls=%d", err, f.calls.Load())
	}
	if m := UserMessage(err); strings.Contains(m, "API key not valid") || !strings.Contains(m, "GEMINI_API_KEY") {
		t.Fatal(m)
	}
	// the owner replaces the key (a restart): the rest is over
	c.Gemini = f.key
	c.ClearBadKey("gemini")
	if ans, err := c.Search(context.Background(), "q"); err != nil || ans == "" {
		t.Fatalf("after the new key: %v", err)
	}
}

func TestR51CleanAPIKey(t *testing.T) {
	for in, want := range map[string]string{
		"AIzaSyA-1234567890123456789012345678901": "AIzaSyA-1234567890123456789012345678901",
		"  \"AQ.Ab8RN6L-abc_def.ghi\"\n":          "AQ.Ab8RN6L-abc_def.ghi",
		"GEMINI_API_KEY=AQ.Ab8RN6L-abc":           "AQ.Ab8RN6L-abc",
		"GEMINI_API_KEY = \"AIzaXYZ\"":            "AIzaXYZ",
		"AIzaXYZ # мой ключ":                      "AIzaXYZ",
		"AIzaXYZ\nAIzaOTHER":                      "AIzaXYZ",
		"Bearer AQ.Ab8RN6L":                       "AQ.Ab8RN6L",
	} {
		if got := cleanAPIKey(in); got != want {
			t.Errorf("%q: %q, want %q", in, got, want)
		}
	}
	for k, want := range map[string]string{
		"AIzaSyA-1234567890123456789012345678901": "",
		"AIzaShort": "не полностью",
		"AQ.Ab8RN6Ltest-key_with.dots-0123456789abcdef": "",
		"sk-ant-api03-xyz": "начинается иначе",
	} {
		if h := geminiShapeHint(k); (want == "" && h != "") || !strings.Contains(h, want) {
			t.Errorf("%s: %q", k, h)
		}
	}
	if s := keyShape("AQ.Ab8RN6Lsecret"); strings.Contains(s, "secret") || !strings.HasPrefix(s, "AQ.") {
		t.Fatal(s)
	}
}

// fakeCompatAI: an OpenAI-style chat endpoint (Groq) that answers «ок».
func fakeCompatAI(t *testing.T) (*httptest.Server, *atomic.Int32) {
	var n atomic.Int32
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n.Add(1)
		_, _ = io.ReadAll(r.Body)
		_ = json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"message": map[string]any{"content": "ок"}}}})
	}))
	t.Cleanup(s.Close)
	return s, &n
}
