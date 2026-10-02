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
	Talents  []gallupTalent `json:"talents"`
	Summary  gallupSummary  `json:"summary"`
	Complete bool           `json:"complete"`
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
		Text string `json:"text"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "bad_request"})
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
	key := "gal_" + hex.EncodeToString(sum[:])[:48]
	ctx := c.Request.Context()
	if f, err := h.repo.GetFile(ctx, key); err == nil && f != nil {
		c.Header("X-Gallup-Cache", "hit")
		c.Data(http.StatusOK, "application/json; charset=utf-8", f.Data)
		return
	}
	if h.AI == nil || (h.AI.Gemini == "" && h.AI.Anthropic == "" && h.AI.OpenAI == "") {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "no_ai"})
		return
	}
	actx, cancel := context.WithTimeout(ctx, 170*time.Second)
	defer cancel()
	raw, err := h.AI.JSON(actx, gallupSystem, gallupPrompt(text))
	if err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": "ai_failed", "detail": err.Error()})
		return
	}
	p, err := parseGallupAI(raw)
	if err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": "ai_parse", "detail": err.Error()})
		return
	}
	b, _ := json.Marshal(p)
	if p.Complete {
		_ = h.repo.PutFile(context.Background(), pg.PlatformFile{ID: key, Name: "gallup.json", Mime: "application/json", Data: b}, platformUser(c))
	}
	c.Data(http.StatusOK, "application/json; charset=utf-8", b)
}
