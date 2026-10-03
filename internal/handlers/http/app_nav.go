package http

import (
	"net/http"

	"github.com/bnursik/business_surgery_backend/web"
	"github.com/gin-gonic/gin"
)

// Nav godoc
// @Summary  The platform's sections for the Telegram app's «Ещё» menu
// @Description  Public: section ids and names only (no club data). The app shows a platform link only while the platform has that section, so a removed link (Google Doc, Google Таблица) leaves the app with the next server deploy.
// @Tags     app
// @Router   /api/v1/app/nav [get]
func (g *AppGateway) Nav(c *gin.Context) {
	c.Header("Cache-Control", "public, max-age=300")
	c.Data(http.StatusOK, "application/json; charset=utf-8", web.PlatformNav())
}
