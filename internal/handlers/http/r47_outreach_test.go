package http

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/bnursik/business_surgery_backend/internal/club"
)

// R47: «Что делать с базой»: WhatsApp batch to the phone-only leads (queue
// only, no Telegram note, the same text never twice), a Telegram draft for
// the leads who wrote to the bot, the breakfast invitation draft with «Иду»;
// nothing is sent until the owner presses «Отправить».
func TestR47SegmentActions(t *testing.T) {
	o, tg, repo, ctx, _ := r38cSetup(t, []club.Resident{{Name: "Айгерим Сапарова", TgID: 1001}})
	o.seedEvents(ctx)
	crm := `{"leads":[{"id":"b1","col":"new","name":"Айдар Нурланов","phone":"+7 701 000 00 01","base":"Клиенты"},` +
		`{"id":"b2","col":"new","name":"+77010000002","phone":"+77010000002","base":"Клиенты"},` +
		`{"id":"b3","col":"new","name":"Без телефона","tg":"@x","base":"Клиенты"},` +
		`{"id":"tg2001","col":"new","name":"Бот Лид","tgId":2001,"funnel":"bot"},` +
		`{"id":"tg2002","col":"new","name":"Бот Два","tgId":2002,"funnel":"bot"},` +
		`{"id":"other","col":"new","name":"Не в сегменте","tgId":2003,"funnel":"bot"}]}`
	if _, err := repo.PutDoc(ctx, "club", "bs_crm", 0, crm, false, "test"); err != nil {
		t.Fatal(err)
	}
	r := r38cRouter(o, "tg:453800951")
	body := func(m map[string]any) string { b, _ := json.Marshal(m); return string(b) }

	code, j, raw := call38(r, "POST", "/api/v1/platform/outreach/segments/act", body(map[string]any{"action": "wa", "ids": []string{"b1", "b2", "b3"}, "text": "Здравствуйте, {имя}! Это Business Surgery.", "limit": 1}))
	if code != 200 || j["queued"].(float64) != 1 || j["left"].(float64) != 1 || j["noPhone"].(float64) != 1 {
		t.Fatalf("wa: %d %s", code, raw)
	}
	code, j, raw = call38(r, "POST", "/api/v1/platform/outreach/segments/act", body(map[string]any{"action": "wa", "ids": []string{"b1", "b2", "b3"}, "text": "Здравствуйте, {имя}! Это Business Surgery."}))
	if code != 200 || j["queued"].(float64) != 1 || j["already"].(float64) != 1 {
		t.Fatalf("wa second batch: %d %s", code, raw)
	}
	items, _ := o.repo.WAList(ctx, 10)
	texts := []string{}
	for _, w := range items {
		texts = append(texts, w.Text)
		if w.Kind != "lead" || w.Status != "pending" {
			t.Fatalf("item %+v", w)
		}
	}
	all := strings.Join(texts, "|")
	if !strings.Contains(all, "Здравствуйте, Айдар! Это") || !strings.Contains(all, "Здравствуйте! Это") || len(items) != 2 {
		t.Fatalf("texts %q", texts)
	}
	if notes, _ := o.repo.WAToNotify(ctx); len(notes) != 0 {
		t.Fatalf("a batch of the base must not ping the owner: %d", len(notes))
	}
	o.notifyWA(ctx)
	if tg.count() != 0 {
		t.Fatalf("sent something: %d", tg.count())
	}

	// Telegram draft: only the bot's leads of the segment; the counts show 2
	code, j, raw = call38(r, "POST", "/api/v1/platform/outreach/segments/act", body(map[string]any{"action": "tg", "ids": []string{"b1", "tg2001", "tg2002"}, "text": "Привет из клуба", "title": "Холодные"}))
	if code != 200 || j["status"] != "draft" || j["noTelegram"].(float64) != 1 {
		t.Fatalf("tg: %d %s", code, raw)
	}
	cnt := j["counts"].(map[string]any)
	if cnt["total"].(float64) != 2 || cnt["crm"].(float64) != 2 || cnt["residents"].(float64) != 0 {
		t.Fatalf("counts %v", cnt)
	}
	id := j["id"].(string)
	if !strings.HasPrefix(id, segCampaignPrefix) || tg.count() != 0 {
		t.Fatalf("draft id %s / sent %d", id, tg.count())
	}
	// an editor that does not know «leads» saves the text: the audience stays the segment
	code, j, raw = call38(r, "PUT", "/api/v1/platform/outreach/campaigns/"+id, `{"text":"Привет из клуба!","audience":{"all":true}}`)
	if code != 200 || j["counts"].(map[string]any)["total"].(float64) != 2 {
		t.Fatalf("audience widened: %d %s", code, raw)
	}
	// the owner sends it: exactly the two leads
	code, _, raw = call38(r, "POST", "/api/v1/platform/outreach/campaigns/"+id+"/send", `{"force":true}`)
	if code != 200 {
		t.Fatalf("send %d %s", code, raw)
	}
	waitFor(t, func() bool { return tg.count() >= 2 })
	if len(tg.to(2001)) != 1 || len(tg.to(2002)) != 1 || len(tg.to(2003)) != 0 || len(tg.to(1001)) != 0 {
		t.Fatalf("recipients wrong: %d", tg.count())
	}

	// breakfast: the event's text and «Иду», hidden from the event's own broadcast list
	code, j, raw = call38(r, "POST", "/api/v1/platform/outreach/segments/act", body(map[string]any{"action": "breakfast", "ids": []string{"tg2001", "b1"}}))
	if code != 200 || j["eventId"] != BreakfastID || !strings.Contains(j["text"].(string), "Бизнес-завтрак") {
		t.Fatalf("breakfast: %d %s", code, raw)
	}
	_, ev, _ := call38(r, "GET", "/api/v1/platform/outreach/events/"+BreakfastID, "")
	for _, c := range ev["campaigns"].([]any) {
		if strings.HasPrefix(c.(map[string]any)["id"].(string), segCampaignPrefix) {
			t.Fatal("segment draft shown as the event's broadcast")
		}
	}
	// nobody of the segment wrote to the bot: a clear message, no draft
	code, j, _ = call38(r, "POST", "/api/v1/platform/outreach/segments/act", body(map[string]any{"action": "tg", "ids": []string{"b1"}, "text": "x"}))
	if code != 400 || !strings.Contains(j["message"].(string), "писали боту") {
		t.Fatalf("empty tg: %d %v", code, j)
	}
}

func waitFor(t *testing.T, ok func() bool) {
	t.Helper()
	for i := 0; i < 300; i++ {
		if ok() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("timed out")
}
