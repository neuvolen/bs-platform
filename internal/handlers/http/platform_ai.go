package http

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"mime"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"

	"github.com/bnursik/business_surgery_backend/internal/ai"
	pg "github.com/bnursik/business_surgery_backend/internal/repository/pg"
	"github.com/gin-gonic/gin"
)

// Files and AI of the platform.
//   POST /files            team uploads a file (raw body, name in X-File-Name)
//   GET  /files/:id        signed-in users read it
//   GET  /ai/status        which models are configured
//   POST /ai/command       a spoken phrase becomes board actions (team)
//   POST /ai/call          a call recording or transcript becomes a summary (team)
//   GET  /ai/jobs/:id      the state of that work
//   GET  /ai/jobs?board=   the latest results of a board

const (
	platformFileMax = 80 << 20
	platformCallMax = 120 << 20
)

type PlatformAI struct {
	repo *pg.PlatformRepo
	// Ops: journal of club data changes (shown in Учёт → Журнал операций).
	Ops interface {
		Ops(ctx context.Context, limit int) ([]pg.ClubOp, error)
	}
	AI *ai.Client
	// Run starts background work; tests replace it to run inline.
	Run func(func())
}

func NewPlatformAI(repo *pg.PlatformRepo, c *ai.Client) *PlatformAI {
	if c == nil {
		c = ai.FromEnv()
	}
	return &PlatformAI{repo: repo, AI: c, Run: func(f func()) { go f() }}
}

func newID() string {
	b := make([]byte, 12)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

func teamOnly(c *gin.Context) bool {
	if isResident(c) {
		forbidden(c, "team_only")
		return false
	}
	return true
}

func (h *PlatformAI) UploadFile(c *gin.Context) {
	if !teamOnly(c) {
		return
	}
	data, err := io.ReadAll(http.MaxBytesReader(c.Writer, c.Request.Body, platformFileMax))
	if err != nil {
		c.JSON(http.StatusRequestEntityTooLarge, gin.H{"error": "too_big", "max": platformFileMax})
		return
	}
	if len(data) == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "empty"})
		return
	}
	name, _ := url.QueryUnescape(c.GetHeader("X-File-Name"))
	if name == "" {
		name = "file"
	}
	mt := c.GetHeader("Content-Type")
	if mt == "" || strings.HasPrefix(mt, "application/x-www-form") {
		mt = http.DetectContentType(data)
	}
	f := pg.PlatformFile{ID: newID(), Name: name, Mime: mt, Data: data}
	if err := h.repo.PutFile(c.Request.Context(), f, platformUser(c)); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "store_failed"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"id": f.ID, "name": name, "size": len(data), "mime": mt})
}

func (h *PlatformAI) GetFile(c *gin.Context) {
	id := c.Param("id")
	if !platformIDRe.MatchString(id) {
		c.Status(http.StatusNotFound)
		return
	}
	f, err := h.repo.GetFile(c.Request.Context(), id)
	if err != nil || f == nil {
		c.Status(http.StatusNotFound)
		return
	}
	c.Header("Content-Disposition", mime.FormatMediaType("inline", map[string]string{"filename": f.Name}))
	c.Header("Cache-Control", "private, max-age=86400")
	c.Data(http.StatusOK, f.Mime, f.Data)
}

func (h *PlatformAI) Status(c *gin.Context) { c.JSON(http.StatusOK, h.AI.Status()) }

// ── Voice assistant ──

const commandSystem = `Ты голосовой помощник платформы Business Surgery. Трекер (Рустам или Береке) ведёт разбор бизнеса резидента на доске и говорит вслух.
Твоя задача: понять фразу (речь распознана автоматически, возможны ошибки, отсутствие знаков препинания, слова-паразиты) и вернуть действия.

ДОСКА. Узлы: root (резидент в центре), point с role pointA/pointB (точки А и Б), strat (стратегия), diag (диагноз), tool (инструмент),
task (задача), goal (цель), quest (вопрос), note (заметка), cont (контакт), dna. Узлы образуют дерево от root. У каждого узла в контексте есть id, type, title и parent.
Органы бизнеса: Стратегия (мозг), Маркетинг (сердце), Продажи (руки), Команда (костяк), Финансы (кровь), Процессы (ДНК), Аналитика (зрение).

ОТВЕТ: только JSON {"actions":[...], "say":"коротко по-русски, что сделано или что уточнить"}.
ДЕЙСТВИЯ:
{"op":"add_node","type":"task|note|goal|quest|strat|cont|diag|tool","title":"...","desc":"...","parent":<id|null>,"date":"ДД.ММ"}
{"op":"edit_node","id":<id>,"title":"...","desc":"..."}   {"op":"delete_node","id":<id>}
{"op":"link","a":<id>,"b":<id>}   {"op":"unlink","a":<id>,"b":<id>}
{"op":"set_point","which":"A|B","text":"..."}   {"op":"set_strategy","text":"..."}
{"op":"pick_diag","query":"название из diag_library","parent":<id|null>}   {"op":"pick_tool","query":"название из tool_library","parent":<id|null>}
{"op":"task_done","id":<id>}   {"op":"select","id":<id>}
{"op":"set_info","field":"name|last|birth|city|biz|fam|a|q|b","value":"..."}
{"op":"open_resident","name":"..."}   {"op":"open_section","id":"<id из sections>"}   {"op":"open_panel","tab":"data|tests|prep|summary|tasks|reports|calls"}
{"op":"rename_board","title":"..."}   {"op":"new_cycle"}   {"op":"presentation","on":true|false}   {"op":"save"}   {"op":"undo"}   {"op":"redo"}
{"op":"fit"}   {"op":"zoom","dir":"in|out"}   {"op":"theme"}   {"op":"health","on":true|false}   {"op":"draw","on":true|false}
{"op":"start_call"}   {"op":"stop_call"}   {"op":"mode","value":"admin|res|lead"}
{"op":"add_contact","name":"...","phone":"...","category":"...","telegram":"...","instagram":"..."}   {"op":"search","query":"..."}   {"op":"add_sticker","query":"..."}

ПРАВИЛА:
1. id бери только из контекста. Узел, упомянутый по смыслу («к кассовым разрывам», «к этому», «к нему»), найди по title; «это/этот/выделенный» = selected; «последний/только что» = последний из recent.
2. Диагноз или инструмент сначала ищи в библиотеке (pick_diag/pick_tool, query = точное название из библиотеки). Если похожего нет: add_node с type diag/tool.
3. Задачу без указания родителя вешай на выделенный узел, если он диагноз или инструмент; иначе parent null. Дату переводи в ДД.ММ («до пятницы», «пятого октября»: вычисли от today).
4. Несколько команд в одной фразе = несколько действий по порядку.
5. Если фраза не команда, а мысль, наблюдение или цитата резидента: add_node type note, title = мысль коротко и грамотно.
6. Исправляй ошибки распознавания по смыслу («касовые разрывы» = Кассовые разрывы, «точка а» = pointA).
7. Не выдумывай действий. Если непонятно, верни пустой actions и в say короткий вопрос.

ПРИМЕРЫ:
«поставь диагноз кассовые разрывы» → {"actions":[{"op":"pick_diag","query":"Кассовые разрывы","parent":null}],"say":"Диагноз «Кассовые разрывы» на доске"}
«к нему инструмент платёжный календарь и задача заполнить до пятого» (selected=12, диагноз) → {"actions":[{"op":"pick_tool","query":"Платёжный календарь","parent":12},{"op":"add_node","type":"task","title":"Заполнить платёжный календарь","parent":12,"date":"05.10"}],"say":"Добавил инструмент и задачу до 05.10"}
«точка а оборот пять миллионов прибыль шестьсот тысяч» → {"actions":[{"op":"set_point","which":"A","text":"Оборот 5 млн, прибыль 600 тыс"}],"say":"Точка А записана"}
«открой доску даулета» → {"actions":[{"op":"open_resident","name":"Даулет"}],"say":"Открываю доску Даулета"}
«клиенты жалуются что долго отвечаем» → {"actions":[{"op":"add_node","type":"note","title":"Клиенты жалуются на долгий ответ"}],"say":"Заметка добавлена"}
«удали это» (selected=7) → {"actions":[{"op":"delete_node","id":7}],"say":"Удалил"}
«покажи подготовку к встрече» → {"actions":[{"op":"open_panel","tab":"prep"}],"say":"Открыл подготовку"}`

type commandReq struct {
	Text    string          `json:"text"`
	Context json.RawMessage `json:"context"`
}

func (h *PlatformAI) Command(c *gin.Context) {
	if !teamOnly(c) {
		return
	}
	var r commandReq
	if err := c.ShouldBindJSON(&r); err != nil || strings.TrimSpace(r.Text) == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "text_required"})
		return
	}
	ctx, cancel := context.WithTimeout(c.Request.Context(), 60*time.Second)
	defer cancel()
	today := time.Now().In(time.FixedZone("Almaty", 5*3600)).Format("02.01.2006, Monday")
	ans, err := h.AI.JSON(ctx, commandSystem, "today: "+today+"\nСостояние платформы:\n"+string(r.Context)+"\n\nФраза трекера: «"+r.Text+"»")
	if err != nil {
		c.JSON(http.StatusOK, gin.H{"error": err.Error(), "noKey": err == ai.ErrNoKey})
		return
	}
	js := ai.JSONFrom(ans)
	var out map[string]any
	if js == "" || json.Unmarshal([]byte(js), &out) != nil {
		c.JSON(http.StatusOK, gin.H{"actions": []any{}, "say": "Не понял команду"})
		return
	}
	c.JSON(http.StatusOK, out)
}

// ── Call summary ──

const summarySystem = `Ты помощник трекеров Business Surgery (Рустам и Береке). По расшифровке онлайн-разбора бизнеса резидента
составь итог встречи. Отвечай ТОЛЬКО JSON:
{"title":"короткое название встречи","summary":"связный текст 5-10 предложений: с чем пришёл, что выяснили, к чему пришли",
"pointA":"где резидент сейчас (цифры, если звучали)","pointB":"куда идёт","diagnoses":["корневые проблемы"],
"decisions":["о чём договорились"],"checklist":[{"text":"конкретное действие резидента","due":"ДД.ММ или пусто"}],
"questions":["что осталось открытым"],"quote":"одна сильная фраза резидента или трекера"}
Пиши по-русски, коротко и конкретно, без воды. Чек-лист: 3-10 действий, каждое начинается с глагола.`

func (h *PlatformAI) Call(c *gin.Context) {
	if !teamOnly(c) {
		return
	}
	data, err := io.ReadAll(http.MaxBytesReader(c.Writer, c.Request.Body, platformCallMax))
	if err != nil || len(data) == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "recording_required"})
		return
	}
	mt := c.GetHeader("Content-Type")
	isText := strings.HasPrefix(mt, "text/")
	st := h.AI.Status()
	if st["text"] == "" || (!isText && st["speech"] == "") {
		c.JSON(http.StatusOK, gin.H{"error": "Нет ключа ИИ. Добавьте GEMINI_API_KEY в переменные Railway", "noKey": true})
		return
	}
	resident := c.Query("resident")
	job := pg.AIJob{ID: newID(), Kind: "call", BoardID: c.Query("board"), Resident: resident, Status: "queued"}
	ctx := c.Request.Context()
	if err := h.repo.CreateAIJob(ctx, job, platformUser(c)); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "store_failed"})
		return
	}
	// The recording is kept: it can be listened to again or re-processed.
	var audioID string
	if !isText {
		audioID = newID()
		name := fmt.Sprintf("Созвон %s %s.webm", resident, time.Now().Format("02.01.2006"))
		if err := h.repo.PutFile(ctx, pg.PlatformFile{ID: audioID, Name: name, Mime: mt, Data: data}, platformUser(c)); err != nil {
			audioID = ""
		}
	}
	date := c.Query("date")
	h.Run(func() { h.runCall(job.ID, data, mt, isText, resident, date, audioID) })
	c.JSON(http.StatusOK, gin.H{"id": job.ID, "status": "queued"})
}

func (h *PlatformAI) runCall(id string, data []byte, mt string, isText bool, resident, date, audioID string) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
	defer cancel()
	fail := func(err error) {
		log.Printf("platform ai call %s: %v", id, err)
		_ = h.repo.UpdateAIJob(ctx, id, "error", err.Error(), nil)
	}
	_ = h.repo.UpdateAIJob(ctx, id, "running", "", nil)
	transcript := string(data)
	if !isText {
		t, err := h.AI.Transcribe(ctx, data, mt)
		if err != nil {
			fail(err)
			return
		}
		transcript = t
	}
	if strings.TrimSpace(transcript) == "" {
		fail(fmt.Errorf("в записи не слышно речи"))
		return
	}
	ans, err := h.AI.Text(ctx, summarySystem, "Резидент: "+resident+"\nДата: "+date+"\n\nРасшифровка:\n"+transcript)
	if err != nil {
		fail(err)
		return
	}
	var sum map[string]any
	if js := ai.JSONFrom(ans); js == "" || json.Unmarshal([]byte(js), &sum) != nil {
		sum = map[string]any{"summary": strings.TrimSpace(ans)}
	}
	res, _ := json.Marshal(map[string]any{
		"resident": resident, "date": date, "transcript": transcript, "audio": audioID, "summary": sum,
	})
	_ = h.repo.UpdateAIJob(ctx, id, "done", "", res)
}

func (h *PlatformAI) Job(c *gin.Context) {
	if !teamOnly(c) {
		return
	}
	j, err := h.repo.GetAIJob(c.Request.Context(), c.Param("id"))
	if err != nil || j == nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "not_found"})
		return
	}
	c.JSON(http.StatusOK, j)
}

func (h *PlatformAI) Jobs(c *gin.Context) {
	if !teamOnly(c) {
		return
	}
	h.repo.FailStaleAIJobs(c.Request.Context())
	list, err := h.repo.AIJobsOf(c.Request.Context(), c.Query("board"))
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "load_failed"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"jobs": list})
}

// ── Almaty events feed ──
// Every morning the server looks for business events in Almaty and keeps them
// in the club document bs_events_feed; the platform shows it in Мероприятия.

const eventsFeedKey = "bs_events_feed"

func (h *PlatformAI) refreshEvents(ctx context.Context) (int, error) {
	loc := time.FixedZone("Almaty", 5*3600)
	items, err := h.AI.FindEvents(ctx, 21, time.Now().In(loc))
	if err != nil {
		return 0, err
	}
	sort.Slice(items, func(i, j int) bool { return items[i].Date+items[i].Time < items[j].Date+items[j].Time })
	val, _ := json.Marshal(map[string]any{"updated": time.Now().UTC().Format(time.RFC3339), "items": items})
	for try := 0; try < 3; try++ {
		base := 0
		if d, err := h.repo.GetDoc(ctx, "club", eventsFeedKey); err == nil && d != nil {
			base = d.Version
		}
		if _, err = h.repo.PutDoc(ctx, "club", eventsFeedKey, base, string(val), false, "server:events"); err == nil {
			return len(items), nil
		}
	}
	return 0, err
}

// untilNextMorning: time left until the next 08:00 in Almaty (the daily refresh).
func untilNextMorning(now time.Time) time.Duration {
	loc := time.FixedZone("Almaty", 5*3600)
	a := now.In(loc)
	next := time.Date(a.Year(), a.Month(), a.Day(), 8, 0, 0, 0, loc)
	if !next.After(a) {
		next = next.AddDate(0, 0, 1)
	}
	return next.Sub(a)
}

// EventsLoop refreshes the feed at start and every morning at 08:00 Almaty.
func (h *PlatformAI) EventsLoop(ctx context.Context) {
	t := time.NewTimer(2 * time.Minute)
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		if h.AI.Status()["text"] != "" {
			c, cancel := context.WithTimeout(ctx, 5*time.Minute)
			n, err := h.refreshEvents(c)
			cancel()
			log.Printf("platform events: %d found, err=%v", n, err)
		}
		t.Reset(untilNextMorning(time.Now()))
	}
}

// RefreshEvents: POST /ai/events (team) refreshes the feed now.
func (h *PlatformAI) RefreshEvents(c *gin.Context) {
	if !teamOnly(c) {
		return
	}
	ctx, cancel := context.WithTimeout(c.Request.Context(), 5*time.Minute)
	defer cancel()
	n, err := h.refreshEvents(ctx)
	if err != nil {
		c.JSON(http.StatusOK, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"found": n})
}

// OpsList: GET /ops (team) — the journal of club data changes.
func (h *PlatformAI) OpsList(c *gin.Context) {
	if !teamOnly(c) {
		return
	}
	if h.Ops == nil {
		c.JSON(http.StatusOK, gin.H{"ops": []any{}})
		return
	}
	list, err := h.Ops.Ops(c.Request.Context(), 500)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "load_failed"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"ops": list})
}
