package http

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/bnursik/business_surgery_backend/internal/content"
	pg "github.com/bnursik/business_surgery_backend/internal/repository/pg"
)

// R32b: личный порядок меню не перебивается старой копией в области клуба.
func TestDropClubPersonal(t *testing.T) {
	in := []pg.PlatformDoc{{Scope: "club", Key: "bs_order", Value: "old"}, {Scope: "user:1", Key: "bs_order", Value: "mine"},
		{Scope: "club", Key: "bs_crm", Value: "{}"}, {Scope: "club", Key: "bs_theme"}}
	out := dropClubPersonal(in)
	if len(out) != 2 || out[0].Value != "mine" || out[1].Key != "bs_crm" {
		t.Fatalf("%+v", out)
	}
}

func TestBuildInsights(t *testing.T) {
	now := time.Date(2026, 10, 3, 12, 0, 0, 0, almaty) // суббота, неделя с 28.09
	raw := map[string]string{
		"bs_crm": `{"leads":[
			{"id":"1","col":"new","source":"Threads","startAt":"2026-09-30T05:00:00Z","ck":{"started":["c1"]}},
			{"id":"2","col":"meet","source":"threads пост","startAt":"2026-10-01T05:00:00Z","express":{"at":"x"}},
			{"id":"3","col":"won","source":"Реферал","date":"29.09.2026"},
			{"id":"4","col":"work","source":"Instagram","startAt":"2026-09-22T05:00:00Z"},
			{"id":"5","col":"new","source":"","deleted":true}]}`,
		contentKey: `{"queue":[{"id":"a","at":"2026-09-29T05:00:00Z","status":"published"},{"id":"b","at":"2026-09-30T05:00:00Z","status":"failed","error":"Threads: токен истёк"},{"id":"c","at":"2026-10-02T05:00:00Z","status":"scheduled"}],"history":[{"id":"a","at":"2026-09-29T05:00:00Z","status":"published"}]}`,
		"bs_mkt_analysis": `{"actions":[{"text":"x","priority":"высокий","status":"done","done":true},{"text":"y","priority":"высокий"},{"text":"z","priority":"средний","status":"work"}]}`,
		platformSeedKey: `{"PL_MONTHS":["Август","Сентябрь","Октябрь"],"PL_ROWS":[{"name":"ИТОГО ДОХОДЫ","vals":[1000000,2000000,0]},{"name":"ИТОГО РАСХОДЫ","vals":[500000,700000,0]},{"name":"ЧИСТАЯ ПРИБЫЛЬ","vals":[500000,1300000,0]}],
			"RESIDENTS":[{"name":"А","rest":100000},{"name":"Б","rest":0}],
			"FINES":[{"res":"А","type":"Не сдан отчёт","amount":10000,"date":"01.10.2026","status":"Не оплатил"},{"res":"Б","type":"Не сдан отчёт","amount":10000,"date":"10.09.2026","status":"Оплатил"}],
			"SDATA":{"schedule":[{"res":"А","date":"04.10.2026","time":"10:00"}],"nps":[{"score":9},{"score":5}]}}`,
		aiRecsKey:     `{"items":[{"id":"r1","title":"SPIN","date":"03.10.2026","status":"new"},{"id":"r2","title":"OKR","status":"added"}]}`,
		eventsFeedKey: `{"items":[{"title":"Форум","date":"2026-10-08"},{"title":"Старое","date":"2026-09-01"}]}`,
		"bs_kanban":   `{"cards":[{"id":"1","col":"work","due":"2026-10-01"},{"id":"2","col":"done","doneAt":"2026-10-02T10:00:00Z"},{"id":"3","col":"idea"}]}`,
		"bs_ck_stats": `{"users":{"1":{"items":{"c1":{"started":"x","finished":"y"},"c2":{"started":"x"}}}}}`,
		"bs_gallup":   `{"b1":{"themes":["context","achiever","learner","input","focus","x"]},"b2":{"themes":["context"]}}`,
	}
	docs := map[string]any{}
	for k, v := range raw {
		var x any
		if err := json.Unmarshal([]byte(v), &x); err != nil {
			t.Fatal(k, err)
		}
		docs[k] = x
	}
	boards := []map[string]any{
		{"nodes": []any{map[string]any{"type": "diag", "title": "Нет учёта", "organ": "Финансы"}, map[string]any{"type": "tool", "title": "x"}}},
		{"nodes": []any{map[string]any{"type": "diag", "title": "Нет учёта", "organ": "Финансы"}, map[string]any{"type": "diag", "title": "Нет найма", "organ": "Команда"}}},
		{"archived": true, "nodes": []any{map[string]any{"type": "diag", "title": "Старый", "organ": "Продажи"}}},
	}
	cards := buildInsights(docs, boards, now)
	by := map[string]insightCard{}
	for _, c := range cards {
		by[c.ID] = c
		if strings.Contains(c.Take, "—") || c.Area == "" || c.Src == "" || c.Take == "" {
			t.Errorf("card %s: %+v", c.ID, c)
		}
	}
	want := []string{"crm_funnel", "leads_src", "mkt_path", "content_week", "mkt_plan", "fin_profit", "fin_debts", "club_reports", "club_meet", "club_nps", "track_health", "team_gallup", "ai_recs", "club_events", "team_tasks", "ck_progress"}
	for _, id := range want {
		if _, ok := by[id]; !ok {
			t.Errorf("no card %s", id)
		}
	}
	if c := by["crm_funnel"]; c.N != 4 || !strings.Contains(c.Take, "До разбора дошли 2 из 4") {
		t.Errorf("funnel: %+v", c)
	}
	if c := by["leads_src"]; c.N != 3 || !strings.Contains(c.Take, "«Threads»: 2") || !strings.Contains(c.Take, "На 2 больше") {
		t.Errorf("src: %+v", c)
	}
	if c := by["mkt_path"]; !strings.Contains(c.Take, "Из 4 пришедших в бот до клуба дошли 1") {
		t.Errorf("path: %+v", c)
	}
	if c := by["content_week"]; c.V != "1 из 3" || c.Tone != "bad" || !strings.Contains(c.Take, "токен истёк") {
		t.Errorf("content: %+v", c)
	}
	if c := by["mkt_plan"]; c.V != "1 из 3" || !strings.Contains(c.Take, "Высокий приоритет ждёт: 1") {
		t.Errorf("plan: %+v", c)
	}
	if c := by["fin_profit"]; c.N != 1300000 || c.Label != "чистая прибыль · Сентябрь" || c.Tone != "good" || !strings.Contains(c.Take, "1 300 000 ₸") || !strings.Contains(c.Take, "на 800 000 ₸") {
		t.Errorf("profit: %+v", c)
	}
	if c := by["fin_debts"]; c.N != 110000 {
		t.Errorf("debts: %+v", c)
	}
	if c := by["club_reports"]; c.N != 1 {
		t.Errorf("reports: %+v", c)
	}
	if c := by["club_nps"]; c.V != "7,0" || !strings.Contains(c.Take, "Недовольных (6 и ниже): 1") {
		t.Errorf("nps: %+v", c)
	}
	if c := by["track_health"]; !strings.Contains(c.Take, "«Нет учёта» у 2 из 2") || !strings.Contains(c.Take, "«Финансы»") {
		t.Errorf("health: %+v", c)
	}
	if c := by["team_gallup"]; c.N != 2 {
		t.Errorf("gallup: %+v", c)
	}
	if c := by["team_tasks"]; c.N != 1 || !strings.Contains(c.Take, "За неделю закрыто 1") {
		t.Errorf("tasks: %+v", c)
	}
	if c := by["ck_progress"]; !strings.Contains(c.Take, "Начато чек-листов 2, пройдено до конца 1 (50%)") {
		t.Errorf("ck: %+v", c)
	}
	if cards[0].Tone != "bad" {
		t.Errorf("bad cards first: %+v", cards[0])
	}
	if len(buildInsights(map[string]any{}, nil, now)) != 0 {
		t.Error("empty docs give cards")
	}
}

func TestMarketingActionIDs(t *testing.T) {
	var m struct {
		Actions []map[string]any `json:"actions"`
	}
	if err := json.Unmarshal(content.Marketing, &m); err != nil || len(m.Actions) != 12 {
		t.Fatal(err, len(m.Actions))
	}
	hi := 0
	for _, a := range m.Actions {
		id, _ := a["id"].(string)
		if strings.Contains(a["priority"].(string), "высок") {
			hi++
			if _, ok := mktActionPlans[id]; !ok {
				t.Errorf("high action %s has no plan", id)
			}
		}
	}
	if hi != len(mktActionPlans) {
		t.Errorf("plans %d for %d high actions", len(mktActionPlans), hi)
	}
	for id, p := range mktActionPlans {
		if strings.Contains(p.Note, "—") || (p.Status == "work") != (p.Task != nil) {
			t.Errorf("%s: %+v", id, p)
		}
	}
}

func TestMergeMktActions(t *testing.T) {
	var doc map[string]any
	_ = json.Unmarshal(content.Marketing, &doc)
	acts := doc["actions"].([]any)
	// прод: id ещё нет, одно действие команда уже отметила, одно переименовала
	for _, x := range acts {
		delete(x.(map[string]any), "id")
	}
	acts[5].(map[string]any)["done"] = true
	acts[2].(map[string]any)["text"] = "Своя формулировка команды"
	n := mergeMktActions(doc, map[string]string{"a4": "mkt-a4"}, time.Now())
	if n == 0 {
		t.Fatal("nothing merged")
	}
	get := func(i int) map[string]any { return acts[i].(map[string]any) }
	if get(0)["status"] != "done" || get(0)["done"] != true || get(0)["note"] == nil || get(0)["id"] != "a1" {
		t.Errorf("a1: %v", get(0))
	}
	if get(3)["status"] != "work" || get(3)["task"] != "mkt-a4" {
		t.Errorf("a4: %v", get(3))
	}
	if get(4)["status"] != nil { // задачи на доске нет: статус не ставим
		t.Errorf("a5 without task: %v", get(4))
	}
	if get(5)["status"] != nil || get(5)["note"] != nil { // команда уже решила
		t.Errorf("a6 team: %v", get(5))
	}
	if get(2)["id"] != nil || get(2)["status"] != nil {
		t.Errorf("renamed: %v", get(2))
	}
	if get(6)["status"] != nil || get(6)["id"] != "a7" { // средний приоритет: только id
		t.Errorf("a7: %v", get(6))
	}
	if mergeMktActions(doc, map[string]string{"a4": "mkt-a4"}, time.Now()) != 0 {
		t.Error("second merge changed something")
	}
}

func TestMigrateMktActions(t *testing.T) {
	repo, ctx := testPlatformDB(t, mktActionsKey, "bs_mkt_analysis", "bs_kanban", "bs_scripts")
	var doc map[string]any
	_ = json.Unmarshal(content.Marketing, &doc)
	for _, x := range doc["actions"].([]any) {
		delete(x.(map[string]any), "id")
	}
	v, _ := json.Marshal(doc)
	for k, val := range map[string]string{
		"bs_mkt_analysis": string(v),
		"bs_kanban":       `{"cards":[{"id":"k1","col":"idea","t":"Своя"}],"goals":[]}`,
		"bs_scripts":      `[{"t":"Первый ответ на заявку","s":"","msg":"","steps":[]}]`,
	} {
		if _, err := repo.PutDoc(ctx, "club", k, 0, val, false, "team"); err != nil {
			t.Fatal(err)
		}
	}
	h := NewPlatformAI(repo, nil)
	h.MigrateMktActions(ctx)
	d, _ := repo.GetDoc(ctx, "club", "bs_kanban")
	var kb map[string]any
	_ = json.Unmarshal([]byte(d.Value), &kb)
	cards := kb["cards"].([]any)
	if len(cards) != 3 || !strings.Contains(d.Value, `"id":"mkt-a4"`) || !strings.Contains(d.Value, `"id":"mkt-a5"`) || !strings.Contains(d.Value, `"who":"Рустам"`) || !strings.Contains(d.Value, `"id":"k1"`) {
		t.Fatalf("kanban: %s", d.Value)
	}
	d, _ = repo.GetDoc(ctx, "club", "bs_scripts")
	if !strings.Contains(d.Value, "После разбора: диагноз, план на 10 дней") || !strings.Contains(d.Value, "Первый ответ на заявку") {
		t.Fatalf("scripts: %s", d.Value)
	}
	d, _ = repo.GetDoc(ctx, "club", "bs_mkt_analysis")
	var out map[string]any
	_ = json.Unmarshal([]byte(d.Value), &out)
	st := map[string]string{}
	for _, x := range out["actions"].([]any) {
		a := x.(map[string]any)
		s, _ := a["status"].(string)
		st[a["id"].(string)] = s
	}
	for id, w := range map[string]string{"a1": "done", "a2": "done", "a3": "done", "a4": "work", "a5": "work", "a6": "done", "a11": "done", "a7": "", "a12": ""} {
		if st[id] != w {
			t.Errorf("%s: %q want %q", id, st[id], w)
		}
	}
	// второй запуск ничего не трогает: команда сняла отметку, она остаётся снятой
	cur, _ := repo.GetDoc(ctx, "club", "bs_mkt_analysis")
	edited := strings.Replace(cur.Value, `"status":"done"`, `"status":"",`+`"x":1,"status2":"done"`, 1)
	_, _ = repo.PutDoc(ctx, "club", "bs_mkt_analysis", cur.Version, edited, false, "team")
	h.MigrateMktActions(ctx)
	if d, _ = repo.GetDoc(ctx, "club", "bs_mkt_analysis"); d.Value != edited {
		t.Fatal("second run changed the doc")
	}
}

func TestWarmStepsHaveCases(t *testing.T) {
	steps := warmSteps()
	if len(steps) != 5 {
		t.Fatalf("steps: %d", len(steps))
	}
	prev := 0.0
	for i, s := range steps {
		txt := s.text("Айдар", "Платёжный календарь", 1, 0)
		if !strings.Contains(txt, "Кейс: ") || strings.Contains(txt, "—") || s.day <= prev {
			t.Errorf("step %d: %q", i, txt)
		}
		prev = s.day
	}
}
