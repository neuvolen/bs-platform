package http

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/bnursik/business_surgery_backend/internal/ai"
	"github.com/bnursik/business_surgery_backend/internal/bot"
	"github.com/bnursik/business_surgery_backend/internal/content"
	"github.com/gin-gonic/gin"
)

const testRichTool = `{"kind":"tool","organ":"Финансы","title":"x","subtitle":"Деньги на 13 недель вперёд за 2 часа сборки",
"promise":"Ты увидишь разрыв за месяц. Успеешь сдвинуть платёж — спокойно.",
"when":["Клиенты платят с отсрочкой","Зарплата 25-го, а деньги приходят 30-го","Планируешь закупку"],
"hero":{"name":"Динара","business":"упаковка для кофеен","city":"Алматы","story":["Абзац один.","Абзац два."],
 "before":[{"label":"Займы","value":"4 млн ₸"},{"label":"Видимость","value":"1 неделя"},{"label":"Просрочки","value":"2"}],
 "after":[{"label":"Займы","value":"0 ₸"},{"label":"Видимость","value":"13 недель"},{"label":"Просрочки","value":"0"}]},
"steps":[
 {"title":"Выпиши платежи","do":"Собери все обязательные платежи.","example":"Аренда 800 000 ₸","mistake":"Забыть налоги"},
 {"title":"Добавь поступления","do":"Внеси ожидаемые оплаты.","example":"Kaspi 1 200 000 ₸","mistake":"Считать надежды"},
 {"title":"Посчитай остаток","do":"Остаток на конец каждой недели.","example":"Неделя 5: минус 300 000 ₸","mistake":"Не обновлять"},
 {"title":"Обновляй по понедельникам","do":"15 минут каждую неделю.","example":"Понедельник 9:00","mistake":"Пропускать"}],
"metrics":["Недель видимости: 13","Точность прогноза: 90%"],
"template":{"title":"Прогноз денег на 13 недель","intro":"Заполни строки и обновляй раз в неделю.","blocks":[
 {"type":"table","title":"Недели","columns":["Неделя","Приход","Расход","Остаток"],"rows":[["1","1 200 000","900 000","300 000"]],"empty_rows":6},
 {"type":"checklist","title":"Проверка","items":["Все платежи внесены","Остаток посчитан"]}]},
"time":"2 часа на внедрение, 15 минут в неделю","level":"базовый","source":"Практика CFO"}`

const testRichDiag = `{"kind":"diag","organ":"Финансы","title":"x","subtitle":"Деньги кончаются к 20-му",
"desc":"Выручка есть, денег нет. Каждый месяц одно и то же.",
"signs":["Займы у родных","Задержки зарплаты","Нервы 20-го","Просрочки поставщикам","Нет остатка"],
"causes":["Нет прогноза","Отсрочки клиентам","Предоплата поставщикам"],
"cost":"Займ 4 млн ₸ под 5% в месяц стоит 200 000 ₸.",
"test":{"questions":[{"q":"1","yes":1},{"q":"2","yes":1},{"q":"3","yes":1},{"q":"4","yes":1},{"q":"5","yes":1},{"q":"6","yes":1}],"scale":"0-2 норма, 3-5 риск, 6+ диагноз подтверждён"},
"case":{"name":"Асет","business":"автомойка","story":"История.","result":"Стало лучше."},
"cure":["платежный календарь","Несуществующий инструмент"],
"first_steps":["Выписать платежи","Собрать поступления","Посчитать остаток"],
"risk":"Через год кассовый разрыв остановит закупки."}`

// The daily recommendation decides itself: a clear tool goes into the
// library in the rich format; a possible replacement is put to the owner in
// the bot, after the quiet hours, once a day, and his button does it.
func TestAutoRecs(t *testing.T) {
	repo, ctx := testPlatformDB(t, aiRecsKey, "bs_tools", "bs_diag", "bs_libver", recsRichKey)
	t.Cleanup(func() { _ = content.SetRichExtra(nil, nil) })
	tools := `[{"organ":"Финансы","icon":"◇","title":"Платёжный календарь","short":"x","why":"y","how":["a"]}]`
	diag := `[{"organ":"Финансы","icon":"◆","title":"Кассовые разрывы","desc":"d"},{"organ":"Продажи","icon":"◆","title":"Нет воронки","desc":"d"}]`
	for k, v := range map[string]string{"bs_tools": tools, "bs_diag": diag, "bs_libver": "3"} {
		if _, err := repo.PutDoc(ctx, "club", k, 0, v, false, "test"); err != nil {
			t.Fatal(err)
		}
	}
	recs := `{"items":[
 {"id":"rec_20261004_bbbbbb","date":"2026-10-04","status":"new","kind":"diag","organ":"Финансы","title":"Хронический кассовый разрыв","summary":"Деньги кончаются к 20-му.","why":"w","steps":["a","b","c"],"source":"Книга","url":"https://example.org/d","item":{"organ":"Финансы","icon":"◆","title":"Хронический кассовый разрыв","desc":"Деньги кончаются к 20-му."}},
 {"id":"rec_20261003_aaaaaa","date":"2026-10-03","status":"new","kind":"tool","organ":"Финансы","title":"Тринадцатинедельный прогноз денег","summary":"Прогноз на 13 недель.","why":"Видно разрыв заранее.","steps":["a","b","c"],"source":"Практика CFO","url":"https://example.org/13w","item":{"organ":"Финансы","icon":"◇","title":"Тринадцатинедельный прогноз денег","short":"Деньги на 13 недель","how":["a"]}}]}`
	if _, err := repo.PutDoc(ctx, "club", aiRecsKey, 0, recs, false, "test"); err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	var judged, enriched int
	gm := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var b bytes.Buffer
		_, _ = b.ReadFrom(r.Body)
		body := b.String()
		mu.Lock()
		defer mu.Unlock()
		switch {
		case strings.Contains(body, "главный редактор"):
			judged++
			if !strings.Contains(body, "Кассовые разрывы") || !strings.Contains(body, "Платёжный календарь") {
				w.WriteHeader(400) // the library must be in the prompt
				return
			}
			if strings.Contains(body, "Хронический кассовый разрыв") {
				geminiAnswer(w, `{"decision":"ask","confidence":0.8,"similar":"кассовые разрывы","replace":true,"conflict":"","question":"","reason":"То же, что существующий диагноз, но глубже"}`)
				return
			}
			geminiAnswer(w, `{"decision":"add","confidence":0.9,"similar":"","replace":false,"conflict":"","question":"","reason":"Новый метод"}`)
		case strings.Contains(body, "богатую карточку"):
			enriched++
			if strings.Contains(body, "Хронический кассовый разрыв") {
				if !strings.Contains(body, "Платёжный календарь") {
					w.WriteHeader(400) // the tools for cure must be listed
					return
				}
				geminiAnswer(w, testRichDiag)
				return
			}
			if enriched == 1 {
				// First answer breaks the rules: 3 steps. The server asks again.
				var o map[string]any
				_ = json.Unmarshal([]byte(testRichTool), &o)
				o["steps"] = o["steps"].([]any)[:3]
				b, _ := json.Marshal(o)
				geminiAnswer(w, string(b))
				return
			}
			geminiAnswer(w, testRichTool)
		default:
			w.WriteHeader(400)
		}
	}))
	defer gm.Close()
	h := NewPlatformAI(repo, &ai.Client{Gemini: "k", GeminiModel: "m", GeminiBase: gm.URL, HTTP: gm.Client()})
	h.Owner = 453800951
	type tgMsg struct {
		chat, msg int64
		text      string
		kb        map[string]any
	}
	var sent, edits []tgMsg
	h.RecsBot = RecsBot{
		Send: func(_ context.Context, chat int64, text string, kb map[string]any) (int64, error) {
			mu.Lock()
			defer mu.Unlock()
			sent = append(sent, tgMsg{chat: chat, text: text, kb: kb})
			return 77, nil
		},
		Edit: func(_ context.Context, chat, msg int64, text string, kb map[string]any) error {
			mu.Lock()
			defer mu.Unlock()
			edits = append(edits, tgMsg{chat: chat, msg: msg, text: text, kb: kb})
			return nil
		},
		Platform: "https://app.example/platform",
	}
	recByID := func(id string) map[string]any {
		d, _ := repo.GetDoc(ctx, "club", aiRecsKey)
		_, items := readRecs(d.Value)
		for _, it := range items {
			if recStr(it, "id") == id {
				return it.(map[string]any)
			}
		}
		return nil
	}

	// 1. A clear tool: added by itself, in the rich format.
	morning := time.Date(2026, 10, 3, 7, 5, 0, 0, almaty)
	out, err := h.autoRec(ctx, recByID("rec_20261003_aaaaaa"), morning)
	if err != nil || out != "added" {
		t.Fatalf("auto: %s %v", out, err)
	}
	if enriched != 2 {
		t.Fatalf("enrich calls %d (the broken card must be asked again)", enriched)
	}
	rec := recByID("rec_20261003_aaaaaa")
	if rec["status"] != "added" || rec["note"] != recNoteAuto || rec["auto"] != true || rec["addedTo"] != "bs_tools" {
		t.Fatalf("rec: %v", rec)
	}
	td, _ := repo.GetDoc(ctx, "club", "bs_tools")
	if !strings.Contains(td.Value, `"id":"ai_20261003_aaaaaa"`) || !strings.Contains(td.Value, "Тринадцатинедельный прогноз денег") {
		t.Fatalf("tools: %s", td.Value)
	}
	if len(sent) != 0 {
		t.Fatalf("a clear one must not bother the owner: %v", sent)
	}
	// The reader and the template see the new item.
	r := richRouter()
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/rich", nil))
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"id":"ai_20261003_aaaaaa"`) || strings.Contains(w.Body.String(), "—") {
		t.Fatalf("rich: %d", w.Code)
	}
	w = httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/tpl/ai_20261003_aaaaaa.pdf", nil))
	if w.Code != 200 || !bytes.HasPrefix(w.Body.Bytes(), []byte("%PDF")) {
		t.Fatalf("template: %d %s", w.Code, w.Body.String())
	}
	// After a restart the item comes back from the database.
	_ = content.SetRichExtra(nil, nil)
	if content.RichToolByID("ai_20261003_aaaaaa") != nil {
		t.Fatal("not reset")
	}
	if err := h.LoadRichExtra(ctx); err != nil || content.RichToolByID("ai_20261003_aaaaaa") == nil {
		t.Fatalf("reload: %v", err)
	}

	// 2. A possible replacement at 07:05: waits, the owner is asked after 10:00.
	out, err = h.autoRec(ctx, recByID("rec_20261004_bbbbbb"), morning.AddDate(0, 0, 1))
	if err != nil || out != "ask" {
		t.Fatalf("ask: %s %v", out, err)
	}
	rec = recByID("rec_20261004_bbbbbb")
	if rec["status"] != "ask" || rec["similar"] != "Кассовые разрывы" || !strings.Contains(recStr(rec, "note"), "Ждёт решения") {
		t.Fatalf("rec: %v", rec)
	}
	if len(sent) != 0 {
		t.Fatal("sent in the quiet hours")
	}
	h.maybeSendAsk(ctx, time.Date(2026, 10, 4, 9, 59, 0, 0, almaty))
	if len(sent) != 0 {
		t.Fatal("sent before 10:00")
	}
	h.maybeSendAsk(ctx, time.Date(2026, 10, 4, 10, 10, 0, 0, almaty))
	if len(sent) != 1 || sent[0].chat != 453800951 {
		t.Fatalf("sent %v", sent)
	}
	msg := sent[0]
	kb, _ := json.Marshal(msg.kb)
	for _, want := range []string{"airec_add_rec_20261004_bbbbbb", "airec_rep_rec_20261004_bbbbbb", "airec_rej_rec_20261004_bbbbbb",
		"Заменить «Кассовые разрывы»", "Подробнее на платформе", "section=aiRec\\u0026id=rec_20261004_bbbbbb"} {
		if !strings.Contains(string(kb), want) && !strings.Contains(string(kb), strings.ReplaceAll(want, "\\u0026", "&")) {
			t.Fatalf("%q not in %s", want, kb)
		}
	}
	if !strings.Contains(msg.text, "Хронический кассовый разрыв") || strings.Contains(msg.text, "—") {
		t.Fatalf("text: %s", msg.text)
	}
	// One message a day.
	h.maybeSendAsk(ctx, time.Date(2026, 10, 4, 15, 0, 0, 0, almaty))
	if len(sent) != 1 {
		t.Fatal("two messages in a day")
	}
	// The owner presses «Заменить».
	toast, ok := h.HandleRecCallback(ctx, bot.CallbackUpdate{ChatID: 453800951, MessageID: 77, FromID: 453800951, Data: "airec_rep_rec_20261004_bbbbbb"})
	if !ok || !strings.Contains(toast, "Заменено") {
		t.Fatalf("toast %q", toast)
	}
	dd, _ := repo.GetDoc(ctx, "club", "bs_diag")
	if strings.Contains(dd.Value, `"Кассовые разрывы"`) || !strings.Contains(dd.Value, "Хронический кассовый разрыв") || !strings.Contains(dd.Value, "Нет воронки") {
		t.Fatalf("diag: %s", dd.Value)
	}
	rec = recByID("rec_20261004_bbbbbb")
	if rec["status"] != "added" || rec["replaced"] != "Кассовые разрывы" || rec["by"] != "tg:453800951" {
		t.Fatalf("rec: %v", rec)
	}
	if len(edits) != 1 || edits[0].msg != 77 || edits[0].kb != nil || !strings.Contains(edits[0].text, "Заменено") {
		t.Fatalf("edits %v", edits)
	}
	// The rich diagnosis is in the library, its cure fixed to the real title.
	plain, _, _, _ := content.LibRich()
	if !strings.Contains(string(plain), `"id":"ai_20261004_bbbbbb"`) || !strings.Contains(string(plain), `"cure":["Платёжный календарь"]`) {
		t.Fatal("rich diag missing or cure not fixed")
	}
	// Pressed again: already decided.
	if toast, _ := h.HandleRecCallback(ctx, bot.CallbackUpdate{FromID: 1, Data: "airec_add_rec_20261004_bbbbbb"}); !strings.Contains(toast, "Уже решено") {
		t.Fatalf("again: %q", toast)
	}
	// The platform's buttons accept «replace» too.
	gin.SetMode(gin.TestMode)
	rr := gin.New()
	rr.Use(func(c *gin.Context) { c.Set("role", "admin") })
	rr.POST("/ai/recs/:id", h.RecAction)
	w = httptest.NewRecorder()
	rr.ServeHTTP(w, httptest.NewRequest("POST", "/ai/recs/rec_20261003_aaaaaa", strings.NewReader(`{"action":"replace"}`)))
	if w.Code != 400 {
		t.Fatalf("replace without similar: %d %s", w.Code, w.Body.String())
	}
}

func TestValidateRich(t *testing.T) {
	var o map[string]any
	_ = json.Unmarshal([]byte(testRichTool), &o)
	o["id"] = "ai_x"
	o = recCleanAny(o).(map[string]any)
	if p := ValidateRich(o, nil); len(p) != 0 {
		t.Fatal(p)
	}
	o["steps"] = o["steps"].([]any)[:2]
	o["template"] = map[string]any{"title": "t", "blocks": []any{map[string]any{"type": "chart"}}}
	o["promise"] = "Ключ к успеху — порядок"
	p := strings.Join(ValidateRich(o, nil), "; ")
	for _, want := range []string{"em dash", "штамп", "шагов меньше 4", "блоков шаблона 1", "тип \"chart\""} {
		if !strings.Contains(p, want) {
			t.Fatalf("%q not in %s", want, p)
		}
	}
	var d map[string]any
	_ = json.Unmarshal([]byte(testRichDiag), &d)
	d["id"] = "ai_y"
	test := d["test"].(map[string]any)
	test["questions"] = test["questions"].([]any)[:5]
	p = strings.Join(ValidateRich(d, []string{"Другой инструмент"}), "; ")
	if !strings.Contains(p, "вопросов теста меньше 6") || !strings.Contains(p, "лечение") {
		t.Fatal(p)
	}
}
