package http

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"unicode/utf8"
)

// R29: the deep part of the Gallup analysis. The ranked list of talents is
// not enough: what matters for an owner is how talents work together, how a
// weak talent «подвешивает» strong ones, what happens under stress and what to
// do with it in the business. The deep analysis needs only the order of the
// themes, so it is asked in two parallel calls (portrait and interactions;
// business, risks and plan) and can be made later for a profile saved before
// R29 (POST /ai/gallup {order}).

type gallupLine struct {
	Key  string `json:"key"`
	Line string `json:"line"`
}

type gallupPortrait struct {
	Headline string       `json:"headline"`
	Plain    string       `json:"plain"`
	Top5     []gallupLine `json:"top5"`
	Top10    string       `json:"top10"`
}

// gallupLink: talents that amplify each other or conflict.
type gallupLink struct {
	Keys   []string `json:"keys"`
	Title  string   `json:"title"`
	Effect string   `json:"effect"`
	Scene  string   `json:"scene"`
	Fix    string   `json:"fix,omitempty"`
}

// gallupAnchor: a weak talent that drags strong ones.
type gallupAnchor struct {
	Weak   string   `json:"weak"`
	Strong []string `json:"strong"`
	Title  string   `json:"title"`
	Effect string   `json:"effect"`
	Scene  string   `json:"scene"`
	Fix    string   `json:"fix"`
}

type gallupBlind struct {
	Title string `json:"title"`
	Text  string `json:"text"`
	Check string `json:"check"`
}

type gallupHire struct {
	Role    string   `json:"role"`
	Talents []string `json:"talents"`
	Why     string   `json:"why"`
}

type gallupArea struct {
	Area   string `json:"area"` // sales | negotiate | team | decisions | money
	How    string `json:"how"`
	Action string `json:"action"`
}

type gallupRisk struct {
	Title   string `json:"title"`
	Trigger string `json:"trigger"`
	Signs   string `json:"signs"`
	Rule    string `json:"rule"`
}

type gallupStep struct {
	When   string `json:"when"`
	Action string `json:"action"`
	Result string `json:"result"`
}

type gallupDeep struct {
	V        int            `json:"v"`
	Portrait gallupPortrait `json:"portrait"`
	Amplify  []gallupLink   `json:"amplify"`
	Conflict []gallupLink   `json:"conflict"`
	Anchors  []gallupAnchor `json:"anchors"`
	Best     []string       `json:"best"`
	Stress   []string       `json:"stress"`
	Reset    []string       `json:"reset"`
	Blind    []gallupBlind  `json:"blind"`
	Keep     []string       `json:"keep"`
	Delegate []string       `json:"delegate"`
	Hires    []gallupHire   `json:"hires"`
	Areas    []gallupArea   `json:"areas"`
	Risks    []gallupRisk   `json:"risks"`
	Plan     []gallupStep   `json:"plan"`
	WorkWith []string       `json:"workWithMe"`
	Partial  bool           `json:"partial,omitempty"`
}

var gallupAreaKeys = []string{"sales", "negotiate", "team", "decisions", "money"}

// gallupOrderLine: «1. Strategic (Стратегия, strategic)» lines for the prompts.
func gallupOrderLine(order []string) string {
	var b strings.Builder
	for i, k := range order {
		th := gallupTheme(k)
		fmt.Fprintf(&b, "%d. %s / %s / ключ %s / домен %s\n", i+1, th[1], th[2], k, gallupDomainRu[th[3]])
	}
	return b.String()
}

func gallupTiers(n int) string {
	if n >= 30 {
		return "Места 1-10 - опора, 11-24 - по ситуации, 25-34 - слабые таланты (якоря)."
	}
	return fmt.Sprintf("В отчёте только %d первых тем: остальные считай неизвестными и не делай выводов о слабых талантах.", n)
}

const gallupStyle = "Правила текста:\n" +
	"- По-русски, простыми словами, как опытный трекер говорит собственнику один на один. Обращение на «вы».\n" +
	"- Только конкретика: поведение, решение, фраза, ситуация, цифра. Запрещены общие слова и гороскоп: «уникальный», «гармоничная личность», «раскрыть потенциал», «вы прирождённый лидер», «энергия», «вселенная», «гармония с собой».\n" +
	"- Сцены из жизни предпринимателя в Казахстане (Алматы, Астана, Шымкент): переговоры с крупным поставщиком, партнёр зарабатывает больше, банк, акимат, тендер, найм РОПа, кассовый разрыв, клиент давит на скидку, семейный бизнес, сотрудник-родственник. Суммы в тенге с пробелами: 10 000 000 ₸.\n" +
	"- Сцена: 1-3 предложения, что именно происходит и что человек делает или не делает. Пример: «На встрече с дистрибьютором, у которого оборот в 20 раз больше, вы заранее знаете, что условия плохие, но киваете и подписываете, а злость выливаете потом на команду».\n" +
	"- Никакого длинного тире, только обычный дефис. Никаких эмодзи. Таланты называй по-русски в кавычках-ёлочках.\n" +
	"- Ссылки на таланты в полях keys, weak, strong, talents и key только ключами из списка (например self-assurance).\n"

func gallupDeepPromptA(order []string) string {
	n := len(order)
	return "Профиль CliftonStrengths собственника бизнеса, порядок от сильного к слабому:\n" + gallupOrderLine(order) + "\n" + gallupTiers(n) + "\n\n" + gallupStyle + "\n" +
		"Сделай первую часть разбора.\n" +
		"1. portrait: headline (одна фраза: какой это предприниматель, без комплиментов), plain (3-4 предложения: как он думает, решает и действует, простыми словами), " +
		"top5 (для каждой из первых пяти тем key и line: одно предложение, как именно эта тема видна в его работе), top10 (2-3 предложения: как места 6-10 поддерживают или меняют пятёрку).\n" +
		"2. amplify: 3-5 связок из 2-3 талантов из первых 12 мест, которые усиливают друг друга. keys, title (короткое имя связки), effect (что даёт вместе, 1-2 предложения), scene (сцена из жизни), fix (как использовать сознательно).\n" +
		"3. conflict: 2-3 пары сильных талантов из первых 15 мест, которые тянут в разные стороны. keys (ровно 2), title, effect, scene, fix (правило, как не застревать).\n" +
		"4. anchors: 2-4 случая, когда слабый талант (места 25-34) подвешивает сильные (места 1-10): weak (ключ слабого), strong (1-3 ключа сильных), title, effect (насколько сильно и в какой момент сильные таланты перестают работать), scene, fix (что делать или кого поставить рядом). " +
		"Пример логики: низкая «Уверенность» при высоких «Конкуренции» и «Распорядителе» - сильный напор, но перед тем, кто богаче и влиятельнее, человек прогибается и молчит.\n" +
		"5. best: 3-4 признака «вы в лучшей форме» (конкретное поведение); stress: 3-4 признака «вы под стрессом» (как сильные стороны переходят в перегиб); reset: 2-3 действия, которые быстро возвращают в форму.\n" +
		"6. blind: 3-4 слепые зоны: title, text (что вы не замечаете и чем это стоит бизнесу), check (как проверить на этой неделе или кого спросить).\n\n" +
		"Ответ строго JSON:\n" +
		`{"portrait":{"headline":"","plain":"","top5":[{"key":"","line":""}],"top10":""},` +
		`"amplify":[{"keys":["",""],"title":"","effect":"","scene":"","fix":""}],` +
		`"conflict":[{"keys":["",""],"title":"","effect":"","scene":"","fix":""}],` +
		`"anchors":[{"weak":"","strong":[""],"title":"","effect":"","scene":"","fix":""}],` +
		`"best":[""],"stress":[""],"reset":[""],"blind":[{"title":"","text":"","check":""}]}`
}

func gallupDeepPromptB(order []string) string {
	n := len(order)
	return "Профиль CliftonStrengths собственника бизнеса, порядок от сильного к слабому:\n" + gallupOrderLine(order) + "\n" + gallupTiers(n) + "\n\n" + gallupStyle + "\n" +
		"Сделай вторую часть разбора: как применять профиль в бизнесе.\n" +
		"1. keep: 3-4 задачи, которые собственник с таким профилем должен оставить себе (конкретно, с опорой на таланты). delegate: 3-4 задачи, которые нужно отдать, и кому.\n" +
		"2. hires: 2-3 человека, которые дополняют профиль: role (должность или роль), talents (2-3 ключа талантов, которые у него должны быть в топе), why (что он закроет у собственника).\n" +
		"3. areas: ровно 5 пунктов с area = sales, negotiate, team, decisions, money (как вы продаёте; как ведёте переговоры; как управляете командой; как принимаете решения; деньги и финансовые привычки). " +
		"how: 2 предложения, как это происходит у этого профиля, с сильной стороной и ловушкой; action: одно конкретное действие или правило, которое можно начать завтра.\n" +
		"4. risks: 3-5 рисков для бизнеса из сочетания талантов: title, trigger (в какой ситуации включается), signs (ранние признаки, которые видно по цифрам или поведению), rule (одно правило профилактики).\n" +
		"5. plan: 3-5 действий на 30 дней: when (например «Неделя 1»), action (что сделать, измеримо), result (что должно получиться к концу срока).\n" +
		"6. workWithMe: 4-6 коротких правил для партнёров и команды «как работать со мной»: как приносить вопросы, как спорить, чего не делать, как сообщать плохие новости.\n\n" +
		"Ответ строго JSON:\n" +
		`{"keep":[""],"delegate":[""],"hires":[{"role":"","talents":[""],"why":""}],` +
		`"areas":[{"area":"sales","how":"","action":""}],"risks":[{"title":"","trigger":"","signs":"","rule":""}],` +
		`"plan":[{"when":"","action":"","result":""}],"workWithMe":[""]}`
}

// Brand rules for every string of the answer: no long dash, «10 000».
var gallupBigNum = regexp.MustCompile(`(^|[^\d.,])(\d{5,})`)

func gallupText(s string) string {
	s = gallupClean(s)
	s = gallupBigNum.ReplaceAllStringFunc(s, func(m string) string {
		i := 0
		for i < len(m) && (m[i] < '0' || m[i] > '9') {
			i++
		}
		pre, d := m[:i], m[i:]
		var out []byte
		for j := range d {
			if j > 0 && (len(d)-j)%3 == 0 {
				out = append(out, ' ')
			}
			out = append(out, d[j])
		}
		return pre + string(out)
	})
	return strings.Join(strings.Fields(s), " ")
}

func gallupTexts(in []string, max int) []string {
	var out []string
	for _, x := range in {
		if x = gallupText(x); x != "" && len(out) < max {
			out = append(out, x)
		}
	}
	return out
}

func gallupJSONObject(raw string) string {
	s := strings.TrimSpace(raw)
	if m := fenceRe.FindStringSubmatch(s); m != nil {
		s = m[1]
	}
	if i, j := strings.Index(s, "{"), strings.LastIndex(s, "}"); i >= 0 && j > i {
		s = s[i : j+1]
	}
	return s
}

func runes(s string) int { return utf8.RuneCountInString(s) }

// gallupKeys resolves a list of talent names to keys that are in the
// profile, in the allowed rank window, without repeats.
func gallupKeys(in []string, rank map[string]int, lo, hi int) []string {
	var out []string
	seen := map[string]bool{}
	for _, x := range in {
		k := gallupKeyOf(x)
		r, ok := rank[k]
		if k == "" || !ok || seen[k] || r < lo || r > hi {
			continue
		}
		seen[k] = true
		out = append(out, k)
	}
	return out
}

func gallupRanks(order []string) map[string]int {
	m := map[string]int{}
	for i, k := range order {
		m[k] = i + 1
	}
	return m
}

// parseGallupDeepA validates the first part. Items with wrong talents or a
// missing scene are dropped; too few items left is an error (the call is
// asked again).
func parseGallupDeepA(raw string, order []string) (*gallupDeep, error) {
	var in gallupDeep
	if err := json.Unmarshal([]byte(gallupJSONObject(raw)), &in); err != nil {
		return nil, fmt.Errorf("JSON не читается: %v", err)
	}
	n, rank := len(order), gallupRanks(order)
	d := &gallupDeep{V: 2}
	p := in.Portrait
	d.Portrait = gallupPortrait{Headline: gallupText(p.Headline), Plain: gallupText(p.Plain), Top10: gallupText(p.Top10)}
	for _, l := range p.Top5 {
		k := gallupKeys([]string{l.Key}, rank, 1, 5)
		if len(k) == 1 && gallupText(l.Line) != "" {
			d.Portrait.Top5 = append(d.Portrait.Top5, gallupLine{Key: k[0], Line: gallupText(l.Line)})
		}
	}
	link := func(src []gallupLink, hi, minK, maxK, max int) []gallupLink {
		var out []gallupLink
		for _, l := range src {
			ks := gallupKeys(l.Keys, rank, 1, hi)
			l.Title, l.Effect, l.Scene, l.Fix = gallupText(l.Title), gallupText(l.Effect), gallupText(l.Scene), gallupText(l.Fix)
			if len(ks) < minK || len(ks) > maxK || l.Title == "" || l.Effect == "" || runes(l.Scene) < 50 || len(out) >= max {
				continue
			}
			l.Keys = ks
			out = append(out, l)
		}
		return out
	}
	d.Amplify = link(in.Amplify, 12, 2, 3, 5)
	d.Conflict = link(in.Conflict, 15, 2, 2, 3)
	for _, a := range in.Anchors {
		w := gallupKeys([]string{a.Weak}, rank, 25, 34)
		st := gallupKeys(a.Strong, rank, 1, 10)
		a.Title, a.Effect, a.Scene, a.Fix = gallupText(a.Title), gallupText(a.Effect), gallupText(a.Scene), gallupText(a.Fix)
		if len(w) != 1 || len(st) == 0 || a.Title == "" || a.Effect == "" || a.Fix == "" || runes(a.Scene) < 50 || len(d.Anchors) >= 4 {
			continue
		}
		if len(st) > 3 {
			st = st[:3]
		}
		a.Weak, a.Strong = w[0], st
		d.Anchors = append(d.Anchors, a)
	}
	d.Best, d.Stress, d.Reset = gallupTexts(in.Best, 4), gallupTexts(in.Stress, 4), gallupTexts(in.Reset, 3)
	for _, b := range in.Blind {
		b = gallupBlind{Title: gallupText(b.Title), Text: gallupText(b.Text), Check: gallupText(b.Check)}
		if b.Title != "" && b.Text != "" && len(d.Blind) < 4 {
			d.Blind = append(d.Blind, b)
		}
	}
	var miss []string
	if d.Portrait.Headline == "" || runes(d.Portrait.Plain) < 80 {
		miss = append(miss, "portrait")
	}
	if len(d.Portrait.Top5) < min(3, n) {
		miss = append(miss, "portrait.top5")
	}
	if n >= 3 && len(d.Amplify) < 2 {
		miss = append(miss, "amplify (нужно 3-5 связок с ключами из первых 12 мест и сценой)")
	}
	if n >= 6 && len(d.Conflict) < 1 {
		miss = append(miss, "conflict (нужны пары из первых 15 мест со сценой)")
	}
	if n >= 30 && len(d.Anchors) < 2 {
		miss = append(miss, "anchors (weak - ключ с места 25-34, strong - ключи с мест 1-10, сцена и fix)")
	}
	if len(d.Best) < 2 || len(d.Stress) < 2 {
		miss = append(miss, "best/stress")
	}
	if len(d.Blind) < 2 {
		miss = append(miss, "blind")
	}
	if len(miss) > 0 {
		return d, errors.New("не хватает: " + strings.Join(miss, "; "))
	}
	return d, nil
}

func parseGallupDeepB(raw string, _ []string) (*gallupDeep, error) {
	var in gallupDeep
	if err := json.Unmarshal([]byte(gallupJSONObject(raw)), &in); err != nil {
		return nil, fmt.Errorf("JSON не читается: %v", err)
	}
	d := &gallupDeep{V: 2, Keep: gallupTexts(in.Keep, 4), Delegate: gallupTexts(in.Delegate, 4), WorkWith: gallupTexts(in.WorkWith, 6)}
	for _, h := range in.Hires {
		var ks []string
		seen := map[string]bool{}
		for _, t := range h.Talents { // the hire's talents may be anything, not only the owner's
			if k := gallupKeyOf(t); k != "" && !seen[k] && len(ks) < 3 {
				seen[k] = true
				ks = append(ks, k)
			}
		}
		h = gallupHire{Role: gallupText(h.Role), Talents: ks, Why: gallupText(h.Why)}
		if h.Role != "" && h.Why != "" && len(d.Hires) < 3 {
			d.Hires = append(d.Hires, h)
		}
	}
	byArea := map[string]gallupArea{}
	for _, a := range in.Areas {
		k := strings.ToLower(strings.TrimSpace(a.Area))
		a = gallupArea{Area: k, How: gallupText(a.How), Action: gallupText(a.Action)}
		if a.How != "" && a.Action != "" {
			if _, dup := byArea[k]; !dup {
				byArea[k] = a
			}
		}
	}
	for _, k := range gallupAreaKeys {
		if a, ok := byArea[k]; ok {
			d.Areas = append(d.Areas, a)
		}
	}
	for _, r := range in.Risks {
		r = gallupRisk{Title: gallupText(r.Title), Trigger: gallupText(r.Trigger), Signs: gallupText(r.Signs), Rule: gallupText(r.Rule)}
		if r.Title != "" && r.Trigger != "" && r.Signs != "" && r.Rule != "" && len(d.Risks) < 5 {
			d.Risks = append(d.Risks, r)
		}
	}
	for _, s := range in.Plan {
		s = gallupStep{When: gallupText(s.When), Action: gallupText(s.Action), Result: gallupText(s.Result)}
		if s.Action != "" && len(d.Plan) < 5 {
			d.Plan = append(d.Plan, s)
		}
	}
	var miss []string
	if len(d.Keep) < 2 || len(d.Delegate) < 2 {
		miss = append(miss, "keep/delegate")
	}
	if len(d.Hires) < 1 {
		miss = append(miss, "hires")
	}
	if len(d.Areas) < len(gallupAreaKeys) {
		miss = append(miss, "areas (ровно 5: sales, negotiate, team, decisions, money)")
	}
	if len(d.Risks) < 3 {
		miss = append(miss, "risks (3-5, у каждого trigger, signs, rule)")
	}
	if len(d.Plan) < 3 {
		miss = append(miss, "plan (3-5 действий)")
	}
	if len(d.WorkWith) < 3 {
		miss = append(miss, "workWithMe")
	}
	if len(miss) > 0 {
		return d, errors.New("не хватает: " + strings.Join(miss, "; "))
	}
	return d, nil
}

// gallupAsk calls the model and validates; a bad answer is asked once more
// with the reason. The last result is returned even when it is incomplete.
func (h *PlatformAI) gallupAsk(ctx context.Context, prompt string, parse func(string) (*gallupDeep, error)) (*gallupDeep, error) {
	var last *gallupDeep
	var lastErr error
	p := prompt
	for try := 0; try < 2; try++ {
		raw, err := h.AI.JSON(ctx, gallupSystem, p)
		if err != nil {
			lastErr = err
			if ctx.Err() != nil {
				break
			}
			continue
		}
		d, err := parse(raw)
		if err == nil {
			return d, nil
		}
		if d != nil {
			last = d
		}
		lastErr = err
		p = prompt + "\n\nПредыдущий ответ не прошёл проверку: " + err.Error() + ". Верни полный ответ заново строго в формате JSON."
	}
	return last, lastErr
}

// gallupDeepFor makes both parts in parallel and merges them. partial says
// that a part did not pass the check even after a retry.
func (h *PlatformAI) gallupDeepFor(ctx context.Context, order []string) (*gallupDeep, error) {
	type res struct {
		d   *gallupDeep
		err error
	}
	ca, cb := make(chan res, 1), make(chan res, 1)
	go func() {
		d, err := h.gallupAsk(ctx, gallupDeepPromptA(order), func(s string) (*gallupDeep, error) { return parseGallupDeepA(s, order) })
		ca <- res{d, err}
	}()
	go func() {
		d, err := h.gallupAsk(ctx, gallupDeepPromptB(order), func(s string) (*gallupDeep, error) { return parseGallupDeepB(s, order) })
		cb <- res{d, err}
	}()
	a, b := <-ca, <-cb
	if a.d == nil && b.d == nil {
		if a.err != nil {
			return nil, a.err
		}
		return nil, b.err
	}
	out := &gallupDeep{V: 2, Partial: a.err != nil || b.err != nil}
	if a.d != nil {
		out.Portrait, out.Amplify, out.Conflict, out.Anchors = a.d.Portrait, a.d.Amplify, a.d.Conflict, a.d.Anchors
		out.Best, out.Stress, out.Reset, out.Blind = a.d.Best, a.d.Stress, a.d.Reset, a.d.Blind
	}
	if b.d != nil {
		out.Keep, out.Delegate, out.Hires, out.Areas = b.d.Keep, b.d.Delegate, b.d.Hires, b.d.Areas
		out.Risks, out.Plan, out.WorkWith = b.d.Risks, b.d.Plan, b.d.WorkWith
	}
	return out, nil
}

// gallupScanOrder: the order of the themes as they first appear in the text
// of a report (the ranked list comes first in a CliftonStrengths 34 report).
// Used only to start the deep analysis in parallel with the talent call; the
// order from the model wins.
func gallupScanOrder(text string) []string {
	low := strings.ReplaceAll(strings.ToLower(text), "ё", "е")
	type hit struct {
		k   string
		pos int
	}
	var hits []hit
	for _, th := range gallupThemes {
		cands := []string{th[1], th[2]}
		for a, k := range gallupAliases {
			if k == th[0] && !strings.ContainsAny(a, " ") {
				cands = append(cands, a)
			}
		}
		best := -1
		for _, c := range cands {
			c = strings.ReplaceAll(strings.ToLower(c), "ё", "е")
			pat := regexp.QuoteMeta(c)
			pat = strings.ReplaceAll(pat, `\-`, `[- ]?`)
			pat = strings.ReplaceAll(pat, "-", `[- ]?`)
			re := regexp.MustCompile(`(?:^|[^\p{L}])(` + pat + `)(?:[^\p{L}]|$)`)
			if m := re.FindStringSubmatchIndex(low); m != nil && (best < 0 || m[2] < best) {
				best = m[2]
			}
		}
		if best >= 0 {
			hits = append(hits, hit{th[0], best})
		}
	}
	sort.SliceStable(hits, func(i, j int) bool { return hits[i].pos < hits[j].pos })
	out := make([]string, len(hits))
	for i, h := range hits {
		out[i] = h.k
	}
	return out
}

func sameOrder(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
