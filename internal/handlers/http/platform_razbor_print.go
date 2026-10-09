package http

import (
	"mime"
	"net/http"

	"github.com/bnursik/business_surgery_backend/internal/tplpdf"
	"github.com/gin-gonic/gin"
)

// POST /api/v1/platform/razbor/pdf {PrintDoc} → «Печать разбора» (R69): the
// board as a black and white A4 checklist, named «Разбор Имя ДД.ММ.ГГГГ.pdf».
// The platform sends what its live preview shows; sizes are bounded here.

const razborPrintMax = 512 << 10

func RazborPrintPDF(c *gin.Context) {
	var d tplpdf.PrintDoc
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, razborPrintMax)
	if err := c.ShouldBindJSON(&d); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "bad_request"})
		return
	}
	d.Clean()
	if d.Empty() {
		c.JSON(http.StatusBadRequest, gin.H{"error": "empty", "detail": "На доске пока нечего печатать"})
		return
	}
	b, err := tplpdf.RenderRazborPrint(&d)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "render"})
		return
	}
	h := c.Writer.Header()
	h.Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": d.FileName()}))
	h.Set("Cache-Control", "private, no-store")
	h.Set("X-Content-Type-Options", "nosniff")
	c.Data(http.StatusOK, "application/pdf", b)
}
