package http

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/bnursik/business_surgery_backend/internal/ai"
)

// fakeCompat: an OpenAI-compatible provider (Groq) that answers every prompt with text.
func fakeCompat(t *testing.T, text func(prompt string) string) (*httptest.Server, *atomic.Int32) {
	var n atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n.Add(1)
		var body struct {
			Model    string `json:"model"`
			Messages []struct {
				Content string `json:"content"`
			} `json:"messages"`
		}
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &body)
		p := ""
		if len(body.Messages) > 1 {
			p = body.Messages[1].Content
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"model": body.Model,
			"choices": []any{map[string]any{"message": map[string]any{"content": text(p)}, "finish_reason": "stop"}}})
	}))
	t.Cleanup(srv.Close)
	return srv, &n
}

// R42: the owner hears once when another provider starts answering; a
// restart does not repeat it (bs_ai_active); Claude's return is told too.
func TestR42SwitchAlert(t *testing.T) {
	repo, ctx := testPlatformDB(t, aiActiveDoc)
	QuotaAlertsToBot = true // R66: off in production
	defer func() { QuotaAlertsToBot = false }()
	var mu sync.Mutex
	var sent []string
	h := &PlatformAI{repo: repo, Owner: 7, AI: &ai.Client{Gemini: "g"},
		Notify: func(_ context.Context, _ int64, text string) error {
			mu.Lock()
			sent = append(sent, text)
			mu.Unlock()
			return nil
		}}
	aiActive.Lock()
	aiActive.name = ""
	aiActive.Unlock()
	h.switchAlert("", "gemini")
	h.switchAlert("claude", "gemini")
	// a restart: the doc remembers
	aiActive.Lock()
	aiActive.name = ""
	aiActive.Unlock()
	h.switchAlert("", "gemini")
	h.switchAlert("gemini", "claude")
	mu.Lock()
	defer mu.Unlock()
	if len(sent) != 2 || !strings.Contains(sent[0], "Gemini") || !strings.Contains(sent[0], "бесплатный") || !strings.Contains(sent[1], "снова работает через Claude") {
		t.Fatalf("sent: %q", sent)
	}
	for _, s := range sent {
		if strings.Contains(s, "—") {
			t.Fatal("em dash")
		}
	}
	d, _ := repo.GetDoc(ctx, "club", aiActiveDoc)
	if d == nil || d.Value != `"claude"` {
		t.Fatalf("doc: %+v", d)
	}
	// Claude's balance pause is not a «pause» message while Gemini answers
	n := 0
	h2 := &PlatformAI{Owner: 7, AI: &ai.Client{Anthropic: "k", Gemini: "g"},
		Notify: func(context.Context, int64, string) error { n++; return nil }}
	h2.quotaAlert(&ai.QuotaError{Service: "claude", Billing: true, Until: time.Now().Add(time.Hour)})
	if n != 0 {
		t.Fatal("pause message although Gemini answers")
	}
}

// R42: no model can search: the AI recommendation comes from the model's
// knowledge (Groq here), marked «без поиска»; events keep the feed.
func TestR42RecsAndEventsWithoutSearch(t *testing.T) {
	repo, ctx := testPlatformDB(t, aiRecsKey, "bs_tools", "bs_diag", "bs_lib_rich", eventsFeedKey)
	if _, err := repo.PutDoc(ctx, "club", "bs_tools", 0, `[{"organ":"Финансы","title":"Платёжный календарь","short":"x","why":"y","how":["a"]}]`, false, "test"); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.PutDoc(ctx, "club", eventsFeedKey, 0, `{"items":[{"title":"Старое","date":"2026-10-20"}]}`, false, "test"); err != nil {
		t.Fatal(err)
	}
	var prompts []string
	var mu sync.Mutex
	srv, _ := fakeCompat(t, func(p string) string {
		mu.Lock()
		prompts = append(prompts, p)
		mu.Unlock()
		if strings.Contains(p, "Кандидат в библиотеку") {
			return `{"duplicate":false,"of":""}`
		}
		return `{"kind":"tool","title":"Тринадцатинедельный прогноз денег","summary":"Прогноз денег на 13 недель.","source":"Практика CFO","url":"","why":"Видно разрыв заранее.","steps":["Выписать платежи","Добавить поступления","Посчитать остаток","Отметить минус","Обновлять"],"metrics":["Дней до разрыва"],"adapt":"Учесть Kaspi.","short":"Деньги на 13 недель","example":"Кофейня.","time":"2 часа","check":["Собран"]}`
	})
	c := &ai.Client{Groq: "gsk", GroqBase: srv.URL, HTTP: srv.Client()}
	h := NewPlatformAI(repo, c)
	rec, err := h.dailyRec(ctx, time.Now(), true)
	if err != nil || rec["nosearch"] != true || rec["title"] != "Тринадцатинедельный прогноз денег" {
		t.Fatalf("rec: %+v %v", rec, err)
	}
	mu.Lock()
	if !strings.Contains(prompts[0], "Поиск в интернете сейчас недоступен") || strings.Contains(prompts[0], "Найди в интернете") {
		t.Fatalf("prompt: %s", prompts[0])
	}
	mu.Unlock()
	// events: the feed stays; R70: the web part is skipped quietly (no error)
	h.Run = func(f func()) { f() }
	<-h.startEvents("button")
	st := h.eventsState()
	if st["nosearch"] != true || st["error"] != nil {
		t.Fatalf("events: %+v", st)
	}
	d, _ := repo.GetDoc(ctx, "club", eventsFeedKey)
	if !strings.Contains(d.Value, "Старое") {
		t.Fatalf("feed lost: %s", d.Value)
	}
}

// R42: /status shows every provider and who answers; the Threads batch
// leaves the free budget to the team.
func TestR42StatusChainAndThreadsBudget(t *testing.T) {
	t.Setenv("AI_BUDGET_GROQ", "10")
	srv, n := fakeCompat(t, func(string) string { return "ок" })
	c := &ai.Client{Groq: "gsk", GroqBase: srv.URL, HTTP: srv.Client()}
	s := &SysCheck{AI: c}
	it := s.chain(context.Background())
	if it.State != "ok" || it.Sig != "groq" || !strings.Contains(it.Text, "Groq: работает, 1 из 10 сегодня (отвечает сейчас)") ||
		!strings.Contains(it.Text, "Claude: нет ключа (ANTHROPIC_API_KEY)") || !strings.Contains(it.Text, "Gemini: нет ключа (GEMINI_API_KEY)") {
		t.Fatalf("chain: %+v", it)
	}
	cl := s.claude(context.Background())
	if cl.State != "off" || cl.Note != "" {
		t.Fatalf("claude line without a key while Groq answers: %+v", cl)
	}
	sr := s.search(context.Background())
	if sr.State != "off" {
		t.Fatalf("search: %+v", sr)
	}
	f := ThreadsAI(c)
	if ans, err := f(context.Background(), "s", "p"); err != nil || ans != "ок" {
		t.Fatalf("threads: %q %v", ans, err)
	}
	c.SetUsed("groq", 7) // 3 of 10 left: under the 40 % reserve
	if _, err := f(context.Background(), "s", "p"); !errors.Is(err, ErrThreadsBudget) {
		t.Fatalf("threads within budget: %v", err)
	}
	if n.Load() != 2 {
		t.Fatalf("calls: %d", n.Load())
	}
	c.SetQuotaUntil("groq", time.Now().Add(2*time.Hour))
	if l := ProviderLine(c.ProviderStates()[0]); !strings.HasPrefix(l, "Groq: лимит до ") {
		t.Fatal(l)
	}
	// R66: the free provider rests and comes back by itself: a calm warning, nothing to do
	if it := s.chain(context.Background()); it.State != "warn" || !strings.HasPrefix(it.Text, "на паузе, восстановятся сами к ") || it.Note != "" {
		t.Fatalf("none answers: %+v", it)
	}
}
