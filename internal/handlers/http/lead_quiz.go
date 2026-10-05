package http

import (
	"context"
	"fmt"
	"log"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/bnursik/business_surgery_backend/internal/bot"
	"github.com/bnursik/business_surgery_backend/internal/content"
)

// R40b: проверка бизнеса прямо в чате бота.
//
// Лид выбирает, что болит (lead_pain.go), и сразу проходит короткую
// проверку этого органа: выручка в месяц (для оценки в деньгах), потом
// 6 вопросов да/нет. Одно сообщение меняется на месте, ничего открывать не
// нужно. В конце: сколько из 6 в порядке, оценка потерь в ₸ в месяц, диагноз
// и один конкретный шаг на неделю. Под результатом запись на экспресс-разбор
// в ближайшее окно одной кнопкой, полный гайд и все 99 в приложении.
//
// Состояние живёт в callback_data (lm_q_<орган>_<выручка><ответы>), поэтому
// перезапуск сервера посреди проверки ничего не ломает. В CRM пишется начало,
// результат (qz.<орган>: st, fin, score, loss, rev) и строки истории; по ним
// платформа считает воронку Threads → бот → начал → закончил → записался.

const (
	quizPrefix     = "lm_q_"
	quizBookPrefix = "lm_bk_"
)

type quizQ struct {
	Text   string
	BadYes bool    // «да» значит проблема (иначе проблема «нет»)
	Weight float64 // доля месячной выручки, которую обычно съедает эта дыра, %
	Action string
}

type quiz struct {
	Key, Organ, Title, Guide string
	Qs                       []quizQ
}

var quizRevenue = []struct {
	Label string
	Mid   int64
}{
	{"до 3 млн ₸", 2_000_000}, {"3-10 млн ₸", 6_000_000}, {"10-30 млн ₸", 18_000_000}, {"больше 30 млн ₸", 40_000_000},
}

var quizzes = []quiz{
	{"fin", "Финансы", "Деньги бизнеса", "g001", []quizQ{
		{"Знаете чистую прибыль прошлого месяца с точностью до 10%?", false, 3, "Посчитайте прибыль прошлого месяца: выручка минус все расходы, включая вашу зарплату. Это 30 минут с выпиской."},
		{"Деньги бизнеса и личные лежат на разных счетах?", false, 2, "Откройте отдельный счёт для бизнеса и платите себе фиксированную зарплату раз в месяц."},
		{"Есть платёжный календарь хотя бы на 2 недели вперёд?", false, 2, "Выпишите все платежи и поступления на 14 дней вперёд в одну таблицу и обновляйте её по понедельникам."},
		{"За последние полгода занимали деньги, чтобы закрыть месяц?", true, 3, "Найдите, где застревают деньги: долги клиентов, склад или предоплаты поставщикам. Начните с самой большой суммы."},
		{"Знаете маржу каждого продукта или услуги?", false, 3, "Посчитайте маржу трёх главных продуктов. Обычно один из них работает почти в ноль."},
		{"Поднимали цены за последние 12 месяцев?", false, 2, "Поднимите цену на 5-10% для новых клиентов на самый ходовой продукт и 2 недели смотрите на конверсию."},
	}},
	{"sales", "Продажи", "Продажи и заявки", "g028", []quizQ{
		{"На новую заявку отвечаете в течение 15 минут?", false, 4, "Назначьте одного человека на первые ответы и поставьте норму: 15 минут в рабочее время."},
		{"Все заявки из директа, WhatsApp и звонков попадают в одну CRM или таблицу?", false, 3, "Заведите одну таблицу заявок: дата, канал, имя, сумма, статус. Неделю записывайте туда всё."},
		{"Знаете конверсию из заявки в оплату?", false, 2, "Посчитайте за прошлый месяц: сколько было заявок и сколько оплат. Это ваша точка отсчёта."},
		{"Тем, кто сказал «подумаю», пишете повторно хотя бы 3 раза?", false, 3, "Соберите всех, кто сказал «подумаю» за 30 дней, и напишите каждому с конкретным поводом."},
		{"Сделки закрываются без вашего личного участия?", false, 2, "Запишите, как продаёте вы: 5 вопросов клиенту и 3 ответа на возражения. Отдайте менеджеру."},
		{"Отслеживаете повторные покупки клиентов?", false, 2, "Выгрузите клиентов за год и напишите тем, кто купил один раз, с личным предложением."},
	}},
	{"team", "Команда", "Команда", "g046", []quizQ{
		{"Можете уехать на 2 недели, и бизнес проработает без звонков вам?", false, 3, "Выпишите задачи, которые делаете только вы, и отдайте одну на этой неделе."},
		{"У каждого ключевого сотрудника есть 1-3 цифры его результата?", false, 3, "Назначьте каждому ключевому сотруднику одну цифру результата и смотрите её раз в неделю."},
		{"Сотрудники приходят к вам с решениями, а не с вопросами?", false, 2, "Введите правило: с проблемой приходят с двумя вариантами решения."},
		{"За год ушло больше трети команды?", true, 3, "Поговорите с тремя лучшими сотрудниками: что их держит и что может заставить уйти."},
		{"Новичок выходит на результат меньше чем за месяц?", false, 2, "Опишите первые 2 недели новичка по дням: что делает, у кого учится, какой результат."},
		{"Планёрки заканчиваются решениями с ответственным и сроком?", false, 1, "В конце каждой планёрки записывайте: что, кто и к какому дню."},
	}},
	{"ops", "Процессы", "Операционка", "g058", []quizQ{
		{"Ключевые процессы описаны письменно?", false, 2, "Опишите самый частый процесс на одной странице: шаги, кто делает, сроки."},
		{"Одни и те же ошибки повторяются каждый месяц?", true, 3, "Соберите 3 повторяющиеся ошибки месяца и на каждую поставьте одно правило проверки."},
		{"Больше половины вашего дня уходит на операционку?", true, 3, "Неделю записывайте, на что уходит время. Всё, что стоит дешевле вашего часа, отдайте."},
		{"Качество не зависит от того, кто из сотрудников на смене?", false, 2, "Сделайте чек-лист приёмки главной услуги и 2 недели проверяйте работу по нему."},
		{"Задачи команды ведутся в одном месте, а не в чатах?", false, 1, "Перенесите задачи на одну доску: «надо», «в работе», «готово»."},
		{"Рутину (отчёты, напоминания, счета) люди делают руками?", true, 2, "Выберите одну ручную рутину и автоматизируйте её: шаблон, бот или CRM."},
	}},
	{"mkt", "Маркетинг", "Маркетинг", "g031", []quizQ{
		{"Знаете, откуда пришли последние 10 клиентов?", false, 3, "Спросите следующих 10 клиентов, откуда они о вас узнали, и запишите ответы."},
		{"Клиенты приходят не только по сарафану?", false, 3, "Запустите один управляемый канал на месяц: Threads, 2GIS или таргет, с бюджетом и целью в заявках."},
		{"Знаете, сколько стоит привлечь одного клиента?", false, 2, "Разделите расходы на рекламу за месяц на число новых клиентов. Это цена клиента."},
		{"Можете одной фразой сказать, чем вы лучше конкурентов?", false, 2, "Спросите 5 постоянных клиентов, почему они выбрали вас. Их слова и есть ваше позиционирование."},
		{"Отзывы и кейсы с цифрами собираете регулярно?", false, 1, "На этой неделе попросите 3 довольных клиентов об отзыве с цифрой результата."},
		{"Бросали рекламу, не посчитав, окупилась ли она?", true, 2, "Верните лучший канал на 2 недели и считайте заявки и оплаты по нему отдельно."},
	}},
	{"strat", "Стратегия", "Стратегия", "g080", []quizQ{
		{"Есть цель на год с цифрой и датой?", false, 2, "Запишите одну цифру на конец года, выручку или прибыль, и три шага к ней."},
		{"Можете назвать 3 приоритета этого квартала?", false, 2, "Выберите 3 приоритета квартала и уберите из плана всё, что им не помогает."},
		{"Ведёте больше двух направлений одновременно?", true, 3, "Посчитайте прибыль каждого направления. Самое слабое поставьте на паузу на 90 дней."},
		{"Выручка за последний год выросла?", false, 3, "Найдите, что дало рост в лучший месяц года, и повторите это осознанно."},
		{"Смотрите на ключевые цифры бизнеса каждую неделю?", false, 2, "Выберите 5 цифр бизнеса и смотрите их каждый понедельник по 15 минут."},
		{"Принимаете решения один, без людей, которые скажут, что вы неправы?", true, 1, "Покажите план на квартал сильному предпринимателю и попросите найти слабое место."},
	}},
}

// quizOrder: what is offered next when one is done.
var quizOrder = []string{"fin", "sales", "team", "ops", "mkt", "strat"}

func quizByKey(k string) *quiz {
	for i := range quizzes {
		if quizzes[i].Key == k {
			return &quizzes[i]
		}
	}
	return nil
}

func quizByOrgan(organ string) *quiz {
	for i := range quizzes {
		if quizzes[i].Organ == organ {
			return &quizzes[i]
		}
	}
	return nil
}

// quizState parses "lm_q_<key>[_<rev><answers>]".
func quizState(data string) (q *quiz, rev int, ans string, ok bool) {
	rest := strings.TrimPrefix(data, quizPrefix)
	key, st, _ := strings.Cut(rest, "_")
	if q = quizByKey(key); q == nil {
		return nil, 0, "", false
	}
	if st == "" {
		return q, -1, "", true
	}
	r, err := strconv.Atoi(st[:1])
	if err != nil || r < 0 || r >= len(quizRevenue) {
		return nil, 0, "", false
	}
	ans = st[1:]
	if len(ans) > len(q.Qs) || strings.Trim(ans, "yn") != "" {
		return nil, 0, "", false
	}
	return q, r, ans, true
}

func roundTenge(v float64) int64 {
	n := int64(v)
	if n >= 100_000 {
		return (n + 50_000) / 100_000 * 100_000
	}
	return (n + 5_000) / 10_000 * 10_000
}

type quizResult struct {
	Score  int
	Loss   int64
	Action string
	Diag   string
}

func quizScore(q *quiz, rev int, ans string) quizResult {
	var r quizResult
	worst := -1.0
	pct := 0.0
	for i, a := range ans {
		if i >= len(q.Qs) {
			break
		}
		qq := q.Qs[i]
		bad := (a == 'y') == qq.BadYes
		if !bad {
			r.Score++
			continue
		}
		pct += qq.Weight
		if qq.Weight > worst {
			worst, r.Action = qq.Weight, qq.Action
		}
	}
	if rev >= 0 && rev < len(quizRevenue) {
		r.Loss = roundTenge(float64(quizRevenue[rev].Mid) * pct / 100)
	}
	switch {
	case r.Score == len(q.Qs):
		r.Diag = "Орган здоров. Держите ритм и проверьте соседний."
	case r.Score >= 4:
		r.Diag = "В целом крепко, но есть утечка, которую легко закрыть."
	case r.Score >= 2:
		r.Diag = "Здесь бизнес теряет деньги каждый месяц. Обычно это лечится за 2-4 недели."
	default:
		r.Diag = "Острая зона. Скорее всего, именно она сейчас держит рост."
	}
	return r
}

func quizYN(q *quiz, rev int, ans string) map[string]any {
	base := quizPrefix + q.Key + "_" + strconv.Itoa(rev) + ans
	return kb(row(map[string]any{"text": "Да", "callback_data": base + "y"}, map[string]any{"text": "Нет", "callback_data": base + "n"}))
}

func quizRevKB(q *quiz) map[string]any {
	var rows [][]map[string]any
	for i := 0; i < len(quizRevenue); i += 2 {
		r := []map[string]any{}
		for j := i; j < i+2 && j < len(quizRevenue); j++ {
			r = append(r, map[string]any{"text": quizRevenue[j].Label, "callback_data": quizPrefix + q.Key + "_" + strconv.Itoa(j)})
		}
		rows = append(rows, r)
	}
	return map[string]any{"inline_keyboard": rows}
}

// show replaces the pressed message, or sends a new one.
func (f *LeadFunnel) show(ctx context.Context, cb bot.CallbackUpdate, text string, keys map[string]any) error {
	if f.Edit != nil && cb.MessageID != 0 {
		if err := f.Edit(ctx, cb.ChatID, cb.MessageID, text, keys); err == nil {
			return nil
		}
	}
	return f.send(ctx, cb.ChatID, text, keys)
}

// quizMark writes the quiz's start or result into the lead's card.
func (f *LeadFunnel) quizMark(ctx context.Context, tg int64, q *quiz, fn func(st map[string]any, lead map[string]any, now time.Time) bool) {
	now := f.now()
	_ = f.mutate(ctx, "bs_crm", func(crm map[string]any) bool {
		lead := findLeadByTg(asList(crm["leads"]), tg)
		if lead == nil {
			return false
		}
		qz, _ := lead["qz"].(map[string]any)
		if qz == nil {
			qz = map[string]any{}
		}
		st, _ := qz[q.Key].(map[string]any)
		if st == nil {
			st = map[string]any{"organ": q.Organ}
		}
		if !fn(st, lead, now) {
			return false
		}
		qz[q.Key] = st
		lead["qz"] = qz
		ts := now.UTC().Format(time.RFC3339)
		lead["botReplyAt"], lead["handledAt"] = ts, ts
		return true
	})
}

// quizPainStart: the pain button: CRM note and the first step at once.
func (f *LeadFunnel) quizPainStart(ctx context.Context, cb bot.CallbackUpdate, organ string, q *quiz) bool {
	_, _, _ = f.ensureLead(ctx, cb.ChatID, cb.FirstName, "", cb.Username, "Telegram: бот", "Выбрал боль в боте: "+organ, "", false)
	_ = f.mutate(ctx, "bs_crm", func(crm map[string]any) bool {
		lead := findLeadByTg(asList(crm["leads"]), cb.ChatID)
		if lead == nil {
			return false
		}
		lead["pain"] = organ
		addLog(lead, f.now(), "Болит: "+organ)
		return true
	})
	return f.quizStep(ctx, cb, q, -1, "")
}

func (f *LeadFunnel) quizCallback(ctx context.Context, cb bot.CallbackUpdate) bool {
	if strings.HasPrefix(cb.Data, quizBookPrefix) {
		return f.quizBook(ctx, cb, strings.TrimPrefix(cb.Data, quizBookPrefix))
	}
	if cb.Data == quizPrefix+"menu" {
		return f.quizMenu(ctx, cb)
	}
	q, rev, ans, ok := quizState(cb.Data)
	if !ok {
		return f.quizMenu(ctx, cb)
	}
	_, _, _ = f.ensureLead(ctx, cb.ChatID, cb.FirstName, "", cb.Username, "Telegram: бот", "Начал проверку в боте", "", false)
	return f.quizStep(ctx, cb, q, rev, ans)
}

// quizMenu: pick one of the six.
func (f *LeadFunnel) quizMenu(ctx context.Context, cb bot.CallbackUpdate) bool {
	var rows [][]map[string]any
	for i := 0; i < len(quizOrder); i += 2 {
		r := []map[string]any{}
		for _, k := range quizOrder[i:minI(i+2, len(quizOrder))] {
			q := quizByKey(k)
			r = append(r, map[string]any{"text": q.Title, "callback_data": quizPrefix + k})
		}
		rows = append(rows, r)
	}
	return f.show(ctx, cb, "Что проверим? 6 вопросов да/нет, 2 минуты.", map[string]any{"inline_keyboard": rows}) == nil
}

func (f *LeadFunnel) quizStep(ctx context.Context, cb bot.CallbackUpdate, q *quiz, rev int, ans string) bool {
	n := len(q.Qs)
	if rev < 0 { // the start: the revenue first
		f.quizMark(ctx, cb.ChatID, q, func(st, lead map[string]any, now time.Time) bool {
			if st["st"] != nil && st["fin"] == nil {
				return false // already started
			}
			if st["st"] == nil {
				st["st"] = now.UTC().Format(time.RFC3339)
			} else {
				st["again"] = now.UTC().Format(time.RFC3339)
			}
			addLog(lead, now, "Начал проверку «"+q.Title+"» в чате")
			return true
		})
		text := "Проверка: " + q.Title + "\n\nСначала один вопрос, чтобы посчитать в деньгах. Какая выручка у бизнеса в месяц?"
		return f.show(ctx, cb, text, quizRevKB(q)) == nil
	}
	if len(ans) < n {
		text := fmt.Sprintf("%s · вопрос %d из %d\n\n%s", q.Title, len(ans)+1, n, q.Qs[len(ans)].Text)
		return f.show(ctx, cb, text, quizYN(q, rev, ans)) == nil
	}
	res := quizScore(q, rev, ans)
	f.quizMark(ctx, cb.ChatID, q, func(st, lead map[string]any, now time.Time) bool {
		if st["fin"] != nil && fmt.Sprint(st["ans"]) == ans {
			return false // the same result pressed twice
		}
		if st["st"] == nil {
			st["st"] = now.UTC().Format(time.RFC3339)
		}
		st["fin"] = now.UTC().Format(time.RFC3339)
		st["score"], st["of"], st["loss"], st["rev"], st["ans"] = res.Score, n, res.Loss, quizRevenue[rev].Label, ans
		addLog(lead, now, fmt.Sprintf("Прошёл проверку «%s»: %d из %d в порядке, потери около %s в месяц", q.Title, res.Score, n, tenge(res.Loss)))
		if res.Score <= 3 {
			lead["hot"] = true
		}
		return true
	})
	text, keys := f.quizResultMsg(ctx, q, res)
	return f.show(ctx, cb, text, keys) == nil
}

// nearestFree: the nearest free разбор slot (2 hours ahead at least).
func (f *LeadFunnel) nearestFree(ctx context.Context) (slot, int64, bool) {
	doc, err := f.readSlots(ctx)
	if err != nil {
		return slot{}, 0, false
	}
	price, _ := slotsPrice(doc)
	now := f.now()
	var free []slot
	for _, v := range asList(doc["slots"]) {
		if s, ok := readSlot(v); ok && s.free() && s.Start.After(now.Add(2*time.Hour)) && s.Start.Before(now.Add(slotsAhead)) {
			free = append(free, s)
		}
	}
	if len(free) == 0 {
		return slot{}, price, false
	}
	sort.Slice(free, func(i, j int) bool { return free[i].Start.Before(free[j].Start) })
	return free[0], price, true
}

func (f *LeadFunnel) quizResultMsg(ctx context.Context, q *quiz, r quizResult) (string, map[string]any) {
	n := len(q.Qs)
	var b strings.Builder
	fmt.Fprintf(&b, "%s: %d из %d в порядке\n\n", q.Title, r.Score, n)
	if r.Loss > 0 {
		b.WriteString("Оценка: такие пробелы обычно стоят около " + tenge(r.Loss) + " в месяц.\n\n")
	}
	b.WriteString(r.Diag)
	if r.Action != "" {
		b.WriteString("\n\nОдин шаг на эту неделю: " + r.Action)
	}
	var rows [][]map[string]any
	if s, price, ok := f.nearestFree(ctx); ok && len(quizBookPrefix+s.ID) <= 64 {
		b.WriteString("\n\nЭкспресс-разбор: час с основателями BS, ваши цифры и план на 10 дней, " + tenge(price) + ". Ближайшее окно: " + whenRu(s.Start) + ".")
		rows = append(rows, row(map[string]any{"text": "📅 Записаться: " + whenShort(s.Start), "callback_data": quizBookPrefix + s.ID}))
		rows = append(rows, row(f.appBtn("🗓 Другое время", "razbor")))
	} else {
		b.WriteString("\n\nЭкспресс-разбор: час с основателями BS, ваши цифры и план на 10 дней.")
		rows = append(rows, row(f.appBtn("📅 Записаться на экспресс-разбор", "razbor")))
	}
	if t := content.GuideTitle(q.Guide); t != "" {
		rows = append(rows, row(f.appBtn("📘 Полный чек-лист: "+t, "guide_"+q.Guide)))
	}
	rows = append(rows, row(map[string]any{"text": "🔁 Проверить другой орган", "callback_data": quizPrefix + "menu"}))
	return b.String(), map[string]any{"inline_keyboard": rows}
}

func whenShort(t time.Time) string {
	t = t.In(almaty)
	return fmt.Sprintf("%s %d %s, %s", ruDays[t.Weekday()], t.Day(), ruMonths[t.Month()-1], t.Format("15:04"))
}

// quizBook: one tap books the offered slot (BookSlot sends the confirmation).
func (f *LeadFunnel) quizBook(ctx context.Context, cb bot.CallbackUpdate, slotID string) bool {
	u := &platformTgUser{ID: cb.FromID, FirstName: cb.FirstName, LastName: cb.LastName, Username: cb.Username}
	if u.ID == 0 {
		u.ID = cb.ChatID
	}
	code, _, _, _, err := f.BookSlot(ctx, u, bookReq{SlotID: slotID}, "бот, после проверки")
	if err != nil {
		log.Printf("funnel: book from chat %d: %v", cb.ChatID, err)
		return f.send(ctx, cb.ChatID, "Не получилось записать, попробуйте выбрать время в приложении.", kb(row(f.appBtn("📅 Выбрать время", "razbor")))) == nil
	}
	switch code {
	case "":
		_ = f.mutate(ctx, "bs_crm", func(crm map[string]any) bool {
			lead := findLeadByTg(asList(crm["leads"]), u.ID)
			if lead == nil {
				return false
			}
			lead["qzBooked"] = f.now().UTC().Format(time.RFC3339)
			return true
		})
		return true
	case "already":
		return f.send(ctx, cb.ChatID, "Вы уже записаны на разбор. Перенести или отменить можно в приложении.", kb(row(f.appBtn("📅 Моя запись", "razbor")))) == nil
	default:
		return f.send(ctx, cb.ChatID, "Это окно уже заняли. Выберите другое время, свободные окна в приложении.", kb(row(f.appBtn("📅 Свободные окна", "razbor")))) == nil
	}
}

// quizNudge: the warm-up's day 1 and day 3: one checklist right in the chat
// (another one than the lead already did). ok false: the usual text.
func (f *LeadFunnel) quizNudge(ctx context.Context, stage int, first string, qz map[string]any, pain string) (string, map[string]any, bool) {
	if stage > 1 {
		return "", nil, false
	}
	hi := ""
	if first != "" {
		hi = first + ", "
	}
	done := map[string]bool{}
	var open *quiz
	var lastDone *quiz
	lastScore := 0
	for k, v := range qz {
		m, _ := v.(map[string]any)
		q := quizByKey(k)
		if m == nil || q == nil {
			continue
		}
		if m["fin"] != nil {
			done[k] = true
			lastDone, lastScore = q, int(anyInt(m["score"]))
		} else if m["st"] != nil {
			open = q
		}
	}
	pick := func() *quiz {
		if stage == 0 {
			if q := quizByOrgan(pain); q != nil && !done[q.Key] && (open == nil || open.Key != q.Key) {
				return q
			}
		}
		skip := 0
		for _, k := range quizOrder {
			if done[k] || (open != nil && open.Key == k) {
				continue
			}
			if stage == 1 && skip == 0 && len(done) == 0 && open == nil {
				skip++ // day 3 offers another one than day 1 did
				continue
			}
			return quizByKey(k)
		}
		return nil
	}
	razbor := row(f.appBtn("📅 Экспресс-разбор", "razbor"))
	if stage == 0 && open != nil && !done[open.Key] {
		text := hi + "проверка «" + open.Title + "» осталась незаконченной. Это 2 минуты, в конце будет сумма потерь в месяц и один конкретный шаг."
		return text, kb(row(map[string]any{"text": "▶️ Пройти проверку", "callback_data": quizPrefix + open.Key}), razbor), true
	}
	q := pick()
	if q == nil {
		return "", nil, false
	}
	var text string
	switch {
	case lastDone != nil:
		text = fmt.Sprintf("%sпо «%s» у вас %d из %d в порядке. Проверим ещё один орган? %s: 6 вопросов да/нет, 2 минуты.", hi, lastDone.Title, lastScore, len(lastDone.Qs), q.Title)
	case stage == 0:
		text = hi + "давайте за 2 минуты проверим " + strings.ToLower(q.Title) + ": 6 вопросов да/нет. В конце сумма, которую бизнес теряет в месяц, и один шаг."
	default:
		text = hi + "ещё одна быстрая проверка: " + strings.ToLower(q.Title) + ". Часто именно здесь прячутся деньги, которых не видно в отчётах."
	}
	return text, kb(row(map[string]any{"text": "✅ Начать: " + q.Title, "callback_data": quizPrefix + q.Key}), razbor), true
}
