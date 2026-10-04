package http

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/bnursik/business_surgery_backend/internal/ai"
	"github.com/gin-gonic/gin"
)

// POST /api/v1/platform/ai/health → organ scores for «Здоровье бизнеса»
// The board sends what it knows (diagnoses and tools with organs, task progress,
// the resident's own diagnostics, the platform's rule-based estimate); the model
// returns a 0..10 score per organ with a short reason:
//
//	{"scores":{"brain":7,...},"why":{"brain":["..."],...}}
//
// Errors come back as {"error": "<текст для человека>", "code": "..."}: the
// board then keeps its rule-based estimate and shows why.

// The seven organs of the body map, in the order of the board (ORG_MAP).
var healthOrgans = [][2]string{
	{"brain", "Стратегия"}, {"heart", "Маркетинг"}, {"hands", "Продажи"}, {"spine", "Команда"},
	{"blood", "Финансы"}, {"dna", "Процессы"}, {"eyes", "Аналитика"},
}

const (
	healthListMax = 120
	healthStrMax  = 200
)

type healthItem struct {
	Title string `json:"title"`
	Organ string `json:"organ"`
}

type healthReq struct {
	Board     string                    `json:"board"`
	Resident  string                    `json:"resident"`
	Diagnoses []healthItem              `json:"diagnoses"`
	Tools     []healthItem              `json:"tools"`
	Tasks     struct{ Total, Done int } `json:"tasks"`
	Tests     map[string]any            `json:"tests"`
	Local     map[string]float64        `json:"local"`
}

type healthResp struct {
	Scores map[string]int      `json:"scores"`
	Why    map[string][]string `json:"why"`
}

func healthCut(s string) string {
	s = strings.TrimSpace(s)
	if utf8.RuneCountInString(s) > healthStrMax {
		s = string([]rune(s)[:healthStrMax])
	}
	return s
}

func healthItems(in []healthItem) []healthItem {
	var out []healthItem
	for _, x := range in {
		if t := healthCut(x.Title); t != "" {
			out = append(out, healthItem{Title: t, Organ: healthCut(x.Organ)})
		}
		if len(out) >= healthListMax {
			break
		}
	}
	return out
}

const healthSystem = "Ты бизнес-трекер клуба предпринимателей Business Surgery. Оцениваешь здоровье бизнеса резидента по семи органам. " +
	"Пишешь по-русски, коротко и по делу. Без длинного тире, только обычный дефис. Отвечаешь строго JSON-объектом без пояснений."

func healthPrompt(r healthReq) string {
	var b strings.Builder
	b.WriteString("Оцени каждый орган бизнеса от 0 до 10 (0 - критично, 5 - средне, 10 - здоров). Органы (ключ: область):\n")
	for _, o := range healthOrgans {
		b.WriteString("- " + o[0] + ": " + o[1] + "\n")
	}
	b.WriteString("Диагнозы с других органов («Мышление», «Цели», «Энергия» - это стратегия; «Продукт» - маркетинг; «Окружение» - команда) учитывай там, куда они ведут.\n")
	b.WriteString("Каждый поставленный диагноз снижает оценку своего органа; назначенные инструменты и закрытые задачи её поднимают. " +
		"Если по органу сигналов нет, ставь 5-6 и так и пиши в причине. Ничего не придумывай сверх данных.\n\n")
	if r.Resident != "" {
		b.WriteString("Резидент: " + r.Resident + "\n")
	}
	list := func(title string, xs []healthItem) {
		b.WriteString(title + ":")
		if len(xs) == 0 {
			b.WriteString(" нет\n")
			return
		}
		b.WriteString("\n")
		for _, x := range xs {
			b.WriteString("- " + x.Title)
			if x.Organ != "" {
				b.WriteString(" (" + x.Organ + ")")
			}
			b.WriteString("\n")
		}
	}
	list("Диагнозы на доске", r.Diagnoses)
	list("Назначенные инструменты", r.Tools)
	fmt.Fprintf(&b, "Задачи цикла: закрыто %d из %d\n", r.Tasks.Done, r.Tasks.Total)
	if len(r.Tests) > 0 {
		t, _ := json.Marshal(r.Tests)
		b.WriteString("Самодиагностика резидента (0-10 по областям): " + string(t) + "\n")
	}
	if len(r.Local) > 0 {
		t, _ := json.Marshal(r.Local)
		b.WriteString("Оценка платформы по правилам (для ориентира): " + string(t) + "\n")
	}
	b.WriteString("\nФормат ответа: {\"scores\":{\"brain\":6,\"heart\":5,\"hands\":4,\"spine\":6,\"blood\":3,\"dna\":5,\"eyes\":6}," +
		"\"why\":{\"blood\":\"кассовые разрывы, нет платёжного календаря\"}}. В why по одной короткой причине на каждый орган.")
	return b.String()
}

// parseHealthAI reads the model's answer: only the seven organ keys, scores
// rounded and clamped to 0..10, reasons as a short list without long dashes.
func parseHealthAI(raw string) (*healthResp, error) {
	s := strings.TrimSpace(raw)
	if m := fenceRe.FindStringSubmatch(s); m != nil {
		s = m[1]
	}
	if i, j := strings.Index(s, "{"), strings.LastIndex(s, "}"); i >= 0 && j > i {
		s = s[i : j+1]
	}
	var in struct {
		Scores map[string]any `json:"scores"`
		Why    map[string]any `json:"why"`
	}
	if err := json.Unmarshal([]byte(s), &in); err != nil {
		return nil, err
	}
	out := &healthResp{Scores: map[string]int{}, Why: map[string][]string{}}
	for _, o := range healthOrgans {
		v, ok := healthNum(in.Scores[o[0]])
		if !ok {
			continue
		}
		out.Scores[o[0]] = int(math.Max(0, math.Min(10, math.Round(v))))
		var why []string
		switch w := in.Why[o[0]].(type) {
		case string:
			why = []string{w}
		case []any:
			for _, x := range w {
				if t, ok := x.(string); ok {
					why = append(why, t)
				}
			}
		}
		for _, t := range why {
			if t = healthCut(gallupClean(t)); t != "" && len(out.Why[o[0]]) < 2 {
				out.Why[o[0]] = append(out.Why[o[0]], t)
			}
		}
	}
	if len(out.Scores) == 0 {
		return nil, errors.New("в ответе нет оценок органов")
	}
	return out, nil
}

func healthNum(v any) (float64, bool) {
	switch x := v.(type) {
	case float64:
		return x, !math.IsNaN(x)
	case string:
		var f float64
		if _, err := fmt.Sscanf(strings.TrimSpace(x), "%g", &f); err == nil {
			return f, true
		}
	}
	return 0, false
}

func (h *PlatformAI) Health(c *gin.Context) {
	var req healthReq
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Не удалось прочитать данные доски", "code": "bad_request"})
		return
	}
	req.Resident = healthCut(req.Resident)
	req.Diagnoses = healthItems(req.Diagnoses)
	req.Tools = healthItems(req.Tools)
	if len(req.Tests) > 20 {
		req.Tests = nil
	}
	if len(req.Diagnoses) == 0 && len(req.Tools) == 0 && len(req.Tests) == 0 && req.Tasks.Total == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Мало данных: на доске нет диагнозов, инструментов и диагностики", "code": "empty"})
		return
	}
	if h.AI == nil || h.AI.Status()["text"] == "" {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": ai.ErrNoKey.Error(), "code": "no_ai"})
		return
	}
	ctx, cancel := context.WithTimeout(c.Request.Context(), 90*time.Second)
	defer cancel()
	raw, err := h.AI.JSON(ctx, healthSystem, healthPrompt(req))
	if err != nil {
		msg := "ИИ не ответил, попробуйте ещё раз"
		if ai.IsQuota(err) || errors.Is(err, ai.ErrNoKey) {
			msg = ai.UserMessage(err)
		}
		c.JSON(http.StatusBadGateway, gin.H{"error": msg, "code": "ai_failed", "detail": ai.UserMessage(err)})
		return
	}
	res, err := parseHealthAI(raw)
	if err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": "ИИ ответил в другом формате, попробуйте ещё раз", "code": "ai_parse", "detail": err.Error()})
		return
	}
	c.JSON(http.StatusOK, res)
}
