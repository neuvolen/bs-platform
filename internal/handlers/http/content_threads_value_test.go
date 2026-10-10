package http

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/gin-gonic/gin"
)

// thValueOn: the R81 policy for one test (the other Threads tests keep the
// classic and magnet plans, content_engine_test.go init).
func thValueOn(t *testing.T) {
	t.Helper()
	old := threadsValueOnly
	threadsValueOnly = true
	t.Cleanup(func() { threadsValueOnly = old })
}

// The gate lets through only concrete posts: the owner's «ерунда» is refused.
func TestThreadsValueGate(t *testing.T) {
	bad := map[string]string{
		"спорный вопрос": "Нанимать родственников в свой бизнес?\n\nЗа: доверие и преданность. Против: их сложно уволить и спросить за результат. А как считаете вы, стоит ли брать семью в бизнес? Пишите в ответах, интересно услышать разные мнения, у каждого своя история и свой опыт с этим.",
		"короткий":       "Сколько стоит ошибка найма? Дороже, чем кажется.",
		"общие слова":    "Как выстроить отдел продаж за 30 дней\n\nВажно понимать: успех отдела продаж зависит от мотивации людей. Каждый предприниматель хочет, чтобы продажи росли сами, но так не бывает. Нужна система, регулярность и внимание собственника. Начни с малого, и через месяц увидишь первые результаты своей работы, проверено на 12 компаниях.",
		"не X, а Y":      "Сколько клиентов теряет твой отдел продаж за месяц?\n\nПроблема не в рекламе, а в том, что заявки висят по 3 часа. 1. Посчитай заявки за неделю. 2. Отметь время первого ответа. 3. Поставь норму 5 минут и проверяй её каждую пятницу по 10 случайным заявкам из таблицы, это займёт 15 минут.",
		"без крючка":     "Платёжный календарь собирается просто и помогает видеть деньги заранее, без сюрпризов в день зарплаты\n\n1. Выпиши платежи за 3 месяца.\n2. Поставь даты.\n3. Внеси поступления.\n4. Считай остаток на каждый день, 25 минут в неделю.",
	}
	for name, body := range bad {
		if p := threadsValueProblems(body); len(p) == 0 {
			t.Errorf("%s: passed the gate:\n%s", name, body)
		}
	}
	good := "Платёжный календарь: 4 шага, 2 часа на первую сборку\n\n1. Выпиши все обязательные платежи с реальными датами.\n2. Внеси ожидаемые поступления по договорам.\n3. Посчитай остаток на конец каждого дня.\n4. Отметь красным дни ниже минимума.\n\nПример: у Динары 17 строк, зарплата 10 и 25 числа по 1,9 млн ₸, аренда склада 650 000 ₸ 5-го."
	if p := threadsValueProblems(good); len(p) > 0 {
		t.Fatalf("a good post refused: %v", p)
	}
	if n, what := thSpecifics(good); n < 4 {
		t.Fatalf("specifics %d %v", n, what)
	}
}

// The plan: half magnets, the rest «польза», no reach rubrics or opinions.
func TestThreadsValuePlan(t *testing.T) {
	for _, n := range []int{2, 3, 4, 8, 16} {
		for d := 0; d < 7; d++ {
			key := contentDay(time.Date(2026, 10, 10+d, 0, 0, 0, 0, almaty))
			f, mg := threadsValuePlan(n, key)
			m := 0
			for i, x := range f {
				if mg[i] != (x == "magnet") {
					t.Fatalf("%d %s: %v %v", n, key, f, mg)
				}
				if x == "magnet" {
					m++
					continue
				}
				if !IsThreadsValue(x) {
					t.Fatalf("%d %s: format %q", n, key, x)
				}
			}
			if want := threadsValueMgCount(n); m != want || (n == 4 && (m != 2 || !mg[1] || !mg[3])) {
				t.Fatalf("%d: magnets %d %v", n, m, f)
			}
		}
	}
}

// Every format composes posts from the library that pass both gates and fit
// one Threads post with the real link.
func TestThreadsValueCompose(t *testing.T) {
	tools, diags := threadsValuePool()
	if len(tools) < 200 || len(diags) < 150 {
		t.Fatalf("pool: %d tools, %d diagnoses", len(tools), len(diags))
	}
	for _, f := range threadsValueFormats {
		ok, tried := 0, 0
		var sample string
		for i := 0; i < 60; i++ {
			var v *thValueCand
			if f.ID == "v_steps" || f.ID == "v_metrics" {
				v, _ = buildValue(f.ID, tools[i*3%len(tools)], nil)
			} else {
				v, _ = buildValue(f.ID, nil, diags[i*3%len(diags)])
			}
			tried++
			if v == nil {
				continue
			}
			ok++
			if p := threadsValueProblems(v.Body); len(p) > 0 {
				t.Fatalf("%s: %v", f.ID, p)
			}
			it := valueItem(v)
			it.Channel, it.Kind, it.At = "threads", "threads", "2026-10-12T16:31:00+05:00"
			manualize(it)
			if n := utf8.RuneCountInString(it.Text); n > threadsMaxText || !strings.HasSuffix(it.Text, "/m/"+v.Magnet.ID+"-p2610121631") ||
				it.Link != contentBotLink+"mg_"+v.Magnet.ID+"_p2610121631" || strings.ContainsAny(it.Text, "—–") {
				t.Fatalf("%s: %d signs %q %q", f.ID, n, it.Text, it.Link)
			}
			if sample == "" {
				sample = it.Text
			}
		}
		if ok*2 < tried && f.ID != "v_metrics" || ok*4 < tried { // few tools have 3 metrics with numbers: v_metrics is the rarest
			t.Errorf("%s: only %d of %d library items make a post", f.ID, ok, tried)
		}
		t.Logf("%s: %d of %d\n%s", f.ID, ok, tried, sample)
	}
}

// The manual mode builds the week ahead: 4 posts a day, 2 magnets and 2
// «польза», nothing repeats; the old plan of today is re-planned once; the
// owner sees the week, replaces and approves posts.
func TestThreadsValueWeek(t *testing.T) {
	thValueOn(t)
	ctx := context.Background()
	now := time.Date(2026, 10, 10, 13, 32, 0, 0, almaty)
	e, _, tg := thManualEngine(t, &now)
	e.AI = func(context.Context, string, string) (string, error) {
		t.Fatal("the value policy does not call the AI")
		return "", nil
	}
	// today's old plan (R76): a reach post and a myth by the AI after now, one sent before
	old := []*contentItem{
		{contentItemData: contentItemData{ID: "th-20261010-0931", Channel: "threads", Kind: "threads", At: "2026-10-10T09:31:00+05:00", Status: "published", Gen: "lib", Auto: true, Text: "старый пост"}},
		{contentItemData: contentItemData{ID: "th-20261010-1634", Channel: "threads", Kind: "threads", At: "2026-10-10T16:34:00+05:00", Status: "planned", Gen: "ai", Auto: true, Format: "r_debate", Text: "Нанимать родственников? За и против."}},
		{contentItemData: contentItemData{ID: "th-20261010-1927", Channel: "threads", Kind: "threads", At: "2026-10-10T19:27:00+05:00", Status: "planned", Gen: "ai", Auto: true, Format: "myth", Text: "Миф: всё решает реклама."}},
	}
	if _, err := e.update(ctx, func(d *contentDoc) bool { d.Queue = append(d.Queue, old...); return true }); err != nil {
		t.Fatal(err)
	}
	e.saveState(ctx, func(s *contentState) {
		s.Threads = map[string]*threadsDayState{"20261010": {N: 4, Added: 2, At: now.UTC().Format(time.RFC3339), AI: "ok", Plan: 76}}
	})
	for i := 0; i < 10; i++ { // one day a tick
		d, _, _ := e.load(ctx)
		e.buildDue(ctx, d)
	}
	d, _, _ := e.load(ctx)
	if findContent(d, "th-20261010-1634") != nil || findContent(d, "th-20261010-1927") != nil || findContent(d, "th-20261010-0931") == nil {
		t.Fatal("today's old future posts must give way, the published one stays")
	}
	roots := map[string]bool{}
	for k := 0; k < threadsAhead; k++ {
		day := contentDay(now.AddDate(0, 0, k))
		items := thItems(d, day)
		var fut []*contentItem
		for _, it := range items {
			if it.Gen == "mg" || it.Gen == "val" {
				fut = append(fut, it)
			}
		}
		want := 4
		if k == 0 {
			want = 2 // 16:30 and 19:30 are left today
		}
		if len(fut) != want {
			t.Fatalf("%s: %d new posts", day, len(fut))
		}
		mg, val := 0, 0
		for _, it := range fut {
			if !strings.Contains(it.Text, "/m/") || !strings.HasPrefix(it.Link, contentBotLink+"mg_") || utf8.RuneCountInString(it.Text) > threadsMaxText {
				t.Fatalf("%s: link: %q", it.ID, it.Text)
			}
			switch it.Gen {
			case "mg":
				mg++
			case "val":
				val++
				r := thValueRoot(it.Src)
				if roots[r] {
					t.Fatalf("%s repeats %s", it.ID, r)
				}
				roots[r] = true
				body := it.Text[:strings.LastIndex(it.Text, "\n\n")]
				if p := threadsValueProblems(body); len(p) > 0 || !IsThreadsValue(it.Format) {
					t.Fatalf("%s: %v\n%s", it.ID, p, it.Text)
				}
			}
		}
		if k > 0 && (mg != 2 || val != 2) {
			t.Fatalf("%s: magnets %d, польза %d", day, mg, val)
		}
	}
	s, _ := e.state(ctx)
	for k := 0; k < threadsAhead; k++ {
		if ds := s.Threads[contentDay(now.AddDate(0, 0, k))]; ds == nil || ds.Plan != threadsPlanRev {
			t.Fatalf("day %d: %+v", k, ds)
		}
	}

	// the week for the SMM view
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(func(c *gin.Context) { c.Set("role", "admin"); c.Set("user_id", "u1"); c.Next() })
	m := &ContentModule{E: e}
	r.GET("/week", m.threadsWeek)
	r.POST("/replace/:id", m.threadsReplace)
	r.POST("/approve/:id", m.threadsApprove)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/week", nil))
	var wk struct {
		Days []struct {
			Day   string
			Posts []map[string]any
		}
	}
	if err := json.Unmarshal(w.Body.Bytes(), &wk); err != nil || len(wk.Days) != threadsAhead {
		t.Fatalf("week: %d %s", w.Code, w.Body.String())
	}
	tom := wk.Days[1].Posts
	if len(tom) != 4 || tom[0]["kind"] != "value" || tom[1]["kind"] != "magnet" || tom[0]["specifics"].(float64) < 2 || tom[1]["magnet"] == "" {
		t.Fatalf("tomorrow: %v", tom)
	}

	// «Заменить»: the same slot, another text of the same kind
	for _, p := range tom[:2] {
		id := p["id"].(string)
		w = httptest.NewRecorder()
		r.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/replace/"+id, nil))
		var res struct {
			OK   bool
			Item *contentItem
		}
		_ = json.Unmarshal(w.Body.Bytes(), &res)
		if !res.OK || res.Item.ID != id || res.Item.At == "" || res.Item.Text == p["text"] || (res.Item.Format == "magnet") != (p["kind"] == "magnet") ||
			!strings.Contains(res.Item.Text, "/m/"+res.Item.Magnet+"-"+strings.TrimPrefix(thPostParam(res.Item), "th_")) {
			t.Fatalf("replace %s: %s", id, w.Body.String())
		}
	}
	// «Одобрить»: stays approved through a re-plan of the day
	id := tom[2]["id"].(string)
	w = httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/approve/"+id, strings.NewReader(`{"on":true}`)))
	if !strings.Contains(w.Body.String(), `"approved"`) {
		t.Fatalf("approve: %s", w.Body.String())
	}
	if _, err := e.BuildThreadsDay(ctx, now.AddDate(0, 0, 1), true); err != nil {
		t.Fatal(err)
	}
	d, _, _ = e.load(ctx)
	if x := findContent(d, id); x == nil || x.Status != "approved" || x.Text != tom[2]["text"] {
		t.Fatalf("approved post after a re-plan: %+v", x)
	}
	if n := len(thItems(d, contentDay(now.AddDate(0, 0, 1)))); n != 4 {
		t.Fatalf("tomorrow after a re-plan: %d", n)
	}

	// the bot: the post of 16:30 goes to the owner, «Другой пост» keeps the policy
	now = time.Date(2026, 10, 10, 16, 40, 0, 0, almaty)
	e.manualDue(ctx)
	if len(tg.sent) != 1 || !strings.Contains(tg.sent[0].text, "/m/") {
		t.Fatalf("sent: %+v", tg.sent)
	}
	d, _, _ = e.load(ctx)
	var sent *contentItem
	for _, it := range thItems(d, "20261010") {
		if it.Status == "sent" {
			sent = it
		}
	}
	if sent == nil {
		t.Fatal("no sent post")
	}
	mem := e.threadsMemory(d, contentState{}, now)
	if !e.swapNext(&contentDoc{}, sent, now, mem) || sent.Gen != "val" || !IsThreadsValue(sent.Format) || !strings.Contains(sent.Text, "/m/") {
		t.Fatalf("Другой пост: %+v", sent.contentItemData)
	}
}
