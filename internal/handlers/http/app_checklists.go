package http

import (
	"net/http"

	"github.com/bnursik/business_surgery_backend/internal/content"
	"github.com/gin-gonic/gin"
)

// Checklists: GET /api/v1/app/checklists?_tg=  — the 99 checklists for every
// Telegram user of the app (leads see the club's value, residents use them).
func (g *AppGateway) Checklists(c *gin.Context) {
	if _, ok := g.identify(c, c.Query("_tg")); !ok {
		return
	}
	c.Header("Cache-Control", "private, max-age=3600")
	c.Header("X-Content-Version", content.ChecklistsVersion())
	c.Data(http.StatusOK, "application/json; charset=utf-8",
		append(append([]byte(`{"version":"`+content.ChecklistsVersion()+`","items":`), content.Checklists...), '}'))
}
