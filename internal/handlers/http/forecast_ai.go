package http

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/bnursik/business_surgery_backend/internal/ai"
	"github.com/gin-gonic/gin"
)

// R48: «Учёт» → «Прогноз» → «Что если…» с ИИ.
// POST /api/v1/platform/ai/forecast {text, start:"2026-11", expLines:[…], horizon}
// The page parses common phrases itself (BSFC.parse in platform.html); this
// endpoint is for the sentences the rules did not understand. The model
// answers {"items":[…],"rest":[…]}; every item goes through
// validateForecastItems, the same checks as BSFC.validate on the page, and the
// page shows the result as editable chips: nothing is applied without the
// owner. Team only (finance of the club).
//
// Items (months are numbered from the first forecast month, 1 = start):
//
//	{t:"res", n, every, price, term, from}  new residents: n every `every` months (0 = once), price paid every `term` months
//	{t:"tax", rate} {t:"kaspi", rate}        % of revenue
//	{t:"churn", pct}                         % of residents leaving per month
//	{t:"hire", title, salary, from}          monthly salary from a month
//	{t:"price", pct, from}                   price change of residents' payments
//	{t:"exp", cat, pct, from}                expense line change ("*" = all lines)
//	{t:"once", title, amount, m}             one-off expense in month m
//	{t:"horizon", months}                    6, 12 or 24
//	{t:"div", amount} {t:"goal", amount}     monthly dividends, monthly profit target

type forecastReq struct {
	Text     string   `json:"text"`
	Start    string   `json:"start"`
	ExpLines []string `json:"expLines"`
	Horizon  int      `json:"horizon"`
}

// Limits of the parameters: the same table as BSFC.LIM in platform.html.
var forecastLim = map[string][2]float64{
	"n": {1, 100}, "every": {0, 24}, "price": {1000, 1e8}, "term": {1, 24}, "from": {1, 24}, "rate": {0, 50},
	"pct": {-100, 500}, "churn": {0, 100}, "salary": {1000, 5e7}, "amount": {1, 1e10}, "m": {1, 24}, "div": {0, 1e10}, "goal": {0, 1e10},
}

const forecastMaxItems = 20

// jsRound rounds like Math.round in the page (halves up, also below zero).
func jsRound(v float64) float64  { return math.Floor(v + 0.5) }
func fcRound2(v float64) float64 { return jsRound(v*100) / 100 }
func fcRound1(v float64) float64 { return jsRound(v*10) / 10 }

// forecastNum: a number of the model's answer (a JSON number or a numeric string).
func forecastNum(v any) (float64, bool) {
	switch x := v.(type) {
	case float64:
		return x, !math.IsNaN(x) && !math.IsInf(x, 0)
	case json.Number:
		f, err := x.Float64()
		return f, err == nil
	case string:
		s := strings.TrimSpace(x)
		if s == "" {
			return 0, false
		}
		f, err := strconv.ParseFloat(s, 64) // as Number() in the page: no spaces or commas inside
		return f, err == nil && !math.IsNaN(f) && !math.IsInf(f, 0)
	case int:
		return float64(x), true
	}
	return 0, false
}

func forecastIn(x map[string]any, k, lim string) (float64, bool) {
	v, ok := forecastNum(x[k])
	if !ok {
		return 0, false
	}
	l := forecastLim[lim]
	return v, v >= l[0] && v <= l[1]
}

var forecastCtl = regexp.MustCompile(`[\x00-\x1f<>]`)

// forecastText: a short label (title, expense line), as txt() in the page.
func forecastText(v any) string {
	s, _ := v.(string)
	if f, ok := v.(float64); ok {
		s = strconv.FormatFloat(f, 'f', -1, 64)
	}
	s = forecastCtl.ReplaceAllString(s, " ")
	s = strings.ReplaceAll(s, "\u2014", "-")
	s = strings.Join(strings.Fields(s), " ")
	if utf8.RuneCountInString(s) > 60 {
		s = string([]rune(s)[:60])
		s = strings.TrimSpace(s)
	}
	return s
}

// validateForecastItem mirrors validItem in platform.html: nil when the item is unusable.
func validateForecastItem(x map[string]any) map[string]any {
	if x == nil {
		return nil
	}
	t, _ := x["t"].(string)
	o := map[string]any{"t": t}
	opt := func(k, lim string, def float64) float64 {
		if v, ok := forecastIn(x, k, lim); ok {
			return jsRound(v)
		}
		return def
	}
	switch t {
	case "res":
		n, ok1 := forecastIn(x, "n", "n")
		p, ok2 := forecastIn(x, "price", "price")
		if !ok1 || !ok2 {
			return nil
		}
		o["n"], o["price"] = jsRound(n), jsRound(p)
		o["every"], o["term"], o["from"] = opt("every", "every", 0), opt("term", "term", 1), opt("from", "from", 1)
	case "tax", "kaspi":
		r, ok := forecastIn(x, "rate", "rate")
		if !ok {
			return nil
		}
		o["rate"] = fcRound2(r)
	case "churn":
		p, ok := forecastIn(x, "pct", "churn")
		if !ok {
			return nil
		}
		o["pct"] = fcRound2(p)
	case "hire":
		s, ok := forecastIn(x, "salary", "salary")
		if !ok {
			return nil
		}
		o["salary"] = jsRound(s)
		o["title"] = forecastText(x["title"])
		if o["title"] == "" {
			o["title"] = "Сотрудник"
		}
		o["from"] = opt("from", "from", 1)
	case "price", "exp":
		p, ok := forecastIn(x, "pct", "pct")
		if !ok || p == 0 {
			return nil
		}
		o["pct"] = fcRound1(p)
		if t == "exp" {
			c := forecastText(x["cat"])
			if c == "" {
				c = "*"
			}
			o["cat"] = c
		}
		o["from"] = opt("from", "from", 1)
	case "once":
		a, ok := forecastIn(x, "amount", "amount")
		if !ok {
			return nil
		}
		o["amount"] = jsRound(a)
		o["m"] = opt("m", "m", 1)
		o["title"] = forecastText(x["title"])
		if o["title"] == "" {
			o["title"] = "Разовый расход"
		}
	case "horizon":
		h, ok := forecastNum(x["months"])
		h = jsRound(h)
		if !ok || (h != 6 && h != 12 && h != 24) {
			return nil
		}
		o["months"] = h
	case "div", "goal":
		a, ok := forecastIn(x, "amount", t)
		if !ok {
			return nil
		}
		o["amount"] = jsRound(a)
	default:
		return nil
	}
	return o
}

// validateForecastItems: at most 20 items; dropped counts the unusable and the extra ones.
func validateForecastItems(in []any) (out []map[string]any, dropped int) {
	out = []map[string]any{}
	for i, v := range in {
		if i >= forecastMaxItems {
			dropped += len(in) - forecastMaxItems
			break
		}
		m, _ := v.(map[string]any)
		if it := validateForecastItem(m); it != nil {
			out = append(out, it)
		} else {
			dropped++
		}
	}
	return out, dropped
}

func parseForecastAI(raw string) (items []map[string]any, rest []string, dropped int, err error) {
	s := strings.TrimSpace(raw)
	if m := fenceRe.FindStringSubmatch(s); m != nil {
		s = m[1]
	}
	if i, j := strings.Index(s, "{"), strings.LastIndex(s, "}"); i >= 0 && j > i {
		s = s[i : j+1]
	}
	var in struct {
		Items []any `json:"items"`
		Rest  []any `json:"rest"`
	}
	if err = json.Unmarshal([]byte(s), &in); err != nil {
		return nil, nil, 0, err
	}
	if in.Items == nil {
		return nil, nil, 0, errors.New("no items")
	}
	items, dropped = validateForecastItems(in.Items)
	rest = []string{}
	for _, r := range in.Rest {
		if t := forecastText(r); t != "" && len(rest) < 5 {
			rest = append(rest, t)
		}
	}
	return items, rest, dropped, nil
}

const forecastSystem = "Ты финансовый аналитик клуба. Переводишь фразу собственника о сценарии прогноза в параметры. Отвечай только JSON."

func forecastPrompt(r forecastReq) string {
	var b strings.Builder
	b.WriteString("Первый месяц прогноза: " + r.Start + " (это месяц 1, следующий месяц 2 и так далее).\n")
	if len(r.ExpLines) > 0 {
		b.WriteString("Статьи расходов: " + strings.Join(r.ExpLines, "; ") + "\n")
	}
	b.WriteString(`Верни {"items":[...],"rest":["части фразы, которые не понял"]}. Пункты:
{"t":"res","n":3,"every":3,"price":500000,"term":1,"from":1}: новые резиденты, n человек каждые every месяцев (0 = один раз), платёж price тенге раз в term месяцев, с месяца from
{"t":"tax","rate":3}: налог, % от выручки (упрощёнка 3)
{"t":"kaspi","rate":1}: комиссия Kaspi, % от выручки
{"t":"churn","pct":5}: отток резидентов, % в месяц
{"t":"hire","title":"Менеджер","salary":400000,"from":3}: найм, зарплата в месяц с месяца from
{"t":"price","pct":10,"from":4}: изменение цены для резидентов в %
{"t":"exp","cat":"Офис","pct":20,"from":2}: изменение статьи расходов в % ("*" = все расходы)
{"t":"once","title":"Ремонт","amount":2000000,"m":4}: разовый расход в месяце m
{"t":"horizon","months":12}: горизонт 6, 12 или 24 месяца
{"t":"div","amount":500000}: дивиденды в месяц
{"t":"goal","amount":5000000}: цель по прибыли в месяц
Суммы в тенге числом (500 тыс = 500000, 1,5 млн = 1500000). Месяцы считай от первого месяца прогноза. Ничего не придумывай: только то, что есть во фразе.
`)
	b.WriteString("Фраза: «" + r.Text + "»")
	return b.String()
}

var forecastStartRe = regexp.MustCompile(`^20\d\d-(0[1-9]|1[0-2])$`)

func (h *PlatformAI) Forecast(c *gin.Context) {
	if !teamOnly(c) {
		return
	}
	var req forecastReq
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Не удалось прочитать фразу", "code": "bad_request"})
		return
	}
	req.Text = strings.TrimSpace(forecastCtl.ReplaceAllString(req.Text, " "))
	if req.Text == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Напишите, что меняется", "code": "empty"})
		return
	}
	if utf8.RuneCountInString(req.Text) > 600 {
		req.Text = string([]rune(req.Text)[:600])
	}
	if !forecastStartRe.MatchString(req.Start) {
		now := time.Now().AddDate(0, 1, 0)
		req.Start = now.Format("2006-01")
	}
	lines := []string{}
	for _, l := range req.ExpLines {
		if t := forecastText(l); t != "" && len(lines) < 40 {
			lines = append(lines, t)
		}
	}
	req.ExpLines = lines
	if h.AI == nil || h.AI.Status()["text"] == "" {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": ai.ErrNoKey.Error(), "code": "no_ai"})
		return
	}
	ctx, cancel := context.WithTimeout(c.Request.Context(), 60*time.Second)
	defer cancel()
	raw, err := h.AI.JSON(ai.Light(ctx), forecastSystem, forecastPrompt(req))
	if err != nil {
		msg := "ИИ не ответил, попробуйте ещё раз"
		if ai.IsQuota(err) || errors.Is(err, ai.ErrNoKey) {
			msg = ai.UserMessage(err)
		}
		c.JSON(http.StatusBadGateway, gin.H{"error": msg, "code": "ai_failed", "detail": ai.UserMessage(err)})
		return
	}
	items, rest, dropped, err := parseForecastAI(raw)
	if err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": "ИИ ответил в другом формате, попробуйте ещё раз", "code": "ai_parse"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"items": items, "rest": rest, "dropped": dropped})
}
