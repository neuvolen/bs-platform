package http

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/bnursik/business_surgery_backend/internal/ai"
	pg "github.com/bnursik/business_surgery_backend/internal/repository/pg"
	"github.com/gin-gonic/gin"
)

// POST /api/v1/platform/ai/gallup {text} → the full CliftonStrengths profile
// The text of a Gallup report (all 34 themes, English or Russian) becomes the
// ranked list of 34 talents with domains and, for each, its essence, how it
// shows in business, blind spots and how to use it in running the company;
// plus a business summary and roles/partners advice. The answer is cached by
// a hash of the text, so uploading the same report again is instant.

const gallupTextMax = 90000

type gallupTalent struct {
	Rank     int    `json:"rank"`
	Key      string `json:"key"`
	Name     string `json:"name"`
	Ru       string `json:"ru"`
	Domain   string `json:"domain"`   // executing | influencing | relationship | strategic
	DomainRu string `json:"domainRu"` // Исполнение | Влияние | Построение отношений | Стратегическое мышление
	Essence  string `json:"essence"`
	Business string `json:"business"`
	Blind    string `json:"blind"`
	Manage   string `json:"manage"`
}

type gallupSummary struct {
	Headline string   `json:"headline"`
	Business []string `json:"business"`
	Roles    []string `json:"roles"`
	Partners []string `json:"partners"`
	Avoid    []string `json:"avoid"`
}

type gallupProfile struct {
	V        int            `json:"v,omitempty"`
	Talents  []gallupTalent `json:"talents"`
	Summary  gallupSummary  `json:"summary"`
	Complete bool           `json:"complete"`
	Deep     *gallupDeep    `json:"deep,omitempty"` // R29, platform_gallup_deep.go
}

// The 34 themes: key, English name, Russian name, domain.
var gallupThemes = [][4]string{
	{"achiever", "Achiever", "Достигатор", "executing"},
	{"arranger", "Arranger", "Организатор", "executing"},
	{"belief", "Belief", "Убеждение", "executing"},
	{"consistency", "Consistency", "Справедливость", "executing"},
	{"deliberative", "Deliberative", "Осмотрительность", "executing"},
	{"discipline", "Discipline", "Дисциплинированность", "executing"},
	{"focus", "Focus", "Сосредоточенность", "executing"},
	{"responsibility", "Responsibility", "Ответственность", "executing"},
	{"restorative", "Restorative", "Восстановление", "executing"},
	{"activator", "Activator", "Активатор", "influencing"},
	{"command", "Command", "Распорядитель", "influencing"},
	{"communication", "Communication", "Коммуникация", "influencing"},
	{"competition", "Competition", "Конкуренция", "influencing"},
	{"maximizer", "Maximizer", "Максимизатор", "influencing"},
	{"self-assurance", "Self-Assurance", "Уверенность", "influencing"},
	{"significance", "Significance", "Значимость", "influencing"},
	{"woo", "Woo", "Обаяние", "influencing"},
	{"adaptability", "Adaptability", "Адаптивность", "relationship"},
	{"connectedness", "Connectedness", "Взаимосвязанность", "relationship"},
	{"developer", "Developer", "Развитие", "relationship"},
	{"empathy", "Empathy", "Эмпатия", "relationship"},
	{"harmony", "Harmony", "Гармония", "relationship"},
	{"includer", "Includer", "Включенность", "relationship"},
	{"individualization", "Individualization", "Индивидуализация", "relationship"},
	{"positivity", "Positivity", "Позитивность", "relationship"},
	{"relator", "Relator", "Отношения", "relationship"},
	{"analytical", "Analytical", "Аналитик", "strategic"},
	{"context", "Context", "Контекст", "strategic"},
	{"futuristic", "Futuristic", "Будущее", "strategic"},
	{"ideation", "Ideation", "Генератор идей", "strategic"},
	{"input", "Input", "Собиратель", "strategic"},
	{"intellection", "Intellection", "Мышление", "strategic"},
	{"learner", "Learner", "Ученик", "strategic"},
	{"strategic", "Strategic", "Стратегия", "strategic"},
}

var gallupDomainRu = map[string]string{
	"executing": "Исполнение", "influencing": "Влияние",
	"relationship": "Построение отношений", "strategic": "Стратегическое мышление",
}

// Other spellings seen in reports and translations.
var gallupAliases = map[string]string{
	"selfassurance": "self-assurance", "самоуверенность": "self-assurance",
	"достижение": "achiever", "достигатель": "achiever", "вера": "belief", "последовательность": "consistency",
	"дисциплина": "discipline", "фокус": "focus", "командование": "command", "командир": "command",
	"соревновательность": "competition", "конкурентность": "competition", "связанность": "connectedness",
	"развитиедругих": "developer", "позитив": "positivity", "близость": "relator", "футурист": "futuristic",
	"футуристичность": "futuristic", "идеация": "ideation", "сбор": "input", "интеллект": "intellection",
	"обучаемость": "learner", "учащийся": "learner", "стратег": "strategic", "стратегическоемышление": "strategic",
	"включённость": "includer", "очарование": "woo", "взаимосвязь": "connectedness", "индивидуальныйподход": "individualization",
}

var gallupNorm = regexp.MustCompile(`[^\p{L}]+`)

func gallupKeyOf(s string) string {
	k := strings.ReplaceAll(gallupNorm.ReplaceAllString(strings.ToLower(s), ""), "ё", "е")
	if k == "" {
		return ""
	}
	for _, t := range gallupThemes {
		for _, v := range []string{t[0], t[1], t[2]} {
			if strings.ReplaceAll(gallupNorm.ReplaceAllString(strings.ToLower(v), ""), "ё", "е") == k {
				return t[0]
			}
		}
	}
	for a, key := range gallupAliases {
		if strings.ReplaceAll(gallupNorm.ReplaceAllString(a, ""), "ё", "е") == k {
			return key
		}
	}
	return ""
}

func gallupTheme(key string) [4]string {
	for _, t := range gallupThemes {
		if t[0] == key {
			return t
		}
	}
	return [4]string{}
}

const gallupSystem = "Ты эксперт по CliftonStrengths (Gallup) и бизнес-трекер клуба предпринимателей Business Surgery. " +
	"Пишешь по-русски, коротко, конкретно, про управление компанией и командой. Без воды, без длинного тире, только обычный дефис. " +
	"Отвечаешь строго JSON-объектом без пояснений."

func gallupPrompt(text string) string {
	var names []string
	for _, t := range gallupThemes {
		names = append(names, t[1]+" ("+t[2]+", "+t[3]+")")
	}
	return "Ниже текст отчёта Gallup CliftonStrengths предпринимателя. Найди порядок ВСЕХ 34 талантов (место 1 - самый сильный, 34 - самый слабый). " +
		"Порядок бери только из отчёта, ничего не придумывай. Если в отчёте меньше 34 тем, верни только те, что есть.\n\n" +
		"Список 34 тем (английское имя, русское имя, домен): " + strings.Join(names, "; ") + ".\n" +
		"Домены: executing = Исполнение, influencing = Влияние, relationship = Построение отношений, strategic = Стратегическое мышление.\n\n" +
		"Для КАЖДОГО таланта напиши применительно к собственнику бизнеса, учитывая его место в профиле (топ-10 - опора, 11-24 - по ситуации, 25-34 - зона, которую закрывают другие люди):\n" +
		"- essence: суть в одном предложении;\n- business: как проявляется в бизнесе (1-2 предложения);\n" +
		"- blind: слепые зоны и риски (1 предложение);\n- manage: как использовать в управлении компанией и командой (1-2 предложения, конкретное действие).\n\n" +
		"Затем summary: headline (одна фраза: какой это предприниматель), business (3-5 пунктов «что это значит для вашего бизнеса»), " +
		"roles (3-4 роли, в которых собственник сильнее всего), partners (3-4 типа людей или партнёров, которых нужно взять рядом, с талантами), avoid (2-3 задачи, которые лучше делегировать).\n\n" +
		"Формат ответа:\n{\"talents\":[{\"rank\":1,\"name\":\"Strategic\",\"ru\":\"Стратегия\",\"domain\":\"strategic\",\"essence\":\"...\",\"business\":\"...\",\"blind\":\"...\",\"manage\":\"...\"}],\n" +
		"\"summary\":{\"headline\":\"...\",\"business\":[\"...\"],\"roles\":[\"...\"],\"partners\":[\"...\"],\"avoid\":[\"...\"]}}\n\n" +
		"Текст отчёта:\n" + text
}

var fenceRe = regexp.MustCompile("(?s)```(?:json)?\\s*(.*?)```")

// parseGallupAI reads the model's answer: talents in order, deduplicated,
// names and domains taken from the canonical list.
func parseGallupAI(raw string) (*gallupProfile, error) {
	s := strings.TrimSpace(raw)
	if m := fenceRe.FindStringSubmatch(s); m != nil {
		s = m[1]
	}
	if i, j := strings.Index(s, "{"), strings.LastIndex(s, "}"); i >= 0 && j > i {
		s = s[i : j+1]
	}
	var in struct {
		Talents []struct {
			Rank     any    `json:"rank"`
			Key      string `json:"key"`
			Name     string `json:"name"`
			Ru       string `json:"ru"`
			Essence  string `json:"essence"`
			Business string `json:"business"`
			Blind    string `json:"blind"`
			Manage   string `json:"manage"`
		} `json:"talents"`
		Summary gallupSummary `json:"summary"`
	}
	if err := json.Unmarshal([]byte(s), &in); err != nil {
		return nil, err
	}
	out := &gallupProfile{Summary: in.Summary}
	seen := map[string]bool{}
	for _, t := range in.Talents {
		key := gallupKeyOf(t.Key)
		if key == "" {
			key = gallupKeyOf(t.Name)
		}
		if key == "" {
			key = gallupKeyOf(t.Ru)
		}
		if key == "" || seen[key] {
			continue
		}
		seen[key] = true
		th := gallupTheme(key)
		out.Talents = append(out.Talents, gallupTalent{
			Rank: len(out.Talents) + 1, Key: key, Name: th[1], Ru: th[2], Domain: th[3], DomainRu: gallupDomainRu[th[3]],
			Essence: gallupClean(t.Essence), Business: gallupClean(t.Business), Blind: gallupClean(t.Blind), Manage: gallupClean(t.Manage),
		})
	}
	if len(out.Talents) == 0 {
		return nil, errors.New("в ответе нет талантов")
	}
	out.Complete = len(out.Talents) == len(gallupThemes)
	out.Summary.Headline = gallupClean(out.Summary.Headline)
	for _, l := range []*[]string{&out.Summary.Business, &out.Summary.Roles, &out.Summary.Partners, &out.Summary.Avoid} {
		var keep []string
		for _, x := range *l {
			if x = gallupClean(x); x != "" {
				keep = append(keep, x)
			}
		}
		*l = keep
	}
	return out, nil
}

// gallupClean: brand rule, no long dashes.
func gallupClean(s string) string {
	s = strings.NewReplacer(" — ", " - ", "—", "-", " – ", " - ", "–", "-").Replace(strings.TrimSpace(s))
	return s
}

func (h *PlatformAI) Gallup(c *gin.Context) {
	var req struct {
		Text  string   `json:"text"`
		Order []string `json:"order"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "bad_request"})
		return
	}
	// The AI answer takes longer than the server's 60 s write timeout.
	longBody(c)
	if strings.TrimSpace(req.Text) == "" && len(req.Order) > 0 {
		h.gallupDeepOnly(c, req.Order)
		return
	}
	text := strings.TrimSpace(req.Text)
	if utf8.RuneCountInString(text) < 40 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "empty"})
		return
	}
	if len(text) > gallupTextMax {
		text = text[:gallupTextMax]
		for !utf8.ValidString(text) {
			text = text[:len(text)-1]
		}
	}
	sum := sha256.Sum256([]byte(text))
	// gal3_ (R68): answers made while the order could come from the report's
	// common text are not reused (gal2_ R29, gal_ the talents only)
	key := "gal3_" + hex.EncodeToString(sum[:])[:48]
	ctx := c.Request.Context()
	if f, err := h.repo.GetFile(ctx, key); err == nil && f != nil {
		c.Header("X-Gallup-Cache", "hit")
		c.Data(http.StatusOK, "application/json; charset=utf-8", f.Data)
		return
	}
	if h.AI == nil || !h.AI.HasText() {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "no_ai"})
		return
	}
	actx, cancel := context.WithTimeout(ctx, 300*time.Second)
	defer cancel()
	// A full ranked list in the text: the deep analysis starts at once, in
	// parallel with the talents; otherwise it waits for the model's order.
	scan := gallupScanOrder(text)
	type deepRes struct {
		d     *gallupDeep
		err   error
		order []string
	}
	var early chan deepRes
	if len(scan) == len(gallupThemes) {
		early = make(chan deepRes, 1)
		go func() {
			d, err := h.gallupDeepFor(actx, scan)
			early <- deepRes{d, err, scan}
		}()
	}
	var p *gallupProfile
	var err error
	prompt := gallupPrompt(text)
	for try := 0; try < 2; try++ {
		var raw string
		raw, err = h.AI.JSON(actx, gallupSystem, prompt)
		if err != nil {
			if actx.Err() != nil {
				break
			}
			continue
		}
		if p, err = parseGallupAI(raw); err == nil {
			break
		}
		prompt = gallupPrompt(text) + "\n\nПредыдущий ответ не прошёл проверку: " + err.Error() + ". Верни полный ответ заново строго в формате JSON."
	}
	if p == nil {
		code := "ai_failed"
		if err != nil && actx.Err() == nil && !isAIHTTP(err) {
			code = "ai_parse"
		}
		c.JSON(http.StatusBadGateway, gin.H{"error": code, "detail": errText(err), "message": GallupUnrecognized})
		return
	}
	// R68: the numbered places of the report win over the model's reading
	if len(scan) == len(gallupThemes) {
		p.Talents, p.Complete = gallupReorder(p.Talents, scan), true
	}
	order := make([]string, len(p.Talents))
	for i, t := range p.Talents {
		order[i] = t.Key
	}
	var dr deepRes
	if early != nil {
		dr = <-early
	}
	if early == nil || !sameOrder(dr.order, order) {
		d, err := h.gallupDeepFor(actx, order)
		dr = deepRes{d, err, order}
	}
	p.V = 2
	p.Deep = dr.d
	b, _ := json.Marshal(p)
	if p.Complete && p.Deep != nil && !p.Deep.Partial {
		_ = h.repo.PutFile(context.Background(), pg.PlatformFile{ID: key, Name: "gallup.json", Mime: "application/json", Data: b}, platformUser(c))
	}
	c.Data(http.StatusOK, "application/json; charset=utf-8", b)
}

// gallupDeepOnly: POST {order:[keys]} - the deep analysis for a profile
// saved before R29 (its order is known, the report text is not kept).
func (h *PlatformAI) gallupDeepOnly(c *gin.Context, in []string) {
	var order []string
	seen := map[string]bool{}
	for _, x := range in {
		if k := gallupKeyOf(x); k != "" && !seen[k] {
			seen[k] = true
			order = append(order, k)
		}
	}
	if len(order) < 5 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "order"})
		return
	}
	sum := sha256.Sum256([]byte(strings.Join(order, ",")))
	key := "gal2o_" + hex.EncodeToString(sum[:])[:46]
	ctx := c.Request.Context()
	if f, err := h.repo.GetFile(ctx, key); err == nil && f != nil {
		c.Header("X-Gallup-Cache", "hit")
		c.Data(http.StatusOK, "application/json; charset=utf-8", f.Data)
		return
	}
	if h.AI == nil || !h.AI.HasText() {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "no_ai"})
		return
	}
	actx, cancel := context.WithTimeout(ctx, 240*time.Second)
	defer cancel()
	d, err := h.gallupDeepFor(actx, order)
	if d == nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": "ai_failed", "detail": errText(err)})
		return
	}
	b, _ := json.Marshal(gin.H{"v": 2, "order": order, "deep": d})
	if !d.Partial {
		_ = h.repo.PutFile(context.Background(), pg.PlatformFile{ID: key, Name: "gallup_deep.json", Mime: "application/json", Data: b}, platformUser(c))
	}
	c.Data(http.StatusOK, "application/json; charset=utf-8", b)
}

// gallupReorder puts the talents in the report's own numbered order.
func gallupReorder(in []gallupTalent, order []string) []gallupTalent {
	by := map[string]gallupTalent{}
	for _, t := range in {
		by[t.Key] = t
	}
	out := make([]gallupTalent, 0, len(order))
	for i, k := range order {
		t, ok := by[k]
		if !ok {
			th := gallupTheme(k)
			t = gallupTalent{Key: k, Name: th[1], Ru: th[2], Domain: th[3], DomainRu: gallupDomainRu[th[3]]}
		}
		t.Rank = i + 1
		out = append(out, t)
	}
	return out
}

func errText(err error) string {
	if err == nil {
		return ""
	}
	return ai.UserMessage(err)
}

func isAIHTTP(err error) bool {
	var he *ai.HTTPError
	return errors.As(err, &he)
}
