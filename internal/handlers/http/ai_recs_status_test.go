package http

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/bnursik/business_surgery_backend/internal/ai"
)

func TestR39RecsNext(t *testing.T) {
	now := time.Date(2026, 10, 5, 15, 26, 0, 0, almaty)
	if n := recsNext(now, true, 1); !n.Equal(now.Add(time.Hour)) {
		t.Fatal(n)
	}
	if n := recsNext(now, true, aiRecsTries); n.In(almaty).Format("02.01 15:04") != "06.10 07:00" {
		t.Fatal(n)
	}
	late := time.Date(2026, 10, 5, 20, 30, 0, 0, almaty)
	if n := recsNext(late, true, 1); n.In(almaty).Format("02.01 15:04") != "06.10 07:00" {
		t.Fatal("no retry after 21:00:", n)
	}
	if n := recsNext(now, false, 0); n.In(almaty).Format("02.01 15:04") != "06.10 07:00" {
		t.Fatal(n)
	}
}

// Prod 05.10.2026: the search failed and the day's run was lost until the
// next morning. Now: the error is kept (platform, /status), the run is tried
// again within the day, and a later success clears it.
func TestR39RecsTickRetryAndStatus(t *testing.T) {
	repo, ctx := testPlatformDB(t, aiRecsKey, "bs_tools", "bs_diag", "bs_lib_rich")
	if _, err := repo.PutDoc(ctx, "club", "bs_tools", 0, `[{"organ":"Финансы","title":"Платёжный календарь","short":"x","why":"y","how":["a"]}]`, false, "test"); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.PutDoc(ctx, "club", "bs_diag", 0, `[]`, false, "test"); err != nil {
		t.Fatal(err)
	}
	var broken atomic.Bool
	broken.Store(true)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		body := string(raw)
		text := `{"duplicate":false,"of":""}`
		if strings.Contains(body, `"web_search`) {
			if broken.Load() {
				w.WriteHeader(400)
				_, _ = w.Write([]byte(`{"type":"error","error":{"type":"invalid_request_error","message":"Web search is not enabled for this organization."}}`))
				return
			}
			text = `{"kind":"tool","title":"Тринадцатинедельный прогноз денег","summary":"Прогноз денег на 13 недель.","source":"Практика CFO","url":"https://example.org/13w","why":"Видно разрыв заранее.","steps":["Выписать платежи","Добавить поступления","Посчитать остаток","Отметить минус","Обновлять"],"metrics":["Дней до разрыва"],"adapt":"Учесть Kaspi.","short":"Деньги на 13 недель","example":"Кофейня.","time":"2 часа","check":["Собран"]}`
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"stop_reason": "end_turn", "content": []any{map[string]any{"type": "text", "text": text}}})
	}))
	defer srv.Close()
	h := NewPlatformAI(repo, &ai.Client{Anthropic: "k", AnthropicBase: srv.URL, ClaudeModel: "claude-sonnet-5", HTTP: srv.Client()})
	now := time.Date(2026, 10, 5, 15, 26, 0, 0, almaty)

	next := h.recsTick(ctx, now)
	if !next.Equal(now.Add(time.Hour)) {
		t.Fatalf("retry at %s", next)
	}
	it := h.RecsStatus(ctx, now.Add(time.Minute))
	if it.State != "fail" || !strings.Contains(it.Text, "Capabilities") || !strings.Contains(it.Text, "следующая попытка 05.10 16:26") || !strings.Contains(it.Text, "ещё не было") {
		t.Fatalf("status: %+v", it)
	}
	d, _ := repo.GetDoc(ctx, "club", aiRecsKey)
	if !strings.Contains(d.Value, `"run":{`) || !strings.Contains(d.Value, `"tries":1`) {
		t.Fatalf("doc: %s", d.Value)
	}

	broken.Store(false)
	next = h.recsTick(ctx, now.Add(time.Hour))
	if next.In(almaty).Format("02.01 15:04") != "06.10 07:00" {
		t.Fatalf("next %s", next)
	}
	it = h.RecsStatus(ctx, now.Add(time.Hour+time.Minute))
	if it.State != "ok" || !strings.HasPrefix(it.Text, "последняя 05.10, добавлено ") {
		t.Fatalf("status after: %+v", it)
	}
	d, _ = repo.GetDoc(ctx, "club", aiRecsKey)
	var doc struct {
		Run   map[string]any   `json:"run"`
		Items []map[string]any `json:"items"`
	}
	_ = json.Unmarshal([]byte(d.Value), &doc)
	if doc.Run["ok"] != true || len(doc.Items) != 1 || doc.Items[0]["status"] == "new" {
		t.Fatalf("run %+v items %d status %v", doc.Run, len(doc.Items), doc.Items[0]["status"])
	}
	// added or put to the owner: both are counted on the line
	if st := doc.Items[0]["status"]; st == "added" && !strings.Contains(it.Text, "добавлено 1 за неделю") || st == "ask" && !strings.Contains(it.Text, "ждёт решения 1") {
		t.Fatalf("%v: %s", st, it.Text)
	}
	// the day's one is there: the next tick does nothing until tomorrow
	if n := h.recsTick(ctx, now.Add(2*time.Hour)); n.In(almaty).Format("02.01 15:04") != "06.10 07:00" {
		t.Fatal(n)
	}
	_ = context.Background
}
