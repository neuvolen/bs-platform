package http

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
)

// Message: POST /api/v1/app/message?_tg=  body {"chatId": "...", "text": "..."}
// The bot delivers a message in the app's name. Telegram cannot open a chat
// by user id from inside a web app, so «Написать» goes through the bot.
// The team may write to anyone; residents and leads only to the team.
func (g *AppGateway) Message(c *gin.Context) {
	u, ok := g.identify(c, c.Query("_tg"))
	if !ok {
		return
	}
	body, _ := io.ReadAll(http.MaxBytesReader(c.Writer, c.Request.Body, 64<<10))
	var req struct {
		ChatID json.RawMessage `json:"chatId"`
		Text   string          `json:"text"`
	}
	if json.Unmarshal(body, &req) != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "bad_body"})
		return
	}
	to, err := strconv.ParseInt(strings.Trim(string(req.ChatID), `" `), 10, 64)
	text := strings.TrimSpace(req.Text)
	if err != nil || to == 0 || text == "" || len([]rune(text)) > 3500 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "chat_and_text_required"})
		return
	}
	sender, fromTeam := g.Admins[u.ID]
	if !fromTeam {
		if _, toTeam := g.Admins[to]; !toTeam {
			c.JSON(http.StatusForbidden, gin.H{"error": "only_team"})
			return
		}
		sender = strings.TrimSpace(u.FirstName + " " + u.LastName)
		if u.Username != "" {
			sender += " (@" + u.Username + ")"
		}
	}
	if sender == "" {
		sender = "BS"
	}
	msg := fmt.Sprintf("Сообщение от %s:\n%s", sender, text)
	payload, _ := json.Marshal(map[string]any{"chat_id": to, "text": msg})
	ctx, cancel := context.WithTimeout(c.Request.Context(), 15*time.Second)
	defer cancel()
	rq, _ := http.NewRequestWithContext(ctx, "POST", g.tgBase()+"/bot"+g.token+"/sendMessage", bytes.NewReader(payload))
	rq.Header.Set("Content-Type", "application/json")
	res, err := g.client.Do(rq)
	if err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": "telegram_unreachable"})
		return
	}
	defer res.Body.Close()
	var tr struct {
		OK          bool   `json:"ok"`
		Description string `json:"description"`
	}
	_ = json.NewDecoder(io.LimitReader(res.Body, 1<<16)).Decode(&tr)
	if !tr.OK {
		why := "Telegram не доставил"
		if strings.Contains(tr.Description, "blocked") || strings.Contains(tr.Description, "chat not found") {
			why = "Человек ещё не запускал бота или заблокировал его"
		}
		c.JSON(http.StatusOK, gin.H{"error": why})
		return
	}
	c.JSON(http.StatusOK, gin.H{"ok": true})
}
