package http

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/bnursik/business_surgery_backend/internal/bot"
	pg "github.com/bnursik/business_surgery_backend/internal/repository/pg"
)

type r58Msg struct {
	text string
	kb   map[string]any
}

// r58Setup: the R57 stand plus «sent», «remind» and a capture of the owner's reminders.
func r58Setup(t *testing.T) (*gin.Engine, *BoardLinks, *[]r58Msg, *time.Time) {
	r, h, _, _, now := r57Setup(t)
	var mu sync.Mutex
	msgs := []r58Msg{}
	h.NotifyKB = func(_ context.Context, text string, kb map[string]any) {
		mu.Lock()
		msgs = append(msgs, r58Msg{text, kb})
		mu.Unlock()
	}
	a := r.Group("/cl2", func(c *gin.Context) { c.Set("userID", "tg:453800951"); c.Next() })
	a.POST("/:board/sent", h.AdminSent)
	a.DELETE("/:board/remind", h.AdminStopRemind)
	return r, h, &msgs, now
}

func r58Buttons(kb map[string]any) (wa, cb string) {
	b, _ := json.Marshal(kb)
	var k struct {
		InlineKeyboard [][]map[string]string `json:"inline_keyboard"`
	}
	_ = json.Unmarshal(b, &k)
	for _, row := range k.InlineKeyboard {
		for _, x := range row {
			if strings.HasPrefix(x["url"], "https://wa.me/") {
				wa = x["url"]
			}
			if x["callback_data"] != "" {
				cb = x["callback_data"]
			}
		}
	}
	return
}

func TestR58WhatsAppSendAndReminders(t *testing.T) {
	r, h, msgs, now := r58Setup(t)
	ctx := context.Background()
	l := r57Link(t, r57Do(r, "POST", "/cl/r57a", ""))
	if l.Phone != "" || !strings.HasPrefix(l.WA, "https://wa.me/?text=") || l.Followup != nil || l.Resident {
		t.Fatalf("before send: %+v", l)
	}
	// телефона в CRM нет: трекер вписывает номер, ссылка с номером, напоминание через день
	w := r57Do(r, "POST", "/cl2/r57a/sent", `{"phone":"8 701 555 66 77"}`)
	s := r57Link(t, w)
	if s.Phone != "+77015556677" || !strings.HasPrefix(s.WA, "https://wa.me/77015556677?text=") || !strings.Contains(s.WAText, "Алия, ваш разбор") {
		t.Fatalf("sent: %+v", s)
	}
	if s.Followup == nil || s.Followup.NextAt == nil || !s.Followup.NextAt.Equal(now.Add(24*time.Hour)) || s.Followup.Stop != "" {
		t.Fatalf("followup: %+v", s.Followup)
	}
	if !strings.Contains(r57Do(r, "GET", "/cl/r57a", "").Body.String(), `"phone":"+77015556677"`) {
		t.Fatal("the phone is kept on the link")
	}
	lead := h.leadOf(ctx, mustBoard(t, h, "r57a"))
	if sStr(lead, "phone") != "+77015556677" {
		t.Fatalf("the phone goes to the lead card: %v", lead)
	}
	if n := h.FollowupTick(ctx); n != 0 || len(*msgs) != 0 {
		t.Fatalf("nothing is due yet: %d", n)
	}

	// +1 день, не открыл: напоминание с цифрами, WhatsApp в одно касание и «Не напоминать»
	*now = now.Add(24*time.Hour + time.Minute)
	if n := h.FollowupTick(ctx); n != 1 || len(*msgs) != 1 {
		t.Fatalf("day 1: %d %d", n, len(*msgs))
	}
	m := (*msgs)[0]
	wa, cb := r58Buttons(m.kb)
	if !strings.Contains(m.text, "не открыл доску") || !strings.Contains(m.text, "Доску не открывал") || !strings.Contains(m.text, "Готовый текст") ||
		!strings.HasPrefix(wa, "https://wa.me/77015556677?text=") || cb != fuCallbackPrefix+r57Token(l.URL) || strings.Contains(m.text, "\u2014") {
		t.Fatalf("day 1 message: %q wa=%q cb=%q", m.text, wa, cb)
	}
	if n := h.FollowupTick(ctx); n != 0 {
		t.Fatal("the same reminder twice")
	}

	// клиент открыл и посмотрел диагнозы; +2 дня: «посмотрел, решения нет»
	tok := r57Token(l.URL)
	r57Do(r, "POST", "/b/"+tok+"/ev", `{"open":true,"s":95,"sec":["diag","plan"]}`)
	r57Do(r, "POST", "/b/"+tok+"/intent", `{"intent":"think"}`)
	*now = now.Add(24 * time.Hour)
	if n := h.FollowupTick(ctx); n != 1 || len(*msgs) != 2 {
		t.Fatalf("day 2: %d %d", n, len(*msgs))
	}
	m = (*msgs)[1]
	if !strings.Contains(m.text, "посмотрел доску, решения нет") || !strings.Contains(m.text, "Открыл 1 раз") || !strings.Contains(m.text, "на странице 2 мин") ||
		!strings.Contains(m.text, "Смотрел: диагнозы, план") || !strings.Contains(m.text, "«Пока подумаю»") {
		t.Fatalf("day 2 message: %q", m.text)
	}

	// «Не напоминать» в боте: дальше тишина
	toast, ok := h.RemindCallback(ctx, bot.CallbackUpdate{Data: cb, ChatID: 453800951, FromID: 453800951})
	if !ok || !strings.Contains(toast, "Больше не напомню") {
		t.Fatalf("callback: %q %v", toast, ok)
	}
	*now = now.Add(4 * 24 * time.Hour)
	if n := h.FollowupTick(ctx); n != 0 || len(*msgs) != 2 {
		t.Fatalf("stopped by the owner, still sent: %d", n)
	}
	g := r57Link(t, r57Do(r, "GET", "/cl/r57a", ""))
	if g.Followup == nil || g.Followup.Stop != "owner" {
		t.Fatalf("stop reason: %+v", g.Followup)
	}
}

func TestR58RemindersStopOnJoinResidentAndPanel(t *testing.T) {
	r, h, msgs, now := r58Setup(t)
	ctx := context.Background()
	l := r57Link(t, r57Do(r, "POST", "/cl/r57a", ""))
	tok := r57Token(l.URL)
	// лид стал резидентом (этап won)
	r57Link(t, r57Do(r, "POST", "/cl2/r57a/sent", `{"phone":"+7 701 111 22 33"}`))
	h.mutateLeadOf(ctx, mustBoard(t, h, "r57a"), func(ld map[string]any) bool { ld["col"] = "won"; return true })
	*now = now.Add(25 * time.Hour)
	if n := h.FollowupTick(ctx); n != 0 || len(*msgs) != 0 {
		t.Fatalf("resident: %d", n)
	}
	g := r57Link(t, r57Do(r, "GET", "/cl/r57a", ""))
	if g.Followup == nil || g.Followup.Stop != "resident" || !g.Resident {
		t.Fatalf("resident stop: %+v", g.Followup)
	}
	// панель: «Не напоминать»
	h.mutateLeadOf(ctx, mustBoard(t, h, "r57a"), func(ld map[string]any) bool { ld["col"] = "decide"; return true })
	r57Link(t, r57Do(r, "POST", "/cl2/r57a/sent", `{}`))
	if g := r57Link(t, r57Do(r, "DELETE", "/cl2/r57a/remind", "")); g.Followup == nil || g.Followup.Stop != "owner" {
		t.Fatalf("panel stop: %+v", g.Followup)
	}
	r57Link(t, r57Do(r, "POST", "/cl2/r57a/sent", `{}`))
	// «Хочу в клуб»: напоминания больше не нужны
	r57Do(r, "POST", "/b/"+tok+"/intent", `{"intent":"join"}`)
	*now = now.Add(3 * 24 * time.Hour)
	if n := h.FollowupTick(ctx); n != 0 || len(*msgs) != 0 {
		t.Fatalf("join: %d", n)
	}
	if g := r57Link(t, r57Do(r, "GET", "/cl/r57a", "")); g.Followup == nil || g.Followup.Stop != "join" {
		t.Fatalf("join stop: %+v", g.Followup)
	}
	// плохой номер
	if w := r57Do(r, "POST", "/cl2/r57a/sent", `{"phone":"12"}`); w.Code != 400 {
		t.Fatalf("bad phone: %d", w.Code)
	}
	// отключённая ссылка: напоминания стоп
	r57Link(t, r57Do(r, "POST", "/cl2/r57a/sent", `{}`))
	r57Do(r, "DELETE", "/cl/r57a", "")
	if g := r57Link(t, r57Do(r, "GET", "/cl/r57a", "")); g.Followup == nil || g.Followup.Stop != "revoked" {
		t.Fatalf("revoke stop: %+v", g.Followup)
	}
}

func TestR58FinalReminderAndNightWindow(t *testing.T) {
	r, h, msgs, now := r58Setup(t)
	ctx := context.Background()
	r57Link(t, r57Do(r, "POST", "/cl/r57a", ""))
	r57Link(t, r57Do(r, "POST", "/cl2/r57a/sent", `{}`))
	// никто не трогал 5 дней: день 1 (не открыл), день 2 пропущен (не открыл), день 5 последнее
	for i := 0; i < 6; i++ {
		*now = now.Add(24 * time.Hour)
		h.FollowupTick(ctx)
	}
	if len(*msgs) != 2 || !strings.Contains((*msgs)[0].text, "не открыл") || !strings.Contains((*msgs)[1].text, "Последнее напоминание") ||
		!strings.Contains((*msgs)[1].text, "Больше напоминаний по этой доске не будет") || !strings.Contains((*msgs)[1].text, "Телефона нет") {
		t.Fatalf("final: %d %+v", len(*msgs), *msgs)
	}
	if wa, _ := r58Buttons((*msgs)[1].kb); wa != "" {
		t.Fatal("no phone: no WhatsApp button")
	}
	g := r57Link(t, r57Do(r, "GET", "/cl/r57a", ""))
	if g.Followup == nil || g.Followup.Stop != "done" || g.Followup.Stage != 3 {
		t.Fatalf("done: %+v", g.Followup)
	}
	// ночь Алматы → 10:00 следующего дня, утро → 10:00 того же дня
	night := time.Date(2026, 10, 8, 16, 30, 0, 0, time.UTC) // 21:30 Алматы
	if w := fuWindow(night).In(almaty); w.Day() != 9 || w.Hour() != 10 {
		t.Fatalf("night: %v", w)
	}
	early := time.Date(2026, 10, 8, 1, 0, 0, 0, time.UTC) // 06:00 Алматы
	if w := fuWindow(early).In(almaty); w.Day() != 8 || w.Hour() != 10 {
		t.Fatalf("early: %v", w)
	}
}

func mustBoard(t *testing.T, h *BoardLinks, id string) *pg.PlatformBoard {
	t.Helper()
	b, err := h.Boards.GetBoard(context.Background(), id)
	if err != nil || b == nil {
		t.Fatalf("board %s: %v", id, err)
	}
	return b
}
