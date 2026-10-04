package http

import (
	"bytes"
	"context"
	"encoding/json"
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

// Записи разборов: запись не теряется, итоги приходят владельцу и резиденту
// в Telegram и сами ложатся в доску, прерванная задача продолжается.
func TestCallRecordingsDelivered(t *testing.T) {
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
	_, _ = db.Pool.Exec(ctx, `TRUNCATE platform_files, platform_ai_jobs`)
	_, _ = db.Pool.Exec(ctx, `DELETE FROM platform_boards WHERE id IN ('call-b1','call-b2')`)
	_, _ = db.Pool.Exec(ctx, `INSERT INTO platform_residents (tg_id, name, active) VALUES (777000111, 'Даулет Сериков', true)
		ON CONFLICT (tg_id) DO UPDATE SET name=EXCLUDED.name, active=true`)
	defer db.Pool.Exec(ctx, `DELETE FROM platform_residents WHERE tg_id = 777000111`) //nolint:errcheck

	var calls []string
	gm := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b := new(bytes.Buffer)
		_, _ = b.ReadFrom(r.Body)
		s, answer := b.String(), ""
		switch {
		case strings.Contains(s, "inline_data"):
			calls = append(calls, "transcribe")
			answer = "Трекер: Сколько оборот?\nРезидент: Пять миллионов, кассовые разрывы."
		case strings.Contains(s, "Расшифровка"):
			calls = append(calls, "summary")
			answer = `{"title":"Разбор Даулета","summary":"Оборот 5 млн — разрывы.","problems":["Кассовые разрывы"],"diagnoses":["Нет платёжного календаря"],` +
				`"decisions":["Ведём календарь"],"checklist":[{"text":"Собрать платёжный календарь","due":"05.10"}],"next":["Отчёт через 10 дней"]}`
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"candidates": []any{map[string]any{
			"content": map[string]any{"parts": []any{map[string]any{"text": answer}}}}}})
	}))
	defer gm.Close()

	repo := pg.NewPlatformRepo(db)
	h := NewPlatformAI(repo, &ai.Client{Gemini: "k", GeminiModel: "m", GeminiBase: gm.URL, HTTP: gm.Client()})
	h.Run = func(f func()) { f() }
	h.Owner = 453800951
	var mu sync.Mutex
	sent := map[int64][]string{}
	h.Notify = func(_ context.Context, id int64, text string) error {
		mu.Lock()
		defer mu.Unlock()
		sent[id] = append(sent[id], text)
		return nil
	}
	board := `{"id":"call-b1","name":"Разбор Даулета","info":{"res":"Даулет Сериков"},"nodes":[],"links":[],"pendingCalls":[]}`
	if _, err := repo.PutBoard(ctx, "call-b1", 0, json.RawMessage(board), "t"); err != nil {
		t.Fatal(err)
	}

	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(func(c *gin.Context) { c.Set("role", "admin"); c.Set("userID", "tg:453800951") })
	r.POST("/ai/call", h.Call)
	r.GET("/ai/jobs/:id", h.Job)
	r.POST("/ai/jobs/:id/retry", h.RetryCall)
	r.GET("/ai/calls", h.Calls)
	do := func(method, path, ct string, body []byte) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		req := httptest.NewRequest(method, path, bytes.NewReader(body))
		if ct != "" {
			req.Header.Set("Content-Type", ct)
		}
		r.ServeHTTP(w, req)
		return w
	}
	var resp struct{ ID, Status, Error, File string }

	// 1. Запись → итоги: в Telegram владельцу и резиденту, в доску сама.
	w := do("POST", "/ai/call?board=call-b1&resident=Даулет%20Сериков&date=02.10.2026", "audio/webm", []byte("OggS fake audio"))
	_ = json.Unmarshal(w.Body.Bytes(), &resp)
	if resp.ID == "" || resp.File == "" {
		t.Fatalf("call: %s", w.Body.String())
	}
	j, _ := repo.GetAIJob(ctx, resp.ID)
	if j.Status != "done" {
		t.Fatalf("job: %+v %s", j, j.Result)
	}
	own, res := strings.Join(sent[453800951], "\n"), strings.Join(sent[777000111], "\n")
	for _, want := range []string{"Итоги разбора · Даулет Сериков · 02.10.2026", "Ключевые проблемы\n• Кассовые разрывы", "Диагнозы\n• Нет платёжного календаря",
		"Решения\n• Ведём календарь", "Задачи\n• Собрать платёжный календарь (до 05.10)", "Следующие шаги\n• Отчёт через 10 дней"} {
		if !strings.Contains(own, want) {
			t.Fatalf("owner message lacks %q:\n%s", want, own)
		}
	}
	// R32e: the resident gets nothing until the team publishes the summary
	if res != "" || !strings.Contains(own, "Это черновик саммари") || strings.Contains(own, "—") {
		t.Fatalf("resident message / draft note / em dash: %q / %q", res, own)
	}
	b, _ := repo.GetBoard(ctx, "call-b1")
	var bd struct {
		Calls []struct {
			ID, Title, Audio string
			Problems, Next   []string
			Checklist        []struct{ Text, Due string }
			Sent             map[string]any
		}
	}
	_ = json.Unmarshal(b.Data, &bd)
	if len(bd.Calls) != 1 || bd.Calls[0].ID != resp.ID || bd.Calls[0].Title != "Разбор Даулета" || bd.Calls[0].Audio == "" ||
		len(bd.Calls[0].Problems) != 1 || len(bd.Calls[0].Next) != 1 || bd.Calls[0].Sent["owner"] != true || bd.Calls[0].Sent["resident"] != false {
		t.Fatalf("board calls: %s", b.Data)
	}

	// 2. Нет ключа ИИ: запись всё равно сохранена, после ключа «Обработать снова».
	key := h.AI
	h.AI = &ai.Client{HTTP: http.DefaultClient}
	w = do("POST", "/ai/call?board=call-b1&resident=Даулет%20Сериков", "audio/webm", []byte("OggS second"))
	_ = json.Unmarshal(w.Body.Bytes(), &resp)
	if resp.ID == "" || resp.File == "" || !strings.Contains(resp.Error, "GEMINI_API_KEY") {
		t.Fatalf("no key must keep the recording: %s", w.Body.String())
	}
	h.AI = key
	w = do("POST", "/ai/jobs/"+resp.ID+"/retry", "", nil)
	if j, _ = repo.GetAIJob(ctx, resp.ID); j.Status != "done" {
		t.Fatalf("retry: %s / %+v", w.Body.String(), j)
	}

	// 3. Сервер перезапустился посреди работы: задача продолжается сама.
	w = do("POST", "/ai/call?board=call-b1&resident=Даулет%20Сериков", "audio/webm", []byte("OggS third"))
	_ = json.Unmarshal(w.Body.Bytes(), &resp)
	_, _ = db.Pool.Exec(ctx, `UPDATE platform_ai_jobs SET status='running', updated_at=now() - interval '10 minutes' WHERE id=$1`, resp.ID)
	rc, cancel := context.WithCancel(ctx)
	h.Run = func(f func()) { f(); cancel() }
	h.ResumeCalls(rc, time.Millisecond, time.Hour, 3*time.Minute)
	h.Run = func(f func()) { f() }
	if j, _ = repo.GetAIJob(ctx, resp.ID); j.Status != "done" {
		t.Fatalf("resume: %+v", j)
	}
	b, _ = repo.GetBoard(ctx, "call-b1")
	_ = json.Unmarshal(b.Data, &bd)
	if len(bd.Calls) != 3 {
		t.Fatalf("three calls on the board: %d", len(bd.Calls))
	}

	// 4. Запись без задачи (например, обработка не стартовала): видна в списке, обрабатывается по file=.
	f := pg.PlatformFile{ID: newID(), Name: "Созвон Даулет 01.10.2026.webm", Mime: "audio/webm", Data: []byte("OggS old")}
	_ = repo.PutFile(ctx, f, "t")
	w = do("GET", "/ai/calls", "", nil)
	var list struct {
		Jobs  []pg.AIJob
		Files []pg.PlatformFile
	}
	_ = json.Unmarshal(w.Body.Bytes(), &list)
	if len(list.Jobs) != 3 || len(list.Files) != 1 || list.Files[0].ID != f.ID || strings.Contains(w.Body.String(), "Резидент: Пять") {
		t.Fatalf("calls list: %s", w.Body.String())
	}
	w = do("POST", "/ai/call?file="+f.ID+"&board=call-b1&resident=Даулет%20Сериков", "", nil)
	_ = json.Unmarshal(w.Body.Bytes(), &resp)
	if j, _ = repo.GetAIJob(ctx, resp.ID); j == nil || j.Status != "done" || resp.File != f.ID {
		t.Fatalf("orphan file: %s", w.Body.String())
	}
	w = do("GET", "/ai/calls", "", nil)
	_ = json.Unmarshal(w.Body.Bytes(), &list)
	if len(list.Files) != 0 {
		t.Fatalf("the recovered file is no longer an orphan: %s", w.Body.String())
	}

	// 5. Резидент не найден: владелец получает итоги, причина записана.
	sent = map[int64][]string{}
	w = do("POST", "/ai/call?board=call-b1&resident=Незнакомец", "text/plain; charset=utf-8", []byte("Трекер: привет"))
	_ = json.Unmarshal(w.Body.Bytes(), &resp)
	j, _ = repo.GetAIJob(ctx, resp.ID)
	if len(sent[453800951]) != 1 || len(sent[777000111]) != 0 || !strings.Contains(string(j.Result), "резидент не найден") {
		t.Fatalf("unknown resident: %v %s", sent, j.Result)
	}
	if CallText("", map[string]any{"summary": map[string]any{"summary": "a — b"}}) != "Итоги разбора\nРазбор \n\na - b" {
		t.Fatalf("text: %q", CallText("", map[string]any{"summary": map[string]any{"summary": "a — b"}}))
	}
}
