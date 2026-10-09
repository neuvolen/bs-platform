package http

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	pg "github.com/bnursik/business_surgery_backend/internal/repository/pg"
	"github.com/bnursik/business_surgery_backend/pkg/auth"
	"github.com/gin-gonic/gin"
	"github.com/golang-jwt/jwt/v5"
)

// PlatformAuthHandler logs the team into the platform with Telegram:
// the Login Widget in a normal browser, or Mini App initData inside Telegram.
// No passwords: Telegram proves who the person is, the server checks the
// signature with the bot token and issues its own JWT.
type PlatformAuthHandler struct {
	botToken string
	team     map[int64]string // Telegram id -> display name
	jwt      *auth.Manager
	client   *http.Client
	repo     *pg.PlatformRepo // set by NewPlatformModule
	names    *residentNames
	leads    *LeadFunnel // the CRM card of a lead who logs in (NewPlatformModule)

	access *AssistAccess // assist_access.go: sessions, assistants, personal links

	mu          sync.Mutex
	botUsername string
	botChecked  time.Time
}

// Sessions are long: the platform is a daily work tool on trusted devices.
const platformSessionTTL = 30 * 24 * time.Hour

// A login proof older than this is refused (replay protection).
const platformLoginMaxAge = 24 * time.Hour

func NewPlatformAuthHandler(botToken, teamSpec, jwtSecret string) *PlatformAuthHandler {
	return &PlatformAuthHandler{
		botToken: strings.TrimSpace(botToken),
		team:     ParsePlatformTeam(teamSpec),
		jwt:      auth.NewManager(jwtSecret, platformSessionTTL, platformSessionTTL),
		client:   &http.Client{Timeout: 6 * time.Second},
	}
}

// DefaultPlatformTeam is used when PLATFORM_TEAM is not set.
const DefaultPlatformTeam = "453800951:Рустам,1285596249:Береке"

// ParsePlatformTeam reads "id:Name,id:Name". A bare id gets no name.
func ParsePlatformTeam(spec string) map[int64]string {
	if strings.TrimSpace(spec) == "" {
		spec = DefaultPlatformTeam
	}
	out := map[int64]string{}
	for _, part := range strings.Split(spec, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		idStr, name, _ := strings.Cut(part, ":")
		id, err := strconv.ParseInt(strings.TrimSpace(idStr), 10, 64)
		if err != nil || id <= 0 {
			continue
		}
		out[id] = strings.TrimSpace(name)
	}
	return out
}

// Config godoc
// @Summary      Platform login settings
// @Description  Public. Tells the login screen which bot to use for the Telegram widget.
// @Tags         platform
// @Produce      json
// @Success      200 {object} map[string]any
// @Router       /api/v1/platform/config [get]
func (h *PlatformAuthHandler) Config(c *gin.Context) {
	if h.botToken == "" {
		c.JSON(http.StatusOK, gin.H{"telegram": false, "reason": "TELEGRAM_BOT_TOKEN is not set on the server"})
		return
	}
	name, err := h.username()
	if err != nil {
		c.JSON(http.StatusOK, gin.H{"telegram": false, "reason": "Telegram did not accept the bot token: " + err.Error()})
		return
	}
	// The numeric bot id is the public part of the token; the redirect login needs it.
	id := h.botToken
	if i := strings.Index(id, ":"); i > 0 {
		id = id[:i]
	}
	c.JSON(http.StatusOK, gin.H{"telegram": true, "bot": name, "id": id})
}

func (h *PlatformAuthHandler) username() (string, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.botUsername != "" {
		return h.botUsername, nil
	}
	// Do not hammer Telegram if the token is wrong.
	if time.Since(h.botChecked) < 30*time.Second && !h.botChecked.IsZero() {
		return "", errors.New("recently failed, retry shortly")
	}
	h.botChecked = time.Now()
	resp, err := h.client.Get("https://api.telegram.org/bot" + h.botToken + "/getMe")
	if err != nil {
		return "", errors.New("telegram unreachable")
	}
	defer resp.Body.Close()
	var out struct {
		OK     bool   `json:"ok"`
		Desc   string `json:"description"`
		Result struct {
			Username string `json:"username"`
		} `json:"result"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return "", errors.New("bad telegram response")
	}
	if !out.OK || out.Result.Username == "" {
		return "", errors.New(out.Desc)
	}
	h.botUsername = out.Result.Username
	return h.botUsername, nil
}

type platformLoginReq struct {
	// Login Widget: the fields Telegram passed to data-onauth, as-is.
	Widget map[string]any `json:"widget"`
	// Mini App: Telegram.WebApp.initData, as-is.
	InitData string `json:"initData"`
}

type platformTgUser struct {
	ID        int64
	FirstName string
	LastName  string
	Username  string
	Photo     string
}

// Login godoc
// @Summary      Log in with Telegram
// @Description  Body: {widget:{…fields from the Telegram Login Widget…}} or {initData:"…"} from a Mini App. The team gets role admin, an active resident role resident, anyone else role lead (the lead home only).
// @Tags         platform
// @Accept       json
// @Produce      json
// @Success      200 {object} map[string]any
// @Failure      401 {object} map[string]any
// @Failure      403 {object} map[string]any
// @Router       /api/v1/platform/auth/telegram [post]
func (h *PlatformAuthHandler) Login(c *gin.Context) {
	if h.botToken == "" {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "telegram_not_configured"})
		return
	}
	var req platformLoginReq
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "bad request"})
		return
	}
	var u *platformTgUser
	var err error
	switch {
	case req.InitData != "":
		u, err = verifyTelegramInitData(req.InitData, h.botToken, time.Now())
	case req.Widget != nil:
		u, err = verifyTelegramWidget(req.Widget, h.botToken, time.Now())
	default:
		err = errors.New("no telegram proof")
	}
	if err != nil {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "telegram_check_failed", "detail": err.Error()})
		return
	}
	ctx := c.Request.Context()
	role, name, err := h.roleFor(ctx, u.ID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal server error"})
		return
	}
	if role == "lead" && h.repo != nil {
		// Ассистент резидента (не команда и не резидент): сразу в кабинет
		// резидента, которого вёл последним; остальные в переключателе.
		if served, err := h.repo.ServedBy(ctx, u.ID); err == nil && len(served) > 0 {
			out, err := h.issueAssistant(c, &served[0], "tg:"+strconv.FormatInt(u.ID, 10), tgDisplayName(u), "Telegram", 0)
			if err != nil {
				c.JSON(http.StatusInternalServerError, gin.H{"error": "internal server error"})
				return
			}
			c.JSON(http.StatusOK, out)
			return
		}
		// Anyone else with a valid Telegram login is a lead: the lead home
		// only (every other endpoint refuses the role), and a card in the
		// CRM without a word to the team chat.
		if h.leads != nil {
			if err := h.leads.PlatformLogin(ctx, u); err != nil {
				log.Printf("platform login: lead %d not saved to the CRM: %v", u.ID, err)
			}
		}
	}
	out, err := h.ownSession(c, u.ID, role, name, u.Photo, tgDisplayName(u), "telegram")
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal server error"})
		return
	}
	c.JSON(http.StatusOK, out)
}

func tgDisplayName(u *platformTgUser) string {
	n := strings.TrimSpace(u.FirstName + " " + u.LastName)
	if n == "" && u.Username != "" {
		n = "@" + u.Username
	}
	return n
}

// roleFor: команда (admin), действующий резидент (resident) или лид (lead).
func (h *PlatformAuthHandler) roleFor(ctx context.Context, tg int64) (string, string, error) {
	if name, ok := h.team[tg]; ok {
		return "admin", name, nil
	}
	if h.repo == nil {
		return "lead", "", nil
	}
	rname, active, err := h.repo.ResidentByTg(ctx, tg)
	if err != nil {
		return "", "", err
	}
	if active {
		return "resident", rname, nil
	}
	return "lead", "", nil
}

// roleOf: роль без ошибки (переключатель ассистента: есть ли свой кабинет).
func (h *PlatformAuthHandler) roleOf(ctx context.Context, tg int64) string {
	r, _, err := h.roleFor(ctx, tg)
	if err != nil {
		return "lead"
	}
	return r
}

// ownSession: вход в свой кабинет (Telegram) и ответ, как у входа.
func (h *PlatformAuthHandler) ownSession(c *gin.Context, tg int64, role, name, photo, actorName, kind string) (gin.H, error) {
	if name == "" {
		name = actorName
	}
	sub := "tg:" + strconv.FormatInt(tg, 10)
	access, err := h.issueSession(c, pg.PlatformSession{Sub: sub, Actor: sub, ActorName: firstNonBlank(name, actorName), Kind: kind}, role, nil)
	if err != nil {
		return nil, err
	}
	team := map[string]string{}
	if role == "admin" {
		for id, n := range h.team {
			team["tg:"+strconv.FormatInt(id, 10)] = n
		}
	}
	setSessionCookie(c, access, int(platformSessionTTL.Seconds()))
	return gin.H{
		"token":     access,
		"expiresAt": time.Now().Add(platformSessionTTL).UTC(),
		"user":      gin.H{"id": sub, "name": name, "photo": photo, "role": role},
		"team":      team,
	}, nil
}

// issueOwn: ассистент возвращается в свой кабинет (переключатель); nil, если
// своего кабинета нет (не команда и не резидент).
func (h *PlatformAuthHandler) issueOwn(c *gin.Context, tg int64, actorName, photo string) (gin.H, error) {
	role, name, err := h.roleFor(c.Request.Context(), tg)
	if err != nil {
		return nil, err
	}
	if role == "lead" {
		return nil, nil
	}
	return h.ownSession(c, tg, role, name, photo, actorName, "telegram")
}

// PlatformSessionCookie holds the same JWT as the API token. It only decides
// whether the platform page itself is served; the API keeps using the
// Authorization header.
const PlatformSessionCookie = "bs_session"

func setSessionCookie(c *gin.Context, value string, maxAge int) {
	secure := c.Request.TLS != nil || strings.EqualFold(c.GetHeader("X-Forwarded-Proto"), "https")
	http.SetCookie(c.Writer, &http.Cookie{
		Name: PlatformSessionCookie, Value: value, Path: "/", MaxAge: maxAge,
		HttpOnly: true, Secure: secure, SameSite: http.SameSiteLaxMode,
	})
}

// Logout godoc
// @Summary      Log out of the platform on this device
// @Tags         platform
// @Success      204
// @Router       /api/v1/platform/auth/logout [post]
func (h *PlatformAuthHandler) Logout(c *gin.Context) {
	// the server forgets this device's session too (the list of devices)
	raw := strings.TrimSpace(strings.TrimPrefix(c.GetHeader("Authorization"), "Bearer "))
	if raw == "" {
		raw, _ = c.Cookie(PlatformSessionCookie)
	}
	if sid := h.sessionID(raw); sid != "" && h.repo != nil {
		_ = h.repo.RevokeSession(c.Request.Context(), sid, "")
		if h.access != nil {
			h.access.forget(sid)
		}
	}
	setSessionCookie(c, "", -1)
	c.Status(http.StatusNoContent)
}

// sessionID: the sid of a valid platform token ("" when none).
func (h *PlatformAuthHandler) sessionID(raw string) string {
	if raw == "" {
		return ""
	}
	tkn, err := jwt.Parse(raw, func(t *jwt.Token) (any, error) {
		if _, ok := t.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, jwt.ErrSignatureInvalid
		}
		return h.jwt.Secret(), nil
	})
	if err != nil || !tkn.Valid {
		return ""
	}
	cl, _ := tkn.Claims.(jwt.MapClaims)
	sid, _ := cl["sid"].(string)
	return sid
}

type residentsSyncReq struct {
	TS        int64                 `json:"ts"`
	Residents []pg.PlatformResident `json:"residents"`
}

// SyncResidents godoc
// @Summary      Resident list from the Google Sheet
// @Description  Sent by the Apps Script. Header X-BS-Signature = hex HMAC-SHA256 of the raw body with the bot token as key. The list replaces the previous one; residents not in it lose access.
// @Tags         platform
// @Accept       json
// @Produce      json
// @Success      200 {object} map[string]any
// @Failure      401 {object} map[string]any
// @Router       /api/v1/platform/residents/sync [post]
func (h *PlatformAuthHandler) SyncResidents(c *gin.Context) {
	if h.botToken == "" {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "telegram_not_configured"})
		return
	}
	body, err := io.ReadAll(http.MaxBytesReader(c.Writer, c.Request.Body, 1<<20))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "bad request"})
		return
	}
	m := hmac.New(sha256.New, []byte(h.botToken))
	m.Write(body)
	got, _ := hex.DecodeString(strings.ToLower(strings.TrimSpace(c.GetHeader("X-BS-Signature"))))
	if !hmac.Equal(m.Sum(nil), got) {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "bad_signature"})
		return
	}
	var req residentsSyncReq
	if err := json.Unmarshal(body, &req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "bad json"})
		return
	}
	// A captured old request must not roll the list back.
	if d := time.Since(time.Unix(req.TS, 0)); d > time.Hour || d < -5*time.Minute {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "stale_request"})
		return
	}
	// An empty list would lock every resident out at once. That is never a
	// real state of the club, only a broken read of the sheet: refuse it.
	valid := 0
	for _, p := range req.Residents {
		if p.TgID > 0 && strings.TrimSpace(p.Name) != "" {
			valid++
		}
	}
	if valid == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "empty_list"})
		return
	}
	n, err := h.repo.ReplaceResidents(c.Request.Context(), req.Residents)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal server error"})
		return
	}
	if h.names != nil {
		h.names.forget()
	}
	c.JSON(http.StatusOK, gin.H{"saved": n})
}

// verifyTelegramWidget checks the Login Widget signature:
// hash == hex(HMAC_SHA256(key = SHA256(bot_token), data_check_string)).
// https://core.telegram.org/widgets/login#checking-authorization
func verifyTelegramWidget(fields map[string]any, botToken string, now time.Time) (*platformTgUser, error) {
	vals := map[string]string{}
	for k, v := range fields {
		switch t := v.(type) {
		case string:
			vals[k] = t
		case float64:
			vals[k] = strconv.FormatFloat(t, 'f', -1, 64)
		case json.Number:
			vals[k] = t.String()
		case bool:
			vals[k] = strconv.FormatBool(t)
		case nil:
		default:
			return nil, fmt.Errorf("unexpected field %s", k)
		}
	}
	hash := vals["hash"]
	if hash == "" {
		return nil, errors.New("no hash")
	}
	delete(vals, "hash")
	secret := sha256.Sum256([]byte(botToken))
	if !checkTelegramHash(vals, secret[:], hash) {
		return nil, errors.New("bad signature")
	}
	if err := checkTelegramAge(vals["auth_date"], now); err != nil {
		return nil, err
	}
	id, err := strconv.ParseInt(vals["id"], 10, 64)
	if err != nil || id <= 0 {
		return nil, errors.New("no user id")
	}
	return &platformTgUser{ID: id, FirstName: vals["first_name"], LastName: vals["last_name"],
		Username: vals["username"], Photo: vals["photo_url"]}, nil
}

// verifyTelegramInitData checks Mini App initData:
// secret = HMAC_SHA256(key = "WebAppData", bot_token), then the same check.
// https://core.telegram.org/bots/webapps#validating-data-received-via-the-mini-app
func verifyTelegramInitData(initData, botToken string, now time.Time) (*platformTgUser, error) {
	q, err := url.ParseQuery(initData)
	if err != nil {
		return nil, errors.New("bad initData")
	}
	vals := map[string]string{}
	for k := range q {
		vals[k] = q.Get(k)
	}
	hash := vals["hash"]
	if hash == "" {
		return nil, errors.New("no hash")
	}
	delete(vals, "hash")
	// Newer clients add an Ed25519 "signature" field; it is part of the
	// data-check string and must stay in, per Telegram's docs.
	m := hmac.New(sha256.New, []byte("WebAppData"))
	m.Write([]byte(botToken))
	if !checkTelegramHash(vals, m.Sum(nil), hash) {
		return nil, errors.New("bad signature")
	}
	if err := checkTelegramAge(vals["auth_date"], now); err != nil {
		return nil, err
	}
	var u struct {
		ID        int64  `json:"id"`
		FirstName string `json:"first_name"`
		LastName  string `json:"last_name"`
		Username  string `json:"username"`
		Photo     string `json:"photo_url"`
	}
	if err := json.Unmarshal([]byte(vals["user"]), &u); err != nil || u.ID <= 0 {
		return nil, errors.New("no user")
	}
	return &platformTgUser{ID: u.ID, FirstName: u.FirstName, LastName: u.LastName, Username: u.Username, Photo: u.Photo}, nil
}

func checkTelegramHash(vals map[string]string, secret []byte, hash string) bool {
	keys := make([]string, 0, len(vals))
	for k := range vals {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	lines := make([]string, 0, len(keys))
	for _, k := range keys {
		lines = append(lines, k+"="+vals[k])
	}
	m := hmac.New(sha256.New, secret)
	m.Write([]byte(strings.Join(lines, "\n")))
	want := m.Sum(nil)
	got, err := hex.DecodeString(strings.ToLower(hash))
	if err != nil {
		return false
	}
	return hmac.Equal(want, got)
}

func checkTelegramAge(authDate string, now time.Time) error {
	ts, err := strconv.ParseInt(authDate, 10, 64)
	if err != nil {
		return errors.New("no auth_date")
	}
	t := time.Unix(ts, 0)
	if now.Sub(t) > platformLoginMaxAge {
		return errors.New("login proof expired, sign in again")
	}
	if t.Sub(now) > 5*time.Minute {
		return errors.New("auth_date in the future")
	}
	return nil
}
