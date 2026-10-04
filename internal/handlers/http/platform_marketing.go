package http

import (
	"context"
	"encoding/json"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/bnursik/business_surgery_backend/internal/ai"
	"github.com/bnursik/business_surgery_backend/internal/content"
	"github.com/gin-gonic/gin"
)

// Маркетинговый анализ: club doc bs_mkt_analysis. Сервер кладёт первую
// версию (если документа ещё нет), дальше его правит команда на платформе.
// POST /ai/marketing {"section": "competitors"} обновляет раздел через ИИ с
// поиском в интернете и возвращает новый вариант, платформа показывает его
// и сохраняет, если трекер согласен.

func (h *PlatformAI) SeedMarketing(ctx context.Context) {
	if h.repo == nil || len(content.Marketing) == 0 {
		return
	}
	if d, err := h.repo.GetDoc(ctx, "club", "bs_mkt_analysis"); err == nil && d != nil {
		return // the team's copy is the truth now
	}
	if _, err := h.repo.PutDoc(ctx, "club", "bs_mkt_analysis", 0, string(content.Marketing), false, "server:content"); err == nil {
		log.Printf("marketing analysis stored")
	}
}

var mktSections = map[string]string{
	"segments":    `"segments": [{"name","share","portrait","revenue","situation","pains":[],"desires":[],"fears":[],"objections":[{"text","answer"}],"triggers":[],"where":[],"quote"}]`,
	"jtbd":        `"jtbd": [{"job","when","outcome","now_hire"}]`,
	"value":       `"value": {"perceived":[{"what","why_matters","proof"}],"gains":[],"pains_removed":[],"price_logic"}`,
	"ladder":      `"ladder": [{"stage":1,"name","state","question","message","content":[],"offer","channels":[],"metric","share"}] (ровно 5 ступеней лестницы Бена Ханта: Нет проблемы, Осознаёт проблему, Ищет решение, Выбирает продукт, Готов купить)`,
	"competitors": `"competitors": [{"name","type":"прямой|косвенный|альтернатива","offer","price","strengths":[],"weaknesses":[],"our_edge","url"}] (только реальные игроки Казахстана и СНГ, которые ты нашёл в поиске, с ссылкой; цену пиши только найденную)`,
	"positioning": `"positioning": {"statement","usp":[],"vs":[{"against","we"}],"proof":[]}`,
	"insights":    `"insights": []`,
	"actions":     `"actions": [{"text","priority":"высокий|средний","effect"}]`,
}

// Marketing: POST /api/v1/platform/ai/marketing {"section": "...", "notes": "..."}
func (h *PlatformAI) Marketing(c *gin.Context) {
	if !teamOnly(c) {
		return
	}
	var req struct {
		Section string `json:"section"`
		Notes   string `json:"notes"`
	}
	_ = c.ShouldBindJSON(&req)
	shape, ok := mktSections[req.Section]
	if !ok {
		c.JSON(http.StatusBadRequest, gin.H{"error": "section"})
		return
	}
	cur := ""
	if d, err := h.repo.GetDoc(c.Request.Context(), "club", "bs_mkt_analysis"); err == nil && d != nil {
		cur = d.Value
	}
	if len(cur) > 60000 {
		cur = cur[:60000]
	}
	notes := strings.TrimSpace(req.Notes)
	if r := []rune(notes); len(r) > 2000 {
		notes = string(r[:2000])
	}
	prompt := "Ты маркетолог-аналитик клуба бизнес-трекинга Business Surgery (Алматы, bxclub.kz). " +
		"Вот текущий маркетинговый анализ клуба (JSON):\n" + cur + "\n\n" +
		"Обнови раздел «" + req.Section + "»: проверь и дополни его по свежим данным из интернета (Казахстан, Алматы, предприниматели с чистой прибылью 2-20 млн ₸ в месяц). " +
		"Сохрани то, что верно, убери устаревшее, добавь новое. Пиши по-русски, коротко и конкретно, без тире «—», числа как «10 000». Не выдумывай факты и цены.\n"
	if notes != "" {
		prompt += "Пожелание команды: " + notes + "\n"
	}
	prompt += "Верни ТОЛЬКО JSON вида {" + shape + "}."
	ctx, cancel := context.WithTimeout(c.Request.Context(), 150*time.Second)
	defer cancel()
	ans, err := h.AI.Search(ctx, prompt)
	if err != nil {
		c.JSON(http.StatusOK, gin.H{"error": ai.UserMessage(err), "quota": ai.IsQuota(err)})
		return
	}
	var out map[string]json.RawMessage
	if js := ai.JSONFrom(ans); js == "" || json.Unmarshal([]byte(js), &out) != nil || out[req.Section] == nil {
		c.JSON(http.StatusOK, gin.H{"error": "ИИ не вернул раздел, попробуйте ещё раз"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"section": req.Section, "data": out[req.Section]})
}
