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
	"testing"
	"time"

	"github.com/bnursik/business_surgery_backend/internal/ai"
	"github.com/bnursik/business_surgery_backend/internal/buildinfo"
	"github.com/bnursik/business_surgery_backend/internal/club"
	"github.com/gin-gonic/gin"
)

type memMeta struct {
	mu sync.Mutex
	m  map[string]string
}

func newMemMeta() *memMeta { return &memMeta{m: map[string]string{}} }
func (x *memMeta) GetMeta(_ context.Context, k string) (string, error) {
	x.mu.Lock()
	defer x.mu.Unlock()
	return x.m[k], nil
}
func (x *memMeta) SetMeta(_ context.Context, k, v string) error {
	x.mu.Lock()
	defer x.mu.Unlock()
	x.m[k] = v
	return nil
}
func (x *memMeta) Once(_ context.Context, k string, _ time.Duration) (bool, error) {
	x.mu.Lock()
	defer x.mu.Unlock()
	if _, ok := x.m["once:"+k]; ok {
		return false, nil
	}
	x.m["once:"+k] = "1"
	return true, nil
}

func fakeClaude(t *testing.T, key string) *httptest.Server {
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.ReadAll(r.Body)
		if r.Header.Get("x-api-key") != key {
			w.WriteHeader(401)
			_, _ = w.Write([]byte(`{"type":"error","error":{"type":"authentication_error","message":"invalid x-api-key"}}`))
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"stop_reason": "end_turn", "content": []any{map[string]any{"type": "text", "text": "ок"}}})
	}))
	t.Cleanup(s.Close)
	return s
}

// R36: the system check: every line, the text the bot sends, what goes to
// the owner after a deploy (only changes and failures, once per deploy).
func TestR36SystemCheck(t *testing.T) {
	SysPersist = 0 // R66: an AI problem waits a day (TestR66AIProblemWaitsADay); here at once
	defer func() { SysPersist = 24 * time.Hour }()
	const key = "sk-ant-api03-SYSCHECK-0123456789-ABCD"
	cl := fakeClaude(t, key)
	ctx := context.Background()
	club.SetSheetMode(club.SheetModeOff)
	defer club.SetSheetMode(club.SheetModeLegacy)
	keys := &ai.KeyBox{}
	c := &ai.Client{AnthropicBase: cl.URL, ClaudeModel: "claude-sonnet-5", HTTP: cl.Client(), Keys: keys}
	meta := newMemMeta()
	now := time.Date(2026, 10, 4, 14, 35, 0, 0, club.Almaty)
	var sent []string
	sendErr := error(nil)
	owned := true
	commit := "abcdef1234567890"
	s := &SysCheck{AI: c, Meta: meta, Owner: 453800951, Now: func() time.Time { return now },
		DB:      func(context.Context) error { return nil },
		Webhook: func(context.Context) (bool, string) { return owned, "" },
		Export:  func(context.Context) bool { return false },
		Builtin: func() (int, int) { return 33, 33 },
		Build: func() buildinfo.Info {
			return buildinfo.Info{Commit: commit, Built: time.Date(2026, 10, 4, 6, 0, 0, 0, time.UTC)}
		},
		Send: func(_ context.Context, chat int64, text string) error {
			if chat != 453800951 {
				t.Errorf("sent to %d", chat)
			}
			if sendErr != nil {
				return sendErr
			}
			sent = append(sent, text)
			return nil
		}}

	// no Claude key, no leads yet
	r := s.Run(ctx)
	txt := r.Text()
	for _, want := range []string{"🩺 Проверка системы", "❌ ИИ: никто не отвечает", "→ Добавьте бесплатный GEMINI_API_KEY", "⚪ Заявки Tilda: заявок ещё не было · сегодня 0",
		"✅ Голос: встроенные записи, готово 33 из 33 фраз", "✅ Webhook Telegram: у сервера",
		"✅ Версия: abcdef1, сборка 04.10.2026 11:00", "✅ База данных: отвечает", "Проверено 04.10 14:35", "/status подробно"} {
		if !strings.Contains(txt, want) {
			t.Errorf("no %q in\n%s", want, txt)
		}
	}
	if strings.Contains(txt, "Экспорт") { // R51: only while it is on
		t.Errorf("export line while off:\n%s", txt)
	}
	if strings.Contains(txt, "\u2014") {
		t.Error("em dash in the check")
	}
	// the first deploy: everything is new: one message
	got, err := s.NotifyDeploy(ctx)
	if err != nil || len(sent) != 1 || !strings.Contains(got, "Сервер обновлён (версия abcdef1") || !strings.Contains(got, "❌ ИИ: не подключён: нет ни одного ключа\n    → Добавьте") {
		t.Fatalf("first deploy: %v %q", err, got)
	}
	// the same deploy again (a restart): nothing more
	if got, _ := s.NotifyDeploy(ctx); got != "" || len(sent) != 1 {
		t.Fatalf("second message in one deploy: %q", got)
	}

	// the owner adds the key; a lead comes straight from Tilda; next deploy
	keys.Set(key)
	tl := NewTildaLeads(nil, nil, meta, "")
	tl.now = func() time.Time { return now.Add(-time.Hour) }
	tl.mark(ctx, tildaLast{Via: "tilda", Test: true}) // Tilda's connection test: not a lead
	tl.mark(ctx, tildaLast{Via: "tilda", Name: "Асет"})
	tl.mark(ctx, tildaLast{Via: "tilda", Name: "Асет", Dup: true}) // a duplicate: not counted
	tl.mark(ctx, tildaLast{Via: "tilda", Name: "Марат"})
	in, _ := ReadTildaLeads(ctx, meta, now)
	if in.Today != 2 || in.Via != "tilda" || in.At.IsZero() {
		t.Fatalf("tilda info: %+v", in)
	}
	commit = "bbbbbbb000000000"
	got, err = s.NotifyDeploy(ctx)
	// R51: everything got better: no problem to act on, no message
	if err != nil || len(sent) != 1 || got != "" {
		t.Fatalf("second deploy: %v %q", err, got)
	}
	line := s.Run(ctx).Items[0]
	if line.Key != "ai" || line.State != "ok" || !strings.HasPrefix(line.Text, "отвечает Claude") ||
		!strings.Contains(line.Detail, "Claude: модель claude-sonnet-5, ключ из настроек платформы") {
		t.Fatalf("ai line: %+v", line)
	}
	if tline := s.Run(ctx).Items[2]; tline.Text != "последняя 04.10 13:35, напрямую с сайта · сегодня 2" {
		t.Fatalf("tilda line: %+v", tline)
	}
	if full := s.Run(ctx).TextFull(); !strings.Contains(full, "    · Claude: модель claude-sonnet-5") || strings.Contains(full, "/status подробно") {
		t.Fatalf("full:\n%s", full)
	}

	// a deploy where nothing changed: no message, the result is still kept
	commit = "ccccccc000000000"
	if got, _ := s.NotifyDeploy(ctx); got != "" || len(sent) != 1 {
		t.Fatalf("nothing changed, sent %q", got)
	}
	// the webhook is lost: the server takes it back itself, nothing for the owner to do
	owned = false
	commit = "ddddddd000000000"
	if got, _ = s.NotifyDeploy(ctx); got != "" || len(sent) != 1 {
		t.Fatalf("webhook: %q", got)
	}
	if !strings.Contains(s.Run(ctx).Text(), "❌ Webhook Telegram: Telegram шлёт сообщения не серверу") {
		t.Fatal("webhook not in /status")
	}
	// the database fails: one problem, one action
	owned = true
	dbErr := errors.New("down")
	s.DB = func(context.Context) error { return dbErr }
	commit = "eeeeeee000000000"
	got, _ = s.NotifyDeploy(ctx)
	if len(sent) != 2 || !strings.Contains(got, "❌ База данных: база данных не отвечает\n    → Откройте Railway") || strings.Contains(got, "ИИ") {
		t.Fatalf("db: %q", got)
	}
	// still failing next deploy: already told, not repeated
	commit = "eeeeeee000000001"
	if got, _ = s.NotifyDeploy(ctx); got != "" || len(sent) != 2 {
		t.Fatalf("repeated: %q", got)
	}
	// the bot is down: nothing kept, the next run of this deploy tries again
	keys.Set("")
	commit = "fffffff000000000"
	sendErr = errors.New("telegram down")
	if _, err := s.NotifyDeploy(ctx); err == nil {
		t.Fatal("send error not reported")
	}
	sendErr = nil
	if got, _ := s.NotifyDeploy(ctx); len(sent) != 3 || !strings.Contains(got, "❌ ИИ: не подключён") {
		t.Fatalf("retry after a failed send: %q", got)
	}
	keys.Set(key)
	s.DB = func(context.Context) error { return nil }

	// the settings' button: admin only, the same lines as JSON
	gin.SetMode(gin.TestMode)
	for _, role := range []string{"admin", "resident", "lead"} {
		rt := gin.New()
		rt.Use(func(c *gin.Context) { c.Set("role", role); c.Set("userID", "tg:1") })
		rt.POST("/system/check", s.Check)
		w := httptest.NewRecorder()
		rt.ServeHTTP(w, httptest.NewRequest("POST", "/system/check", nil))
		if role != "admin" {
			if w.Code != 403 {
				t.Fatalf("%s: %d", role, w.Code)
			}
			continue
		}
		var j struct {
			Items []CheckItem `json:"items"`
			Text  string      `json:"text"`
		}
		_ = json.Unmarshal(w.Body.Bytes(), &j)
		if w.Code != 200 || len(j.Items) < 7 || j.Items[0].Key != "ai" || j.Items[1].Key != "airecs" || j.Items[0].State != "ok" || !strings.HasPrefix(j.Text, "🩺") {
			t.Fatalf("http: %d %s", w.Code, w.Body.String())
		}
		if strings.Contains(w.Body.String(), key) || strings.Contains(w.Body.String(), "SYSCHECK") {
			t.Fatal("the key went out")
		}
	}
}

func TestR36QuietHours(t *testing.T) {
	at := func(h, m int) time.Time { return time.Date(2026, 10, 4, h, m, 0, 0, club.Almaty) }
	cases := []struct {
		now, want time.Time
	}{
		{at(14, 0), at(14, 0)},
		{at(9, 0), at(9, 0)},
		{at(21, 59), at(21, 59)},
		{at(22, 0), time.Date(2026, 10, 5, 9, 0, 0, 0, club.Almaty)},
		{at(23, 30), time.Date(2026, 10, 5, 9, 0, 0, 0, club.Almaty)},
		{at(3, 10), at(9, 0)},
		{at(8, 59), at(9, 0)},
	}
	for _, c := range cases {
		if got := QuietUntil(c.now); !got.Equal(c.want) {
			t.Errorf("%s: %s, want %s", c.now.Format("15:04"), got.In(club.Almaty).Format("02 15:04"), c.want.Format("02 15:04"))
		}
	}
	// UTC input: 17:30 UTC is 22:30 in Almaty (UTC+5)
	if got := QuietUntil(time.Date(2026, 10, 4, 17, 30, 0, 0, time.UTC)); !got.Equal(time.Date(2026, 10, 5, 9, 0, 0, 0, club.Almaty)) {
		t.Errorf("utc: %s", got)
	}
}

// After a deploy: waits, then (in quiet hours) waits for 09:00, sends once.
func TestR36AfterDeployWaitsAndSends(t *testing.T) {
	SysPersist = 0 // R66: here the AI problem is sent at once
	defer func() { SysPersist = 24 * time.Hour }()
	meta := newMemMeta()
	var mu sync.Mutex
	var sent []string
	s := &SysCheck{Meta: meta, Owner: 1, Now: time.Now,
		Build: func() buildinfo.Info { return buildinfo.Info{Commit: "1234567abc"} },
		Send: func(_ context.Context, _ int64, text string) error {
			mu.Lock()
			sent = append(sent, text)
			mu.Unlock()
			return nil
		}}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() { s.AfterDeploy(ctx, 50*time.Millisecond); close(done) }()
	mu.Lock()
	early := len(sent)
	mu.Unlock()
	if early != 0 {
		t.Fatal("sent before the delay")
	}
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		if QuietUntil(time.Now()).After(time.Now()) {
			t.Skip("quiet hours in Almaty right now: waiting for 09:00 is the expected behaviour")
		}
		t.Fatal("after deploy did not finish")
	}
	if QuietUntil(time.Now()).After(time.Now()) {
		return
	}
	mu.Lock()
	defer mu.Unlock()
	// no Claude client and no DB here: both fail, so a message goes
	if len(sent) != 1 || !strings.Contains(sent[0], "1234567") {
		t.Fatalf("sent %v", sent)
	}
	if meta.m[metaSysSent] != "1234567abc" || meta.m[metaSysLast] == "" {
		t.Fatalf("meta %v", meta.m)
	}
}

// R36c: «Заявки Tilda» says what the newest attempt was: never called, refused
// with a reason (and how many attempts in a day), or only the connection test.
func TestR36cTildaLine(t *testing.T) {
	ctx := context.Background()
	meta := newMemMeta()
	now := time.Date(2026, 10, 5, 0, 30, 0, 0, club.Almaty)
	s := &SysCheck{Meta: meta, Now: func() time.Time { return now }}
	if it := s.tilda(ctx); it.Text != "заявок ещё не было · сегодня 0 · обращений с сайта не было" || it.State != "off" {
		t.Fatalf("never: %+v", it)
	}
	tl := NewTildaLeads(nil, nil, meta, "")
	for i, m := range []int{40, 30, 10} {
		tl.now = func() time.Time { return now.Add(-time.Duration(m) * time.Minute) }
		tl.record(ctx, tildaTry{Method: "POST", Path: "/tilda/029f…5429", Result: "rejected", Reason: "неверный ключ", Fields: []string{"Name", "Phone", "tranid"}})
		_ = i
	}
	it := s.tilda(ctx)
	if it.Text != "заявок ещё не было · сегодня 0 · последняя попытка 00:20, отклонена: неверный ключ (3 попытки за сутки)" || it.State != "warn" ||
		!strings.Contains(it.Note, "неверный ключ") {
		t.Fatalf("refused: %+v", it)
	}
	// then a lead comes: the line is the lead's again
	tl.now = func() time.Time { return now.Add(-5 * time.Minute) }
	tl.record(ctx, tildaTry{Method: "POST", Result: "accepted"})
	tl.mark(ctx, tildaLast{Via: "tilda", Name: "А"})
	if it := s.tilda(ctx); it.Text != "последняя 05.10 00:25, напрямую с сайта · сегодня 1" || it.State != "ok" {
		t.Fatalf("lead: %+v", it)
	}
	if strings.Contains(s.tilda(ctx).Text+s.tilda(ctx).Note, "—") {
		t.Fatal("em dash")
	}
}
