package http

import (
	"context"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/bnursik/business_surgery_backend/internal/ai"
	"github.com/bnursik/business_surgery_backend/internal/buildinfo"
	"github.com/gin-gonic/gin"
)

// R66: the free models rest (both Gemini models paused): a calm warning on
// /status, nothing for the owner to do, never «пополните баланс Claude».
func TestR66StatusFreeModelsRest(t *testing.T) {
	const key = "AQ.Ab8RN6L-free_key.0123456789abcdefghijklmnop"
	s := r51Check(t, r51Fake(t, key), key)
	s.AI.SetQuotaUntil("gemini", time.Now().Add(2*time.Hour))
	s.AI.SetQuotaUntil("gemini-lite", time.Now().Add(2*time.Hour))
	r := s.Run(context.Background())
	it := r.Items[0]
	if it.Key != "ai" || it.State != "warn" || !strings.Contains(it.Text, "бесплатные модели на паузе") || it.Action != "" {
		t.Fatalf("ai: %+v", it)
	}
	full := r.TextFull()
	for _, no := range []string{"Пополните", "пополните", "никто не отвечает", "→"} {
		if strings.Contains(r.Text(), no) {
			t.Errorf("%q in the report:\n%s", no, r.Text())
		}
	}
	if strings.Contains(full, "Пополните") {
		t.Errorf("top-up advice in the full report:\n%s", full)
	}
	if n := DeployNote(r, nil); n != "" {
		t.Fatalf("deploy message with nothing to do: %q", n)
	}
}

// R66, prod 09.10: the main model's daily limit (20 a day free) is used up,
// the light model answers: Gemini answers, the AI is not «paused».
func TestR66LiteAnswersWhileMainRests(t *testing.T) {
	const key = "AQ.Ab8RN6L-free_key.0123456789abcdefghijklmnop"
	s := r51Check(t, r51Fake(t, key), key)
	s.AI.SetQuotaUntil("gemini", time.Now().Add(5*time.Hour))
	if s.AI.Paused() {
		t.Fatal("paused although the light model answers")
	}
	it := s.Run(context.Background()).Items[0]
	if it.State != "ok" || !strings.HasPrefix(it.Text, "отвечает Gemini (бесплатно)") {
		t.Fatalf("ai: %+v", it)
	}
}

// R66: an AI problem the owner can act on reaches him after a deploy only
// when it lasts a day; before that /status shows it.
func TestR66AIProblemWaitsADay(t *testing.T) {
	s := r51Check(t, r51Fake(t, "AQ.the-right-one-000000000000000000000000000"), "AQ.Ab8RN6L-wrong-0000000000000000000000000")
	now := time.Date(2026, 10, 9, 12, 0, 0, 0, almaty)
	commit := "66666666aaaa"
	var sent []string
	s.Now = func() time.Time { return now }
	s.Build = func() buildinfo.Info { return buildinfo.Info{Commit: commit} }
	s.Meta, s.Owner = newMemMeta(), 453800951
	s.Send = func(_ context.Context, _ int64, text string) error { sent = append(sent, text); return nil }
	if got, err := s.NotifyDeploy(context.Background()); err != nil || got != "" || len(sent) != 0 {
		t.Fatalf("a fresh AI problem was sent: %v %q", err, got)
	}
	if !strings.Contains(s.Run(context.Background()).Text(), "❌ ИИ: никто не отвечает") {
		t.Fatal("not on /status")
	}
	now, commit = now.Add(25*time.Hour), "66666666bbbb"
	got, err := s.NotifyDeploy(context.Background())
	if err != nil || len(sent) != 1 || !strings.Contains(got, "ключ Gemini не принят") || strings.Contains(got, "Пополните") {
		t.Fatalf("a day later: %v %q", err, got)
	}
	now, commit = now.Add(time.Hour), "66666666cccc"
	if got, _ := s.NotifyDeploy(context.Background()); got != "" || len(sent) != 1 {
		t.Fatalf("repeated: %q", got)
	}
}

func TestR66LocalSimilarity(t *testing.T) {
	lib := []recLibItem{
		{Kind: "tool", Organ: "Продажи", Title: "Скрипт продаж", Line: "Готовые фразы для звонка и переписки"},
		{Kind: "tool", Organ: "Финансы", Title: "Платёжный календарь", Line: "Все платежи на месяц вперёд"},
		{Kind: "diag", Organ: "Маркетинг", Title: "Нет воронки продаж", Line: "Не видно, где теряются клиенты", Cure: []string{"Воронка продаж"}},
		{Kind: "diag", Organ: "Финансы", Title: "Кассовые разрывы", Line: "Деньги кончаются к 20-му", Cure: []string{"Платёжный календарь"}},
	}
	if d, sc := recLocalDup("Платежный календарь", "", lib); d != "Платёжный календарь" || sc != 1 {
		t.Fatalf("same title: %q %.2f", d, sc)
	}
	if d, _ := recLocalDup("Платёжный календарь на месяц", "все платежи вперёд", lib); d != "Платёжный календарь" {
		t.Fatalf("near copy: %q", d)
	}
	if d, _ := recLocalDup("Метод SPIN продаж", "Вопросы о ситуации, проблеме, последствиях и выгоде", lib); d != "" {
		t.Fatalf("SPIN is not a copy of a script: %q", d)
	}
	c := recCands("Скрипт продаж для звонков", "Фразы для звонка", lib)
	if len(c) == 0 || recStr(c[0], "t") != "Скрипт продаж" {
		t.Fatalf("close items: %v", c)
	}
	if c := recCands("Карта эмпатии клиента", "Что клиент думает и чувствует", lib); len(c) != 0 {
		t.Fatalf("unrelated close items: %v", c)
	}
	// the gaps: no tool cures «Нет воронки продаж», so the text asks for one
	g := recGapText("Маркетинг", lib)
	if !strings.Contains(g, "«Нет воронки продаж»") || !strings.Contains(g, "инструментов 0, диагнозов 1") || strings.Contains(g, "—") {
		t.Fatal(g)
	}
	// the day's organ: one of the emptiest, after the last one
	if o := recPickOrgan([]any{map[string]any{"organ": "Команда"}}, lib); o != "Процессы" {
		t.Fatalf("organ %q", o)
	}
	if o := recPickOrgan(nil, lib); o == "Финансы" || o == "Продажи" {
		t.Fatalf("a full organ picked: %q", o)
	}
}

// R66: «Слить с существующим» on the platform; the AI's pauses do not reach
// the bot.
func TestR66MergeAndQuiet(t *testing.T) {
	repo, ctx := testPlatformDB(t, aiRecsKey, "bs_tools", "bs_diag", "bs_libver", recsRichKey)
	tools := `[{"organ":"Продажи","icon":"◇","title":"Скрипт продаж","short":"x","how":["Выписать возражения"],"source":"BS"}]`
	for k, v := range map[string]string{"bs_tools": tools, "bs_diag": `[]`, "bs_libver": "3"} {
		if _, err := repo.PutDoc(ctx, "club", k, 0, v, false, "test"); err != nil {
			t.Fatal(err)
		}
	}
	recs := `{"items":[{"id":"rec_20261008_spin01","date":"2026-10-08","status":"ask","kind":"tool","organ":"Продажи","title":"Метод SPIN продаж","summary":"Вопросы о ситуации, проблеме, последствиях и выгоде","similar":"Скрипт продаж","source":"Нил Рекхэм","item":{"organ":"Продажи","title":"Метод SPIN продаж","how":["Выписать возражения","Задать вопросы о ситуации","Спросить о последствиях"],"source":"Нил Рекхэм"}}]}`
	if _, err := repo.PutDoc(ctx, "club", aiRecsKey, 0, recs, false, "test"); err != nil {
		t.Fatal(err)
	}
	n := 0
	h := NewPlatformAI(repo, &ai.Client{Gemini: "k"})
	h.Owner, h.Notify = 7, func(context.Context, int64, string) error { n++; return nil }
	h.quotaAlert(&ai.QuotaError{Service: "gemini", Daily: true, Until: time.Now().Add(time.Hour)})
	aiActive.Lock()
	aiActive.name = "claude"
	aiActive.Unlock()
	h.switchAlert("claude", "gemini")
	if n != 0 {
		t.Fatalf("%d AI status messages sent to the bot", n)
	}
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(func(c *gin.Context) { c.Set("role", "admin") })
	r.POST("/ai/recs/:id", h.RecAction)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest("POST", "/ai/recs/rec_20261008_spin01", strings.NewReader(`{"action":"merge"}`)))
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"merged":"Скрипт продаж"`) {
		t.Fatalf("merge: %d %s", w.Code, w.Body.String())
	}
	td, _ := repo.GetDoc(ctx, "club", "bs_tools")
	if strings.Count(td.Value, `"title":"Скрипт продаж"`) != 1 || strings.Count(td.Value, `"organ"`) != 1 ||
		!strings.Contains(td.Value, "Спросить о последствиях") || strings.Count(td.Value, "Выписать возражения") != 1 ||
		!strings.Contains(td.Value, "BS; Нил Рекхэм") || !strings.Contains(td.Value, `"rec":"rec_20261008_spin01"`) {
		t.Fatalf("tools: %s", td.Value)
	}
	d, _ := repo.GetDoc(ctx, "club", aiRecsKey)
	if !strings.Contains(d.Value, `"status":"added"`) || !strings.Contains(d.Value, `"merged":"Скрипт продаж"`) || !strings.Contains(d.Value, "Слито") {
		t.Fatalf("rec: %s", d.Value)
	}
	// a merge into a card that is not there: a clear error
	w = httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest("POST", "/ai/recs/rec_20261008_spin01", strings.NewReader(`{"action":"merge","target":"Нет такого"}`)))
	if w.Code != 409 || !strings.Contains(w.Body.String(), "не нашёл «Нет такого»") {
		t.Fatalf("missing target: %d %s", w.Code, w.Body.String())
	}
}
