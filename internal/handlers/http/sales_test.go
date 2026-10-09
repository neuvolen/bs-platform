package http

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/bnursik/business_surgery_backend/internal/bot"
	"github.com/bnursik/business_surgery_backend/internal/club"
	pg "github.com/bnursik/business_surgery_backend/internal/repository/pg"
	"github.com/bnursik/business_surgery_backend/internal/tplpdf"
	"github.com/gin-gonic/gin"
)

// ── fakes: docs in memory, Telegram that records, a clock that moves ──

type salesFakeDocs struct {
	mu sync.Mutex
	m  map[string]*pg.PlatformDoc
}

func (f *salesFakeDocs) GetDoc(ctx context.Context, scope, key string) (*pg.PlatformDoc, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if d := f.m[scope+"/"+key]; d != nil {
		c := *d
		return &c, nil
	}
	return nil, nil
}

func (f *salesFakeDocs) PutDoc(ctx context.Context, scope, key string, base int, value string, deleted bool, by string) (*pg.PlatformDoc, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.m == nil {
		f.m = map[string]*pg.PlatformDoc{}
	}
	cur := f.m[scope+"/"+key]
	v := 0
	if cur != nil {
		v = cur.Version
	}
	if v != base {
		return nil, pg.ErrPlatformConflict
	}
	d := &pg.PlatformDoc{Scope: scope, Key: key, Value: value, Version: v + 1, Deleted: deleted, UpdatedBy: by}
	f.m[scope+"/"+key] = d
	return d, nil
}

func (f *salesFakeDocs) put(t *testing.T, scope, key string, v any) {
	t.Helper()
	b, _ := json.Marshal(v)
	cur, _ := f.GetDoc(context.Background(), scope, key)
	base := 0
	if cur != nil {
		base = cur.Version
	}
	if _, err := f.PutDoc(context.Background(), scope, key, base, string(b), false, "test"); err != nil {
		t.Fatal(err)
	}
}

func (f *salesFakeDocs) get(scope, key string) map[string]any {
	d, _ := f.GetDoc(context.Background(), scope, key)
	out := map[string]any{}
	if d != nil {
		_ = json.Unmarshal([]byte(d.Value), &out)
	}
	return out
}

type salesSent struct {
	Chat int64
	Text string
	KB   map[string]any
	File string
	Data []byte
	Kind string // msg | doc | wa | resident
}

type salesTG struct {
	mu   sync.Mutex
	sent []salesSent
}

func (g *salesTG) send(ctx context.Context, chat int64, text string, kb map[string]any) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.sent = append(g.sent, salesSent{Chat: chat, Text: text, KB: kb, Kind: "msg"})
	return nil
}

func (g *salesTG) doc(ctx context.Context, chat int64, key, name string, data []byte, fileID, caption string, kb map[string]any) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.sent = append(g.sent, salesSent{Chat: chat, Text: caption, File: name, Data: data, Kind: "doc"})
	return nil
}

func (g *salesTG) wa(ctx context.Context, m bot.WAMessage) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.sent = append(g.sent, salesSent{Text: m.Text, File: m.Phone, Kind: "wa"})
	return nil
}

func (g *salesTG) take() []salesSent {
	g.mu.Lock()
	defer g.mu.Unlock()
	out := g.sent
	g.sent = nil
	return out
}

func (g *salesTG) to(chat int64) []salesSent {
	var out []salesSent
	for _, m := range g.take() {
		if m.Chat == chat {
			out = append(out, m)
		}
	}
	return out
}

type salesFakeClub struct {
	residents []club.Resident
	snap      club.Snapshot
	days      []pg.ReportDay
	boards    []pg.PlatformBoard
	profit    [][]string
	tg        map[string]int64
}

func (c *salesFakeClub) LoadResidents(ctx context.Context) ([]club.Resident, error) {
	return c.residents, nil
}
func (c *salesFakeClub) Load(ctx context.Context) (*club.Snapshot, error) {
	s := c.snap
	s.Residents = c.residents
	return &s, nil
}
func (c *salesFakeClub) ReportDays(ctx context.Context, from, to time.Time) ([]pg.ReportDay, error) {
	return c.days, nil
}
func (c *salesFakeClub) LiveBoards(ctx context.Context) ([]pg.PlatformBoard, error) {
	return c.boards, nil
}
func (c *salesFakeClub) ResidentByTg(ctx context.Context, tg int64) (string, bool, error) {
	for n, id := range c.tg {
		if id == tg {
			return n, true, nil
		}
	}
	return "", false, nil
}
func (c *salesFakeClub) ResidentTgByName(ctx context.Context, name string) (int64, string, error) {
	return c.tg[name], name, nil
}
func (c *salesFakeClub) SheetsByName(ctx context.Context, names []string) (map[string][][]string, error) {
	return map[string][][]string{club.SheetProfit: c.profit}, nil
}

type salesEnv struct {
	s    *ClubSales
	docs *salesFakeDocs
	tg   *salesTG
	club *salesFakeClub
	now  time.Time
	mu   sync.Mutex
}

func (e *salesEnv) at(t time.Time) {
	e.mu.Lock()
	e.now = t
	e.mu.Unlock()
}

func (e *salesEnv) clock() time.Time {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.now
}

func alm(y int, m time.Month, d, h, min int) time.Time {
	return time.Date(y, m, d, h, min, 0, 0, almaty)
}

func newSalesEnv(t *testing.T) *salesEnv {
	e := &salesEnv{docs: &salesFakeDocs{}, tg: &salesTG{}, club: &salesFakeClub{tg: map[string]int64{}}, now: alm(2026, 10, 6, 12, 0)}
	s := NewClubSales(e.docs, []byte("test-secret"))
	s.Now = e.clock
	s.Send, s.Doc, s.WA = e.tg.send, e.tg.doc, e.tg.wa
	s.Admins, s.Owner = []int64{111}, 111
	s.Club, s.Boards = e.club, e.club
	f := NewLeadFunnel(e.docs, e.tg.send, []int64{111})
	f.Doc = e.tg.doc
	f.now = e.clock
	s.F = f
	e.s = s
	return e
}

func noLong(t *testing.T, where, s string) {
	t.Helper()
	if strings.ContainsRune(s, '—') {
		t.Fatalf("%s: long dash in %q", where, s)
	}
}

func setLeadDoc(t *testing.T, e *salesEnv, leads ...map[string]any) {
	var l []any
	for _, x := range leads {
		l = append(l, x)
	}
	e.docs.put(t, "club", "bs_crm", map[string]any{"leads": l})
}

func leadOf(e *salesEnv, id string) map[string]any {
	for _, x := range asList(e.docs.get("club", "bs_crm")["leads"]) {
		if m, _ := x.(map[string]any); m != nil && sStr(m, "id") == id {
			return m
		}
	}
	return nil
}

func testForm() RazborForm {
	var f RazborForm
	f.Date = "06.10.2026"
	f.Situation = "Кофейня у дома, 2 точки в Алматы. Выручка растёт, а денег на счёте к 20 числу нет."
	for _, d := range [][2]string{{"Кассовые разрывы", "занимает на зарплату каждый месяц"}, {"Нет системы продаж", "бариста не предлагают десерты"}, {"Собственник в операционке", "сам стоит за стойкой 50 часов в неделю"}} {
		f.Diag = append(f.Diag, struct {
			Title string `json:"title"`
			Why   string `json:"why"`
		}{d[0], d[1]})
	}
	f.Steps = []string{"Выписать платежи на 4 недели вперёд", "Добавить допродажу десерта в скрипт", "Назначить старшего бариста"}
	for _, n := range [][2]string{{"Выручка в месяц", "6 500 000 ₸"}, {"Средний чек", "2 400 ₸"}, {"Часов собственника", "50 в неделю"}} {
		f.Numbers = append(f.Numbers, struct {
			Label string `json:"label"`
			Value string `json:"value"`
		}{n[0], n[1]})
	}
	return f
}

func scratch(t *testing.T) string {
	dir := os.Getenv("R51_OUT")
	if dir == "" {
		dir = filepath.Join(os.TempDir(), "r51")
	}
	_ = os.MkdirAll(dir, 0o755)
	return dir
}

func savePDF(t *testing.T, name string, pdf []byte) {
	t.Helper()
	dir := scratch(t)
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, pdf, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := exec.LookPath("pdftoppm"); err == nil {
		_ = exec.Command("pdftoppm", "-png", "-r", "60", p, strings.TrimSuffix(p, ".pdf")).Run()
	}
}

// ── 1. сценарий после разбора ──

func TestSalesSequence(t *testing.T) {
	e := newSalesEnv(t)
	ctx := context.Background()
	setLeadDoc(t, e, map[string]any{"id": "tg501", "col": "meet", "name": "Айгерим Сапарова", "tgId": float64(501), "niche": "кофейня"})
	r := &razborSum{LeadID: "tg501", TgID: 501, Name: "Айгерим Сапарова", Form: testForm(), Source: "form"}
	r.Form.clean()
	if err := e.s.saveRazborSum(ctx, r); err != nil {
		t.Fatal(err)
	}
	pdf, err := tplpdf.RenderCallSummary(razborDoc(r))
	if err != nil {
		t.Fatal(err)
	}
	savePDF(t, "lead_razbor.pdf", pdf)

	// filled at 22:00: nothing until 10:00
	e.at(alm(2026, 10, 6, 22, 0))
	if err := e.s.StartSequence(ctx, "tg501", "Итоги разбора заполнены"); err != nil {
		t.Fatal(err)
	}
	if l := leadOf(e, "tg501"); sStr(l, "col") != "diag" || sStr(sMap(l, "pz"), "st") != "wait" {
		t.Fatalf("start: %v", l)
	}
	if n := e.s.RazborTick(ctx); n != 0 || len(e.tg.take()) != 0 {
		t.Fatal("sent at night")
	}
	e.at(alm(2026, 10, 7, 10, 2))
	if n := e.s.RazborTick(ctx); n != 1 {
		t.Fatalf("offer: %d", n)
	}
	got := e.tg.to(501)
	if len(got) != 2 || got[0].Kind != "doc" || !strings.HasSuffix(got[0].File, ".pdf") || len(got[0].Data) < 1000 {
		t.Fatalf("offer msgs: %+v", got)
	}
	offer := got[1].Text
	for _, want := range []string{"Айгерим", "Кассовые разрывы", "Платёжный календарь", "1 500 000 ₸", "500 000 ₸", "Что даст клуб"} {
		if !strings.Contains(offer, want) {
			t.Fatalf("offer misses %q:\n%s", want, offer)
		}
	}
	noLong(t, "offer", offer)
	b, _ := json.Marshal(got[1].KB)
	for _, want := range []string{"pz:join", "pz:ask", "pz:no", "Хочу в клуб", "Есть вопрос", "Не сейчас"} {
		if !strings.Contains(string(b), want) {
			t.Fatalf("buttons miss %s: %s", want, b)
		}
	}
	st := pzStatus(sMap(leadOf(e, "tg501"), "pz"), e.clock())
	if !strings.Contains(st, "отправлено 1 из 4") || !strings.Contains(st, "день 2") {
		t.Fatalf("status: %s", st)
	}
	// nothing more on the same day
	e.at(alm(2026, 10, 7, 15, 0))
	if n := e.s.RazborTick(ctx); n != 0 {
		t.Fatal("double")
	}
	// day 2
	e.at(alm(2026, 10, 9, 10, 5))
	e.s.RazborTick(ctx)
	got = e.tg.to(501)
	if len(got) != 1 || !strings.Contains(got[0].Text, "Выписать платежи на 4 недели вперёд") || !strings.Contains(got[0].Text, "/t/") {
		t.Fatalf("d2: %+v", got)
	}
	noLong(t, "d2", got[0].Text)
	// day 5: the library's case (nothing published yet)
	e.at(alm(2026, 10, 12, 10, 5))
	e.s.RazborTick(ctx)
	got = e.tg.to(501)
	if len(got) != 1 || !strings.Contains(got[0].Text, "похожего бизнеса") || !strings.Contains(got[0].Text, "За 3 месяца") {
		t.Fatalf("d5: %+v", got)
	}
	// day 10
	e.at(alm(2026, 10, 17, 19, 30))
	e.s.RazborTick(ctx)
	got = e.tg.to(501)
	if len(got) != 1 || !strings.Contains(got[0].Text, "последнее напоминание") {
		t.Fatalf("d10: %+v", got)
	}
	l := leadOf(e, "tg501")
	if sStr(sMap(l, "pz"), "st") != "done" || !strings.Contains(pzStatus(sMap(l, "pz"), e.clock()), "4 из 4") {
		t.Fatalf("done: %v", l["pz"])
	}
	e.at(alm(2026, 10, 25, 12, 0))
	if n := e.s.RazborTick(ctx); n != 0 {
		t.Fatal("after done")
	}
}

func TestSalesSequenceStopsAndButtons(t *testing.T) {
	e := newSalesEnv(t)
	ctx := context.Background()
	mk := func(id string, tg int) map[string]any {
		return map[string]any{"id": id, "col": "diag", "name": "Лид " + id, "tgId": float64(tg), "tg": "@lead" + id}
	}
	setLeadDoc(t, e, mk("a", 601), mk("b", 602), mk("c", 603), mk("d", 604))
	for _, id := range []string{"a", "b", "c", "d"} {
		r := &razborSum{LeadID: id, Name: "Лид " + id, Form: testForm()}
		_ = e.s.saveRazborSum(ctx, r)
		if err := e.s.StartSequence(ctx, id, "test"); err != nil {
			t.Fatal(err)
		}
	}
	e.s.RazborTick(ctx)
	e.tg.take()
	// a: answered in the bot; b: became a resident
	_ = e.s.mutateLead(ctx, byLeadID("a"), func(l map[string]any) bool { l["tgInAt"] = rfc(e.clock().Add(time.Hour)); return true })
	_ = e.s.mutateLead(ctx, byLeadID("b"), func(l map[string]any) bool { l["col"] = "won"; return true })
	e.at(e.clock().Add(2 * time.Hour))
	e.s.RazborTick(ctx)
	if w := sStr(sMap(leadOf(e, "a"), "pz"), "why"); w != "лид ответил" {
		t.Fatalf("a: %q", w)
	}
	if w := sStr(sMap(leadOf(e, "b"), "pz"), "why"); !strings.Contains(w, "оплатил") {
		t.Fatalf("b: %q", w)
	}
	// c: «Хочу в клуб»
	if _, ok := e.s.HandleCallback(ctx, bot.CallbackUpdate{ChatID: 603, FromID: 603, Data: "pz:join"}); !ok {
		t.Fatal("join not handled")
	}
	lc := leadOf(e, "c")
	if sStr(lc, "col") != "decide" || sStr(sMap(lc, "pz"), "st") != "stopped" || lc["hot"] != true {
		t.Fatalf("c: %v", lc)
	}
	msgs := e.tg.take()
	team, lead := 0, 0
	for _, m := range msgs {
		if m.Chat == 111 && strings.Contains(m.Text, "Хочу в клуб") {
			team++
		}
		if m.Chat == 603 {
			lead++
		}
	}
	if team != 1 || lead != 1 {
		t.Fatalf("join msgs: %+v", msgs)
	}
	// d: «Не сейчас» → «Отложено», when to remind → 14 days
	e.s.HandleCallback(ctx, bot.CallbackUpdate{ChatID: 604, FromID: 604, Data: "pz:no"})
	ld := leadOf(e, "d")
	if sStr(ld, "col") != "later" || sStr(sMap(ld, "pz"), "why") != "не сейчас" {
		t.Fatalf("d: %v", ld)
	}
	m := e.tg.to(604)
	if len(m) != 1 || !strings.Contains(fmt.Sprint(m[0].KB), "pz:rem:14") {
		t.Fatalf("ask when: %+v", m)
	}
	e.s.HandleCallback(ctx, bot.CallbackUpdate{ChatID: 604, FromID: 604, Data: "pz:rem:14"})
	rem := sTime(sMap(leadOf(e, "d"), "pz"), "remindAt")
	if rem.IsZero() || rem.In(almaty).Hour() != 11 {
		t.Fatalf("remindAt %v", rem)
	}
	e.tg.take()
	e.at(rem.Add(-time.Hour))
	e.s.RazborTick(ctx)
	if len(e.tg.to(604)) != 0 {
		t.Fatal("reminded early")
	}
	e.at(rem.Add(10 * time.Minute))
	e.s.RazborTick(ctx)
	got := e.tg.to(604)
	if len(got) != 1 || !strings.Contains(got[0].Text, "просили напомнить") {
		t.Fatalf("later: %+v", got)
	}
	e.s.RazborTick(ctx)
	if len(e.tg.to(604)) != 0 {
		t.Fatal("reminded twice")
	}
	// the team stops a sequence from the card
	if st := pzStatus(sMap(leadOf(e, "d"), "pz"), e.clock()); !strings.Contains(st, "остановлен") {
		t.Fatal(st)
	}
}

func TestSalesSequenceCatchUpAndWhatsApp(t *testing.T) {
	e := newSalesEnv(t)
	ctx := context.Background()
	setLeadDoc(t, e, map[string]any{"id": "x", "col": "diag", "name": "Ерлан", "tgId": float64(701)},
		map[string]any{"id": "w", "col": "diag", "name": "Марат", "phone": "8 701 555 66 77"})
	for _, id := range []string{"x", "w"} {
		_ = e.s.saveRazborSum(ctx, &razborSum{LeadID: id, Name: id, Form: testForm()})
		_ = e.s.StartSequence(ctx, id, "test")
	}
	e.s.RazborTick(ctx)
	msgs := e.tg.take()
	var wa *salesSent
	for i := range msgs {
		if msgs[i].Kind == "wa" {
			wa = &msgs[i]
		}
	}
	if wa == nil || wa.File != "77015556677" || !strings.Contains(wa.Text, "/api/v1/public/sales/razbor/w/") {
		t.Fatalf("wa: %+v", msgs)
	}
	// the open PDF link works, a wrong signature does not
	gin.SetMode(gin.TestMode)
	r := gin.New()
	(&SalesModule{S: e.s}).Register(r)
	link := wa.Text[strings.Index(wa.Text, "/api/v1/public/"):]
	link = strings.Fields(link)[0]
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest("GET", link, nil))
	if w.Code != 200 || w.Header().Get("Content-Type") != "application/pdf" {
		t.Fatalf("pdf link %s: %d", link, w.Code)
	}
	w = httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest("GET", "/api/v1/public/sales/razbor/w/00000000000000000000000a.pdf", nil))
	if w.Code != 404 {
		t.Fatal("bad signature served")
	}
	// the server slept 6 days: only day 5 goes, day 2 is skipped
	e.at(e.clock().AddDate(0, 0, 6))
	e.s.RazborTick(ctx)
	got := e.tg.to(701)
	if len(got) != 1 || !strings.Contains(got[0].Text, "похожего бизнеса") {
		t.Fatalf("catch-up: %+v", got)
	}
	if s := sStr(sMap(sMap(leadOf(e, "x"), "pz"), "steps"), "d2"); !strings.HasPrefix(s, "skipped") {
		t.Fatalf("d2 not skipped: %q", s)
	}
}

// ── 2. согласие и кейсы ──

func boardJSON(t *testing.T, v any) json.RawMessage {
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func residentBoard(t *testing.T, name string, start time.Time, now time.Time) pg.PlatformBoard {
	data := map[string]any{
		"date": rfc(start), "updated": rfc(now), "version": 4,
		"info":    map[string]any{"name": strings.Fields(name)[0], "biz": "Сеть кофеен", "city": "Алматы", "res": name},
		"health":  map[string]any{"brain": 7, "heart": 6, "hands": 8, "spine": 6, "blood": 8, "dna": 7, "eyes": 7},
		"summary": map[string]any{"rev": "9 800 000", "profit": "1 900 000", "hours": "28"},
		"gallup":  map[string]any{"themes": []any{"achiever", "strategic", "focus", "learner", "activator", "relator"}},
		"nodes": []any{
			map[string]any{"type": "diag", "title": "Нет системы продаж", "organ": "Продажи"},
			map[string]any{"type": "tool", "title": "Платёжный календарь", "organ": "Финансы"},
			map[string]any{"type": "task", "title": "Скрипт допродажи", "task": map[string]any{"completed": true}},
			map[string]any{"type": "task", "title": "Нанять управляющего", "task": map[string]any{"completed": false}},
			map[string]any{"role": "pointA", "title": "Сейчас", "point": map[string]any{"rev": "6 500 000", "profit": "700 000", "hours": "50"}},
			map[string]any{"role": "pointB", "title": "12 млн выручки и 20 часов в неделю"},
		},
		"history": []any{
			map[string]any{"version": 1, "date": rfc(start.AddDate(0, 0, 1)),
				"health":  map[string]any{"brain": 5, "heart": 4, "hands": 4, "spine": 3, "blood": 3, "dna": 4, "eyes": 3},
				"summary": map[string]any{"rev": "6 500 000", "profit": "700 000", "hours": "50"},
				"nodes": []any{
					map[string]any{"type": "diag", "title": "Кассовые разрывы", "organ": "Финансы"},
					map[string]any{"type": "diag", "title": "Нет системы продаж", "organ": "Продажи"},
					map[string]any{"type": "diag", "title": "Собственник в операционке", "organ": "Команда"},
					map[string]any{"type": "task", "title": "Платёжный календарь на 8 недель", "task": map[string]any{"completed": true}},
				}},
			map[string]any{"version": 2, "date": rfc(start.AddDate(0, 1, 0)),
				"nodes": []any{map[string]any{"type": "task", "title": "Регламент смены", "task": map[string]any{"completed": true}}}},
		},
	}
	return pg.PlatformBoard{ID: "b_" + strings.Fields(name)[0], Resident: name, Data: boardJSON(t, data), UpdatedAt: now}
}

func TestSalesConsentAndCases(t *testing.T) {
	e := newSalesEnv(t)
	ctx := context.Background()
	now := e.clock()
	j1 := now.AddDate(0, -4, 0)
	e.club.residents = []club.Resident{
		{Name: "Айдана Ким", TgID: 801, JoinedAt: &j1, Tariff: 500000},
		{Name: "Болат Нуров", TgID: 802, JoinedAt: &j1, Tariff: 500000},
		{Name: "Вера Ли", TgID: 803, JoinedAt: &j1, Tariff: 500000},
		{Name: "Команда", Admin: true},
	}
	e.club.tg = map[string]int64{"Айдана Ким": 801, "Болат Нуров": 802, "Вера Ли": 803}
	e.club.boards = []pg.PlatformBoard{residentBoard(t, "Айдана Ким", j1, now), residentBoard(t, "Болат Нуров", j1, now), residentBoard(t, "Вера Ли", j1, now)}

	// R52: by default «можно анонимно»; an explicit choice stays as it is
	if m := e.s.ConsentOf(ctx, "Айдана Ким"); m != "anon" {
		t.Fatalf("default consent %s", m)
	}
	if err := e.s.setConsent(ctx, "Вера Ли", "no", "резидент в боте"); err != nil {
		t.Fatal(err)
	}
	// the team asks through the bot at night: it goes at 10:00
	e.at(alm(2026, 10, 6, 21, 0))
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest("POST", "/", strings.NewReader(`{"name":"Айдана Ким"}`))
	e.s.RequestConsent(c)
	time.Sleep(50 * time.Millisecond)
	// R70: every resident consents on joining: shown anonymously unless the team names the case
	if md, chosen := consentMode(e.s.consents(ctx)[normName("Айдана Ким")]); md != "anon" || !chosen {
		t.Fatalf("consent on joining: %s %v", md, chosen)
	}
	e.s.ConsentTick(ctx)
	if len(e.tg.to(801)) != 0 {
		t.Fatal("consent at night")
	}
	e.at(alm(2026, 10, 7, 10, 1))
	e.s.ConsentTick(ctx)
	got := e.tg.to(801)
	if len(got) != 1 || !strings.Contains(fmt.Sprint(got[0].KB), "cs:anon") {
		t.Fatalf("consent ask: %+v", got)
	}
	e.s.ConsentTick(ctx)
	if len(e.tg.to(801)) != 0 {
		t.Fatal("asked twice")
	}
	// Айдана: anonymously (bot), Болат: with name (profile), Вера: no (her own choice before R52 stays)
	if _, ok := e.s.ConsentCallback(ctx, bot.CallbackUpdate{ChatID: 801, FromID: 801, Data: "cs:anon"}); !ok {
		t.Fatal("cs not handled")
	}
	if err := e.s.setConsent(ctx, "Болат Нуров", "name", "резидент на платформе"); err != nil {
		t.Fatal(err)
	}
	// R70: an old choice no longer counts: everyone consents, anonymously by default
	if e.s.ConsentOf(ctx, "Айдана Ким") != "anon" || e.s.ConsentOf(ctx, "Болат Нуров") != "anon" || e.s.ConsentOf(ctx, "Вера Ли") != "anon" {
		t.Fatal("consents")
	}
	n, skipped, err := e.s.Generate(ctx, "test")
	if err != nil || n != 3 {
		t.Fatalf("generate %d %v %v", n, skipped, err)
	}
	cs := e.s.cases(ctx)
	var anon, named *salesCase
	for i := range cs {
		switch cs[i].Resident {
		case "Айдана Ким":
			anon = &cs[i]
		case "Болат Нуров":
			named = &cs[i]
		}
		if cs[i].Status != "draft" {
			t.Fatal("not a draft")
		}
	}
	if anon == nil || named == nil {
		t.Fatalf("cases %+v", cs)
	}
	lbl := map[string]tplpdf.ReportMetric{}
	for _, m := range anon.Metrics {
		lbl[m.Label] = m
	}
	if m := lbl["Выручка в месяц"]; m.Before != 6500000 || m.After != 9800000 {
		t.Fatalf("rev %+v", m)
	}
	if m := lbl["Часы собственника в неделю"]; m.Before != 50 || m.After != 28 || !m.Lower {
		t.Fatalf("hours %+v", m)
	}
	if m := lbl["Индекс здоровья бизнеса"]; m.Before != 37 || m.After != 70 {
		t.Fatalf("health %+v", m)
	}
	if strings.Join(anon.Closed, ",") != "Кассовые разрывы,Собственник в операционке" {
		t.Fatalf("closed %v", anon.Closed)
	}
	// anonymous: no name anywhere
	ex := caseExports(anon, "anon")
	all := anon.Title + anon.Story + fmt.Sprint(ex)
	if strings.Contains(all, "Айдана") || strings.Contains(all, "Ким") || !strings.Contains(all, "Сеть кофеен") || !strings.Contains(all, "Алматы") {
		t.Fatalf("anon leak: %s", all)
	}
	noLong(t, "case", all)
	if th := ex["threads"].(string); len([]rune(th)) > 500 {
		t.Fatalf("threads too long %d", len([]rune(th)))
	}
	if strings.Contains(named.Story+named.Title, "Болат") {
		t.Fatalf("a new case names the resident: %s", named.Story)
	}
	// publishing needs consent; /about gets only published ones
	var public []PublicCaseView
	e.s.OnCases = func(l []PublicCaseView) { public = l }
	r := gin.New()
	(&SalesModule{S: e.s}).Register(r)
	put := func(id, body string) int {
		c, _ := gin.CreateTestContext(httptest.NewRecorder())
		c.Params = gin.Params{{Key: "id", Value: id}}
		c.Request = httptest.NewRequest("PUT", "/", strings.NewReader(body))
		e.s.PutCase(c)
		return c.Writer.Status()
	}
	if code := put(anon.ID, `{"status":"published"}`); code != 200 {
		t.Fatalf("publish %d", code)
	}
	if len(public) != 1 || strings.Contains(public[0].Who, "Айдана") || public[0].Story != "" {
		t.Fatalf("public %+v", public)
	}
	// R70: the team names a case: «Показывать имя»
	if code := put(named.ID, `{"status":"published","named":true}`); code != 200 {
		t.Fatalf("publish named %d", code)
	}
	if len(public) != 2 || !strings.Contains(public[0].Who+public[1].Who, "Болат") {
		t.Fatalf("named public %+v", public)
	}
	if code := put(named.ID, `{"status":"hidden","named":false}`); code != 200 || len(public) != 1 {
		t.Fatalf("hide %d %+v", code, public)
	}
	// day 5 of a lead with a finance diagnosis takes the published case
	txt := e.s.caseFor(ctx, []diagPlan{planForDiag("Кассовые разрывы", "")}, "кофейня")
	if !strings.Contains(txt, "Сеть кофеен") || strings.Contains(txt, "Айдана") {
		t.Fatalf("caseFor: %s", txt)
	}
	// R70: an old «нельзя» from the bot no longer hides a case: consent comes with residency
	_ = e.s.setConsent(ctx, "Айдана Ким", "no", "резидент")
	if len(public) != 1 {
		t.Fatalf("public after an old-style answer: %+v", public)
	}
}

// ── 3. итог периода и продление ──

func TestSalesReportAndRenewal(t *testing.T) {
	e := newSalesEnv(t)
	ctx := context.Background()
	now := alm(2026, 10, 6, 12, 0)
	e.at(now)
	j := alm(2026, 7, 9, 0, 0) // a 3-month package ends 09.10: 3 days left
	e.club.residents = []club.Resident{{Name: "Айдана Ким", TgID: 801, JoinedAt: &j, Tariff: 500000, Granted: 9, Done: 7}}
	e.club.tg = map[string]int64{"Айдана Ким": 801}
	e.club.boards = []pg.PlatformBoard{residentBoard(t, "Айдана Ким", j, now)}
	e.club.snap.Meetings = []club.Meeting{{Resident: "Айдана Ким", Date: alm(2026, 8, 1, 0, 0), Done: true}, {Resident: "Айдана Ким", Date: alm(2026, 9, 1, 0, 0), Done: true},
		{Resident: "Айдана Ким", Date: alm(2026, 9, 20, 0, 0)}}
	e.club.snap.Fines = []club.Fine{{Name: "Айдана Ким", Date: alm(2026, 8, 15, 0, 0), Amount: 5000}}
	e.club.days = []pg.ReportDay{{TgID: 801, Name: "Айдана", Days: 70}}
	var writes []map[string]string
	e.s.Write = func(ctx context.Context, action string, p map[string]string) error {
		if action == "setMeetings" {
			writes = append(writes, p)
		}
		return nil
	}

	// the auto draft: 4 days before the 3-month boundary; the team is told
	e.s.ReportTick(ctx)
	items, _ := e.s.reportsDoc(ctx)
	if len(items) != 1 || items[0].Months != 3 || items[0].Status != "draft" || items[0].Auto == "" {
		t.Fatalf("auto draft: %+v", items)
	}
	rep := items[0]
	if rep.Doc.Meetings != 2 || rep.Doc.Fines != 1 || rep.Doc.ReportDays != 70 || rep.Doc.TasksDone < 2 || len(rep.Doc.Gallup) != 5 {
		t.Fatalf("report data: %+v", rep.Doc)
	}
	team := 0
	renew := 0
	for _, m := range e.tg.take() {
		if m.Chat == 111 && strings.Contains(m.Text, "Готов черновик") {
			team++
		}
		if m.Chat == 801 && strings.Contains(m.Text, "заканчивается") {
			renew++
			if !strings.Contains(fmt.Sprint(m.KB), "rn:3") || !strings.Contains(fmt.Sprint(m.KB), "rn:12") {
				t.Fatalf("renew kb %v", m.KB)
			}
		}
	}
	if team != 1 || renew != 1 {
		t.Fatalf("team %d renew %d", team, renew)
	}
	e.s.ReportTick(ctx)
	items, _ = e.s.reportsDoc(ctx)
	if len(items) != 1 || len(e.tg.take()) != 0 {
		t.Fatal("auto draft or reminder twice")
	}
	// the PDFs: 3 months (draft) and a year
	pdf, err := tplpdf.RenderReport(e.s.reportDoc(ctx, &rep, true))
	if err != nil {
		t.Fatal(err)
	}
	savePDF(t, "report_3m.pdf", pdf)
	ry, err := e.s.buildReport(ctx, e.club.residents[0], 12, now)
	if err != nil {
		t.Fatal(err)
	}
	ry.Doc.Note = "Год закрыли с выручкой 9,8 млн ₸ в месяц. Следующий шаг: управляющий и вторая точка."
	pdf, err = tplpdf.RenderReport(e.s.reportDoc(ctx, ry, false))
	if err != nil {
		t.Fatal(err)
	}
	savePDF(t, "report_12m.pdf", pdf)

	// send: at night it waits, at 10:00 the PDF and the renewal buttons go
	e.at(alm(2026, 10, 6, 21, 0))
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Params = gin.Params{{Key: "id", Value: rep.ID}}
	c.Request = httptest.NewRequest("POST", "/", nil)
	e.s.SendReport(c)
	time.Sleep(50 * time.Millisecond)
	if len(e.tg.to(801)) != 0 {
		t.Fatal("report at night")
	}
	e.at(alm(2026, 10, 7, 10, 0))
	e.s.ReportTick(ctx)
	got := e.tg.to(801)
	if len(got) < 2 || got[0].Kind != "doc" || !strings.Contains(fmt.Sprint(got[1].KB), "rn:12") || !strings.Contains(got[1].Text, "1 500 000 ₸") {
		t.Fatalf("report send: %+v", got)
	}
	if r := e.s.reportByID(ctx, rep.ID); r.Status != "sent" || r.SentVia != "telegram" {
		t.Fatalf("status %+v", r)
	}
	// the resident sees it
	me := e.s.meView(ctx, "Айдана Ким")
	if reps := me["reports"].([]gin.H); len(reps) != 1 {
		t.Fatalf("me %v", me)
	}

	// renewal: no Kaspi link set → the team is warned
	if _, ok := e.s.RenewCallback(ctx, bot.CallbackUpdate{ChatID: 801, FromID: 801, Data: "rn:3"}); !ok {
		t.Fatal("rn not handled")
	}
	warned := false
	for _, m := range e.tg.take() {
		if m.Chat == 111 && strings.Contains(m.Text, "Ссылка Kaspi клуба не задана") {
			warned = true
		}
	}
	if !warned {
		t.Fatal("no kaspi warning")
	}
	// with the club's Kaspi link (not the fines one)
	e.docs.put(t, "server", salesSettingsKey, SalesCfg{YearPrice: 1500000, Q3Price: 500000, KaspiClub: "https://pay.kaspi.kz/pay/clubtest"})
	e.s.RenewCallback(ctx, bot.CallbackUpdate{ChatID: 801, FromID: 801, Data: "rn:12"})
	got = e.tg.to(801)
	if len(got) != 1 || !strings.Contains(fmt.Sprint(got[0].KB), "pay.kaspi.kz/pay/clubtest") || !strings.Contains(got[0].Text, "1 500 000 ₸") {
		t.Fatalf("kaspi: %+v", got)
	}
	_, ren := e.s.reportsDoc(ctx)
	if p := sMap(ren["айдана ким"], "pending"); anyInt(p["months"]) != 12 {
		t.Fatalf("pending %v", ren)
	}
	// the team enters a fine payment: nothing; then the club payment: the package is extended
	e.s.OnClubWrite(ctx, "addPayment", map[string]string{"type": "income", "resident": "Айдана Ким", "src": "Штраф", "amount": "5000"})
	if len(writes) != 0 {
		t.Fatal("fine renewed")
	}
	// the server applied the payment already (≥ tariff): 3 packages by tariff, done reset
	e.club.residents[0].Granted, e.club.residents[0].Done = 3*3*3+2, 0
	e.s.OnClubWrite(ctx, "addPayment", map[string]string{"type": "income", "resident": "Айдана Ким", "src": "Резидентство", "amount": "1500000"})
	if len(writes) != 1 || writes[0]["granted"] != "38" || writes[0]["done"] != "0" {
		t.Fatalf("meetings %v", writes)
	}
	_, ren = e.s.reportsDoc(ctx)
	if st := ren["айдана ким"]; st["pending"] != nil || sStr(st, "packEnd") != "2027-10-09" {
		t.Fatalf("renewed %v", st)
	}
	msgs := e.tg.take()
	ok := false
	for _, m := range msgs {
		if m.Chat == 801 && strings.Contains(m.Text, "оплата получена") && strings.Contains(m.Text, "9 октября 2027") {
			ok = true
		}
	}
	if !ok {
		t.Fatalf("resident note: %+v", msgs)
	}
	// the next reminders count from the new end
	e.at(alm(2027, 9, 25, 11, 0))
	e.s.ReportTick(ctx)
	r14 := 0
	for _, m := range e.tg.to(801) {
		if strings.Contains(m.Text, "9 октября") {
			r14++
		}
	}
	if r14 != 1 {
		t.Fatal("14-day reminder")
	}
}

func TestSalesSettings(t *testing.T) {
	e := newSalesEnv(t)
	gin.SetMode(gin.TestMode)
	call := func(body string) (int, map[string]any) {
		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Request = httptest.NewRequest("PUT", "/", strings.NewReader(body))
		e.s.PutSettings(c)
		var out map[string]any
		_ = json.Unmarshal(w.Body.Bytes(), &out)
		return w.Code, out
	}
	// R55: the owner pays everything through one Kaspi link
	if code, _ := call(`{"kaspiClub":"` + bot.KaspiLink + `"}`); code != 200 {
		t.Fatalf("the owner's Kaspi link refused %d", code)
	}
	if code, _ := call(`{"kaspiClub":"http://x"}`); code != 400 {
		t.Fatal("http accepted")
	}
	code, out := call(`{"yearPrice":1600000,"q3Price":0,"bonusText":"Бонус — разбор команды","bonusDays":5,"texts":{"d2":"{имя}, как дела? {шаг}","offer":""}}`)
	if code != 200 {
		t.Fatalf("save %d %v", code, out)
	}
	cfg := e.s.Settings(context.Background())
	if cfg.YearPrice != 1600000 || cfg.Q3Price != 0 || strings.Contains(cfg.BonusText, "—") || cfg.Text("d2") != "{имя}, как дела? {шаг}" || cfg.Text("offer") != salesDefaultTexts["offer"] {
		t.Fatalf("cfg %+v", cfg)
	}
	if p := pricesLine(cfg); strings.Contains(p, "3 месяца") || !strings.Contains(p, "1 600 000 ₸") {
		t.Fatal(p)
	}
	if _, err := e.s.RequestRenewal(context.Background(), "X", 3, "test"); err == nil {
		t.Fatal("3 months without a price")
	}
}

// R55: the owner's Kaspi link goes into an empty «Ссылка на оплату клуба»
// once; a link the team cleared afterwards stays cleared; the renewal names
// the sum to type (the link opens Kaspi without one).
func TestR55SeedKaspiClub(t *testing.T) {
	e := newSalesEnv(t)
	ctx := context.Background()
	if ok, err := e.s.SeedKaspi(ctx); err != nil || !ok {
		t.Fatalf("seed: %v %v", ok, err)
	}
	cfg := e.s.Settings(ctx)
	if cfg.KaspiClub != bot.KaspiLink || cfg.KaspiSeeded == "" || cfg.YearPrice != salesYearPrice {
		t.Fatalf("cfg %+v", cfg)
	}
	if ok, _ := e.s.SeedKaspi(ctx); ok {
		t.Fatal("seeded twice")
	}
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest("PUT", "/", strings.NewReader(`{"kaspiClub":""}`))
	e.s.PutSettings(c)
	if w.Code != 200 {
		t.Fatalf("save %d", w.Code)
	}
	if ok, _ := e.s.SeedKaspi(ctx); ok || e.s.Settings(ctx).KaspiClub != "" {
		t.Fatal("a cleared link came back")
	}
	if s := kaspiSum(bot.KaspiLink, 1500000); s != " (сумму 1 500 000 ₸ введите в Kaspi сами)" {
		t.Fatalf("sum hint %q", s)
	}
	if kaspiSum("https://kaspi.kz/pay/fixed?amount=5", 5) != "" {
		t.Fatal("hint for another link")
	}
}

// ── 4. отчёт недели ──

func TestSalesWeekly(t *testing.T) {
	e := newSalesEnv(t)
	ctx := context.Background()
	mon := alm(2026, 10, 12, 10, 5)
	j := alm(2026, 7, 20, 0, 0)
	e.club.residents = []club.Resident{{Name: "Айдана Ким", TgID: 801, JoinedAt: &j, Tariff: 500000}, {Name: "Болат Нуров", TgID: 802, JoinedAt: &j, Tariff: 1500000}}
	e.club.days = []pg.ReportDay{{TgID: 801, Days: 7}, {TgID: 802, Days: 2}}
	e.club.snap.Payments = []club.Payment{{Date: alm(2026, 10, 8, 0, 0), Income: 500000, Resident: "Айдана Ким"}, {Date: alm(2026, 10, 1, 0, 0), Income: 999}}
	setLeadDoc(t, e,
		map[string]any{"id": "1", "col": "new", "source": "Threads: пост", "startAt": rfc(alm(2026, 10, 7, 9, 0))},
		map[string]any{"id": "2", "col": "new", "source": "Threads: другой", "startAt": rfc(alm(2026, 10, 9, 9, 0)), "handledAt": "x"},
		map[string]any{"id": "3", "col": "decide", "source": "Сайт (Tilda): форма", "startAt": rfc(alm(2026, 10, 10, 9, 0))})
	e.s.Check = func(ctx context.Context) CheckResult {
		return CheckResult{Items: []CheckItem{{Key: "db", Title: "База", State: "ok", Text: "ok"}, {Key: "voice", Title: "Голос", State: "fail", Text: "ключ не подходит"}}}
	}
	e.s.Errors = NewErrCounter()
	e.s.Errors.now = func() time.Time { return alm(2026, 10, 8, 12, 0) }
	fmt.Fprintf(e.s.Errors, "2026/10/08 12:00:00 booking: confirm 5: error x\n2026/10/08 12:00:01 ok line\n")
	e.s.Errors.Flush(ctx, e.s.meta(), mon)
	text, n := e.s.WeeklyReport(ctx, mon)
	for _, want := range []string{"Итоги недели 05.10 - 11.10", "Голос: ключ не подходит", "1 новый лид без ответа", "в «Решение»", "Болат 2 из 7", "лиды 3 (Threads 2, Сайт (Tilda) 1)", "оплаты 500 000 ₸ (1)", "отчёты 64%", "ошибки сервера 1"} {
		if !strings.Contains(text, want) {
			t.Fatalf("weekly misses %q:\n%s", want, text)
		}
	}
	if strings.Contains(text, "База") || n < 4 {
		t.Fatalf("not only actionable (%d):\n%s", n, text)
	}
	noLong(t, "weekly", text)
	_ = os.WriteFile(filepath.Join(scratch(t), "weekly.txt"), []byte(text), 0o644)
	// the tick: Monday 10:05 once; not on Tuesday, not at 10:00
	e.at(alm(2026, 10, 12, 10, 0))
	e.s.WeeklyTick(ctx)
	if len(e.tg.take()) != 0 {
		t.Fatal("too early")
	}
	e.at(mon)
	e.s.WeeklyTick(ctx)
	e.at(mon.Add(30 * time.Minute))
	e.s.WeeklyTick(ctx)
	if got := e.tg.to(111); len(got) != 1 {
		t.Fatalf("weekly sends: %d", len(got))
	}
	e.at(alm(2026, 10, 13, 10, 5))
	e.s.WeeklyTick(ctx)
	if len(e.tg.take()) != 0 {
		t.Fatal("tuesday")
	}
}

func TestSalesNumbers(t *testing.T) {
	for in, want := range map[string]float64{"1 500 000": 1500000, "1,500,000 ₸": 1500000, "7,5": 7.5, "12.5": 12.5, "50 в неделю": 50, "-200 000": -200000} {
		if got, ok := sNum(in); !ok || got != want {
			t.Errorf("sNum(%q) = %v", in, got)
		}
	}
	if salesWindow(alm(2026, 10, 6, 9, 59)) || !salesWindow(alm(2026, 10, 6, 10, 0)) || salesWindow(alm(2026, 10, 6, 20, 0)) {
		t.Fatal("window")
	}
	if got := salesNextWindow(alm(2026, 10, 6, 21, 0)); !got.Equal(alm(2026, 10, 7, 10, 0)) {
		t.Fatal(got)
	}
	if got := tplpdf.MetricDelta(tplpdf.ReportMetric{Before: 50, After: 28, Unit: "ч"}); got != "-44%" {
		t.Fatal(got)
	}
	if got := planForDiag("Хаос в процессах", ""); got.Organ != "Процессы" || len(got.Tools) == 0 {
		t.Fatalf("%+v", got)
	}
}

func TestSalesHTTP(t *testing.T) {
	e := newSalesEnv(t)
	gin.SetMode(gin.TestMode)
	setLeadDoc(t, e, map[string]any{"id": "tg901", "col": "meet", "name": "Дамир", "tgId": float64(901)}, map[string]any{"id": "nocontact", "col": "diag", "name": "Нет"})
	j := alm(2026, 7, 9, 0, 0)
	e.club.residents = []club.Resident{{Name: "Айдана Ким", TgID: 801, JoinedAt: &j, Tariff: 500000}}
	e.club.tg = map[string]int64{"Айдана Ким": 801}
	as := func(role string, tg int64) gin.HandlerFunc {
		return func(c *gin.Context) { c.Set("role", role); c.Set("userID", fmt.Sprintf("tg:%d", tg)); c.Next() }
	}
	r := gin.New()
	team := r.Group("/t", as("admin", 111))
	team.PUT("/razbor/:lead", e.s.PutRazbor)
	team.GET("/razbor/:lead", e.s.GetRazbor)
	team.GET("/razbor/:lead/pdf", e.s.RazborPDF)
	team.GET("/seq", e.s.SeqList)
	res := r.Group("/r", as("resident", 801))
	res.GET("/me", e.s.Me)
	res.PUT("/me/consent", e.s.PutMyConsent)
	res.POST("/me/renew", e.s.MyRenew)
	other := r.Group("/o", as("resident", 999))
	other.GET("/me", e.s.Me)
	do := func(method, path, body string) (int, map[string]any, []byte) {
		w := httptest.NewRecorder()
		r.ServeHTTP(w, httptest.NewRequest(method, path, strings.NewReader(body)))
		var out map[string]any
		_ = json.Unmarshal(w.Body.Bytes(), &out)
		return w.Code, out, w.Body.Bytes()
	}
	if code, _, _ := do("PUT", "/t/razbor/tg901", `{"form":{"diag":[],"steps":["x"]}}`); code != 400 {
		t.Fatalf("no diag %d", code)
	}
	if code, out, _ := do("PUT", "/t/razbor/nocontact", `{"form":{"diag":[{"title":"Кассовые разрывы"}],"steps":["x"]},"send":true}`); code != 400 || out["error"] != "no_contact" {
		t.Fatalf("no contact %d %v", code, out)
	}
	code, out, _ := do("PUT", "/t/razbor/tg901", `{"form":{"diag":[{"title":"Кассовые разрывы","why":"занимает"}],"steps":["Платежи на 4 недели"],"numbers":[{"label":"Выручка","value":"5 000 000 ₸"}]},"send":false}`)
	if code != 200 || out["sum"] == nil || out["pz"] != nil {
		t.Fatalf("save %d %v", code, out)
	}
	code, out, _ = do("PUT", "/t/razbor/tg901", `{"form":{"diag":[{"title":"Кассовые разрывы","why":"занимает"}],"steps":["Платежи на 4 недели"]},"send":true}`)
	if code != 200 || out["pz"] == nil || !strings.Contains(fmt.Sprint(out["status"]), "Сценарий") {
		t.Fatalf("send %d %v", code, out)
	}
	if code, _, body := do("GET", "/t/razbor/tg901/pdf", ""); code != 200 || !strings.HasPrefix(string(body), "%PDF") {
		t.Fatalf("pdf %d", code)
	}
	if _, out, _ := do("GET", "/t/seq", ""); !strings.Contains(fmt.Sprint(out["seq"]), "tg901") {
		t.Fatalf("seq %v", out)
	}
	// R70: consent comes with residency: no choice, no line about it in the profile
	if code, out, _ := do("GET", "/r/me", ""); code != 200 || out["consent"] != "anon" || out["consentChosen"] != true || out["name"] != "Айдана Ким" || out["consentNote"] != nil {
		t.Fatalf("me %d %v", code, out)
	}
	e.at(e.clock().Add(8 * 24 * time.Hour))
	if _, out, _ := do("GET", "/r/me", ""); out["consentNote"] != nil || out["consent"] != "anon" {
		t.Fatalf("the line stays after a week: %v", out)
	}
	if code, _, _ := do("GET", "/o/me", ""); code != 403 {
		t.Fatalf("stranger %d", code)
	}
	if code, out, _ := do("PUT", "/r/me/consent", `{"mode":"anon"}`); code != 200 || out["consent"] != "anon" || out["consentChosen"] != true {
		t.Fatalf("consent %d %v", code, out)
	}
	if _, out, _ := do("GET", "/r/me", ""); out["consentNote"] != nil {
		t.Fatalf("a chosen consent still shows the line: %v", out)
	}
	if code, out, _ := do("POST", "/r/me/renew", `{"months":3}`); code != 200 || fmt.Sprint(sMap(out, "request")["status"]) != "Ожидает оплату продления" {
		t.Fatalf("renew %d %v", code, out)
	}
	if code, _, _ := do("POST", "/r/me/renew", `{"months":5}`); code != 400 {
		t.Fatal("bad period")
	}
}
