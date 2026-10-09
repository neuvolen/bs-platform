package http

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strings"

	"github.com/bnursik/business_surgery_backend/internal/ai"
	"github.com/bnursik/business_surgery_backend/internal/content"
	"github.com/bnursik/business_surgery_backend/internal/tplpdf"
	"github.com/gin-gonic/gin"
)

// R71: the analysis ×2 and «Чем Business Surgery будет полезен именно вам».
//
// gallupPlusFor builds the second half of the analysis from the order of the
// talents only, by rules (platform_gallup_kb.go), so it is there without AI:
// on the screen (POST /gallup/plus), in the PDF (sanitizeGallupDoc) and in
// the corrected analysis sent to a resident. The light AI model adds one
// part (gallupClubAI: the intro, why each mechanic of the club fits this
// person, why the likely diagnoses, the first cycle); it rides with the deep
// analysis (deep.club, v 3) and replaces the rule lines it covers.

const gallupDeepVer = 3

var gallupDomainOrder = []string{"executing", "influencing", "relationship", "strategic"}

// gallupClubAI: what the light model writes for the club section.
type gallupClubAI struct {
	Intro     string `json:"intro"`
	Mechanics []struct {
		Key string `json:"key"`
		Why string `json:"why"`
	} `json:"mechanics"`
	Diags []struct {
		ID  string `json:"id"`
		Why string `json:"why"`
	} `json:"diags"`
	First []struct {
		Task   string `json:"task"`
		Talent string `json:"talent"`
		Result string `json:"result"`
	} `json:"first"`
}

var gallupMechKeys = []string{"cycle", "tracking", "five", "library", "tasks", "report"}

// gallupOrderOf: talent keys from what the platform sends (keys, English or
// Russian names), without repeats.
func gallupOrderOf(in []string) []string {
	var out []string
	seen := map[string]bool{}
	for _, x := range in {
		if k := gallupKeyOf(x); k != "" && !seen[k] {
			seen[k] = true
			out = append(out, k)
		}
	}
	return out
}

func ruRank(k string, rank map[string]int) string {
	return gallupTheme(k)[2] + " (" + fmt.Sprint(rank[k]) + ")"
}

func quoteRu(k string) string { return "«" + gallupTheme(k)[2] + "»" }

// gallupStrips: the places of each domain's talents, the domain strength
// (100: all its talents on top, 50: the middle of the list).
func gallupStrips(order []string) []tplpdf.GallupStrip {
	n := len(order)
	var out []tplpdf.GallupStrip
	for _, dk := range gallupDomainOrder {
		s := tplpdf.GallupStrip{Key: dk, Ru: gallupDomainRu[dk]}
		sum, cnt := 0.0, 0
		for i, k := range order {
			if gallupTheme(k)[3] != dk {
				continue
			}
			r := i + 1
			s.Ranks = append(s.Ranks, r)
			if r <= 5 {
				s.Top5++
			}
			if r <= 10 {
				s.Top10++
			}
			if n > 1 {
				sum += float64(n-r) / float64(n-1)
			} else {
				sum++
			}
			cnt++
		}
		if cnt > 0 {
			s.Score = int(sum/float64(cnt)*100 + 0.5)
		}
		out = append(out, s)
	}
	return out
}

// gallupDomainsRanked: domain keys from the strongest; with a partial
// report the count in the top 10 decides first.
func gallupDomainsRanked(st []tplpdf.GallupStrip, full bool) []tplpdf.GallupStrip {
	r := append([]tplpdf.GallupStrip(nil), st...)
	sort.SliceStable(r, func(i, j int) bool {
		if !full && r[i].Top10 != r[j].Top10 {
			return r[i].Top10 > r[j].Top10
		}
		if r[i].Score != r[j].Score {
			return r[i].Score > r[j].Score
		}
		return r[i].Top5 > r[j].Top5
	})
	return r
}

func gallupPairFor(a, b string) (pairKB, bool) {
	for _, p := range gallupPairs {
		if (p.A == a && p.B == b) || (p.A == b && p.B == a) {
			return p, true
		}
	}
	return pairKB{}, false
}

// gallupTopPairs: the ten pairs of the top 5, named pairs first.
func gallupTopPairs(top []string, rank map[string]int) []tplpdf.GallupPair {
	var named, generic []tplpdf.GallupPair
	used := map[string]int{}
	for i := 0; i < len(top); i++ {
		for j := i + 1; j < len(top); j++ {
			a, b := top[i], top[j]
			pr := tplpdf.GallupPair{A: ruRank(a, rank), B: ruRank(b, rank)}
			if kb, ok := gallupPairFor(a, b); ok {
				pr.Kind, pr.Title, pr.Text, pr.Rule = kb.Kind, kb.Title, kb.Text, kb.Rule
				named = append(named, pr)
				continue
			}
			da, db := gallupTheme(a)[3], gallupTheme(b)[3]
			if da > db {
				da, db = db, da
			}
			g := gallupDomainPair[da+"|"+db]
			switch used[da+"|"+db] {
			case 0:
			case 1:
				g = gallupDomainPair2[da+"|"+db]
			default: // a third pair of the same domains: named by its talents
				g2 := gallupDomainPair2[da+"|"+db]
				g = [3]string{gallupTheme(a)[2] + " и " + gallupTheme(b)[2], g[1], g2[2]}
			}
			used[da+"|"+db]++
			pr.Kind, pr.Title = "synergy", g[0]
			pr.Text = quoteRu(a) + " и " + quoteRu(b) + ": " + lowerFirst(g[1])
			pr.Rule = g[2]
			generic = append(generic, pr)
		}
	}
	return append(named, generic...)
}

// gallupLikelyDiags: diagnoses of the library the profile leads to. Top
// talents count by their place (1-5 three points, 6-10 two), the weakest
// talents of a full profile by theirs (30-34 two, 25-29 one).
func gallupLikelyDiags(order []string, max int) []tplpdf.GallupDiag {
	n := len(order)
	type acc struct {
		score int
		first int
		why   []string
	}
	m := map[string]*acc{}
	seq := 0
	add := func(id string, pts int, why string) {
		a := m[id]
		if a == nil {
			a = &acc{first: seq}
			m[id] = a
			seq++
		}
		a.score += pts
		if len(a.why) < 2 {
			a.why = append(a.why, why)
		}
	}
	for i, k := range order {
		r := i + 1
		pts := 0
		switch {
		case r <= 5:
			pts = 3
		case r <= 10:
			pts = 2
		}
		if pts > 0 {
			for _, l := range gallupDiagTop[k] {
				add(l.ID, pts, quoteRu(k)+" на "+fmt.Sprint(r)+" месте "+l.Why+".")
			}
		}
		if n >= 30 && r >= 25 {
			lp := 1
			if r >= 30 {
				lp = 2
			}
			for _, l := range gallupDiagLow[k] {
				add(l.ID, lp, quoteRu(k)+" на "+fmt.Sprint(r)+" месте из "+fmt.Sprint(n)+": без человека с этим талантом рядом "+l.Why+".")
			}
		}
	}
	ids := make([]string, 0, len(m))
	for id := range m {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool {
		a, b := m[ids[i]], m[ids[j]]
		if a.score != b.score {
			return a.score > b.score
		}
		return a.first < b.first
	})
	var out []tplpdf.GallupDiag
	organs := map[string]int{}
	for _, id := range ids {
		d, ok := content.RichDiagByID(id)
		if !ok {
			continue
		}
		if organs[d.Organ] >= 2 { // spread over the organs of the business
			continue
		}
		organs[d.Organ]++
		g := tplpdf.GallupDiag{ID: id, Title: d.Title, Organ: d.Organ, Sub: d.Subtitle, Why: strings.Join(m[id].why, " ")}
		for _, t := range d.Cure {
			if t = strings.TrimSpace(t); t != "" && len(g.Tools) < 2 {
				g.Tools = append(g.Tools, t)
			}
		}
		out = append(out, g)
		if len(out) >= max {
			break
		}
	}
	return out
}

// gallupHook: a talent that changes what a mechanic of the club gives.
var gallupHook = map[string][2]string{
	"command":        {"tracking", "С «Распорядителем» в топе вам редко возражают. На разборе двое трекеров спорят с вами по фактам, и решения проходят проверку до того, как станут приказом."},
	"self-assurance": {"tracking", "С высокой «Уверенностью» вы решаете сами. Разбор даёт вам внешнюю проверку: двое трекеров смотрят решение до того, как вы вложите в него деньги."},
	"deliberative":   {"tracking", "«Осмотрительность» тянет решение до полной ясности. Трекеры ставят дату решения и проверяют, что риск закрыт, а решение принято."},
	"competition":    {"five", "«Конкуренция» включается от сравнения. В пятёрке ваши цифры видны рядом с цифрами других собственников, и это работает сильнее любой мотивации."},
	"significance":   {"five", "«Значимости» нужен видимый результат. Пятёрка и разбор каждые 10 дней дают место, где ваш рост замечают и признают."},
	"woo":            {"five", "«Обаяние» даёт вам быстрые знакомства. Пятёрка и встречи клуба превращают их в деловые связи с собственниками вашего уровня."},
	"relator":        {"five", "«Отношениям» нужен близкий круг. Пятёрка становится группой, где можно говорить честно о трудном."},
	"responsibility": {"tasks", "С «Ответственностью» вы берёте на себя всё. Задачи цикла пишутся с именем ответственного на каждом шаге, и на разборе трекеры проверяют, что вы их отдали."},
	"ideation":       {"cycle", "«Генератору идей» нужен фильтр. В цикле в работу идёт одна идея, остальные ждут следующего разбора, и ни одна не теряется."},
	"activator":      {"cycle", "«Активатор» быстро запускает. Цикл добавляет проверку на 10-й день, и запущенное доводится до результата."},
	"analytical":     {"report", "«Аналитику» нужны цифры. Замер раз в цикл и ежедневный отчёт дают вам ряд данных, по которому решение принимается без ожидания полной картины."},
	"discipline":     {"report", "«Дисциплинированность» любит ритм. Отчёт до 22:00 и замер раз в цикл ложатся в ваш порядок без усилий."},
	"adaptability":   {"report", "С «Адаптивностью» своего ритма нет. Ежедневный отчёт до 22:00 даёт внешний ритм, а цикл в 10 дней достаточно короткий, чтобы не душить."},
	"input":          {"library", "«Собиратель» копит знания. Библиотека клуба заменяет поиск: вы берёте один проверенный инструмент и внедряете его в этом цикле."},
	"learner":        {"library", "«Ученику» интересно новое. Каждый цикл вы осваиваете инструмент из библиотеки и сразу применяете его в бизнесе."},
	"intellection":   {"cycle", "«Мышление» требует времени подумать. Цикл даёт на это 10 дней и заканчивает обдумывание задачей с датой."},
	"strategic":      {"tasks", "Со «Стратегией» в топе путь у вас в голове. Задачи цикла переводят его в шаги для команды с ответственными и сроками."},
	"achiever":       {"tasks", "«Достигатор» вычёркивает много дел. В цикле трекеры оставляют 2-3 задачи, которые двигают цифру, и ваш темп идёт в них."},
	"maximizer":      {"tasks", "«Максимизатор» доводит до идеала. Задачи цикла ставятся с планкой «достаточно хорошо» и датой запуска."},
	"empathy":        {"tracking", "«Эмпатия» откладывает жёсткие решения о людях и ценах. Трекеры помогают принять их по фактам и провести разговор."},
	"harmony":        {"tracking", "«Гармония» уводит от конфликта. Трекеры возвращают на разбор отложенные трудные разговоры и помогают провести их по шаблону."},
	"positivity":     {"report", "«Позитивность» приукрашивает. Замер раз в цикл показывает цифры как есть, без настроения."},
	"futuristic":     {"tasks", "«Будущее» видит цель через годы. Задачи цикла показывают, какой шаг ведёт к ней в эти 10 дней."},
	"developer":      {"tasks", "«Развитие» растит людей. Задачи цикла ставят каждому цель с цифрой, и рост сотрудника становится измеримым."},
}

// gallupPlusFor: the second half of the analysis by rules, with the AI
// lines when they are there.
func gallupPlusFor(orderIn []string, cai *gallupClubAI) *tplpdf.GallupPlus {
	order := gallupOrderOf(orderIn)
	n := len(order)
	if n < 3 {
		return nil
	}
	full := n >= 30
	rank := gallupRanks(order)
	top := order[:min(5, n)]
	x := &tplpdf.GallupPlus{N: n}

	// 1. the top 5 in the owner's work
	for _, k := range top {
		kb, th := gallupKB[k], gallupTheme(k)
		x.Top = append(x.Top, tplpdf.GallupPlusTalent{Rank: rank[k], Key: k, Ru: th[2], Name: th[1], Domain: gallupDomainRu[th[3]], Rows: []tplpdf.GallupPlusRow{
			{L: "Продажи", T: kb.Sales}, {L: "Команда", T: kb.Team}, {L: "Деньги", T: kb.Money}, {L: "Найм", T: kb.Hire},
			{L: "Слепое пятно", T: kb.Blind}, {L: "Что выжигает", T: kb.Burnout}, {L: "Как с вами говорить", T: kb.Talk},
		}})
	}
	// 2. domains: where the talents sit
	x.Strip = gallupStrips(order)
	ranked := gallupDomainsRanked(x.Strip, full)
	lead, weak := ranked[0].Key, ranked[len(ranked)-1].Key
	// 3. the pairs of the top 5
	x.Pairs = gallupTopPairs(top, rank)
	// 4. who owns which function
	who := []string{"", "Вы сами", "Вы со страховкой", "Партнёр или руководитель", "Нанятый специалист"}
	for i, s := range ranked {
		kb := gallupDomainKB[s.Key]
		why := "Ваш ведущий домен: вы " + kb.Own + "."
		switch i {
		case 1:
			why = "Сильная сторона, но не главная: вы " + kb.Own + ", рядом нужен человек, который проверяет."
		case 2:
			why = "Домен в середине профиля. Держите роль за партнёром или руководителем с этой сильной стороной, сами задавайте цель и цифру."
		case 3:
			why = "Слабый домен: без сильного человека здесь " + kb.Weak + "."
		}
		x.Split = append(x.Split, tplpdf.GallupSplit{Area: kb.Role, Who: who[i+1], Level: i + 1, Why: why})
	}
	// 5. the person to hire
	wk := gallupDomainKB[weak]
	h := &tplpdf.GallupHireProfile{Role: wk.Seek, Why: "Самый слабый домен вашего профиля: " + gallupDomainRu[weak] + ". Без такого человека " + wk.Weak + ".", Ask: wk.Ask[:], Red: "Кандидат вам не подходит, если " + wk.RedFlag + "."}
	type cand struct {
		k string
		r int
	}
	var cs []cand
	for _, t := range gallupThemes {
		if t[3] != weak {
			continue
		}
		r, ok := rank[t[0]]
		if !ok {
			r = 99
		}
		cs = append(cs, cand{t[0], r})
	}
	sort.SliceStable(cs, func(i, j int) bool { return cs[i].r > cs[j].r })
	for _, c := range cs[:min(3, len(cs))] {
		h.Talents = append(h.Talents, gallupTheme(c.k)[2])
	}
	x.Hire = h
	// 6. how to talk to you
	for _, k := range top {
		x.Talk = append(x.Talk, quoteRu(k)+": "+lowerFirst(gallupKB[k].Talk))
	}
	// 7. the 10-day experiment
	t1, t2 := top[0], top[min(1, len(top)-1)]
	x.Exp = []tplpdf.GallupTriple{
		{A: "День 1", B: "Выберите одну цель цикла с цифрой и запишите её в отчёт. Задачу сформулируйте под ваш главный талант: " + gallupKB[t1].Task + ".", C: "Цель, цифра и задача в отчёте до 22:00."},
		{A: "Дни 1-10", B: gallupKB[t1].Exp, C: "Опора на " + quoteRu(t1) + ", место " + fmt.Sprint(rank[t1]) + ". Отметка каждый день в отчёте."},
	}
	if t2 != t1 {
		x.Exp = append(x.Exp, tplpdf.GallupTriple{A: "Дни 2-10", B: gallupKB[t2].Exp, C: "Опора на " + quoteRu(t2) + ", место " + fmt.Sprint(rank[t2]) + "."})
	}
	x.Exp = append(x.Exp,
		tplpdf.GallupTriple{A: "День 5", B: "Середина цикла: покажите промежуточную цифру пятёрке и попросите человека с сильным доменом «" + gallupDomainRu[weak] + "» проверить ваш план.", C: "Слабое место профиля закрыто внешним взглядом."},
		tplpdf.GallupTriple{A: "Дни 6-9", B: "Следите за слепым пятном " + quoteRu(t1) + ": " + lowerFirst(gallupKB[t1].Blind), C: "Если заметили, запишите в отчёт и что сделали."},
		tplpdf.GallupTriple{A: "День 10", B: "Разбор с трекерами: цифра цели, что дал каждый эксперимент, что оставляем в работе на следующий цикл.", C: "Замер и задачи следующего цикла."},
	)
	// 8. the club
	x.Club = gallupClubFor(order, rank, top, lead, weak, cai)
	x.AI = cai != nil && x.Club != nil && (cai.Intro != "" || len(cai.First) > 0)
	return x
}

func gallupClubFor(order []string, rank map[string]int, top []string, lead, weak string, cai *gallupClubAI) *tplpdf.GallupClub {
	lk := gallupDomainKB[lead]
	c := &tplpdf.GallupClub{}
	var names []string
	for _, k := range top[:min(3, len(top))] {
		names = append(names, quoteRu(k))
	}
	c.Intro = "Ваш ведущий домен: " + gallupDomainRu[lead] + ", вы " + lk.Own + ". Опора профиля: " + strings.Join(names, ", ") +
		". Слабее всего «" + gallupDomainRu[weak] + "»: здесь " + gallupDomainKB[weak].Weak + ". Клуб работает с обеими сторонами: каждые 10 дней задачи ставятся под ваши сильные таланты, а слабое место закрывают трекеры, пятёрка и готовые инструменты."
	// what each mechanic gives this profile: the lead domain, then the first
	// hook of a top-5 talent
	hooks := map[string]string{}
	for _, k := range top {
		if hk, ok := gallupHook[k]; ok {
			if _, dup := hooks[hk[0]]; !dup {
				hooks[hk[0]] = hk[1]
			}
		}
	}
	tasks := []string{}
	for _, k := range top[:min(2, len(top))] {
		tasks = append(tasks, quoteRu(k)+": "+gallupKB[k].Task)
	}
	base := map[string][3]string{
		"cycle":    {"10-дневный цикл", "Каждые 10 дней разбор: диагноз, инструмент, задачи со сроками и проверка результата прошлого цикла.", lk.Cycle},
		"tracking": {"Трекинг двумя основателями", "На каждом разборе оба основателя клуба, Рустам и Береке. Между разборами трекеры видят ваш ежедневный отчёт и задачи.", lk.Tracking},
		"five":     {"Ваша пятёрка", "Вы в группе из пяти собственников, которые решают похожие задачи и видят прогресс друг друга.", lk.Five},
		"library":  {"Библиотека диагнозов и инструментов", "Больше 170 диагнозов с симптомами, причинами и ценой болезни, больше 250 инструментов с шаблоном, пошаговой инструкцией и видео.", lk.Library},
		"tasks":    {"Задачи под ваш Gallup", "Трекеры формулируют задачи цикла с учётом ваших талантов: так задача делается на вашей сильной стороне.", "Для вашего профиля: " + strings.Join(tasks, "; ") + "."},
		"report":   {"Ежедневный отчёт и замер", "Каждый день до 22:00 короткий отчёт, опоздание стоит 10 000 ₸. Раз в цикл пятиминутный замер показывает, что в бизнесе реально выросло.", "Отчёт держит ритм цикла, замер показывает цифрой, сработала ли опора на ваши таланты."},
	}
	aiWhy := map[string]string{}
	if cai != nil {
		for _, m := range cai.Mechanics {
			aiWhy[m.Key] = m.Why
		}
	}
	for _, k := range gallupMechKeys {
		b := base[k]
		you := b[2]
		if hk := hooks[k]; hk != "" {
			you = hk + " " + you
		}
		if w := aiWhy[k]; w != "" {
			you = w
		}
		c.Mechanics = append(c.Mechanics, tplpdf.GallupMech{Key: k, Title: b[0], What: b[1], You: you})
	}
	c.Diags = gallupLikelyDiags(order, 5)
	if cai != nil {
		why := map[string]string{}
		for _, d := range cai.Diags {
			why[d.ID] = d.Why
		}
		for i := range c.Diags {
			if w := why[c.Diags[i].ID]; w != "" {
				c.Diags[i].Why = w
			}
		}
	}
	seen := map[string]bool{}
	for _, d := range c.Diags {
		for _, t := range d.Tools {
			if !seen[t] && len(c.Tools) < 6 {
				seen[t] = true
				c.Tools = append(c.Tools, t)
			}
		}
	}
	// the first cycle: AI, else the top diagnosis with its first tool as a
	// task framed for the top talent
	if cai != nil && len(cai.First) > 0 {
		for _, f := range cai.First {
			t := tplpdf.GallupTriple{A: f.Task, C: f.Result}
			if k := gallupKeyOf(f.Talent); k != "" {
				t.B = "Опора на " + quoteRu(k) + "."
			}
			c.First = append(c.First, t)
		}
	} else if len(c.Diags) > 0 {
		d := c.Diags[0]
		t1 := top[0]
		c.First = append(c.First, tplpdf.GallupTriple{A: "Разбор: проверяем диагноз «" + d.Title + "» на ваших цифрах.", B: "Трекеры смотрят симптомы и считают цену болезни в тенге.", C: ""})
		if len(d.Tools) > 0 {
			c.First = append(c.First, tplpdf.GallupTriple{A: "Инструмент цикла: «" + d.Tools[0] + "».", B: "Задача под " + quoteRu(t1) + ": " + gallupKB[t1].Task + ".", C: ""})
		}
		c.First = append(c.First, tplpdf.GallupTriple{A: "День 10: замер и проверка результата.", B: "Если цифра сдвинулась, инструмент остаётся в работе, следующий цикл берёт второй диагноз.", C: ""})
	}
	if cai != nil && cai.Intro != "" {
		c.Intro = cai.Intro
	}
	return c
}

// ── AI: the club part, light model ──

func gallupClubPrompt(order []string, x *tplpdf.GallupPlus) string {
	var b strings.Builder
	b.WriteString("Профиль CliftonStrengths собственника бизнеса (место, тема, домен):\n")
	for i, k := range order[:min(10, len(order))] {
		th := gallupTheme(k)
		fmt.Fprintf(&b, "%d. %s (%s), домен %s\n", i+1, th[2], k, gallupDomainRu[th[3]])
	}
	if len(order) >= 30 {
		b.WriteString("Самые слабые: ")
		var w []string
		for i := len(order) - 5; i < len(order); i++ {
			w = append(w, fmt.Sprintf("%d. %s", i+1, gallupTheme(order[i])[2]))
		}
		b.WriteString(strings.Join(w, ", ") + "\n")
	}
	b.WriteString("\nКлуб Business Surgery (Алматы), что в нём есть:\n" +
		"- cycle: каждые 10 дней разбор бизнеса: диагноз, инструмент, задачи со сроками, проверка результата;\n" +
		"- tracking: на каждом разборе оба основателя, Рустам и Береке, между разборами видят ежедневный отчёт;\n" +
		"- five: пятёрка, группа из пяти собственников;\n" +
		"- library: больше 170 диагнозов и 250 инструментов с шаблонами и видео;\n" +
		"- tasks: задачи цикла формулируются под таланты Gallup;\n" +
		"- report: ежедневный отчёт до 22:00 и замер раз в цикл.\n\n")
	if x != nil && x.Club != nil && len(x.Club.Diags) > 0 {
		b.WriteString("Вероятные диагнозы из библиотеки (id: название):\n")
		for _, d := range x.Club.Diags {
			fmt.Fprintf(&b, "%s: %s\n", d.ID, d.Title)
		}
	}
	b.WriteString("\nНапиши раздел «Чем клуб будет полезен именно вам». Обращение на «вы», по-русски, конкретно, без комплиментов и общих слов. " +
		"Без длинного тире, только дефис. Не используй конструкцию «не X, а Y». Числа с пробелом: 10 000. Таланты называй по-русски в кавычках-ёлочках.\n" +
		"1. intro: 3-4 предложения, как профиль этого собственника встречается с клубом: что клуб усилит и что закроет.\n" +
		"2. mechanics: для каждого key (cycle, tracking, five, library, tasks, report) why: 1-2 предложения с конкретным примером из жизни этого собственника.\n" +
		"3. diags: для каждого диагноза из списка id и why: одно предложение, почему этот профиль к нему склонен.\n" +
		"4. first: 3 задачи первого цикла: task (что сделать за 10 дней, измеримо), talent (ключ таланта из профиля, на который задача опирается), result (цифра или факт к 10-му дню).\n\n" +
		"Ответ строго JSON:\n" +
		`{"intro":"","mechanics":[{"key":"cycle","why":""}],"diags":[{"id":"","why":""}],"first":[{"task":"","talent":"","result":""}]}`)
	return b.String()
}

// parseGallupClubAI keeps the valid lines; too little left is an error.
func parseGallupClubAI(raw string, order []string, diagIDs []string) (*gallupClubAI, error) {
	var in gallupClubAI
	if err := json.Unmarshal([]byte(gallupJSONObject(raw)), &in); err != nil {
		return nil, fmt.Errorf("JSON не читается: %v", err)
	}
	rank := gallupRanks(order)
	out := &gallupClubAI{Intro: gallupText(in.Intro)}
	okMech := map[string]bool{}
	for _, k := range gallupMechKeys {
		okMech[k] = true
	}
	seen := map[string]bool{}
	for _, m := range in.Mechanics {
		k := strings.ToLower(strings.TrimSpace(m.Key))
		w := gallupText(m.Why)
		if okMech[k] && !seen[k] && runes(w) >= 30 {
			seen[k] = true
			out.Mechanics = append(out.Mechanics, struct {
				Key string `json:"key"`
				Why string `json:"why"`
			}{k, w})
		}
	}
	okDiag := map[string]bool{}
	for _, id := range diagIDs {
		okDiag[id] = true
	}
	for _, d := range in.Diags {
		if w := gallupText(d.Why); okDiag[d.ID] && runes(w) >= 20 {
			out.Diags = append(out.Diags, struct {
				ID  string `json:"id"`
				Why string `json:"why"`
			}{d.ID, w})
		}
	}
	for _, f := range in.First {
		t, r := gallupText(f.Task), gallupText(f.Result)
		k := gallupKeyOf(f.Talent)
		if _, ok := rank[k]; !ok {
			k = ""
		}
		if runes(t) >= 15 && len(out.First) < 3 {
			out.First = append(out.First, struct {
				Task   string `json:"task"`
				Talent string `json:"talent"`
				Result string `json:"result"`
			}{t, k, r})
		}
	}
	var miss []string
	if runes(out.Intro) < 80 {
		miss = append(miss, "intro (3-4 предложения)")
	}
	if len(out.Mechanics) < 4 {
		miss = append(miss, "mechanics (cycle, tracking, five, library, tasks, report)")
	}
	if len(out.First) < 2 {
		miss = append(miss, "first (3 задачи)")
	}
	if len(miss) > 0 {
		return out, errors.New("не хватает: " + strings.Join(miss, "; "))
	}
	return out, nil
}

// gallupClubAsk: the light model writes the club part; asked once more
// with the reason when the answer does not pass.
func (h *PlatformAI) gallupClubAsk(ctx context.Context, order []string) (*gallupClubAI, error) {
	x := gallupPlusFor(order, nil)
	if x == nil {
		return nil, errors.New("мало талантов")
	}
	var ids []string
	for _, d := range x.Club.Diags {
		ids = append(ids, d.ID)
	}
	prompt := gallupClubPrompt(order, x)
	p := prompt
	var last *gallupClubAI
	var lastErr error
	for try := 0; try < 2; try++ {
		raw, err := h.AI.JSON(ai.Light(ctx), gallupSystem, p)
		if err != nil {
			lastErr = err
			if ctx.Err() != nil {
				break
			}
			continue
		}
		c, err := parseGallupClubAI(raw, order, ids)
		if err == nil {
			return c, nil
		}
		if c != nil && (c.Intro != "" || len(c.Mechanics) > 0) {
			last = c
		}
		lastErr = err
		p = prompt + "\n\nПредыдущий ответ не прошёл проверку: " + err.Error() + ". Верни полный ответ заново строго в формате JSON."
	}
	return last, lastErr
}

// POST /api/v1/platform/gallup/plus {order, club} → the rule-based second
// half of the analysis (R71), with the AI club lines the platform keeps in
// deep.club. No AI call here: it answers at once and works without AI.
func GallupPlus(c *gin.Context) {
	var req struct {
		Order []string        `json:"order"`
		Club  json.RawMessage `json:"club"`
	}
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 256<<10)
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "bad_request"})
		return
	}
	order := gallupOrderOf(req.Order)
	x := gallupPlusFor(order, gallupClubOf(req.Club, order))
	if x == nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "order"})
		return
	}
	c.Header("Cache-Control", "private, no-store")
	c.JSON(http.StatusOK, gin.H{"v": gallupDeepVer, "plus": x})
}

// gallupClubOf: the AI club part the platform sends back, checked again
// (it came through the browser).
func gallupClubOf(raw json.RawMessage, order []string) *gallupClubAI {
	if len(raw) < 3 || string(raw) == "null" {
		return nil
	}
	x := gallupPlusFor(order, nil)
	if x == nil {
		return nil
	}
	var ids []string
	for _, d := range x.Club.Diags {
		ids = append(ids, d.ID)
	}
	c, _ := parseGallupClubAI(string(raw), order, ids)
	if c == nil || (c.Intro == "" && len(c.Mechanics) == 0 && len(c.First) == 0) {
		return nil
	}
	return c
}

// gallupDocPlus: the PDF's Plus from the order the platform sends, or from
// the talent names of the document (an older page).
func gallupDocPlus(g *tplpdf.GallupDoc) {
	order := gallupOrderOf(g.Order)
	if len(order) < 3 {
		var names []string
		for _, t := range g.Talents {
			names = append(names, t.Name)
		}
		if len(names) < 3 {
			for _, t := range g.Top5 {
				names = append(names, t.Name)
			}
		}
		order = gallupOrderOf(names)
	}
	g.Plus = gallupPlusFor(order, gallupClubOf(g.ClubAI, order))
	g.Order, g.ClubAI = nil, nil
}
