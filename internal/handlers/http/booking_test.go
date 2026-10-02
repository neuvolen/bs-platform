package http

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/bnursik/business_surgery_backend/internal/bot"
	pg "github.com/bnursik/business_surgery_backend/internal/repository/pg"
	"github.com/gin-gonic/gin"
)

func TestDue24(t *testing.T) {
	at := func(d, h, m int) time.Time { return time.Date(2026, 10, d, h, m, 0, 0, almaty) }
	for _, c := range []struct{ start, want time.Time }{
		{at(5, 15, 0), at(4, 15, 0)},
		{at(5, 9, 0), at(4, 9, 0)},
		{at(5, 8, 0), at(4, 10, 0)},  // 08:00 the day before: 10:00
		{at(5, 23, 0), at(5, 10, 0)}, // 23:00 the day before: 10:00 that day
		{at(5, 22, 0), at(5, 10, 0)},
		{at(5, 0, 30), at(4, 10, 0)},
	} {
		if got := due24(c.start); !got.Equal(c.want) {
			t.Errorf("due24(%v) = %v, want %v", c.start, got, c.want)
		}
	}
	if s := whenRu(at(5, 11, 0)); s != "пн, 5 октября, 11:00" {
		t.Fatal(s)
	}
	if s := tenge(50000); s != "50 000 ₸" {
		t.Fatal(s)
	}
}

func TestBooking(t *testing.T) {
	e := newBotEnv(t)
	ctx := context.Background()
	if _, err := e.db.Pool.Exec(ctx, `DELETE FROM platform_docs WHERE scope='club' AND key IN ('bs_crm','bs_slots')`); err != nil {
		t.Fatal(err)
	}
	repo := pg.NewPlatformRepo(e.db)
	f := NewLeadFunnel(repo, e.svc.SendMessageKB, []int64{111})
	g := NewAppGateway(testBotToken, "http://127.0.0.1:1/exec")
	g.Funnel = f
	gin.SetMode(gin.TestMode)
	r := gin.New()
	NewAppGatewayModule(g).Register(r)

	now := time.Now().In(almaty)
	day := func(d, h int) string {
		x := now.AddDate(0, 0, d)
		return time.Date(x.Year(), x.Month(), x.Day(), h, 0, 0, 0, almaty).Format(time.RFC3339)
	}
	s1, _ := parseSlotTime(day(3, 11))
	// В доке старая цена 30 000: сервер отдаёт новую, 50 000.
	slots := map[string]any{"price": 30000, "slots": []any{
		map[string]any{"id": "s2", "start": day(5, 15), "dur": 60, "format": "офлайн", "place": "Алматы, Абая 10", "link": "", "status": "free"},
		map[string]any{"id": "s1", "start": day(3, 11), "dur": 60, "format": "онлайн", "place": "", "link": "https://meet.example/x", "status": "free"},
		map[string]any{"id": "s3", "start": day(4, 12), "dur": 60, "format": "онлайн", "status": "blocked"},
		map[string]any{"id": "s4", "start": day(-1, 12), "dur": 60, "format": "онлайн", "status": "free"},
		map[string]any{"id": "s5", "start": day(30, 12), "dur": 60, "format": "онлайн", "status": "free"},
		map[string]any{"id": "s6", "start": day(6, 12), "dur": 60, "format": "онлайн", "status": "booked",
			"booking": map[string]any{"tgId": 9100, "name": "Другой", "phone": "+7 700", "paid": false, "reminded": []any{}}},
	}}
	raw, _ := json.Marshal(slots)
	if _, err := repo.PutDoc(ctx, "club", "bs_slots", 0, string(raw), false, "test"); err != nil {
		t.Fatal(err)
	}

	tgq := func(id int64) string {
		return url.Values{"_tg": {makeInitData(testBotToken, id, fmt.Sprintf("Имя%d", id), time.Now())}}.Encode()
	}
	call := func(method, path string, id int64, body any) (int, map[string]any) {
		var b []byte
		if body != nil {
			b, _ = json.Marshal(body)
		}
		w := httptest.NewRecorder()
		r.ServeHTTP(w, httptest.NewRequest(method, path+"?"+tgq(id), bytes.NewReader(b)))
		var out map[string]any
		_ = json.Unmarshal(w.Body.Bytes(), &out)
		return w.Code, out
	}
	ids := func(out map[string]any) string {
		var s []string
		for _, x := range out["slots"].([]any) {
			s = append(s, x.(map[string]any)["id"].(string))
		}
		return strings.Join(s, ",")
	}

	code, out := call("GET", "/api/v1/app/slots", 7001, nil)
	if code != 200 || ids(out) != "s1,s2" || out["mine"] != nil || out["price"] != float64(50000) || out["kaspiLink"] != bot.KaspiLink {
		t.Fatalf("slots: %d %v", code, out)
	}
	if b, _ := json.Marshal(out); strings.Contains(string(b), "booking") || strings.Contains(string(b), "meet.example") || strings.Contains(string(b), "Другой") {
		t.Fatalf("slots leak: %s", b)
	}

	// Six people take s1 at once: exactly one gets it.
	var wg sync.WaitGroup
	var mu sync.Mutex
	codes := map[int64]map[string]any{}
	for i := int64(7001); i <= 7006; i++ {
		wg.Add(1)
		go func(id int64) {
			defer wg.Done()
			c, o := call("POST", "/api/v1/app/book", id, map[string]any{"slotId": "s1", "phone": fmt.Sprintf("+7 701 %d", id), "niche": "Кофейни", "question": "Почему нет прибыли?"})
			mu.Lock()
			o["_code"] = c
			codes[id] = o
			mu.Unlock()
		}(i)
	}
	wg.Wait()
	var winner int64
	for id, o := range codes {
		switch {
		case o["ok"] == true && o["_code"] == 200:
			if winner != 0 {
				t.Fatalf("two winners: %v", codes)
			}
			winner = id
		case o["error"] == "taken" && o["_code"] == 409:
		default:
			t.Fatalf("book %d: %v", id, o)
		}
	}
	if winner == 0 {
		t.Fatalf("nobody got the slot: %v", codes)
	}
	bk := codes[winner]["booking"].(map[string]any)
	if bk["price"] != float64(50000) || bk["kaspiLink"] != bot.KaspiLink || bk["slot"].(map[string]any)["link"] != "https://meet.example/x" {
		t.Fatalf("booking answer: %v", bk)
	}
	m := e.msgsTo(winner)
	if len(m) != 1 || !strings.Contains(m[0]["text"].(string), whenRu(s1)) || !strings.Contains(m[0]["text"].(string), "meet.example") {
		t.Fatalf("confirmation: %v", m)
	}
	kbs, _ := json.Marshal(m[0]["reply_markup"])
	if !strings.Contains(string(kbs), bot.KaspiLink) || !strings.Contains(string(kbs), "?p=razbor") || !strings.Contains(string(kbs), "web_app") {
		t.Fatalf("confirmation buttons: %s", kbs)
	}
	var note string
	for _, a := range e.msgsTo(111) {
		if s := a["text"].(string); strings.HasPrefix(s, "📅 Запись на разбор") {
			note = s
		}
	}
	if !strings.Contains(note, fmt.Sprintf("+7 701 %d", winner)) || !strings.Contains(note, "Почему нет прибыли?") || !strings.Contains(note, s1.In(almaty).Format("15:04")) {
		t.Fatalf("admin note: %q", note)
	}
	l := crmLead(t, repo, winner)
	if l == nil || l["col"] != "meet" || l["nextAt"] != s1.In(almaty).Format("2006-01-02") || l["phone"] != fmt.Sprintf("+7 701 %d", winner) || l["niche"] != "Кофейни" || l["razborSlot"] != "s1" {
		t.Fatalf("lead: %v", l)
	}
	other := int64(7001)
	if winner == 7001 {
		other = 7002
	}
	for _, c := range []struct {
		id   int64
		slot string
		want string
		code int
	}{
		{winner, "s2", "already", 409},
		{other, "s4", "past", 409},
		{other, "s3", "taken", 409},
		{other, "s6", "taken", 409},
		{other, "nope", "not_found", 404},
	} {
		if code, out := call("POST", "/api/v1/app/book", c.id, map[string]any{"slotId": c.slot}); code != c.code || out["error"] != c.want {
			t.Fatalf("book %s by %d: %d %v", c.slot, c.id, code, out)
		}
	}
	if code, _ := call("POST", "/api/v1/app/book", other, map[string]any{"phone": "1"}); code != 400 {
		t.Fatalf("no slot: %d", code)
	}

	code, out = call("GET", "/api/v1/app/slots", winner, nil)
	mine, _ := out["mine"].(map[string]any)
	if code != 200 || ids(out) != "s2" || mine == nil || mine["slot"].(map[string]any)["id"] != "s1" || mine["paid"] != false ||
		mine["slot"].(map[string]any)["link"] != "https://meet.example/x" || mine["question"] != "Почему нет прибыли?" {
		t.Fatalf("mine: %d %v", code, out)
	}
	if _, out := call("GET", "/api/v1/app/slots", other, nil); out["mine"] != nil {
		t.Fatalf("someone else's booking: %v", out)
	}

	// Cancel: only the owner; the slot is free again.
	if code, out := call("POST", "/api/v1/app/book/cancel", other, map[string]any{"slotId": "s1"}); code != 404 || out["error"] != "not_found" {
		t.Fatalf("cancel by other: %d %v", code, out)
	}
	if code, out := call("POST", "/api/v1/app/book/cancel", winner, map[string]any{"slotId": "s1"}); code != 200 || out["ok"] != true {
		t.Fatalf("cancel: %d %v", code, out)
	}
	if _, out := call("GET", "/api/v1/app/slots", winner, nil); ids(out) != "s1,s2" || out["mine"] != nil {
		t.Fatalf("after cancel: %v", out)
	}
	l = crmLead(t, repo, winner)
	lg, _ := json.Marshal(l["log"])
	if l["razborSlot"] != nil || l["nextAt"] != "" || !strings.Contains(string(lg), "Отменил запись на разбор") {
		t.Fatalf("lead after cancel: %v", l)
	}
	cancelled := false
	for _, a := range e.msgsTo(111) {
		if strings.HasPrefix(a["text"].(string), "❌ Отмена разбора") {
			cancelled = true
		}
	}
	if !cancelled {
		t.Fatal("no cancel note")
	}

	// Book again; and a second person takes s2.
	if code, out := call("POST", "/api/v1/app/book", winner, map[string]any{"slotId": "s1", "phone": "x"}); code != 200 {
		t.Fatalf("rebook: %d %v", code, out)
	}
	if code, out := call("POST", "/api/v1/app/book", other, map[string]any{"slotId": "s2", "phone": "+7 702"}); code != 200 {
		t.Fatalf("book s2: %d %v", code, out)
	}

	// Reminders.
	base := len(e.msgsTo(winner))
	admins := len(e.msgsTo(111))
	f.now = func() time.Time { return s1.Add(-48 * time.Hour) }
	if n := f.RemindOnce(ctx); n != 0 {
		t.Fatalf("too early: %d", n)
	}
	f.now = func() time.Time { return s1.Add(-24*time.Hour + time.Minute) }
	if n := f.RemindOnce(ctx); n != 1 {
		t.Fatalf("24h: %d", n)
	}
	if n := f.RemindOnce(ctx); n != 0 {
		t.Fatalf("24h again: %d", n)
	}
	m = e.msgsTo(winner)
	if len(m) != base+1 || !strings.Contains(m[base]["text"].(string), "завтра в 11:00") || !strings.Contains(m[base]["text"].(string), "Kaspi") {
		t.Fatalf("24h text: %v", m[base:])
	}
	if kbs, _ := json.Marshal(m[base]["reply_markup"]); !strings.Contains(string(kbs), bot.KaspiLink) {
		t.Fatalf("24h pay button: %s", kbs)
	}
	// paid by now: no pay button
	_ = f.mutateIn(ctx, "club", "bs_slots", "test", func(doc map[string]any) bool {
		for _, v := range doc["slots"].([]any) {
			if s := v.(map[string]any); s["id"] == "s1" {
				s["booking"].(map[string]any)["paid"] = true
			}
		}
		return true
	})
	f.now = func() time.Time { return s1.Add(-59 * time.Minute) }
	if n := f.RemindOnce(ctx); n != 2 { // the client and the team
		t.Fatalf("1h: %d", n)
	}
	if n := f.RemindOnce(ctx); n != 0 {
		t.Fatalf("1h again: %d", n)
	}
	m = e.msgsTo(winner)
	if len(m) != base+2 || !strings.Contains(m[base+1]["text"].(string), "Через час") || !strings.Contains(m[base+1]["text"].(string), "meet.example") {
		t.Fatalf("1h text: %v", m[base:])
	}
	if kbs, _ := json.Marshal(m[base+1]["reply_markup"]); strings.Contains(string(kbs), bot.KaspiLink) {
		t.Fatalf("paid, yet a pay button: %s", kbs)
	}
	if a := e.msgsTo(111); len(a) != admins+1 || !strings.Contains(a[admins]["text"].(string), "Через час разбор") || !strings.Contains(a[admins]["text"].(string), "Оплата: оплачен") {
		t.Fatalf("admin 1h: %v", a[admins:])
	}

	// After the meeting: meet → diag, unless the team moved the card.
	setLead(t, f, other, map[string]any{"col": "work"})
	s2, _ := parseSlotTime(day(5, 15))
	o2 := len(e.msgsTo(other))
	f.now = func() time.Time { return s2.Add(2 * time.Hour) }
	f.RemindOnce(ctx)
	if l := crmLead(t, repo, winner); l["col"] != "diag" || l["next"] != "" {
		t.Fatalf("after: %v", l)
	}
	if l := crmLead(t, repo, other); l["col"] != "work" {
		t.Fatalf("team's column overwritten: %v", l["col"])
	}
	if len(e.msgsTo(other)) != o2 {
		t.Fatal("late reminders sent")
	}
	if n := f.RemindOnce(ctx); n != 0 {
		t.Fatalf("again: %d", n)
	}
	if code, out := call("POST", "/api/v1/app/book/cancel", winner, map[string]any{"slotId": "s1"}); code == 200 {
		t.Fatalf("cancel a past one: %v", out)
	}
}
