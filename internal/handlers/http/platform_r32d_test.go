package http

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/bnursik/business_surgery_backend/internal/ai"
	pg "github.com/bnursik/business_surgery_backend/internal/repository/pg"
	"github.com/gin-gonic/gin"
)

const r32dDaily = `{"error":{"code":429,"message":"You exceeded your current quota, please check your plan and billing details.","status":"RESOURCE_EXHAUSTED","details":[{"@type":"type.googleapis.com/google.rpc.QuotaFailure","violations":[{"quotaId":"GenerateRequestsPerDayPerProjectPerModel-FreeTier"}]},{"@type":"type.googleapis.com/google.rpc.RetryInfo","retryDelay":"37s"}]}}`

// The tour's voice as files: the manifest gives every phrase its immutable
// URL (the new voice where it is made, the earlier one meanwhile), the files
// are served with a year of cache, the quota state reaches the settings line,
// and a used-up quota is asked once, not for every phrase.
func TestR32dTourVoiceFilesAndQuota(t *testing.T) {
	repo, db := platformTestRepo(t)
	ctx := context.Background()
	_, _ = db.Pool.Exec(ctx, `DELETE FROM tts_audio`)
	_, _ = db.Pool.Exec(ctx, `DELETE FROM platform_docs WHERE key = $1`, ttsVoiceDoc)
	ttsForgetVoice()
	defer func() {
		_, _ = db.Pool.Exec(ctx, `DELETE FROM platform_docs WHERE key = $1`, ttsVoiceDoc)
		ttsForgetVoice()
	}()
	t.Setenv("AI_TTS_VOICE", "")
	t.Setenv("AI_GEMINI_TTS_MODEL", "gemini-tts")

	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.WriteHeader(429)
		_, _ = w.Write([]byte(r32dDaily))
	}))
	defer srv.Close()
	client := &ai.Client{Gemini: "k", GeminiBase: srv.URL, HTTP: srv.Client()}
	h := NewPlatformAI(repo, client)
	texts := []string{"Добро пожаловать.", "Трекинг: доски резидентов.", "Клуб: резиденты и встречи.", "Готово."}
	h.TourTexts = func() []string { return texts }
	// Charon made all four before; the new default voice (Iapetus) two of them.
	for _, tx := range texts {
		_ = repo.PutTTS(ctx, ttsKey("Charon", tx), "Charon", ttsStyle, tx, []byte("RIFF-charon-"+tx))
	}
	for _, tx := range texts[:2] {
		_ = repo.PutTTS(ctx, ttsKey(ttsVoiceDefault, tx), ttsVoiceDefault, ttsStyle, tx, []byte("RIFF-new-"+tx))
	}

	gin.SetMode(gin.TestMode)
	role := "admin"
	r := gin.New()
	r.Use(func(c *gin.Context) { c.Set("role", role); c.Set("userID", "tg:1") })
	r.GET("/tts/manifest", h.TTSManifest)
	r.POST("/tts/voice", h.TTSSetVoice)
	r.GET("/tts/a/:file", h.TTSFile)
	do := func(method, path, body string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		r.ServeHTTP(w, req)
		return w
	}
	type man struct {
		Voice                        string
		Total, Ready, Older, Missing int
		State, Until                 string
		Items                        map[string]string
		Voices                       []map[string]string
	}
	get := func() man {
		var m man
		w := do("GET", "/tts/manifest", "")
		if w.Code != 200 {
			t.Fatalf("manifest: %d %s", w.Code, w.Body.String())
		}
		_ = json.Unmarshal(w.Body.Bytes(), &m)
		return m
	}
	m := get()
	if m.Voice != ttsVoiceDefault || m.Total != 4 || m.Ready != 2 || m.Older != 2 || m.Missing != 2 || len(m.Items) != 4 || len(m.Voices) == 0 {
		t.Fatalf("manifest: %+v", m)
	}
	if !strings.Contains(m.Items[texts[0]], ttsKey(ttsVoiceDefault, texts[0])) || !strings.Contains(m.Items[texts[3]], ttsKey("Charon", texts[3])) {
		t.Fatalf("items: %v", m.Items)
	}
	// The file: immutable, a year of cache, 304 on a repeat.
	path := strings.TrimPrefix(m.Items[texts[0]], "/api/v1/platform")
	w := do("GET", path, "")
	if w.Code != 200 || !strings.Contains(w.Header().Get("Cache-Control"), "immutable") || w.Header().Get("Content-Type") != "audio/wav" || !strings.HasPrefix(w.Body.String(), "RIFF-new") {
		t.Fatalf("file: %d %v", w.Code, w.Header())
	}
	req := httptest.NewRequest("GET", path, nil)
	req.Header.Set("If-None-Match", w.Header().Get("ETag"))
	w2 := httptest.NewRecorder()
	r.ServeHTTP(w2, req)
	if w2.Code != 304 {
		t.Fatalf("etag: %d", w2.Code)
	}
	if w := do("GET", "/tts/a/tts_"+strings.Repeat("0", 48)+".wav", ""); w.Code != 404 {
		t.Fatalf("missing file: %d", w.Code)
	}
	if w := do("GET", "/tts/a/../../etc.wav", ""); w.Code != 404 {
		t.Fatalf("bad name: %d", w.Code)
	}

	// The missing phrases are made: Gemini says the daily quota is used up.
	// One call, the queue waits for tomorrow, the settings line says so.
	q, missing := h.PrewarmTour(ctx, texts)
	if q != 2 || missing != 2 {
		t.Fatalf("prewarm: %d %d", q, missing)
	}
	deadline := time.Now().Add(5 * time.Second)
	for client.QuotaUntil("tts").IsZero() && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	time.Sleep(100 * time.Millisecond)
	if hits.Load() != 1 {
		t.Fatalf("quota burnt %d calls", hits.Load())
	}
	if q, missing := h.PrewarmTour(ctx, texts); q != 0 || missing != 2 || hits.Load() != 1 {
		t.Fatalf("closed quota queued %d (missing %d), calls %d", q, missing, hits.Load())
	}
	if m := get(); m.State != "quota" || m.Until == "" || len(m.Items) != 4 {
		t.Fatalf("quota state: %+v", m)
	}
	// The live endpoint does not call the model either.
	r.POST("/tts", h.TTS)
	if w := do("POST", "/tts", `{"text":"Новая фраза"}`); w.Code != 503 || hits.Load() != 1 {
		t.Fatalf("live tts under quota: %d calls %d", w.Code, hits.Load())
	}

	// Voice: the team changes it, the earlier recordings keep playing.
	if w := do("POST", "/tts/voice", `{"voice":"<x>"}`); w.Code != 400 {
		t.Fatalf("bad voice: %d", w.Code)
	}
	if w := do("POST", "/tts/voice", `{"voice":"Schedar"}`); w.Code != 200 {
		t.Fatalf("voice: %d %s", w.Code, w.Body.String())
	}
	if m := get(); m.Voice != "Schedar" || m.Ready != 0 || m.Older != 4 || len(m.Items) != 4 {
		t.Fatalf("after voice change: %+v", m)
	}
	role = "resident"
	if w := do("POST", "/tts/voice", `{"voice":"Orus"}`); w.Code != 403 {
		t.Fatalf("resident sets voice: %d", w.Code)
	}
	if m := get(); len(m.Voices) != 0 || len(m.Items) != 4 {
		t.Fatalf("resident manifest: %+v", m)
	}
	// All made: «ready».
	client.SetQuotaUntil("tts", time.Time{})
	for _, tx := range texts {
		_ = repo.PutTTS(ctx, ttsKey("Schedar", tx), "Schedar", ttsStyle, tx, []byte("RIFF-s"))
	}
	if m := get(); m.State != "ready" || m.Ready != 4 || m.Missing != 0 {
		t.Fatalf("ready: %+v", m)
	}
}

// Online residents get «Саммари разбора» (PDF) instead of the recording;
// the recording and the transcript stay with the team. Calls processed
// before get their summary on first open, made once.
func TestR32dCallSummaryPDF(t *testing.T) {
	repo, db := platformTestRepo(t)
	ctx := context.Background()
	_, _ = db.Pool.Exec(ctx, `DELETE FROM platform_boards WHERE id IN ('sum-b1','sum-b2')`)
	_, _ = db.Pool.Exec(ctx, `INSERT INTO platform_residents (tg_id, name, active) VALUES (777000222, 'Мади Актобе', true), (777000333, 'Асет', true)
		ON CONFLICT (tg_id) DO UPDATE SET name=EXCLUDED.name, active=true`)
	defer db.Pool.Exec(ctx, `DELETE FROM platform_residents WHERE tg_id IN (777000222, 777000333)`) //nolint:errcheck

	var summaries atomic.Int32
	rich := `{"title":"Маржа и найм","summary":"Мади считает маржу по-новому.","participants":["Рустам (трекер)","Мади (резидент)"],` +
		`"topics":["Маржа","Найм"],"problems":["Маржа 12%"],"diagnoses":["Цена без учёта логистики"],"decisions":["Поднять цену на 8%"],` +
		`"checklist":[{"text":"Пересчитать прайс","owner":"Мади","due":"10.10"}],"numbers":[{"label":"Маржа","value":"12%"},{"label":"Оборот","value":"3 000 000 ₸"}],` +
		`"next":["Разбор 20.10"],"quotes":["Логистика съела прибыль"]}`
	gm := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b := new(bytes.Buffer)
		_, _ = b.ReadFrom(r.Body)
		answer := "Трекер: Какая маржа?\nРезидент: Двенадцать процентов."
		if strings.Contains(b.String(), "Расшифровка") {
			summaries.Add(1)
			answer = rich
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"candidates": []any{map[string]any{
			"content": map[string]any{"parts": []any{map[string]any{"text": answer}}}}}})
	}))
	defer gm.Close()
	h := NewPlatformAI(repo, &ai.Client{Gemini: "k", GeminiModel: "m", GeminiBase: gm.URL, HTTP: gm.Client()})
	h.Run = func(f func()) { f() }
	ph := NewPlatformHandler(repo)
	board := `{"id":"sum-b1","name":"Разбор Мади","info":{"res":"Мади Актобе"},"nodes":[],"links":[],"pendingCalls":[]}`
	if _, err := repo.PutBoard(ctx, "sum-b1", 0, json.RawMessage(board), "t"); err != nil {
		t.Fatal(err)
	}

	gin.SetMode(gin.TestMode)
	role, user := "admin", "tg:453800951"
	r := gin.New()
	r.Use(func(c *gin.Context) { c.Set("role", role); c.Set("userID", user) })
	r.POST("/ai/call", h.Call)
	r.GET("/ai/calls/:id/summary.pdf", h.CallSummaryPDF)
	r.POST("/ai/calls/:id/publish", h.PublishCall)
	r.GET("/files/:id", h.GetFile)
	r.GET("/sync", ph.Sync)
	r.PUT("/boards/:id", ph.PutBoard)
	do := func(method, path, ct string, body []byte) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		req := httptest.NewRequest(method, path, bytes.NewReader(body))
		if ct != "" {
			req.Header.Set("Content-Type", ct)
		}
		r.ServeHTTP(w, req)
		return w
	}
	var resp struct{ ID, File string }
	w := do("POST", "/ai/call?board=sum-b1&resident=Мади%20Актобе&date=03.10.2026", "audio/webm", []byte("OggS fake"))
	_ = json.Unmarshal(w.Body.Bytes(), &resp)
	j, _ := repo.GetAIJob(ctx, resp.ID)
	if j == nil || j.Status != "done" {
		t.Fatalf("job: %s", w.Body.String())
	}
	var meta map[string]any
	_ = json.Unmarshal(j.Result, &meta)
	if meta["summaryPdf"] == nil {
		t.Fatalf("no summary pdf kept: %s", j.Result)
	}
	isPDF := func(w *httptest.ResponseRecorder) bool {
		return w.Code == 200 && w.Header().Get("Content-Type") == "application/pdf" && bytes.HasPrefix(w.Body.Bytes(), []byte("%PDF-")) &&
			strings.Contains(w.Header().Get("Content-Disposition"), "attachment")
	}
	// The team.
	if w := do("GET", "/ai/calls/"+resp.ID+"/summary.pdf", "", nil); !isPDF(w) {
		t.Fatalf("team pdf: %d %s", w.Code, w.Body.String())
	}
	// R32e: a draft is the team's; the team publishes it.
	role, user = "resident", "tg:777000222"
	if w := do("GET", "/ai/calls/"+resp.ID+"/summary.pdf", "", nil); w.Code != 404 {
		t.Fatalf("resident opened a draft: %d", w.Code)
	}
	role, user = "admin", "tg:453800951"
	if w := do("POST", "/ai/calls/"+resp.ID+"/publish", "", nil); w.Code != 200 {
		t.Fatalf("publish: %d %s", w.Code, w.Body.String())
	}
	// The resident whose board it is: the PDF; sync without recording and transcript.
	role, user = "resident", "tg:777000222"
	if w := do("GET", "/ai/calls/"+resp.ID+"/summary.pdf", "", nil); !isPDF(w) {
		t.Fatalf("resident pdf: %d %s", w.Code, w.Body.String())
	}
	w = do("GET", "/sync?since=0", "", nil)
	var sy struct {
		Boards []pg.PlatformBoard
	}
	_ = json.Unmarshal(w.Body.Bytes(), &sy)
	var bd map[string]any
	for _, b := range sy.Boards {
		if b.ID == "sum-b1" {
			_ = json.Unmarshal(b.Data, &bd)
		}
	}
	calls, _ := bd["calls"].([]any)
	if len(calls) != 1 {
		t.Fatalf("resident board calls: %v", bd)
	}
	c0 := calls[0].(map[string]any)
	if c0["audio"] != nil || c0["transcript"] != nil || c0["file"] != nil || c0["title"] != "Маржа и найм" || c0["summaryPdf"] == nil {
		t.Fatalf("resident sees: %v", c0)
	}
	// Nor by the file id.
	if w := do("GET", "/files/"+resp.File, "", nil); w.Code != 404 {
		t.Fatalf("resident opened the recording: %d", w.Code)
	}
	// A resident's save keeps the calls (their copy has no recording).
	cur, _ := repo.GetBoard(ctx, "sum-b1")
	bd["calls"] = []any{}
	bd["note"] = "резидент отметил задачу"
	nb, _ := json.Marshal(map[string]any{"version": cur.Version, "data": bd})
	if w := do("PUT", "/boards/sum-b1", "application/json", nb); w.Code != 200 {
		t.Fatalf("resident save: %d %s", w.Code, w.Body.String())
	}
	after, _ := repo.GetBoard(ctx, "sum-b1")
	if !strings.Contains(string(after.Data), `"audio"`) || !strings.Contains(string(after.Data), "резидент отметил") {
		t.Fatalf("calls lost on a resident's save: %s", after.Data)
	}
	// Another resident: no.
	user = "tg:777000333"
	if w := do("GET", "/ai/calls/"+resp.ID+"/summary.pdf", "", nil); w.Code != 403 {
		t.Fatalf("other resident: %d", w.Code)
	}

	// Backfill: a call processed before (old summary, no PDF): on first open
	// the fuller summary is asked once, kept, and the PDF served.
	role, user = "admin", "tg:453800951"
	old := pg.AIJob{ID: newID(), Kind: "call", BoardID: "sum-b1", Resident: "Мади Актобе", Status: "done"}
	_ = repo.CreateAIJob(ctx, old, "t")
	om, _ := json.Marshal(map[string]any{"date": "01.09.2026", "resident": "Мади Актобе", "transcript": "Трекер: Маржа?\nРезидент: 12%.",
		"summary": map[string]any{"title": "Старый разбор", "summary": "Было.", "checklist": []any{map[string]any{"text": "Сделать", "due": ""}}}})
	_ = repo.UpdateAIJob(ctx, old.ID, "done", "", om)
	before := summaries.Load()
	if w := do("GET", "/ai/calls/"+old.ID+"/summary.pdf", "", nil); !isPDF(w) {
		t.Fatalf("backfill: %d %s", w.Code, w.Body.String())
	}
	if w := do("GET", "/ai/calls/"+old.ID+"/summary.pdf", "", nil); !isPDF(w) {
		t.Fatalf("backfill again: %d", w.Code)
	}
	if summaries.Load()-before != 1 {
		t.Fatalf("backfill asked %d times", summaries.Load()-before)
	}
	oj, _ := repo.GetAIJob(ctx, old.ID)
	if !strings.Contains(string(oj.Result), `"topics"`) || !strings.Contains(string(oj.Result), `"summaryPdf"`) || oj.Status != "done" {
		t.Fatalf("backfill kept: %s", oj.Result)
	}
	// The model is out of quota: the PDF is made from what the call has.
	_ = repo.CreateAIJob(ctx, pg.AIJob{ID: "q" + old.ID[1:], Kind: "call", BoardID: "sum-b1", Status: "done"}, "t")
	_ = repo.UpdateAIJob(ctx, "q"+old.ID[1:], "done", "", om)
	h.AI.SetQuotaUntil("gemini", time.Now().Add(time.Hour))
	before = summaries.Load()
	if w := do("GET", "/ai/calls/q"+old.ID[1:]+"/summary.pdf", "", nil); !isPDF(w) || summaries.Load() != before {
		t.Fatalf("quota backfill: %d calls %d", w.Code, summaries.Load()-before)
	}
}
