package bot

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/bnursik/business_surgery_backend/internal/club"
)

// R38c: «Иду» is anyone's button (a lead, a resident, the team) and comes
// before every other hook; a shared phone goes to the contact hook first.
func TestR38cPublicCallbackAndContact(t *testing.T) {
	var mu sync.Mutex
	var answers []map[string]any
	tg := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var p map[string]any
		_ = json.NewDecoder(r.Body).Decode(&p)
		if strings.HasSuffix(r.URL.Path, "/answerCallbackQuery") {
			mu.Lock()
			answers = append(answers, p)
			mu.Unlock()
		}
		_, _ = w.Write([]byte(`{"ok":true,"result":true}`))
	}))
	defer tg.Close()
	s := New(nil, Options{Token: "T", APIBase: tg.URL, Admins: []int64{453800951}})
	var got []string
	s.SetPublicCallbackHook("ev:", func(_ context.Context, cb CallbackUpdate) (string, bool) {
		got = append(got, cb.Data)
		return "Вы записаны ✅", true
	})
	s.SetTeamCallbackHook("ev:", func(context.Context, CallbackUpdate) (string, bool) { t.Fatal("team hook first"); return "", false })
	body := func(from int64, data, chatType string) []byte {
		b, _ := json.Marshal(map[string]any{"update_id": 1, "callback_query": map[string]any{
			"id": "cb1", "data": data, "from": map[string]any{"id": from, "first_name": "Лид"},
			"message": map[string]any{"message_id": 5, "chat": map[string]any{"id": from, "type": chatType}}}})
		return b
	}
	ctx := context.Background()
	for _, from := range []int64{777, 453800951} {
		if !s.takeCallback(ctx, body(from, "ev:go:bb20261008", "private")) {
			t.Fatalf("%d: not taken", from)
		}
	}
	if len(got) != 2 || len(answers) != 2 || answers[0]["text"] != "Вы записаны ✅" {
		t.Fatalf("got %v answers %v", got, answers)
	}
	if s.takeCallback(ctx, body(777, "ev:go:bb20261008", "supergroup")) {
		t.Fatal("a group press is not an RSVP")
	}

	var phones []ContactUpdate
	s.SetContactHook(func(_ context.Context, cu ContactUpdate) bool { phones = append(phones, cu); return cu.FromID == 777 })
	contact := func(from int64) []byte {
		b, _ := json.Marshal(map[string]any{"update_id": 2, "message": map[string]any{"message_id": 9,
			"chat": map[string]any{"id": from, "type": "private"}, "from": map[string]any{"id": from, "first_name": "Лид"},
			"contact": map[string]any{"phone_number": "77015550001"}}})
		return b
	}
	if !s.takeContact(ctx, contact(777)) || len(phones) != 1 || phones[0].Phone != "+77015550001" {
		t.Fatalf("contact: %+v", phones)
	}
	if s.takeContact(ctx, contact(888)) {
		t.Fatal("a phone the hook did not want goes on")
	}
}

// R38c: WhatsApp residents (no Telegram) get the meeting and the report reminders too.
func TestR38cRemindersWhatsApp(t *testing.T) {
	now := time.Date(2026, 10, 5, 12, 0, 0, 0, club.Almaty)
	res := []club.Resident{{Name: "Елена Иванова"}, {Name: "Айгерим", TgID: 1001}, {Name: "Без канала"}}
	m := []club.Meeting{
		{Resident: "Елена Иванова", Date: now.AddDate(0, 0, 1), Time: "15:00", Place: "Офис"},
		{Resident: "Без канала", Date: now.AddDate(0, 0, 1), Time: "16:00", Place: "Офис"},
	}
	if r := DueReminders(m, res, nil, now); len(r) != 0 {
		t.Fatalf("without WhatsApp: %+v", r)
	}
	r := DueRemindersWA(m, res, nil, now, map[string]bool{"елена иванова": true})
	if len(r) != 1 || r[0].TgID != -1 || r[0].Resident != "Елена Иванова" || !strings.Contains(r[0].Text, "Елена, встреча ЗАВТРА") {
		t.Fatalf("WhatsApp reminder: %+v", r)
	}
	ev := EveningTargetsWA(res, nil, map[string]bool{"елена иванова": true})
	if len(ev) != 2 || ev[1].Name != "Елена Иванова" && ev[0].Name != "Елена Иванова" {
		t.Fatalf("evening: %+v", ev)
	}
	if len(EveningTargets(res, nil)) != 1 {
		t.Fatal("old behaviour changed")
	}
	if l := kbLinks(appButton("fines")); l != "" {
		t.Fatalf("a web_app button has no link outside Telegram: %q", l)
	}
	if l := kbLinks(map[string]any{"inline_keyboard": [][]map[string]any{{{"text": "Оплатить", "url": "https://pay.kaspi.kz/pay/x"}}}}); l != "\n\nОплатить: https://pay.kaspi.kz/pay/x" {
		t.Fatalf("links %q", l)
	}
}
