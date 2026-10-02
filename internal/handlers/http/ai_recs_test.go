package http

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/bnursik/business_surgery_backend/internal/ai"
	pg "github.com/bnursik/business_surgery_backend/internal/repository/pg"
	"github.com/bnursik/business_surgery_backend/migrations"
	"github.com/gin-gonic/gin"
)

func testPlatformDB(t *testing.T, keys ...string) (*pg.PlatformRepo, context.Context) {
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
	t.Cleanup(db.Pool.Close)
	_ = pg.Migrate(ctx, db, migrations.FS)
	for _, k := range keys {
		_, _ = db.Pool.Exec(ctx, `DELETE FROM platform_docs WHERE key=$1`, k)
		_, _ = db.Pool.Exec(ctx, `DELETE FROM platform_doc_versions WHERE key=$1`, k)
	}
	return pg.NewPlatformRepo(db), ctx
}

func geminiAnswer(w http.ResponseWriter, text string) {
	_ = json.NewEncoder(w).Encode(map[string]any{"candidates": []any{map[string]any{"content": map[string]any{"parts": []any{map[string]any{"text": text}}}}}})
}

func TestRecCleanAndOrgan(t *testing.T) {
	if s := recClean(" Метод — простой – ясный "); s != "Метод: простой - ясный" {
		t.Fatal(s)
	}
	if libNorm("  Платёжный  календарь!") != "платежный календарь" {
		t.Fatal(libNorm("  Платёжный  календарь!"))
	}
	items := []any{map[string]any{"organ": "Аналитика"}}
	if o := nextRecOrgan(items, time.Now()); o != "Финансы" {
		t.Fatal(o)
	}
	if o := nextRecOrgan([]any{map[string]any{"organ": "Финансы"}}, time.Now()); o != "Продажи" {
		t.Fatal(o)
	}
	now := time.Date(2026, 10, 2, 6, 30, 0, 0, almaty)
	if d := untilAlmatyHour(now, 7); d != 30*time.Minute {
		t.Fatal(d)
	}
	if d := untilAlmatyHour(now.Add(time.Hour), 7); d != 23*time.Hour+30*time.Minute {
		t.Fatal(d)
	}
}

func TestDailyRec(t *testing.T) {
	repo, ctx := testPlatformDB(t, aiRecsKey, "bs_tools", "bs_diag", "bs_libver")
	tools := `[{"organ":"Финансы","icon":"◇","title":"Платёжный календарь","short":"x","why":"y","how":["a"]}]`
	for k, v := range map[string]string{"bs_tools": tools, "bs_diag": `[{"organ":"Финансы","icon":"◆","title":"Кассовые разрывы","desc":"d"}]`, "bs_libver": "3"} {
		if _, err := repo.PutDoc(ctx, "club", k, 0, v, false, "test"); err != nil {
			t.Fatal(err)
		}
	}
	var searches, checks atomic.Int32
	gm := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var b bytes.Buffer
		_, _ = b.ReadFrom(r.Body)
		body := b.String()
		switch {
		case strings.Contains(body, "google_search"):
			n := searches.Add(1)
			if !strings.Contains(body, "Платёжный календарь") || !strings.Contains(body, "Кассовые разрывы") {
				w.WriteHeader(400) // the library must be in the prompt
				return
			}
			title := "Платежный календарь" // 1st: duplicate by title
			if n == 2 {
				title = "Еженедельный денежный созвон" // 2nd: duplicate by meaning
			}
			if n >= 3 {
				title = "Тринадцатинедельный прогноз денег"
			}
			geminiAnswer(w, "```json\n"+`{"kind":"tool","title":"`+title+`","summary":"Прогноз денег на 13 недель вперёд — каждую неделю.","source":"Практика CFO","company":"","author":"","url":"https://example.org/13w","why":"Видно разрыв заранее.","steps":["Выписать платежи","Добавить поступления","Посчитать остаток","Отметить минус","Обновлять по понедельникам","Лишний шаг"],"metrics":["Дней до разрыва","Точность прогноза"],"adapt":"Учесть сроки Kaspi.","short":"Деньги на 13 недель","example":"Кофейня увидела разрыв за месяц.","time":"2 часа","check":["Прогноз собран","Обновлён"]}`+"\n```")
		case strings.Contains(body, "responseMimeType"):
			checks.Add(1)
			if strings.Contains(body, "Еженедельный денежный созвон") {
				geminiAnswer(w, `{"duplicate":true,"of":"Платёжный календарь"}`)
				return
			}
			geminiAnswer(w, `{"duplicate":false,"of":""}`)
		default:
			w.WriteHeader(400)
		}
	}))
	defer gm.Close()
	h := NewPlatformAI(repo, &ai.Client{Gemini: "k", GeminiModel: "m", GeminiBase: gm.URL, HTTP: gm.Client()})
	now := time.Date(2026, 10, 3, 7, 5, 0, 0, almaty)
	rec, err := h.dailyRec(ctx, now, false)
	if err != nil {
		t.Fatal(err)
	}
	if searches.Load() != 3 || checks.Load() != 2 {
		t.Fatalf("calls: search %d, check %d", searches.Load(), checks.Load())
	}
	if rec["title"] != "Тринадцатинедельный прогноз денег" || rec["status"] != "new" || rec["date"] != "2026-10-03" || rec["kind"] != "tool" || rec["k"] != "world" {
		t.Fatalf("rec: %v", rec)
	}
	if s := rec["summary"].(string); strings.Contains(s, "—") {
		t.Fatal("em dash kept:", s)
	}
	item := rec["item"].(map[string]any)
	if len(item["how"].([]string)) != 5 || item["icon"] != "◇" || item["organ"] != rec["organ"] || item["short"] != "Деньги на 13 недель" {
		t.Fatalf("item: %v", item)
	}
	// Once a day.
	if _, err := h.dailyRec(ctx, now.Add(5*time.Hour), false); err != errRecDone {
		t.Fatalf("second run: %v", err)
	}
	d, _ := repo.GetDoc(ctx, "club", aiRecsKey)
	var doc struct {
		Updated string           `json:"updated"`
		Items   []map[string]any `json:"items"`
	}
	if err := json.Unmarshal([]byte(d.Value), &doc); err != nil || len(doc.Items) != 1 || doc.Updated == "" {
		t.Fatalf("doc: %s", d.Value)
	}
	// Next day: the next organ, and the earlier rec is not offered again.
	searches.Store(2)
	rec2, err := h.dailyRec(ctx, now.AddDate(0, 0, 1), false)
	if err == nil {
		t.Fatalf("same title again must be a duplicate: %v", rec2)
	}

	// The team adds it to the library.
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(func(c *gin.Context) { c.Set("role", "admin") })
	r.POST("/ai/recs/:id", h.RecAction)
	id := rec["id"].(string)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest("POST", "/ai/recs/"+id, strings.NewReader(`{"action":"add"}`)))
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"added":true`) || !strings.Contains(w.Body.String(), `"key":"bs_tools"`) {
		t.Fatalf("add: %d %s", w.Code, w.Body.String())
	}
	td, _ := repo.GetDoc(ctx, "club", "bs_tools")
	if !strings.Contains(td.Value, "Тринадцатинедельный прогноз денег") || !strings.Contains(td.Value, "Платёжный календарь") {
		t.Fatalf("tools: %s", td.Value)
	}
	w = httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest("POST", "/ai/recs/"+id, strings.NewReader(`{"action":"add"}`)))
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"added":false`) {
		t.Fatalf("add twice: %s", w.Body.String())
	}
	d, _ = repo.GetDoc(ctx, "club", aiRecsKey)
	if !strings.Contains(d.Value, `"status":"added"`) || !strings.Contains(d.Value, `"addedTo":"bs_tools"`) {
		t.Fatalf("status: %s", d.Value)
	}
	w = httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest("POST", "/ai/recs/nope", strings.NewReader(`{"action":"reject"}`)))
	if w.Code != 404 {
		t.Fatalf("missing: %d", w.Code)
	}
}
