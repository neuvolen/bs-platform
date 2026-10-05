package http

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/bnursik/business_surgery_backend/internal/bot"
	"github.com/bnursik/business_surgery_backend/internal/club"
	pg "github.com/bnursik/business_surgery_backend/internal/repository/pg"
	"github.com/gin-gonic/gin"
)

type fakeResidents struct{ list []club.Resident }

func (f fakeResidents) LoadResidents(context.Context) ([]club.Resident, error) { return f.list, nil }

type sentMsg struct {
	Chat int64
	Text string
	KB   map[string]any
}

type fakeTG struct {
	mu   sync.Mutex
	msgs []sentMsg
	fail map[int64]string
}

func (f *fakeTG) send(_ context.Context, chat int64, text string, kb map[string]any) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if e, ok := f.fail[chat]; ok {
		return &tgErr{e}
	}
	f.msgs = append(f.msgs, sentMsg{chat, text, kb})
	return nil
}

type tgErr struct{ s string }

func (e *tgErr) Error() string { return e.s }

func (f *fakeTG) to(chat int64) []sentMsg {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []sentMsg
	for _, m := range f.msgs {
		if m.Chat == chat {
			out = append(out, m)
		}
	}
	return out
}

func (f *fakeTG) count() int { f.mu.Lock(); defer f.mu.Unlock(); return len(f.msgs) }

func kbData(kb map[string]any) string { b, _ := json.Marshal(kb); return string(b) }

const owner = int64(453800951)

func r38cSetup(t *testing.T, residents []club.Resident) (*Outreach, *fakeTG, *pg.PlatformRepo, context.Context, *time.Time) {
	repo, ctx := testPlatformDB(t, "bs_crm")
	db, err := pg.NewDB(ctx, os.Getenv("BS_TEST_DSN"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(db.Pool.Close)
	for _, q := range []string{`DELETE FROM bc_campaigns`, `DELETE FROM club_events`, `DELETE FROM event_notes`, `DELETE FROM wa_outbox`,
		`DELETE FROM resident_channels`, `DELETE FROM bot_updates WHERE chat_id > 0`} {
		if _, err := db.Pool.Exec(ctx, q); err != nil {
			t.Fatal(q, err)
		}
	}
	t.Setenv("GREEN_API_ID", "")
	t.Setenv("GREEN_API_TOKEN", "")
	t.Setenv("EVENT_SEATS", "")
	t.Setenv("EVENTS_PAY_LINK", "")
	o := NewOutreach(pg.NewOutreachRepo(db), repo, fakeResidents{residents}, []byte("secret-r38c"), []int64{owner, 7000001}, owner)
	now := time.Date(2026, 10, 5, 12, 0, 0, 0, club.Almaty)
	o.now = func() time.Time { return now }
	o.pubBase = func() string { return "https://bs.example" }
	o.tgBase = "https://bs.example/platform"
	o.Pace = time.Millisecond
	tg := &fakeTG{fail: map[int64]string{}}
	o.Send = tg.send
	o.Contact = func(ctx context.Context, chat int64, text string) error {
		return tg.send(ctx, chat, "CONTACT:"+text, nil)
	}
	return o, tg, repo, ctx, &now
}

func r38cRouter(o *Outreach, user string) *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	g := r.Group("/api/v1/platform")
	g.Use(func(c *gin.Context) { c.Set("role", "admin"); c.Set("userID", user) })
	o.Register(r, g)
	return r
}

func call38(r *gin.Engine, method, path, body string) (int, map[string]any, string) {
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(method, path, strings.NewReader(body)))
	var j map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &j)
	return w.Code, j, w.Body.String()
}

// R38c: the business breakfast: the event and a draft broadcast are made
// once, nothing goes out by itself; the owner sees the counts, tests the
// message on himself and sends it with one click (once, in broadcast hours);
// «Иду» books a seat (the waitlist after), asks the phone, puts the lead into
// the CRM; the address and the reminders go once each.
func TestR38cBreakfast(t *testing.T) {
	residents := []club.Resident{
		{Name: "Айгерим Сапарова", TgID: 1001},
		{Name: "Елена Иванова"},                // no Telegram: WhatsApp
		{Name: "Рустам Кабден", TgID: owner, Admin: true}, // the team
		{Name: "Бывший Резидент", TgID: 1009, Former: true},
	}
	o, tg, repo, ctx, now := r38cSetup(t, residents)
	// CRM: two leads with Telegram (stages new and qual), one with a phone only
	crm := `{"leads":[{"id":"tg2001","col":"new","name":"Лид Новый","tgId":2001,"funnel":"bot","phone":""},` +
		`{"id":"tg2002","col":"qual","name":"Лид Квал","tgId":2002,"phone":"+77010000002"},` +
		`{"id":"wa1","col":"work","name":"Лид Без ТГ","phone":"+77010000003"}]}`
	if _, err := repo.PutDoc(ctx, "club", "bs_crm", 0, crm, false, "test"); err != nil {
		t.Fatal(err)
	}
	db, _ := pg.NewDB(ctx, os.Getenv("BS_TEST_DSN"))
	defer db.Pool.Close()
	if _, err := db.Pool.Exec(ctx, `INSERT INTO bot_updates (update_id, kind, chat_id, body) VALUES
		(990000001, 'message', 3001, '{"message":{"from":{"first_name":"Подписчик"}}}'),
		(990000002, 'message', 2001, '{"message":{"from":{"first_name":"Лид"}}}'),
		(990000003, 'message', $1, '{"message":{"from":{"first_name":"Рустам"}}}')
		ON CONFLICT (update_id) DO UPDATE SET chat_id = EXCLUDED.chat_id`, owner); err != nil {
		t.Fatal(err)
	}
	defer db.Pool.Exec(context.Background(), `DELETE FROM bot_updates WHERE update_id IN (990000001, 990000002, 990000003)`) //nolint:errcheck

	// the seeds: once; Елена gets WhatsApp
	o.seedEvents(ctx)
	o.seedEvents(ctx)
	if !o.SeedElena(ctx) || !o.SeedElena(ctx) {
		t.Fatal("Елена not merged")
	}
	if p := o.Route(ctx, "Елена Иванова"); p != "77071144645" {
		t.Fatalf("Елена's route %q", p)
	}
	if tg.count() != 0 {
		t.Fatalf("something went out by itself: %+v", tg.msgs)
	}
	adm := r38cRouter(o, "tg:453800951")
	code, ev, body := call38(adm, "GET", "/api/v1/platform/outreach/events/"+BreakfastID, "")
	if code != 200 || ev["title"] != "Бизнес-завтрак Business Surgery" || ev["seats"] != 15.0 || ev["price"] != 10000.0 || ev["date"] != "2026-10-08" || ev["time"] != "10:00" ||
		ev["when"] != "Четверг, 8 октября, 10:00" || ev["addressNote"] != "адрес сообщим участникам накануне" {
		t.Fatalf("event: %d %s", code, body)
	}
	camps := ev["campaigns"].([]any)
	if len(camps) != 1 || camps[0].(map[string]any)["status"] != "draft" || camps[0].(map[string]any)["text"] != BreakfastText {
		t.Fatalf("campaign: %s", body)
	}
	if strings.Contains(BreakfastText, "—") {
		t.Fatal("em dash")
	}

	// the counts for «Все»: residents (Telegram 1 + WhatsApp 1), CRM (2 with Telegram, 1 without), subscribers (3001), the team left out
	code, cj, body := call38(adm, "GET", "/api/v1/platform/outreach/campaigns/"+BreakfastCampaign, "")
	cnt, _ := cj["counts"].(map[string]any)
	if code != 200 || cnt["residents"] != 2.0 || cnt["residentsWa"] != 1.0 || cnt["crm"] != 2.0 || cnt["crmNoTelegram"] != 1.0 ||
		cnt["subscribers"] != 1.0 || cnt["total"] != 5.0 || cnt["whatsapp"] != 1.0 || cnt["telegram"] != 4.0 || cnt["teamExcluded"] != 1.0 {
		t.Fatalf("counts: %s", body)
	}
	// only the qual stage of the CRM
	_, pj, _ := call38(adm, "POST", "/api/v1/platform/outreach/campaigns/"+BreakfastCampaign+"/preview", `{"audience":{"crm":["qual"]}}`)
	if c := pj["counts"].(map[string]any); c["total"] != 1.0 || c["crm"] != 1.0 {
		t.Fatalf("crm qual: %v", pj)
	}
	// the owner edits the text (line breaks kept)
	edited := BreakfastText + "\n\nP.S. Будет кофе."
	ab, _ := json.Marshal(map[string]any{"text": edited, "audience": map[string]any{"all": true}})
	if code, _, body := call38(adm, "PUT", "/api/v1/platform/outreach/campaigns/"+BreakfastCampaign, string(ab)); code != 200 || !strings.Contains(body, `P.S. Будет кофе.`) {
		t.Fatalf("edit: %d %s", code, body)
	}

	// the test: to the owner only, with «Иду» / «Подробнее»
	if code, j, _ := call38(adm, "POST", "/api/v1/platform/outreach/campaigns/"+BreakfastCampaign+"/test", ""); code != 200 || j["ok"] != true {
		t.Fatalf("test: %d %v", code, j)
	}
	if m := tg.to(owner); len(m) != 1 || m[0].Text != edited || !strings.Contains(kbData(m[0].KB), `"callback_data":"ev:go:bb20261008"`) ||
		!strings.Contains(kbData(m[0].KB), `"text":"Иду"`) || !strings.Contains(kbData(m[0].KB), `"text":"Подробнее"`) || tg.count() != 1 {
		t.Fatalf("test message: %+v", tg.msgs)
	}
	if code, _, _ := call38(adm, "POST", "/api/v1/platform/outreach/campaigns/"+BreakfastCampaign+"/test", ""); code != 429 {
		t.Fatalf("test twice in 15 s: %d", code)
	}

	// quiet hours: 21:00 is refused unless forced
	*now = time.Date(2026, 10, 5, 21, 0, 0, 0, club.Almaty)
	if code, j, _ := call38(adm, "POST", "/api/v1/platform/outreach/campaigns/"+BreakfastCampaign+"/send", `{}`); code != 409 || j["error"] != "quiet" {
		t.Fatalf("quiet: %d %v", code, j)
	}
	*now = time.Date(2026, 10, 5, 12, 0, 0, 0, club.Almaty)
	tg.fail[3001] = "telegram sendMessage: Forbidden: bot was blocked by the user"
	code, sj, body := call38(adm, "POST", "/api/v1/platform/outreach/campaigns/"+BreakfastCampaign+"/send", `{}`)
	if code != 200 || sj["status"] != "sending" {
		t.Fatalf("send: %d %s", code, body)
	}
	// a second click, another tab: nothing more
	if code, j, _ := call38(adm, "POST", "/api/v1/platform/outreach/campaigns/"+BreakfastCampaign+"/send", `{"force":true}`); code != 409 || j["error"] != "not_draft" {
		t.Fatalf("second send: %d %v", code, j)
	}
	var cv map[string]any
	for i := 0; i < 200; i++ {
		_, cv, _ = call38(adm, "GET", "/api/v1/platform/outreach/campaigns/"+BreakfastCampaign, "")
		if cv["status"] == "done" {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	pr := cv["progress"].(map[string]any)
	if cv["status"] != "done" || pr["sent"] != 3.0 || pr["wa"] != 1.0 || pr["failed"] != 1.0 {
		t.Fatalf("done: %+v", cv)
	}
	delete(tg.fail, 3001)
	o.runCampaign(ctx, BreakfastCampaign) // a restart: nobody gets it twice
	for _, id := range []int64{1001, 2001, 2002} {
		if m := tg.to(id); len(m) != 1 || m[0].Text != edited {
			t.Fatalf("%d got %+v", id, m)
		}
	}
	if len(tg.to(1009)) != 0 || len(tg.to(7000001)) != 0 {
		t.Fatal("a former resident or the team got the broadcast")
	}
	if m := tg.to(owner); len(m) != 2 || !strings.Contains(m[1].Text, "Рассылка «Анонс: Бизнес-завтрак 8 октября» отправлена: Telegram 3, WhatsApp 1, не доставлено 1") {
		t.Fatalf("owner summary: %+v", m)
	}
	// Елена's copy waits in the WhatsApp queue, without buttons, «ответьте «Иду»»
	_, wq, body := call38(adm, "GET", "/api/v1/platform/outreach/wa", "")
	items := wq["items"].([]any)
	if wq["pending"] != 1.0 || len(items) != 1 {
		t.Fatalf("wa queue: %s", body)
	}
	it := items[0].(map[string]any)
	wantLink := "https://wa.me/77071144645?text=" + strings.ReplaceAll(url.QueryEscape(waText(edited)), "+", "%20")
	if it["resident"] != "Елена Иванова" || it["kind"] != "broadcast" || !strings.Contains(it["text"].(string), "ответьте «Иду» на это сообщение") || it["link"] != wantLink {
		t.Fatalf("wa item: %+v", it)
	}

	// «Иду» from the new lead (no phone): booked, the phone asked, the CRM notes it
	press := func(from int64, op string) string {
		toast, ok := o.EventButton(ctx, bot.CallbackUpdate{ID: "cb", ChatID: from, FromID: from, Data: "ev:" + op + ":" + BreakfastID, FirstName: "Имя", LastName: "Фамилия", Username: "user"})
		if !ok {
			t.Fatalf("button %d %s not taken", from, op)
		}
		return toast
	}
	if toast := press(2001, "go"); toast != "Вы записаны ✅" {
		t.Fatalf("go: %q", toast)
	}
	m := tg.to(2001)
	if len(m) != 3 || !strings.Contains(m[1].Text, "✅ Вы записаны: Бизнес-завтрак Business Surgery") || !strings.Contains(m[1].Text, "💳 Участие: 10 000 ₸: оплата на месте или по ссылке, которую пришлём") ||
		!strings.Contains(m[1].Text, "📍 Алматы, адрес сообщим участникам накануне") || !strings.Contains(kbData(m[1].KB), "ev:no:") || !strings.HasPrefix(m[2].Text, "CONTACT:") {
		t.Fatalf("confirmation: %+v", m)
	}
	if toast := press(2001, "go"); toast != "Вы уже записаны ✅" || len(tg.to(2001)) != 3 {
		t.Fatalf("twice: %q", toast)
	}
	if !o.EventContact(ctx, bot.ContactUpdate{ChatID: 2001, FromID: 2001, Phone: "+77015550001"}) {
		t.Fatal("contact not taken")
	}
	if o.EventContact(ctx, bot.ContactUpdate{ChatID: 5555, FromID: 5555, Phone: "+77015550009"}) {
		t.Fatal("a phone nobody asked for must go on to the usual note")
	}
	d, _ := repo.GetDoc(ctx, "club", "bs_crm")
	if !strings.Contains(d.Value, `"phone":"+77015550001"`) || !strings.Contains(d.Value, "Записался: Бизнес-завтрак Business Surgery (08.10)") {
		t.Fatalf("crm: %s", d.Value)
	}
	// a stranger: a new lead in the CRM
	press(4001, "go")
	d, _ = repo.GetDoc(ctx, "club", "bs_crm")
	if !strings.Contains(d.Value, `"id":"tg4001"`) || !strings.Contains(d.Value, `"source":"Мероприятие: Бизнес-завтрак Business Surgery"`) {
		t.Fatalf("new lead: %s", d.Value)
	}
	// «Подробнее»: the text with «Иду» again
	press(4002, "more")
	if m := tg.to(4002); len(m) != 1 || !strings.HasPrefix(m[0].Text, "☕ Бизнес-завтрак Business Surgery") || !strings.Contains(kbData(m[0].KB), "ev:go:") {
		t.Fatalf("more: %+v", m)
	}

	// 3 seats: the resident takes the last one, the next goes to the waitlist; the owner hears once
	if code, _, body := call38(adm, "PUT", "/api/v1/platform/outreach/events/"+BreakfastID, `{"seats":3}`); code != 200 {
		t.Fatalf("seats: %s", body)
	}
	press(1001, "go")
	if toast := press(3001, "go"); toast != "Вы в листе ожидания" {
		t.Fatalf("waitlist: %q", toast)
	}
	if m := tg.to(3001); len(m) < 1 || !strings.Contains(m[0].Text, "листе ожидания") {
		t.Fatalf("waitlist msg: %+v", m)
	}
	full := 0
	for _, m := range tg.to(owner) {
		if strings.Contains(m.Text, "Места на «Бизнес-завтрак Business Surgery» закончились: 3 из 3") {
			full++
		}
	}
	press(5001, "go")
	if full != 1 {
		t.Fatalf("full note %d", full)
	}
	// «Не смогу»: the seat goes to the first of the waitlist, who hears it
	if toast := press(1001, "no"); toast != "Запись отменена" {
		t.Fatalf("no: %q", toast)
	}
	if m := tg.to(3001); !strings.Contains(m[len(m)-1].Text, "Освободилось место, и оно ваше") {
		t.Fatalf("promoted: %+v", m)
	}
	// Елена is added by the team: her reminders go to WhatsApp
	if code, _, body := call38(adm, "POST", "/api/v1/platform/outreach/events/"+BreakfastID+"/participants", `{"name":"Елена Иванова","phone":"+7 707 114 46 45"}`); code != 200 || !strings.Contains(body, `"who":"wa:77071144645"`) {
		t.Fatalf("add: %d %s", code, body)
	}
	_, evj, _ := call38(adm, "GET", "/api/v1/platform/outreach/events/"+BreakfastID, "")
	if evj["going"] != 3.0 || evj["waitlist"] != 2.0 {
		t.Fatalf("participants: going %v wait %v", evj["going"], evj["waitlist"])
	}
	// more seats: the waitlist moves up
	call38(adm, "PUT", "/api/v1/platform/outreach/events/"+BreakfastID, `{"seats":15}`)
	_, evj, _ = call38(adm, "GET", "/api/v1/platform/outreach/events/"+BreakfastID, "")
	if evj["going"] != 5.0 || evj["waitlist"] != 0.0 {
		t.Fatalf("after more seats: going %v wait %v", evj["going"], evj["waitlist"])
	}

	// the address: to everyone going, once
	before := len(tg.to(2001))
	code, aj, body := call38(adm, "POST", "/api/v1/platform/outreach/events/"+BreakfastID+"/address", `{"address":"Кофейня Nomad, ул. Кунаева 77, 2 этаж"}`)
	if code != 200 || aj["sentNow"] != true {
		t.Fatalf("address: %d %s", code, body)
	}
	o.eventTick(ctx)
	if m := tg.to(2001); len(m) != before+1 || !strings.Contains(m[len(m)-1].Text, "Кофейня Nomad, ул. Кунаева 77, 2 этаж") {
		t.Fatalf("address msg: %+v", m[before:])
	}
	// the day before at 18:00 and the morning at 08:30: once each
	*now = time.Date(2026, 10, 7, 18, 5, 0, 0, club.Almaty)
	o.eventTick(ctx)
	o.eventTick(ctx)
	*now = time.Date(2026, 10, 8, 8, 31, 0, 0, club.Almaty)
	o.eventTick(ctx)
	o.eventTick(ctx)
	m = tg.to(2001)
	if len(m) != before+3 || !strings.Contains(m[before+1].Text, "Напоминаем: завтра, Четверг, 8 октября, 10:00") || !strings.Contains(m[before+2].Text, "☕ Сегодня в 10:00") {
		t.Fatalf("reminders: %+v", m[before:])
	}
	if len(tg.to(1001)) != 3 { // the broadcast, the confirmation, the cancel; no reminders
		for _, x := range tg.to(1001) {
			t.Log(x.Text)
		}
		t.Fatalf("cancelled one got reminders")
	}
	// Елена's reminders wait in the WhatsApp queue
	_, wq, _ = call38(adm, "GET", "/api/v1/platform/outreach/wa", "")
	if wq["pending"] != 5.0 { // the broadcast, «место ваше», the address, the eve, the morning
		t.Fatalf("wa pending %v", wq["pending"])
	}

	// the owner's note: one per batch, a button per message (signed), not at night
	*now = time.Date(2026, 10, 8, 9, 0, 0, 0, club.Almaty)
	o.notifyWA(ctx)
	o.notifyWA(ctx)
	var note *sentMsg
	for _, x := range tg.to(owner) {
		if strings.HasPrefix(x.Text, "📲 WhatsApp: к отправке") {
			if note != nil {
				t.Fatal("two notes")
			}
			x := x
			note = &x
		}
	}
	if note == nil || !strings.Contains(note.Text, "• Елена Иванова: рассылка") || !strings.Contains(kbData(note.KB), "https://bs.example/api/v1/wa/open/") {
		t.Fatalf("note: %+v", note)
	}
	// one click in Telegram: WhatsApp opens with the text, the message is marked sent
	first := items[0].(map[string]any)["id"].(float64)
	link := o.OpenURL(int64(first))
	w := httptest.NewRecorder()
	adm.ServeHTTP(w, httptest.NewRequest("GET", strings.TrimPrefix(link, "https://bs.example"), nil))
	if w.Code != 302 || w.Header().Get("Location") != wantLink {
		t.Fatalf("open: %d %s", w.Code, w.Header().Get("Location"))
	}
	w = httptest.NewRecorder()
	adm.ServeHTTP(w, httptest.NewRequest("GET", strings.TrimPrefix(link, "https://bs.example")+"x", nil))
	if w.Code != 404 {
		t.Fatalf("bad signature: %d", w.Code)
	}
	_, wq, _ = call38(adm, "GET", "/api/v1/platform/outreach/wa", "")
	if wq["pending"] != 4.0 {
		t.Fatalf("after open: %v", wq["pending"])
	}
	// on the platform: «Отправлено» and back
	sec := wq["items"].([]any)[0].(map[string]any)["id"].(float64)
	if _, j, _ := call38(adm, "POST", "/api/v1/platform/outreach/wa/"+jsonNum(sec)+"/sent", ""); j["ok"] != true || j["pending"] != 3.0 {
		t.Fatalf("sent: %v", j)
	}
	if _, j, _ := call38(adm, "POST", "/api/v1/platform/outreach/wa/"+jsonNum(sec)+"/back", ""); j["ok"] != true || j["pending"] != 4.0 {
		t.Fatalf("back: %v", j)
	}
}

func jsonNum(f float64) string { b, _ := json.Marshal(int64(f)); return string(b) }

// R38c: every personal message to a WhatsApp resident goes through the
// queue (or Green-API when connected), once per key; Telegram residents
// are untouched. Several «Елена»: nothing is guessed.
func TestR38cResidentWhatsApp(t *testing.T) {
	residents := []club.Resident{{Name: "Айгерим Сапарова", TgID: 1001}, {Name: "Елена Иванова"}}
	o, tg, _, ctx, _ := r38cSetup(t, residents)
	if !o.SeedElena(ctx) {
		t.Fatal("seed")
	}
	// the bot routes by the channel
	var tgSent []int64
	tgSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var p map[string]any
		_ = json.NewDecoder(r.Body).Decode(&p)
		tgSent = append(tgSent, int64(p["chat_id"].(float64)))
		_, _ = w.Write([]byte(`{"ok":true,"result":{"message_id":1}}`))
	}))
	defer tgSrv.Close()
	svc := bot.New(nil, bot.Options{Token: "T", APIBase: tgSrv.URL})
	svc.SetWhatsApp(o.Route, o.DeliverWA)
	kb := map[string]any{"inline_keyboard": [][]map[string]any{{{"text": "📱 Открыть в BS", "web_app": map[string]string{"url": "https://t.me/app"}}, {"text": "Оплатить", "url": "https://pay.kaspi.kz/pay/x"}}}}
	for i := 0; i < 2; i++ {
		if err := svc.SendResident(ctx, "meeting", "1d|2026-10-06T15:00", "Елена Иванова", 0, "⏰ Елена, встреча ЗАВТРА!", kb); err != nil {
			t.Fatal(err)
		}
	}
	if err := svc.SendResident(ctx, "meeting", "1d|2026-10-06T15:00", "Айгерим Сапарова", 1001, "⏰ Айгерим, встреча ЗАВТРА!", kb); err != nil {
		t.Fatal(err)
	}
	if len(tgSent) != 1 || tgSent[0] != 1001 {
		t.Fatalf("telegram: %v", tgSent)
	}
	// the call summary
	if ok, err := o.HandleResidentWA(ctx, "summary", "job1", "Елена Иванова", "Елена, привет! Саммари"); !ok || err != nil {
		t.Fatal("summary not routed")
	}
	if ok, _ := o.HandleResidentWA(ctx, "summary", "job1", "Айгерим Сапарова", "x"); ok {
		t.Fatal("a Telegram resident routed to WhatsApp")
	}
	adm := r38cRouter(o, "tg:453800951")
	_, wq, body := call38(adm, "GET", "/api/v1/platform/outreach/wa", "")
	if wq["pending"] != 2.0 || !strings.Contains(body, "Оплатить: https://pay.kaspi.kz/pay/x") || strings.Contains(body, "t.me/app") {
		t.Fatalf("queue: %s", body)
	}
	// the channel list on the platform, and a switch back to Telegram
	_, cj, body := call38(adm, "GET", "/api/v1/platform/outreach/channels", "")
	if !strings.Contains(body, `"name":"Елена Иванова","phone":"77071144645","phonePretty":"+7 707 114 46 45","tg":false`) && !strings.Contains(body, `"channel":"wa"`) {
		t.Fatalf("channels: %s", body)
	}
	_ = cj
	if code, _, _ := call38(adm, "PUT", "/api/v1/platform/outreach/channels", `{"name":"Айгерим Сапарова","channel":"wa","phone":"123"}`); code != 400 {
		t.Fatal("short phone accepted")
	}
	if code, _, _ := call38(adm, "PUT", "/api/v1/platform/outreach/channels", `{"name":"Елена Иванова","channel":"tg","phone":"+77071144645"}`); code != 200 {
		t.Fatal("switch")
	}
	if o.Route(ctx, "Елена Иванова") != "" {
		t.Fatal("still WhatsApp")
	}
	// the merge never overrides the owner's choice
	o.SeedElena(ctx)
	if o.Route(ctx, "Елена Иванова") != "" {
		t.Fatal("merge overrode the owner")
	}

	// Green-API connected: sent at once, nothing waits
	var greenGot []map[string]any
	green := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var p map[string]any
		_ = json.NewDecoder(r.Body).Decode(&p)
		greenGot = append(greenGot, p)
		_, _ = w.Write([]byte(`{"idMessage":"ABC"}`))
	}))
	defer green.Close()
	t.Setenv("GREEN_API_ID", "1101")
	t.Setenv("GREEN_API_TOKEN", "tok")
	t.Setenv("GREEN_API_URL", green.URL)
	call38(adm, "PUT", "/api/v1/platform/outreach/channels", `{"name":"Елена Иванова","channel":"wa","phone":"+7 707 114 46 45"}`)
	if err := svc.SendResident(ctx, "fine", "05.10.2026", "Елена Иванова", 0, "⚠️ Елена, штраф", nil); err != nil {
		t.Fatal(err)
	}
	if len(greenGot) != 1 || greenGot[0]["chatId"] != "77071144645@c.us" || greenGot[0]["message"] != "⚠️ Елена, штраф" {
		t.Fatalf("green: %v", greenGot)
	}
	_, wq, _ = call38(adm, "GET", "/api/v1/platform/outreach/wa", "")
	if wq["pending"] != 2.0 || wq["green"] != true {
		t.Fatalf("after green: %v", wq)
	}
	_ = tg

	// two «Елена»: no guess, a hint
	o2, _, _, ctx2, _ := r38cSetup(t, []club.Resident{{Name: "Елена Иванова"}, {Name: "Елена Петрова", TgID: 5}})
	if o2.SeedElena(ctx2) {
		t.Fatal("guessed")
	}
	adm2 := r38cRouter(o2, "tg:453800951")
	_, cj, _ = call38(adm2, "GET", "/api/v1/platform/outreach/channels", "")
	if h, _ := cj["hint"].(string); !strings.Contains(h, "Елена Иванова, Елена Петрова") || !strings.Contains(h, "+7 707 114 46 45") {
		t.Fatalf("hint: %v", cj["hint"])
	}
	if o2.Route(ctx2, "Елена Иванова") != "" || o2.Route(ctx2, "Елена Петрова") != "" {
		t.Fatal("routed without a choice")
	}
}
