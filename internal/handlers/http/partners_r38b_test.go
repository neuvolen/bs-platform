package http

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/bnursik/business_surgery_backend/internal/bot"
	"github.com/bnursik/business_surgery_backend/internal/club"
	"github.com/bnursik/business_surgery_backend/internal/content"
	pg "github.com/bnursik/business_surgery_backend/internal/repository/pg"
	"github.com/gin-gonic/gin"
)

// R38b: партнёры (pt_ в боте, ?ref на сайте, выплаты), воронки CRM, импорт базы.

type r38Docs struct {
	mu   sync.Mutex
	docs map[string]*pg.PlatformDoc
	puts int
}

func (d *r38Docs) GetDoc(_ context.Context, scope, key string) (*pg.PlatformDoc, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if x := d.docs[scope+"/"+key]; x != nil {
		c := *x
		return &c, nil
	}
	return nil, nil
}

func (d *r38Docs) PutDoc(_ context.Context, scope, key string, base int, value string, deleted bool, by string) (*pg.PlatformDoc, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.docs == nil {
		d.docs = map[string]*pg.PlatformDoc{}
	}
	cur := d.docs[scope+"/"+key]
	v := 0
	if cur != nil {
		v = cur.Version
	}
	if base != v {
		return nil, pg.ErrPlatformConflict
	}
	d.puts++
	x := &pg.PlatformDoc{Scope: scope, Key: key, Value: value, Version: v + 1, Deleted: deleted, UpdatedBy: by}
	d.docs[scope+"/"+key] = x
	return x, nil
}

func (d *r38Docs) set(t *testing.T, key string, v any) {
	t.Helper()
	b, _ := json.Marshal(v)
	d.mu.Lock()
	cur := d.docs["club/"+key]
	d.mu.Unlock()
	base := 0
	if cur != nil {
		base = cur.Version
	}
	if _, err := d.PutDoc(context.Background(), "club", key, base, string(b), false, "test"); err != nil {
		t.Fatal(err)
	}
}

func (d *r38Docs) get(t *testing.T, scope, key string) map[string]any {
	t.Helper()
	x, _ := d.GetDoc(context.Background(), scope, key)
	if x == nil {
		return nil
	}
	var m map[string]any
	if err := json.Unmarshal([]byte(x.Value), &m); err != nil {
		t.Fatal(err)
	}
	return m
}

func r38Leads(m map[string]any) []map[string]any {
	var out []map[string]any
	ls, _ := m["leads"].([]any)
	for _, x := range ls {
		if l, ok := x.(map[string]any); ok {
			out = append(out, l)
		}
	}
	return out
}

func TestR38bPartnerSeed(t *testing.T) {
	seed := partnersSeedList()
	if len(seed) < 30 || len(seed) > 50 {
		t.Fatalf("seed size %d", len(seed))
	}
	codes, types := map[string]bool{}, map[string]int{}
	for _, p := range seed {
		c := pStr(p, "code")
		if PartnerCode(c) != c || codes[c] {
			t.Fatalf("bad or repeated code %q", c)
		}
		codes[c] = true
		types[pStr(p, "type")]++
		for _, k := range []string{"name", "type", "city", "contact", "audience", "why", "offer", "priority", "verified"} {
			if strings.TrimSpace(pStr(p, k)) == "" {
				t.Fatalf("%s: no %s", c, k)
			}
		}
		b, _ := json.Marshal(p)
		if strings.Contains(string(b), "—") {
			t.Fatalf("%s: em dash", c)
		}
	}
	for _, ty := range []string{"gallup", "coach", "accounting", "legal", "bank", "coworking", "hr", "marketing", "association", "media", "events"} {
		if types[ty] == 0 {
			t.Fatalf("no partners of type %s: %v", ty, types)
		}
	}
	sortPartners(seed)
	if pStr(seed[0], "code") != "nurken" {
		t.Fatalf("top partner %s", pStr(seed[0], "code"))
	}

	now := time.Date(2026, 10, 5, 9, 0, 0, 0, time.UTC)
	doc := map[string]any{}
	if n := MergePartnersSeed(doc, seed, now); n != len(seed) {
		t.Fatalf("first merge %d", n)
	}
	st := doc["settings"].(map[string]any)
	if pNum(st["rewardClub"]) != 100000 || PartnerRazborReward(doc) != 5000 {
		t.Fatalf("settings %v", st)
	}
	if MergePartnersSeed(doc, seed, now) != 0 {
		t.Fatal("second merge added")
	}
	// The team renames one, deletes another, adds its own: nothing comes back, nothing is overwritten.
	parts := doc["partners"].([]any)
	parts[0].(map[string]any)["name"] = "Нуркен (переименован)"
	parts[0].(map[string]any)["stage"] = "meet"
	doc["partners"] = append(parts[2:3], parts[0], map[string]any{"id": "p_own", "code": "own", "name": "Свой партнёр"})
	if n := MergePartnersSeed(doc, seed, now); n != 0 {
		t.Fatalf("deleted came back: %d", n)
	}
	if len(doc["partners"].([]any)) != 3 || pStr(doc["partners"].([]any)[1].(map[string]any), "stage") != "meet" {
		t.Fatalf("team edits lost: %v", doc["partners"])
	}
	// A new partner in a later seed is added once.
	more := append(seed, map[string]any{"key": "newone", "code": "newone", "name": "Новый партнёр", "type": "hr"})
	if n := MergePartnersSeed(doc, more, now); n != 1 {
		t.Fatalf("new seed partner: %d", n)
	}
}

func TestR38bPartnerStart(t *testing.T) {
	if s := startSource("pt_nurken"); s != "Партнёр: nurken" {
		t.Fatalf("startSource: %q", s)
	}
	if startSource("pt_Bad Code!") == "Партнёр: bad code!" || PartnerCode("Bad Code!") != "" || PartnerCode("FiftyFour") != "fiftyfour" {
		t.Fatal("PartnerCode")
	}
	docs := &r38Docs{}
	var mu sync.Mutex
	sent := map[int64][]string{}
	send := func(_ context.Context, chat int64, text string, _ map[string]any) error {
		mu.Lock()
		sent[chat] = append(sent[chat], text)
		mu.Unlock()
		return nil
	}
	f := NewLeadFunnel(docs, send, []int64{111})
	p := &Partners{docs: docs, now: time.Now}
	if _, err := p.SeedOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	hook := f.WithReferrals(nil, map[int64]string{111: "Рустам"})
	if !hook(context.Background(), bot.StartUpdate{ChatID: 5001, Param: "pt_nurken", FirstName: "Айдар", Username: "aidar"}) {
		t.Fatal("not handled")
	}
	crm := docs.get(t, "club", "bs_crm")
	ls := r38Leads(crm)
	if len(ls) != 1 || pStr(ls[0], "source") != "Партнёр: Нуркен Ордабаев, Gallup Talents" || pStr(ls[0], "partner") != "nurken" || pStr(ls[0], "partnerName") == "" {
		t.Fatalf("lead %v", ls)
	}
	if len(sent[5001]) != 2 || !strings.Contains(sent[5001][0], "99 гайдов") {
		t.Fatalf("welcome %v", sent[5001])
	}
	if len(sent[111]) != 1 || !strings.Contains(sent[111][0], "Партнёр: Нуркен") {
		t.Fatalf("team note %v", sent[111])
	}
	// An unknown code still marks the lead; an old lead keeps its first source.
	hook(context.Background(), bot.StartUpdate{ChatID: 5002, Param: "pt_ghost", FirstName: "Б"})
	hook(context.Background(), bot.StartUpdate{ChatID: 5003, Param: "threads", FirstName: "В"})
	time.Sleep(10 * time.Millisecond)
	f.now = func() time.Time { return time.Now().Add(2 * time.Minute) }
	hook(context.Background(), bot.StartUpdate{ChatID: 5003, Param: "pt_nurken", FirstName: "В"})
	for _, l := range r38Leads(docs.get(t, "club", "bs_crm")) {
		switch leadTg(l) {
		case 5002:
			if pStr(l, "source") != "Партнёр: ghost" || pStr(l, "partner") != "ghost" {
				t.Fatalf("ghost %v", l)
			}
		case 5003:
			if pStr(l, "source") != "Threads" || l["partner"] != nil {
				t.Fatalf("old lead changed %v", l)
			}
		}
	}
	// The platform link sends to the bot with the mark and counts the click.
	gin.SetMode(gin.TestMode)
	r := gin.New()
	p.Bot = func() string { return "bsurgery_bot" }
	r.GET("/p/:code", p.Redirect)
	r.GET("/r", p.Redirect)
	for _, u := range []string{"/p/nurken", "/r?ref=nurken"} {
		w := httptest.NewRecorder()
		r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, u, nil))
		if w.Code != http.StatusFound || w.Header().Get("Location") != "https://t.me/bsurgery_bot?start=pt_nurken" {
			t.Fatalf("%s: %d %s", u, w.Code, w.Header().Get("Location"))
		}
	}
	deadline := time.Now().Add(2 * time.Second)
	for {
		pd := docs.get(t, "club", partnersKey)
		if pt := partnerFind(pd, "nurken", ""); pt != nil && pNum(pt["clicks"]) == 2 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("clicks not counted")
		}
		time.Sleep(20 * time.Millisecond)
	}
	// Tilda: ?ref=<code> in a hidden field
	tl := tildaParse(map[string]string{"name": "Сауле", "phone": "+7 701 111 22 33", "ref": "kenes", "formname": "Заявка"})
	if tl.Ref != "kenes" || tl.Source != "Партнёр: kenes" || tl.Name != "Сауле" {
		t.Fatalf("tilda %+v", tl)
	}
}

func TestR38bPayouts(t *testing.T) {
	now := time.Date(2026, 10, 5, 9, 0, 0, 0, time.UTC)
	pdoc := map[string]any{}
	MergePartnersSeed(pdoc, partnersSeedList(), now)
	// a tier and a custom reward
	for _, x := range pdoc["partners"].([]any) {
		p := x.(map[string]any)
		switch pStr(p, "code") {
		case "kenes":
			p["tier"] = "gold"
		case "qeepe":
			p["reward"] = 80000
		}
	}
	pdoc["deleted"] = []any{"p_ugolek"}
	crm := map[string]any{"leads": []any{
		map[string]any{"id": "a", "name": "Асхат", "col": "won", "partner": "nurken", "source": "Партнёр: Нуркен"},
		map[string]any{"id": "b", "name": "Бота", "col": "diag", "partner": "nurken"},
		map[string]any{"id": "c", "name": "Вика", "col": "new", "partner": "nurken"},
		map[string]any{"id": "d", "name": "Гани", "col": "won", "source": "Партнёр: Kenes Outsourcing (бухгалтерия)"}, // by name, no field
		map[string]any{"id": "e", "name": "Дана", "col": "won", "partner": "qeepe"},
		map[string]any{"id": "f", "name": "Ержан", "col": "won", "partner": "ugolek"}, // partner deleted by the team
		map[string]any{"id": "g", "name": "Жан", "col": "won", "source": "Threads"},
		map[string]any{"id": "h", "name": "Зере", "col": "meet", "partner": "talentcraft", "razborPaid": true},
	}}
	fresh := AccruePartnerPayouts(pdoc, crm, now)
	got := map[string]int{}
	for _, r := range fresh {
		got[pStr(r, "id")] = int(pNum(r["amount"]))
		if pStr(r, "status") != "accrued" {
			t.Fatalf("status %v", r)
		}
	}
	want := map[string]int{"club:a": 100000, "razbor:a": 5000, "razbor:b": 5000, "club:d": 150000, "razbor:d": 5000, "club:e": 80000, "razbor:e": 5000, "razbor:h": 5000}
	if len(got) != len(want) {
		t.Fatalf("rows %v", got)
	}
	for k, v := range want {
		if got[k] != v {
			t.Fatalf("%s: %d, want %d (%v)", k, got[k], v, got)
		}
	}
	// Each row once; a paid row stays paid.
	pdoc["ledger"].([]any)[0].(map[string]any)["status"] = "paid"
	if n := len(AccruePartnerPayouts(pdoc, crm, now)); n != 0 {
		t.Fatalf("again: %d", n)
	}
	if pStr(pdoc["ledger"].([]any)[0].(map[string]any), "status") != "paid" {
		t.Fatal("paid row changed")
	}
	// The разбор reward can be fixed or off.
	pdoc["settings"].(map[string]any)["razborMode"] = "fixed"
	pdoc["settings"].(map[string]any)["razborFixed"] = 7000
	if PartnerRazborReward(pdoc) != 7000 {
		t.Fatal("fixed")
	}
	pdoc["settings"].(map[string]any)["razborMode"] = "off"
	crm["leads"] = append(crm["leads"].([]any), map[string]any{"id": "i", "col": "diag", "partner": "nurken"})
	if n := len(AccruePartnerPayouts(pdoc, crm, now)); n != 0 {
		t.Fatalf("razbor off: %d", n)
	}
	// Through the docs: AccrueOnce writes the ledger.
	docs := &r38Docs{}
	docs.set(t, "bs_crm", crm)
	p := &Partners{docs: docs, now: func() time.Time { return now }}
	if _, err := p.SeedOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	// the base program (no tiers, nothing deleted): a, d, e, f club+razbor; b, h, i razbor
	if n := p.AccrueOnce(context.Background()); n != 11 {
		pd := docs.get(t, "club", partnersKey)
		t.Fatalf("AccrueOnce %d: %v", n, pd["ledger"])
	}
	if p.AccrueOnce(context.Background()) != 0 {
		t.Fatal("AccrueOnce twice")
	}
}

func TestR38bPipelines(t *testing.T) {
	pipes := CrmPipesDefault()
	ids := []string{}
	for _, p := range pipes {
		ids = append(ids, p.ID)
	}
	if strings.Join(ids, ",") != "partners,refs,threads,ig,site,tg,direct,base" {
		t.Fatalf("pipes %v", ids)
	}
	cases := []struct {
		lead map[string]any
		want string
	}{
		{map[string]any{"source": "Threads"}, "threads"},
		{map[string]any{"source": "Threads: Финансы под контролем"}, "threads"},
		{map[string]any{"source": "Instagram шапка"}, "ig"},
		{map[string]any{"source": "Карусель: деньги"}, "ig"},
		{map[string]any{"source": "Реклама: таргет октябрь"}, "ig"},
		{map[string]any{"source": "Сайт: Заявка на разбор"}, "site"},
		{map[string]any{"source": "Сайт: Заявка", "utm": map[string]any{"utm_source": "ig"}}, "ig"},
		{map[string]any{"source": "Telegram-канал: Кейс"}, "tg"},
		{map[string]any{"source": "Гайд: Финансы"}, "tg"},
		{map[string]any{"source": "Telegram: бот"}, "tg"},
		{map[string]any{"source": "Лид-магнит: Касса"}, "tg"},
		{map[string]any{"source": "Партнёр: Kenes"}, "partners"},
		{map[string]any{"source": "Threads", "partner": "kenes"}, "partners"},
		{map[string]any{"source": "Реферал: Асет"}, "refs"},
		{map[string]any{"source": "Telegram: бот", "ref": float64(12)}, "refs"},
		{map[string]any{"source": "Рекомендация"}, "refs"},
		{map[string]any{"source": "WhatsApp"}, "direct"},
		{map[string]any{"source": "QR офлайн"}, "direct"},
		{map[string]any{"source": "База: Холодная 2025"}, "base"},
		{map[string]any{"source": ""}, "base"},
	}
	for _, c := range cases {
		if got := CrmPipeOf(c.lead, pipes); got != c.want {
			t.Errorf("%v: %s, want %s", c.lead, got, c.want)
		}
	}
	// The platform keeps the same pipelines and rules.
	html, err := os.ReadFile("../../../web/platform.html")
	if err != nil {
		t.Fatal(err)
	}
	m := regexp.MustCompile(`var CRM_PIPES_DEF = (\[.*?\]);\n`).FindSubmatch(html)
	if m == nil {
		t.Fatal("CRM_PIPES_DEF not in platform.html")
	}
	var web []CrmPipe
	if err := json.Unmarshal(m[1], &web); err != nil {
		t.Fatal(err)
	}
	a, _ := json.Marshal(web)
	b, _ := json.Marshal(pipes)
	if string(a) != string(b) {
		t.Fatalf("platform pipes differ:\n%s\n%s", a, b)
	}
	_ = content.CrmPipes
}

type r38Base struct {
	sheets map[string][][]string
	starts []pg.BotStart
	chats  []pg.CrmChat
	ids    map[int64]bool
	names  map[string]bool
}

func (b *r38Base) SheetsByName(context.Context, []string) (map[string][][]string, error) {
	return b.sheets, nil
}
func (b *r38Base) BotStarts(context.Context) ([]pg.BotStart, error) { return b.starts, nil }
func (b *r38Base) CrmChats(context.Context) ([]pg.CrmChat, error)   { return b.chats, nil }
func (b *r38Base) ClubPeople(context.Context) (map[int64]bool, map[string]bool, error) {
	return b.ids, b.names, nil
}

func TestR38bBaseImport(t *testing.T) {
	at := time.Date(2026, 9, 20, 6, 0, 0, 0, time.UTC)
	src := &r38Base{
		sheets: map[string][][]string{
			club.SheetLeads: {
				{"Дата", "Имя", "Телефон", "Telegram", "Источник", "Кампания", "Ниша", "Оборот", "Запрос", "Статус", "Ответственный", "Комментарий"},
				{"12.08.2026 10:00", "Ерлан", "8 701 111 22 33", "@erlan", "Сайт: Разбор", "", "Кофейня", "5 млн", "Нет прибыли", "Новый", "", ""},
				{"13.08.2026", "Жанна", "+7 702 222 33 44", "", "Instagram", "oct", "Салон", "", "", "Отказ", "", "дорого"},
				{"14.08.2026", "Тимур", "+7 703 333 44 55", "", "", "", "", "", "", "", "", ""},             // already in the CRM
				{"15.08.2026", "Удалённый", "+7 704 444 55 66", "", "WhatsApp", "", "", "", "", "", "", ""}, // deleted by the team
				{"16.08.2026", "Ерлан К.", "+7 (701) 111-22-33", "", "Threads", "", "", "", "", "", "", ""}, // the same Ерлан
				{"", "", "", "", "", "", "", "", "", "", "", ""},
			},
			club.SheetDiagRequests: {{"Дата", "Имя", "Телефон", "Ниша", "Запрос", "Chat ID", "Статус"},
				{"01.09.2026", "Алия", "+7 705 555 66 77", "Стоматология", "Команда", "9001", "Записан"}},
			club.SheetDiagnostics: {{"Дата", "Chat ID", "Имя", "Telegram", "Общий %", "Слабый орган", "Сильный орган", "Ответы JSON", "Источник"},
				{"02.09.2026", "9001", "Алия С.", "@aliya", "54", "Финансы", "Продажи", "{}", "threads"}, // the same Алия by chat id
				{"03.09.2026", "777", "Резидент", "", "70", "", "", "", ""}},                             // a resident
			club.SheetLMHistory: {{"Chat ID", "Ключ", "Дата", "Название"}, {"9002", "cashflow", "9/3/2026 10:00:00", "Денежный поток"}},
			club.SheetAcceptsLog: {{"Дата/время", "Chat ID", "Username", "First Name", "Версия оферты", "Версия политики", "Источник"},
				{"03.09.2026 10:00", "9002", "nurlan", "Нурлан", "1", "1", ""}},
		},
		starts: []pg.BotStart{{ChatID: 9003, First: "Сауле", Username: "saule", Param: "th_g003", At: at}, {ChatID: 9001, First: "Алия", Param: "", At: at}},
		chats:  []pg.CrmChat{{Phone: "77016667788", Name: "Марат", At: at}, {Phone: "77011112233", Name: "Ерлан WA", At: at}},
		ids:    map[int64]bool{777: true},
		names:  map[string]bool{"рустам": true},
	}
	docs := &r38Docs{}
	docs.set(t, "bs_crm", map[string]any{
		"leads":   []any{map[string]any{"id": "L1", "name": "Тимур", "phone": "+7 703 333 44 55", "col": "work", "source": "WhatsApp", "note": "команда"}},
		"deleted": []any{"ph:77044445566"},
	})
	now := time.Date(2026, 10, 5, 9, 0, 0, 0, time.UTC)
	p := &Partners{docs: docs, now: func() time.Time { return now }, Base: src}
	rep, err := p.ImportBase(context.Background(), false)
	if err != nil {
		t.Fatal(err)
	}
	// Ерлан (sheet ×2 + WA), Жанна, Алия (request + diagnostic + start), Нурлан (magnet + accept), Сауле, Марат = 6 new;
	// Тимур in the CRM; the deleted one and the resident skipped.
	if rep.Added != 6 || rep.InCRM != 1 || rep.Skipped != 2 || rep.Total != 7 {
		t.Fatalf("report %+v", rep)
	}
	crm := docs.get(t, "club", "bs_crm")
	by := map[string]map[string]any{}
	for _, l := range r38Leads(crm) {
		by[pStr(l, "name")] = l
	}
	if l := by["Ерлан"]; l == nil || pStr(l, "source") != "Сайт: Разбор" || pStr(l, "id") != "ph77011112233" || pStr(l, "tg") != "@erlan" || pStr(l, "imp") != "base" {
		t.Fatalf("Ерлан %v", l)
	}
	if l := by["Жанна"]; l == nil || pStr(l, "col") != "lost" || pStr(l, "campaign") != "oct" {
		t.Fatalf("Жанна %v", l)
	}
	if l := by["Алия"]; l == nil || pStr(l, "id") != "tg9001" || pStr(l, "tg") != "@aliya" || pStr(l, "col") != "meet" || pStr(l, "source") != "Заявка на диагностику" {
		t.Fatalf("Алия %v", l)
	}
	if l := by["Нурлан"]; l == nil || pStr(l, "source") != "Лид-магнит: Денежный поток" || pStr(l, "tg") != "@nurlan" {
		t.Fatalf("Нурлан %v", l)
	}
	if l := by["Сауле"]; l == nil || !strings.HasPrefix(pStr(l, "source"), "Threads") {
		t.Fatalf("Сауле %v", l)
	}
	if l := by["Тимур"]; pStr(l, "col") != "work" || pStr(l, "note") != "команда" || l["imp"] != nil {
		t.Fatalf("Тимур changed %v", l)
	}
	bi, _ := crm["baseImport"].(map[string]any)
	if bi == nil || pNum(bi["added"]) != 6 {
		t.Fatalf("baseImport %v", crm["baseImport"])
	}
	if bp, _ := bi["byPipe"].(map[string]any); pNum(bp["site"]) != 1 || pNum(bp["ig"]) != 1 || pNum(bp["threads"]) != 1 || pNum(bp["tg"]) != 2 || pNum(bp["direct"]) != 1 {
		t.Fatalf("byPipe %v", bi["byPipe"])
	}
	// Once: the marker answers without touching the CRM; forced again it adds nothing.
	puts := docs.puts
	if r2, _ := p.ImportBase(context.Background(), false); r2.Added != 6 || docs.puts != puts {
		t.Fatalf("marker: %+v puts %d→%d", r2, puts, docs.puts)
	}
	r3, err := p.ImportBase(context.Background(), true)
	if err != nil || r3.Added != 0 || r3.InCRM != 7 || r3.Total != 7 {
		t.Fatalf("forced again: %+v %v", r3, err)
	}
	if n := len(r38Leads(docs.get(t, "club", "bs_crm"))); n != 7 {
		t.Fatalf("leads %d", n)
	}
	// A lead the team deletes after the import does not come back.
	crm = docs.get(t, "club", "bs_crm")
	var keep []any
	for _, l := range r38Leads(crm) {
		if pStr(l, "name") != "Марат" {
			keep = append(keep, l)
		}
	}
	crm["leads"], crm["deleted"] = keep, []any{"ph:77044445566", "ph77016667788", "ph:77016667788"}
	docs.set(t, "bs_crm", crm)
	if r4, _ := p.ImportBase(context.Background(), true); r4.Added != 0 {
		t.Fatalf("deleted came back: %+v", r4)
	}
}
