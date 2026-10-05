package http

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"mime/multipart"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/bnursik/business_surgery_backend/internal/content"
	"github.com/gin-gonic/gin"
)

// 99 гайдов клуба.
//   GET  /api/v1/app/guides?_tg=          каталог (и старый адрес /app/checklists)
//   GET  /api/v1/app/guide/:id?_tg=       гайд целиком для чтения в приложении
//   POST /api/v1/app/guide/:id/send?_tg=  бот присылает PDF в Telegram
//   GET  /api/v1/public/guide/:id         PDF по открытой ссылке (сайт, Threads)

func (g *AppGateway) Guides(c *gin.Context) {
	if _, ok := g.identify(c, c.Query("_tg")); !ok {
		return
	}
	c.Header("Cache-Control", "private, max-age=3600")
	c.Header("X-Content-Version", content.GuidesVersion())
	c.Data(http.StatusOK, "application/json; charset=utf-8", content.GuidesIndex())
}

func (g *AppGateway) Guide(c *gin.Context) {
	u, ok := g.identify(c, c.Query("_tg"))
	if !ok {
		return
	}
	b := content.Guide(c.Param("id"))
	if b == nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "not_found"})
		return
	}
	g.noteGuideOpen(u, c.Param("id")) // R40b: lead_open.go
	c.Header("Cache-Control", "private, max-age=86400")
	c.Data(http.StatusOK, "application/json; charset=utf-8", b)
}

// PublicGuidePDF serves the shipped PDF; the bot's older PDFs come from Telegram.
func (g *AppGateway) PublicGuidePDF(c *gin.Context) {
	id := strings.TrimSuffix(c.Param("id"), ".pdf")
	if key, ok := content.LeadMagnetKey[id]; ok {
		c.Params = gin.Params{{Key: "key", Value: key}}
		g.LeadMagnet(c)
		return
	}
	b := content.GuidePDF(id)
	if b == nil {
		c.Status(http.StatusNotFound)
		return
	}
	c.Header("Cache-Control", "public, max-age=86400")
	c.Header("Content-Disposition", fmt.Sprintf(`inline; filename="BS-%s.pdf"`, id))
	c.Data(http.StatusOK, "application/pdf", b)
}

var guideFileIDs sync.Map // id -> Telegram file_id after the first upload

// SendGuide: the bot sends the guide's PDF to the user's chat.
func (g *AppGateway) SendGuide(c *gin.Context) {
	u, ok := g.identify(c, c.Query("_tg"))
	if !ok {
		return
	}
	id := c.Param("id")
	title := content.GuideTitle(id)
	if title == "" {
		c.JSON(http.StatusNotFound, gin.H{"error": "not_found"})
		return
	}
	caption := "📘 " + title + "\n\nГайд Business Surgery. Все 99 гайдов в приложении BS."
	ctx, cancel := context.WithTimeout(c.Request.Context(), 60*time.Second)
	defer cancel()
	fileID := ""
	if key, ok := content.LeadMagnetKey[id]; ok {
		fileID = leadMagnets[key].FileID
	} else if v, ok := guideFileIDs.Load(id); ok {
		fileID = v.(string)
	}
	var body bytes.Buffer
	ct := "application/json"
	if fileID != "" {
		b, _ := json.Marshal(map[string]any{"chat_id": u.ID, "document": fileID, "caption": caption})
		body.Write(b)
	} else {
		pdf := content.GuidePDF(id)
		if pdf == nil {
			c.JSON(http.StatusNotFound, gin.H{"error": "no_pdf"})
			return
		}
		w := multipart.NewWriter(&body)
		_ = w.WriteField("chat_id", fmt.Sprint(u.ID))
		_ = w.WriteField("caption", caption)
		fw, _ := w.CreateFormFile("document", "BS — "+title+".pdf")
		_, _ = fw.Write(pdf)
		_ = w.Close()
		ct = w.FormDataContentType()
	}
	req, _ := http.NewRequestWithContext(ctx, "POST", g.tgBase()+"/bot"+g.token+"/sendDocument", &body)
	req.Header.Set("Content-Type", ct)
	res, err := g.client.Do(req)
	if err != nil {
		c.JSON(http.StatusOK, gin.H{"error": "Telegram недоступен, попробуйте позже"})
		return
	}
	defer res.Body.Close()
	var tr struct {
		OK          bool   `json:"ok"`
		Description string `json:"description"`
		Result      struct {
			Document struct {
				FileID string `json:"file_id"`
			} `json:"document"`
		} `json:"result"`
	}
	_ = json.NewDecoder(io.LimitReader(res.Body, 1<<16)).Decode(&tr)
	if !tr.OK {
		why := "Telegram не доставил файл"
		if strings.Contains(tr.Description, "blocked") || strings.Contains(tr.Description, "chat not found") {
			why = "Сначала нажмите «Старт» в боте @bsurgery_bot"
		}
		c.JSON(http.StatusOK, gin.H{"error": why})
		return
	}
	if tr.Result.Document.FileID != "" {
		guideFileIDs.Store(id, tr.Result.Document.FileID)
	}
	if g.Funnel != nil {
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			defer cancel()
			if err := g.Funnel.Downloaded(ctx, u.ID, id, title); err != nil {
				log.Printf("guide download note: %v", err)
			}
		}()
	}
	c.JSON(http.StatusOK, gin.H{"ok": true})
}
