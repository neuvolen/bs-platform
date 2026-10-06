package http

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
)

func r47Lead(m map[string]any) map[string]any { return m }

// R47: every lead of the CRM gets a segment and a source by the rules; the
// imported base's source column (kept in the note by the old import) is read;
// resident duplicates, site leads, warm and cold ones are told apart.
func TestR47SegmentCRM(t *testing.T) {
	now := time.Date(2026, 10, 6, 12, 0, 0, 0, almaty)
	leads := []any{
		r47Lead(map[string]any{"id": "b1", "name": "Айгерим Сапарова", "phone": "+7 701 111 22 33", "base": "Клиенты 2024", "source": "База: Клиенты 2024", "note": "Источник в базе: Инстаграм"}),
		r47Lead(map[string]any{"id": "b2", "name": "Ержан", "phone": "87012223344", "base": "Клиенты 2024", "source": "База: Клиенты 2024", "note": ""}),
		r47Lead(map[string]any{"id": "b3", "name": "Олег", "tg": "@oleg", "base": "Telegram контакты", "source": "База: Telegram контакты"}),
		r47Lead(map[string]any{"id": "b4", "name": "Без контактов", "base": "Клиенты 2024", "source": "База: Клиенты 2024"}),
		r47Lead(map[string]any{"id": "b5", "name": "Рустам Кабден", "phone": "+77079998877", "base": "Клиенты 2024", "source": "База: Клиенты 2024"}),
		r47Lead(map[string]any{"id": "tg77", "name": "Бот Лид", "tgId": float64(77), "funnel": "bot", "source": "Threads: 99 чек-листов", "botReplyAt": "2026-10-05T10:00:00Z"}),
		r47Lead(map[string]any{"id": "site1", "name": "С сайта", "phone": "+77015550000", "funnel": "site", "form": "Заявка на разбор", "source": "Сайт: Заявка на разбор"}),
		r47Lead(map[string]any{"id": "b6", "name": "Старый контакт", "phone": "+77016660000", "base": "Клиенты 2024", "source": "База: Клиенты 2024", "lastContact": "01.09.2026"}),
		r47Lead(map[string]any{"id": "b7", "name": "Резидент По Тг", "tgId": float64(1001), "base": "Клиенты 2024", "source": "База: Клиенты 2024"}),
		r47Lead(map[string]any{"id": "b8", "name": "Был на разборе", "phone": "+77017770000", "col": "diag", "source": "Не указан"}),
		r47Lead(map[string]any{"id": "b9", "name": "Форма", "phone": "+77018880000", "base": "Выгрузка", "source": "База: Выгрузка", "srcTag": "Tilda, лендинг"}),
	}
	crm := map[string]any{"leads": leads}
	ppl := SegPeople{TgIDs: map[int64]bool{1001: true}, Names: map[string]bool{"рустам кабден": true}, Phones: map[string]bool{"7016660000": false}}
	rep := SegmentCRM(crm, ppl, now)
	got := map[string][2]string{}
	for _, x := range crm["leads"].([]any) {
		l := x.(map[string]any)
		got[pStr(l, "id")] = [2]string{pStr(l, "seg"), pStr(l, "segSrc")}
	}
	want := map[string][2]string{
		"b1":    {SegPhone, "Instagram"},
		"b2":    {SegPhone, segNoSrc},
		"b3":    {SegCold, "Telegram"},
		"b4":    {SegNone, segNoSrc},
		"b5":    {SegResident, segNoSrc},
		"tg77":  {SegWarm, "Threads"},
		"site1": {SegSite, "Сайт (Tilda)"},
		"b6":    {SegWarm, segNoSrc},
		"b7":    {SegResident, segNoSrc},
		"b8":    {SegWarm, segNoSrc},
		"b9":    {SegSite, "Сайт (Tilda)"},
	}
	for id, w := range want {
		if got[id] != w {
			t.Errorf("%s: got %v want %v", id, got[id], w)
		}
	}
	if rep.Total != 11 || rep.Changed != 11 || rep.BySeg[SegPhone] != 2 || rep.ByFile["Клиенты 2024"]["Instagram"] != 1 || rep.Forms["Заявка на разбор"] != 1 {
		t.Fatalf("report %+v", rep)
	}
	l1 := crm["leads"].([]any)[0].(map[string]any)
	if l1["srcTag"] != "Инстаграм" || l1["segFile"] != "Клиенты 2024" {
		t.Fatalf("srcTag/file: %v", l1)
	}
	// a second run changes nothing; a rule edited by the team moves the source
	if r := SegmentCRM(crm, ppl, now.Add(time.Hour)); r.Changed != 0 {
		t.Fatalf("second run changed %d", r.Changed)
	}
	crm["segRules"] = []any{map[string]any{"n": "Клиенты компании", "any": []any{"клиенты"}}}
	if r := SegmentCRM(crm, ppl, now); r.BySrc["Клиенты компании"] < 4 {
		t.Fatalf("team rule: %+v", r.BySrc)
	}
	// short tokens match word starts only: «wa» is not in «Kawasaki»
	if s := SegSource(map[string]any{"srcTag": "Kawasaki dealers"}, SegRulesDefault()); s != segNoSrc {
		t.Fatalf("wa matched inside a word: %s", s)
	}
	// the base stays in «База» whatever the file is called
	pipes := CrmPipesDefault()
	if p := CrmPipeOf(map[string]any{"source": "База: Telegram контакты", "base": "Telegram контакты"}, pipes); p != "base" {
		t.Fatalf("pipe %s", p)
	}
	if p := CrmPipeOf(map[string]any{"source": "Threads: 99"}, pipes); p != "threads" {
		t.Fatalf("pipe %s", p)
	}
}

// The endpoint: «Пересчитать» marks the leads; the loop runs only when a lead lacks a segment.
func TestR47SegmentEndpoint(t *testing.T) {
	docs := &r38Docs{}
	docs.set(t, "bs_crm", map[string]any{"leads": []any{map[string]any{"id": "b1", "name": "А", "phone": "+77010000001", "base": "Файл", "source": "База: Файл"}}})
	p := NewPartners(docs)
	p.now = func() time.Time { return time.Date(2026, 10, 6, 12, 0, 0, 0, almaty) }
	if r, err := p.SegmentNow(context.Background(), false); err != nil || r == nil || r.Changed != 1 {
		t.Fatalf("first: %+v %v", r, err)
	}
	puts := docs.puts
	if _, err := p.SegmentNow(context.Background(), false); err != nil || docs.puts != puts {
		t.Fatalf("idle run wrote: %d → %d", puts, docs.puts)
	}
	gin.SetMode(gin.TestMode)
	r := gin.New()
	g := r.Group("/api/v1/platform")
	g.Use(func(c *gin.Context) { c.Set("role", "admin") })
	p.Register(r, g)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest("POST", "/api/v1/platform/crm/segment", nil))
	var rep SegReport
	_ = json.Unmarshal(w.Body.Bytes(), &rep)
	if w.Code != 200 || rep.BySeg[SegPhone] != 1 {
		t.Fatalf("%d %s", w.Code, w.Body.String())
	}
	var crm map[string]any
	d, _ := docs.GetDoc(context.Background(), "club", "bs_crm")
	_ = json.Unmarshal([]byte(d.Value), &crm)
	if crm["segReport"] == nil {
		t.Fatal("no report in bs_crm")
	}
}

const tildaCSV = "\ufefftranid;formid;formname;Name;Phone;Email;Telegram;Comment;referer;utm_source;utm_campaign;sent;COOKIES\n" +
	"5550001;form1;Заявка на разбор;Айдар;+7 (701) 123-45-67;aidar@mail.kz;;Хочу на разбор;https://bxclub.kz/;ig;oct;2025-11-03 14:20:11;x=1\n" +
	"5550002;form2;Бизнес-завтрак;Мария;87019998877;;@maria_kz;;https://bxclub.kz/breakfast;;;2026-02-14 09:05:00;\n" +
	"5550003;form1;Заявка на разбор;Айдар повтор;+77011234567;;;Ещё раз;https://bxclub.kz/;;;2026-03-01 10:00:00;\n" +
	"5550004;form1;Заявка на разбор;Уже в CRM;+7 702 000 11 22;;;;https://bxclub.kz/;;;2026-04-10 12:00:00;\n" +
	"5550005;form3;Подписка;;;;;;https://bxclub.kz/;;;2026-05-01 12:00:00;\n"

// R47: the Tilda export (CSV): auto mapping, dedupe with the CRM and inside
// the file, «Сайт (Tilda): <форма>», the request's date kept, the existing
// card marked; dry shows the same counts and writes nothing.
func TestR47TildaImport(t *testing.T) {
	rows, err := ParseTildaCSV(tildaCSV)
	if err != nil || len(rows) != 6 || !LooksTilda(rows[0]) {
		t.Fatalf("parse: %v %d", err, len(rows))
	}
	if LooksTilda([]string{"Имя", "Телефон", "Город"}) {
		t.Fatal("a plain base taken for Tilda")
	}
	now := time.Date(2026, 10, 6, 12, 0, 0, 0, almaty)
	mk := func() map[string]any {
		return map[string]any{"leads": []any{map[string]any{"id": "x1", "name": "Уже в CRM", "phone": "+77020001122", "source": "WhatsApp", "col": "work"}}}
	}
	crm := mk()
	dry := ImportTilda(crm, rows, "leads.csv", now, true)
	if dry.Rows != 5 || dry.Fresh != 2 || dry.Dup != 1 || dry.DupFile != 1 || dry.Skip != 1 || dry.Updated != 1 || dry.Done {
		t.Fatalf("dry %+v", dry)
	}
	if len(crm["leads"].([]any)) != 1 || dry.From != "03.11.2025" || dry.To != "10.04.2026" || dry.Forms["Заявка на разбор"] != 3 {
		t.Fatalf("dry wrote or range: %d %s %s %v", len(crm["leads"].([]any)), dry.From, dry.To, dry.Forms)
	}
	rep := ImportTilda(crm, rows, "leads.csv", now, false)
	if rep.Fresh != 2 || !rep.Done {
		t.Fatalf("import %+v", rep)
	}
	ls := crm["leads"].([]any)
	if len(ls) != 3 {
		t.Fatalf("leads %d", len(ls))
	}
	a := ls[1].(map[string]any)
	// R50: one person = one card from the latest request, both in the history
	if a["id"] != "site5550003" || a["source"] != "Сайт (Tilda): Заявка на разбор" || a["date"] != "01.03.2026" || a["phone"] != "+77011234567" ||
		a["tildaN"] != 2 || !strings.Contains(pStr(a, "note"), "Ещё раз") || len(asList(a["log"])) != 3 ||
		a["email"] != "aidar@mail.kz" || a["funnel"] != "site" || !strings.Contains(pStr(a, "note"), "Хочу на разбор") ||
		a["utm"].(map[string]any)["utm_source"] != "ig" || strings.Contains(pStr(a, "note"), "x=1") {
		t.Fatalf("lead %v", a)
	}
	m := ls[2].(map[string]any)
	if m["tg"] != "@maria_kz" || m["form"] != "Бизнес-завтрак" || m["date"] != "14.02.2026" {
		t.Fatalf("maria %v", m)
	}
	x := ls[0].(map[string]any)
	if x["form"] != "Заявка на разбор" || x["tildaAt"] == nil || x["source"] != "WhatsApp" || !logHas(x, "экспорте заявок Tilda") {
		t.Fatalf("existing card %v", x)
	}
	// the same file again: nothing new, nothing marked twice
	if again := ImportTilda(crm, rows, "leads.csv", now, false); again.Fresh != 0 || again.Updated != 0 {
		t.Fatalf("again %+v", again)
	}
	// segments: the Tilda leads land in «Сайт (Tilda)» with the form
	SegmentCRM(crm, SegPeople{}, now)
	if a["seg"] != SegSite || a["segForm"] != "Заявка на разбор" || x["seg"] != SegSite {
		t.Fatalf("segments %v / %v", a["seg"], x["seg"])
	}
	// Tilda CRM export (Russian headers, commas)
	ru := "Имя,Телефон,Email,Название формы,Дата создания\nИван,+77015554433,,Консультация,12.03.2026 10:00\n"
	rr, _ := ParseTildaCSV(ru)
	r2 := ImportTilda(map[string]any{}, rr, "crm.csv", now, true)
	if r2.Fresh != 1 || r2.Forms["Консультация"] != 1 || r2.From != "12.03.2026" {
		t.Fatalf("tilda crm %+v", r2)
	}
}

func TestR47TildaImportEndpoint(t *testing.T) {
	docs := &r38Docs{}
	docs.set(t, "bs_crm", map[string]any{"leads": []any{}})
	p := NewPartners(docs)
	p.now = func() time.Time { return time.Date(2026, 10, 6, 12, 0, 0, 0, almaty) }
	gin.SetMode(gin.TestMode)
	r := gin.New()
	g := r.Group("/api/v1/platform")
	g.Use(func(c *gin.Context) { c.Set("role", "admin") })
	p.Register(r, g)
	post := func(dry bool) map[string]any {
		b, _ := json.Marshal(map[string]any{"csv": tildaCSV, "file": "leads.csv", "dry": dry})
		w := httptest.NewRecorder()
		r.ServeHTTP(w, httptest.NewRequest("POST", "/api/v1/platform/crm/tilda-import", strings.NewReader(string(b))))
		if w.Code != http.StatusOK {
			t.Fatalf("%d %s", w.Code, w.Body.String())
		}
		var j map[string]any
		_ = json.Unmarshal(w.Body.Bytes(), &j)
		return j
	}
	if j := post(true); j["fresh"].(float64) != 3 || j["tilda"] != true || j["done"] == true {
		t.Fatalf("dry %v", j)
	}
	if j := post(false); j["fresh"].(float64) != 3 || j["done"] != true {
		t.Fatalf("import %v", j)
	}
	var crm map[string]any
	d, _ := docs.GetDoc(context.Background(), "club", "bs_crm")
	_ = json.Unmarshal([]byte(d.Value), &crm)
	if n := len(crm["leads"].([]any)); n != 3 || len(asList(crm["tildaImports"])) != 1 || crm["segReport"] == nil {
		t.Fatalf("stored %d %v", n, crm["tildaImports"])
	}
	for _, x := range crm["leads"].([]any) {
		if x.(map[string]any)["seg"] != SegSite {
			t.Fatalf("not segmented %v", x)
		}
	}
	_ = fmt.Sprint
}
