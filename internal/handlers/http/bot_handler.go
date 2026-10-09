package http

import (
	"crypto/subtle"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/bnursik/business_surgery_backend/internal/bot"
	"github.com/bnursik/business_surgery_backend/internal/club"
	"github.com/bnursik/business_surgery_backend/internal/content"
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
	r.POST("/api/v1/bot/features", m.h.SetFeatures)
	r.POST("/api/v1/bot/control", m.h.Control)
	r.POST("/api/v1/bot/tick", m.h.Tick)
	r.GET("/api/v1/script/latest", m.h.ScriptLatest)
	r.POST("/api/v1/script/updated", m.h.ScriptUpdated)
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
	if !club.SheetLegacy() {
		// After the cutover the bot is the server: nothing to relay to.
		c.JSON(http.StatusOK, gin.H{"ok": true, "sheetMode": club.SheetMode(), "detail": "бот работает на сервере, таблица отключена"})
		return
	}
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
	out := gin.H{"relay": h.svc.RelayURL(), "features": nzs(h.svc.Features())}
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

func nzs(v []string) []string {
	if v == nil {
		return []string{}
	}
	return v
}

type botFeaturesReq struct {
	TS    int64    `json:"ts"`
	On    []string `json:"on"`
	Clear bool     `json:"clear"`
}

// SetFeatures godoc
// @Summary  The sheet's override of what the server does
// @Description  Signed by the sheet. {ts, on:[…]}: exactly these features, whatever the rollout says ([] gives everything back to the sheet). {ts, clear:true}: the rollout decides again.
// @Tags     bot
// @Router   /api/v1/bot/features [post]
func (h *BotHandler) SetFeatures(c *gin.Context) {
	var req botFeaturesReq
	if !h.signed(c, &req) {
		return
	}
	var list []string
	var err error
	if req.Clear {
		list, err = h.svc.ClearOverride(c.Request.Context())
	} else {
		if len(req.On) > 0 && h.svc.RelayURL() == "" {
			c.JSON(http.StatusConflict, gin.H{"error": "not_via_server", "detail": "сначала бот должен принимать сообщения через сервер"})
			return
		}
		list, err = h.svc.SetFeatures(c.Request.Context(), req.On)
	}
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "bad_feature", "detail": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"features": nzs(list)})
}

type botControlReq struct {
	TS      int64  `json:"ts"`
	Version string `json:"version"`
}

// Control godoc
// @Summary  What the server does itself, asked by the sheet every few minutes
// @Description  Signed by the sheet. Answer {features:[…], master:"sheet"|"server"}; the sheet skips whatever the server does.
// @Tags     bot
// @Router   /api/v1/bot/control [post]
func (h *BotHandler) Control(c *gin.Context) {
	var req botControlReq
	if !h.signed(c, &req) {
		return
	}
	h.svc.NoteScript(c.Request.Context(), req.Version)
	// sheetMode: the script (v40+) is dormant unless "legacy"; in "mirror" it
	// keeps the hourly read-only copy (bsPullFromServer), as long as the
	// export is on (R32d, sheet_owner.go). reconcile: the server asks for one
	// copy of the sheet to compare (v41+ sends it to /api/v1/club/reconcile).
	ctx := c.Request.Context()
	c.JSON(http.StatusOK, gin.H{"features": nzs(h.svc.Features()), "master": h.svc.Master(ctx), "sheetMode": club.ScriptMode(ctx),
		"reconcile": club.ReconcileWanted(ctx)})
}

type botTickReq struct {
	TS  int64  `json:"ts"`
	Now string `json:"now"` // RFC3339, honoured only with BOT_TEST_CLOCK=1
}

// Tick godoc
// @Summary  Run the bot's timed jobs now
// @Description  Signed by the sheet. The same jobs the server runs every minute: what it does, the daily check, meeting reminders, the daily comparison, the 22:00 reminder. Each job still keeps to its own hours and runs once a day.
// @Tags     bot
// @Router   /api/v1/bot/tick [post]
func (h *BotHandler) Tick(c *gin.Context) {
	var req botTickReq
	if !h.signed(c, &req) {
		return
	}
	now := time.Now()
	if req.Now != "" && h.svc.TestClock() {
		if t, err := time.Parse(time.RFC3339, req.Now); err == nil {
			now = t
		}
	}
	c.JSON(http.StatusOK, h.svc.Tick(c.Request.Context(), now))
}

// signedGet checks a GET signed by the sheet:
// X-BS-Signature = hex(HMAC-SHA256("GET " + path + "?" + raw query, bot token)),
// the query has ts (unix seconds), not older than an hour.
func (h *BotHandler) signedGet(c *gin.Context) bool {
	if !h.svc.Enabled() {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "telegram_not_configured"})
		return false
	}
	canon := "GET " + c.Request.URL.Path + "?" + c.Request.URL.RawQuery
	if !VerifyBotSignature([]byte(canon), c.GetHeader("X-BS-Signature"), h.svc.Token()) {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "bad_signature"})
		return false
	}
	ts, err := strconv.ParseInt(c.Query("ts"), 10, 64)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "bad ts"})
		return false
	}
	if d := time.Since(time.Unix(ts, 0)); d > time.Hour || d < -5*time.Minute {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "stale_request"})
		return false
	}
	return true
}

// ScriptLatest godoc
// @Summary  The latest Apps Script code of the sheet, for its self-update
// @Description  Signed GET by the sheet (see signedGet), query version = its own BS_VERSION. Answer {version, relay, apps:[web app urls], files:[{name,type,source}]}; files are empty when the sheet already runs this version. The bot token in the code is the mark "__BS_BOT_TOKEN__"; the sheet puts its own back.
// @Tags     bot
// @Router   /api/v1/script/latest [get]
func (h *BotHandler) ScriptLatest(c *gin.Context) {
	if !h.signedGet(c) {
		return
	}
	have := strings.TrimSpace(c.Query("version"))
	if have != "" {
		h.svc.NoteScript(c.Request.Context(), have)
	}
	v := content.ScriptVersion()
	// apps: the other web app deployments the server calls (the Telegram
	// app's and the platform's club writes go to APP_SCRIPT_URL): the sheet
	// moves them to the new version too (v38), not only the bot's relay.
	out := gin.H{"version": v, "relay": h.svc.RelayURL(), "apps": []string{AppScriptURL()}, "files": []content.ScriptFile{}}
	if have == v {
		out["upToDate"] = true
	} else {
		out["files"] = content.ScriptFiles()
	}
	c.JSON(http.StatusOK, out)
}

// ScriptUpdated godoc
// @Summary  The sheet reports how its self-update went
// @Description  Signed by the sheet: {ts, version, from, deploymentId, versionNumber} after an update, or {ts, version, error} after a failure. Kept in bot_meta, shown in /api/v1/club/migration (script).
// @Tags     bot
// @Router   /api/v1/script/updated [post]
func (h *BotHandler) ScriptUpdated(c *gin.Context) {
	var req bot.ScriptUpdated
	if !h.signed(c, &req) {
		return
	}
	if strings.TrimSpace(req.Version) == "" && req.Error == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "no version"})
		return
	}
	if len(req.Error) > 1000 {
		req.Error = req.Error[:1000]
	}
	// R67: the sheet's old script (v32) failed to update itself every hour
	// unseen, and kept asking «встреча прошла?» from the stale sheet
	if req.Error != "" {
		log.Printf("script self-update failed (running %s, latest %s): %s", req.Version, content.ScriptVersion(), req.Error)
	} else {
		log.Printf("script self-update: %s → %s", req.From, req.Version)
	}
	if err := h.svc.NoteScriptUpdated(c.Request.Context(), req); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal server error"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"ok": true, "latest": content.ScriptVersion()})
}
