package http

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"mime"
	"net/http"
	"strings"
	"time"

	"github.com/bnursik/business_surgery_backend/internal/ai"
	pg "github.com/bnursik/business_surgery_backend/internal/repository/pg"
	"github.com/bnursik/business_surgery_backend/internal/tplpdf"
	"github.com/gin-gonic/gin"
)

// R32d: «Саммари разбора» instead of the recording.
//
// Owner: offline residents see no recordings at all; online residents get,
// in place of the recording, a PDF summary of the call made from the
// transcript. The raw recording and the transcript stay with the team:
//
//   - the resident's sync answer carries the board's calls without audio,
//     file and transcript (residentCalls), and a resident's save of the
//     board never touches its calls (keepCalls);
//   - a resident cannot open a call recording by its file id (GetFile);
//   - GET /ai/calls/:id/summary.pdf gives the PDF to the team and to the
//     resident whose board it is. The PDF is made when the call is processed
//     (runCallJob) and kept; for calls processed before, it is made on first
//     open: the summary is asked again from the transcript with the fuller
//     prompt (participants, topics, numbers, quotes), and when the model is
//     not there (quota) from what the call already has.

func numbersOf(v any) []map[string]any {
	out := []map[string]any{}
	arr, _ := v.([]any)
	for _, e := range arr {
		switch y := e.(type) {
		case map[string]any:
			l, _ := y["label"].(string)
			val := fmt.Sprint(y["value"])
			if y["value"] == nil {
				val = ""
			}
			if strings.TrimSpace(val) != "" {
				out = append(out, map[string]any{"label": strings.TrimSpace(l), "value": strings.TrimSpace(val)})
			}
		case string:
			if strings.TrimSpace(y) != "" {
				out = append(out, map[string]any{"label": "", "value": strings.TrimSpace(y)})
			}
		}
	}
	return out
}

// callDocOf: the PDF's content from a call card (callCard's shape, or a
// board's calls[] item made by the page).
func callDocOf(card map[string]any, resident string) *tplpdf.CallDoc {
	s := func(k string) string { v, _ := card[k].(string); return strings.TrimSpace(v) }
	d := &tplpdf.CallDoc{
		Resident: resident, Date: s("date"), Title: s("title"), Summary: s("summary"),
		PointA: s("pointA"), PointB: s("pointB"),
		Participants: strs(card["participants"]), Topics: strs(card["topics"]),
		Problems: strs(card["problems"]), Diagnoses: strs(card["diagnoses"]), Decisions: strs(card["decisions"]),
		Next: strs(card["next"]), Questions: strs(card["questions"]), Quotes: strs(card["quotes"]),
	}
	if q := s("quote"); q != "" && len(d.Quotes) == 0 {
		d.Quotes = []string{q}
	}
	if len(d.Participants) == 0 {
		d.Participants = []string{"Трекер Business Surgery"}
		if resident != "" {
			d.Participants = append(d.Participants, resident+" (резидент)")
		}
	}
	var ck []any
	switch x := card["checklist"].(type) {
	case []any:
		ck = x
	case []map[string]any:
		for _, m := range x {
			ck = append(ck, m)
		}
	}
	for _, k := range checklistOf(ck) {
		t := tplpdf.CallTask{Text: fmt.Sprint(k["text"]), Due: fmt.Sprint(k["due"])}
		if o, ok := k["owner"].(string); ok {
			t.Owner = o
		}
		if t.Owner == "" {
			t.Owner = firstName(resident)
		}
		d.Tasks = append(d.Tasks, t)
	}
	var ns []any
	switch x := card["numbers"].(type) {
	case []any:
		ns = x
	case []map[string]any:
		for _, m := range x {
			ns = append(ns, m)
		}
	}
	for _, n := range numbersOf(ns) {
		d.Numbers = append(d.Numbers, tplpdf.CallNumber{Label: fmt.Sprint(n["label"]), Value: fmt.Sprint(n["value"])})
	}
	callDocV2(d, card, resident) // R32e (callsum_flow.go)
	return d
}

// callDocV2: the R32e sections of a card; an older card gets them from its
// R32d fields (callSumNorm), so every PDF has the same structure.
func callDocV2(d *tplpdf.CallDoc, card map[string]any, resident string) {
	if _, ok := card["plan"]; !ok {
		sum := callSumNorm(callJSON(map[string]any{"summary": card["summary"], "diagnoses": card["diagnoses"],
			"decisions": card["decisions"], "checklist": card["checklist"], "next": card["next"]}))
		for _, k := range []string{"situation", "diagList", "solutions", "plan", "nextMeeting"} {
			card[k] = sum[k]
		}
	}
	d.Draft = csS(card["sumStatus"]) == "draft"
	if s := csS(card["situation"]); s != "" {
		d.Situation = s
	}
	d.RootCause = csS(card["rootCause"])
	for _, g := range csMaps(card["diagList"], "title") {
		d.DiagList = append(d.DiagList, tplpdf.CallDiag{Title: csS(g["title"]), Why: csS(g["why"]), Lib: g["lib"] == true})
	}
	for _, x := range csMaps(card["solutions"], "text") {
		d.Solutions = append(d.Solutions, tplpdf.CallSolution{Text: csS(x["text"]), Tool: csS(x["tool"]), ToolURL: csS(x["toolUrl"])})
	}
	if plan := csMaps(card["plan"], "what"); len(plan) > 0 {
		d.Tasks = nil
		for _, t := range plan {
			who := csS(t["who"])
			if who == "" {
				who = firstName(resident)
			}
			d.Tasks = append(d.Tasks, tplpdf.CallTask{Text: csS(t["what"]), Owner: who, Due: csS(t["due"])})
		}
	}
	for _, m := range csMaps(card["metrics"], "name") {
		d.Metrics = append(d.Metrics, tplpdf.CallMetric{Name: csS(m["name"]), Now: csS(m["now"]), Target: csS(m["target"])})
	}
	d.Homework = strs(card["homework"])
	if nm := csM(card["nextMeeting"]); nm != nil {
		d.NextDate, d.NextAgenda = csS(nm["date"]), strs(nm["agenda"])
	}
}

func firstName(s string) string {
	if f := strings.Fields(s); len(f) > 0 {
		return f[0]
	}
	return "Резидент"
}

// callJSON makes a card's typed lists plain JSON values ([]any, map[string]any).
func callJSON(card map[string]any) map[string]any {
	b, _ := json.Marshal(card)
	out := map[string]any{}
	_ = json.Unmarshal(b, &out)
	return out
}

func summaryName(resident, date string) string {
	n := "Саммари разбора"
	if resident != "" {
		n += " " + resident
	}
	if date != "" {
		n += " " + date
	}
	return strings.Join(strings.Fields(n), " ") + ".pdf"
}

// makeSummaryPDF renders the call's summary and keeps it (meta.summaryPdf).
func (h *PlatformAI) makeSummaryPDF(ctx context.Context, j *pg.AIJob, meta map[string]any) string {
	resident, _ := meta["resident"].(string)
	if resident == "" {
		resident = j.Resident
	}
	card := callJSON(callCard(j.ID, meta))
	pdf, err := tplpdf.RenderCallSummary(callDocOf(card, resident))
	if err != nil {
		log.Printf("platform call %s: summary pdf: %v", j.ID, err)
		return ""
	}
	date, _ := meta["date"].(string)
	f := pg.PlatformFile{ID: newID(), Name: summaryName(resident, date), Mime: "application/pdf", Data: pdf}
	if err := h.repo.PutFile(ctx, f, "server:call"); err != nil {
		log.Printf("platform call %s: summary pdf not kept: %v", j.ID, err)
		return ""
	}
	meta["summaryPdf"] = f.ID
	return f.ID
}

// richSummary: the summary was made with the R32e prompt (callSumPrompt).
func richSummary(meta map[string]any) bool {
	sum, _ := meta["summary"].(map[string]any)
	return callSumV2(sum)
}

// residentName: the calling resident's name ("" when they lost access).
func (h *PlatformAI) residentName(c *gin.Context) string {
	name, active, err := h.repo.ResidentByTg(c.Request.Context(), platformTgID(c))
	if err != nil || !active {
		return ""
	}
	return name
}

// CallSummaryPDF: GET /ai/calls/:id/summary.pdf[?board=<id>]
func (h *PlatformAI) CallSummaryPDF(c *gin.Context) {
	if isLead(c) {
		forbidden(c, "team_only")
		return
	}
	ctx := c.Request.Context()
	id := c.Param("id")
	if !platformIDRe.MatchString(id) {
		c.JSON(http.StatusNotFound, gin.H{"error": "not_found"})
		return
	}
	j, _ := h.repo.GetAIJob(ctx, id)
	boardID := c.Query("board")
	if j != nil && j.BoardID != "" {
		boardID = j.BoardID
	}
	var board *pg.PlatformBoard
	if boardID != "" && platformIDRe.MatchString(boardID) {
		board, _ = h.repo.GetBoard(ctx, boardID)
	}
	if isResident(c) {
		name := h.residentName(c)
		if name == "" || board == nil || board.Deleted || !boardBelongsTo(board, name) {
			forbidden(c, "not_your_call")
			return
		}
		// R32e: an offline resident has no summaries; a draft is the team's
		if offlineFormat(h.repo.ResidentFormat(ctx, name)) {
			forbidden(c, "offline")
			return
		}
		if j != nil && j.Kind == "call" {
			var m map[string]any
			_ = json.Unmarshal(j.Result, &m)
			if callSumStatus(m) != "published" {
				c.JSON(http.StatusNotFound, gin.H{"error": "not_found"})
				return
			}
		} else if card := boardCall(board, id); card != nil && !callVisible(card, nil) {
			c.JSON(http.StatusNotFound, gin.H{"error": "not_found"})
			return
		}
	}
	resident := ""
	if board != nil {
		resident = board.Resident
	}
	serve := func(name string, data []byte) {
		c.Header("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": name}))
		c.Header("Cache-Control", "private, no-cache")
		c.Data(http.StatusOK, "application/pdf", data)
	}
	// A call the page made itself (no job on the server): from the board's card.
	if j == nil || j.Kind != "call" {
		card := boardCall(board, id)
		if card == nil {
			c.JSON(http.StatusNotFound, gin.H{"error": "not_found"})
			return
		}
		pdf, err := tplpdf.RenderCallSummary(callDocOf(card, resident))
		if err != nil {
			c.JSON(http.StatusConflict, gin.H{"error": "no_summary", "detail": "У этого разбора ещё нет итогов"})
			return
		}
		d, _ := card["date"].(string)
		serve(summaryName(resident, d), pdf)
		return
	}
	unlock := csLock(id) // two first opens make it once (and not during a publish)
	defer unlock()
	j, meta := h.jobMeta(ctx, id)
	if j == nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "not_found"})
		return
	}
	if r, _ := meta["resident"].(string); r != "" {
		resident = r
	} else if resident == "" {
		resident = j.Resident
	}
	date, _ := meta["date"].(string)
	if fid, _ := meta["summaryPdf"].(string); fid != "" {
		if f, err := h.repo.GetFile(ctx, fid); err == nil && f != nil {
			serve(summaryName(resident, date), f.Data)
			return
		}
	}
	if meta["summary"] == nil && meta["transcript"] == nil {
		c.JSON(http.StatusConflict, gin.H{"error": "no_summary", "detail": "Разбор ещё обрабатывается"})
		return
	}
	// Backfill: a call processed before R32d gets the fuller summary once.
	if tr, _ := meta["transcript"].(string); strings.TrimSpace(tr) != "" && !richSummary(meta) && h.AI != nil && h.AI.Status()["text"] != "" {
		actx, cancel := context.WithTimeout(ctx, 3*time.Minute)
		ans, err := h.AI.Text(ai.Heavy(actx), callSumPrompt(), "Резидент: "+resident+"\nДата: "+date+"\n\nРасшифровка:\n"+tr)
		cancel()
		var sum map[string]any
		if err == nil {
			if js := ai.JSONFrom(ans); js != "" && json.Unmarshal([]byte(js), &sum) == nil && len(sum) > 0 {
				old, _ := meta["summary"].(map[string]any)
				for k, v := range old {
					if _, ok := sum[k]; !ok {
						sum[k] = v
					}
				}
				meta["summary"] = callSumNorm(sum)
			}
		} else {
			log.Printf("platform call %s: summary backfill: %v", id, err)
		}
	}
	fid := h.makeSummaryPDF(ctx, j, meta)
	if fid == "" {
		c.JSON(http.StatusConflict, gin.H{"error": "no_summary", "detail": "У этого разбора ещё нет итогов"})
		return
	}
	b, _ := json.Marshal(meta)
	_ = h.repo.UpdateAIJob(ctx, j.ID, j.Status, j.Error, b)
	f, err := h.repo.GetFile(ctx, fid)
	if err != nil || f == nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "store_failed"})
		return
	}
	serve(summaryName(resident, date), f.Data)
}

// boardCall: one item of a board's calls[].
func boardCall(b *pg.PlatformBoard, id string) map[string]any {
	if b == nil || b.Deleted {
		return nil
	}
	var data map[string]any
	if json.Unmarshal(b.Data, &data) != nil {
		return nil
	}
	calls, _ := data["calls"].([]any)
	for _, c := range calls {
		if m, ok := c.(map[string]any); ok && m["id"] == id {
			return m
		}
	}
	return nil
}

// callPrivate: what of a call only the team sees.
var callPrivate = []string{"audio", "file", "transcript"}

// residentCalls: the board as a resident gets it, without recordings and transcripts.
func residentCalls(data json.RawMessage) json.RawMessage {
	if len(data) == 0 || !strings.Contains(string(data), `"calls"`) {
		return data
	}
	var m map[string]json.RawMessage
	if json.Unmarshal(data, &m) != nil {
		return data
	}
	var calls []map[string]any
	if json.Unmarshal(m["calls"], &calls) != nil {
		return data
	}
	for _, c := range calls {
		for _, k := range callPrivate {
			delete(c, k)
		}
	}
	b, _ := json.Marshal(calls)
	m["calls"] = b
	out, err := json.Marshal(m)
	if err != nil {
		return data
	}
	return out
}

// keepCalls: a resident's save keeps the board's calls as the server has them.
func keepCalls(cur, next json.RawMessage) (json.RawMessage, error) {
	var nm map[string]json.RawMessage
	if err := json.Unmarshal(next, &nm); err != nil {
		return nil, err
	}
	var cm map[string]json.RawMessage
	_ = json.Unmarshal(cur, &cm)
	for _, k := range []string{"calls", "pendingCalls"} {
		if v, ok := cm[k]; ok {
			nm[k] = v
		} else {
			delete(nm, k)
		}
	}
	delete(nm, "callsOff") // R32e: the resident's sync flag, not board data
	return json.Marshal(nm)
}

// callRecordingName: a file a resident may not open (platform_calls.go names).
func callRecordingName(name string) bool {
	for _, p := range []string{"Запись разбора", "Расшифровка разбора", "Созвон ", "Саммари разбора", "Черновик · "} {
		if strings.HasPrefix(name, p) {
			return true
		}
	}
	return false
}
