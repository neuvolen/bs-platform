package http

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/bnursik/business_surgery_backend/internal/ai"
	"github.com/bnursik/business_surgery_backend/internal/bot"
	"github.com/bnursik/business_surgery_backend/internal/club"
	"github.com/bnursik/business_surgery_backend/internal/middleware"
	pg "github.com/bnursik/business_surgery_backend/internal/repository/pg"
	"github.com/bnursik/business_surgery_backend/migrations"
	"github.com/gin-gonic/gin"
)

// ── R83: the sales manager ──

type mgEnv struct {
	*asEnv
	mgr  *SalesManagers
	sent []string
	mu   sync.Mutex
}

func newMgEnv(t *testing.T) *mgEnv {
	dsn := os.Getenv("BS_TEST_DSN")
	if dsn == "" {
		t.Skip("BS_TEST_DSN not set")
	}
	ctx := context.Background()
	db, err := pg.NewDB(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(db.Pool.Close)
	if err := pg.Migrate(ctx, db, migrations.FS); err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{
		`TRUNCATE platform_sessions, platform_session_cutoff`,
		`DELETE FROM platform_docs WHERE (scope = 'server' AND key IN ('bs_managers', 'bs_mgrlog', 'bs_qnotes')) OR (scope = 'club' AND key IN ('bs_crm', 'bs_slots', 'bs_scripts'))`,
		`DELETE FROM platform_residents WHERE tg_id IN (73001, 73002)`,
	} {
		if _, err := db.Pool.Exec(ctx, q); err != nil {
			t.Fatal(q, err)
		}
	}
	repo := pg.NewPlatformRepo(db)
	now := time.Now()
	crm := map[string]any{"leads": []any{
		map[string]any{"id": "L1", "col": "new", "name": "Арман Лидов", "phone": "+7 701 555 11 22", "source": "Сайт", "niche": "кофейни", "date": "09.10.2026", "refName": "Альфа Резидентова"},
		map[string]any{"id": "L2", "col": "qual", "name": "Командный Лид", "phone": "+77015552233", "source": "Telegram", "next": "Звонок", "nextAt": "2026-10-11"},
		map[string]any{"id": "L3", "col": "work", "name": "Берик", "phone": "87015553344", "source": "Instagram"},
	}}
	b, _ := json.Marshal(crm)
	if _, err := repo.PutDoc(ctx, "club", "bs_crm", 0, string(b), false, "test"); err != nil {
		t.Fatal(err)
	}
	slots := map[string]any{"price": 50000, "slots": []any{
		map[string]any{"id": "s1", "start": now.Add(72 * time.Hour).UTC().Format(time.RFC3339), "dur": 60, "format": "онлайн", "status": "free"},
	}}
	b, _ = json.Marshal(slots)
	if _, err := repo.PutDoc(ctx, "club", "bs_slots", 0, string(b), false, "test"); err != nil {
		t.Fatal(err)
	}
	h := NewPlatformHandler(repo)
	a := NewPlatformAuthHandler(testBotToken, "72999:Команда", asSecret)
	a.repo, a.names = repo, h.names
	pm := &PlatformModule{h: h, auth: a, secret: []byte(asSecret), AI: &PlatformAI{repo: repo, AI: &ai.Client{}}}
	pm.Access = NewAssistAccess(repo, a, h.names)
	a.access = pm.Access
	pm.Managers = NewSalesManagers(repo, a, []byte(asSecret))
	pm.Managers.Repo, pm.Managers.AI = repo, pm.AI
	pm.Access.Mgr = pm.Managers
	pm.Inbox = NewQuickInbox(repo, pm.AI)
	e := &mgEnv{mgr: pm.Managers}
	pm.Managers.Send = func(ctx context.Context, chat int64, text string, kb map[string]any) error {
		e.mu.Lock()
		e.sent = append(e.sent, text)
		e.mu.Unlock()
		return nil
	}
	pm.Managers.Admins = []int64{72999}
	middleware.SessionGuard = pm.Access.Guard
	t.Cleanup(func() { middleware.SessionGuard = nil })
	gin.SetMode(gin.TestMode)
	r := gin.New()
	pm.Register(r)
	e.asEnv = &asEnv{r: r, db: db, repo: repo, auth: a, acc: pm.Access}
	return e
}

func r83Lead(t *testing.T, repo *pg.PlatformRepo, id string) map[string]any {
	t.Helper()
	d, _ := repo.GetDoc(context.Background(), "club", "bs_crm")
	var crm map[string]any
	_ = json.Unmarshal([]byte(d.Value), &crm)
	leads, _ := crm["leads"].([]any)
	return findLeadID(leads, id)
}

func TestR83ManagerFlow(t *testing.T) {
	e := newMgEnv(t)
	ctx := context.Background()
	admin := tokenOf(e.login(t, 72999, "Команда"))

	// the owner invites: a one-time link, only its hash is kept
	w := e.call("POST", "/api/v1/platform/managers/invite", admin, map[string]any{"name": "Айгерим"})
	if w.Code != 200 {
		t.Fatalf("invite: %d %s", w.Code, w.Body.String())
	}
	inv := asJSON(t, w)
	link, _ := inv["link"].(string)
	if !strings.HasPrefix(link, "https://app.bxclub.kz/crm/join/") {
		t.Fatalf("link %q", link)
	}
	tok := lastPath(link)
	if d, _ := e.repo.GetDoc(ctx, "server", mgrDocKey); d == nil || strings.Contains(d.Value, tok) || !strings.Contains(d.Value, hashSecretToken(tok)) {
		t.Fatal("the raw invite token is stored")
	}
	if w := e.call("GET", "/crm/join/"+tok, "", nil); w.Code != 200 || !strings.Contains(w.Body.String(), "Приглашение в CRM") {
		t.Fatalf("join page: %d", w.Code)
	}
	if w := e.call("GET", "/api/v1/manager/invite-info?t="+tok, "", nil); w.Code != 200 || !strings.Contains(w.Body.String(), "Айгерим") {
		t.Fatalf("invite info: %d %s", w.Code, w.Body.String())
	}
	// the team cannot take it, the manager accepts with his Telegram
	if w := e.call("POST", "/api/v1/manager/accept", "", map[string]any{"token": tok, "widget": widgetFor(72999, "Команда", "")}); w.Code != http.StatusConflict {
		t.Fatalf("team accept: %d", w.Code)
	}
	w = e.call("POST", "/api/v1/manager/accept", "", map[string]any{"token": tok, "widget": widgetFor(73001, "Айгерим", "aigerim")})
	if w.Code != 200 || !strings.Contains(w.Header().Get("Set-Cookie"), "bs_session=") {
		t.Fatalf("accept: %d %s", w.Code, w.Body.String())
	}
	acc := asJSON(t, w)
	mt := tokenOf(acc)
	if u, _ := acc["user"].(map[string]any); u["role"] != RoleSales {
		t.Fatalf("user %v", acc["user"])
	}
	if w := e.call("POST", "/api/v1/manager/accept", "", map[string]any{"token": tok, "widget": widgetFor(73001, "Айгерим", "")}); w.Code != http.StatusGone {
		t.Fatalf("second accept: %d", w.Code)
	}
	// nothing of the platform but his own API
	for _, p := range []string{"/api/v1/platform/sync", "/api/v1/platform/managers", "/api/v1/platform/ops", "/api/v1/platform/crm/chats", "/api/v1/platform/inbox"} {
		if w := e.call("GET", p, mt, nil); w.Code != http.StatusForbidden {
			t.Fatalf("%s for a manager: %d %s", p, w.Code, w.Body.String())
		}
	}
	if w := e.call("GET", "/api/v1/manager/me", mt, nil); w.Code != 200 || !strings.Contains(w.Body.String(), "Айгерим") {
		t.Fatalf("me: %d %s", w.Code, w.Body.String())
	}
	// the team's token does not open the manager's API
	if w := e.call("GET", "/api/v1/manager/leads", admin, nil); w.Code != http.StatusForbidden {
		t.Fatalf("admin on manager api: %d", w.Code)
	}

	// the pool: new leads without phones; a qualified team lead is not there
	w = e.call("GET", "/api/v1/manager/leads", mt, nil)
	body := w.Body.String()
	if w.Code != 200 || !strings.Contains(body, "Арман Лидов") || strings.Contains(body, "555 11 22") || strings.Contains(body, "Командный Лид") || !strings.Contains(body, "Альфа Резидентова") {
		t.Fatalf("pool: %s", body)
	}
	if w := e.call("GET", "/api/v1/manager/leads/L2", mt, nil); w.Code != http.StatusForbidden {
		t.Fatalf("team lead: %d", w.Code)
	}
	w = e.call("POST", "/api/v1/manager/leads/L1/take", mt, nil)
	if w.Code != 200 || !strings.Contains(w.Body.String(), "555 11 22") {
		t.Fatalf("take: %d %s", w.Code, w.Body.String())
	}
	if l := r83Lead(t, e.repo, "L1"); mgrOf(l) == 0 || l["mgrName"] != "Айгерим" {
		t.Fatalf("taken lead %v", l)
	}

	// required fields of the stages
	check := func(body map[string]any, code int, want string) {
		t.Helper()
		w := e.call("POST", "/api/v1/manager/leads/L1", mt, body)
		if w.Code != code || (want != "" && !strings.Contains(w.Body.String(), want)) {
			t.Fatalf("update %v: %d %s", body, w.Code, w.Body.String())
		}
	}
	check(map[string]any{"col": "qual"}, http.StatusUnprocessableEntity, "следующий шаг")
	check(map[string]any{"col": "qual", "next": "Отправить кейс", "nextAt": "2026-10-12", "nextTime": "15:30", "note": "Две точки, хочет третью"}, 200, "Отправить кейс")
	check(map[string]any{"col": "won"}, http.StatusUnprocessableEntity, "сумму")
	check(map[string]any{"col": "lost"}, http.StatusUnprocessableEntity, "причину")
	check(map[string]any{"col": "lost", "lostReason": "Дорого"}, 200, "Дорого")
	if l := r83Lead(t, e.repo, "L1"); l["col"] != "lost" || l["next"] != nil || !strings.Contains(mustJSONs(l["log"]), "Причина отказа: Дорого") {
		t.Fatalf("lost lead %v", l)
	}
	// a touch is counted
	if w := e.call("POST", "/api/v1/manager/leads/L1/touch", mt, map[string]any{"kind": "call"}); w.Code != 200 {
		t.Fatalf("touch: %d %s", w.Code, w.Body.String())
	}

	// the owner gives L3 to him; the bot tells the manager once
	if w := e.call("POST", "/api/v1/platform/managers/assign", admin, map[string]any{"lead": "L3", "id": 1}); w.Code != 200 {
		t.Fatalf("assign: %d %s", w.Code, w.Body.String())
	}
	e.mgr.Tick(ctx)
	e.mgr.Tick(ctx)
	e.mu.Lock()
	n := 0
	for _, s := range e.sent {
		if strings.Contains(s, "Вам передан лид: Берик") {
			n++
		}
	}
	e.mu.Unlock()
	if n != 1 {
		t.Fatalf("assignment note sent %d times: %v", n, e.sent)
	}
	// booking an express-разбор
	if w := e.call("GET", "/api/v1/manager/slots", mt, nil); w.Code != 200 || !strings.Contains(w.Body.String(), `"id":"s1"`) {
		t.Fatalf("slots: %d %s", w.Code, w.Body.String())
	}
	w = e.call("POST", "/api/v1/manager/leads/L3/book", mt, map[string]any{"slotId": "s1"})
	if w.Code != 200 {
		t.Fatalf("book: %d %s", w.Code, w.Body.String())
	}
	if l := r83Lead(t, e.repo, "L3"); l["col"] != "meet" || l["razborSlot"] != "s1" {
		t.Fatalf("booked lead %v", l)
	}
	if d, _ := e.repo.GetDoc(ctx, "club", "bs_slots"); !strings.Contains(d.Value, `"status":"booked"`) || !strings.Contains(d.Value, `"leadId":"L3"`) {
		t.Fatalf("slot not booked: %s", d.Value)
	}
	if w := e.call("POST", "/api/v1/manager/leads/L3/book", mt, map[string]any{"slotId": "s1"}); w.Code != http.StatusConflict {
		t.Fatalf("double booking: %d", w.Code)
	}
	// a lead of his own
	if w := e.call("POST", "/api/v1/manager/leads", mt, map[string]any{"name": "С улицы", "phone": "+77019990000", "next": "Перезвонить", "nextAt": "2026-10-13"}); w.Code != 200 {
		t.Fatalf("create: %d %s", w.Code, w.Body.String())
	}
	// his numbers and the owner's view with the journal
	w = e.call("GET", "/api/v1/manager/stats", mt, nil)
	var st struct{ Stats mgrStats }
	_ = json.Unmarshal(w.Body.Bytes(), &st)
	if st.Stats.Calls7 != 1 || st.Stats.Taken30 != 1 || st.Stats.Booked30 != 1 || st.Stats.Lost30 != 1 || st.Stats.Active < 2 {
		t.Fatalf("stats %+v", st.Stats)
	}
	w = e.call("GET", "/api/v1/platform/managers", admin, nil)
	if w.Code != 200 || !strings.Contains(w.Body.String(), "Записал на экспресс-разбор") || !strings.Contains(w.Body.String(), `"booked30":1`) {
		t.Fatalf("managers: %d %s", w.Code, w.Body.String())
	}
	// the main login page brings him to the CRM, not to the lead home
	if m := e.login(t, 73001, "Айгерим"); m["user"].(map[string]any)["role"] != RoleSales {
		t.Fatalf("login role %v", m["user"])
	}
	// a personal link without Telegram
	w = e.call("POST", "/api/v1/platform/managers/link", admin, map[string]any{"id": 1})
	plink, _ := asJSON(t, w)["link"].(string)
	if w := e.call("POST", "/api/v1/manager/link", "", map[string]any{"token": lastPath(plink)}); w.Code != 200 {
		t.Fatalf("link login: %d %s", w.Code, w.Body.String())
	}
	// revoked: every token of his stops at once
	if w := e.call("POST", "/api/v1/platform/managers/revoke", admin, map[string]any{"id": 1}); w.Code != 200 {
		t.Fatalf("revoke: %d", w.Code)
	}
	if w := e.call("GET", "/api/v1/manager/me", mt, nil); w.Code != http.StatusUnauthorized {
		t.Fatalf("revoked manager: %d %s", w.Code, w.Body.String())
	}
	if w := e.call("POST", "/api/v1/manager/link", "", map[string]any{"token": lastPath(plink)}); w.Code != http.StatusGone {
		t.Fatalf("revoked link: %d", w.Code)
	}
}

func mustJSONs(v any) string { b, _ := json.Marshal(v); return string(b) }

func TestR83ManagerReminders(t *testing.T) {
	docs := &salesFakeDocs{}
	now := time.Date(2026, 10, 12, 4, 25, 0, 0, time.UTC) // 09:25 Almaty
	s := NewSalesManagers(docs, nil, nil)
	s.Now = func() time.Time { return now }
	var sent []string
	s.Send = func(ctx context.Context, chat int64, text string, kb map[string]any) error {
		sent = append(sent, text)
		return nil
	}
	docs.put(t, "server", mgrDocKey, mgrDoc{Seq: 1, Managers: []SalesMgr{{ID: 1, Name: "Айгерим Б", Tg: 73001}}})
	docs.put(t, "club", "bs_crm", map[string]any{"leads": []any{
		map[string]any{"id": "a", "col": "work", "name": "Арман", "phone": "+77015551122", "mgr": 1, "mgrNotified": 1, "next": "Звонок", "nextAt": "2026-10-12", "nextTime": "09:30"},
		map[string]any{"id": "b", "col": "work", "name": "Олжас", "mgr": 1, "mgrNotified": 1, "next": "Кейс", "nextAt": "2026-10-10"},
	}})
	s.Tick(context.Background())
	s.Tick(context.Background())
	all := strings.Join(sent, "\n---\n")
	if strings.Count(all, "⏰ 09:30: Звонок") != 1 || strings.Count(all, "План на сегодня, Айгерим") != 1 || !strings.Contains(all, "Просрочено: 1") {
		t.Fatalf("reminders: %s", all)
	}
	// a step changed: the reminder comes again for the new time
	now = now.Add(2 * time.Hour) // 11:25
	_ = r83Mutate(context.Background(), docs, "club", "bs_crm", "t", func(c *map[string]any) bool {
		l := findLeadID((*c)["leads"].([]any), "a")
		l["nextTime"] = "11:30"
		return true
	})
	s.Tick(context.Background())
	if !strings.Contains(strings.Join(sent, "\n"), "⏰ 11:30") {
		t.Fatalf("no reminder for the new time: %v", sent)
	}
}

// ── R83: the next cycle after an offline разбор ──

func cyDate(s string) time.Time {
	d, _ := time.ParseInLocation("2006-01-02", s, club.Almaty)
	return d
}

func cySnap() *club.Snapshot {
	return &club.Snapshot{
		Residents: []club.Resident{
			{Name: "Асет", Format: "Офлайн"}, {Name: "Дамил", Format: "Офлайн"}, {Name: "Бывший", Format: "Офлайн", Former: true},
			{Name: "Мади Актобе", Format: "Онлайн"}, {Name: "Даулет", Format: "Онлайн"}, {Name: "Рустам", Format: "Онлайн", Admin: true},
		},
		Meetings: []club.Meeting{
			{Resident: "Асет", Date: cyDate("2026-10-08"), Time: "11:00", Place: "Офис"},
			{Resident: "Дамил", Date: cyDate("2026-10-08"), Time: "11:00", Place: "Офис"},
			{Resident: "Бывший", Date: cyDate("2026-10-08"), Time: "11:00"},
			{Resident: "Мади Актобе", Date: cyDate("2026-10-06"), Time: "15:00", Online: true, Link: "https://app.bxclub.kz/call/x"},
			{Resident: "Даулет", Date: cyDate("2026-10-06"), Time: "15:00", Online: true, Link: "https://app.bxclub.kz/call/y"},
			{Resident: "Даулет", Date: cyDate("2026-10-20"), Time: "12:00", Online: true}, // already planned
		},
	}
}

func TestR83CyclePropose(t *testing.T) {
	now := time.Date(2026, 10, 10, 8, 0, 0, 0, club.Almaty)
	group, items := Propose(cySnap(), "2026-10-08", now)
	if strings.Join(group, ",") != "Асет,Бывший,Дамил" {
		t.Fatalf("group %v", group)
	}
	got := map[string]CyItem{}
	for _, it := range items {
		got[it.Res] = it
	}
	// the offline day 10 days later at the same time (18.10 is a Sunday → Monday 19.10)
	if a := got["Асет"]; a.Kind != "offline" || a.Date != "2026-10-19" || a.Time != "11:00" || got["Дамил"].Date != "2026-10-19" {
		t.Fatalf("offline %v", got)
	}
	if _, ok := got["Бывший"]; ok {
		t.Fatal("a former resident gets a meeting")
	}
	// online: 10 days after the last meeting, the usual time; Даулет is planned already, the owner never
	if m := got["Мади Актобе"]; m.Kind != "online" || m.Date != "2026-10-16" || m.Time != "15:00" {
		t.Fatalf("online %v", m)
	}
	if _, ok := got["Даулет"]; ok {
		t.Fatal("Даулет has his next meeting already")
	}
	if _, ok := got["Рустам"]; ok {
		t.Fatal("the team is not a resident")
	}
	// a taken hour moves the proposal
	s := cySnap()
	s.Meetings = append(s.Meetings, club.Meeting{Resident: "Гость", Date: cyDate("2026-10-16"), Time: "15:00"})
	_, items = Propose(s, "2026-10-08", now)
	for _, it := range items {
		if it.Res == "Мади Актобе" && it.Time != "16:00" {
			t.Fatalf("busy hour not skipped: %v", it)
		}
	}
}

func TestR83CycleTaskFlow(t *testing.T) {
	docs := &salesFakeDocs{}
	now := time.Date(2026, 10, 10, 10, 30, 0, 0, club.Almaty) // 47.5 h after the offline day
	type msg struct {
		text string
		kb   map[string]any
	}
	var sent []msg
	var edits []string
	var writes []map[string]string
	p := &CyclePlanner{Docs: docs, Owner: 777, Team: map[int64]string{777: "Рустам"},
		Load: func(ctx context.Context) (*club.Snapshot, error) { return cySnap(), nil },
		Write: func(ctx context.Context, action string, prm map[string]string) error {
			if action != "addSchedule" {
				t.Fatalf("action %s", action)
			}
			writes = append(writes, prm)
			return nil
		},
		Send: func(ctx context.Context, chat int64, text string, kb map[string]any) (int64, error) {
			sent = append(sent, msg{text, kb})
			return int64(100 + len(sent)), nil
		},
		Edit: func(ctx context.Context, chat, id int64, text string, kb map[string]any) error {
			edits = append(edits, text)
			return nil
		},
		Now: func() time.Time { return now },
	}
	ctx := context.Background()
	if err := p.Tick(ctx); err != nil || len(sent) != 0 {
		t.Fatalf("too early: %v %d", err, len(sent))
	}
	now = now.Add(time.Hour) // 48.5 h
	_ = p.Tick(ctx)
	_ = p.Tick(ctx)
	if len(sent) != 1 || !strings.Contains(sent[0].text, "Утвердите время следующих встреч (онлайн и офлайн)") ||
		!strings.Contains(sent[0].text, "🏢 Офлайн: пн, 19.10, 11:00") || !strings.Contains(sent[0].text, "• Мади Актобе: пт, 16.10, 15:00") {
		t.Fatalf("task: %d %v", len(sent), sent)
	}
	kb := mustJSONs(sent[0].kb)
	if !strings.Contains(kb, `"callback_data":"cy:ok:off20261008"`) || !strings.Contains(kb, "/cycle/off20261008") || !strings.Contains(kb, "Другое время") {
		t.Fatalf("buttons %s", kb)
	}
	// not answered: again in 24 hours
	now = now.Add(25 * time.Hour)
	_ = p.Tick(ctx)
	if len(sent) != 2 || !strings.Contains(sent[1].text, "Напоминаю") {
		t.Fatalf("reminder: %v", sent)
	}
	// «Утвердить»: the meetings are created, the message says so
	toast, ok := p.HandleCallback(ctx, bot.CallbackUpdate{Data: "cy:ok:off20261008", FromID: 777, ChatID: 777, MessageID: 102})
	if !ok || toast != "Встречи созданы" || len(writes) != 3 {
		t.Fatalf("approve: %q %v %v", toast, ok, writes)
	}
	if writes[0]["date"] == "" || !strings.Contains(mustJSONs(writes), `"date":"19.10.2026","res":"Асет","time":"11:00"`) || !strings.Contains(mustJSONs(writes), `"date":"16.10.2026","res":"Мади Актобе","time":"15:00"`) {
		t.Fatalf("writes %v", writes)
	}
	if len(edits) == 0 || !strings.Contains(edits[0], "✅ Время следующих встреч утверждено") {
		t.Fatalf("edits %v", edits)
	}
	// once: a second press creates nothing, no more reminders
	_, _ = p.HandleCallback(ctx, bot.CallbackUpdate{Data: "cy:ok:off20261008", FromID: 777, ChatID: 777, MessageID: 101})
	now = now.Add(30 * time.Hour)
	_ = p.Tick(ctx)
	if len(writes) != 3 || len(sent) != 2 {
		t.Fatalf("after approval: %d writes, %d sent", len(writes), len(sent))
	}
}

func TestR83CyclePage(t *testing.T) {
	docs := &salesFakeDocs{}
	docs.put(t, "server", cyDocKey, cyDoc{Tasks: []CyTask{{ID: "off20261008", OffDate: "2026-10-08", Status: "pending",
		Items: []CyItem{{Res: "Асет", Kind: "offline", Date: "2026-10-19", Time: "11:00"}}}}})
	var writes []map[string]string
	p := &CyclePlanner{Docs: docs, Owner: 777, Team: map[int64]string{72999: "Команда"}, BotToken: testBotToken, JWT: []byte(asSecret),
		Write: func(ctx context.Context, action string, prm map[string]string) error { writes = append(writes, prm); return nil }}
	gin.SetMode(gin.TestMode)
	r := gin.New()
	p.Register(r)
	call := func(method, path, init string, body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		if init != "" {
			req.Header.Set("X-TG-Init", init)
		}
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		return w
	}
	if w := call("GET", "/cycle/off20261008", "", ""); w.Code != 200 || w.Header().Get("X-Frame-Options") != "" {
		t.Fatalf("page: %d %v", w.Code, w.Header())
	}
	if w := call("GET", "/api/v1/cycle/off20261008", "", ""); w.Code != http.StatusUnauthorized {
		t.Fatalf("no proof: %d", w.Code)
	}
	if w := call("GET", "/api/v1/cycle/off20261008", initDataFor(72001, "Резидент"), ""); w.Code != http.StatusUnauthorized {
		t.Fatalf("not the team: %d", w.Code)
	}
	init := initDataFor(72999, "Команда")
	if w := call("GET", "/api/v1/cycle/off20261008", init, ""); w.Code != 200 || !strings.Contains(w.Body.String(), "Асет") {
		t.Fatalf("task: %d %s", w.Code, w.Body.String())
	}
	if w := call("POST", "/api/v1/cycle/off20261008/approve", init, `{"items":[{"res":"Асет","kind":"offline","date":"2026-10-21","time":"25:00"}]}`); w.Code != http.StatusBadRequest {
		t.Fatalf("bad time accepted: %d", w.Code)
	}
	w := call("POST", "/api/v1/cycle/off20261008/approve", init, `{"items":[{"res":"Асет","kind":"offline","date":"2026-10-21","time":"12:00"}]}`)
	if w.Code != 200 || len(writes) != 1 || writes[0]["date"] != "21.10.2026" || writes[0]["time"] != "12:00" {
		t.Fatalf("approve: %d %s %v", w.Code, w.Body.String(), writes)
	}
}

// ── R83: «Быстрая заметка» ──

func TestR83Inbox(t *testing.T) {
	docs := &salesFakeDocs{}
	q := &QuickInbox{Docs: docs, Names: func(tg int64) string { return map[int64]string{72999: "Рустам"}[tg] }}
	gin.SetMode(gin.TestMode)
	r := gin.New()
	role := "admin"
	g := r.Group("/api/v1/platform", func(c *gin.Context) { c.Set("role", role); c.Set("userID", "tg:72999"); c.Next() })
	q.Register(g)
	call := func(method, path, body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		return w
	}
	if w := call("POST", "/api/v1/platform/inbox", `{"text":"  "}`); w.Code != http.StatusBadRequest {
		t.Fatalf("empty note: %d", w.Code)
	}
	w := call("POST", "/api/v1/platform/inbox", `{"text":"Позвонить бухгалтеру — про НДС"}`)
	if w.Code != 200 || strings.Contains(w.Body.String(), "—") || !strings.Contains(w.Body.String(), `"byName":"Рустам"`) {
		t.Fatalf("create: %d %s", w.Code, w.Body.String())
	}
	// a message forwarded to the bot lands in the same inbox
	reply, ok := q.FromBot(context.Background(), bot.InboxNote{FromID: 72999, FromName: "Рустам", Text: "Идея: клуб для жён", Forward: "Береке"})
	if !ok || !strings.Contains(reply, "Быстрые заметки") {
		t.Fatalf("bot: %q %v", reply, ok)
	}
	if _, ok := q.FromBot(context.Background(), bot.InboxNote{FromID: 72999}); ok {
		t.Fatal("an empty message is kept")
	}
	w = call("GET", "/api/v1/platform/inbox", "")
	var l struct{ Notes []QNote }
	_ = json.Unmarshal(w.Body.Bytes(), &l)
	if len(l.Notes) != 2 || l.Notes[0].Src != "telegram" || l.Notes[0].From != "Береке" || l.Notes[1].Src != "app" {
		t.Fatalf("list: %s", w.Body.String())
	}
	id := l.Notes[0].ID
	if w := call("POST", "/api/v1/platform/inbox/"+id, `{"status":"done","to":"task","toName":"Цели и задачи"}`); w.Code != 200 || !strings.Contains(w.Body.String(), `"status":"done"`) {
		t.Fatalf("sort: %d %s", w.Code, w.Body.String())
	}
	if w := call("DELETE", "/api/v1/platform/inbox/"+id, ""); w.Code != 200 {
		t.Fatalf("delete: %d", w.Code)
	}
	// residents and assistants never see it
	role = "resident"
	if w := call("GET", "/api/v1/platform/inbox", ""); w.Code != http.StatusForbidden {
		t.Fatalf("resident: %d", w.Code)
	}
}

// ── R83: WhatsApp: the keys on the platform, the reason in words ──

func TestR83WAStatusInWords(t *testing.T) {
	t.Setenv("GREEN_API_ID", "")
	t.Setenv("GREEN_API_TOKEN", "")
	waSaved.Store(nil)
	t.Cleanup(func() { waSaved.Store(nil) })
	state := "notAuthorized"
	code := 200
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if code != 200 {
			w.WriteHeader(code)
			_, _ = w.Write([]byte(`{"message":"Unauthorized"}`))
			return
		}
		switch {
		case strings.Contains(r.URL.Path, "/getStateInstance/"):
			_, _ = w.Write([]byte(`{"stateInstance":"` + state + `"}`))
		case strings.Contains(r.URL.Path, "/qr/"):
			_, _ = w.Write([]byte(`{"type":"qrCode","message":"iVBORw0KGgo="}`))
		default:
			w.WriteHeader(404)
		}
	}))
	defer srv.Close()
	h := &PlatformAI{AI: &ai.Client{HTTP: srv.Client()}}
	ctx := context.Background()
	v := h.waStatusView(ctx)
	if v["connected"] != false || v["step"] != "keys" || !strings.Contains(v["reason"].(string), "ещё не подключали") || strings.Contains(mustJSONs(v), "GREEN_API") {
		t.Fatalf("no keys: %v", v)
	}
	waSaved.Store(&greenAPI{id: "7103123456", token: "tok", base: srv.URL})
	if v := h.waStatusView(ctx); v["step"] != "qr" || !strings.Contains(v["hint"].(string), "QR") || v["instance"] != "••••••3456" || v["source"] != "platform" {
		t.Fatalf("not linked: %v", v)
	}
	state = "authorized"
	if v := h.waStatusView(ctx); v["connected"] != true {
		t.Fatalf("linked: %v", v)
	}
	code = 401
	if v := h.waStatusView(ctx); v["step"] != "keys" || v["reason"] != "Ключи не подходят" || strings.Contains(mustJSONs(v), "Green-API 401") {
		t.Fatalf("bad keys: %v", v)
	}
	// the QR code comes to the card
	code = 200
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.GET("/qr", func(c *gin.Context) { c.Set("role", "admin"); h.WAQR(c) })
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest("GET", "/qr", nil))
	if !strings.Contains(w.Body.String(), `"png":"iVBORw0KGgo="`) {
		t.Fatalf("qr: %s", w.Body.String())
	}
	// the Railway variables still win
	t.Setenv("GREEN_API_ID", "1")
	t.Setenv("GREEN_API_TOKEN", "2")
	if waSourceOf() != "railway" || greenFromEnv().id != "1" {
		t.Fatal("env must win")
	}
}

func initDataFor(id int64, first string) string {
	u, _ := json.Marshal(map[string]any{"id": id, "first_name": first})
	f := map[string]string{"user": string(u), "auth_date": strconv.FormatInt(time.Now().Unix(), 10)}
	v := url.Values{}
	for k, x := range f {
		v.Set(k, x)
	}
	v.Set("hash", signInitData(f, testBotToken))
	return v.Encode()
}
