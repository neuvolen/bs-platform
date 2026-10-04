package http

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/bnursik/business_surgery_backend/internal/ai"
	"github.com/bnursik/business_surgery_backend/internal/content"
	pg "github.com/bnursik/business_surgery_backend/internal/repository/pg"
	"github.com/gin-gonic/gin"
)

// R32e: «Режим резидента: записи разборов убрать для офлайн, а для онлайн не
// запись разбора, а саммари разбора, PDF-файл на основе созвона».
//
// After an online разбор is recorded the server transcribes it (runCallJob)
// and asks the model for the structured summary (callSumSystem: ситуация и
// цифры, диагнозы, корневая причина, решения и инструменты, план на 10 дней,
// метрики, домашнее задание, следующая встреча). Diagnoses and tools are
// matched to the BS library by exact title only (callSumNorm). The summary
// is a DRAFT: the team reads it, edits the text or asks the model again, and
// publishes it. Publishing renders the PDF without the draft mark, shows it to
// the resident («Саммари разбора (PDF)» with the date) and sends it to them in
// Telegram, 09:00-21:00 Almaty (later ones wait in CallSumLoop). A setting
// publishes an unedited draft by itself after N hours (off by default).
//
// The resident never gets the recording or the transcript; an offline
// resident (format «Офлайн») gets no calls at all (gateResidentCalls,
// CallSummaryPDF). Calls processed before R32e have no state: they were
// already sent to the resident, so they count as published.
//
//   GET  /ai/calls/:id/summary             the draft for the editor (team)
//   PUT  /ai/calls/:id/summary             save the team's edit, new PDF
//   POST /ai/calls/:id/summary/regenerate  ask the model again from the transcript
//   POST /ai/calls/:id/publish             to the resident (+ Telegram)
//   GET  /ai/callsum/settings, PUT ...     {autoPublish, hours}

const (
	callSumSettingsKey = "callsum_settings" // scope server
	callSumSendFrom    = 9                  // Telegram to residents 09:00-21:00 Almaty
	callSumSendUntil   = 21
	callSumAutoHours   = 24
)

var csAlmaty = time.FixedZone("Almaty", 5*3600)

// callSumNow: the clock of the publish handler (tests set it).
var callSumNow = time.Now

const callSumSystem = `Ты помощник трекеров Business Surgery (Рустам и Береке). По расшифровке онлайн-разбора бизнеса резидента
собери саммари встречи: его получит резидент одним PDF. Отвечай ТОЛЬКО JSON:
{"title":"короткое название встречи","participants":["кто был: имя и роль (трекер, резидент, партнёр)"],
"situation":"ситуация 3-6 предложений: что за бизнес, с чем пришёл, что выяснили",
"numbers":[{"label":"что за цифра","value":"цифра с единицей: 5 000 000 ₸, 30 дней, 15%"}],
"pointA":"где резидент сейчас","pointB":"куда идёт",
"diagnoses":[{"title":"название диагноза","why":"как это проявилось у резидента, одно предложение"}],
"rootCause":"одна корневая причина под диагнозами, 1-3 предложения",
"solutions":[{"text":"о чём договорились","tool":"инструмент из библиотеки BS или пусто"}],
"plan":[{"who":"кто делает: имя резидента, трекер или сотрудник","what":"что сделать, с глагола","due":"ДД.ММ"}],
"metrics":[{"name":"метрика контроля","now":"значение сейчас","target":"цель"}],
"homework":["что резидент делает к следующей встрече"],
"nextMeeting":{"date":"ДД.ММ.ГГГГ, ЧЧ:ММ или пусто, если не договорились","agenda":["что обсудим"]},
"quote":"одна сильная фраза резидента или трекера"}
Правила. Диагноз и инструмент называй ТОЧНО как в списках библиотеки BS ниже, только если по смыслу это он; нет уверенного
совпадения: своё короткое название диагноза, а tool пустой. План на 10 дней: 3-8 пунктов, сроки в пределах 10 дней от даты
разбора. Метрики: 2-5, по ним видно, что план работает. Цифры только те, что прозвучали, тысячи через пробел: 10 000.
По-русски, коротко и конкретно, без воды и без длинного тире.`

// ── BS library (exact titles) ──

type csRef struct{ ID, Title string }

type csLibIdx struct {
	etag         string
	diag, tools  map[string]csRef
	diagT, toolT []string
}

var (
	csLibMu sync.Mutex
	csLibC  *csLibIdx
)

// csNorm: the same title written a little differently is the same title.
func csNorm(s string) string {
	s = strings.ToLower(strings.ReplaceAll(strings.ReplaceAll(s, "ё", "е"), "Ё", "е"))
	s = strings.Trim(strings.TrimSpace(s), "«»\"'“”„.,:;!?() ")
	return strings.Join(strings.Fields(s), " ")
}

func callLib() *csLibIdx {
	plain, _, etag, err := content.LibRich()
	csLibMu.Lock()
	defer csLibMu.Unlock()
	if csLibC != nil && csLibC.etag == etag {
		return csLibC
	}
	x := &csLibIdx{etag: etag, diag: map[string]csRef{}, tools: map[string]csRef{}}
	if err == nil {
		var all struct {
			Tools []csRef `json:"tools"`
			Diag  []csRef `json:"diag"`
		}
		var raw struct {
			Tools []map[string]any `json:"tools"`
			Diag  []map[string]any `json:"diag"`
		}
		_ = json.Unmarshal(plain, &raw)
		for _, t := range raw.Tools {
			all.Tools = append(all.Tools, csRef{ID: csS(t["id"]), Title: csS(t["title"])})
		}
		for _, t := range raw.Diag {
			all.Diag = append(all.Diag, csRef{ID: csS(t["id"]), Title: csS(t["title"])})
		}
		for _, d := range all.Diag {
			if k := csNorm(d.Title); k != "" {
				if _, dup := x.diag[k]; !dup {
					x.diag[k] = d
					x.diagT = append(x.diagT, d.Title)
				}
			}
		}
		for _, t := range all.Tools {
			if k := csNorm(t.Title); k != "" {
				if _, dup := x.tools[k]; !dup {
					x.tools[k] = t
					x.toolT = append(x.toolT, t.Title)
				}
			}
		}
	}
	csLibC = x
	return x
}

// callSumPrompt: the system prompt with the library's exact titles.
func callSumPrompt() string {
	lib := callLib()
	return callSumSystem + "\n\nДиагнозы библиотеки BS (точные названия): " + strings.Join(lib.diagT, "; ") +
		"\n\nИнструменты библиотеки BS (точные названия): " + strings.Join(lib.toolT, "; ")
}

// toolURL: the open link to a library tool's branded template (library_rich.go /t/).
func toolURL(id string) string {
	base := publicBase()
	if base == "" {
		base = "https://app.bxclub.kz"
	}
	return base + "/t/" + id + ".pdf"
}

// ── small JSON helpers ──

func csS(v any) string {
	switch x := v.(type) {
	case string:
		return strings.TrimSpace(x)
	case float64:
		return strings.TrimSpace(fmt.Sprint(x))
	case nil:
		return ""
	}
	return strings.TrimSpace(fmt.Sprint(v))
}

func csM(v any) map[string]any { m, _ := v.(map[string]any); return m }

// csMaps: a list of objects (a string item becomes {key: it}).
func csMaps(v any, key string) []map[string]any {
	out := []map[string]any{}
	switch x := v.(type) {
	case []any:
		for _, e := range x {
			switch y := e.(type) {
			case map[string]any:
				out = append(out, y)
			case string:
				if strings.TrimSpace(y) != "" {
					out = append(out, map[string]any{key: strings.TrimSpace(y)})
				}
			}
		}
	case []map[string]any:
		out = append(out, x...)
	case string:
		if strings.TrimSpace(x) != "" {
			out = append(out, map[string]any{key: strings.TrimSpace(x)})
		}
	}
	return out
}

func csFirst(m map[string]any, keys ...string) string {
	for _, k := range keys {
		if s := csS(m[k]); s != "" {
			return s
		}
	}
	return ""
}

// ── the structured summary ──

// callSumNorm brings a summary (the model's answer, a team edit, or an R32d
// summary) to the R32e shape, matches the library by exact title and fills
// the R32d fields from it (the board card, Telegram text and «Задачи на
// доску» read those).
func callSumNorm(sum map[string]any) map[string]any {
	if sum == nil {
		sum = map[string]any{}
	}
	lib := callLib()
	dash := func(s string) string { return strings.ReplaceAll(strings.ReplaceAll(s, "—", "-"), "–", "-") }
	// 1. Ситуация
	sit := csS(sum["situation"])
	if sit == "" {
		sit = csS(sum["summary"])
	}
	sum["situation"], sum["summary"] = dash(sit), dash(sit)
	// 2. Диагнозы: diagList, else diagnoses (objects or strings)
	src := sum["diagList"]
	if _, ok := sum["diagList"]; !ok {
		src = sum["diagnoses"]
	}
	diags, titles := []any{}, []any{}
	for _, d := range csMaps(src, "title") {
		t := dash(csFirst(d, "title", "text", "name"))
		if t == "" {
			continue
		}
		item := map[string]any{"title": t, "why": dash(csFirst(d, "why", "desc")), "lib": false, "id": ""}
		if r, ok := lib.diag[csNorm(t)]; ok {
			item["title"], item["lib"], item["id"] = r.Title, true, r.ID
		}
		diags = append(diags, item)
		titles = append(titles, item["title"])
	}
	sum["diagList"], sum["diagnoses"] = diags, titles
	sum["rootCause"] = dash(csS(sum["rootCause"]))
	// 4. Решения и инструменты: solutions, else decisions
	src = sum["solutions"]
	if _, ok := sum["solutions"]; !ok {
		src = sum["decisions"]
	}
	sols, decs := []any{}, []any{}
	for _, s := range csMaps(src, "text") {
		text, tool := dash(csFirst(s, "text", "what")), dash(csS(s["tool"]))
		item := map[string]any{"text": text, "tool": "", "toolId": "", "toolUrl": ""}
		if tool != "" {
			if r, ok := lib.tools[csNorm(tool)]; ok {
				item["tool"], item["toolId"], item["toolUrl"] = r.Title, r.ID, toolURL(r.ID)
			} else if text == "" {
				text = tool
			} else if !strings.Contains(strings.ToLower(text), strings.ToLower(tool)) {
				text += " (" + tool + ")"
			}
		}
		item["text"] = text
		if text == "" && item["tool"] == "" {
			continue
		}
		if text == "" {
			item["text"] = "Работать по инструменту «" + csS(item["tool"]) + "»"
		}
		sols = append(sols, item)
		decs = append(decs, item["text"])
	}
	sum["solutions"], sum["decisions"] = sols, decs
	// 5. План на 10 дней: plan, else checklist
	src = sum["plan"]
	if _, ok := sum["plan"]; !ok {
		src = sum["checklist"]
	}
	plan, ck := []any{}, []any{}
	for _, t := range csMaps(src, "what") {
		what := dash(csFirst(t, "what", "text"))
		if what == "" {
			continue
		}
		who, due := dash(csFirst(t, "who", "owner")), dash(csS(t["due"]))
		plan = append(plan, map[string]any{"who": who, "what": what, "due": due})
		k := map[string]any{"text": what, "due": due}
		if who != "" {
			k["owner"] = who
		}
		ck = append(ck, k)
	}
	sum["plan"], sum["checklist"] = plan, ck
	// 6. Метрики
	ms := []any{}
	for _, m := range csMaps(sum["metrics"], "name") {
		if n := dash(csFirst(m, "name", "label")); n != "" {
			ms = append(ms, map[string]any{"name": n, "now": dash(csS(m["now"])), "target": dash(csS(m["target"]))})
		}
	}
	sum["metrics"] = ms
	// 7. Домашнее задание
	hw := []any{}
	for _, s := range strs(sum["homework"]) {
		hw = append(hw, dash(s))
	}
	sum["homework"] = hw
	// 8. Следующая встреча: nextMeeting, else next
	nm := csM(sum["nextMeeting"])
	if nm == nil {
		nm = map[string]any{"date": "", "agenda": toAny(strs(sum["next"]))}
	}
	ag := []any{}
	for _, s := range strs(nm["agenda"]) {
		ag = append(ag, dash(s))
	}
	date := dash(csS(nm["date"]))
	sum["nextMeeting"] = map[string]any{"date": date, "agenda": ag}
	next := []any{}
	if date != "" {
		next = append(next, "Следующая встреча: "+date)
	}
	next = append(next, ag...)
	sum["next"] = next
	for _, k := range []string{"title", "pointA", "pointB", "quote"} {
		if s, ok := sum[k].(string); ok {
			sum[k] = dash(strings.TrimSpace(s))
		}
	}
	return sum
}

func toAny(a []string) []any {
	out := make([]any, 0, len(a))
	for _, s := range a {
		out = append(out, s)
	}
	return out
}

// callSumV2 tells an R32e summary from an older one.
func callSumV2(sum map[string]any) bool {
	if sum == nil {
		return false
	}
	_, a := sum["rootCause"]
	_, b := sum["plan"]
	_, c := sum["situation"]
	return a || b || c
}

// callSumFields: the R32e part of a board card (callCard).
func callSumFields(card map[string]any, meta map[string]any) {
	sum := csM(meta["summary"])
	if sum == nil {
		return
	}
	if !callSumV2(sum) {
		sum = callSumNorm(callJSON(sum))
	}
	for _, k := range []string{"situation", "diagList", "rootCause", "solutions", "plan", "metrics", "homework", "nextMeeting"} {
		if v, ok := sum[k]; ok {
			card[k] = v
		}
	}
	st := callSumStateOf(meta)
	if st == nil {
		return // before R32e: already with the resident
	}
	card["sumStatus"] = csS(st["status"])
	for _, k := range []string{"editedAt", "publishedAt", "auto"} {
		if v, ok := st[k]; ok && v != nil && v != "" {
			card["sum"+strings.ToUpper(k[:1])+k[1:]] = v
		}
	}
	if tg := csM(st["tg"]); tg != nil {
		card["sumTg"] = tg
	}
}

// ── state ──

func callSumStateOf(meta map[string]any) map[string]any { return csM(meta["sumState"]) }

// callSumStatus: draft | published ("" state = before R32e = published).
func callSumStatus(meta map[string]any) string {
	st := callSumStateOf(meta)
	if st == nil {
		return "published"
	}
	if s := csS(st["status"]); s != "" {
		return s
	}
	return "published"
}

func offlineFormat(f string) bool { return strings.Contains(strings.ToLower(f), "офлайн") }

func (h *PlatformAI) residentOfJob(ctx context.Context, j *pg.AIJob, meta map[string]any) string {
	if r := csS(meta["resident"]); r != "" {
		return r
	}
	if j.Resident != "" {
		return j.Resident
	}
	if j.BoardID != "" {
		if b, _ := h.repo.GetBoard(ctx, j.BoardID); b != nil {
			return b.Resident
		}
	}
	return ""
}

// callSumDraft: the processed call's summary in the R32e shape, a draft
// unless it was already published, and its PDF (runCallJob).
func (h *PlatformAI) callSumDraft(ctx context.Context, j *pg.AIJob, meta map[string]any) {
	meta["summary"] = callSumNorm(csM(meta["summary"]))
	st := callSumStateOf(meta)
	if st == nil || csS(st["status"]) != "published" {
		meta["sumState"] = map[string]any{"status": "draft", "createdAt": time.Now().UTC().Format(time.RFC3339)}
	}
	h.makeSummaryPDF(ctx, j, meta)
}

// callSumOwnerNote: the line under the owner's Telegram summary.
func (h *PlatformAI) callSumOwnerNote(ctx context.Context, resident string) string {
	if offlineFormat(h.repo.ResidentFormat(ctx, resident)) {
		return "\n\nРезидент на офлайн-формате: саммари ему не публикуется, остаётся у команды."
	}
	s := "\n\nЭто черновик саммари. Резидент получит PDF после публикации: платформа → доска резидента → Записи разборов → «Проверить и опубликовать»."
	if cfg := h.callSumSettings(ctx); cfg.Auto {
		s += fmt.Sprintf(" Без правок опубликуется сам через %d ч.", cfg.Hours)
	}
	return s
}

// ── settings ──

type callSumCfg struct {
	Auto  bool `json:"autoPublish"`
	Hours int  `json:"hours"`
}

func (h *PlatformAI) callSumSettings(ctx context.Context) callSumCfg {
	cfg := callSumCfg{Hours: callSumAutoHours}
	if d, err := h.repo.GetDoc(ctx, "server", callSumSettingsKey); err == nil && d != nil && !d.Deleted {
		_ = json.Unmarshal([]byte(d.Value), &cfg)
	}
	if cfg.Hours < 1 || cfg.Hours > 24*14 {
		cfg.Hours = callSumAutoHours
	}
	return cfg
}

// CallSumSettings: GET /ai/callsum/settings
func (h *PlatformAI) CallSumSettings(c *gin.Context) {
	if !teamOnly(c) {
		return
	}
	c.JSON(http.StatusOK, h.callSumSettings(c.Request.Context()))
}

// PutCallSumSettings: PUT /ai/callsum/settings {autoPublish, hours}
func (h *PlatformAI) PutCallSumSettings(c *gin.Context) {
	if !teamOnly(c) {
		return
	}
	var in callSumCfg
	if err := c.ShouldBindJSON(&in); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "bad_json"})
		return
	}
	if in.Hours < 1 || in.Hours > 24*14 {
		in.Hours = callSumAutoHours
	}
	ctx := c.Request.Context()
	val, _ := json.Marshal(in)
	for try := 0; try < 4; try++ {
		ver := 0
		if d, err := h.repo.GetDoc(ctx, "server", callSumSettingsKey); err == nil && d != nil {
			ver = d.Version
		}
		if _, err := h.repo.PutDoc(ctx, "server", callSumSettingsKey, ver, string(val), false, platformUser(c)); err == nil {
			c.JSON(http.StatusOK, in)
			return
		}
	}
	c.JSON(http.StatusInternalServerError, gin.H{"error": "store_failed"})
}

// ── team endpoints ──

var csLocks = struct {
	sync.Mutex
	m map[string]*sync.Mutex
}{m: map[string]*sync.Mutex{}}

func csLock(id string) func() {
	csLocks.Lock()
	mu := csLocks.m[id]
	if mu == nil {
		mu = &sync.Mutex{}
		csLocks.m[id] = mu
	}
	csLocks.Unlock()
	mu.Lock()
	return mu.Unlock
}

// callJobFor loads a processed call job for the editor.
func (h *PlatformAI) callJobFor(c *gin.Context) (*pg.AIJob, map[string]any, bool) {
	id := c.Param("id")
	if !platformIDRe.MatchString(id) {
		c.JSON(http.StatusNotFound, gin.H{"error": "not_found"})
		return nil, nil, false
	}
	j, meta := h.jobMeta(c.Request.Context(), id)
	if j == nil || j.Kind != "call" {
		c.JSON(http.StatusNotFound, gin.H{"error": "not_found"})
		return nil, nil, false
	}
	if j.Status != "done" || meta["summary"] == nil {
		c.JSON(http.StatusConflict, gin.H{"error": "not_ready", "detail": "Разбор ещё обрабатывается"})
		return nil, nil, false
	}
	return j, meta, true
}

// callSumView: what the editor shows.
func (h *PlatformAI) callSumView(ctx context.Context, j *pg.AIJob, meta map[string]any) gin.H {
	sum := csM(meta["summary"])
	if !callSumV2(sum) {
		sum = callSumNorm(callJSON(sum))
	}
	out := map[string]any{}
	for _, k := range []string{"title", "participants", "situation", "numbers", "pointA", "pointB", "diagList", "rootCause", "solutions", "plan", "metrics", "homework", "nextMeeting"} {
		out[k] = sum[k]
	}
	resident := h.residentOfJob(ctx, j, meta)
	lib := callLib()
	tr, _ := meta["transcript"].(string)
	return gin.H{"id": j.ID, "boardId": j.BoardID, "resident": resident, "date": meta["date"], "status": callSumStatus(meta),
		"state": callSumStateOf(meta), "offline": offlineFormat(h.repo.ResidentFormat(ctx, resident)),
		"summary": out, "pdf": meta["summaryPdf"], "hasTranscript": strings.TrimSpace(tr) != "",
		"settings": h.callSumSettings(ctx), "lib": gin.H{"diag": lib.diagT, "tools": lib.toolT}}
}

// CallSummary: GET /ai/calls/:id/summary
func (h *PlatformAI) CallSummary(c *gin.Context) {
	if !teamOnly(c) {
		return
	}
	j, meta, ok := h.callJobFor(c)
	if !ok {
		return
	}
	c.JSON(http.StatusOK, h.callSumView(c.Request.Context(), j, meta))
}

// callSumSave keeps the job, renders the PDF again and puts the card on the board.
func (h *PlatformAI) callSumSave(ctx context.Context, j *pg.AIJob, meta map[string]any, render bool) map[string]any {
	if render {
		h.makeSummaryPDF(ctx, j, meta)
	}
	b, _ := json.Marshal(meta)
	_ = h.repo.UpdateAIJob(ctx, j.ID, j.Status, j.Error, b)
	card := callCard(j.ID, meta)
	if j.BoardID != "" {
		if err := h.putBoardCall(ctx, j.BoardID, card); err != nil {
			log.Printf("callsum %s: board %s: %v", j.ID, j.BoardID, err)
		}
	}
	return callJSON(card)
}

// PutCallSummary: PUT /ai/calls/:id/summary {summary: {...}}
func (h *PlatformAI) PutCallSummary(c *gin.Context) {
	if !teamOnly(c) {
		return
	}
	var in struct {
		Summary map[string]any `json:"summary"`
	}
	if err := c.ShouldBindJSON(&in); err != nil || in.Summary == nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "bad_json"})
		return
	}
	defer csLock(c.Param("id"))()
	j, meta, ok := h.callJobFor(c)
	if !ok {
		return
	}
	ctx := c.Request.Context()
	sum := csM(meta["summary"])
	if !callSumV2(sum) {
		sum = callSumNorm(sum)
	}
	for _, k := range []string{"title", "situation", "numbers", "pointA", "pointB", "diagList", "rootCause", "solutions", "plan", "metrics", "homework", "nextMeeting"} {
		if v, ok := in.Summary[k]; ok {
			sum[k] = v
		}
	}
	meta["summary"] = callSumNorm(sum)
	st := callSumStateOf(meta)
	if st == nil {
		st = map[string]any{"status": "published"}
	}
	st["editedAt"], st["editedBy"] = time.Now().UTC().Format(time.RFC3339), platformUser(c)
	meta["sumState"] = st
	card := h.callSumSave(ctx, j, meta, true)
	c.JSON(http.StatusOK, gin.H{"ok": true, "card": card, "view": h.callSumView(ctx, j, meta)})
}

// RegenCallSummary: POST /ai/calls/:id/summary/regenerate
func (h *PlatformAI) RegenCallSummary(c *gin.Context) {
	if !teamOnly(c) {
		return
	}
	defer csLock(c.Param("id"))()
	j, meta, ok := h.callJobFor(c)
	if !ok {
		return
	}
	ctx := c.Request.Context()
	tr, _ := meta["transcript"].(string)
	if strings.TrimSpace(tr) == "" {
		c.JSON(http.StatusConflict, gin.H{"error": "no_transcript", "detail": "У этого разбора нет расшифровки"})
		return
	}
	if h.AI == nil || h.AI.Status()["text"] == "" {
		c.JSON(http.StatusConflict, gin.H{"error": "no_ai", "detail": "ИИ сейчас недоступен, попробуйте позже"})
		return
	}
	resident := h.residentOfJob(ctx, j, meta)
	actx, cancel := context.WithTimeout(ctx, 3*time.Minute)
	ans, err := h.AI.Text(ai.Heavy(actx), callSumPrompt(), "Резидент: "+resident+"\nДата: "+csS(meta["date"])+"\n\nРасшифровка:\n"+tr)
	cancel()
	var sum map[string]any
	if err == nil {
		if js := ai.JSONFrom(ans); js == "" || json.Unmarshal([]byte(js), &sum) != nil || len(sum) == 0 {
			err = fmt.Errorf("ответ ИИ не разобран")
		}
	}
	if err != nil {
		log.Printf("callsum %s: regenerate: %v", j.ID, err)
		c.JSON(http.StatusBadGateway, gin.H{"error": "ai_failed", "detail": "ИИ не ответил, попробуйте ещё раз"})
		return
	}
	meta["summary"] = callSumNorm(sum)
	st := callSumStateOf(meta)
	if st == nil || csS(st["status"]) != "published" {
		st = map[string]any{"status": "draft", "createdAt": time.Now().UTC().Format(time.RFC3339)}
	}
	delete(st, "editedAt")
	delete(st, "editedBy")
	st["regeneratedAt"] = time.Now().UTC().Format(time.RFC3339)
	meta["sumState"] = st
	card := h.callSumSave(ctx, j, meta, true)
	c.JSON(http.StatusOK, gin.H{"ok": true, "card": card, "view": h.callSumView(ctx, j, meta)})
}

// PublishCall: POST /ai/calls/:id/publish
func (h *PlatformAI) PublishCall(c *gin.Context) {
	if !teamOnly(c) {
		return
	}
	defer csLock(c.Param("id"))()
	j, meta, ok := h.callJobFor(c)
	if !ok {
		return
	}
	ctx := c.Request.Context()
	if offlineFormat(h.repo.ResidentFormat(ctx, h.residentOfJob(ctx, j, meta))) {
		c.JSON(http.StatusConflict, gin.H{"error": "offline", "detail": "Резидент на офлайн-формате: саммари разборов ему не показываются"})
		return
	}
	card, tg := h.publishCall(ctx, j, meta, platformUser(c), false, callSumNow())
	c.JSON(http.StatusOK, gin.H{"ok": true, "card": card, "tg": tg, "view": h.callSumView(ctx, j, meta)})
}

// publishCall: published, the PDF without the draft mark, the board card, Telegram.
func (h *PlatformAI) publishCall(ctx context.Context, j *pg.AIJob, meta map[string]any, by string, auto bool, now time.Time) (map[string]any, map[string]any) {
	meta["summary"] = callSumNorm(csM(meta["summary"]))
	st := callSumStateOf(meta)
	if st == nil {
		st = map[string]any{}
	}
	first := csS(st["status"]) != "published" || st["publishedAt"] == nil
	st["status"] = "published"
	st["publishedAt"], st["publishedBy"] = now.UTC().Format(time.RFC3339), by
	if auto {
		st["auto"] = true
	}
	meta["sumState"] = st
	h.makeSummaryPDF(ctx, j, meta)
	if first || csM(st["tg"]) == nil || csM(st["tg"])["ok"] != true {
		st["tg"] = map[string]any{"pending": true, "queuedAt": now.UTC().Format(time.RFC3339)}
		h.callSumSendTG(ctx, j, meta, now)
	}
	card := h.callSumSave(ctx, j, meta, false)
	return card, csM(st["tg"])
}

func callSumInHours(now time.Time) bool {
	hr := now.In(csAlmaty).Hour()
	return hr >= callSumSendFrom && hr < callSumSendUntil
}

// callSumSendTG sends the published PDF to the resident (now, or in the
// morning: quiet hours). Fills sumState.tg; the caller keeps meta.
func (h *PlatformAI) callSumSendTG(ctx context.Context, j *pg.AIJob, meta map[string]any, now time.Time) {
	st := callSumStateOf(meta)
	if st == nil {
		return
	}
	tg := csM(st["tg"])
	if tg == nil || tg["pending"] != true {
		return
	}
	if !callSumInHours(now) {
		tg["why"] = fmt.Sprintf("тихие часы: уйдёт в %02d:00", callSumSendFrom)
		if sent := csM(meta["sent"]); sent != nil {
			sent["residentWhy"] = fmt.Sprintf("опубликовано, PDF уйдёт в Telegram в %02d:00", callSumSendFrom)
		}
		return
	}
	resident := h.residentOfJob(ctx, j, meta)
	done := func(ok bool, why string) {
		tg["pending"], tg["ok"], tg["at"] = false, ok, now.UTC().Format(time.RFC3339)
		if why != "" {
			tg["why"] = why
		} else {
			delete(tg, "why")
		}
		if ok {
			sent := csM(meta["sent"])
			if sent == nil {
				sent = map[string]any{}
			}
			sent["resident"], sent["residentName"] = true, resident
			delete(sent, "residentWhy")
			meta["sent"] = sent
		}
	}
	id, name, _ := h.repo.ResidentTgByName(ctx, resident)
	if id == 0 {
		done(false, "резидент не найден в клубе или без Telegram")
		return
	}
	fid := csS(meta["summaryPdf"])
	var f *pg.PlatformFile
	if fid != "" {
		f, _ = h.repo.GetFile(ctx, fid)
	}
	if f == nil {
		done(false, "PDF не сохранился")
		return
	}
	date := csS(meta["date"])
	caption := "Саммари нашего разбора" + map[bool]string{true: " · " + date, false: ""}[date != ""] +
		"\n\nВсё главное в одном файле: диагнозы, решения, план на 10 дней, метрики и домашнее задание. Задачи уже в приложении, отчёт по ним в конце цикла."
	if first := firstName(name); first != "Резидент" && name != "" {
		caption = first + ", привет! " + caption
	}
	switch {
	case h.SendDoc != nil:
		if err := h.SendDoc(ctx, id, summaryName(resident, date), f.Data, caption); err != nil {
			done(false, err.Error())
			return
		}
	case h.Notify != nil:
		if err := h.send(ctx, id, caption+"\n\nPDF в приложении: Трекинг → Саммари разборов."); err != nil {
			done(false, err.Error())
			return
		}
	default:
		done(false, "бот не подключён")
		return
	}
	done(true, "")
}

// putBoardCall puts the call's card on the board: an existing card keeps the
// page's own fields (tasksOnBoard and the like), the server's are replaced.
func (h *PlatformAI) putBoardCall(ctx context.Context, boardID string, card map[string]any) error {
	card = callJSON(card)
	for try := 0; try < 6; try++ {
		b, err := h.repo.GetBoard(ctx, boardID)
		if err != nil || b == nil || b.Deleted {
			return err
		}
		var data map[string]any
		if json.Unmarshal(b.Data, &data) != nil || data == nil {
			return fmt.Errorf("board data")
		}
		calls, _ := data["calls"].([]any)
		have := false
		for i, x := range calls {
			if m, ok := x.(map[string]any); ok && m["id"] == card["id"] {
				for k, v := range card {
					m[k] = v
				}
				calls[i], have = m, true
			}
		}
		if !have {
			calls = append(calls, card)
		}
		data["calls"] = calls
		raw, _ := json.Marshal(data)
		if _, err = h.repo.PutBoard(ctx, boardID, b.Version, raw, "server:call"); err == nil {
			return nil
		}
		if !errors.Is(err, pg.ErrPlatformConflict) {
			return err
		}
		time.Sleep(150 * time.Millisecond)
	}
	return pg.ErrPlatformConflict
}

// ── background: quiet-hours sends and auto-publish ──

// CallSumLoop: every few minutes sends what waited for the morning and
// publishes drafts nobody touched (when the setting is on).
func (h *PlatformAI) CallSumLoop(ctx context.Context, every time.Duration) {
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		c, cancel := context.WithTimeout(ctx, 5*time.Minute)
		h.CallSumTick(c, time.Now())
		cancel()
	}
}

// CallSumTick: one pass (tests call it with a fixed time).
func (h *PlatformAI) CallSumTick(ctx context.Context, now time.Time) {
	jobs, err := h.repo.CallJobs(ctx, 300)
	if err != nil {
		return
	}
	cfg := h.callSumSettings(ctx)
	for _, x := range jobs {
		if x.Status != "done" {
			continue
		}
		var m map[string]any
		if json.Unmarshal(x.Result, &m) != nil {
			continue
		}
		st := callSumStateOf(m)
		if st == nil {
			continue
		}
		pending := csS(st["status"]) == "published" && csM(st["tg"]) != nil && csM(st["tg"])["pending"] == true
		auto := false
		if csS(st["status"]) == "draft" && cfg.Auto && st["editedAt"] == nil {
			if t, err := time.Parse(time.RFC3339, csS(st["createdAt"])); err == nil && now.Sub(t) >= time.Duration(cfg.Hours)*time.Hour {
				auto = true
			}
		}
		if !(pending && callSumInHours(now)) && !auto {
			continue
		}
		func() {
			defer csLock(x.ID)()
			j, meta := h.jobMeta(ctx, x.ID) // the full job (with the transcript)
			if j == nil {
				return
			}
			st := callSumStateOf(meta)
			if st == nil {
				return
			}
			if auto && csS(st["status"]) == "draft" && st["editedAt"] == nil {
				if offlineFormat(h.repo.ResidentFormat(ctx, h.residentOfJob(ctx, j, meta))) {
					return
				}
				h.publishCall(ctx, j, meta, "server:auto", true, now)
				log.Printf("callsum %s: published automatically", j.ID)
				return
			}
			if csS(st["status"]) == "published" {
				h.callSumSendTG(ctx, j, meta, now)
				b, _ := json.Marshal(meta)
				_ = h.repo.UpdateAIJob(ctx, j.ID, j.Status, j.Error, b)
				if j.BoardID != "" {
					_ = h.putBoardCall(ctx, j.BoardID, callCard(j.ID, meta))
				}
			}
		}()
	}
}

// ── the resident gate ──

// callVisible: a resident sees a call that is published (or from before R32e).
func callVisible(card map[string]any, statuses map[string]string) bool {
	id := csS(card["id"])
	if st, ok := statuses[id]; ok {
		return st == "" || st == "published"
	}
	st := csS(card["sumStatus"])
	return st == "" || st == "published"
}

// gateResidentCalls: an offline resident gets no calls at all, an online one
// only published summaries (the recordings are already gone: residentCalls).
func (h *PlatformHandler) gateResidentCalls(ctx context.Context, boards []pg.PlatformBoard, name string) []pg.PlatformBoard {
	if len(boards) == 0 {
		return boards
	}
	offline := offlineFormat(h.repo.ResidentFormat(ctx, name))
	type parsed struct {
		m     map[string]json.RawMessage
		calls []map[string]any
	}
	ps := make([]*parsed, len(boards))
	var ids []string
	for i := range boards {
		if !offline && !strings.Contains(string(boards[i].Data), `"calls"`) && !strings.Contains(string(boards[i].Data), `"pendingCalls"`) {
			continue
		}
		var m map[string]json.RawMessage
		if json.Unmarshal(boards[i].Data, &m) != nil {
			continue
		}
		p := &parsed{m: m}
		_ = json.Unmarshal(m["calls"], &p.calls)
		for _, c := range p.calls {
			if id := csS(c["id"]); id != "" {
				ids = append(ids, id)
			}
		}
		ps[i] = p
	}
	statuses := map[string]string{}
	if !offline && len(ids) > 0 {
		var err error
		if statuses, err = h.repo.CallSumStatuses(ctx, ids); err != nil {
			offline = true // the state is unknown: show nothing rather than a draft
		}
	}
	for i, p := range ps {
		if p == nil {
			continue
		}
		keep := []map[string]any{}
		if !offline {
			for _, c := range p.calls {
				if callVisible(c, statuses) {
					keep = append(keep, c)
				}
			}
		}
		raw, _ := json.Marshal(keep)
		p.m["calls"] = raw
		delete(p.m, "pendingCalls")
		if offline { // the page hides «Саммари разборов» for an offline resident
			p.m["callsOff"] = json.RawMessage("true")
		} else {
			delete(p.m, "callsOff")
		}
		if out, err := json.Marshal(p.m); err == nil {
			boards[i].Data = out
		}
	}
	return boards
}
