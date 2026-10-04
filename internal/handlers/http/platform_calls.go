package http

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/bnursik/business_surgery_backend/internal/ai"
	pg "github.com/bnursik/business_surgery_backend/internal/repository/pg"
	"github.com/gin-gonic/gin"
)

// Записи разборов: запись созвона → расшифровка → упаковка итогов →
// доска резидента, владелец и резидент в Telegram.
//
//   POST /ai/call              запись (тело) или ?file=<id> уже сохранённой записи
//   POST /ai/jobs/:id/retry    обработать ещё раз (запись хранится)
//   GET  /ai/calls             все записи разборов: задачи и файлы без задачи
//
// Запись сохраняется до любой обработки: без ключа ИИ, при ошибке ИИ или
// перезапуске сервера она не теряется. Прерванные задачи сервер продолжает сам
// (ResumeCalls), а готовые итоги сам кладёт в доску, не дожидаясь страницы.

const callSummarySystem = `Ты помощник трекеров Business Surgery (Рустам и Береке). По расшифровке онлайн-разбора бизнеса резидента
упакуй итог встречи. Отвечай ТОЛЬКО JSON:
{"title":"короткое название встречи","summary":"связный текст 5-10 предложений: с чем пришёл, что выяснили, к чему пришли",
"participants":["кто был на встрече: имя и роль (трекер, резидент, партнёр)"],"topics":["ключевые темы встречи, 3-6"],
"pointA":"где резидент сейчас (цифры, если звучали)","pointB":"куда идёт",
"problems":["ключевые проблемы, которые прозвучали"],"diagnoses":["корневые причины, диагнозы бизнеса"],
"decisions":["о чём договорились"],"checklist":[{"text":"конкретная задача","owner":"кто делает: имя резидента, трекер или сотрудник","due":"ДД.ММ или пусто"}],
"numbers":[{"label":"что это за цифра","value":"цифра с единицей: 5 000 000 ₸, 30 дней, 15%"}],
"next":["следующие шаги: что и когда дальше (следующая встреча, отчёт, контрольная точка)"],
"questions":["что осталось открытым"],"quote":"одна сильная фраза резидента или трекера","quotes":["2-4 точные цитаты из разговора"]}
Пиши по-русски, коротко и конкретно, без воды и без длинного тире. Цифры только те, что прозвучали, тысячи через пробел: 10 000.
Чек-лист: 3-10 задач, каждая начинается с глагола.`

// longBody: a recording of an hour uploads longer than the server's 60 s read limit.
func longBody(c *gin.Context) {
	rc := http.NewResponseController(c.Writer)
	_ = rc.SetReadDeadline(time.Now().Add(30 * time.Minute))
	_ = rc.SetWriteDeadline(time.Now().Add(30 * time.Minute))
}

func (h *PlatformAI) Call(c *gin.Context) {
	if !teamOnly(c) {
		return
	}
	longBody(c)
	ctx := c.Request.Context()
	resident, date, board := c.Query("resident"), c.Query("date"), c.Query("board")
	if date == "" {
		date = time.Now().In(time.FixedZone("Almaty", 5*3600)).Format("02.01.2006")
	}
	meta := map[string]any{"resident": resident, "date": date, "stage": "queued"}
	if fid := c.Query("file"); fid != "" { // a recording kept earlier: process it now
		if !platformIDRe.MatchString(fid) || !h.repo.FileExists(ctx, fid) {
			c.JSON(http.StatusNotFound, gin.H{"error": "Запись не найдена"})
			return
		}
		f, err := h.repo.GetFile(ctx, fid)
		if err != nil || f == nil {
			c.JSON(http.StatusNotFound, gin.H{"error": "Запись не найдена"})
			return
		}
		meta["file"], meta["mime"], meta["size"] = f.ID, f.Mime, f.Size
		if strings.HasPrefix(f.Mime, "text/") {
			meta["text"] = true
		} else {
			meta["audio"] = f.ID
		}
	} else {
		data, err := io.ReadAll(http.MaxBytesReader(c.Writer, c.Request.Body, platformCallMax))
		if err != nil {
			c.JSON(http.StatusRequestEntityTooLarge, gin.H{"error": "Запись не принята: больше 120 МБ или связь оборвалась"})
			return
		}
		if len(data) == 0 {
			c.JSON(http.StatusBadRequest, gin.H{"error": "recording_required"})
			return
		}
		mt := c.GetHeader("Content-Type")
		if mt == "" {
			mt = "audio/webm"
		}
		isText := strings.HasPrefix(mt, "text/")
		day := time.Now().In(time.FixedZone("Almaty", 5*3600)).Format("02.01.2006 15.04")
		name := fmt.Sprintf("Запись разбора %s %s.webm", resident, day)
		if isText {
			name = fmt.Sprintf("Расшифровка разбора %s %s.txt", resident, day)
		}
		// The recording is kept first: whatever happens next, it is not lost.
		f := pg.PlatformFile{ID: newID(), Name: strings.Join(strings.Fields(name), " "), Mime: mt, Data: data}
		if err := h.repo.PutFile(ctx, f, platformUser(c)); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Запись не сохранилась на сервере, попробуйте ещё раз"})
			return
		}
		meta["file"], meta["mime"], meta["size"] = f.ID, mt, len(data)
		if isText {
			meta["text"] = true
		} else {
			meta["audio"] = f.ID
		}
	}
	job := pg.AIJob{ID: newID(), Kind: "call", BoardID: board, Resident: resident, Status: "queued"}
	if err := h.repo.CreateAIJob(ctx, job, platformUser(c)); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "store_failed"})
		return
	}
	mb, _ := json.Marshal(meta)
	_ = h.repo.UpdateAIJob(ctx, job.ID, "queued", "", mb)
	if msg := h.noKey(meta["text"] == true); msg != "" {
		// Kept, not processed: «Обработать снова» works once the key is there.
		_ = h.repo.UpdateAIJob(ctx, job.ID, "error", msg, nil)
		c.JSON(http.StatusOK, gin.H{"id": job.ID, "status": "error", "error": msg, "noKey": true, "file": meta["file"]})
		return
	}
	h.Run(func() { h.runCallJob(job.ID) })
	c.JSON(http.StatusOK, gin.H{"id": job.ID, "status": "queued", "file": meta["file"]})
}

func (h *PlatformAI) noKey(isText bool) string {
	st := h.AI.Status()
	if st["text"] == "" || (!isText && st["speech"] == "") {
		return "Нет ключа ИИ. Добавьте GEMINI_API_KEY в переменные Railway. Запись сохранена"
	}
	return ""
}

// Retry: POST /ai/jobs/:id/retry processes the stored recording again.
func (h *PlatformAI) RetryCall(c *gin.Context) {
	if !teamOnly(c) {
		return
	}
	ctx := c.Request.Context()
	j, err := h.repo.GetAIJob(ctx, c.Param("id"))
	if err != nil || j == nil || j.Kind != "call" {
		c.JSON(http.StatusNotFound, gin.H{"error": "not_found"})
		return
	}
	meta := map[string]any{}
	_ = json.Unmarshal(j.Result, &meta)
	fid, _ := meta["file"].(string)
	if fid == "" {
		fid, _ = meta["audio"].(string)
	}
	if fid == "" || !h.repo.FileExists(ctx, fid) {
		c.JSON(http.StatusOK, gin.H{"error": "Запись не сохранилась на сервере, обработать снова нельзя"})
		return
	}
	if msg := h.noKey(meta["text"] == true); msg != "" {
		c.JSON(http.StatusOK, gin.H{"error": msg, "noKey": true})
		return
	}
	if b := c.Query("board"); b != "" && j.BoardID == "" {
		_, _ = h.repo.SetAIJobBoard(ctx, j.ID, b, c.Query("resident"))
	}
	meta["stage"] = "queued"
	mb, _ := json.Marshal(meta)
	_ = h.repo.UpdateAIJob(ctx, j.ID, "queued", "", mb)
	h.Run(func() { h.runCallJob(j.ID) })
	c.JSON(http.StatusOK, gin.H{"id": j.ID, "status": "queued"})
}

// Calls: GET /ai/calls lists every call job and the recordings no job uses.
func (h *PlatformAI) Calls(c *gin.Context) {
	if !teamOnly(c) {
		return
	}
	ctx := c.Request.Context()
	h.repo.FailStaleAIJobs(ctx)
	jobs, err := h.repo.CallJobs(ctx, 200)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "load_failed"})
		return
	}
	used := map[string]bool{}
	for _, j := range jobs {
		var m map[string]any
		_ = json.Unmarshal(j.Result, &m)
		for _, k := range []string{"file", "audio"} {
			if s, _ := m[k].(string); s != "" {
				used[s] = true
			}
		}
	}
	files, _ := h.repo.CallFiles(ctx, 200)
	orphans := []pg.PlatformFile{}
	for _, f := range files {
		if !used[f.ID] {
			orphans = append(orphans, f)
		}
	}
	c.JSON(http.StatusOK, gin.H{"jobs": jobs, "files": orphans})
}

func (h *PlatformAI) jobMeta(ctx context.Context, id string) (*pg.AIJob, map[string]any) {
	j, err := h.repo.GetAIJob(ctx, id)
	if err != nil || j == nil {
		return nil, nil
	}
	m := map[string]any{}
	_ = json.Unmarshal(j.Result, &m)
	return j, m
}

func (h *PlatformAI) runCallJob(id string) {
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Minute)
	defer cancel()
	j, meta := h.jobMeta(ctx, id)
	if j == nil {
		return
	}
	// Heartbeat: a live job is not taken over by ResumeCalls.
	stop := make(chan struct{})
	defer close(stop)
	go func() {
		t := time.NewTicker(30 * time.Second)
		defer t.Stop()
		for {
			select {
			case <-stop:
				return
			case <-t.C:
				h.repo.TouchAIJob(context.Background(), id)
			}
		}
	}()
	stage := func(s string) {
		meta["stage"] = s
		b, _ := json.Marshal(meta)
		_ = h.repo.UpdateAIJob(ctx, id, "running", "", b)
	}
	fail := func(err error) {
		log.Printf("platform call %s: %v", id, err)
		meta["stage"] = "error"
		b, _ := json.Marshal(meta)
		msg := err.Error()
		if he := (*ai.HTTPError)(nil); errors.As(err, &he) {
			msg = fmt.Sprintf("ИИ ответил ошибкой %d. Запись сохранена, её можно обработать снова", he.Status)
		}
		_ = h.repo.UpdateAIJob(ctx, id, "error", msg, b)
		h.notifyFail(ctx, j, meta, msg)
	}
	fid, _ := meta["file"].(string)
	if fid == "" {
		fid, _ = meta["audio"].(string)
	}
	if fid == "" {
		fail(errors.New("запись не сохранилась на сервере"))
		return
	}
	f, err := h.repo.GetFile(ctx, fid)
	if err != nil || f == nil {
		fail(errors.New("запись не найдена на сервере"))
		return
	}
	isText := strings.HasPrefix(f.Mime, "text/")
	transcript := string(f.Data)
	if !isText {
		stage("transcribe")
		t, err := h.AI.Transcribe(ctx, f.Data, f.Mime)
		if err != nil {
			fail(err)
			return
		}
		transcript = t
	}
	if strings.TrimSpace(transcript) == "" {
		fail(errors.New("в записи не слышно речи"))
		return
	}
	meta["transcript"] = transcript
	stage("summary")
	resident, _ := meta["resident"].(string)
	if resident == "" {
		resident = j.Resident
	}
	date, _ := meta["date"].(string)
	ans, err := h.AI.Text(ctx, callSumPrompt(), "Резидент: "+resident+"\nДата: "+date+"\n\nРасшифровка:\n"+transcript) // R32e: callsum_flow.go
	if err != nil {
		fail(err)
		return
	}
	var sum map[string]any
	if js := ai.JSONFrom(ans); js == "" || json.Unmarshal([]byte(js), &sum) != nil {
		sum = map[string]any{"summary": strings.TrimSpace(ans)}
	}
	meta["summary"] = sum
	// R32e: the structured summary as a draft and its PDF; the resident gets
	// it when the team publishes (callsum_flow.go)
	h.callSumDraft(ctx, j, meta)
	meta["stage"] = "deliver"
	b, _ := json.Marshal(meta)
	_ = h.repo.UpdateAIJob(ctx, id, "running", "", b)
	h.deliverCall(ctx, j, meta)
	meta["stage"] = "done"
	b, _ = json.Marshal(meta)
	_ = h.repo.UpdateAIJob(ctx, id, "done", "", b)
}

// strs: a list of strings from the model's JSON (it may send one string).
func strs(v any) []string {
	out := []string{}
	switch x := v.(type) {
	case []any:
		for _, e := range x {
			switch y := e.(type) {
			case string:
				if s := strings.TrimSpace(y); s != "" {
					out = append(out, s)
				}
			case map[string]any:
				if s, _ := y["text"].(string); strings.TrimSpace(s) != "" {
					out = append(out, strings.TrimSpace(s))
				}
			}
		}
	case string:
		if s := strings.TrimSpace(x); s != "" {
			out = append(out, s)
		}
	}
	return out
}

func checklistOf(v any) []map[string]any {
	out := []map[string]any{}
	if arr, ok := v.([]any); ok {
		for _, e := range arr {
			switch y := e.(type) {
			case string:
				if strings.TrimSpace(y) != "" {
					out = append(out, map[string]any{"text": strings.TrimSpace(y), "due": ""})
				}
			case map[string]any:
				t, _ := y["text"].(string)
				d, _ := y["due"].(string)
				o, _ := y["owner"].(string)
				if strings.TrimSpace(t) != "" {
					k := map[string]any{"text": strings.TrimSpace(t), "due": strings.TrimSpace(d)}
					if o = strings.TrimSpace(o); o != "" {
						k["owner"] = o
					}
					out = append(out, k)
				}
			}
		}
	}
	return out
}

// callCard: the call as the board keeps it (board.calls[], same shape as the page).
func callCard(id string, meta map[string]any) map[string]any {
	sum, _ := meta["summary"].(map[string]any)
	if sum == nil {
		sum = map[string]any{}
	}
	date, _ := meta["date"].(string)
	title, _ := sum["title"].(string)
	if strings.TrimSpace(title) == "" {
		title = "Разбор " + date
	}
	s := func(k string) string { v, _ := sum[k].(string); return strings.TrimSpace(v) }
	card := map[string]any{
		"id": id, "date": date, "title": title, "summary": s("summary"),
		"pointA": s("pointA"), "pointB": s("pointB"), "quote": s("quote"),
		"problems": strs(sum["problems"]), "diagnoses": strs(sum["diagnoses"]), "decisions": strs(sum["decisions"]),
		"checklist": checklistOf(sum["checklist"]), "next": strs(sum["next"]), "questions": strs(sum["questions"]),
		"transcript": meta["transcript"], "audio": meta["audio"], "file": meta["file"], "by": "server",
		// R32d: the summary PDF's parts (platform_calls_summary.go)
		"participants": strs(sum["participants"]), "topics": strs(sum["topics"]), "quotes": strs(sum["quotes"]),
		"numbers": numbersOf(sum["numbers"]),
	}
	if pdf, _ := meta["summaryPdf"].(string); pdf != "" {
		card["summaryPdf"] = pdf
	}
	callSumFields(card, meta) // R32e: sections and draft/published (callsum_flow.go)
	if sent, ok := meta["sent"]; ok {
		card["sent"] = sent
	}
	return card
}

// CallText: the packaged summary as a Telegram message (plain text).
func CallText(resident string, meta map[string]any) string {
	card := callCard("", meta)
	var b strings.Builder
	b.WriteString("Итоги разбора")
	if resident != "" {
		b.WriteString(" · " + resident)
	}
	if d, _ := card["date"].(string); d != "" {
		b.WriteString(" · " + d)
	}
	b.WriteString("\n" + card["title"].(string) + "\n")
	if s, _ := card["summary"].(string); s != "" {
		b.WriteString("\n" + s + "\n")
	}
	if a, _ := card["pointA"].(string); a != "" {
		b.WriteString("\nТочка А: " + a)
		if p, _ := card["pointB"].(string); p != "" {
			b.WriteString("\nТочка Б: " + p)
		}
		b.WriteString("\n")
	}
	list := func(head string, items []string) {
		if len(items) == 0 {
			return
		}
		b.WriteString("\n" + head + "\n")
		for _, x := range items {
			b.WriteString("• " + x + "\n")
		}
	}
	list("Ключевые проблемы", card["problems"].([]string))
	list("Диагнозы", card["diagnoses"].([]string))
	list("Решения", card["decisions"].([]string))
	var tasks []string
	for _, k := range card["checklist"].([]map[string]any) {
		t := k["text"].(string)
		if d, _ := k["due"].(string); d != "" {
			t += " (до " + d + ")"
		}
		tasks = append(tasks, t)
	}
	list("Задачи", tasks)
	list("Следующие шаги", card["next"].([]string))
	list("Открытые вопросы", card["questions"].([]string))
	return strings.ReplaceAll(strings.TrimSpace(b.String()), "—", "-")
}

// splitTG: Telegram takes up to 4096 characters per message.
func splitTG(s string) []string {
	const max = 3900
	var out []string
	r := []rune(s)
	for len(r) > max {
		cut := max
		for i := max; i > max/2; i-- {
			if r[i] == '\n' {
				cut = i
				break
			}
		}
		out = append(out, string(r[:cut]))
		r = r[cut:]
	}
	if len(r) > 0 {
		out = append(out, string(r))
	}
	return out
}

func (h *PlatformAI) send(ctx context.Context, id int64, text string) error {
	if h.Notify == nil {
		return errors.New("бот не подключён")
	}
	for _, part := range splitTG(text) {
		if err := h.Notify(ctx, id, part); err != nil {
			return err
		}
	}
	return nil
}

// deliverCall: Telegram to the owner and the resident, then the board.
func (h *PlatformAI) deliverCall(ctx context.Context, j *pg.AIJob, meta map[string]any) {
	resident, _ := meta["resident"].(string)
	if resident == "" {
		resident = j.Resident
	}
	var board *pg.PlatformBoard
	if j.BoardID != "" {
		board, _ = h.repo.GetBoard(ctx, j.BoardID)
		if board != nil && resident == "" {
			resident = board.Resident
		}
	}
	if _, done := meta["sent"]; !done { // a resumed job does not send twice
		// R32e: the owner gets the summary and the draft PDF; the resident gets
		// the PDF when the team publishes it (callsum_flow.go)
		text := CallText(resident, meta)
		draft := callSumStatus(meta) == "draft"
		if draft {
			text += h.callSumOwnerNote(ctx, resident)
		}
		sent := map[string]any{"at": time.Now().UTC().Format(time.RFC3339)}
		if h.Owner > 0 {
			if err := h.send(ctx, h.Owner, text); err != nil {
				sent["owner"], sent["ownerWhy"] = false, err.Error()
			} else {
				sent["owner"] = true
				if fid, _ := meta["summaryPdf"].(string); fid != "" && h.SendDoc != nil {
					if f, err := h.repo.GetFile(ctx, fid); err == nil && f != nil {
						_ = h.SendDoc(ctx, h.Owner, "Черновик · "+f.Name, f.Data, "Черновик саммари для проверки")
					}
				}
			}
		}
		tg, _, _ := h.repo.ResidentTgByName(ctx, resident)
		switch {
		case tg == 0:
			sent["resident"], sent["residentWhy"] = false, "резидент не найден в клубе или без Telegram"
		case draft:
			sent["resident"], sent["residentWhy"] = false, "саммари в черновике: уйдёт резиденту после публикации"
		}
		meta["sent"] = sent
		b, _ := json.Marshal(meta)
		_ = h.repo.UpdateAIJob(ctx, j.ID, "running", "", b)
	}
	if board != nil {
		if err := h.attachToBoard(ctx, j.BoardID, callCard(j.ID, meta)); err != nil {
			log.Printf("platform call %s: board %s: %v", j.ID, j.BoardID, err)
		}
	}
}

// attachToBoard adds the call to board.calls (once) and drops it from pendingCalls.
func (h *PlatformAI) attachToBoard(ctx context.Context, boardID string, card map[string]any) error {
	for try := 0; try < 6; try++ {
		b, err := h.repo.GetBoard(ctx, boardID)
		if err != nil || b == nil || b.Deleted {
			return err
		}
		var data map[string]any
		if json.Unmarshal(b.Data, &data) != nil || data == nil {
			return errors.New("board data")
		}
		calls, _ := data["calls"].([]any)
		have := false
		for i, c := range calls {
			if m, ok := c.(map[string]any); ok && m["id"] == card["id"] {
				have = true
				if _, ok := m["sent"]; !ok && card["sent"] != nil {
					m["sent"] = card["sent"]
					calls[i] = m
				}
			}
		}
		if !have {
			calls = append(calls, card)
		}
		data["calls"] = calls
		if pend, ok := data["pendingCalls"].([]any); ok {
			keep := []any{}
			for _, p := range pend {
				if p != card["id"] {
					keep = append(keep, p)
				}
			}
			data["pendingCalls"] = keep
		}
		raw, _ := json.Marshal(data)
		if _, err = h.repo.PutBoard(ctx, boardID, b.Version, raw, "server:call"); err == nil {
			return nil
		}
		if !errors.Is(err, pg.ErrPlatformConflict) {
			return err
		}
		time.Sleep(200 * time.Millisecond)
	}
	return pg.ErrPlatformConflict
}

func (h *PlatformAI) notifyFail(ctx context.Context, j *pg.AIJob, meta map[string]any, msg string) {
	if h.Owner == 0 || h.Notify == nil {
		return
	}
	res, _ := meta["resident"].(string)
	if res == "" {
		res = j.Resident
	}
	_ = h.send(ctx, h.Owner, "Запись разбора"+map[bool]string{true: " · " + res, false: ""}[res != ""]+
		" не обработана: "+msg+"\nЗапись сохранена. На платформе: доска → Записи разборов → «Обработать снова».")
}

// ResumeCalls continues call jobs that a restart or a deploy interrupted.
func (h *PlatformAI) ResumeCalls(ctx context.Context, first, every, idle time.Duration) {
	t := time.NewTimer(first)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		ids, err := h.repo.ClaimStaleCallJobs(ctx, idle)
		if err != nil {
			log.Printf("platform calls resume: %v", err)
		}
		for _, id := range ids {
			id := id
			log.Printf("platform call %s: resumed after a restart", id)
			h.Run(func() { h.runCallJob(id) })
		}
		t.Reset(every)
	}
}
