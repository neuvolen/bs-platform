package http

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/bnursik/business_surgery_backend/internal/content"
)

func TestThreadsSlotsSpread(t *testing.T) {
	day := time.Date(2026, 10, 5, 0, 0, 0, 0, almaty)
	for n := 1; n <= threadsPerDayMax; n++ {
		s := threadsSlots(day, n, 8*60, 22*60)
		if len(s) != n {
			t.Fatalf("n=%d: %d slots", n, len(s))
		}
		seg := float64(14*60) / float64(n)
		for i, x := range s {
			a := x.In(almaty)
			m := a.Hour()*60 + a.Minute()
			if m < 8*60 || m >= 22*60 || m%15 == 0 || a.Day() != 5 {
				t.Fatalf("n=%d slot %d at %s", n, i, a.Format("15:04"))
			}
			if i > 0 && x.Sub(s[i-1]) < 6*time.Minute {
				t.Fatalf("n=%d: slots %d and %d %s apart", n, i-1, i, x.Sub(s[i-1]))
			}
			// each slot in its own share of the day: the posts are spread, not bunched
			lo, hi := 8*60+float64(i)*seg, 8*60+float64(i+1)*seg
			if float64(m) < lo-1 || float64(m) > hi+8 {
				t.Fatalf("n=%d slot %d at %s outside %.0f-%.0f", n, i, a.Format("15:04"), lo, hi)
			}
		}
	}
	a, b := threadsSlots(day, 16, 480, 1320), threadsSlots(day, 16, 480, 1320)
	c := threadsSlots(day.AddDate(0, 0, 1), 16, 480, 1320)
	same := 0
	for i := range a {
		if !a[i].Equal(b[i]) {
			t.Fatal("the same day must give the same times")
		}
		if a[i].Format("15:04") == c[i].Format("15:04") {
			same++
		}
	}
	if same > 4 {
		t.Fatalf("the next day has the same times: %d of 16", same)
	}
	// 16 a day: one post per 52 minutes on average, the morning and the evening covered
	if f, l := a[0].In(almaty), a[15].In(almaty); f.Hour() != 8 || l.Hour() < 21 {
		t.Fatalf("window: %s-%s", f.Format("15:04"), l.Format("15:04"))
	}
}

func TestThreadsFormatsAndCTA(t *testing.T) {
	f := threadsDayFormats(16, "20261005")
	cnt := map[string]int{}
	for i, x := range f {
		cnt[x]++
		if i > 0 && f[i-1] == x {
			t.Fatalf("format %s twice in a row: %v", x, f)
		}
	}
	want := map[string]int{"tip": 2, "checklist": 2, "numbers": 2, "myth": 2, "question": 1, "case": 2, "symptom": 2, "series": 1, "opinion": 1, "backstage": 1}
	for k, v := range want {
		if cnt[k] != v {
			t.Fatalf("16 posts: %v", cnt)
		}
	}
	if g := threadsDayFormats(16, "20261006"); strings.Join(g, ",") == strings.Join(f, ",") {
		t.Fatal("the order of formats must vary by day")
	}
	for n := 2; n <= threadsPerDayMax; n++ {
		fs := threadsDayFormats(n, "20261005")
		cta := threadsCTASlots(n, fs)
		c := 0
		for i, x := range cta {
			if x {
				c++
				if fs[i] == "question" {
					t.Fatalf("n=%d: a question post with the CTA", n)
				}
			}
		}
		if cta[0] {
			t.Fatalf("n=%d: the first post sells", n)
		}
		if r := float64(c) / float64(n); c < 1 || r > 0.5 || (n >= 8 && (r < 0.2 || r > 0.3)) {
			t.Fatalf("n=%d: %d CTA posts", n, c)
		}
		if strings.Count(strings.Join(fs, ","), "series") > 1 {
			t.Fatalf("n=%d: more than one series", n)
		}
	}
	if c := threadsCTASlots(16, f); !c[3] || !c[7] && !c[6] || len(c) != 16 {
		t.Fatalf("16: %v", c)
	}
	for _, l := range threadsCTALines {
		if !strings.HasSuffix(l, "t.me/bsurgery_bot?start=th_99") || strings.ContainsAny(l, "—–") {
			t.Fatalf("CTA line %q", l)
		}
	}
	if got := startSource(content.StartParam(threadsCTALines[0])); got != "Threads: 99 чек-листов" {
		t.Fatalf("lead source %q", got)
	}
}

func TestThreadsQualityGate(t *testing.T) {
	ok := "Сколько денег будет на счёте через 5 недель?\n\nЕсли ответа нет, о кассовом разрыве ты узнаешь за 2-3 дня, когда вариантов уже нет. Выпиши все платежи с датами на 6 недель вперёд."
	if p := threadsQuality(ok, ok, nil); len(p) != 0 {
		t.Fatalf("a good post: %v", p)
	}
	bad := map[string]string{
		"длинное тире": "Платёжный календарь — это таблица, где видно остаток денег на каждый день следующих 8 недель. Собери за 2 часа.",
		"штамп":        "Важно отметить: ключ к успеху в том, чтобы видеть остаток денег на каждый день следующих 8 недель вперёд.",
		"статистика":   "По статистике 80% собственников не знают, сколько денег будет на счету через месяц. Не будь среди них, проверь.",
		"статистика без источника: 7 из 10": "7 из 10 компаний закрываются из-за кассовых разрывов, а не из-за убытков. Проверь свой платёжный календарь сегодня.",
		"ссылка":       "Собери платёжный календарь по шаблону, он лежит здесь: https://example.com/x и займёт у тебя 2 часа в первый раз.",
		"хэштег":       "Собери платёжный календарь на 8 недель вперёд и смотри на него каждый понедельник. #финансы #бизнес #деньги",
		"коротко":      "Считай деньги.",
		"длинно":       strings.Repeat("Выпиши платежи с датами. ", 20),
		"слабый зачин": "Привет! Сегодня про деньги. Выпиши все обязательные платежи с реальными датами за последние 3 месяца из выписки.",
		"эмодзи":       "Выпиши все платежи 🔥🔥🔥 с реальными датами за последние 3 месяца из выписки банка и посмотри на 25 число.",
		"не по-русски": "Cash flow calendar helps you see the balance every day for the next 8 weeks, and this is what we do at BS.",
	}
	for why, txt := range bad {
		if p := threadsQuality(txt, txt, nil); len(p) == 0 {
			t.Fatalf("%s must be refused: %q", why, txt)
		}
	}
	fine := "Сверь остаток по данным кассы и банка в пятницу.\n\nЕсли расхождение больше 50 000 ₸, ищи наличные, которые не дошли до сейфа. Обученный кассир найдёт их за 20 минут."
	if p := threadsQuality(fine, fine, nil); len(p) != 0 {
		t.Fatalf("own data is not statistics: %v", p)
	}
	if x := fine + " По данным исследования, так делает каждый второй."; len(threadsQuality(x, x, nil)) == 0 {
		t.Fatal("«по данным исследования» must be refused")
	}
	long := ok + "\n\n" + strings.Repeat("а", 400)
	if p := threadsQuality(ok, long, nil); len(p) == 0 {
		t.Fatal("over 500 with the CTA must be refused")
	}
	if p := threadsQuality(ok, ok, []string{"Коротко"}); len(p) == 0 {
		t.Fatal("a too short series part must be refused")
	}
	// safe fixes: ranges, thousands, spaces; a year stays
	got := threadsClean("Выручка 14000000 ₸, чек 4500 ₸, срок 3–6 недель, в 2026 году,  остаток   1,5 млн ₸\n\n\n\nДалее")
	if got != "Выручка 14 000 000 ₸, чек 4 500 ₸, срок 3-6 недель, в 2026 году, остаток 1,5 млн ₸\n\nДалее" {
		t.Fatalf("clean: %q", got)
	}
}

// thFakeAI answers the batch prompt with a post per slot made from its
// material; bad[n] replaces post n with a text the gate refuses.
type thFakeAI struct {
	mu      sync.Mutex
	calls   int
	prompts []string
	fail    error
	bad     map[int]string
	fixed   string // a ready answer
}

var thSlotRe = regexp.MustCompile(`(?m)^### Пост (\d+) · ([^·\n]+) ·`)

func (f *thFakeAI) call(_ context.Context, system, prompt string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	f.prompts = append(f.prompts, prompt)
	if f.fail != nil {
		return "", f.fail
	}
	if f.fixed != "" {
		return f.fixed, nil
	}
	if !strings.Contains(system, "Threads") || !strings.Contains(system, "₸") {
		return "", errors.New("no system prompt")
	}
	day := regexp.MustCompile(`День: (\d+ \S+)`).FindStringSubmatch(prompt)[1]
	blocks := thSlotRe.FindAllStringSubmatchIndex(prompt, -1)
	var posts []map[string]any
	for i, b := range blocks {
		end := len(prompt)
		if i+1 < len(blocks) {
			end = blocks[i+1][0]
		}
		block := prompt[b[0]:end]
		n := prompt[b[2]:b[3]]
		mat := ""
		if m := regexp.MustCompile(`Материал: ([^\n]+)`).FindStringSubmatch(block); m != nil {
			mat = m[1]
		}
		if m := regexp.MustCompile(`Подробности: ([^\n]+)`).FindStringSubmatch(block); m != nil {
			mat += " " + m[1]
		}
		text := fmt.Sprintf("%s, пост %s\n\n%s", day, n, cutRunes(strings.ReplaceAll(mat, "…", ""), 300))
		p := map[string]any{"n": i + 1, "text": text}
		if strings.Contains(block, "· Серия") {
			p["parts"] = []string{"Первый шаг серии. " + cutRunes(mat, 150), "Второй шаг серии. Проверь результат через неделю и запиши цифры в таблицу."}
		}
		if x, ok := f.bad[i+1]; ok {
			p["text"] = x
		}
		posts = append(posts, p)
	}
	b, _ := json.Marshal(map[string]any{"posts": posts})
	return "Вот посты:\n```json\n" + string(b) + "\n```", nil
}

// thReachPct: the classic Threads tests run without the reach rubrics (R58
// has its own tests in content_threads_reach_test.go).
var thReachPct any = 0

// thDoc: bs_content with Threads 16 a day (or n) and the classic channels off.
func thDoc(t *testing.T, docs *cntDocs, perDay int) {
	t.Helper()
	b, _ := json.Marshal(map[string]any{"settings": map[string]any{
		"channels": map[string]any{"threads": map[string]any{"on": true, "time": "10:00", "perDay": perDay, "reachPct": thReachPct, "magnetPct": thMagnetPct},
			"telegram": map[string]any{"on": false}, "instagram": map[string]any{"on": false}},
		"days": []int{1, 2, 3, 4, 5, 6, 7}, "approval": "auto", "previewHour": 9, "rev": 2}, "queue": []any{}})
	cur := 0
	if d := docs.docs["club/"+contentKey]; d != nil {
		cur = d.Version
	}
	if _, err := docs.PutDoc(context.Background(), "club", contentKey, cur, string(b), false, "t"); err != nil {
		t.Fatal(err)
	}
}

func thItems(d *contentDoc, day string) []*contentItem {
	var out []*contentItem
	for _, it := range dayItems(d, day) {
		if it.Channel == "threads" {
			out = append(out, it)
		}
	}
	return out
}

func TestThreadsBatchBuild(t *testing.T) {
	ctx := context.Background()
	docs := &cntDocs{}
	thDoc(t, docs, 16)
	now := time.Date(2026, 10, 5, 6, 30, 0, 0, almaty)
	e := cntEngine(docs, &now)
	e.Lib = thLib
	fai := &thFakeAI{bad: map[int]string{
		2: "Платёжный календарь — это таблица, где видно остаток денег на каждый день. Собери его за 2 часа в понедельник.",
		5: "По статистике 80% собственников не знают свой остаток денег через месяц. Проверь себя и выпиши платежи.",
	}}
	e.AI = fai.call

	res, err := e.BuildThreadsDay(ctx, now, false)
	if err != nil {
		t.Fatal(err)
	}
	d := docs.doc(t)
	items := thItems(d, "20261005")
	if res.PerDay != 16 || res.Added != 16 || len(items) != 16 || res.Missing != 0 || fai.calls != 1 {
		t.Fatalf("build: %+v, %d items, %d AI calls", res, len(items), fai.calls)
	}
	if res.AI != 14 || res.Lib != 2 {
		t.Fatalf("the two refused posts are replaced from the library: %+v", res)
	}
	p := fai.prompts[0]
	for _, w := range []string{"Пост 1 · ", "Пост 16 · ", "Материал: ", "Орган: ", "99 чек-листов", "Мини-чек-лист", "Серия"} {
		if !strings.Contains(p, w) {
			t.Fatalf("prompt without %q:\n%s", w, p)
		}
	}
	ids := map[string]bool{}
	cta, series, ai := 0, 0, 0
	organs := map[string]int{}
	var prev time.Time
	for i, it := range items {
		at, _ := parseContentAt(it.At)
		if ids[it.ID] || it.Status != "planned" || !it.Auto || it.Kind != "threads" || it.Gen == "" || !at.After(prev) {
			t.Fatalf("item %d: %+v", i, it.contentItemData)
		}
		ids[it.ID] = true
		prev = at
		if m := at.In(almaty).Minute(); m%15 == 0 {
			t.Fatalf("on the hour: %s", it.At)
		}
		if strings.ContainsAny(it.Text, "—") || strings.Contains(it.Text, "статистике") || utf8.RuneCountInString(it.Text) > 500 {
			t.Fatalf("a refused text went in: %q", it.Text)
		}
		if it.Gen == "ai" {
			ai++
			if it.Format == "" || it.Format == "library" || it.Src == "" || it.Organ == "" {
				t.Fatalf("AI post: %+v", it.contentItemData)
			}
			organs[it.Organ]++
		}
		if it.CTA {
			cta++
			if it.Gen == "ai" && it.Link != contentBotLink+threadsCTAParam {
				t.Fatalf("CTA link %q", it.Link)
			}
			whole := it.Text + strings.Join(it.Parts, "")
			if !strings.Contains(whole, "t.me/bsurgery_bot?start=th_") {
				t.Fatalf("CTA post without the link: %q", whole)
			}
		} else if it.Link != "" || strings.Contains(it.Text, "t.me/") {
			t.Fatalf("a value post with a link: %+v", it.contentItemData)
		}
		if it.Format == "series" {
			series++
			if len(it.Parts) < 1 {
				t.Fatalf("series without parts: %+v", it.contentItemData)
			}
		}
	}
	lines := map[string]bool{}
	for _, it := range items {
		if it.CTA {
			ls := strings.Split(strings.TrimSpace(it.Text+"\n"+strings.Join(it.Parts, "\n")), "\n")
			lines[ls[len(ls)-1]] = true
		}
	}
	if len(lines) != 4 {
		t.Fatalf("the 4 CTA lines of a day must differ: %v", lines)
	}
	if cta != 4 || series != 1 || ai != 14 {
		t.Fatalf("cta %d series %d ai %d", cta, series, ai)
	}
	if len(organs) < 5 {
		t.Fatalf("organs must rotate: %v", organs)
	}
	for i := 1; i < len(items); i++ {
		if items[i].Gen == "ai" && items[i-1].Gen == "ai" && items[i].Organ == items[i-1].Organ {
			t.Fatalf("organ %s twice in a row (%d)", items[i].Organ, i)
		}
	}

	// Building again changes nothing; the day's state says so.
	ver := docs.docs["club/"+contentKey].Version
	if r2, _ := e.BuildThreadsDay(ctx, now, false); r2.Added != 0 || docs.docs["club/"+contentKey].Version != ver || fai.calls != 1 {
		t.Fatalf("second build: %+v", r2)
	}
	st, _ := e.state(ctx)
	if ds := st.Threads["20261005"]; ds == nil || ds.N != 16 || ds.AI != "ok" || len(st.Recent) != 16 {
		t.Fatalf("state: %+v %d", ds, len(st.Recent))
	}

	// The next day: no seed of yesterday again, no repeated opening.
	used := map[string]bool{}
	for _, it := range items {
		used[it.Src] = true
	}
	now = time.Date(2026, 10, 6, 6, 31, 0, 0, almaty)
	if _, err := e.BuildThreadsDay(ctx, now, false); err != nil {
		t.Fatal(err)
	}
	for _, it := range thItems(docs.doc(t), "20261006") {
		if it.Gen == "ai" && used[it.Src] {
			t.Fatalf("seed %s again within 30 days", it.Src)
		}
	}

	// A text like one of the last 30 days is refused (the AI repeats yesterday).
	old := items[0]
	fai.fixed = `{"posts":[{"n":1,"text":` + thJSON(old.Text+" Ещё раз.") + `}]}`
	now = time.Date(2026, 10, 7, 20, 0, 0, 0, almaty) // the evening slots are left
	r3, err := e.BuildThreadsDay(ctx, now, false)
	if err != nil {
		t.Fatal(err)
	}
	for _, it := range thItems(docs.doc(t), "20261007") {
		if it.Gen == "ai" {
			t.Fatalf("a repeated text went in: %q", it.Text)
		}
	}
	if r3.Slots < 1 || r3.Slots > 4 || r3.AI != 0 {
		t.Fatalf("late build: %+v", r3)
	}

	// The AI fails: library posts, no message to the owner, a retry later.
	fai.fixed, fai.fail = "", errors.New("Gemini 503: overloaded")
	var told []string
	e.Send = func(_ context.Context, _ int64, text string, _ map[string]any) (int64, error) {
		told = append(told, text)
		return 1, nil
	}
	now = time.Date(2026, 10, 8, 6, 30, 0, 0, almaty)
	r4, err := e.BuildThreadsDay(ctx, now, false)
	if err != nil || r4.AIError == "" || r4.AI != 0 || r4.Lib == 0 || r4.Lib+r4.Missing != 16 || len(told) != 0 {
		t.Fatalf("AI down: %+v %v told %v", r4, err, told)
	}
	st, _ = e.state(ctx)
	if ds := st.Threads["20261008"]; ds.Tries != 1 || ds.AI == "ok" {
		t.Fatalf("state after the AI failed: %+v", ds)
	}
	for _, it := range thItems(docs.doc(t), "20261008") {
		if it.Gen != "lib" || it.Format != "library" {
			t.Fatalf("fallback: %+v", it.contentItemData)
		}
	}

	// force rebuilds the untouched future posts, keeps the edited one.
	fai.fail = nil
	d = docs.doc(t)
	var m map[string]any
	_ = json.Unmarshal([]byte(docs.docs["club/"+contentKey].Value), &m)
	var editedID string
	for _, x := range m["queue"].([]any) {
		it := x.(map[string]any)
		if strings.HasPrefix(it["id"].(string), "th-20261008-1") && editedID == "" {
			it["text"], it["edited"] = "Свой текст команды про деньги и остаток на счёте, проверено руками.", true
			editedID = it["id"].(string)
		}
	}
	b, _ := json.Marshal(m)
	_, _ = docs.PutDoc(ctx, "club", contentKey, docs.docs["club/"+contentKey].Version, string(b), false, "tg:1")
	r5, err := e.BuildThreadsDay(ctx, now, true)
	if err != nil || r5.AI == 0 {
		t.Fatalf("force: %+v %v", r5, err)
	}
	day8 := thItems(docs.doc(t), "20261008")
	if len(day8) != 16-r5.Missing || findContent(docs.doc(t), editedID) == nil || findContent(docs.doc(t), editedID).Text[:10] != "Свой текст"[:10] {
		t.Fatalf("force kept %d, edited %v", len(day8), findContent(docs.doc(t), editedID))
	}
}

func thNoPreview(l []string) []string {
	var out []string
	for _, x := range l {
		if !strings.HasPrefix(x, "📅") {
			out = append(out, x)
		}
	}
	return out
}

// thLib: the test library with Threads texts long enough to stand alone
// and different from each other.
func thLib() []*content.LibItem {
	vocab := strings.Fields("выручка прибыль касса склад поставщик клиент менеджер скидка аренда зарплата налоги кредит отсрочка закуп остаток маржа чек воронка звонок заявка " +
		"договор отчёт таблица неделя месяц квартал сезон доставка сервис витрина реклама бюджет охват отзыв возврат долг резерв лимит график смена бригада мастер " +
		"филиал точка магазин кофейня салон автомойка производство цех упаковка логистика маршрут водитель бухгалтер юрист партнёр инвестор стратегия регламент")
	out := cntLib()
	for i, li := range out {
		var th []string
		for v, x := range li.Threads {
			var w []string
			for k := 0; k < 22; k++ {
				w = append(w, vocab[hashN(fmt.Sprintf("%d/%d/%d", i, v, k), len(vocab))])
			}
			th = append(th, fmt.Sprintf("Готовый пост %d-%d про %s.\n\n%s.\n\n%s", i, v, li.Src, strings.Join(w, " "), x))
		}
		li.Threads = th
	}
	return out
}

func thJSON(s string) string { b, _ := json.Marshal(s); return string(b) }

func TestThreadsPlanKeepsBatch(t *testing.T) {
	ctx := context.Background()
	docs := &cntDocs{}
	thDoc(t, docs, 16)
	now := time.Date(2026, 10, 5, 6, 30, 0, 0, almaty)
	e := cntEngine(docs, &now)
	e.Lib = thLib
	e.AI = (&thFakeAI{}).call
	if _, err := e.BuildThreadsDay(ctx, now, false); err != nil {
		t.Fatal(err)
	}
	// The hourly plan and «Перепланировать» keep the batch and add no classic Threads post.
	if _, _, err := e.Plan(ctx, false); err != nil {
		t.Fatal(err)
	}
	if _, _, err := e.Plan(ctx, true); err != nil {
		t.Fatal(err)
	}
	d := docs.doc(t)
	if n := len(cntChannel(d, "threads")); n != 16 {
		t.Fatalf("after plan: %d threads posts", n)
	}
	// The cadence goes down to 10: six untouched posts go, spread.
	var m map[string]any
	_ = json.Unmarshal([]byte(docs.docs["club/"+contentKey].Value), &m)
	m["settings"].(map[string]any)["channels"].(map[string]any)["threads"].(map[string]any)["perDay"] = 10
	b, _ := json.Marshal(m)
	_, _ = docs.PutDoc(ctx, "club", contentKey, docs.docs["club/"+contentKey].Version, string(b), false, "tg:1")
	if _, _, err := e.Plan(ctx, false); err != nil {
		t.Fatal(err)
	}
	left := cntChannel(docs.doc(t), "threads")
	if len(left) != 10 {
		t.Fatalf("perDay 10: %d left", len(left))
	}
	for i := 1; i < len(left); i++ {
		a, _ := parseContentAt(left[i-1].At)
		b, _ := parseContentAt(left[i].At)
		if b.Sub(a) > 3*time.Hour {
			t.Fatalf("the rest is not spread: %s → %s", left[i-1].At, left[i].At)
		}
	}
	// One a day again: the batch's future posts go, the classic plan comes back at 10:00.
	_ = json.Unmarshal([]byte(docs.docs["club/"+contentKey].Value), &m)
	m["settings"].(map[string]any)["channels"].(map[string]any)["threads"].(map[string]any)["perDay"] = 1
	b, _ = json.Marshal(m)
	_, _ = docs.PutDoc(ctx, "club", contentKey, docs.docs["club/"+contentKey].Version, string(b), false, "tg:1")
	if _, _, err := e.Plan(ctx, false); err != nil {
		t.Fatal(err)
	}
	th := cntChannel(docs.doc(t), "threads")
	if len(th) != 14 || atClock(th[0].At) != "10:00" || th[0].Gen != "" {
		t.Fatalf("classic again: %d, %+v", len(th), th[0].contentItemData)
	}
	if _, err := e.BuildThreadsDay(ctx, now, false); err != errThreadsNoBatch {
		t.Fatalf("no batch at 1 a day: %v", err)
	}
	// Settings: the cadence is kept within 1-25
	if s := parseContentSettings(json.RawMessage(`{"channels":{"threads":{"on":true,"perDay":99}}}`)); s.threadsPerDay() != 25 {
		t.Fatalf("max: %d", s.threadsPerDay())
	}
	if s := parseContentSettings(json.RawMessage(`{"channels":{"threads":{"on":true}}}`)); s.threadsPerDay() != contentThreadsPerDay {
		t.Fatalf("default: %d", s.threadsPerDay())
	}
}

// thPub: a fake Threads that records posts and fails on demand.
type thPub struct {
	mu      sync.Mutex
	posts   []string
	replies [][2]string
	err     error
}

func (p *thPub) publish(_ context.Context, _, text string) (string, string, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.err != nil {
		return "", "", p.err
	}
	p.posts = append(p.posts, text)
	id := fmt.Sprintf("p%d", len(p.posts))
	return id, "https://www.threads.net/@bsurgery/post/" + id, nil
}

func (p *thPub) reply(_ context.Context, to, text string) (string, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.replies = append(p.replies, [2]string{to, text})
	return fmt.Sprintf("r%d", len(p.replies)), nil
}

func TestThreadsPublishing(t *testing.T) {
	ctx := context.Background()
	docs := &cntDocs{}
	thDoc(t, docs, 16)
	now := time.Date(2026, 10, 5, 6, 29, 0, 0, almaty)
	e := cntEngine(docs, &now)
	e.Lib = thLib
	e.AI = (&thFakeAI{}).call
	e.Go = func(f func()) { f() }
	pub := &thPub{}
	e.Threads, e.ThreadsReply = pub.publish, pub.reply
	var told []string
	e.Send = func(_ context.Context, _ int64, text string, _ map[string]any) (int64, error) {
		told = append(told, text)
		return 1, nil
	}

	// 06:29 nothing, 06:30 the batch is made by the tick, once.
	e.Tick(ctx)
	if len(thItems(docs.doc(t), "20261005")) != 0 {
		t.Fatal("built before 06:30")
	}
	now = now.Add(time.Minute)
	e.Tick(ctx)
	e.Tick(ctx)
	items := thItems(docs.doc(t), "20261005")
	if len(items) != 16 {
		t.Fatalf("the tick built %d", len(items))
	}

	// Two posts already due (the server was busy): one per tick, 2 minutes apart.
	a0, _ := parseContentAt(items[0].At)
	a1, _ := parseContentAt(items[1].At)
	now = a1.Add(time.Minute)
	e.Tick(ctx)
	if len(pub.posts) != 1 || pub.posts[0] != items[0].Text {
		t.Fatalf("first tick: %d posts", len(pub.posts))
	}
	now = now.Add(time.Minute)
	e.Tick(ctx)
	if len(pub.posts) != 1 {
		t.Fatal("two posts within 2 minutes")
	}
	now = now.Add(time.Minute)
	e.Tick(ctx)
	if len(pub.posts) != 2 || pub.posts[1] != items[1].Text {
		t.Fatalf("second post: %d", len(pub.posts))
	}
	_ = a0

	// The whole day goes out minute by minute: 16 posts, the series as a reply chain.
	var series *contentItem
	for _, it := range items {
		if it.Format == "series" {
			series = it
		}
	}
	end := time.Date(2026, 10, 5, 22, 30, 0, 0, almaty)
	for now.Before(end) {
		now = now.Add(time.Minute)
		e.Tick(ctx)
	}
	d := docs.doc(t)
	day := thItems(d, "20261005")
	pubd := 0
	for i, it := range day {
		if it.Status == "published" {
			pubd++
			pa, _ := parseContentAt(it.PublishedAt)
			at, _ := parseContentAt(it.At)
			if i > 1 && pa.Sub(at) > 3*time.Minute || pa.Before(at) { // the first two caught up late
				t.Fatalf("published at %s for %s", it.PublishedAt, it.At)
			}
		}
	}
	if pubd != 16 || len(pub.posts) != 16 {
		t.Fatalf("published %d / %d", pubd, len(pub.posts))
	}
	if series != nil {
		if len(pub.replies) != len(series.Parts) || pub.replies[0][1] != series.Parts[0] {
			t.Fatalf("series replies: %v", pub.replies)
		}
		if len(pub.replies) > 1 && pub.replies[1][0] != "r1" {
			t.Fatalf("a series is a chain: %v", pub.replies)
		}
	}
	var alerts []string
	for _, x := range told {
		if !strings.HasPrefix(x, "📅") { // the morning preview
			alerts = append(alerts, x)
		}
	}
	if len(alerts) != 0 {
		t.Fatalf("no messages on a good day: %v", alerts)
	}
	told = nil

	// The token dies: the post waits, the owner is told once, not 16 times.
	pub.err = &threadsAPIError{Status: 400, Code: 190, Type: "OAuthException", Msg: "Error validating access token: Session has expired"}
	now = time.Date(2026, 10, 6, 6, 30, 0, 0, almaty)
	e.Tick(ctx)
	end = time.Date(2026, 10, 6, 12, 0, 0, 0, almaty)
	for now.Before(end) {
		now = now.Add(time.Minute)
		e.Tick(ctx)
	}
	told = thNoPreview(told)
	tok := 0
	for _, m := range told {
		if strings.Contains(m, "THREADS_TOKEN") {
			tok++
		}
	}
	if tok != 1 || len(told) != 1 {
		t.Fatalf("token alerts: %d, all %v", tok, told)
	}
	d = docs.doc(t)
	st := map[string]int{}
	for _, it := range thItems(d, "20261006") {
		st[it.Status]++
		if it.Status == "failed" {
			t.Fatalf("a token error is not a failed post: %+v", it.contentItemData)
		}
	}
	if st["published"] != 0 || st["skipped"] == 0 || st["planned"] == 0 {
		t.Fatalf("statuses while the token is dead: %v", st)
	}
	// The token works again: posts go on, the owner hears it once.
	pub.err = nil
	end = time.Date(2026, 10, 6, 14, 0, 0, 0, almaty)
	for now.Before(end) {
		now = now.Add(time.Minute)
		e.Tick(ctx)
	}
	told = thNoPreview(told)
	if len(told) != 2 || !strings.Contains(told[1], "снова публикует") {
		t.Fatalf("recovery: %v", told)
	}
	ok := 0
	for _, it := range thItems(docs.doc(t), "20261006") {
		if it.Status == "published" {
			ok++
		}
	}
	if ok == 0 {
		t.Fatal("nothing published after the token came back")
	}

	// A network error: tried 3 times, then failed with one message.
	pub.err = errors.New("dial tcp: connection refused")
	before := len(told)
	end = time.Date(2026, 10, 6, 15, 30, 0, 0, almaty)
	var tried *contentItem
	for now.Before(end) {
		now = now.Add(time.Minute)
		e.Tick(ctx)
	}
	told = thNoPreview(told)
	for _, it := range thItems(docs.doc(t), "20261006") {
		if it.Tries > 0 && tried == nil {
			tried = it
		}
	}
	if tried == nil || (tried.Status != "failed" && tried.Status != "skipped") || len(told) != before+1 || !strings.Contains(told[len(told)-1], "3 попыток") {
		t.Fatalf("network: %+v told %v", tried, told[before:])
	}
	pub.err = nil
}

func TestThreadsDailyLimit(t *testing.T) {
	ctx := context.Background()
	docs := &cntDocs{}
	thDoc(t, docs, 16)
	now := time.Date(2026, 10, 5, 12, 0, 0, 0, almaty)
	e := cntEngine(docs, &now)
	var told []string
	e.Send = func(_ context.Context, _ int64, text string, _ map[string]any) (int64, error) {
		told = append(told, text)
		return 1, nil
	}
	pub := &thPub{}
	e.Threads = pub.publish
	// 240 posts in the last 24 hours (made by hand in the queue) and one due
	var m map[string]any
	_ = json.Unmarshal([]byte(docs.docs["club/"+contentKey].Value), &m)
	var q []any
	for i := 0; i < threadsQuotaSafe; i++ {
		at := now.Add(-time.Duration(i+1) * 5 * time.Minute).Format(time.RFC3339)
		q = append(q, map[string]any{"id": fmt.Sprintf("old%d", i), "channel": "threads", "kind": "threads", "text": "x", "at": at, "publishedAt": at, "status": "published", "gen": "ai"})
	}
	q = append(q, map[string]any{"id": "due1", "channel": "threads", "kind": "threads", "text": "Пост, который ждёт лимита", "at": now.Add(-time.Minute).Format(time.RFC3339), "status": "planned", "gen": "ai", "auto": true})
	m["queue"] = q
	b, _ := json.Marshal(m)
	_, _ = docs.PutDoc(ctx, "club", contentKey, docs.docs["club/"+contentKey].Version, string(b), false, "t")
	e.Tick(ctx)
	e.Tick(ctx)
	var lim []string
	for _, x := range told {
		if strings.Contains(x, "лимит API 250") {
			lim = append(lim, x)
		}
	}
	if len(pub.posts) != 0 || len(lim) != 1 {
		t.Fatalf("limit: %d posts, told %v", len(pub.posts), told)
	}
	// An hour later the oldest posts left the 24 hours: it goes.
	now = now.Add(20*time.Hour + 5*time.Minute) // the posts before 08:05 yesterday left the 24 hours
	_ = json.Unmarshal([]byte(docs.docs["club/"+contentKey].Value), &m)
	for _, x := range m["queue"].([]any) {
		it := x.(map[string]any)
		if it["id"] == "due1" {
			it["at"] = now.Add(-time.Minute).Format(time.RFC3339)
		}
	}
	b, _ = json.Marshal(m)
	_, _ = docs.PutDoc(ctx, "club", contentKey, docs.docs["club/"+contentKey].Version, string(b), false, "t")
	e.Tick(ctx)
	if len(pub.posts) != 1 {
		t.Fatalf("after the window moved: %d", len(pub.posts))
	}
	// A rate error from Threads itself: a pause of an hour, no failed post.
	pub.err = &threadsAPIError{Status: 400, Code: 4, Msg: "Application request limit reached"}
	if k := threadsErrKind(pub.err); k != "rate" {
		t.Fatalf("kind %s", k)
	}
	for _, c := range []struct {
		err  error
		kind string
	}{{errors.New("Нет THREADS_TOKEN в переменных Railway"), "auth"}, {&threadsAPIError{Status: 500, Msg: "x"}, "temp"},
		{&threadsAPIError{Status: 400, Code: 100, Msg: "Invalid parameter"}, "other"}, {context.DeadlineExceeded, "temp"}} {
		if k := threadsErrKind(c.err); k != c.kind {
			t.Fatalf("%v: %s, want %s", c.err, k, c.kind)
		}
	}
}

// TestThreadsSample writes a sample day (THREADS_SAMPLE=<dir>): the real
// prompt and the posts of a ready AI answer (THREADS_SAMPLE_AI=<json file>)
// through the real pipeline.
func TestThreadsSample(t *testing.T) {
	dir := os.Getenv("THREADS_SAMPLE")
	if dir == "" {
		t.Skip("THREADS_SAMPLE not set")
	}
	ctx := context.Background()
	docs := &cntDocs{}
	thDoc(t, docs, 16)
	now := time.Date(2026, 10, 5, 6, 30, 0, 0, almaty)
	e := cntEngine(docs, &now)
	e.Lib = content.Library
	fai := &thFakeAI{}
	if f := os.Getenv("THREADS_SAMPLE_AI"); f != "" {
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		fai.fixed = string(b)
	} else {
		fai.fail = errors.New("no answer yet")
	}
	e.AI = fai.call
	res, err := e.BuildThreadsDay(ctx, now, false)
	if err != nil {
		t.Fatal(err)
	}
	_ = os.WriteFile(dir+"/threads_prompt.txt", []byte(threadsSystem+"\n\n=====\n\n"+res.Prompt), 0o644)
	var b strings.Builder
	fmt.Fprintf(&b, "build: %+v\n\n", *res)
	for i, it := range thItems(docs.doc(t), "20261005") {
		fmt.Fprintf(&b, "## %d · %s · %s · %s · %s · %d зн.%s\n\n%s\n", i+1, atClock(it.At), ThreadsFormatName(it.Format), it.Organ, it.Gen, utf8.RuneCountInString(it.Text), map[bool]string{true: " · CTA"}[it.CTA], it.Text)
		for j, p := range it.Parts {
			fmt.Fprintf(&b, "\n↳ %d/%d\n%s\n", j+2, len(it.Parts)+1, p)
		}
		b.WriteString("\n")
	}
	_ = os.WriteFile(dir+"/threads_sample_raw.md", []byte(b.String()), 0o644)
}
