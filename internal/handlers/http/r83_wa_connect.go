package http

import (
	"context"
	"encoding/json"
	"log"
	"net/http"
	"os"
	"regexp"
	"strings"
	"sync/atomic"
	"time"

	"github.com/bnursik/business_surgery_backend/internal/ai"
	"github.com/gin-gonic/gin"
)

// R83: «Продажи → WhatsApp не подключён: нажимаю, и он вообще гонит».
//
// What «подключить» is: the club's WhatsApp number is linked to the CRM
// through Green-API (green-api.com), the same way WhatsApp Web is linked on a
// computer: a QR code scanned in WhatsApp → «Связанные устройства». Incoming
// messages then land in the CRM (column «Новый», tab «Чаты») and replies go
// out from the lead's card.
//
// What went wrong: the keys were only read from Railway variables
// (GREEN_API_ID, GREEN_API_TOKEN), which nobody had set, and the card showed
// the raw reason («Нет GREEN_API_ID и GREEN_API_TOKEN в переменных Railway»,
// or Green-API's own error text) with a developer's instruction; the
// «Как подключить» block folded back by itself on the next repaint.
//
// Now the owner connects it on the platform: the two keys from the Green-API
// cabinet go into a form (sealed in the server store, like the Claude key),
// the server checks them at once, shows the QR code right on the card and
// sets the webhook itself. Railway variables still win when they are set.

const waCredsDoc = "wa_green_creds" // platform_docs, scope "server": never synced to a page

var waSaved atomic.Pointer[greenAPI]

type waCreds struct {
	ID    string `json:"id"`
	Token string `json:"token"`
	URL   string `json:"url,omitempty"`
	By    string `json:"by,omitempty"`
	At    string `json:"at,omitempty"`
}

func waSourceOf() string {
	if strings.TrimSpace(os.Getenv("GREEN_API_ID")) != "" && strings.TrimSpace(os.Getenv("GREEN_API_TOKEN")) != "" {
		return "railway"
	}
	if waSaved.Load() != nil {
		return "platform"
	}
	return ""
}

var waIDRe = regexp.MustCompile(`^\d{6,16}$`)

func normGreenURL(u string) string {
	u = strings.TrimRight(strings.TrimSpace(u), "/")
	if u == "" {
		return "https://api.green-api.com"
	}
	if !strings.HasPrefix(u, "https://") {
		return ""
	}
	return u
}

// LoadWACreds reads the saved keys (at start).
func (h *PlatformAI) LoadWACreds(ctx context.Context) {
	if h.repo == nil {
		return
	}
	sec := h.aiKeySecret()
	d, err := h.repo.GetDoc(ctx, "server", waCredsDoc)
	if err != nil || d == nil || d.Deleted || d.Value == "" {
		waSaved.Store(nil)
		return
	}
	plain, err := ai.Open(sec, d.Value)
	if err != nil {
		log.Printf("whatsapp: the saved Green-API keys cannot be read: %v", err)
		return
	}
	var cr waCreds
	if json.Unmarshal([]byte(plain), &cr) != nil || cr.ID == "" || cr.Token == "" {
		return
	}
	waSaved.Store(&greenAPI{id: cr.ID, token: cr.Token, base: normGreenURL(cr.URL)})
}

// waHuman turns Green-API's state or error into words for the owner.
func waHuman(state string, err error) (reason, hint string) {
	if err != nil {
		e := err.Error()
		switch {
		case strings.Contains(e, "Green-API 401"), strings.Contains(e, "Green-API 403"):
			return "Ключи не подходят", "Проверьте idInstance и apiTokenInstance в кабинете Green-API и вставьте их заново."
		case strings.Contains(e, "Green-API 466"):
			return "Инстанс Green-API не оплачен или его срок закончился", "Кабинет Green-API → ваш инстанс → продлите тариф (Developer бесплатный, его продлевают раз в месяц)."
		case strings.Contains(e, "Green-API 404"):
			return "Инстанс не найден", "Проверьте номер инстанса (idInstance) и адрес API в кабинете Green-API."
		case strings.Contains(e, "Green-API 429"):
			return "Green-API просит подождать", "Слишком частые проверки. Нажмите «Проверить» через минуту."
		}
		return "Green-API сейчас не отвечает", "Обычно это на минуту-две. Нажмите «Проверить» чуть позже."
	}
	switch state {
	case "authorized":
		return "", ""
	case "notAuthorized":
		return "Номер ещё не привязан", "Отсканируйте QR-код ниже телефоном, на котором WhatsApp клуба."
	case "blocked":
		return "WhatsApp заблокировал этот номер", "Нужен другой номер. Удалите ключи и подключите новый инстанс."
	case "sleepMode":
		return "Телефон с WhatsApp не в сети", "Включите интернет на телефоне и откройте WhatsApp, затем нажмите «Проверить»."
	case "starting":
		return "Green-API запускает инстанс", "Это 1-2 минуты после создания или перезагрузки. Нажмите «Проверить» чуть позже."
	case "yellowCard":
		return "WhatsApp временно ограничил отправку", "Подключение есть, но отправка приостановлена. Пишите реже и не рассылайте одинаковый текст многим."
	}
	return "Статус Green-API: " + state, "Нажмите «Проверить» через минуту."
}

func (h *PlatformAI) waState(ctx context.Context, g *greenAPI) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	st, err := h.greenCall(ctx, g, "getStateInstance", "GET", nil)
	if err != nil {
		return "", err
	}
	s, _ := st["stateInstance"].(string)
	return s, nil
}

func maskID(id string) string {
	if len(id) <= 4 {
		return id
	}
	return strings.Repeat("•", len(id)-4) + id[len(id)-4:]
}

func waTeam(c *gin.Context) bool {
	if assistOf(c) != nil {
		forbidden(c, "team_only")
		return false
	}
	return teamOnly(c)
}

// waStatusView: GET /crm/wa/status (and the answer of the form).
func (h *PlatformAI) waStatusView(ctx context.Context) gin.H {
	g := greenFromEnv()
	src := waSourceOf()
	if g == nil {
		return gin.H{"connected": false, "source": "", "step": "keys",
			"reason": "WhatsApp ещё не подключали",
			"hint":   "Подключение займёт 5 минут: ключи из Green-API и QR-код с телефона. Нажмите «Подключить WhatsApp»."}
	}
	state, err := h.waState(ctx, g)
	reason, hint := waHuman(state, err)
	step := "check"
	switch {
	case err == nil && state == "authorized":
		step = "done"
	case err == nil && state == "notAuthorized":
		step = "qr"
	case err != nil && (strings.Contains(err.Error(), "Green-API 401") || strings.Contains(err.Error(), "Green-API 403") || strings.Contains(err.Error(), "Green-API 404")):
		step = "keys"
	}
	if err != nil {
		log.Printf("whatsapp status: %v", err)
	}
	return gin.H{"connected": err == nil && state == "authorized", "state": state, "source": src, "instance": maskID(g.id),
		"step": step, "reason": reason, "hint": hint}
}

type waConfigReq struct {
	ID    string `json:"id"`
	Token string `json:"token"`
	URL   string `json:"url"`
}

// WAConfig: PUT /crm/wa/config {id, token, url?}: the keys from the Green-API
// cabinet. Checked before they are kept: wrong keys are refused in words.
func (h *PlatformAI) WAConfig(c *gin.Context) {
	if !waTeam(c) {
		return
	}
	if waSourceOf() == "railway" {
		c.JSON(http.StatusConflict, gin.H{"error": "railway", "message": "Ключи уже заданы в Railway (GREEN_API_ID и GREEN_API_TOKEN): они главнее. Удалите их там, чтобы задавать здесь"})
		return
	}
	var r waConfigReq
	if err := c.ShouldBindJSON(&r); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "bad_request"})
		return
	}
	r.ID = strings.TrimSpace(r.ID)
	r.Token = strings.TrimSpace(r.Token)
	base := normGreenURL(r.URL)
	if !waIDRe.MatchString(r.ID) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "bad_id", "message": "idInstance: это только цифры, например 7103123456. Скопируйте его из кабинета Green-API"})
		return
	}
	if len(r.Token) < 20 || strings.ContainsAny(r.Token, " /?#") {
		c.JSON(http.StatusBadRequest, gin.H{"error": "bad_token", "message": "apiTokenInstance: длинная строка из букв и цифр. Скопируйте её целиком кнопкой копирования в кабинете"})
		return
	}
	if base == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "bad_url", "message": "Адрес API должен начинаться с https://, например https://7103.api.greenapi.com"})
		return
	}
	g := &greenAPI{id: r.ID, token: r.Token, base: base}
	ctx := c.Request.Context()
	state, err := h.waState(ctx, g)
	if err != nil && (strings.Contains(err.Error(), "Green-API 401") || strings.Contains(err.Error(), "Green-API 403") || strings.Contains(err.Error(), "Green-API 404")) {
		reason, hint := waHuman("", err)
		c.JSON(http.StatusBadRequest, gin.H{"error": "keys_rejected", "message": reason + ". " + hint})
		return
	}
	sec := h.aiKeySecret()
	plain, _ := json.Marshal(waCreds{ID: r.ID, Token: r.Token, URL: r.URL, By: platformUser(c), At: time.Now().UTC().Format(time.RFC3339)})
	sealed, serr := ai.Seal(sec, string(plain))
	if serr != nil || h.repo == nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "store_failed", "message": "Не получилось сохранить ключи"})
		return
	}
	if !h.putServerDoc(ctx, waCredsDoc, sealed, false, platformUser(c)) {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "store_failed", "message": "Не получилось сохранить ключи"})
		return
	}
	waSaved.Store(g)
	log.Printf("whatsapp: Green-API keys saved on the platform by %s (instance …%s, state %q)", platformUser(c), maskID(r.ID), state)
	go h.SetupWhatsApp(context.Background())
	c.JSON(http.StatusOK, h.waStatusView(ctx))
}

// WAForget: DELETE /crm/wa/config: the saved keys are removed.
func (h *PlatformAI) WAForget(c *gin.Context) {
	if !waTeam(c) {
		return
	}
	if h.repo != nil && !h.putServerDoc(c.Request.Context(), waCredsDoc, "", true, platformUser(c)) {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "store_failed"})
		return
	}
	waSaved.Store(nil)
	c.JSON(http.StatusOK, h.waStatusView(c.Request.Context()))
}

// WAQR: GET /crm/wa/qr: the QR code to scan (a PNG as base64), or
// {type:"alreadyLogged"} when the number is linked already.
func (h *PlatformAI) WAQR(c *gin.Context) {
	if !waTeam(c) {
		return
	}
	c.Header("Cache-Control", "no-store")
	g := greenFromEnv()
	if g == nil {
		c.JSON(http.StatusOK, gin.H{"type": "error", "message": "Сначала вставьте ключи Green-API"})
		return
	}
	ctx, cancel := context.WithTimeout(c.Request.Context(), 20*time.Second)
	defer cancel()
	out, err := h.greenCall(ctx, g, "qr", "GET", nil)
	if err != nil {
		reason, hint := waHuman("", err)
		c.JSON(http.StatusOK, gin.H{"type": "error", "message": reason + ". " + hint})
		return
	}
	typ, _ := out["type"].(string)
	msg, _ := out["message"].(string)
	switch typ {
	case "qrCode":
		c.JSON(http.StatusOK, gin.H{"type": "qrCode", "png": msg})
	case "alreadyLogged":
		c.JSON(http.StatusOK, gin.H{"type": "alreadyLogged"})
	default:
		if msg == "" {
			msg = "Green-API пока не выдал QR-код. Подождите 10 секунд"
		}
		c.JSON(http.StatusOK, gin.H{"type": "wait", "message": msg})
	}
}

func (h *PlatformAI) putServerDoc(ctx context.Context, key, val string, deleted bool, by string) bool {
	for try := 0; try < 4; try++ {
		ver := 0
		if d, err := h.repo.GetDoc(ctx, "server", key); err == nil && d != nil {
			ver = d.Version
		}
		if ver == 0 && deleted {
			return true
		}
		if _, err := h.repo.PutDoc(ctx, "server", key, ver, val, deleted, by); err == nil {
			return true
		}
	}
	return false
}
