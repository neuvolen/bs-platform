package http

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	pg "github.com/bnursik/business_surgery_backend/internal/repository/pg"
)

// R65: созвон на доске: войти, показать экран (показывает нажавший последним,
// прежний перестаёт сам), письма соединения доходят только участнику,
// молчащая вкладка выходит и забирает с собой показ.
func TestR65CallHub(t *testing.T) {
	h := newCallHub()
	now := time.Date(2026, 10, 9, 12, 0, 0, 0, time.UTC)
	h.now = func() time.Time { return now }
	tr := callMember{Key: "tg:1|ta", ID: "tg:1", Name: "Рустам", Role: "admin", Team: true}
	rs := callMember{Key: "tg:2|tb", ID: "tg:2", Name: "Альтаир", Role: "resident"}
	const b = "board-1"

	if v, _ := h.Op(b, tr, "peek"); len(v.Members) != 0 || v.In {
		t.Fatalf("empty room: %+v", v)
	}
	if _, ok := h.Op(b, tr, "present"); ok {
		t.Fatal("present without join must fail")
	}
	h.Op(b, tr, "join")
	h.OpWho(b, tr, "auto", "") // R75 call: резидент без лобби, если команда так включила
	v, _ := h.Op(b, rs, "join")
	if len(v.Members) != 2 || !v.In {
		t.Fatalf("two members: %+v", v)
	}
	// трекер показывает, потом резидент нажимает «Показать мой экран»: показывает он
	if v, _ = h.Op(b, tr, "present"); v.Presenter == nil || v.Presenter.Key != tr.Key {
		t.Fatalf("tracker presents: %+v", v.Presenter)
	}
	if v, _ = h.Op(b, rs, "present"); v.Presenter == nil || v.Presenter.Name != "Альтаир" {
		t.Fatalf("resident takes over: %+v", v.Presenter)
	}
	// «Остановить» у прежнего показывающего не снимает чужой показ
	if v, _ = h.Op(b, tr, "unpresent"); v.Presenter == nil || v.Presenter.Key != rs.Key {
		t.Fatalf("stale unpresent: %+v", v.Presenter)
	}
	// одним нажатием обратно
	if v, _ = h.Op(b, tr, "present"); v.Presenter.Key != tr.Key {
		t.Fatalf("switch back: %+v", v.Presenter)
	}
	// запись видна обоим
	if v, _ = h.Op(b, tr, "rec"); v.Rec == nil || v.Rec.Name != "Рустам" {
		t.Fatalf("rec mark: %+v", v.Rec)
	}
	// письма
	if !h.Send(b, tr.Key, rs.Key, callMsg{Kind: "offer", Data: json.RawMessage(`{"sdp":"x"}`)}) {
		t.Fatal("send to a member")
	}
	if h.Send(b, tr.Key, "tg:9|zz", callMsg{Kind: "offer"}) {
		t.Fatal("send to a stranger must fail")
	}
	if _, m := h.take(b, rs.Key); len(m) != 1 || m[0].From != tr.Key || m[0].Kind != "offer" {
		t.Fatalf("mailbox: %+v", m)
	}
	if _, m := h.take(b, rs.Key); len(m) != 0 {
		t.Fatalf("mailbox drained: %+v", m)
	}
	// трекер замолчал (вкладку закрыли): выходит, показ и запись уходят с ним
	now = now.Add(10 * time.Second)
	h.take(b, rs.Key) // резидент на связи
	now = now.Add(15 * time.Second)
	v, _ = h.Op(b, rs, "peek")
	if len(v.Members) != 1 || v.Presenter != nil || v.Rec != nil {
		t.Fatalf("silent member expires: %+v", v)
	}
	// открытый поток держит вкладку в созвоне
	h.Op(b, tr, "join")
	_, stop := h.subscribe(b, tr.Key)
	now = now.Add(time.Minute)
	if v, _ = h.Op(b, tr, "peek"); !v.In {
		t.Fatal("a live stream keeps the member")
	}
	stop()
	h.Op(b, tr, "leave")
	h.Op(b, rs, "leave")
	if len(h.rooms) != 0 {
		t.Fatalf("room gone: %+v", h.rooms)
	}
}

// R65: запись удаляется после саммари и PDF; без саммари ждёт 7 дней.
func TestR65RecordingSweep(t *testing.T) {
	repo, db := platformTestRepo(t)
	ctx := context.Background()
	const board, res = "rec-r65", "Альтаир Тестов"
	_, _ = db.Pool.Exec(ctx, `DELETE FROM platform_boards WHERE id = $1`, board)
	defer db.Pool.Exec(ctx, `DELETE FROM platform_boards WHERE id = $1`, board) //nolint:errcheck
	h := NewPlatformAI(repo, nil)

	var sum map[string]any
	if err := json.Unmarshal([]byte(r32eSummary), &sum); err != nil {
		t.Fatal(err)
	}
	mk := func(status string, withSum bool, age time.Duration) (string, string) {
		audio := pg.PlatformFile{ID: newID(), Name: "Запись разбора " + res + ".webm", Mime: "audio/webm", Data: make([]byte, 3<<20)}
		if err := repo.PutFile(ctx, audio, "t"); err != nil {
			t.Fatal(err)
		}
		id := newID()
		if err := repo.CreateAIJob(ctx, pg.AIJob{ID: id, Kind: "call", BoardID: board, Resident: res, Status: status}, "t"); err != nil {
			t.Fatal(err)
		}
		meta := map[string]any{"resident": res, "date": "07.10.2026", "file": audio.ID, "audio": audio.ID}
		if withSum {
			meta["transcript"] = "Трекер: коротко."
			meta["summary"] = callSumNorm(sum)
			j, _ := repo.GetAIJob(ctx, id)
			if h.makeSummaryPDF(ctx, j, meta) == "" {
				t.Fatal("no pdf")
			}
		}
		mb, _ := json.Marshal(meta)
		_ = repo.UpdateAIJob(ctx, id, status, "", mb)
		if age > 0 {
			_, _ = db.Pool.Exec(ctx, `UPDATE platform_ai_jobs SET created_at = now() - make_interval(secs => $2), updated_at = now() - make_interval(secs => $2) WHERE id = $1`, id, age.Seconds())
		}
		return id, audio.ID
	}
	done, doneA := mk("done", true, 0)
	fresh, freshA := mk("error", false, 2*24*time.Hour)
	old, oldA := mk("error", false, 8*24*time.Hour)
	busy, busyA := mk("running", true, 0)

	_, dm := h.jobMeta(ctx, done)
	card := callJSON(callCard(done, map[string]any{"date": "07.10.2026", "audio": doneA, "file": doneA, "summary": dm["summary"], "summaryPdf": dm["summaryPdf"]}))
	bd, _ := json.Marshal(map[string]any{"id": board, "name": "Разбор", "info": map[string]any{"res": res}, "nodes": []any{}, "links": []any{}, "calls": []any{card}})
	if _, err := repo.PutBoard(ctx, board, 0, bd, "t"); err != nil {
		t.Fatal(err)
	}

	n, freed := h.CallRecSweep(ctx, time.Now())
	if n < 2 || freed < 6<<20 {
		t.Fatalf("sweep: %d deleted, %d bytes", n, freed)
	}
	if repo.FileExists(ctx, doneA) || repo.FileExists(ctx, oldA) {
		t.Fatal("recordings with a summary and older than 7 days must go")
	}
	if !repo.FileExists(ctx, freshA) || !repo.FileExists(ctx, busyA) {
		t.Fatal("a failed recording waits 7 days for a retry; a running job keeps its recording")
	}
	_, m := h.jobMeta(ctx, done)
	if m["audio"] != nil || m["file"] != nil || csS(m["transcript"]) == "" || len(csM(m["summary"])) == 0 || csS(m["summaryPdf"]) == "" {
		t.Fatalf("done job meta: %v", m)
	}
	if rd := csM(m["recDeleted"]); rd["why"] != "summary" {
		t.Fatalf("recDeleted: %v", m["recDeleted"])
	}
	if _, m = h.jobMeta(ctx, old); csM(m["recDeleted"])["why"] != "7 days without summary" {
		t.Fatalf("old job: %v", m)
	}
	if _, m = h.jobMeta(ctx, fresh); m["audio"] != freshA {
		t.Fatalf("fresh job keeps audio: %v", m)
	}
	b, _ := repo.GetBoard(ctx, board)
	var data struct{ Calls []map[string]any }
	_ = json.Unmarshal(b.Data, &data)
	if len(data.Calls) != 1 || data.Calls[0]["audio"] != nil || csM(data.Calls[0]["recDeleted"])["why"] != "summary" {
		t.Fatalf("board card: %s", b.Data)
	}
	// второй проход ничего не трогает
	if n, _ = h.CallRecSweep(ctx, time.Now()); n != 0 {
		t.Fatalf("second sweep deleted %d", n)
	}
	for _, id := range []string{done, fresh, old, busy} {
		_ = repo.DeleteCallJob(ctx, id)
	}
	_ = repo.DeleteFile(ctx, freshA)
	_ = repo.DeleteFile(ctx, busyA)
}
