package http

import (
	"context"
	"encoding/json"
	"log"
	"strings"
	"time"
	"unicode"

	"github.com/bnursik/business_surgery_backend/internal/content"
)

// R32b: «Маркетинг → Стратегия → Инсайты и план». Действия с высоким
// приоритетом разобраны: что можно сделать в нашей системе (платформа, бот,
// контент-завод, страница лида), сделано и отмечено «Внедрено» с пояснением;
// что требует собственника (видео, таргет), стало задачей на доске «Цели и
// задачи» с шагами и отмечено «В работе».
//
// Миграция один раз дописывает это в документ клуба bs_mkt_analysis: действие
// находится по id или по началу текста, статус ставится, только если команда
// его ещё не трогала (нет status и не отмечено выполненным). Тексты, которые
// команда правила, не меняются; удалённые действия не возвращаются.

const mktActionsKey = "mkt_actions_r32b" // scope server: done marker

type mktTask struct {
	T     string
	Steps []string
}

type mktActionPlan struct {
	Status string // done | work
	Note   string
	Task   *mktTask
}

// mktActionPlans: id действия из marketing.json → что с ним сделано.
var mktActionPlans = map[string]mktActionPlan{
	"a1": {Status: "done", Note: "Контент-завод сам публикует в Threads каждый день (до 16 постов): органы бизнеса, кейсы, возражения, формат клуба. У каждого поста ссылка в @bsurgery_bot с меткой источника, лиды по ней видны в CRM и в «Идеи и заметки → Аналитика». Настройки: Маркетинг → Контент-завод."},
	"a2": {Status: "done", Note: "Бот ведёт лида 5 касаниями (1, 3, 7, 10 и 14 день), в каждом короткий кейс резидента с цифрами. Чек-листы в приложении разложены по органам бизнеса (финансы, продажи, команда, операционка и другие), прогрев учитывает, какой чек-лист лид открыл и прошёл."},
	"a3": {Status: "done", Note: "Готово на странице лида (app.bxclub.kz, вход через Telegram): после экспресс-диагностики карта органов с пометкой «Болит» и кнопка «Записаться на разбор · 50 000 ₸»."},
	"a4": {Status: "work", Note: "Нужны цифры, согласие и видео резидентов: задача на доске «Цели и задачи».", Task: &mktTask{
		T: "Упаковать 6 кейсов до/после: карусели, Reels, видео-отзывы",
		Steps: []string{
			"Собрать цифры до/после и согласие на публикацию: Исфандияр, Казбек К9, Артём, Даулет, Елена, Дмитрий",
			"Записать 6 видео-отзывов по 1-2 минуты: что было, что сделали, что стало (в цифрах)",
			"Отдать цифры в Контент-завод → Карусели: 6 каруселей «до/после»",
			"Нарезать Reels из видео-отзывов",
			"Поставить кейсы в план публикаций и на страницу экспресс-разбора",
		}}},
	"a5": {Status: "work", Note: "Делается в рекламном кабинете: задача на доске «Цели и задачи».", Task: &mktTask{
		T: "Пересобрать таргет: только на экспресс-разбор, кейс в креативе",
		Steps: []string{
			"Остановить кампании, которые ведут на клуб",
			"Новая кампания на страницу экспресс-разбора (50 000 ₸) с меткой источника в ссылке",
			"Креатив: кейс до/после с цифрами (из задачи про 6 кейсов), 2-3 варианта",
			"Тест 7 дней с небольшим бюджетом",
			"Через неделю сравнить цену заявки и оплаченного разбора: Идеи и заметки → Аналитика",
		}}},
	"a6":  {Status: "done", Note: "Скрипт «После разбора: диагноз, план на 10 дней, тариф за 24 часа» добавлен в Продажи → Скрипты: что сказать в конце разбора, что отправить в тот же день и как предложить тариф в течение 24 часов."},
	"a11": {Status: "done", Note: "Путь лида Threads → бот → чек-лист → диагностика → разбор → клуб считается сам за 30 дней и неделя к неделе: Идеи и заметки → Аналитика (карточки «Путь лида» и «Лиды по источникам»). Отчёт живёт на платформе, без рассылок в бот."},
}

// Скрипт для действия a6 (Продажи → Скрипты, документ bs_scripts).
var razborFollowScript = map[string]any{
	"t":   "После разбора: диагноз, план на 10 дней, тариф за 24 часа",
	"s":   "Цель: решение о резидентстве в течение 24 часов",
	"msg": "Спасибо за разбор. Как обещали: ваш главный диагноз, план первых 10 дней и что даст клуб именно вам. Если удобно, завтра в 12:00 созвонимся на 15 минут и ответим на вопросы.",
	"steps": []any{
		"В конце разбора: назвать главный диагноз его словами и цену проблемы в тенге за месяц",
		"Там же: 3 шага на первые 10 дней, первый можно сделать сегодня",
		"В тот же день: отправить диагноз и план сообщением, приложить кейс из его ниши",
		"Предложить тариф: что изменится за 3 месяца в клубе, цена, оплата Kaspi",
		"Через 24 часа: короткий звонок, решение «да» или «нет», без «подумаю»",
	},
}

// mktKey: начало текста без знаков, чтобы найти действие, которое команда не переименовала.
func mktKey(s string) string {
	var b strings.Builder
	n := 0
	for _, r := range strings.ToLower(s) {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			b.WriteRune(r)
			n++
			if n >= 40 {
				break
			}
		}
	}
	return b.String()
}

// mktDefaultIDs: начало текста → id, по marketing.json.
func mktDefaultIDs() map[string]string {
	var m struct {
		Actions []map[string]any `json:"actions"`
	}
	_ = json.Unmarshal(content.Marketing, &m)
	out := map[string]string{}
	for _, a := range m.Actions {
		id, _ := a["id"].(string)
		t, _ := a["text"].(string)
		if id != "" && t != "" {
			out[mktKey(t)] = id
		}
	}
	return out
}

// mergeMktActions ставит статусы в документе. tasks: id действия → id карточки,
// которая есть на доске. Возвращает, сколько действий изменено.
func mergeMktActions(doc map[string]any, tasks map[string]string, now time.Time) int {
	acts, _ := doc["actions"].([]any)
	ids := mktDefaultIDs()
	n := 0
	for _, x := range acts {
		a, _ := x.(map[string]any)
		if a == nil {
			continue
		}
		id, _ := a["id"].(string)
		if id == "" {
			t, _ := a["text"].(string)
			if id = ids[mktKey(t)]; id == "" {
				continue
			}
			a["id"] = id
			n++
		}
		p, ok := mktActionPlans[id]
		if !ok {
			continue
		}
		if st, _ := a["status"].(string); st != "" || a["done"] == true {
			continue // команда уже решила сама
		}
		if p.Status == "work" {
			cid := tasks[id]
			if cid == "" {
				continue // доски ещё нет: задачу создаст кнопка «Внедрить»
			}
			a["task"] = cid
		}
		a["status"] = p.Status
		a["note"] = p.Note
		a["statusAt"] = now.UTC().Format(time.RFC3339)
		if p.Status == "done" {
			a["done"] = true
			a["doneAt"] = now.UTC().Format(time.RFC3339)
		}
		n++
	}
	return n
}

// mktTaskCard: карточка доски «Цели и задачи» для действия.
func mktTaskCard(id string, p mktActionPlan, effect string, now time.Time) map[string]any {
	chk := []any{}
	for _, s := range p.Task.Steps {
		chk = append(chk, map[string]any{"t": s, "ok": false})
	}
	desc := "Из плана маркетинга (Маркетинг → Стратегия → Инсайты и план)."
	if effect != "" {
		desc += "\nЧто даст: " + effect
	}
	at := now.UTC().Format(time.RFC3339)
	return map[string]any{
		"id": "mkt-" + id, "col": "work", "t": p.Task.T, "who": "Рустам", "pri": "high", "labels": []any{"mkt"},
		"chk": chk, "desc": desc, "src": "mkt:" + id, "created": at,
		"log": []any{map[string]any{"at": at, "by": "Платформа", "k": "a", "t": "Задача создана из плана маркетинга"}},
	}
}

// MigrateMktActions runs once (marker in the server doc), after SeedMarketing.
func (h *PlatformAI) MigrateMktActions(ctx context.Context) {
	if h.repo == nil {
		return
	}
	if d, err := h.repo.GetDoc(ctx, "server", mktActionsKey); err == nil && d != nil && !d.Deleted {
		return
	}
	now := time.Now()
	// 1. Доска: задачи собственнику (если доски нет, их создаст «Внедрить» на платформе)
	tasks := map[string]string{}
	effects := map[string]string{}
	if d, err := h.repo.GetDoc(ctx, "club", "bs_mkt_analysis"); err == nil && d != nil && !d.Deleted {
		var doc map[string]any
		if json.Unmarshal([]byte(d.Value), &doc) == nil {
			ids := mktDefaultIDs()
			for _, x := range iList(doc["actions"]) {
				a := iMap(x)
				id := iStr(a["id"])
				if id == "" {
					id = ids[mktKey(iStr(a["text"]))]
				}
				if id != "" && iStr(a["status"]) == "" && a["done"] != true {
					effects[id] = iStr(a["effect"])
				}
			}
		}
	}
	for try := 0; try < 4; try++ {
		d, err := h.repo.GetDoc(ctx, "club", "bs_kanban")
		if err != nil || d == nil || d.Deleted {
			break
		}
		kb := map[string]any{}
		if json.Unmarshal([]byte(d.Value), &kb) != nil {
			break
		}
		have := map[string]bool{}
		for _, part := range []string{"cards", "archive"} {
			for _, x := range iList(kb[part]) {
				have[iStr(iMap(x)["id"])] = true
			}
		}
		cards := iList(kb["cards"])
		added := 0
		for _, id := range []string{"a4", "a5"} {
			eff, want := effects[id]
			if !want && !have["mkt-"+id] {
				continue
			}
			tasks[id] = "mkt-" + id
			if have["mkt-"+id] {
				continue
			}
			cards = append([]any{mktTaskCard(id, mktActionPlans[id], eff, now)}, cards...)
			added++
		}
		if added == 0 {
			break
		}
		kb["cards"] = cards
		val, _ := json.Marshal(kb)
		if _, err := h.repo.PutDoc(ctx, "club", "bs_kanban", d.Version, string(val), false, "server:mkt"); err == nil {
			break
		}
		tasks = map[string]string{}
	}
	// 2. Скрипт после разбора
	for try := 0; try < 4; try++ {
		d, err := h.repo.GetDoc(ctx, "club", "bs_scripts")
		if err != nil || d == nil || d.Deleted {
			break // документа нет: платформа покажет скрипт из своих стандартных
		}
		var list []any
		if json.Unmarshal([]byte(d.Value), &list) != nil {
			break
		}
		dup := false
		for _, x := range list {
			if mktKey(iStr(iMap(x)["t"])) == mktKey(razborFollowScript["t"].(string)) {
				dup = true
			}
		}
		if dup {
			break
		}
		list = append(list, razborFollowScript)
		val, _ := json.Marshal(list)
		if _, err := h.repo.PutDoc(ctx, "club", "bs_scripts", d.Version, string(val), false, "server:mkt"); err == nil {
			break
		}
	}
	// 3. Статусы действий
	n, ok := 0, false
	for try := 0; try < 4; try++ {
		d, err := h.repo.GetDoc(ctx, "club", "bs_mkt_analysis")
		if err != nil || d == nil || d.Deleted {
			return // документа ещё нет: попробуем при следующем запуске
		}
		doc := map[string]any{}
		if json.Unmarshal([]byte(d.Value), &doc) != nil {
			return
		}
		if n = mergeMktActions(doc, tasks, now); n == 0 {
			ok = true
			break
		}
		val, _ := json.Marshal(doc)
		if _, err := h.repo.PutDoc(ctx, "club", "bs_mkt_analysis", d.Version, string(val), false, "server:mkt"); err == nil {
			ok = true
			break
		}
	}
	if !ok {
		return
	}
	if _, err := h.repo.PutDoc(ctx, "server", mktActionsKey, 0, `{"v":1}`, false, "server:mkt"); err == nil {
		log.Printf("marketing: %d plan actions updated", n)
	}
}
