package http

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
)

// Лид-магниты бота (PDF-чек-листы из листа «Лид-магниты»): открытая ссылка
// на файл, чтобы его можно было дать на сайте, в Threads и в приложении.
// GET /api/v1/public/leadmagnet/:key  (sales, unit, delegate, hire, marketing)
var leadMagnets = map[string]struct{ Title, FileID string }{
	"sales":     {"Реанимация продаж", "BQACAgIAAxkBAAIKbWobYv3Qvl14K0O4hW7XH_UGVLVoAAJmlAACg9bhSJKa-UezEAPuOwQ"},
	"delegate":  {"Хирургия рутины", "BQACAgIAAxkBAAIKZGobYtEDwafV4ZhdrJaYtbPJXLsEAAJjlAACg9bhSE-1YQG5I7sHOwQ"},
	"hire":      {"Найм команды", "BQACAgIAAxkBAAIKZ2obYuSnMLVusQ678xJoXDSno6KJAAJklAACg9bhSIyhXQrIdDstOwQ"},
	"marketing": {"Маркетинг без бюджета", "BQACAgIAAxkBAAIKamobYvO9CRxr8bbhj2sy-hzsDqDaAAJllAACg9bhSAXaF1Fgz-a8OwQ"},
}

var lmCache sync.Map // key -> []byte

func (g *AppGateway) LeadMagnet(c *gin.Context) {
	lm, ok := leadMagnets[c.Param("key")]
	if !ok || g.token == "" {
		c.Status(http.StatusNotFound)
		return
	}
	if b, ok := lmCache.Load(c.Param("key")); ok {
		c.Header("Cache-Control", "public, max-age=86400")
		c.Data(http.StatusOK, "application/pdf", b.([]byte))
		return
	}
	ctx, cancel := context.WithTimeout(c.Request.Context(), 60*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, "GET", g.tgBase()+"/bot"+g.token+"/getFile?file_id="+lm.FileID, nil)
	res, err := g.client.Do(req)
	if err != nil {
		c.Status(http.StatusBadGateway)
		return
	}
	var f struct {
		OK     bool `json:"ok"`
		Result struct {
			FilePath string `json:"file_path"`
		} `json:"result"`
	}
	_ = json.NewDecoder(io.LimitReader(res.Body, 1<<16)).Decode(&f)
	res.Body.Close()
	if !f.OK || f.Result.FilePath == "" {
		c.Status(http.StatusNotFound)
		return
	}
	req, _ = http.NewRequestWithContext(ctx, "GET", fmt.Sprintf("%s/file/bot%s/%s", g.tgBase(), g.token, f.Result.FilePath), nil)
	res, err = g.client.Do(req)
	if err != nil {
		c.Status(http.StatusBadGateway)
		return
	}
	defer res.Body.Close()
	b, err := io.ReadAll(io.LimitReader(res.Body, 20<<20))
	if err != nil || res.StatusCode != 200 {
		c.Status(http.StatusBadGateway)
		return
	}
	lmCache.Store(c.Param("key"), b)
	c.Header("Cache-Control", "public, max-age=86400")
	c.Data(http.StatusOK, "application/pdf", b)
}
