package http

import (
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
)

// R32b: «Идеи и заметки» → «Аналитика». Одна лента, куда сходится аналитика
// со всей платформы: маркетинг, контент, CRM, учёт, ИИ, здоровье бизнеса
// резидентов, Gallup, мероприятия, дисциплина отчётов. Сервер собирает её
// из тех же документов клуба, что уже лежат в базе (ничего не считает
// заново в других местах), и держит минуту в памяти, чтобы лента
// открывалась сразу.
//
// GET /api/v1/platform/insights → {at, cards:[{id, area, src, date, n, unit,
// v, label, take, open, tone, data}]}

type insightCard struct {
	ID    string         `json:"id"`
	Area  string         `json:"area"`  // mkt, sales, fin, club, track, ai, team
	Src   string         `json:"src"`   // где это на платформе
	Date  string         `json:"date"`  // YYYY-MM-DD: свежесть данных
	N     float64        `json:"n"`     // ключевое число
	Unit  string         `json:"unit"`  // ₸, %, или пусто
	V     string         `json:"v"`     // ключевое число строкой, если оно не одно (12 из 40)
	Label string         `json:"label"` // что за число
	Take  string         `json:"take"`  // вывод одной строкой
	Open  string         `json:"open"`  // раздел платформы
	Tone  string         `json:"tone"`  // bad | good | ""
	Data  map[string]any `json:"data,omitempty"`
}

var insightsCache struct {
	sync.Mutex
	at   time.Time
	body []byte
}

const insightsTTL = time.Minute

// Insights: GET /api/v1/platform/insights (команда).
func (h *PlatformAI) Insights(c *gin.Context) {
	if !teamOnly(c) {
		return
	}
	insightsCache.Lock()
	if c.Query("fresh") == "" && insightsCache.body != nil && time.Since(insightsCache.at) < insightsTTL {
		b := insightsCache.body
		insightsCache.Unlock()
		c.Data(http.StatusOK, "application/json; charset=utf-8", b)
		return
	}
	insightsCache.Unlock()
	if h.repo == nil {
		c.JSON(http.StatusOK, gin.H{"at": time.Now().UTC(), "cards": []insightCard{}})
		return
	}
	ctx := c.Request.Context()
	docs := map[string]any{}
	for _, k := range []string{"bs_crm", contentKey, "bs_mkt_analysis", aiRecsKey, eventsFeedKey, "bs_kanban", "bs_ck_stats", "bs_gallup", platformSeedKey, "bs_threads_status"} {
		if d, err := h.repo.GetDoc(ctx, "club", k); err == nil && d != nil && !d.Deleted && d.Value != "" {
			var v any
			if json.Unmarshal([]byte(d.Value), &v) == nil {
				docs[k] = v
			}
		}
	}
	var boards []map[string]any
	if bs, err := h.repo.LiveBoards(ctx); err == nil {
		for _, b := range bs {
			var m map[string]any
			if json.Unmarshal(b.Data, &m) == nil && m != nil {
				boards = append(boards, m)
			}
		}
	}
	cards := buildInsights(docs, boards, time.Now())
	body, _ := json.Marshal(gin.H{"at": time.Now().UTC(), "cards": cards})
	insightsCache.Lock()
	insightsCache.at, insightsCache.body = time.Now(), body
	insightsCache.Unlock()
	c.Data(http.StatusOK, "application/json; charset=utf-8", body)
}

// ── Чтение документов ──

func iMap(v any) map[string]any { m, _ := v.(map[string]any); return m }
func iList(v any) []any         { l, _ := v.([]any); return l }
func iStr(v any) string {
	switch x := v.(type) {
	case string:
		return x
	case nil:
		return ""
	case float64:
		return strconv.FormatFloat(x, 'f', -1, 64)
	}
	return fmt.Sprint(v)
}
func iNum(v any) float64 {
	switch x := v.(type) {
	case float64:
		return x
	case string:
		s := strings.Map(func(r rune) rune {
			if (r >= '0' && r <= '9') || r == '-' || r == '.' {
				return r
			}
			return -1
		}, x)
		f, _ := strconv.ParseFloat(s, 64)
		return f
	}
	return 0
}

// iDay: дата из ISO (2026-10-03T…) или «03.10.2026», в днях Алматы.
func iDay(v any) (time.Time, bool) {
	s := strings.TrimSpace(iStr(v))
	if s == "" {
		return time.Time{}, false
	}
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		t = t.In(almaty)
		return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, almaty), true
	}
	if len(s) >= 10 {
		if t, err := time.ParseInLocation("2006-01-02", s[:10], almaty); err == nil {
			return t, true
		}
		if t, err := time.ParseInLocation("02.01.2006", s[:10], almaty); err == nil {
			return t, true
		}
	}
	return time.Time{}, false
}

// fmtThousands: 1234567 → «1 234 567» (пробел, как на платформе).
func fmtThousands(n float64) string {
	neg := n < 0
	if neg {
		n = -n
	}
	s := strconv.FormatInt(int64(n+0.5), 10)
	var b strings.Builder
	for i, r := range s {
		if i > 0 && (len(s)-i)%3 == 0 {
			b.WriteRune(' ')
		}
		b.WriteRune(r)
	}
	if neg {
		return "-" + b.String()
	}
	return b.String()
}

func pct(a, b float64) int {
	if b <= 0 {
		return 0
	}
	return int(a/b*100 + 0.5)
}

func plural(n int, one, few, many string) string {
	n = n % 100
	if n >= 11 && n <= 14 {
		return many
	}
	switch n % 10 {
	case 1:
		return one
	case 2, 3, 4:
		return few
	}
	return many
}

var leadSources = []struct{ id, name, pre string }{
	{"threads", "Threads", "threads"}, {"tgch", "Telegram-канал", "telegram-канал"}, {"ig", "Instagram", "instagram"},
	{"guide", "Чек-листы", "чек-лист"}, {"ref", "Рефералы", "реферал"},
}

func leadSource(s string) (string, string) {
	low := strings.ToLower(strings.TrimSpace(s))
	for _, x := range leadSources {
		if strings.HasPrefix(low, x.pre) || (x.id == "ig" && strings.HasPrefix(low, "карусель")) ||
			(x.id == "guide" && (strings.HasPrefix(low, "гайд") || strings.HasPrefix(low, "pdf-гайд"))) ||
			(x.id == "ref" && strings.HasPrefix(low, "рекомендац")) {
			return x.id, x.name
		}
	}
	return "other", "Бот, сайт и другие"
}

func leadDay(l map[string]any) (time.Time, bool) {
	if t, ok := iDay(l["startAt"]); ok {
		return t, true
	}
	if t, ok := iDay(l["created"]); ok {
		return t, true
	}
	return iDay(l["date"])
}

// buildInsights: карточки ленты. now: «сегодня» (тесты подставляют своё).
func buildInsights(docs map[string]any, boards []map[string]any, now time.Time) []insightCard {
	n := now.In(almaty)
	today := time.Date(n.Year(), n.Month(), n.Day(), 0, 0, 0, 0, almaty)
	ds := today.Format("2006-01-02")
	wd := int(today.Weekday())
	if wd == 0 {
		wd = 7
	}
	monday := today.AddDate(0, 0, 1-wd)
	prevMon := monday.AddDate(0, 0, -7)
	month := today.AddDate(0, 0, -30)
	out := []insightCard{}
	add := func(c insightCard) {
		if c.Date == "" {
			c.Date = ds
		}
		out = append(out, c)
	}

	// ── CRM: воронка, источники, путь лида ──
	crm := iMap(docs["bs_crm"])
	leads := []map[string]any{}
	for _, x := range iList(crm["leads"]) {
		if l := iMap(x); l != nil && l["deleted"] != true {
			leads = append(leads, l)
		}
	}
	if len(leads) > 0 {
		cols := []struct{ id, n string }{{"new", "Новый"}, {"work", "В работе"}, {"qual", "Квалифицирован"}, {"prepay", "Предоплата за разбор"}, {"meet", "Записан на разбор"}, {"diag", "Разбор проведён"}, {"decide", "Решение"}, {"won", "Резидент"}}
		cnt := map[string]int{}
		for _, l := range leads {
			cnt[iStr(l["col"])]++
		}
		steps := []any{}
		for _, c := range cols {
			steps = append(steps, []any{c.n, cnt[c.id]})
		}
		reached := cnt["prepay"] + cnt["meet"] + cnt["diag"] + cnt["decide"] + cnt["later"] + cnt["won"]
		take := fmt.Sprintf("До разбора дошли %d из %d лидов (%d%%), резидентами стали %d.", reached, len(leads), pct(float64(reached), float64(len(leads))), cnt["won"])
		tone := ""
		if pct(float64(reached), float64(len(leads))) < 10 {
			tone = "bad"
			take += " Узкое место: от заявки до записи на разбор."
		}
		add(insightCard{ID: "crm_funnel", Area: "sales", Src: "Продажи → Конверсии", N: float64(len(leads)), Label: "лидов в CRM", Take: take, Open: "crmFunnel", Tone: tone, Data: map[string]any{"steps": steps}})

		cur, prev := map[string]int{}, map[string]int{}
		names := map[string]string{}
		wk, pw := 0, 0
		for _, l := range leads {
			d, ok := leadDay(l)
			if !ok {
				continue
			}
			id, name := leadSource(iStr(l["source"]))
			names[id] = name
			if !d.Before(monday) && !d.After(today) {
				cur[id]++
				wk++
			} else if !d.Before(prevMon) && d.Before(monday) {
				prev[id]++
				pw++
			}
		}
		src := []any{}
		best, bestN := "", 0
		for _, s := range append(leadSources, struct{ id, name, pre string }{"other", "Бот, сайт и другие", ""}) {
			if cur[s.id] == 0 && prev[s.id] == 0 {
				continue
			}
			src = append(src, []any{s.name, cur[s.id], prev[s.id]})
			if cur[s.id] > bestN {
				best, bestN = s.name, cur[s.id]
			}
		}
		take = "На этой неделе лидов нет."
		tone = "bad"
		if wk > 0 {
			tone = ""
			take = fmt.Sprintf("Больше всего лидов из «%s»: %d.", best, bestN)
			if wk > pw {
				tone = "good"
				take += fmt.Sprintf(" На %d больше, чем на прошлой неделе.", wk-pw)
			} else if wk < pw {
				take += fmt.Sprintf(" На %d меньше, чем на прошлой неделе.", pw-wk)
			}
		}
		add(insightCard{ID: "leads_src", Area: "mkt", Src: "Маркетинг → Обзор", N: float64(wk), Label: "лидов за неделю", Take: take, Open: "mHome", Tone: tone, Data: map[string]any{"src": src, "prev": pw}})

		// Путь лида за 30 дней: Threads → бот → чек-лист → диагностика → разбор → клуб
		var th, bot, ck, dx, rz, won int
		for _, l := range leads {
			d, ok := leadDay(l)
			if !ok || d.Before(month) {
				continue
			}
			bot++
			if id, _ := leadSource(iStr(l["source"])); id == "threads" {
				th++
			}
			if k := iMap(l["ck"]); len(iList(k["started"]))+len(iList(k["done"]))+len(iList(k["pdf"])) > 0 {
				ck++
			}
			col := iStr(l["col"])
			if l["express"] != nil || col == "diag" || col == "won" {
				dx++
			}
			if col == "meet" || col == "diag" || col == "won" {
				rz++
			}
			if col == "won" {
				won++
			}
		}
		if bot > 0 {
			path := []any{[]any{"Threads", th}, []any{"Бот", bot}, []any{"Чек-лист", ck}, []any{"Диагностика", dx}, []any{"Разбор", rz}, []any{"Клуб", won}}
			worst, wn, wv := "", "", 101
			pv := []int{th, bot, ck, dx, rz, won}
			pn := []string{"Threads", "Бот", "Чек-лист", "Диагностика", "Разбор", "Клуб"}
			for i := 2; i < len(pv); i++ {
				if pv[i-1] > 0 {
					if p := pct(float64(pv[i]), float64(pv[i-1])); p < wv {
						wv, worst, wn = p, pn[i-1], pn[i]
					}
				}
			}
			take := fmt.Sprintf("Из %d пришедших в бот до клуба дошли %d.", bot, won)
			if worst != "" {
				take += fmt.Sprintf(" Больше всего теряем на шаге «%s → %s»: проходит %d%%.", worst, wn, wv)
			}
			add(insightCard{ID: "mkt_path", Area: "mkt", Src: "Маркетинг → путь лида", N: float64(bot), Label: "лидов за 30 дней", Take: take, Open: "crmFunnel", Tone: map[bool]string{true: "bad", false: ""}[wv < 15], Data: map[string]any{"steps": path}})
		}
	}

	// ── Контент-завод ──
	if ct := iMap(docs[contentKey]); ct != nil {
		pub, plan, fail := 0, 0, 0
		var lastFail string
		seen := map[string]bool{}
		for _, part := range []string{"queue", "history"} {
			for _, x := range iList(ct[part]) {
				it := iMap(x)
				if it == nil {
					continue
				}
				if id := iStr(it["id"]); id != "" {
					if seen[id] {
						continue
					}
					seen[id] = true
				}
				d, ok := iDay(it["at"])
				if !ok || d.Before(monday) || d.After(monday.AddDate(0, 0, 6)) {
					continue
				}
				switch iStr(it["status"]) {
				case "skipped":
					continue
				case "published":
					pub++
				case "failed":
					fail++
					lastFail = iStr(it["error"])
				}
				plan++
			}
		}
		if plan > 0 {
			take := fmt.Sprintf("Вышло %d из %d публикаций недели.", pub, plan)
			tone := ""
			if fail > 0 {
				tone = "bad"
				take += fmt.Sprintf(" Не опубликовались: %d", fail)
				if lastFail != "" {
					r := []rune(lastFail)
					if len(r) > 80 {
						lastFail = string(r[:80]) + "…"
					}
					take += " (" + lastFail + ")"
				}
				take += "."
			}
			add(insightCard{ID: "content_week", Area: "mkt", Src: "Маркетинг → Контент-завод", N: float64(pub), V: fmt.Sprintf("%d из %d", pub, plan), Label: "публикаций вышло", Take: take, Open: "content", Tone: tone})
		}
		if st := iMap(ct["stats"]); st != nil && iNum(st["total"]) > 0 {
			add(insightCard{ID: "content_total", Area: "mkt", Src: "Маркетинг → Контент-завод", N: iNum(st["total"]), Label: "публикаций всего", Take: "Контент выходит сам, лиды по ссылкам постов считаются в CRM.", Open: "content"})
		}
	}

	// ── План маркетинга ──
	if ma := iMap(docs["bs_mkt_analysis"]); ma != nil {
		acts := iList(ma["actions"])
		done, hi, work := 0, 0, 0
		for _, x := range acts {
			a := iMap(x)
			if a == nil {
				continue
			}
			if a["done"] == true || iStr(a["status"]) == "done" {
				done++
				continue
			}
			if iStr(a["status"]) == "work" {
				work++
			}
			if strings.Contains(strings.ToLower(iStr(a["priority"])), "высок") {
				hi++
			}
		}
		if len(acts) > 0 {
			take := fmt.Sprintf("Внедрено %d из %d действий, в работе %d.", done, len(acts), work)
			if hi > 0 {
				take += fmt.Sprintf(" Высокий приоритет ждёт: %d.", hi)
			}
			add(insightCard{ID: "mkt_plan", Area: "mkt", Src: "Маркетинг → Стратегия → Инсайты и план", N: float64(done), V: fmt.Sprintf("%d из %d", done, len(acts)), Label: "действий внедрено", Take: take, Open: "mktA", Tone: map[bool]string{true: "bad", false: ""}[hi > 0], Data: map[string]any{"tab": "insights"}})
		}
	}

	// ── Учёт: последний месяц P&L, долги, штрафы, отчёты, встречи, NPS ──
	seed := iMap(docs[platformSeedKey])
	if seed != nil {
		months := iList(seed["PL_MONTHS"])
		row := func(name string) []any {
			for _, x := range iList(seed["PL_ROWS"]) {
				r := iMap(x)
				if r != nil && strings.EqualFold(strings.TrimSpace(iStr(r["name"])), name) {
					return iList(r["vals"])
				}
			}
			return nil
		}
		inc, exp, prof := row("ИТОГО ДОХОДЫ"), row("ИТОГО РАСХОДЫ"), row("ЧИСТАЯ ПРИБЫЛЬ")
		if k := len(prof); k > 0 {
			// последний месяц, где были деньги
			i := k - 1
			for i > 0 && iNum(prof[i]) == 0 && (len(inc) <= i || iNum(inc[i]) == 0) {
				i--
			}
			p := iNum(prof[i])
			mname := ""
			if i < len(months) {
				mname = iStr(months[i])
			}
			take := "Прибыль " + strings.ToLower(mname) + ": " + fmtThousands(p) + " ₸"
			if i < len(inc) && i < len(exp) {
				take += fmt.Sprintf(" (доходы %s ₸, расходы %s ₸)", fmtThousands(iNum(inc[i])), fmtThousands(iNum(exp[i])))
			}
			tone := ""
			if i > 0 {
				pp := iNum(prof[i-1])
				if p > pp {
					take += ". Больше, чем месяцем раньше, на " + fmtThousands(p-pp) + " ₸"
					tone = "good"
				} else if p < pp {
					take += ". Меньше, чем месяцем раньше, на " + fmtThousands(pp-p) + " ₸"
					tone = "bad"
				}
			}
			if p < 0 {
				tone = "bad"
			}
			add(insightCard{ID: "fin_profit", Area: "fin", Src: "Учёт → PL и ДДС", N: p, Unit: "₸", Label: "чистая прибыль · " + mname, Take: take + ".", Open: "pl", Tone: tone})
		}
		debt, debtN := 0.0, 0
		for _, x := range iList(seed["RESIDENTS"]) {
			r := iMap(x)
			if r == nil {
				continue
			}
			if v := iNum(r["rest"]) + iNum(r["debtRenew"]); v > 0 {
				debt += v
				debtN++
			}
		}
		fines, finesN, rep, repPrev := 0.0, 0, 0, 0
		for _, x := range iList(seed["FINES"]) {
			f := iMap(x)
			if f == nil {
				continue
			}
			if !strings.Contains(strings.ToLower(iStr(f["status"])), "оплатил") || strings.Contains(strings.ToLower(iStr(f["status"])), "не ") {
				fines += iNum(f["amount"])
				finesN++
			}
			if strings.Contains(strings.ToLower(iStr(f["type"])), "отч") {
				if d, ok := iDay(f["date"]); ok {
					if !d.Before(today.AddDate(0, 0, -14)) {
						rep++
					} else if !d.Before(today.AddDate(0, 0, -28)) {
						repPrev++
					}
				}
			}
		}
		if debtN > 0 || finesN > 0 {
			take := fmt.Sprintf("Долги по оплате у %d %s на %s ₸", debtN, plural(debtN, "резидента", "резидентов", "резидентов"), fmtThousands(debt))
			take += fmt.Sprintf(", неоплаченных штрафов %d на %s ₸.", finesN, fmtThousands(fines))
			add(insightCard{ID: "fin_debts", Area: "fin", Src: "Клуб → Резиденты, Штрафы", N: debt + fines, Unit: "₸", Label: "ждём от резидентов", Take: take, Open: "fines", Tone: map[bool]string{true: "bad", false: ""}[debt+fines > 0]})
		}
		res := len(iList(seed["RESIDENTS"]))
		if res > 0 {
			take := fmt.Sprintf("За 2 недели не сдано отчётов: %d (до этого %d).", rep, repPrev)
			tone := ""
			if rep > repPrev {
				tone = "bad"
				take += " Дисциплина падает, стоит напомнить в чате."
			} else if rep < repPrev {
				tone = "good"
				take += " Дисциплина растёт."
			}
			add(insightCard{ID: "club_reports", Area: "club", Src: "Клуб → Отчёты", N: float64(rep), Label: "пропущенных отчётов за 14 дней", Take: take, Open: "aReports", Tone: tone})
		}
		sd := iMap(seed["SDATA"])
		up := 0
		next := ""
		for _, x := range iList(sd["schedule"]) {
			m := iMap(x)
			if m == nil {
				continue
			}
			if d, ok := iDay(m["date"]); ok && !d.Before(today) && d.Before(today.AddDate(0, 0, 7)) {
				up++
				if next == "" {
					next = iStr(m["res"]) + ", " + iStr(m["date"]) + " " + iStr(m["time"])
				}
			}
		}
		if res > 0 {
			take := "На ближайшие 7 дней встреч не назначено."
			if up > 0 {
				take = fmt.Sprintf("Ближайшая: %s.", strings.TrimSpace(next))
			}
			add(insightCard{ID: "club_meet", Area: "club", Src: "Клуб → Встречи", N: float64(up), Label: "встреч на 7 дней", Take: take, Open: "aSched", Tone: map[bool]string{true: "bad", false: ""}[up == 0]})
		}
		if nps := iList(sd["nps"]); len(nps) > 0 {
			// R39: NPS = % промоутеров (9-10) − % критиков (0-6), последний опрос, без Chat ID вместо оценки (nps.go)
			var rows []map[string]any
			for _, x := range nps {
				if m := iMap(x); m != nil {
					rows = append(rows, m)
				}
			}
			if r := ComputeNPS(rows); r.Count > 0 {
				take := fmt.Sprintf("NPS %d по %d %s, средняя оценка %s из 10. Промоутеров %d, критиков %d.", r.NPS, r.Count, plural(r.Count, "ответу", "ответам", "ответам"),
					strings.Replace(fmt.Sprintf("%.1f", r.Avg), ".", ",", 1), r.Promoters, r.Detractors)
				if r.Detractors > 0 {
					take += " Критикам (6 и ниже) стоит позвонить."
				}
				add(insightCard{ID: "club_nps", Area: "club", Src: "Клуб → NPS", N: float64(r.NPS), V: strconv.Itoa(r.NPS), Label: "NPS резидентов", Take: take, Open: "nps", Tone: map[bool]string{true: "bad", false: "good"}[r.NPS < 30]})
			}
		}
	}

	// ── Здоровье бизнеса резидентов: диагнозы на досках ──
	if len(boards) > 0 {
		organs, diags := map[string]int{}, map[string]int{}
		nb := 0
		for _, b := range boards {
			if b["archived"] == true || b["deleted"] == true {
				continue
			}
			seen := map[string]bool{}
			has := false
			for _, x := range iList(b["nodes"]) {
				nd := iMap(x)
				if nd == nil || iStr(nd["type"]) != "diag" || nd["deleted"] == true {
					continue
				}
				t := strings.TrimSpace(iStr(nd["title"]))
				if t == "" || seen[t] {
					continue
				}
				seen[t] = true
				has = true
				diags[t]++
				if o := strings.TrimSpace(iStr(nd["organ"])); o != "" {
					organs[o]++
				}
			}
			if has {
				nb++
			}
		}
		if len(diags) > 0 {
			top := topN(organs, 3)
			td := topN(diags, 3)
			take := ""
			if len(td) > 0 {
				take = fmt.Sprintf("Чаще всего: «%s» у %d из %d.", td[0][0], td[0][1], nb)
			}
			if len(top) > 0 {
				take += fmt.Sprintf(" Больше всего болит орган «%s».", top[0][0])
			}
			if len(td) > 0 && nb >= 3 && td[0][1].(int)*3 >= nb {
				take += " Это вопрос к продукту клуба, а не к людям."
			}
			add(insightCard{ID: "track_health", Area: "track", Src: "Трекинг → Карта диагнозов", N: float64(len(diags)), Label: "разных диагнозов у " + strconv.Itoa(nb) + " " + plural(nb, "резидента", "резидентов", "резидентов"), Take: take, Open: "diagMap", Data: map[string]any{"organs": top, "diags": td}})
		}
	}

	// ── Gallup: таланты клуба ──
	if g := iMap(docs["bs_gallup"]); len(g) > 0 {
		cnt := map[string]int{}
		people := 0
		for _, x := range g {
			th := iList(iMap(x)["themes"])
			if len(th) == 0 {
				continue
			}
			people++
			for i, t := range th {
				if i >= 5 {
					break
				}
				cnt[iStr(t)]++
			}
		}
		if people > 0 {
			top := topN(cnt, 5)
			add(insightCard{ID: "team_gallup", Area: "team", Src: "Резиденты → Gallup", N: float64(people), Label: "разборов Gallup", Take: "Сильные стороны клуба: топ-5 талантов у резидентов. Подбирайте пятёрки так, чтобы таланты дополняли друг друга.", Open: "sFive", Data: map[string]any{"themes": top}})
		}
	}

	// ── Рекомендации ИИ ──
	if r := iMap(docs[aiRecsKey]); r != nil {
		items := iList(r["items"])
		nw, last, lastD := 0, "", ""
		for _, x := range items {
			it := iMap(x)
			if it == nil {
				continue
			}
			st := iStr(it["status"])
			if st == "" || st == "new" || st == "ask" {
				nw++
			}
			if last == "" {
				last, lastD = iStr(it["title"]), iStr(it["date"])
			}
		}
		if len(items) > 0 {
			take := fmt.Sprintf("Ждут решения: %d.", nw)
			if last != "" {
				take = "Последняя: «" + last + "». " + take
			}
			c := insightCard{ID: "ai_recs", Area: "ai", Src: "BS → Рекомендации ИИ", N: float64(nw), Label: "практик ждут решения", Take: take, Open: "aiRec", Tone: map[bool]string{true: "bad", false: ""}[nw > 3]}
			if d, ok := iDay(lastD); ok {
				c.Date = d.Format("2006-01-02")
			}
			add(c)
		}
	}

	// ── Мероприятия ──
	if ev := iMap(docs[eventsFeedKey]); ev != nil {
		up := 0
		next := ""
		for _, x := range iList(ev["items"]) {
			it := iMap(x)
			if d, ok := iDay(it["date"]); ok && !d.Before(today) && d.Before(today.AddDate(0, 0, 14)) {
				up++
				if next == "" {
					next = iStr(it["title"]) + ", " + d.Format("02.01")
				}
			}
		}
		if up > 0 {
			add(insightCard{ID: "club_events", Area: "club", Src: "BS → Мероприятия", N: float64(up), Label: "событий в Алматы на 2 недели", Take: "Ближайшее: " + next + ". Хороший повод для офлайн-касания с лидами.", Open: "events"})
		}
	}

	// ── Задачи команды ──
	if kb := iMap(docs["bs_kanban"]); kb != nil {
		open, over, done7 := 0, 0, 0
		for _, x := range iList(kb["cards"]) {
			c := iMap(x)
			if c == nil {
				continue
			}
			if iStr(c["col"]) == "done" || c["done"] == true {
				if d, ok := iDay(c["doneAt"]); ok && !d.Before(today.AddDate(0, 0, -7)) {
					done7++
				}
				continue
			}
			open++
			if d, ok := iDay(c["due"]); ok && d.Before(today) {
				over++
			}
		}
		take := fmt.Sprintf("Открыто %d, просрочено %d.", open, over)
		if done7 > 0 {
			take += fmt.Sprintf(" За неделю закрыто %d.", done7)
		}
		add(insightCard{ID: "team_tasks", Area: "team", Src: "BS → Цели и задачи", N: float64(over), Label: "просроченных задач", Take: take, Open: "kanban", Tone: map[bool]string{true: "bad", false: "good"}[over > 0]})
	}

	// ── Чек-листы: кто проходит ──
	if ck := iMap(docs["bs_ck_stats"]); ck != nil {
		users := iMap(ck["users"])
		started, finished := 0, 0
		for _, x := range users {
			for _, y := range iMap(iMap(x)["items"]) {
				started++
				if iMap(y)["finished"] != nil {
					finished++
				}
			}
		}
		if len(users) > 0 {
			add(insightCard{ID: "ck_progress", Area: "mkt", Src: "Трекинг → Инструменты → Чек-листы", N: float64(len(users)), Label: "человек открыли чек-листы", Take: fmt.Sprintf("Начато чек-листов %d, пройдено до конца %d (%d%%).", started, finished, pct(float64(finished), float64(started))), Open: "guides"})
		}
	}

	areaRank := map[string]int{"sales": 0, "mkt": 1, "fin": 2, "club": 3, "track": 4, "team": 5, "ai": 6}
	sort.SliceStable(out, func(i, j int) bool {
		ti, tj := out[i].Tone == "bad", out[j].Tone == "bad"
		if ti != tj {
			return ti
		}
		return areaRank[out[i].Area] < areaRank[out[j].Area]
	})
	return out
}

func topN(m map[string]int, n int) [][]any {
	type kv struct {
		k string
		v int
	}
	l := make([]kv, 0, len(m))
	for k, v := range m {
		l = append(l, kv{k, v})
	}
	sort.Slice(l, func(i, j int) bool { return l[i].v > l[j].v || (l[i].v == l[j].v && l[i].k < l[j].k) })
	out := [][]any{}
	for i := 0; i < len(l) && i < n; i++ {
		out = append(out, []any{l[i].k, l[i].v})
	}
	return out
}
