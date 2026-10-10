package http

import (
	"github.com/bnursik/business_surgery_backend/internal/middleware"

	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"math/rand"
	"net/http"
	"os"
	"regexp"
	"strings"
	"time"

	pg "github.com/bnursik/business_surgery_backend/internal/repository/pg"
	"github.com/gin-gonic/gin"
)

// WhatsApp в CRM через Green-API (green-api.com): номер подключается по QR,
// ключи GREEN_API_ID и GREEN_API_TOKEN в переменных Railway. Сервер сам
// прописывает Green-API адрес вебхука, принимает входящие, заводит лида в CRM
// (club doc bs_crm) и отправляет ответы, написанные на платформе.

type greenAPI struct{ id, token, base string }

func greenFromEnv() *greenAPI {
	id, tok := strings.TrimSpace(os.Getenv("GREEN_API_ID")), strings.TrimSpace(os.Getenv("GREEN_API_TOKEN"))
	if id == "" || tok == "" {
		// R83: the keys the owner saved on the platform (r83_wa_connect.go)
		if g := waSaved.Load(); g != nil {
			c := *g
			return &c
		}
		return nil
	}
	base := strings.TrimRight(strings.TrimSpace(os.Getenv("GREEN_API_URL")), "/")
	if base == "" {
		base = "https://api.green-api.com"
	}
	return &greenAPI{id: id, token: tok, base: base}
}

func (g *greenAPI) url(method string) string {
	return fmt.Sprintf("%s/waInstance%s/%s/%s", g.base, g.id, method, g.token)
}

// webhookSecret: a path part only Green-API (set up by this server) knows.
func (g *greenAPI) webhookSecret() string {
	s := sha256.Sum256([]byte("wa-hook|" + g.token))
	return hex.EncodeToString(s[:12])
}

func (h *PlatformAI) greenCall(ctx context.Context, g *greenAPI, method, verb string, body any) (map[string]any, error) {
	var rd io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	}
	req, _ := http.NewRequestWithContext(ctx, verb, g.url(method), rd)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	res, err := h.AI.HTTP.Do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	var out map[string]any
	_ = json.Unmarshal(b, &out)
	if res.StatusCode >= 300 {
		return out, fmt.Errorf("Green-API %d: %s", res.StatusCode, strings.TrimSpace(string(b)))
	}
	return out, nil
}

func publicBase() string {
	if u := middleware.HTTPSURL(os.Getenv("PUBLIC_URL")); u != "" { // R38a: http:// in the variable still gives https links
		return u
	}
	if d := strings.TrimSpace(os.Getenv("RAILWAY_PUBLIC_DOMAIN")); d != "" {
		return "https://" + d
	}
	return ""
}

// SetupWhatsApp points Green-API's webhook at this server (at start).
func (h *PlatformAI) SetupWhatsApp(ctx context.Context) {
	if waSaved.Load() == nil {
		h.LoadWACreds(ctx) // R83: keys saved on the platform
	}
	g := greenFromEnv()
	base := publicBase()
	if g == nil || base == "" {
		return
	}
	hook := base + "/api/v1/wa/webhook/" + g.webhookSecret()
	_, err := h.greenCall(ctx, g, "setSettings", "POST", map[string]any{
		"webhookUrl": hook, "incomingWebhook": "yes", "outgoingMessageWebhook": "yes",
		"outgoingAPIMessageWebhook": "no", "stateWebhook": "yes",
	})
	log.Printf("whatsapp: webhook set err=%v", err)
}

var digitsRe = regexp.MustCompile(`\D`)

func phoneDigits(s string) string {
	d := digitsRe.ReplaceAllString(s, "")
	if len(d) == 11 && strings.HasPrefix(d, "8") {
		d = "7" + d[1:]
	}
	if len(d) == 10 {
		d = "7" + d
	}
	return d
}

// WAWebhook: POST /api/v1/wa/webhook/:secret (Green-API)
func (h *PlatformAI) WAWebhook(c *gin.Context) {
	g := greenFromEnv()
	if g == nil || c.Param("secret") != g.webhookSecret() {
		c.Status(http.StatusNotFound)
		return
	}
	body, _ := io.ReadAll(http.MaxBytesReader(c.Writer, c.Request.Body, 1<<20))
	var ev struct {
		Type      string `json:"typeWebhook"`
		IDMessage string `json:"idMessage"`
		Timestamp int64  `json:"timestamp"`
		Sender    struct {
			ChatID     string `json:"chatId"`
			SenderName string `json:"senderName"`
			ChatName   string `json:"chatName"`
		} `json:"senderData"`
		Message struct {
			Type string `json:"typeMessage"`
			Text struct {
				Text string `json:"textMessage"`
			} `json:"textMessageData"`
			Ext struct {
				Text string `json:"text"`
			} `json:"extendedTextMessageData"`
			File struct {
				Caption string `json:"caption"`
			} `json:"fileMessageData"`
		} `json:"messageData"`
	}
	if json.Unmarshal(body, &ev) != nil {
		c.Status(http.StatusOK)
		return
	}
	c.Status(http.StatusOK) // Green-API only needs a quick 200
	if ev.Type != "incomingMessageReceived" && ev.Type != "outgoingMessageReceived" {
		return
	}
	if !strings.HasSuffix(ev.Sender.ChatID, "@c.us") { // groups and channels are not leads
		return
	}
	phone := phoneDigits(strings.TrimSuffix(ev.Sender.ChatID, "@c.us"))
	text := ev.Message.Text.Text
	if text == "" {
		text = ev.Message.Ext.Text
	}
	if text == "" {
		kinds := map[string]string{"imageMessage": "фото", "videoMessage": "видео", "audioMessage": "голосовое", "documentMessage": "файл", "locationMessage": "геолокация", "contactMessage": "контакт", "stickerMessage": "стикер"}
		k := kinds[ev.Message.Type]
		if k == "" {
			k = "сообщение"
		}
		text = "[" + k + "]"
		if ev.Message.File.Caption != "" {
			text += " " + ev.Message.File.Caption
		}
	}
	dir, name := "in", ev.Sender.SenderName
	if ev.Type == "outgoingMessageReceived" {
		dir, name = "out", ""
	}
	at := time.Now()
	if ev.Timestamp > 0 {
		at = time.Unix(ev.Timestamp, 0)
	}
	ctx := c.Request.Context()
	fresh, err := h.repo.AddCrmMessage(ctx, pg.CrmMessage{Phone: phone, Dir: dir, Text: text, Name: name, At: at}, ev.IDMessage)
	if err != nil || !fresh {
		return
	}
	if dir == "in" {
		if err := h.upsertWALead(ctx, phone, name, text, at); err != nil {
			log.Printf("whatsapp lead: %v", err)
		}
	}
}

// upsertWALead finds the lead by phone in bs_crm or creates one in «Новые».
func (h *PlatformAI) upsertWALead(ctx context.Context, phone, name, text string, at time.Time) error {
	for try := 0; try < 4; try++ {
		var crm map[string]any
		base := 0
		if d, err := h.repo.GetDoc(ctx, "club", "bs_crm"); err == nil && d != nil {
			base = d.Version
			_ = json.Unmarshal([]byte(d.Value), &crm)
		}
		if crm == nil {
			crm = map[string]any{}
		}
		leads, _ := crm["leads"].([]any)
		var lead map[string]any
		for _, l := range leads {
			m, _ := l.(map[string]any)
			p, _ := m["phone"].(string)
			if pd := phoneDigits(p); pd != "" && len(pd) >= 10 && pd[len(pd)-10:] == phone[len(phone)-10:] {
				lead = m
				break
			}
		}
		loc := time.FixedZone("Almaty", 5*3600)
		if lead == nil {
			if name == "" {
				name = "+" + phone
			}
			lead = map[string]any{
				"id": fmt.Sprintf("wa%d", rand.Int63()%1e9), "col": "new", "name": name, "phone": "+" + phone,
				"tg": "", "source": "WhatsApp", "niche": "", "note": "", "sum": "",
				"date": at.In(loc).Format("02.01.2006"),
			}
			leads = append([]any{lead}, leads...)
		}
		unread, _ := lead["waUnread"].(float64)
		lead["wa"] = true
		lead["waLast"] = text
		lead["waAt"] = at.UTC().Format(time.RFC3339)
		lead["waUnread"] = unread + 1
		crm["leads"] = leads
		val, _ := json.Marshal(crm)
		if _, err := h.repo.PutDoc(ctx, "club", "bs_crm", base, string(val), false, "server:whatsapp"); err == nil {
			return nil
		}
	}
	return pg.ErrPlatformConflict
}

// ── API for the platform (team only) ──

func (h *PlatformAI) WAStatus(c *gin.Context) {
	if !teamOnly(c) {
		return
	}
	// R83: the reason in words and the next step (r83_wa_connect.go)
	c.Header("Cache-Control", "no-store")
	c.JSON(http.StatusOK, h.waStatusView(c.Request.Context()))
}

func (h *PlatformAI) WAChats(c *gin.Context) {
	if !teamOnly(c) {
		return
	}
	list, err := h.repo.CrmChats(c.Request.Context())
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "load_failed"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"chats": list})
}

func (h *PlatformAI) WAMessages(c *gin.Context) {
	if !teamOnly(c) {
		return
	}
	phone := phoneDigits(c.Param("phone"))
	list, err := h.repo.CrmMessages(c.Request.Context(), phone)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "load_failed"})
		return
	}
	_ = h.repo.CrmMarkRead(c.Request.Context(), phone)
	c.JSON(http.StatusOK, gin.H{"messages": list})
}

func (h *PlatformAI) WASend(c *gin.Context) {
	if !teamOnly(c) {
		return
	}
	g := greenFromEnv()
	if g == nil {
		c.JSON(http.StatusOK, gin.H{"error": "WhatsApp не подключён: Продажи → CRM → «Подключить WhatsApp»"})
		return
	}
	var req struct {
		Text string `json:"text"`
	}
	if err := c.ShouldBindJSON(&req); err != nil || strings.TrimSpace(req.Text) == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "text_required"})
		return
	}
	phone := phoneDigits(c.Param("phone"))
	if len(phone) < 11 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "bad_phone"})
		return
	}
	ctx, cancel := context.WithTimeout(c.Request.Context(), 20*time.Second)
	defer cancel()
	out, err := h.greenCall(ctx, g, "sendMessage", "POST", map[string]any{"chatId": phone + "@c.us", "message": req.Text})
	if err != nil {
		c.JSON(http.StatusOK, gin.H{"error": err.Error()})
		return
	}
	id, _ := out["idMessage"].(string)
	_, _ = h.repo.AddCrmMessage(ctx, pg.CrmMessage{Phone: phone, Dir: "out", Text: req.Text, At: time.Now()}, id)
	c.JSON(http.StatusOK, gin.H{"ok": true, "id": id})
}
