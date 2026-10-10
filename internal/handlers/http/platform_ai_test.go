package http

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/bnursik/business_surgery_backend/internal/ai"
	pg "github.com/bnursik/business_surgery_backend/internal/repository/pg"
	"github.com/bnursik/business_surgery_backend/migrations"
	"github.com/gin-gonic/gin"
)

// fakeGemini answers generateContent: audio parts get a transcript, text gets JSON.
func fakeGemini(t *testing.T, calls *[]string) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		s := string(b)
		answer := ""
		switch {
		case strings.Contains(s, "google_search"):
			*calls = append(*calls, "events")
			answer = `Нашёл: {"items":[{"title":"Бизнес-завтрак","date":"2099-01-10","time":"09:00","place":"Алматы","url":"https://ex.kz/a","source":"ex.kz"},{"title":"Старое","date":"2000-01-01","url":"https://ex.kz/b"},{"title":"Без ссылки","date":"2099-01-11"}]}`
		case strings.Contains(s, "inline_data"):
			*calls = append(*calls, "transcribe")
			answer = "Трекер: Сколько оборот?\nРезидент: Пять миллионов, кассовые разрывы."
		case strings.Contains(s, "Расшифровка"):
			*calls = append(*calls, "summary")
			answer = "```json\n{\"title\":\"Разбор Даулета\",\"summary\":\"Оборот 5 млн, разрывы.\",\"checklist\":[{\"text\":\"Собрать платёжный календарь\",\"due\":\"05.10\"}]}\n```"
		case strings.Contains(s, "Фраза трекера"):
			*calls = append(*calls, "command")
			answer = `{"actions":[{"op":"add_node","type":"task","title":"Позвонить бухгалтеру"}],"say":"Добавил задачу"}`
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"candidates": []any{map[string]any{
			"content": map[string]any{"parts": []any{map[string]any{"text": answer}}}}}})
	}))
}

func TestPlatformFilesAndAI(t *testing.T) {
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
	_, _ = db.Pool.Exec(ctx, `DELETE FROM platform_docs WHERE key = 'bs_events_feed'`)
	var calls []string
	gm := fakeGemini(t, &calls)
	defer gm.Close()

	repo := pg.NewPlatformRepo(db)
	cl := &ai.Client{Gemini: "k", GeminiModel: "m", GeminiBase: gm.URL, HTTP: gm.Client()}
	h := NewPlatformAI(repo, cl)
	h.Run = func(f func()) { f() } // inline

	gin.SetMode(gin.TestMode)
	role := "admin"
	r := gin.New()
	r.Use(func(c *gin.Context) { c.Set("role", role); c.Set("userID", "tg:453800951") })
	r.POST("/files", h.UploadFile)
	r.GET("/files/:id", h.GetFile)
	r.POST("/ai/command", h.Command)
	r.POST("/ai/call", h.Call)
	r.GET("/ai/jobs/:id", h.Job)
	r.GET("/ai/jobs", h.Jobs)
	do := func(method, path, ct string, body []byte, hdr map[string]string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		req := httptest.NewRequest(method, path, bytes.NewReader(body))
		if ct != "" {
			req.Header.Set("Content-Type", ct)
		}
		for k, v := range hdr {
			req.Header.Set(k, v)
		}
		r.ServeHTTP(w, req)
		return w
	}

	// A book: the team uploads, a resident reads, the name survives Cyrillic.
	pdf := append([]byte("%PDF-1.4\n"), bytes.Repeat([]byte("x"), 3<<20)...)
	w := do("POST", "/files", "application/pdf", pdf, map[string]string{"X-File-Name": "%D0%9F%D1%80%D0%B8%D0%BD%D1%86%D0%B8%D0%BF%D1%8B.pdf"})
	var up struct{ ID, Name string }
	_ = json.Unmarshal(w.Body.Bytes(), &up)
	if w.Code != 200 || up.ID == "" || up.Name != "Принципы.pdf" {
		t.Fatalf("upload: %d %s", w.Code, w.Body.String())
	}
	role = "resident"
	w = do("GET", "/files/"+up.ID, "", nil, nil)
	if w.Code != 200 || w.Body.Len() != len(pdf) || w.Header().Get("Content-Type") != "application/pdf" {
		t.Fatalf("read: %d %d %s", w.Code, w.Body.Len(), w.Header().Get("Content-Type"))
	}
	if w = do("POST", "/files", "application/pdf", pdf, nil); w.Code != 403 {
		t.Fatalf("a resident must not upload: %d", w.Code)
	}
	if w = do("POST", "/ai/call", "audio/webm", []byte("x"), nil); w.Code != 403 {
		t.Fatalf("a resident must not run the AI: %d", w.Code)
	}
	role = "admin"

	// Voice command: R79: the rules understand a plain command without the AI.
	w = do("POST", "/ai/command", "application/json", []byte(`{"text":"Джарвис, добавь задачу позвонить бухгалтеру","context":{"nodes":[]}}`), nil)
	if !strings.Contains(w.Body.String(), `"op":"add_node"`) || !strings.Contains(w.Body.String(), `"via":"rules"`) || len(calls) != 0 {
		t.Fatalf("command by rules: %s %v", w.Body.String(), calls)
	}
	// What the rules do not understand goes to the AI and becomes actions.
	w = do("POST", "/ai/command", "application/json", []byte(`{"text":"ну вот бухгалтеру бы позвонить как-нибудь","context":{"nodes":[]}}`), nil)
	if !strings.Contains(w.Body.String(), `"op":"add_node"`) || !strings.Contains(w.Body.String(), "Добавил задачу") {
		t.Fatalf("command: %s", w.Body.String())
	}

	// A call: recording -> transcript -> summary with a checklist, kept with the board.
	w = do("POST", "/ai/call?board=b1&resident=Даулет&date=01.10.2026", "audio/webm", []byte("OggS fake audio"), nil)
	var job struct{ ID string }
	_ = json.Unmarshal(w.Body.Bytes(), &job)
	if job.ID == "" {
		t.Fatalf("call: %s", w.Body.String())
	}
	w = do("GET", "/ai/jobs/"+job.ID, "", nil, nil)
	var j pg.AIJob
	_ = json.Unmarshal(w.Body.Bytes(), &j)
	var res struct {
		Transcript string
		Audio      string
		Summary    struct {
			Title     string
			Checklist []struct{ Text, Due string }
		}
	}
	_ = json.Unmarshal(j.Result, &res)
	if j.Status != "done" || !strings.Contains(res.Transcript, "Резидент: Пять миллионов") ||
		res.Summary.Title != "Разбор Даулета" || len(res.Summary.Checklist) != 1 || res.Audio != "" { // R65: аудио удалено после саммари
		t.Fatalf("job: %+v / %s", j, j.Result)
	}
	if strings.Join(calls, ",") != "command,transcribe,summary" {
		t.Fatalf("calls: %v", calls)
	}
	w = do("GET", "/ai/jobs?board=b1", "", nil, nil)
	if !strings.Contains(w.Body.String(), job.ID) {
		t.Fatalf("jobs of the board: %s", w.Body.String())
	}

	// A ready transcript (text) skips speech recognition.
	calls = nil
	w = do("POST", "/ai/call?board=b1&resident=Даулет", "text/plain; charset=utf-8", []byte("Трекер: привет"), nil)
	_ = json.Unmarshal(w.Body.Bytes(), &job)
	if strings.Join(calls, ",") != "summary" {
		t.Fatalf("text call: %v", calls)
	}

	// Events feed: only future events with a link are kept, stored as a club doc.
	r.POST("/ai/events", h.RefreshEvents)
	w = do("POST", "/ai/events", "", nil, nil)
	if !strings.Contains(w.Body.String(), `"found":1`) {
		t.Fatalf("events: %s", w.Body.String())
	}
	d, _ := repo.GetDoc(ctx, "club", "bs_events_feed")
	if d == nil || !strings.Contains(d.Value, "Бизнес-завтрак") || strings.Contains(d.Value, "Старое") {
		t.Fatalf("events doc: %+v", d)
	}
	w = do("POST", "/ai/events", "", nil, nil) // second run updates the same doc
	if !strings.Contains(w.Body.String(), `"found":1`) {
		t.Fatalf("events again: %s", w.Body.String())
	}

	// Ingest from the private file store: books attach to their card, stickers join the pack.
	{
		sum := sha256.Sum256([]byte("test-ingest-key-123456789"))
		ingestKeySHA256 = hex.EncodeToString(sum[:])
	}
	_, _ = db.Pool.Exec(ctx, `DELETE FROM platform_docs WHERE key IN ('bs_bookfiles','bs_stickerpack_srv','bs_tools')`)
	_, _ = repo.PutDoc(ctx, "club", "bs_tools", 0, `[{"title":"Книга: «Принципы»","isBook":true,"match":"далио|принцип"},{"title":"Платёжный календарь"}]`, false, "t")
	r.POST("/ingest", h.Ingest)
	ing := func(key, kind, name string, body []byte) *httptest.ResponseRecorder {
		return do("POST", "/ingest", "application/pdf", body, map[string]string{"X-Ingest-Key": key, "X-Kind": kind, "X-File-Name": url.QueryEscape(name)})
	}
	if w = ing("wrong-key-wrong-key-wrong", "book", "x.pdf", []byte("%PDF")); w.Code != 401 {
		t.Fatalf("bad key: %d", w.Code)
	}
	w = ing("test-ingest-key-123456789", "book", "Рэй Далио Принципы.pdf", []byte("%PDF-book"))
	if !strings.Contains(w.Body.String(), `"attached":"Книга: «Принципы»"`) {
		t.Fatalf("book ingest: %s", w.Body.String())
	}
	if w = ing("test-ingest-key-123456789", "book", "Рэй Далио Принципы.pdf", []byte("%PDF-book")); !strings.Contains(w.Body.String(), `"added":false`) {
		t.Fatalf("repeat ingest must be a no-op: %s", w.Body.String())
	}
	ing("test-ingest-key-123456789", "sticker", "facepalm.jpg", []byte("\xff\xd8\xff jpeg"))
	dt, _ := repo.GetDoc(ctx, "club", "bs_tools")
	ds, _ := repo.GetDoc(ctx, "club", "bs_stickerpack_srv")
	db2, _ := repo.GetDoc(ctx, "club", "bs_bookfiles")
	if !strings.Contains(dt.Value, `"fileName":"Рэй Далио Принципы.pdf"`) || !strings.Contains(ds.Value, "facepalm.jpg") || !strings.Contains(db2.Value, "Принципы") {
		t.Fatalf("docs: %s | %s | %s", dt.Value, ds.Value, db2.Value)
	}

	// No key: a clear message, nothing queued.
	h.AI = &ai.Client{HTTP: http.DefaultClient}
	w = do("POST", "/ai/call?board=b1", "audio/webm", []byte("x"), nil)
	if !strings.Contains(w.Body.String(), "ANTHROPIC_API_KEY") || !strings.Contains(w.Body.String(), "Ключ Claude") {
		t.Fatalf("no key: %s", w.Body.String())
	}
	_ = time.Now
}
