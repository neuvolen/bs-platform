package http

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	pg "github.com/bnursik/business_surgery_backend/internal/repository/pg"
	"github.com/gin-gonic/gin"
)

// R63: the file is «Саммари <Имя Фамилия> <ДД.ММ.ГГГГ>.pdf», never «черновик».
func TestR63SummaryName(t *testing.T) {
	for in, want := range map[[2]string]string{
		{"Айдос Онлайнов", "02.10.2026"}: "Саммари Айдос Онлайнов 02.10.2026.pdf",
		{"Айдос Онлайнов", "2026-10-07"}: "Саммари Айдос Онлайнов 07.10.2026.pdf",
		{"Альтаир", "7.10.2026"}:         "Саммари Альтаир 07.10.2026.pdf",
		{"Имя/Фамилия", ""}:              "Саммари ИмяФамилия.pdf",
	} {
		if got := summaryName(in[0], in[1]); got != want {
			t.Errorf("summaryName(%q, %q) = %q, want %q", in[0], in[1], got, want)
		}
	}
}

// R63: delete a call (job, files, board card; tasks stay with the page), the
// board does not take it back from an older copy; WhatsApp: the resident's
// phone, the text and the signed PDF link.
func TestR63DeleteAndShareCall(t *testing.T) {
	repo, db := platformTestRepo(t)
	ctx := context.Background()
	const board, res = "sum-r63", "Айдос Шеринов"
	_, _ = db.Pool.Exec(ctx, `DELETE FROM platform_boards WHERE id = $1`, board)
	_, _ = db.Pool.Exec(ctx, `DELETE FROM resident_channels WHERE name_key = 'айдос шеринов'`)
	_, _ = db.Pool.Exec(ctx, `INSERT INTO resident_channels (name_key, name, channel, phone) VALUES ('айдос шеринов', $1, 'wa', '+7 (701) 000-11-22')`, res)
	defer func() {
		_, _ = db.Pool.Exec(ctx, `DELETE FROM resident_channels WHERE name_key = 'айдос шеринов'`)
		_, _ = db.Pool.Exec(ctx, `DELETE FROM platform_boards WHERE id = $1`, board)
	}()
	h := NewPlatformAI(repo, nil)
	h.KeySecret = []byte("jwt-secret-for-tests-0123456789")

	var sum map[string]any
	if err := json.Unmarshal([]byte(r32eSummary), &sum); err != nil {
		t.Fatal(err)
	}
	audio := pg.PlatformFile{ID: newID(), Name: "Запись разбора " + res + ".webm", Mime: "audio/webm", Data: []byte("OggS")}
	if err := repo.PutFile(ctx, audio, "t"); err != nil {
		t.Fatal(err)
	}
	id := newID()
	if err := repo.CreateAIJob(ctx, pg.AIJob{ID: id, Kind: "call", BoardID: board, Resident: res, Status: "done"}, "t"); err != nil {
		t.Fatal(err)
	}
	meta := map[string]any{"resident": res, "date": "02.10.2026", "file": audio.ID, "audio": audio.ID, "transcript": "Трекер: коротко.",
		"summary": callSumNorm(sum), "sumState": map[string]any{"status": "draft"}}
	j, _ := repo.GetAIJob(ctx, id)
	h.makeSummaryPDF(ctx, j, meta)
	pdfID := csS(meta["summaryPdf"])
	if pdfID == "" {
		t.Fatal("no summary pdf")
	}
	mb, _ := json.Marshal(meta)
	_ = repo.UpdateAIJob(ctx, id, "done", "", mb)
	if f, _ := repo.GetFile(ctx, pdfID); f == nil || f.Name != "Саммари Айдос Шеринов 02.10.2026.pdf" {
		t.Fatalf("pdf name: %+v", f)
	}
	card := callJSON(callCard(id, meta))
	bd, _ := json.Marshal(map[string]any{"id": board, "name": "Разбор", "info": map[string]any{"res": res}, "nodes": []any{}, "links": []any{}, "calls": []any{card}})
	if _, err := repo.PutBoard(ctx, board, 0, bd, "t"); err != nil {
		t.Fatal(err)
	}

	gin.SetMode(gin.TestMode)
	role := "admin"
	r := gin.New()
	r.Use(func(c *gin.Context) { c.Set("role", role); c.Set("userID", "tg:1") })
	r.POST("/ai/calls/:id/share", h.ShareCall)
	r.DELETE("/ai/calls/:id", h.DeleteCall)
	r.GET("/sum/:key", h.PublicSummary)
	r.PUT("/boards/:id", NewPlatformHandler(repo).PutBoard)
	do := func(method, path string, body []byte) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		r.ServeHTTP(w, httptest.NewRequest(method, path, bytes.NewReader(body)))
		return w
	}

	// WhatsApp
	w := do("POST", "/ai/calls/"+id+"/share", nil)
	var sh struct{ Phone, Text, URL, Wa, Name string }
	_ = json.Unmarshal(w.Body.Bytes(), &sh)
	if w.Code != 200 || sh.Phone != "77010001122" || !strings.HasPrefix(sh.Wa, "https://wa.me/77010001122?text=") ||
		!strings.Contains(sh.Text, "Айдос, привет!") || !strings.Contains(sh.Text, "Собрать платёжный календарь") ||
		!strings.Contains(sh.Text, sh.URL) || strings.Contains(sh.Text, "\u2014") || sh.Name != "Саммари Айдос Шеринов 02.10.2026.pdf" {
		t.Fatalf("share: %d %+v", w.Code, sh)
	}
	u, _ := url.Parse(sh.URL)
	if w := do("GET", u.Path, nil); w.Code != 200 || !bytes.HasPrefix(w.Body.Bytes(), []byte("%PDF-")) ||
		!strings.Contains(w.Header().Get("Content-Disposition"), "inline") {
		t.Fatalf("open link: %d %s", w.Code, w.Header().Get("Content-Disposition"))
	}
	if w := do("GET", "/sum/"+id+".0123456789abcdef01234567", nil); w.Code != 404 {
		t.Fatalf("bad signature: %d", w.Code)
	}
	role = "resident"
	if w := do("DELETE", "/ai/calls/"+id, nil); w.Code != 403 {
		t.Fatalf("resident deleted a call: %d", w.Code)
	}
	role = "admin"

	// Delete
	b0, _ := repo.GetBoard(ctx, board)
	if w := do("DELETE", "/ai/calls/"+id, nil); w.Code != 200 {
		t.Fatalf("delete: %d %s", w.Code, w.Body.String())
	}
	if j, _ := repo.GetAIJob(ctx, id); j != nil {
		t.Fatal("job kept")
	}
	for _, f := range []string{audio.ID, pdfID} {
		if repo.FileExists(ctx, f) {
			t.Fatalf("file %s kept", f)
		}
	}
	b1, _ := repo.GetBoard(ctx, board)
	var d1 map[string]any
	_ = json.Unmarshal(b1.Data, &d1)
	if boardCall(b1, id) != nil || !callGone(d1, id) {
		t.Fatalf("board: %s", b1.Data)
	}
	if w := do("GET", u.Path, nil); w.Code != 404 {
		t.Fatalf("link of a deleted call: %d", w.Code)
	}
	// an older copy of the board (the page saving the version it had) does not bring it back
	var old map[string]any
	_ = json.Unmarshal(b0.Data, &old)
	old["name"] = "Разбор 2"
	body, _ := json.Marshal(map[string]any{"version": b1.Version, "data": old})
	if w := do("PUT", "/boards/"+board, body); w.Code != 200 {
		t.Fatalf("put: %d %s", w.Code, w.Body.String())
	}
	b2, _ := repo.GetBoard(ctx, board)
	if boardCall(b2, id) != nil || !strings.Contains(string(b2.Data), "callsGone") || !strings.Contains(string(b2.Data), "Разбор 2") {
		t.Fatalf("old copy brought the call back: %s", b2.Data)
	}
	// a job still running does not put it back either
	if err := h.attachToBoard(ctx, board, card); err != nil || boardCall(func() *pg.PlatformBoard { b, _ := repo.GetBoard(ctx, board); return b }(), id) != nil {
		t.Fatalf("attach after delete: %v", err)
	}
	if w := do("DELETE", "/ai/calls/"+id, nil); w.Code != 404 {
		t.Fatalf("second delete: %d", w.Code)
	}
}

// R63: Альтаир's 07.10 payment (entered before R59) is counted at the start,
// once; the other payments since the cutover are counted when clear.
func TestR63DebtFixAtStart(t *testing.T) {
	e := newOffEnv(t, "server", nil)
	ctx := context.Background()
	meta := pg.NewBotRepo(e.db)
	if _, err := e.db.Pool.Exec(ctx, `UPDATE club_residents SET renew_debt = 50000, rest_entry = 0 WHERE name = 'Альтаир'`); err != nil {
		t.Fatal(err)
	}
	if _, err := e.db.Pool.Exec(ctx, `UPDATE club_residents SET renew_debt = 40000, rest_entry = 0 WHERE name = 'Асет'`); err != nil {
		t.Fatal(err)
	}
	_ = meta.SetMeta(ctx, metaCutover, "done:2026-10-04T10:00:00Z")
	_, _ = e.db.Pool.Exec(ctx, `DELETE FROM club_payments WHERE date >= '2026-10-01'`)
	ins := func(date string, amount int64, cat, res string) {
		if _, err := e.db.Pool.Exec(ctx, `INSERT INTO club_payments (date, income, income_cat, resident, source) VALUES ($1,$2,$3,$4,'server')`, date, amount, cat, res); err != nil {
			t.Fatal(err)
		}
	}
	ins("2026-10-07", 50000, "БХ Трекинг продление", "Альтаир")
	ins("2026-10-06", 40000, "БХ Трекинг продление", "асет")    // clear: counted
	ins("2026-10-06", 15000, "БХ Экспресс разбор", "Асет")      // not membership: left
	ins("2026-10-05", 10000, "БХ Трекинг", "Никто Неизвестный") // no resident: left
	ins("2026-09-20", 30000, "БХ Трекинг продление", "Асет")    // before the cutover: not touched
	debt := func(name string) int {
		return e.n(`SELECT rest_entry + renew_debt FROM club_residents WHERE name = $1`, name)
	}
	refreshed := 0
	DebtFixAtStart(ctx, e.repo, meta, func() { refreshed++ })
	if d := debt("Альтаир"); d != 0 {
		t.Fatalf("Альтаир debt %d", d)
	}
	if d := debt("Асет"); d != 0 {
		t.Fatalf("Асет debt %d", d)
	}
	if n := e.n(`SELECT count(*) FROM club_payments WHERE date >= '2026-10-01' AND applied`); n != 2 || refreshed != 1 {
		t.Fatalf("applied %d, refreshed %d", n, refreshed)
	}
	if n := e.n(`SELECT count(*) FROM club_payments WHERE date = '2026-09-20' AND applied`); n != 0 {
		t.Fatal("a payment before the cutover was counted")
	}
	// again (a restart): nothing more, even with a new debt
	_, _ = e.db.Pool.Exec(ctx, `UPDATE club_residents SET renew_debt = 50000 WHERE name = 'Альтаир'`)
	DebtFixAtStart(ctx, e.repo, meta, func() { refreshed++ })
	if d := debt("Альтаир"); d != 50000 || refreshed != 1 {
		t.Fatalf("second start changed the debt: %d (%d)", d, refreshed)
	}
	_ = time.Now
}
