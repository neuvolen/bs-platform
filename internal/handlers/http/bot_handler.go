package http

import (
	"crypto/subtle"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/bnursik/business_surgery_backend/internal/bot"
	"github.com/gin-gonic/gin"
)

// BotHandler: the Telegram webhook, and the calls the Google Sheet makes to
// switch the bot over to the server and to check on it.
type BotHandler struct {
	svc       *bot.Service
	publicURL string // https://host the webhook lives at; empty: taken from the request
}

func NewBotHandler(svc *bot.Service, publicURL string) *BotHandler {
	return &BotHandler{svc: svc, publicURL: strings.TrimRight(strings.TrimSpace(publicURL), "/")}
}

type BotModule struct{ h *BotHandler }

func NewBotModule(h *BotHandler) *BotModule { return &BotModule{h: h} }

func (m *BotModule) Register(r *gin.Engine) {
	r.POST("/api/v1/bot/webhook", m.h.Webhook)
	r.GET("/api/v1/bot/webhook", m.h.Version)
	r.POST("/api/v1/bot/connect", m.h.Connect)
	r.POST("/api/v1/bot/status", m.h.Status)
	r.POST("/api/v1/bot/retry", m.h.RetryFailed)
}

// Webhook godoc
// @Summary  Telegram webhook
// @Description  Stores the update and answers at once; the update then goes on to the Apps Script bot. Checked by X-Telegram-Bot-Api-Secret-Token.
// @Tags     bot
// @Router   /api/v1/bot/webhook [post]
func (h *BotHandler) Webhook(c *gin.Context) {
	if !h.svc.Enabled() {
		c.Status(http.StatusServiceUnavailable)
		return
	}
	got := c.GetHeader("X-Telegram-Bot-Api-Secret-Token")
	if subtle.ConstantTimeCompare([]byte(got), []byte(h.svc.WebhookSecret())) != 1 {
		c.Status(http.StatusUnauthorized)
		return
	}
	body, err := io.ReadAll(http.MaxBytesReader(c.Writer, c.Request.Body, 4<<20))
	if err != nil {
		c.Status(http.StatusBadRequest)
		return
	}
	if _, err := h.svc.Receive(c.Request.Context(), body); err != nil {
		if errors.Is(err, bot.ErrNotUpdate) {
			// Not an update: nothing to keep, and a retry would not help.
			c.Status(http.StatusOK)
			return
		}
		// Storage failed: Telegram keeps the update and sends it again.
		c.Status(http.StatusInternalServerError)
		return
	}
	c.Status(http.StatusOK)
}

// Version godoc
// @Summary  Which Apps Script version the bot's updates reach (?action=bsVersion)
// @Description  The sheet's own checks ask the webhook address for its version. With the server in between, the answer is the version of the script behind it.
// @Tags     bot
// @Router   /api/v1/bot/webhook [get]
func (h *BotHandler) Version(c *gin.Context) {
	relay := h.svc.RelayURL()
	if c.Query("action") != "bsVersion" || relay == "" {
		c.JSON(http.StatusOK, gin.H{"via": "server", "relay": relay != ""})
		return
	}
	v, err := h.svc.ScriptVersion(c.Request.Context(), relay)
	if err != nil {
		c.JSON(http.StatusOK, gin.H{"via": "server", "version": "", "error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"via": "server", "version": v})
}

// signed reads a body signed by the sheet with the bot token, not older than an hour.
func (h *BotHandler) signed(c *gin.Context, dst any) bool {
	if !h.svc.Enabled() {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "telegram_not_configured"})
		return false
	}
	body, err := io.ReadAll(http.MaxBytesReader(c.Writer, c.Request.Body, 1<<20))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "bad request"})
		return false
	}
	if !VerifyBotSignature(body, c.GetHeader("X-BS-Signature"), h.svc.Token()) {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "bad_signature"})
		return false
	}
	var head struct {
		TS int64 `json:"ts"`
	}
	if json.Unmarshal(body, &head) != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "bad json"})
		return false
	}
	if d := time.Since(time.Unix(head.TS, 0)); d > time.Hour || d < -5*time.Minute {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "stale_request"})
		return false
	}
	if dst != nil && json.Unmarshal(body, dst) != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "bad json"})
		return false
	}
	return true
}

func (h *BotHandler) webhookURL(c *gin.Context) string {
	base := h.publicURL
	if base == "" {
		host := c.GetHeader("X-Forwarded-Host")
		if host == "" {
			host = c.Request.Host
		}
		base = "https://" + host
	}
	return base + "/api/v1/bot/webhook"
}

type botConnectReq struct {
	TS    int64  `json:"ts"`
	Relay string `json:"relay"`
	Want  string `json:"version"`
}

// Connect godoc
// @Summary  Switch the Telegram bot to the server
// @Description  Signed by the sheet. Body {ts, relay: Apps Script /exec address, version}. Checks the script answers there with that version, remembers it as the relay and points Telegram at the server.
// @Tags     bot
// @Router   /api/v1/bot/connect [post]
func (h *BotHandler) Connect(c *gin.Context) {
	var req botConnectReq
	if !h.signed(c, &req) {
		return
	}
	req.Relay = strings.TrimSpace(req.Relay)
	if !bot.RelayURLPattern.MatchString(req.Relay) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "bad_relay", "detail": "нужен адрес вида https://script.google.com/macros/s/.../exec"})
		return
	}
	ctx := c.Request.Context()
	live, err := h.svc.ScriptVersion(ctx, req.Relay)
	if err != nil || (req.Want != "" && live != req.Want) {
		c.JSON(http.StatusConflict, gin.H{"error": "relay_version", "version": live,
			"detail": "по этому адресу скрипт отвечает версией «" + live + "», а нужна " + req.Want})
		return
	}
	if err := h.svc.SetRelayURL(ctx, req.Relay); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal server error"})
		return
	}
	hook := h.webhookURL(c)
	if err := h.svc.SetWebhook(ctx, hook); err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": "telegram", "detail": err.Error()})
		return
	}
	info, _ := h.svc.GetWebhookInfo(ctx)
	c.JSON(http.StatusOK, gin.H{"ok": true, "webhook": info, "relay": req.Relay, "version": live})
}

// Status godoc
// @Summary  How the bot's updates are flowing
// @Description  Signed by the sheet. Telegram's webhook state, the relay address, the script version behind it and the queue on the server.
// @Tags     bot
// @Router   /api/v1/bot/status [post]
func (h *BotHandler) Status(c *gin.Context) {
	if !h.signed(c, nil) {
		return
	}
	ctx := c.Request.Context()
	out := gin.H{"relay": h.svc.RelayURL()}
	if info, err := h.svc.GetWebhookInfo(ctx); err == nil {
		out["webhook"] = info
		out["viaServer"] = strings.HasSuffix(info.URL, "/api/v1/bot/webhook")
	} else {
		out["webhookError"] = err.Error()
	}
	if st, err := h.svc.Stats(ctx); err == nil {
		out["queue"] = st
	}
	if r := h.svc.RelayURL(); r != "" {
		v, err := h.svc.ScriptVersion(ctx, r)
		out["version"] = v
		if err != nil {
			out["versionError"] = err.Error()
		}
	}
	c.JSON(http.StatusOK, out)
}

// RetryFailed godoc
// @Summary  Send again the updates the script refused ten times
// @Tags     bot
// @Router   /api/v1/bot/retry [post]
func (h *BotHandler) RetryFailed(c *gin.Context) {
	if !h.signed(c, nil) {
		return
	}
	n, err := h.svc.Retry(c.Request.Context())
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal server error"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"requeued": n})
}
