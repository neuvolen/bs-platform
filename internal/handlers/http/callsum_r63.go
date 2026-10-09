package http

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"mime"
	"net/http"
	"net/url"
	"strings"
	"time"

	pg "github.com/bnursik/business_surgery_backend/internal/repository/pg"
	"github.com/bnursik/business_surgery_backend/internal/tplpdf"
	"github.com/gin-gonic/gin"
)

// R63: «Добавь возможность удалять саммари разбора: некоторые случайно
// записались и были на 2 минуты» and «кнопка "Отправить в WhatsApp"».
//
//   DELETE /ai/calls/:id[?board=<id>]  the call goes away: the job, the
//          recording, the transcript, the summary PDF and the board's card
//          (the board remembers the id in callsGone, so an older copy of the
//          board or a job still running does not bring it back). The tasks
//          the summary put on the board are removed by the page, only when
//          the team ticks «Удалить также задачи из этого саммари».
//   POST   /ai/calls/:id/share     {phone, text, url, name}: the resident's
//          WhatsApp phone (resident_channels; "" = the contact picker), a
//          short message and the PDF by a signed open link.
//   GET    /sum/:key               the PDF by that link (<id>.<sig>), without
//          the draft mark, named «Саммари <Имя Фамилия> <ДД.ММ.ГГГГ>.pdf».

const callsGoneMax = 300

// callGone: the board's data says the team deleted this call.
func callGone(data map[string]any, id any) bool {
	sid, _ := id.(string)
	if sid == "" {
		return false
	}
	gone, _ := data["callsGone"].([]any)
	for _, g := range gone {
		if g == sid {
			return true
		}
	}
	return false
}

// dropGoneCalls: a board saved from an older copy loses the deleted calls
// and keeps the server's callsGone.
func dropGoneCalls(gone []string, next json.RawMessage) json.RawMessage {
	if len(gone) == 0 || len(next) == 0 {
		return next
	}
	var nm map[string]json.RawMessage
	if json.Unmarshal(next, &nm) != nil || nm == nil {
		return next
	}
	g := map[string]bool{}
	for _, id := range gone {
		g[id] = true
	}
	if raw, ok := nm["calls"]; ok {
		var calls []json.RawMessage
		if json.Unmarshal(raw, &calls) == nil {
			keep := make([]json.RawMessage, 0, len(calls))
			for _, c := range calls {
				var x struct {
					ID string `json:"id"`
				}
				if json.Unmarshal(c, &x) == nil && g[x.ID] {
					continue
				}
				keep = append(keep, c)
			}
			nm["calls"], _ = json.Marshal(keep)
		}
	}
	nm["callsGone"], _ = json.Marshal(gone)
	out, err := json.Marshal(nm)
	if err != nil {
		return next
	}
	return out
}

// dropBoardCall removes the call's card from the board and remembers its id.
// It returns the files the card pointed at.
func (h *PlatformAI) dropBoardCall(ctx context.Context, boardID, id string) (bool, []string, error) {
	for try := 0; try < 6; try++ {
		b, err := h.repo.GetBoard(ctx, boardID)
		if err != nil || b == nil || b.Deleted {
			return false, nil, err
		}
		var data map[string]any
		if json.Unmarshal(b.Data, &data) != nil || data == nil {
			return false, nil, errors.New("board data")
		}
		var files []string
		found := false
		calls, _ := data["calls"].([]any)
		keep := make([]any, 0, len(calls))
		for _, x := range calls {
			if m, ok := x.(map[string]any); ok && m["id"] == id {
				found = true
				for _, k := range []string{"audio", "file", "summaryPdf"} {
					if s, _ := m[k].(string); s != "" {
						files = append(files, s)
					}
				}
				continue
			}
			keep = append(keep, x)
		}
		data["calls"] = keep
		if pend, ok := data["pendingCalls"].([]any); ok {
			kp := make([]any, 0, len(pend))
			for _, p := range pend {
				if p != id {
					kp = append(kp, p)
				} else {
					found = true
				}
			}
			data["pendingCalls"] = kp
		}
		if !callGone(data, id) {
			gone, _ := data["callsGone"].([]any)
			gone = append(gone, id)
			if len(gone) > callsGoneMax {
				gone = gone[len(gone)-callsGoneMax:]
			}
			data["callsGone"] = gone
		}
		raw, _ := json.Marshal(data)
		if _, err = h.repo.PutBoard(ctx, boardID, b.Version, raw, "server:call-delete"); err == nil {
			return found, files, nil
		}
		if !errors.Is(err, pg.ErrPlatformConflict) {
			return found, files, err
		}
		time.Sleep(150 * time.Millisecond)
	}
	return false, nil, pg.ErrPlatformConflict
}

// DeleteCall: DELETE /ai/calls/:id[?board=<id>] (the team).
func (h *PlatformAI) DeleteCall(c *gin.Context) {
	if !teamOnly(c) {
		return
	}
	id := c.Param("id")
	if !platformIDRe.MatchString(id) {
		c.JSON(http.StatusNotFound, gin.H{"error": "not_found"})
		return
	}
	defer csLock(id)()
	ctx := c.Request.Context()
	j, meta := h.jobMeta(ctx, id)
	if j != nil && j.Kind != "call" {
		c.JSON(http.StatusNotFound, gin.H{"error": "not_found"})
		return
	}
	boardID := c.Query("board")
	if j != nil && j.BoardID != "" {
		boardID = j.BoardID
	}
	var files []string
	if j != nil {
		for _, k := range []string{"file", "audio", "summaryPdf"} {
			if s := csS(meta[k]); s != "" {
				files = append(files, s)
			}
		}
	}
	onBoard := false
	if boardID != "" && platformIDRe.MatchString(boardID) {
		found, more, err := h.dropBoardCall(ctx, boardID, id)
		if err != nil {
			log.Printf("callsum %s: delete: board %s: %v", id, boardID, err)
			c.JSON(http.StatusInternalServerError, gin.H{"error": "store_failed", "detail": "Доска не сохранилась, попробуйте ещё раз"})
			return
		}
		onBoard = found
		files = append(files, more...)
	}
	if j == nil && !onBoard {
		c.JSON(http.StatusNotFound, gin.H{"error": "not_found", "detail": "Разбор уже удалён"})
		return
	}
	seen, n := map[string]bool{}, 0
	for _, f := range files {
		if f == "" || seen[f] || !platformIDRe.MatchString(f) {
			continue
		}
		seen[f] = true
		if err := h.repo.DeleteFile(ctx, f); err != nil {
			log.Printf("callsum %s: delete file: %v", id, err)
			continue
		}
		n++
	}
	if j != nil {
		if err := h.repo.DeleteCallJob(ctx, id); err != nil {
			log.Printf("callsum %s: delete job: %v", id, err)
			c.JSON(http.StatusInternalServerError, gin.H{"error": "store_failed"})
			return
		}
	}
	log.Printf("callsum %s: deleted by %s (board card %v, %d files)", id, platformUser(c), onBoard, n)
	c.JSON(http.StatusOK, gin.H{"ok": true, "files": n, "board": onBoard})
}

// ── WhatsApp ──

func (h *PlatformAI) sumShareSig(id string) string {
	m := hmac.New(sha256.New, h.KeySecret)
	m.Write([]byte("callsum-share|" + id))
	return hex.EncodeToString(m.Sum(nil))[:24]
}

// SumShareURL: the open link of a call's summary PDF.
func (h *PlatformAI) SumShareURL(id string) string {
	base := publicBase()
	if base == "" {
		base = "https://app.bxclub.kz"
	}
	return base + "/sum/" + id + "." + h.sumShareSig(id)
}

// callShareText: the WhatsApp message: hello, the meeting, the root cause,
// up to three steps of the plan, the PDF link.
func callShareText(resident, date string, sum map[string]any, link string) string {
	var b strings.Builder
	b.WriteString(firstName(resident) + ", привет! Саммари нашего разбора")
	if d := summaryDate(date); d != "" {
		b.WriteString(" · " + d)
	}
	if t := strings.TrimSpace(csS(sum["title"])); t != "" {
		b.WriteString("\n\n«" + t + "»")
	}
	if rc := strings.TrimSpace(csS(sum["rootCause"])); rc != "" {
		b.WriteString("\n\nГлавное: " + rc)
	}
	n := 0
	for _, p := range csMaps(sum["plan"], "what") {
		what := strings.TrimSpace(csS(p["what"]))
		if what == "" {
			continue
		}
		if n == 0 {
			b.WriteString("\n\nПлан на 10 дней:")
		}
		n++
		b.WriteString(fmt.Sprintf("\n%d. %s", n, what))
		if due := strings.TrimSpace(csS(p["due"])); due != "" {
			b.WriteString(" (до " + due + ")")
		}
		if n == 3 {
			break
		}
	}
	b.WriteString("\n\nВсё целиком в PDF: диагнозы, решения, план, метрики и домашнее задание:\n" + link)
	return strings.NewReplacer("\u2014", "-", "\u2013", "-").Replace(b.String())
}

// ShareCall: POST /ai/calls/:id/share (the team).
func (h *PlatformAI) ShareCall(c *gin.Context) {
	if !teamOnly(c) {
		return
	}
	j, meta, ok := h.callJobFor(c)
	if !ok {
		return
	}
	ctx := c.Request.Context()
	resident := h.residentOfJob(ctx, j, meta)
	sum := csM(meta["summary"])
	if !callSumV2(sum) {
		sum = callSumNorm(callJSON(sum))
	}
	link := h.SumShareURL(j.ID)
	phone := h.repo.ResidentPhone(ctx, resident)
	text := callShareText(resident, csS(meta["date"]), sum, link)
	wa := "https://wa.me/"
	if phone != "" {
		wa += phone
	}
	wa += "?text=" + url.QueryEscape(text)
	log.Printf("callsum %s: WhatsApp link by %s (phone %v)", j.ID, platformUser(c), phone != "")
	c.JSON(http.StatusOK, gin.H{"ok": true, "phone": phone, "text": text, "url": link, "wa": wa,
		"name": summaryName(resident, csS(meta["date"]))})
}

// PublicSummary: GET /sum/:key, the PDF by the signed link.
func (h *PlatformAI) PublicSummary(c *gin.Context) {
	key := strings.TrimSuffix(c.Param("key"), ".pdf")
	id, sig, _ := strings.Cut(key, ".")
	if !platformIDRe.MatchString(id) || len(h.KeySecret) == 0 || !hmac.Equal([]byte(sig), []byte(h.sumShareSig(id))) {
		c.String(http.StatusNotFound, "Ссылка не найдена")
		return
	}
	ctx := c.Request.Context()
	j, meta := h.jobMeta(ctx, id)
	if j == nil || j.Kind != "call" || meta["summary"] == nil {
		c.String(http.StatusNotFound, "Саммари удалено или ещё не готово")
		return
	}
	resident := h.residentOfJob(ctx, j, meta)
	name := summaryName(resident, csS(meta["date"]))
	var data []byte
	if callSumStatus(meta) == "published" {
		if fid := csS(meta["summaryPdf"]); fid != "" {
			if f, err := h.repo.GetFile(ctx, fid); err == nil && f != nil {
				data = f.Data
			}
		}
	}
	if data == nil { // a draft the team sent: the same PDF without the draft mark
		card := callJSON(callCard(j.ID, meta))
		delete(card, "sumStatus")
		pdf, err := tplpdf.RenderCallSummary(callDocOf(card, resident))
		if err != nil {
			log.Printf("callsum %s: open link pdf: %v", id, err)
			c.String(http.StatusInternalServerError, "PDF не получился, попробуйте позже")
			return
		}
		data = pdf
	}
	c.Header("Content-Disposition", mime.FormatMediaType("inline", map[string]string{"filename": name}))
	c.Header("Cache-Control", "private, no-cache")
	c.Header("X-Robots-Tag", "noindex, nofollow")
	c.Data(http.StatusOK, "application/pdf", data)
}
