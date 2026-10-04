package http

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/bnursik/business_surgery_backend/internal/bot"
	"github.com/bnursik/business_surgery_backend/internal/club"
	"github.com/bnursik/business_surgery_backend/internal/content"
	pg "github.com/bnursik/business_surgery_backend/internal/repository/pg"
	"github.com/gin-gonic/gin"
)

// R32c: the sheet is off. A day of the club with the Apps Script unreachable:
// reports, the 22:00 reminder, the 10:00 check and its fines, a payment, a
// meeting, an offline day (the broadcast), a new resident's offer and
// onboarding, the app's bundle and roles, a lead from a form. Everything
// works, and not one request reaches the script.

// tgRecorder is Telegram: every call is kept.
type tgRecorder struct {
	mu    sync.Mutex
	calls []map[string]any // {"_m": method, …params}
	hook  string
}

func (f *tgRecorder) handler(w http.ResponseWriter, r *http.Request) {
	method := r.URL.Path[strings.LastIndex(r.URL.Path, "/")+1:]
	var p map[string]any
	_ = json.NewDecoder(r.Body).Decode(&p)
	if p == nil {
		p = map[string]any{}
	}
	p["_m"] = method
	f.mu.Lock()
	f.calls = append(f.calls, p)
	if method == "setWebhook" {
		f.hook, _ = p["url"].(string)
	}
	hook := f.hook
	f.mu.Unlock()
	switch method {
	case "getWebhookInfo":
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "result": map[string]any{"url": hook}})
	case "getUserProfilePhotos":
		_, _ = w.Write([]byte(`{"ok":true,"result":{"total_count":0,"photos":[]}}`))
	default:
		_, _ = w.Write([]byte(`{"ok":true,"result":{"message_id":1}}`))
	}
}

// to: the texts sent to chat (sendMessage), in order.
func (f *tgRecorder) to(chat int64) []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []string
	for _, c := range f.calls {
		if c["_m"] != "sendMessage" {
			continue
		}
		if id, _ := c["chat_id"].(float64); int64(id) == chat {
			s, _ := c["text"].(string)
			out = append(out, s)
		}
	}
	return out
}

func (f *tgRecorder) count(method string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, c := range f.calls {
		if c["_m"] == method {
			n++
		}
	}
	return n
}

func (f *tgRecorder) waitText(t *testing.T, chat int64, part string) string {
	t.Helper()
	for i := 0; i < 300; i++ {
		for _, s := range f.to(chat) {
			if strings.Contains(s, part) {
				return s
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("no message to %d with %q; got %q", chat, part, f.to(chat))
	return ""
}

type offEnv struct {
	t      *testing.T
	db     *pg.DB
	repo   *pg.ClubRepo
	svc    *bot.Service
	g      *AppGateway
	writes *ClubWrites
	cut    *SheetCutover
	r      *gin.Engine
	tg     *tgRecorder
	script *int64 // requests that reached the script
}

const (
	offOwner  = int64(453800951)
	offAset   = int64(478757502)
	offAltair = int64(490685605)
)

func newOffEnv(t *testing.T, master string, imported *time.Time) *offEnv {
	t.Cleanup(club.SetSheetMode(club.SheetModeOff))
	db, repo := clubTestDB(t)
	ctx := context.Background()
	if _, err := db.Pool.Exec(ctx, `TRUNCATE bot_updates, bot_meta, bot_reports, bot_shadow_days`); err != nil {
		t.Fatal(err)
	}
	// The other tests expect the sheet as the master: put it back afterwards.
	t.Cleanup(func() { _ = repo.SetMaster(context.Background(), "sheet") })
	if err := repo.SetMaster(ctx, "sheet"); err != nil {
		t.Fatal(err)
	}
	snap := sheetSnap()
	snap.Raw[club.SheetScriptProps] = [][]string{} // the app's sections came over (step 3)
	if err := repo.ReplaceAll(ctx, snap, "sheet"); err != nil {
		t.Fatal(err)
	}
	if imported != nil {
		if _, err := db.Pool.Exec(ctx, `INSERT INTO club_imports (at, by, dry_run, raw, report) VALUES ($1,'sheet',false,'{}','{}')`, *imported); err != nil {
			t.Fatal(err)
		}
	}
	if err := repo.SetMaster(ctx, master); err != nil {
		t.Fatal(err)
	}
	var hits int64
	script := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt64(&hits, 1)
		_, _ = w.Write([]byte(`{"ok":true,"version":"2026-10-03-39"}`))
	}))
	t.Cleanup(script.Close)
	tg := &tgRecorder{}
	tgSrv := httptest.NewServer(http.HandlerFunc(tg.handler))
	t.Cleanup(tgSrv.Close)

	svc := bot.New(pg.NewBotRepo(db), bot.Options{Token: testBotToken, APIBase: tgSrv.URL, Admins: []int64{offOwner},
		Workers: 2, PublicURL: "https://srv.example.kz", TestClock: true})
	// The script's addresses are still on record from before: never used now.
	if err := svc.SetRelayURL(ctx, script.URL+"/exec"); err != nil {
		t.Fatal(err)
	}
	g := NewAppGateway(testBotToken, script.URL+"/exec")
	g.Admins = map[int64]string{offOwner: "Рустам"}
	g.TGBase = tgSrv.URL
	g.Club, g.Ops, g.Done = repo, repo, repo
	g.Fallback = func() string { return script.URL + "/exec2" }
	w := NewClubWrites(repo, g)
	g.Writes = w
	m := NewBundleMigration(g, repo, pg.NewBotRepo(db), nil, w)
	_ = m
	cut := NewSheetCutover(repo, pg.NewBotRepo(db))
	w.Cutover = cut
	w.Notify = &WriteNotify{Send: svc.SendMessageKB, Topic: svc.SendTopic, Admins: []int64{offOwner}}
	g.Contact, g.Photo = svc.RequestContact, svc.SendPhotoKB
	svc.SetFineSink(func(ctx context.Context, fines []bot.FineRow) ([]string, error) {
		var added []string
		for _, f := range fines {
			day, _ := club.Date(f.Date)
			if have, err := repo.FineExists(ctx, f.Name, f.Type, day); err != nil || have {
				continue
			}
			if _, err := w.Local(ctx, "bot", 0, "Бот", "addFine", map[string]string{"name": f.Name, "type": f.Type,
				"amount": strconv.FormatInt(f.Amount, 10), "date": f.Date}); err != nil {
				return added, err
			}
			added = append(added, f.Name)
		}
		return added, nil
	})
	svc.SetNoteSink(func(ctx context.Context, tg int64, who, action string, p map[string]string) error {
		_, err := w.Local(ctx, "bot", tg, who, action, p)
		return err
	})
	ch := NewClubHandler(repo, nil, testBotToken, "test-secret")
	ch.Cutover = cut
	gin.SetMode(gin.TestMode)
	r := gin.New()
	NewAppGatewayModule(g).Register(r)
	NewBotModule(NewBotHandler(svc, "")).Register(r)
	NewClubModule(ch).Register(r)
	c, cancel := context.WithCancel(ctx)
	t.Cleanup(cancel)
	if err := cut.Begin(ctx); err != nil {
		t.Fatal(err)
	}
	svc.Start(c)
	go w.Loop(c)
	return &offEnv{t: t, db: db, repo: repo, svc: svc, g: g, writes: w, cut: cut, r: r, tg: tg, script: &hits}
}

func (e *offEnv) hook(body string) {
	e.t.Helper()
	req := httptest.NewRequest("POST", "/api/v1/bot/webhook", strings.NewReader(body))
	req.Header.Set("X-Telegram-Bot-Api-Secret-Token", e.svc.WebhookSecret())
	w := httptest.NewRecorder()
	e.r.ServeHTTP(w, req)
	if w.Code != 200 {
		e.t.Fatalf("webhook %d", w.Code)
	}
}

func (e *offEnv) call(id int64, action string, kv ...string) map[string]any {
	q := url.Values{"action": {action}, "_tg": {makeInitData(testBotToken, id, "Тест", time.Now())}}
	for i := 0; i+1 < len(kv); i += 2 {
		q.Set(kv[i], kv[i+1])
	}
	w := get(e.r, q)
	var out map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &out)
	if out == nil {
		out = map[string]any{"_raw": w.Body.String(), "_code": w.Code}
	}
	return out
}

func (e *offEnv) n(q string, args ...any) int {
	e.t.Helper()
	var n int
	if err := e.db.Pool.QueryRow(context.Background(), q, args...).Scan(&n); err != nil {
		e.t.Fatal(err)
	}
	return n
}

func groupMsg(id int64, from int64, first string, at time.Time, text string) string {
	return fmt.Sprintf(`{"update_id":%d,"message":{"message_id":%d,"date":%d,"chat":{"id":%d,"type":"supergroup"},"message_thread_id":9,"is_topic_message":true,"from":{"id":%d,"first_name":%q},"text":%q}}`,
		id, id, at.Unix(), bot.DefaultGroupID, from, first, text)
}

func privMsg(id int64, from int64, text string) string {
	return fmt.Sprintf(`{"update_id":%d,"message":{"message_id":%d,"date":%d,"chat":{"id":%d,"type":"private"},"from":{"id":%d,"first_name":"Дана"},"text":%q}}`,
		id, id, time.Now().Unix(), from, from, text)
}

func TestSheetOffDayWithoutScript(t *testing.T) {
	e := newOffEnv(t, "sheet", nil) // no import for days: the server becomes the master at once
	ctx := context.Background()
	if m, _ := e.repo.Master(ctx); m != "server" {
		t.Fatalf("master %q, want server", m)
	}
	// Telegram is pointed at the server by the server itself.
	for i := 0; i < 200 && e.tg.count("setWebhook") == 0; i++ {
		time.Sleep(20 * time.Millisecond)
	}
	if e.tg.hook != "https://srv.example.kz/api/v1/bot/webhook" {
		t.Fatalf("webhook %q", e.tg.hook)
	}

	a := time.Now().In(club.Almaty)
	today := time.Date(a.Year(), a.Month(), a.Day(), 0, 0, 0, 0, club.Almaty)
	day := today.AddDate(0, 0, -1)
	// The server has had the bot's messages for days (full coverage of the day checked).
	if _, err := e.db.Pool.Exec(ctx, `INSERT INTO bot_updates (update_id, kind, body, received_at, relayed_at) VALUES (1, 'other', '{}', $1, $1)`,
		day.AddDate(0, 0, -2)); err != nil {
		t.Fatal(err)
	}

	// 1. Reports in the group, yesterday: Асет a full one, Альтаир a short one.
	report := "Отчёт за день: сделал три звонка клиентам, закрыл сделку на 500 000, провёл планёрку с командой. Не получилось: запустить рекламу. Завтра: запуск рекламы и найм менеджера."
	e.hook(groupMsg(100, offAset, "Асет", day.Add(15*time.Hour), report))
	e.hook(groupMsg(101, offAltair, "Альтаир", day.Add(16*time.Hour), "Сегодня был занят"))
	e.tg.waitText(t, offAltair, "слишком короткое")
	for i := 0; i < 200 && e.tg.count("setMessageReaction") == 0; i++ {
		time.Sleep(20 * time.Millisecond)
	}
	if e.tg.count("setMessageReaction") != 1 {
		t.Fatalf("reaction on the report: %d", e.tg.count("setMessageReaction"))
	}
	if n := e.n(`SELECT count(*) FROM club_reports WHERE name = 'Асет' AND tg_user_id = $1`, offAset); n != 1 {
		t.Fatalf("report log: %d rows", n)
	}

	// 2. 22:00: the reminder goes to Альтаир only.
	res := e.svc.Tick(ctx, day.Add(22*time.Hour+5*time.Minute))
	if len(res.Evening) != 1 || res.Evening[0] != "Альтаир" {
		t.Fatalf("evening %v %v", res.Evening, res.Errors)
	}

	// 3. 10:00 next day: the check fines Альтаир in the server's tables, tells him and the team.
	res = e.svc.Tick(ctx, today.Add(10*time.Hour+30*time.Minute))
	if res.Daily == nil || len(res.Daily.Fined) != 1 || res.Daily.Fined[0] != "Альтаир" {
		t.Fatalf("daily %+v %v", res.Daily, res.Errors)
	}
	if n := e.n(`SELECT count(*) FROM club_fines WHERE resident = 'Альтаир' AND type = 'Не сдан отчёт' AND date = $1`, day.Format("2006-01-02")); n != 1 {
		t.Fatalf("fine rows %d", n)
	}
	e.tg.waitText(t, offAltair, "штраф за несдачу отчёта")
	e.tg.waitText(t, offOwner, "Проверка отчётов за "+day.Format("02.01.2006"))
	if again := e.svc.Tick(ctx, today.Add(11*time.Hour)); again.Daily != nil {
		t.Fatal("the check ran twice")
	}

	// 4. The app: the bundle and roles come from the server.
	for id, want := range map[int64]string{offOwner: "admin", offAset: "resident", 999: "lead"} {
		if r := e.call(id, "checkUserRole"); r["role"] != want {
			t.Fatalf("role of %d: %v", id, r)
		}
	}
	b := e.call(offAset, "getBotCache")
	if _, ok := b["residents"]; !ok || b["_partial"] != nil {
		t.Fatalf("bundle %v", b)
	}

	// 5. A payment of the fine: on the server, the fine is closed.
	if r := e.call(offAset, "addFine", "name", "Альтаир", "amount", "10000"); r["error"] == nil {
		t.Fatalf("a resident wrote a fine: %v", r)
	}
	if r := e.call(offOwner, "addPayment", "type", "income", "amount", "10000", "src", "БХ Штраф", "resident", "Альтаир", "isCash", "true"); r["ok"] != true {
		t.Fatalf("payment %v", r)
	}
	if n := e.n(`SELECT count(*) FROM club_payments WHERE resident = 'Альтаир' AND income = 10000`); n != 1 {
		t.Fatalf("payment rows %d", n)
	}
	if n := e.n(`SELECT count(*) FROM club_fines WHERE resident = 'Альтаир' AND date = $1`, day.Format("2006-01-02")); n != 0 {
		t.Fatalf("the paid fine is still open")
	}

	// 6. A meeting: kept, the resident is told; a second one that day is a double.
	md := today.AddDate(0, 0, 3).Format("02.01.2006")
	if r := e.call(offOwner, "addSchedule", "res", "Асет", "date", md, "time", "12:00"); r["ok"] != true || r["duplicate"] == true {
		t.Fatalf("meeting %v", r)
	}
	e.tg.waitText(t, offAset, "Назначена встреча")
	if r := e.call(offOwner, "addSchedule", "res", "Асет", "date", md, "time", "15:00"); r["duplicate"] != true {
		t.Fatalf("double meeting %v", r)
	}

	// 7. The broadcast: an offline day goes to the group's «ВАЖНОЕ» and to each offline resident.
	od := today.AddDate(0, 0, 5).Format("02.01.2006")
	if r := e.call(offOwner, "addOfflineGroup", "date", od, "time", "11:00"); r["ok"] != true || r["count"] != float64(2) {
		t.Fatalf("offline day %v", r)
	}
	e.tg.waitText(t, offAltair, "Офлайн встреча")
	e.tg.mu.Lock()
	topic := false
	for _, c := range e.tg.calls {
		if c["_m"] == "sendMessage" && c["message_thread_id"] == float64(ImportantTopic) && strings.Contains(fmt.Sprint(c["text"]), "Офлайн день BS") {
			topic = true
		}
	}
	e.tg.mu.Unlock()
	if !topic {
		t.Fatal("no announcement in the group")
	}

	// 8. A new resident: added in the app, /start, the offer, onboarding day by day.
	const dana = int64(777000111)
	if r := e.call(offOwner, "addResident", "name", "Дана", "residentChatId", strconv.FormatInt(dana, 10), "format", "Онлайн", "debt", "300000"); r["ok"] != true {
		t.Fatalf("add resident %v", r)
	}
	e.hook(privMsg(200, dana, "/start"))
	e.tg.waitText(t, dana, "Публичная оферта")
	e.hook(fmt.Sprintf(`{"update_id":201,"callback_query":{"id":"cb1","data":"accept_terms","from":{"id":%d,"first_name":"Дана"},"message":{"message_id":5,"chat":{"id":%d,"type":"private"}}}}`, dana, dana))
	e.tg.waitText(t, dana, "Условия приняты")
	if n := e.n(`SELECT count(*) FROM club_sheets WHERE name = 'Акцепты' AND rows::text LIKE '%' || $1 || '%'`, strconv.FormatInt(dana, 10)); n != 1 {
		t.Fatal("acceptance not kept")
	}
	res = e.svc.Tick(ctx, today.Add(11*time.Hour))
	if len(res.Onboarding) != 1 {
		t.Fatalf("onboarding day 1: %v %v", res.Onboarding, res.Errors)
	}
	e.tg.waitText(t, dana, "День 1️⃣")
	if r := e.svc.Tick(ctx, today.Add(15*time.Hour)); len(r.Onboarding) != 0 {
		t.Fatal("day 2 came the same day")
	}
	if r := e.svc.Tick(ctx, today.AddDate(0, 0, 1).Add(10*time.Hour+5*time.Minute)); len(r.Onboarding) != 1 {
		t.Fatalf("onboarding day 2: %v", r.Onboarding)
	}
	e.tg.waitText(t, dana, "День 2️⃣")
	e.hook(privMsg(202, dana, "/start"))
	e.tg.waitText(t, dana, "с возвращением")

	// 9. A resident's long message is kept for CustDev; /status answers from the server.
	e.hook(privMsg(203, dana, "Хочу обсудить на встрече найм первого менеджера по продажам и мотивацию"))
	e.hook(privMsg(204, dana, "/status"))
	e.tg.waitText(t, dana, "Общий долг")
	if r := e.call(offOwner, "getCustdevResponses"); fmt.Sprint(r["responses"]) == "[]" {
		t.Fatalf("custdev %v", r)
	}

	// 10. A lead from the site form, passed on by the dormant script.
	body, _ := json.Marshal(map[string]any{"ts": time.Now().Unix(), "lead": map[string]string{"name": "Ерлан", "phone": "77011234567", "source": "Tilda: заявка"}})
	req := httptest.NewRequest("POST", "/api/v1/bot/lead", bytes.NewReader(body))
	req.Header.Set("X-BS-Signature", sign(body))
	w := httptest.NewRecorder()
	e.r.ServeHTTP(w, req)
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"ok":true`) {
		t.Fatalf("lead %d %s", w.Code, w.Body.String())
	}
	e.tg.waitText(t, offOwner, "Новый лид")
	if n := e.n(`SELECT count(*) FROM club_sheets WHERE name = 'CRM Лиды' AND rows::text LIKE '%Ерлан%'`); n != 1 {
		t.Fatal("lead not kept")
	}

	// 11. Functions that lived next to the sheet work without the script
	// (R32d: an event goes into the schedule, the calendar gets a link).
	if r := e.call(offOwner, "createEvent", "name", "МК", "date", md, "time", "18:00"); r["ok"] != true || r["calendarUrl"] == nil {
		t.Fatalf("createEvent %v", r)
	}
	if r := e.call(offOwner, "getMonthlyPL"); r["ok"] != true {
		t.Fatalf("PL %v", r)
	}
	if r := e.call(offAset, "getOfflineResidents"); r["ok"] != true || r["offlineCount"] != float64(2) {
		t.Fatalf("offline residents %v", r)
	}
	if r := e.call(offAset, "saveProfit", "name", "Асет", "revenue", "5000000", "profit", "1200000"); r["ok"] != true {
		t.Fatalf("profit %v", r)
	}
	if r := e.call(offAset, "getProfit", "name", "Асет"); fmt.Sprint(r["records"]) != fmt.Sprintf("[map[date:%s profit:1.2e+06 revenue:5e+06]]", time.Now().In(club.Almaty).Format("02.01.06")) {
		t.Fatalf("getProfit %v", r)
	}

	// Every write is done on the server; nothing waits for the sheet.
	if n := e.n(`SELECT count(*) FROM club_writes WHERE status IN ('pending','unknown')`); n != 0 {
		t.Fatalf("%d writes wait for the sheet", n)
	}
	if n := atomic.LoadInt64(e.script); n != 0 {
		t.Fatalf("the script was called %d times", n)
	}
	// The bot does not relay: the updates are answered by the server.
	if n := e.n(`SELECT count(*) FROM bot_updates WHERE relayed_at IS NULL AND update_id > 1`); n != 0 {
		t.Fatalf("%d updates not handled", n)
	}
}

// The cutover: the sheet still fed the server, so the server waits for the
// final import; a write made meanwhile survives it; then the sheet's imports
// are refused.
func TestSheetCutoverFinalImport(t *testing.T) {
	recent := time.Now().Add(-30 * time.Minute)
	e := newOffEnv(t, "sheet", &recent)
	ctx := context.Background()
	if !e.cut.Pending(ctx) {
		t.Fatal("not waiting for the final import")
	}
	// The bot's control answer keeps the old script importing meanwhile.
	ctl := e.signed("/api/v1/bot/control", map[string]any{"version": "2026-10-03-39"})
	if ctl["master"] != "sheet" || ctl["sheetMode"] != "off" {
		t.Fatalf("control %v", ctl)
	}
	if r := e.call(offOwner, "addFine", "name", "Асет", "amount", "5000", "type", "Опоздание"); r["ok"] != true {
		t.Fatalf("fine %v", r)
	}
	if n := e.n(`SELECT count(*) FROM club_writes WHERE status = 'pending'`); n != 1 {
		t.Fatalf("the write must wait for the final import, %d pending", n)
	}
	sheets := func(final bool, extra map[string][][]string) map[string]any {
		s := club.Sheets{
			club.SheetDebet: {
				{"№", "Имя резидента", "Тариф", "Встреч оплачено", "Встреч проведено", "Осталось", "Оплачено (вход)", "Остаток (вход)",
					"Долг продление", "Штрафы", "Общий долг", "Бывший", "Исключение", "Chat ID", "Формат", "Админ"},
				{"1", "Асет", "100000", "3", "1", "2", "0", "0", "0", "0", "0", "", "", "478757502", "Офлайн", ""},
				{"2", "Рустам", "0", "0", "0", "0", "0", "0", "0", "0", "0", "", "", "453800951", "Онлайн", "Да"},
			},
			club.SheetDDS: {{"Дата", "Приход", "Расход", "Источник", "Категория +", "Категория -"}},
		}
		for k, v := range extra {
			s[k] = v
		}
		return map[string]any{"sheets": s, "final": final}
	}
	// v39's ordinary hourly import: accepted, does not end the waiting.
	if code, body := e.importSheet(sheets(false, nil)); code != 200 {
		t.Fatalf("import %d %s", code, body)
	}
	if !e.cut.Pending(ctx) {
		t.Fatal("an ordinary import ended the cutover")
	}
	// v40's final import with the archive sheets.
	arch := map[string][][]string{club.SheetProfit: {{"Дата", "Резидент", "Выручка", "Прибыль"}, {"01.09.2026", "Асет", "4,000,000", "900,000"}}}
	if code, body := e.importSheet(sheets(true, arch)); code != 200 {
		t.Fatalf("final import %d %s", code, body)
	}
	if e.cut.Pending(ctx) {
		t.Fatal("still waiting")
	}
	if m, _ := e.repo.Master(ctx); m != "server" {
		t.Fatalf("master %q", m)
	}
	if n := e.n(`SELECT count(*) FROM club_fines WHERE resident = 'Асет' AND type = 'Опоздание'`); n != 1 {
		t.Fatal("the write made while waiting was lost")
	}
	if n := e.n(`SELECT count(*) FROM club_writes WHERE status IN ('pending','unknown')`); n != 0 {
		t.Fatal("writes still open")
	}
	if n := e.n(`SELECT count(*) FROM club_sheets WHERE name = $1`, club.SheetProfit); n != 1 {
		t.Fatal("the archive sheet did not come over")
	}
	if code, _ := e.importSheet(sheets(true, nil)); code != http.StatusConflict {
		t.Fatalf("an import after the cutover: %d", code)
	}
	if n := atomic.LoadInt64(e.script); n != 0 {
		t.Fatalf("the script was called %d times", n)
	}
}

func (e *offEnv) signed(path string, v map[string]any) map[string]any {
	v["ts"] = time.Now().Unix()
	b, _ := json.Marshal(v)
	req := httptest.NewRequest("POST", path, bytes.NewReader(b))
	req.Header.Set("X-BS-Signature", sign(b))
	w := httptest.NewRecorder()
	e.r.ServeHTTP(w, req)
	var out map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &out)
	return out
}

func (e *offEnv) importSheet(v map[string]any) (int, string) {
	v["ts"] = time.Now().Unix()
	b, _ := json.Marshal(v)
	req := httptest.NewRequest("POST", "/api/v1/club/import", bytes.NewReader(b))
	req.Header.Set("X-BS-Signature", sign(b))
	w := httptest.NewRecorder()
	e.r.ServeHTTP(w, req)
	return w.Code, w.Body.String()
}

// The export replaces the sheet as the owner's backup: a real workbook and CSV.
func TestClubDataExport(t *testing.T) {
	j := mustDate("01.06.2026")
	s := &club.Snapshot{
		Residents: []club.Resident{{Name: "Асет", TgID: 478757502, Format: "Офлайн", Tariff: 100000, Granted: 3, Done: 1, RestEntry: 50000, JoinedAt: &j}},
		Payments:  []club.Payment{{Date: j, Income: 100000, IncomeCat: "БХ Трекинг", Resident: "Асет"}},
		Fines:     []club.Fine{{Name: "Асет", Type: "Не сдан отчёт", Amount: 10000, Date: j}},
		Reports:   []club.ReportEntry{{At: j, Name: "Асет", Text: "Отчёт <важный> & длинный"}},
		Raw:       club.Sheets{"NPS ответы": {{"Дата", "Резидент", "Chat ID", "Ответ"}, {"01.06.2026", "Асет", "1", "Всё хорошо"}}},
	}
	csvb := ExportCSV(ExportTables(s, "fines")[0])
	if !bytes.HasPrefix(csvb, []byte("\xef\xbb\xbf")) || !strings.Contains(string(csvb), "Асет;Не сдан отчёт;10000;01.06.2026;Не оплатил") {
		t.Fatalf("csv %q", csvb)
	}
	x, err := ExportXLSX(ExportTables(s, "all"))
	if err != nil {
		t.Fatal(err)
	}
	z, err := zip.NewReader(bytes.NewReader(x), int64(len(x)))
	if err != nil {
		t.Fatal(err)
	}
	files := map[string]string{}
	for _, f := range z.File {
		rc, _ := f.Open()
		b, _ := io.ReadAll(rc)
		rc.Close()
		files[f.Name] = string(b)
	}
	wb := files["xl/workbook.xml"]
	for _, n := range []string{"Резиденты", "ДДС", "Штрафы", "Встречи", "Отчёты", "NPS ответы"} {
		if !strings.Contains(wb, `name="`+n+`"`) {
			t.Fatalf("no sheet %s in %s", n, wb)
		}
	}
	all := strings.Join(func() []string {
		var v []string
		for k, s := range files {
			if strings.HasPrefix(k, "xl/worksheets/") {
				v = append(v, s)
			}
		}
		return v
	}(), "")
	if !strings.Contains(all, "Отчёт &lt;важный&gt; &amp; длинный") || !strings.Contains(all, "<v>50000</v>") {
		t.Fatal("cells missing or not escaped")
	}
}

// The script v40 the server ships: asleep unless the server says legacy.
func TestScriptV40Dormant(t *testing.T) {
	if v := content.ScriptVersion(); v != bot.LatestScript {
		t.Fatalf("Code.js %s, LatestScript %s", v, bot.LatestScript)
	}
	code, err := os.ReadFile("../../content/script/Code.js")
	if err != nil {
		t.Fatal(err)
	}
	src := string(code)
	for _, want := range []string{
		"function bsEvery5Min(){\n  if(bsIsCopy()) return;\n  if(bsDormant()){ bsDormantTick(); return; }",
		"function eveningReminder(){\n  if(bsDormant()) return;",
		"function dailyCheck(){\n  if(bsDormant()) return;",
		"function sendOnboardingMessages(){\n  if(bsDormant()) return;",
		"function sendNPS(){\n  if(bsDormant()) return;",
		"if(action && bsDormant()){",
		"if(bsDormant()){\n      if(u && (u.bsAction === \"lead\"",
		"bsClubImport(false, true)",
		"sheetMode: r.j.sheetMode",
	} {
		if !strings.Contains(src, want) {
			t.Fatalf("Code.js lacks %q", want)
		}
	}
}
