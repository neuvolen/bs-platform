package http

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/bnursik/business_surgery_backend/internal/ai"
	"github.com/bnursik/business_surgery_backend/internal/bot"
	"github.com/bnursik/business_surgery_backend/internal/content"
	pg "github.com/bnursik/business_surgery_backend/internal/repository/pg"
	"github.com/gin-gonic/gin"
)

// R32e: the resident gets a structured summary PDF of an online разбор, not
// the recording; the team edits and publishes it; offline residents get
// nothing; Telegram respects the quiet hours; auto-publish is a setting.
const r32eSummary = `{"title":"Кассовые разрывы студии","participants":["Рустам (трекер)","Айдос (резидент)"],
"situation":"Айдос ведёт веб-студию. Оборот 5 000 000 ₸, каждый месяц разрывы.",
"numbers":[{"label":"Оборот","value":"5 000 000 ₸"},{"label":"Отсрочка","value":"30 дней"}],
"pointA":"Разрывы каждый месяц","pointB":"Без разрывов к декабрю",
"diagnoses":[{"title":"кассовые разрывы","why":"Платит подрядчикам раньше клиентов"},{"title":"Хаос в задачах студии","why":"Никто не видит сроки"}],
"rootCause":"Условия оплаты никто не сводил вместе — студия кредитует клиентов.",
"solutions":[{"text":"Вести платёжный календарь на 8 недель","tool":"Платёжный календарь"},{"text":"Ставить задачи в одной системе","tool":"Секретная методика Х"}],
"plan":[{"who":"Айдос","what":"Собрать платёжный календарь","due":"05.10"},{"who":"Рустам","what":"Прислать шаблон","due":"03.10"}],
"metrics":[{"name":"Дней разрыва","now":"15","target":"0"}],
"homework":["Заполнить календарь"],"nextMeeting":{"date":"14.10.2026, 15:00","agenda":["Итоги календаря"]},"quote":"Деньги есть на бумаге"}`

type r32eDoc struct {
	chat int64
	name string
	data []byte
	cap  string
}

func TestR32eCallSummaryFlow(t *testing.T) {
	repo, db := platformTestRepo(t)
	ctx := context.Background()
	_, _ = db.Pool.Exec(ctx, `DELETE FROM platform_boards WHERE id IN ('sum-e1','sum-e2')`)
	_, _ = db.Pool.Exec(ctx, `DELETE FROM platform_docs WHERE scope='server' AND key=$1`, callSumSettingsKey)
	_, _ = db.Pool.Exec(ctx, `INSERT INTO platform_residents (tg_id, name, active) VALUES (777000444, 'Айдос Онлайнов', true), (777000555, 'Ерлан Офлайнов', true)
		ON CONFLICT (tg_id) DO UPDATE SET name=EXCLUDED.name, active=true`)
	_, _ = db.Pool.Exec(ctx, `DELETE FROM club_residents WHERE name IN ('Айдос Онлайнов','Ерлан Офлайнов')`)
	_, _ = db.Pool.Exec(ctx, `INSERT INTO club_residents (name, tg_id, format) VALUES ('Айдос Онлайнов', 777000444, 'Онлайн'), ('Ерлан Офлайнов', 777000555, 'Офлайн')`)
	defer func() {
		_, _ = db.Pool.Exec(ctx, `DELETE FROM platform_residents WHERE tg_id IN (777000444, 777000555)`)
		_, _ = db.Pool.Exec(ctx, `DELETE FROM club_residents WHERE name IN ('Айдос Онлайнов','Ерлан Офлайнов')`)
		_, _ = db.Pool.Exec(ctx, `DELETE FROM platform_docs WHERE scope='server' AND key=$1`, callSumSettingsKey)
		callSumNow = time.Now
	}()

	var summaries atomic.Int32
	gm := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b := new(bytes.Buffer)
		_, _ = b.ReadFrom(r.Body)
		answer := "Трекер: Какой оборот?\nРезидент: Пять миллионов, разрывы каждый месяц."
		if strings.Contains(b.String(), "Расшифровка") {
			summaries.Add(1)
			if !strings.Contains(b.String(), "Платёжный календарь") || !strings.Contains(b.String(), "Кассовые разрывы") {
				answer = `{"title":"нет библиотеки в промпте"}`
			} else {
				answer = r32eSummary
			}
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"candidates": []any{map[string]any{
			"content": map[string]any{"parts": []any{map[string]any{"text": answer}}}}}})
	}))
	defer gm.Close()
	h := NewPlatformAI(repo, &ai.Client{Gemini: "k", GeminiModel: "m", GeminiBase: gm.URL, HTTP: gm.Client()})
	h.Run = func(f func()) { f() }
	h.Owner = 453800951
	var mu sync.Mutex
	msgs := map[int64][]string{}
	var docs []r32eDoc
	h.Notify = func(_ context.Context, id int64, text string) error {
		mu.Lock()
		defer mu.Unlock()
		msgs[id] = append(msgs[id], text)
		return nil
	}
	h.SendDoc = func(_ context.Context, id int64, name string, data []byte, caption string) error {
		mu.Lock()
		defer mu.Unlock()
		docs = append(docs, r32eDoc{id, name, data, caption})
		return nil
	}
	docsTo := func(id int64) []r32eDoc {
		mu.Lock()
		defer mu.Unlock()
		var out []r32eDoc
		for _, d := range docs {
			if d.chat == id {
				out = append(out, d)
			}
		}
		return out
	}
	ph := NewPlatformHandler(repo)
	for id, res := range map[string]string{"sum-e1": "Айдос Онлайнов", "sum-e2": "Ерлан Офлайнов"} {
		board := `{"id":"` + id + `","name":"Разбор","info":{"res":"` + res + `"},"nodes":[],"links":[],"pendingCalls":[]}`
		if _, err := repo.PutBoard(ctx, id, 0, json.RawMessage(board), "t"); err != nil {
			t.Fatal(err)
		}
	}

	gin.SetMode(gin.TestMode)
	role, user := "admin", "tg:453800951"
	r := gin.New()
	r.Use(func(c *gin.Context) { c.Set("role", role); c.Set("userID", user) })
	r.POST("/ai/call", h.Call)
	r.GET("/ai/calls/:id/summary.pdf", h.CallSummaryPDF)
	r.GET("/ai/calls/:id/summary", h.CallSummary)
	r.PUT("/ai/calls/:id/summary", h.PutCallSummary)
	r.POST("/ai/calls/:id/summary/regenerate", h.RegenCallSummary)
	r.POST("/ai/calls/:id/publish", h.PublishCall)
	r.GET("/ai/callsum/settings", h.CallSumSettings)
	r.PUT("/ai/callsum/settings", h.PutCallSumSettings)
	r.GET("/files/:id", h.GetFile)
	r.GET("/sync", ph.Sync)
	do := func(method, path, ct string, body []byte) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		req := httptest.NewRequest(method, path, bytes.NewReader(body))
		if ct != "" {
			req.Header.Set("Content-Type", ct)
		}
		r.ServeHTTP(w, req)
		return w
	}
	call := func(board, res string) string {
		var resp struct{ ID string }
		w := do("POST", "/ai/call?board="+board+"&resident="+strings.ReplaceAll(res, " ", "%20")+"&date=02.10.2026", "audio/webm", []byte("OggS fake"))
		_ = json.Unmarshal(w.Body.Bytes(), &resp)
		if j, _ := repo.GetAIJob(ctx, resp.ID); j == nil || j.Status != "done" {
			t.Fatalf("job: %s", w.Body.String())
		}
		return resp.ID
	}
	boardCalls := func(id string) []map[string]any {
		b, _ := repo.GetBoard(ctx, id)
		var bd struct{ Calls []map[string]any }
		_ = json.Unmarshal(b.Data, &bd)
		return bd.Calls
	}
	residentSync := func(tg, board string) []any {
		role, user = "resident", tg
		defer func() { role, user = "admin", "tg:453800951" }()
		w := do("GET", "/sync?since=0", "", nil)
		var sy struct{ Boards []pg.PlatformBoard }
		_ = json.Unmarshal(w.Body.Bytes(), &sy)
		for _, b := range sy.Boards {
			if b.ID == board {
				var bd map[string]any
				_ = json.Unmarshal(b.Data, &bd)
				if _, ok := bd["pendingCalls"]; ok {
					t.Fatalf("pendingCalls reached the resident: %v", bd)
				}
				c, _ := bd["calls"].([]any)
				return c
			}
		}
		t.Fatalf("resident %s has no board %s: %s", tg, board, w.Body.String())
		return nil
	}
	isPDF := func(w *httptest.ResponseRecorder) bool {
		return w.Code == 200 && bytes.HasPrefix(w.Body.Bytes(), []byte("%PDF-"))
	}

	// 1. Processed: a draft with the eight sections, the library matched by exact title.
	id := call("sum-e1", "Айдос Онлайнов")
	j, meta := h.jobMeta(ctx, id)
	if callSumStatus(meta) != "draft" || meta["summaryPdf"] == nil {
		t.Fatalf("draft: %s", j.Result)
	}
	cs := boardCalls("sum-e1")
	if len(cs) != 1 || cs[0]["sumStatus"] != "draft" {
		t.Fatalf("board card: %v", cs)
	}
	c0 := cs[0]
	dl := csMaps(c0["diagList"], "title")
	if len(dl) != 2 || dl[0]["title"] != "Кассовые разрывы" || dl[0]["lib"] != true || dl[1]["lib"] != false {
		t.Fatalf("diagnoses: %v", c0["diagList"])
	}
	sl := csMaps(c0["solutions"], "text")
	if len(sl) != 2 || sl[0]["tool"] != "Платёжный календарь" || !strings.Contains(csS(sl[0]["toolUrl"]), "/t/") ||
		sl[1]["tool"] != "" || !strings.Contains(csS(sl[1]["text"]), "Секретная методика Х") {
		t.Fatalf("solutions: %v", c0["solutions"])
	}
	if strings.Contains(csS(c0["rootCause"]), "—") || len(csMaps(c0["plan"], "what")) != 2 || len(csMaps(c0["checklist"], "text")) != 2 {
		t.Fatalf("root/plan: %v", c0)
	}
	// The owner: the summary text with the draft note and the draft PDF; the resident: nothing.
	if own := strings.Join(msgs[453800951], "\n"); !strings.Contains(own, "Это черновик саммари") {
		t.Fatalf("owner: %q", own)
	}
	if len(docsTo(453800951)) != 1 || len(docsTo(777000444)) != 0 || len(msgs[777000444]) != 0 {
		t.Fatalf("docs: owner %d resident %d", len(docsTo(453800951)), len(docsTo(777000444)))
	}
	// 2. The resident sees no draft: not in sync, not by the PDF, not by the file.
	if c := residentSync("tg:777000444", "sum-e1"); len(c) != 0 {
		t.Fatalf("draft reached the resident: %v", c)
	}
	role, user = "resident", "tg:777000444"
	if w := do("GET", "/ai/calls/"+id+"/summary.pdf", "", nil); w.Code != 404 {
		t.Fatalf("resident draft pdf: %d", w.Code)
	}
	if w := do("GET", "/files/"+csS(meta["summaryPdf"]), "", nil); w.Code != 404 {
		t.Fatalf("resident draft file: %d", w.Code)
	}
	if w := do("GET", "/ai/calls/"+id+"/summary", "", nil); w.Code != 403 {
		t.Fatalf("resident editor: %d", w.Code)
	}
	role, user = "admin", "tg:453800951"

	// 3. The team: the editor, an edit, the model asked again.
	w := do("GET", "/ai/calls/"+id+"/summary", "", nil)
	var view struct {
		Status  string
		Offline bool
		Summary map[string]any
		Lib     struct{ Diag, Tools []string }
	}
	_ = json.Unmarshal(w.Body.Bytes(), &view)
	if view.Status != "draft" || view.Offline || len(view.Lib.Diag) < 50 || len(view.Lib.Tools) < 50 || csS(view.Summary["rootCause"]) == "" {
		t.Fatalf("editor: %s", w.Body.String()[:300])
	}
	pdf0 := csS(meta["summaryPdf"])
	edit := map[string]any{"summary": map[string]any{"rootCause": "Нет правила: сначала деньги, потом подрядчик",
		"plan":     []any{map[string]any{"who": "Айдос", "what": "Перейти на предоплату 30%", "due": "09.10"}},
		"diagList": []any{map[string]any{"title": "Нет управленческого учёта", "why": "Решения по остатку на счёте"}}}}
	eb, _ := json.Marshal(edit)
	if w := do("PUT", "/ai/calls/"+id+"/summary", "application/json", eb); w.Code != 200 {
		t.Fatalf("edit: %d %s", w.Code, w.Body.String())
	}
	_, meta = h.jobMeta(ctx, id)
	st := callSumStateOf(meta)
	if st["editedAt"] == nil || callSumStatus(meta) != "draft" || csS(meta["summaryPdf"]) == pdf0 {
		t.Fatalf("edit kept: %v %v", st, meta["summaryPdf"])
	}
	c0 = boardCalls("sum-e1")[0]
	if c0["rootCause"] != "Нет правила: сначала деньги, потом подрядчик" || len(csMaps(c0["checklist"], "text")) != 1 ||
		csMaps(c0["diagList"], "title")[0]["lib"] != true {
		t.Fatalf("edit on the board: %v", c0)
	}
	before := summaries.Load()
	if w := do("POST", "/ai/calls/"+id+"/summary/regenerate", "", nil); w.Code != 200 || summaries.Load() != before+1 {
		t.Fatalf("regenerate: %d %s", w.Code, w.Body.String())
	}
	_, meta = h.jobMeta(ctx, id)
	if callSumStateOf(meta)["editedAt"] != nil || !strings.HasPrefix(csS(csM(meta["summary"])["rootCause"]), "Условия оплаты") {
		t.Fatalf("regenerated: %v", meta["sumState"])
	}

	// 4. Publish at 23:00 Almaty: on the platform now, in Telegram at 09:00.
	callSumNow = func() time.Time { return time.Date(2026, 10, 4, 23, 0, 0, 0, csAlmaty) }
	w = do("POST", "/ai/calls/"+id+"/publish", "", nil)
	var pub struct {
		Tg map[string]any
	}
	_ = json.Unmarshal(w.Body.Bytes(), &pub)
	if w.Code != 200 || pub.Tg["pending"] != true || len(docsTo(777000444)) != 0 {
		t.Fatalf("publish at night: %d %s", w.Code, w.Body.String())
	}
	if c := residentSync("tg:777000444", "sum-e1"); len(c) != 1 {
		t.Fatalf("published not shown: %v", c)
	} else if m := c[0].(map[string]any); m["audio"] != nil || m["transcript"] != nil || m["file"] != nil || m["sumStatus"] != "published" {
		t.Fatalf("resident card: %v", m)
	}
	role, user = "resident", "tg:777000444"
	if w := do("GET", "/ai/calls/"+id+"/summary.pdf", "", nil); !isPDF(w) {
		t.Fatalf("resident pdf: %d", w.Code)
	}
	role, user = "admin", "tg:453800951"
	h.CallSumTick(ctx, time.Date(2026, 10, 5, 3, 0, 0, 0, csAlmaty)) // still night
	if len(docsTo(777000444)) != 0 {
		t.Fatal("sent in the quiet hours")
	}
	h.CallSumTick(ctx, time.Date(2026, 10, 5, 9, 5, 0, 0, csAlmaty))
	rd := docsTo(777000444)
	if len(rd) != 1 || !bytes.HasPrefix(rd[0].data, []byte("%PDF-")) || !strings.HasPrefix(rd[0].name, "Саммари разбора Айдос Онлайнов") ||
		!strings.Contains(rd[0].cap, "план на 10 дней") || strings.Contains(rd[0].cap, "—") {
		t.Fatalf("morning send: %+v", rd)
	}
	_, meta = h.jobMeta(ctx, id)
	if tg := csM(callSumStateOf(meta)["tg"]); tg["ok"] != true || tg["pending"] != false {
		t.Fatalf("tg state: %v", tg)
	}
	h.CallSumTick(ctx, time.Date(2026, 10, 5, 10, 0, 0, 0, csAlmaty))
	if len(docsTo(777000444)) != 1 {
		t.Fatal("sent twice")
	}

	// 5. Offline: no publish, nothing in sync, no PDF, even when forced.
	off := call("sum-e2", "Ерлан Офлайнов")
	if own := strings.Join(msgs[453800951], "\n"); !strings.Contains(own, "офлайн-формате") {
		t.Fatalf("owner offline note: %q", own)
	}
	if w := do("POST", "/ai/calls/"+off+"/publish", "", nil); w.Code != 409 {
		t.Fatalf("offline publish: %d", w.Code)
	}
	_, om := h.jobMeta(ctx, off)
	om["sumState"] = map[string]any{"status": "published"}
	ob, _ := json.Marshal(om)
	_ = repo.UpdateAIJob(ctx, off, "done", "", ob)
	if c := residentSync("tg:777000555", "sum-e2"); len(c) != 0 {
		t.Fatalf("offline resident sees calls: %v", c)
	}
	role, user = "resident", "tg:777000555"
	if w := do("GET", "/ai/calls/"+off+"/summary.pdf", "", nil); w.Code != 403 {
		t.Fatalf("offline pdf: %d", w.Code)
	}
	role, user = "admin", "tg:453800951"

	// 6. Auto-publish: off by default; on, an untouched draft goes after N hours, an edited one waits.
	if w := do("GET", "/ai/callsum/settings", "", nil); !strings.Contains(w.Body.String(), `"autoPublish":false`) {
		t.Fatalf("settings default: %s", w.Body.String())
	}
	a1, a2 := call("sum-e1", "Айдос Онлайнов"), call("sum-e1", "Айдос Онлайнов")
	noon := time.Date(2026, 10, 5, 12, 0, 0, 0, csAlmaty)
	old := noon.Add(-25 * time.Hour).UTC().Format(time.RFC3339)
	for _, x := range []string{a1, a2} {
		_, m := h.jobMeta(ctx, x)
		callSumStateOf(m)["createdAt"] = old
		if x == a2 {
			callSumStateOf(m)["editedAt"] = old
		}
		b, _ := json.Marshal(m)
		_ = repo.UpdateAIJob(ctx, x, "done", "", b)
	}
	h.CallSumTick(ctx, noon)
	if _, m := h.jobMeta(ctx, a1); callSumStatus(m) != "draft" {
		t.Fatal("published without the setting")
	}
	if w := do("PUT", "/ai/callsum/settings", "application/json", []byte(`{"autoPublish":true,"hours":24}`)); w.Code != 200 {
		t.Fatalf("settings: %d", w.Code)
	}
	h.CallSumTick(ctx, noon)
	_, m1 := h.jobMeta(ctx, a1)
	_, m2 := h.jobMeta(ctx, a2)
	if callSumStatus(m1) != "published" || callSumStateOf(m1)["auto"] != true || callSumStatus(m2) != "draft" {
		t.Fatalf("auto-publish: %v / %v", m1["sumState"], m2["sumState"])
	}
	if len(docsTo(777000444)) != 2 {
		t.Fatalf("auto-published PDF not sent: %d", len(docsTo(777000444)))
	}
}

// An R32d summary gets the R32e sections from its own fields.
func TestR32eCallSumNormLegacy(t *testing.T) {
	sum := callSumNorm(map[string]any{"summary": "Было — стало.", "diagnoses": []any{"Кассовые разрывы", "Свой диагноз"},
		"decisions": []any{"Ведём календарь"}, "checklist": []any{map[string]any{"text": "Собрать календарь", "due": "05.10", "owner": "Даулет"}},
		"next": []any{"Отчёт через 10 дней"}})
	if sum["situation"] != "Было - стало." || len(sum["diagList"].([]any)) != 2 || sum["diagList"].([]any)[0].(map[string]any)["lib"] != true {
		t.Fatalf("norm: %v", sum)
	}
	p := sum["plan"].([]any)[0].(map[string]any)
	if p["who"] != "Даулет" || p["what"] != "Собрать календарь" || csM(sum["nextMeeting"])["agenda"].([]any)[0] != "Отчёт через 10 дней" {
		t.Fatalf("plan/next: %v", sum)
	}
	// idempotent
	again := callSumNorm(callJSON(sum))
	a, _ := json.Marshal(again)
	b, _ := json.Marshal(callJSON(sum))
	if string(a) != string(b) {
		t.Fatalf("not idempotent:\n%s\n%s", a, b)
	}
	if callSumInHours(time.Date(2026, 1, 1, 8, 59, 0, 0, csAlmaty)) || !callSumInHours(time.Date(2026, 1, 1, 9, 0, 0, 0, csAlmaty)) ||
		callSumInHours(time.Date(2026, 1, 1, 21, 0, 0, 0, csAlmaty)) {
		t.Fatal("quiet hours")
	}
}

// Маркетинг a2: the bot asks what hurts and gives 3 checklists of that organ.
func TestR32eLeadPain(t *testing.T) {
	repo, ctx := testPlatformDB(t, "bs_crm")
	type msg struct {
		chat int64
		text string
		kb   map[string]any
	}
	var got []msg
	f := NewLeadFunnel(repo, func(_ context.Context, id int64, text string, kb map[string]any) error {
		got = append(got, msg{id, text, kb})
		return nil
	}, nil)
	if err := f.sendPainAsk(ctx, 5550001); err != nil || len(got) != 1 || !strings.Contains(got[0].text, "болит") {
		t.Fatalf("ask: %v %+v", err, got)
	}
	kb, _ := json.Marshal(got[0].kb)
	for _, k := range []string{"lm_pain_ops", "lm_pain_team", "lm_pain_fin", "lm_pain_sales"} {
		if !strings.Contains(string(kb), k) {
			t.Fatalf("button %s: %s", k, kb)
		}
	}
	got = nil
	cb := bot.CallbackUpdate{ChatID: 5550001, FromID: 5550001, FirstName: "Тест", Data: "lm_pain_fin"}
	if !f.HandleCallback(ctx, cb) || len(got) != 1 {
		t.Fatalf("pick: %+v", got)
	}
	if !strings.Contains(got[0].text, "Финансы: начните") || !strings.Contains(got[0].text, "Платёжный календарь за один вечер") ||
		!strings.Contains(got[0].text, "Кейс: ") || strings.Contains(got[0].text, "—") {
		t.Fatalf("checklists: %q", got[0].text)
	}
	d, _ := repo.GetDoc(ctx, "club", "bs_crm")
	if d == nil || !strings.Contains(d.Value, `"pain":"Финансы"`) || !strings.Contains(d.Value, "Болит: Финансы") {
		t.Fatalf("crm: %v", d)
	}
}

// Маркетинг: notes over the platform's own only, deadlines on the owner's tasks.
func TestR32eMktMerge(t *testing.T) {
	repo, ctx := testPlatformDB(t, mktActionsKey, mktActionsR32eKey, "bs_mkt_analysis", "bs_kanban", "bs_scripts")
	var doc map[string]any
	_ = json.Unmarshal(content.Marketing, &doc)
	v, _ := json.Marshal(doc)
	for k, val := range map[string]string{
		"bs_mkt_analysis": string(v),
		"bs_kanban":       `{"cards":[{"id":"k1","col":"idea","t":"Своя"}],"goals":[]}`,
		"bs_scripts":      `[]`,
	} {
		if _, err := repo.PutDoc(ctx, "club", k, 0, val, false, "team"); err != nil {
			t.Fatal(err)
		}
	}
	h := NewPlatformAI(repo, &ai.Client{})
	h.MigrateMktActions(ctx)
	// the team rewrote a2's note itself
	d, _ := repo.GetDoc(ctx, "club", "bs_mkt_analysis")
	_ = json.Unmarshal([]byte(d.Value), &doc)
	for _, x := range doc["actions"].([]any) {
		if a := x.(map[string]any); a["id"] == "a2" {
			a["note"] = "Своя заметка команды"
		}
	}
	v, _ = json.Marshal(doc)
	_, _ = repo.PutDoc(ctx, "club", "bs_mkt_analysis", d.Version, string(v), false, "team")
	h.MigrateMktR32e(ctx)
	d, _ = repo.GetDoc(ctx, "club", "bs_mkt_analysis")
	_ = json.Unmarshal([]byte(d.Value), &doc)
	acts := map[string]map[string]any{}
	for _, x := range doc["actions"].([]any) {
		a := x.(map[string]any)
		acts[csS(a["id"])] = a
	}
	if !strings.Contains(csS(acts["a1"]["note"]), "5 рубрикам") || acts["a1"]["status"] != "done" {
		t.Errorf("a1: %v", acts["a1"])
	}
	if acts["a2"]["note"] != "Своя заметка команды" {
		t.Errorf("a2 team note overwritten: %v", acts["a2"]["note"])
	}
	for _, id := range []string{"a4", "a5"} {
		if acts[id]["status"] != "work" || !strings.Contains(csS(acts[id]["note"]), "предложенный срок") {
			t.Errorf("%s: %v", id, acts[id])
		}
	}
	for _, id := range []string{"a7", "a8", "a9", "a10", "a12"} { // средний приоритет не тронут
		if acts[id]["status"] != nil {
			t.Errorf("%s: %v", id, acts[id])
		}
	}
	k, _ := repo.GetDoc(ctx, "club", "bs_kanban")
	var kb struct{ Cards []map[string]any }
	_ = json.Unmarshal([]byte(k.Value), &kb)
	n := 0
	for _, c := range kb.Cards {
		if strings.HasPrefix(csS(c["src"]), "mkt:a") {
			n++
			if _, err := time.Parse("2006-01-02", csS(c["due"])); err != nil || c["who"] != "Рустам" || len(csList(c["chk"])) < 4 {
				t.Errorf("owner task: %v", c)
			}
		}
	}
	if n != 2 {
		t.Fatalf("owner tasks: %d", n)
	}
	// once
	before := k.Version
	h.MigrateMktR32e(ctx)
	if k2, _ := repo.GetDoc(ctx, "club", "bs_kanban"); k2.Version != before {
		t.Fatal("ran twice")
	}
}
