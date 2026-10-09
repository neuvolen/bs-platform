package http

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"log"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	pg "github.com/bnursik/business_surgery_backend/internal/repository/pg"
	"github.com/gin-gonic/gin"
	"github.com/golang-jwt/jwt/v5"
)

// Доступ для ассистента и вход без Telegram.
//
// Резидент в профиле («Доступ для ассистента») выпускает одноразовое
// приглашение на 72 часа. Ассистент открывает ссылку, входит своим Telegram и
// получает сессию в кабинете резидента: токен с sub резидента (все разделы
// работают как у него), с id доступа (as) и id сессии (sid). Права задаёт
// набор (preset): «Задачи и отчёты», «Всё, кроме финансов», «Только просмотр»;
// сервер проверяет их на каждом запросе (Guard), набор берётся из базы, так
// что смена прав и отзыв действуют сразу. Каждая правка ассистента идёт в
// журнал, его видит резидент. Один ассистент может вести нескольких
// резидентов и переключаться между ними.
//
// Вход без Telegram: команда в карточке резидента выпускает личную ссылку на
// 30 дней (для самого резидента или для его ассистента без Telegram), её
// можно отправить в WhatsApp. Ссылка открывает платформу на любом устройстве,
// пока её не отозвали; отзыв закрывает и все сессии, открытые ею.
//
// Все токены (приглашения, ссылки) хранятся только хешем SHA-256; вход по
// ним ограничен по частоте. Каждый вход получает запись в platform_sessions:
// резидент видит свои устройства и может выйти на любом из них.

const (
	assistInviteTTL = 72 * time.Hour
	loginLinkTTL    = 30 * 24 * time.Hour
	assistMaxActive = 5
)

// Наборы прав ассистента.
var assistPresets = []struct{ ID, Name, Hint string }{
	{"tasks", "Задачи и отчёты", "Задания, отчёты, разбор и календарь. Без финансов и настроек"},
	{"nofin", "Всё, кроме финансов", "Всё, что видит резидент, кроме денег: долги, оплаты, штрафы, показатели бизнеса. Без настроек доступа"},
	{"view", "Только просмотр", "Видит задания, разбор и календарь, ничего не меняет"},
}

func assistPresetName(id string) string {
	for _, p := range assistPresets {
		if p.ID == id {
			return p.Name
		}
	}
	return ""
}

// AssistAccess: приглашения, личные ссылки, сессии и проверка прав.
type AssistAccess struct {
	repo  *pg.PlatformRepo
	auth  *PlatformAuthHandler
	names *residentNames
	rl    *rateLimiter
	now   func() time.Time

	mu   sync.Mutex
	sess map[string]sessEntry
	cut  map[string]cutEntry
}

type sessEntry struct {
	s   *pg.PlatformSession
	a   *pg.PlatformAssistant
	err error
	at  time.Time
}

type cutEntry struct {
	at  time.Time
	got time.Time
}

// How long a session's state is trusted from memory. A revoke made here
// drops the entry at once; the TTL only bounds what another instance sees.
const sessCacheTTL = 10 * time.Second

func NewAssistAccess(repo *pg.PlatformRepo, auth *PlatformAuthHandler, names *residentNames) *AssistAccess {
	return &AssistAccess{repo: repo, auth: auth, names: names, rl: newRateLimiter(), now: time.Now,
		sess: map[string]sessEntry{}, cut: map[string]cutEntry{}}
}

// ── токены ──

func newSecretToken() string {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return base64.RawURLEncoding.EncodeToString(b)
}

func hashSecretToken(t string) string {
	s := sha256.Sum256([]byte(t))
	return hex.EncodeToString(s[:])
}

// A token as it comes in a URL: 43 characters of base64url.
func validSecretToken(t string) bool {
	if len(t) != 43 {
		return false
	}
	for _, r := range t {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '_') {
			return false
		}
	}
	return true
}

func newSessionID() string {
	b := make([]byte, 18)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return base64.RawURLEncoding.EncodeToString(b)
}

// linkBase: адрес платформы для ссылок. Ссылки всегда ведут на домен клуба
// (app.bxclub.kz), даже если команда открыла платформу по адресу Railway;
// локальный стенд получает свой адрес.
func linkBase(c *gin.Context) string {
	host := strings.ToLower(c.Request.Host)
	switch {
	case strings.HasPrefix(host, "localhost"), strings.HasPrefix(host, "127."):
		return "http://" + host
	case host == "bxclub.kz" || strings.HasSuffix(host, ".bxclub.kz"):
		return "https://" + host
	}
	return "https://app.bxclub.kz"
}

// deviceOf: «iPhone · Safari» из User-Agent (адрес IP не храним).
func deviceOf(ua string) string {
	l := strings.ToLower(ua)
	osName := ""
	switch {
	case strings.Contains(l, "iphone"):
		osName = "iPhone"
	case strings.Contains(l, "ipad"):
		osName = "iPad"
	case strings.Contains(l, "android"):
		osName = "Android"
	case strings.Contains(l, "mac os"):
		osName = "Mac"
	case strings.Contains(l, "windows"):
		osName = "Windows"
	case strings.Contains(l, "linux"):
		osName = "Linux"
	}
	br := ""
	switch {
	case strings.Contains(l, "telegram"):
		br = "Telegram"
	case strings.Contains(l, "whatsapp"):
		br = "WhatsApp"
	case strings.Contains(l, "edg/"):
		br = "Edge"
	case strings.Contains(l, "yabrowser"):
		br = "Яндекс Браузер"
	case strings.Contains(l, "firefox") || strings.Contains(l, "fxios"):
		br = "Firefox"
	case strings.Contains(l, "chrome") || strings.Contains(l, "crios"):
		br = "Chrome"
	case strings.Contains(l, "safari"):
		br = "Safari"
	}
	switch {
	case osName != "" && br != "":
		return osName + " · " + br
	case osName != "":
		return osName
	case br != "":
		return br
	}
	return "Устройство"
}

// ── выпуск сессии ──

// issueSession: запись сессии и токен с её id. Без хранилища (тесты без
// базы) токен выходит без sid, как раньше.
func (h *PlatformAuthHandler) issueSession(c *gin.Context, s pg.PlatformSession, role string, extra jwt.MapClaims) (string, error) {
	now := time.Now()
	claims := jwt.MapClaims{
		"sub": s.Sub, "role": role, "perms": []string{}, "typ": "access",
		"iat": now.Unix(), "exp": now.Add(platformSessionTTL).Unix(),
	}
	for k, v := range extra {
		claims[k] = v
	}
	if h.repo != nil {
		s.ID = newSessionID()
		s.ExpiresAt = now.Add(platformSessionTTL)
		s.Device = clip(deviceOf(c.GetHeader("User-Agent")), 60)
		s.ActorName = clip(s.ActorName, 80)
		if err := h.repo.CreateSession(c.Request.Context(), s); err != nil {
			return "", err
		}
		claims["sid"] = s.ID
	}
	return jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString(h.jwt.Secret())
}

// assistUser: кто в ответе входа у ассистента (кабинет резидента + кто он).
func assistUser(a *pg.PlatformAssistant, actorName string) gin.H {
	return gin.H{
		"id": "tg:" + strconv.FormatInt(a.ResidentTg, 10), "name": a.ResidentName, "role": "resident",
		"assistant": gin.H{"id": a.ID, "preset": a.Preset, "presetName": assistPresetName(a.Preset),
			"name": actorName, "resident": a.ResidentName},
	}
}

// issueAssistant: сессия ассистента в кабинете резидента, ответ как у входа.
func (h *PlatformAuthHandler) issueAssistant(c *gin.Context, a *pg.PlatformAssistant, actor, actorName, kind string, linkID int64) (gin.H, error) {
	tok, err := h.issueSession(c, pg.PlatformSession{
		Sub: "tg:" + strconv.FormatInt(a.ResidentTg, 10), Actor: actor, ActorName: actorName,
		Kind: "assistant", AssistantID: a.ID, LinkID: linkID,
	}, "resident", jwt.MapClaims{"as": a.ID})
	if err != nil {
		return nil, err
	}
	if h.repo != nil {
		h.repo.TouchAssistant(c.Request.Context(), a.ID)
		_ = h.repo.AssistLog(c.Request.Context(), a.ResidentTg, a.ID, actorName, "Вход на платформу", kind)
	}
	setSessionCookie(c, tok, int(platformSessionTTL.Seconds()))
	return gin.H{"token": tok, "expiresAt": time.Now().Add(platformSessionTTL).UTC(),
		"user": assistUser(a, actorName), "team": gin.H{}}, nil
}

// ── проверка каждого запроса ──

func (x *AssistAccess) session(ctx context.Context, sid string) sessEntry {
	now := x.now()
	x.mu.Lock()
	e, ok := x.sess[sid]
	x.mu.Unlock()
	if ok && now.Sub(e.at) < sessCacheTTL {
		return e
	}
	e = sessEntry{at: now}
	e.s, e.err = x.repo.SessionByID(ctx, sid)
	if e.err == nil && e.s.AssistantID != 0 {
		e.a, e.err = x.repo.AssistantByID(ctx, e.s.AssistantID)
	}
	if e.err == nil && e.s.LastSeen.Before(now.Add(-5*time.Minute)) {
		go x.repo.TouchSession(context.Background(), sid)
		e.s.LastSeen = now
	}
	if e.err != nil && !errors.Is(e.err, pg.ErrAssistNotFound) {
		return e // сбой базы не кешируем
	}
	x.mu.Lock()
	if len(x.sess) > 5000 {
		x.sess = map[string]sessEntry{}
	}
	x.sess[sid] = e
	x.mu.Unlock()
	return e
}

func (x *AssistAccess) forget(sids ...string) {
	x.mu.Lock()
	if len(sids) == 0 {
		x.sess = map[string]sessEntry{}
		x.cut = map[string]cutEntry{}
	}
	for _, s := range sids {
		delete(x.sess, s)
	}
	x.mu.Unlock()
}

func (x *AssistAccess) cutoff(ctx context.Context, sub string) time.Time {
	now := x.now()
	x.mu.Lock()
	e, ok := x.cut[sub]
	x.mu.Unlock()
	if ok && now.Sub(e.got) < 30*time.Second {
		return e.at
	}
	at, err := x.repo.SessionCutoff(ctx, sub)
	if err != nil {
		return time.Time{} // без базы старые токены продолжают работать
	}
	x.mu.Lock()
	if len(x.cut) > 5000 {
		x.cut = map[string]cutEntry{}
	}
	x.cut[sub] = cutEntry{at: at, got: now}
	x.mu.Unlock()
	return at
}

func claimInt(v any) int64 {
	switch t := v.(type) {
	case float64:
		return int64(t)
	case int64:
		return t
	case int:
		return int64(t)
	case string:
		n, _ := strconv.ParseInt(t, 10, 64)
		return n
	}
	return 0
}

// Guard: middleware.SessionGuard.
func (x *AssistAccess) Guard(c *gin.Context, claims jwt.MapClaims) (bool, func()) {
	ctx := c.Request.Context()
	sub, _ := claims["sub"].(string)
	sid, _ := claims["sid"].(string)
	if sid == "" {
		if claims["as"] != nil {
			c.JSON(http.StatusUnauthorized, gin.H{"error": "session_revoked"})
			return false, nil
		}
		if at := x.cutoff(ctx, sub); !at.IsZero() && claimInt(claims["iat"]) < at.Unix() {
			c.JSON(http.StatusUnauthorized, gin.H{"error": "session_revoked"})
			return false, nil
		}
		return true, nil
	}
	e := x.session(ctx, sid)
	if e.err != nil {
		if errors.Is(e.err, pg.ErrAssistNotFound) {
			c.JSON(http.StatusUnauthorized, gin.H{"error": "session_revoked"})
			return false, nil
		}
		log.Printf("session check %s: %v", sub, e.err)
		if claims["as"] != nil { // права ассистента без базы не проверить
			c.JSON(http.StatusServiceUnavailable, gin.H{"error": "try_again"})
			return false, nil
		}
		return true, nil
	}
	s := e.s
	if s.RevokedAt != nil || !x.now().Before(s.ExpiresAt) || s.Sub != sub {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "session_revoked"})
		return false, nil
	}
	c.Set("sid", sid)
	if s.Kind != "assistant" {
		return true, nil
	}
	a := e.a
	if a == nil || !a.Active() || claimInt(claims["as"]) != a.ID {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "assistant_revoked"})
		return false, nil
	}
	c.Set("assist", a)
	c.Set("assistActor", s.Actor)
	c.Set("assistActorName", s.ActorName)
	method, path := c.Request.Method, c.FullPath()
	if !assistAllowed(a.Preset, method, path, c.Param("key")) {
		c.JSON(http.StatusForbidden, gin.H{"error": "forbidden", "reason": "assistant_preset",
			"detail": "У ассистента нет прав на это действие: " + assistPresetName(a.Preset)})
		return false, nil
	}
	if method == http.MethodGet || method == http.MethodHead || assistReadPost[path] {
		return true, nil
	}
	return true, func() {
		if c.Writer.Status() >= 400 {
			return
		}
		action, detail := x.assistAction(c, method, path)
		lctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		if err := x.repo.AssistLog(lctx, a.ResidentTg, a.ID, s.ActorName, action, detail); err != nil {
			log.Printf("assist log %d: %v", a.ID, err)
		}
	}
}

// Запросы POST, которые ничего не меняют (документ на печать, присутствие на доске).
var assistReadPost = map[string]bool{
	"/api/v1/platform/presence":      true,
	"/api/v1/platform/razbor/pdf":    true,
	"/api/v1/platform/gallup/pdf":    true,
	"/api/v1/platform/gallup/plus":   true, // R71: the second half of the analysis, read only
	"/api/v1/platform/tts":           true,
	"/api/v1/platform/assist/switch": true,
}

// Никогда для ассистента: управление доступом, входами, Google и ключами.
var assistNever = []string{
	"/api/v1/platform/assist/invite", "/api/v1/platform/assist/revoke", "/api/v1/platform/assist/preset",
	"/api/v1/platform/sessions", "/api/v1/platform/access", "/api/v1/platform/import", "/api/v1/platform/ai/key",
	"/api/v1/platform/gcal/connect", "/api/v1/platform/gcal/settings", "/api/v1/platform/gcal/disconnect",
}

// Деньги резидента: оплаты, продление, отчёты об оплате, финансовый прогноз.
var assistFinance = []string{
	"/api/v1/platform/sales/me", "/api/v1/platform/ai/forecast",
}

// Личные настройки устройства резидента (тема, порядок меню, обучение):
// ассистент их не перезаписывает.
var assistDeviceKeys = map[string]bool{"bs_theme": true, "bs_order": true, "bs_onboard": true, "bs_me": true, "bs_an_state": true}

func hasPrefixAny(p string, list []string) bool {
	for _, x := range list {
		if p == x || strings.HasPrefix(p, x+"/") {
			return true
		}
	}
	return false
}

// assistAllowed: может ли ассистент с этим набором прав сделать запрос.
func assistAllowed(preset, method, path, key string) bool {
	read := method == http.MethodGet || method == http.MethodHead || method == http.MethodOptions
	if path == "" {
		return false
	}
	// управление доступом и деньги резидента: ни при каком наборе прав
	if hasPrefixAny(path, assistNever) || hasPrefixAny(path, assistFinance) {
		return false
	}
	if read || assistReadPost[path] {
		return true
	}
	if path == "/api/v1/platform/docs/:key" && assistDeviceKeys[key] {
		return false
	}
	switch preset {
	case "view":
		return false
	case "tasks":
		switch path {
		case "/api/v1/platform/boards/:id", "/api/v1/platform/mycal", "/api/v1/platform/files", "/api/v1/platform/gcal/sync":
			return method != http.MethodDelete
		case "/api/v1/platform/docs/:key":
			return key == "bs_mycal"
		}
		return false
	case "nofin":
		return true
	}
	return false
}

// assistHidesFinance: деньги резидента (долги, оплаты, штрафы) ассистенту не
// приходят ни при каком наборе прав: «Только просмотр» тоже про задачи, не про счета.
func assistHidesFinance(c *gin.Context) bool { return assistOf(c) != nil }

func assistOf(c *gin.Context) *pg.PlatformAssistant {
	v, ok := c.Get("assist")
	if !ok {
		return nil
	}
	a, _ := v.(*pg.PlatformAssistant)
	return a
}

func (x *AssistAccess) assistAction(c *gin.Context, method, path string) (string, string) {
	switch path {
	case "/api/v1/platform/boards/:id":
		name := ""
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		if b, err := x.repo.GetBoard(ctx, c.Param("id")); err == nil && b != nil {
			name = b.Name
		}
		return "Изменения в разборе", clip(name, 80)
	case "/api/v1/platform/docs/:key":
		if c.Param("key") == "bs_mycal" {
			return "Календарь работы", ""
		}
		return "Изменения в разделе", c.Param("key")
	case "/api/v1/platform/mycal":
		return "Календарь работы", ""
	case "/api/v1/platform/files":
		return "Загружен файл", ""
	case "/api/v1/platform/gcal/sync":
		return "Синхронизация с Google Календарём", ""
	}
	p := strings.TrimPrefix(path, "/api/v1/")
	return "Действие на платформе", method + " " + p
}

// ── частота попыток ──

type rateLimiter struct {
	mu sync.Mutex
	m  map[string][]time.Time
}

func newRateLimiter() *rateLimiter { return &rateLimiter{m: map[string][]time.Time{}} }

func (r *rateLimiter) allow(key string, n int, win time.Duration) bool {
	now := time.Now()
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.m) > 20000 {
		r.m = map[string][]time.Time{}
	}
	keep := r.m[key][:0]
	for _, t := range r.m[key] {
		if now.Sub(t) < win {
			keep = append(keep, t)
		}
	}
	if len(keep) >= n {
		r.m[key] = keep
		return false
	}
	r.m[key] = append(keep, now)
	return true
}

func tooMany(c *gin.Context) {
	c.Header("Retry-After", "600")
	c.JSON(http.StatusTooManyRequests, gin.H{"error": "too_many", "detail": "Слишком много попыток. Подождите 10 минут"})
}

// ── кто спрашивает ──

func (x *AssistAccess) isTeam(c *gin.Context) bool {
	r := platformRole(c)
	return (r == "admin" || r == "moderator") && assistOf(c) == nil
}

// ownResident: резидент в своём кабинете (не ассистент): tg и имя.
func (x *AssistAccess) ownResident(c *gin.Context) (int64, string, bool) {
	if !isResident(c) || assistOf(c) != nil {
		return 0, "", false
	}
	tg := platformTgID(c)
	name, active := x.names.get(c.Request.Context(), tg)
	if !active || tg == 0 {
		return 0, "", false
	}
	return tg, name, true
}

func sidOf(c *gin.Context) string {
	v, _ := c.Get("sid")
	s, _ := v.(string)
	return s
}

func assistJSON(list []pg.PlatformAssistant) []gin.H {
	out := make([]gin.H, 0, len(list))
	for _, a := range list {
		st := "active"
		switch {
		case a.RevokedAt != nil:
			st = "revoked"
		case a.AcceptedAt == nil:
			st = "pending"
		}
		name := a.AssistantName
		if name == "" {
			name = a.Label
		}
		out = append(out, gin.H{"id": a.ID, "name": name, "label": a.Label, "preset": a.Preset, "presetName": assistPresetName(a.Preset),
			"status": st, "telegram": a.Telegram, "createdAt": a.CreatedAt, "acceptedAt": a.AcceptedAt, "lastUsed": a.LastUsed,
			"inviteExpires": a.InviteExpires, "revokedAt": a.RevokedAt})
	}
	return out
}

func presetsJSON() []gin.H {
	out := make([]gin.H, 0, len(assistPresets))
	for _, p := range assistPresets {
		out = append(out, gin.H{"id": p.ID, "name": p.Name, "hint": p.Hint})
	}
	return out
}

// ── API резидента и ассистента ──

// State: GET /api/v1/platform/assist. Резиденту: его ассистенты, журнал и
// входы; ассистенту: за кого он работает и кого ещё ведёт (переключатель).
func (x *AssistAccess) State(c *gin.Context) {
	ctx := c.Request.Context()
	if a := assistOf(c); a != nil {
		actor, _ := c.Get("assistActor")
		as, _ := actor.(string)
		served := []pg.PlatformAssistant{}
		if tg, ok := strings.CutPrefix(as, "tg:"); ok {
			id, _ := strconv.ParseInt(tg, 10, 64)
			served, _ = x.repo.ServedBy(ctx, id)
		} else {
			served = append(served, *a)
		}
		list := make([]gin.H, 0, len(served))
		for _, s := range served {
			list = append(list, gin.H{"id": s.ID, "resident": s.ResidentName, "preset": s.Preset, "presetName": assistPresetName(s.Preset), "current": s.ID == a.ID})
		}
		own := false
		if tg, ok := strings.CutPrefix(as, "tg:"); ok {
			id, _ := strconv.ParseInt(tg, 10, 64)
			own = x.auth.roleOf(ctx, id) != "lead"
		}
		name, _ := c.Get("assistActorName")
		c.JSON(http.StatusOK, gin.H{"mode": "assistant", "me": name,
			"acting":    gin.H{"id": a.ID, "resident": a.ResidentName, "preset": a.Preset, "presetName": assistPresetName(a.Preset)},
			"residents": list, "own": own})
		return
	}
	tg, name, ok := x.ownResident(c)
	if !ok {
		c.JSON(http.StatusOK, gin.H{"mode": platformRole(c)})
		return
	}
	list, err := x.repo.AssistantsOf(ctx, tg)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal server error"})
		return
	}
	logs, err := x.repo.AssistLogOf(ctx, tg, 100)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal server error"})
		return
	}
	sess, err := x.repo.SessionsOf(ctx, platformUser(c), []string{"telegram", "link"})
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal server error"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"mode": "resident", "resident": name, "presets": presetsJSON(),
		"assistants": assistJSON(list), "log": logs, "sessions": sessJSON(sess, sidOf(c)), "current": sidOf(c)})
}

func sessJSON(list []pg.PlatformSession, current string) []gin.H {
	out := make([]gin.H, 0, len(list))
	for _, s := range list {
		how := map[string]string{"telegram": "Telegram", "link": "Личная ссылка", "assistant": "Ассистент"}[s.Kind]
		out = append(out, gin.H{"id": s.ID, "kind": s.Kind, "how": how, "name": s.ActorName, "device": s.Device,
			"createdAt": s.CreatedAt, "lastSeen": s.LastSeen, "current": s.ID == current, "assistantId": s.AssistantID})
	}
	return out
}

type assistInviteReq struct {
	Preset string `json:"preset"`
	Label  string `json:"label"`
}

// Invite: POST /api/v1/platform/assist/invite {preset, label}. Ссылка
// показывается один раз: на сервере остаётся только её хеш.
func (x *AssistAccess) Invite(c *gin.Context) {
	tg, name, ok := x.ownResident(c)
	if !ok {
		forbidden(c, "resident_only")
		return
	}
	var req assistInviteReq
	_ = c.ShouldBindJSON(&req)
	if req.Preset == "" {
		req.Preset = "tasks"
	}
	if assistPresetName(req.Preset) == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "bad_preset"})
		return
	}
	if !x.rl.allow("invite:"+platformUser(c), 10, time.Hour) {
		tooMany(c)
		return
	}
	ctx := c.Request.Context()
	list, err := x.repo.AssistantsOf(ctx, tg)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal server error"})
		return
	}
	n := 0
	for _, a := range list {
		if a.RevokedAt == nil {
			n++
		}
	}
	if n >= assistMaxActive {
		c.JSON(http.StatusConflict, gin.H{"error": "too_many_assistants", "detail": "Не больше 5 ассистентов и приглашений. Отзовите лишний доступ"})
		return
	}
	tok := newSecretToken()
	exp := x.now().Add(assistInviteTTL)
	a, err := x.repo.CreateAssistInvite(ctx, tg, name, clip(req.Label, 60), req.Preset, hashSecretToken(tok), exp, platformUser(c))
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal server error"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"url": linkBase(c) + "/assist/" + tok, "expiresAt": exp.UTC(), "assistant": assistJSON([]pg.PlatformAssistant{*a})[0]})
}

type idReq struct {
	ID     int64  `json:"id"`
	SID    string `json:"sid"`
	Preset string `json:"preset"`
	Name   string `json:"name"`
}

// mayManage: доступ этого ассистента может менять тот, кто спрашивает
// (резидент: только своих; команда: любых).
func (x *AssistAccess) mayManage(c *gin.Context, id int64) (*pg.PlatformAssistant, bool) {
	a, err := x.repo.AssistantByID(c.Request.Context(), id)
	if err != nil {
		return nil, false
	}
	if x.isTeam(c) {
		return a, true
	}
	tg, _, ok := x.ownResident(c)
	return a, ok && a.ResidentTg == tg
}

// Revoke: POST /assist/revoke {id}: резидент или команда закрывает доступ.
func (x *AssistAccess) Revoke(c *gin.Context) {
	var req idReq
	if err := c.ShouldBindJSON(&req); err != nil || req.ID <= 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "bad request"})
		return
	}
	a, ok := x.mayManage(c, req.ID)
	if !ok {
		c.JSON(http.StatusNotFound, gin.H{"error": "not_found"})
		return
	}
	if err := x.repo.RevokeAssistant(c.Request.Context(), a.ID, platformUser(c)); err != nil && !errors.Is(err, pg.ErrAssistNotFound) {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal server error"})
		return
	}
	x.forget()
	if a.AcceptedAt != nil {
		by := "Резидент"
		if x.isTeam(c) {
			by = "Команда BS"
		}
		_ = x.repo.AssistLog(c.Request.Context(), a.ResidentTg, a.ID, firstNonBlank(a.AssistantName, a.Label), "Доступ отозван", by)
	}
	c.JSON(http.StatusOK, gin.H{"ok": true})
}

func firstNonBlank(v ...string) string {
	for _, s := range v {
		if strings.TrimSpace(s) != "" {
			return s
		}
	}
	return ""
}

// Preset: POST /assist/preset {id, preset}: права меняются сразу.
func (x *AssistAccess) Preset(c *gin.Context) {
	var req idReq
	if err := c.ShouldBindJSON(&req); err != nil || req.ID <= 0 || assistPresetName(req.Preset) == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "bad request"})
		return
	}
	a, ok := x.mayManage(c, req.ID)
	if !ok {
		c.JSON(http.StatusNotFound, gin.H{"error": "not_found"})
		return
	}
	if err := x.repo.SetAssistPreset(c.Request.Context(), a.ID, req.Preset); err != nil {
		if errors.Is(err, pg.ErrAssistNotFound) {
			c.JSON(http.StatusNotFound, gin.H{"error": "not_found"})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal server error"})
		return
	}
	x.forget()
	c.JSON(http.StatusOK, gin.H{"ok": true, "preset": req.Preset, "presetName": assistPresetName(req.Preset)})
}

// Switch: POST /assist/switch {id}: ассистент переходит к другому резиденту
// (id доступа) или в свой кабинет (id 0). Прежняя сессия закрывается.
func (x *AssistAccess) Switch(c *gin.Context) {
	cur := assistOf(c)
	if cur == nil {
		forbidden(c, "assistant_only")
		return
	}
	var req idReq
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "bad request"})
		return
	}
	actor, _ := c.Get("assistActor")
	as, _ := actor.(string)
	tgs, ok := strings.CutPrefix(as, "tg:")
	tgID, _ := strconv.ParseInt(tgs, 10, 64)
	if !ok || tgID <= 0 {
		forbidden(c, "no_telegram")
		return
	}
	ctx := c.Request.Context()
	nameV, _ := c.Get("assistActorName")
	actorName, _ := nameV.(string)
	var out gin.H
	var err error
	if req.ID == 0 {
		out, err = x.auth.issueOwn(c, tgID, actorName, "")
		if err == nil && out == nil {
			forbidden(c, "no_own_account")
			return
		}
	} else {
		a, gerr := x.repo.AssistantByID(ctx, req.ID)
		if gerr != nil || a.AssistantTg != tgID || !a.Active() {
			c.JSON(http.StatusNotFound, gin.H{"error": "not_found"})
			return
		}
		out, err = x.auth.issueAssistant(c, a, as, actorName, "переключение", 0)
	}
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal server error"})
		return
	}
	if sid := sidOf(c); sid != "" {
		_ = x.repo.RevokeSession(ctx, sid, "")
		x.forget(sid)
	}
	c.JSON(http.StatusOK, out)
}

// Sessions: GET /sessions: свои входы (Telegram и ссылки) с текущим.
func (x *AssistAccess) Sessions(c *gin.Context) {
	if assistOf(c) != nil {
		forbidden(c, "assistant")
		return
	}
	list, err := x.repo.SessionsOf(c.Request.Context(), platformUser(c), []string{"telegram", "link"})
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal server error"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"sessions": sessJSON(list, sidOf(c)), "current": sidOf(c)})
}

// RevokeSession: POST /sessions/revoke {sid}: выйти на этом устройстве
// (свой вход или вход своего ассистента).
func (x *AssistAccess) RevokeSession(c *gin.Context) {
	var req idReq
	if err := c.ShouldBindJSON(&req); err != nil || req.SID == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "bad request"})
		return
	}
	if err := x.repo.RevokeSession(c.Request.Context(), req.SID, platformUser(c)); err != nil {
		if errors.Is(err, pg.ErrAssistNotFound) {
			c.JSON(http.StatusNotFound, gin.H{"error": "not_found"})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal server error"})
		return
	}
	x.forget(req.SID)
	c.JSON(http.StatusOK, gin.H{"ok": true, "self": req.SID == sidOf(c)})
}

// RevokeOthers: POST /sessions/revoke-others: выйти везде, кроме этого устройства.
func (x *AssistAccess) RevokeOthers(c *gin.Context) {
	n, err := x.repo.RevokeOtherSessions(c.Request.Context(), platformUser(c), sidOf(c))
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal server error"})
		return
	}
	x.forget()
	c.JSON(http.StatusOK, gin.H{"ok": true, "revoked": n})
}

// ── API команды (карточка резидента) ──

func (x *AssistAccess) teamOnly(c *gin.Context) bool {
	if !x.isTeam(c) {
		forbidden(c, "team_only")
		return false
	}
	return true
}

// TeamResident: GET /access/resident?name=: ассистенты, личные ссылки и входы резидента.
func (x *AssistAccess) TeamResident(c *gin.Context) {
	if !x.teamOnly(c) {
		return
	}
	ctx := c.Request.Context()
	tg, exact, telegram, err := x.repo.ResidentForAccess(ctx, c.Query("name"))
	if errors.Is(err, pg.ErrAssistNotFound) {
		c.JSON(http.StatusNotFound, gin.H{"error": "not_found", "detail": "Резидент не найден среди действующих"})
		return
	}
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal server error"})
		return
	}
	list, err1 := x.repo.AssistantsOf(ctx, tg)
	links, err2 := x.repo.LinksOf(ctx, tg)
	sess, err3 := x.repo.SessionsOf(ctx, "tg:"+strconv.FormatInt(tg, 10), nil)
	if err1 != nil || err2 != nil || err3 != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal server error"})
		return
	}
	names := map[int64]string{}
	for _, a := range list {
		names[a.ID] = firstNonBlank(a.AssistantName, a.Label)
	}
	lj := make([]gin.H, 0, len(links))
	for _, l := range links {
		lj = append(lj, gin.H{"id": l.ID, "for": firstNonBlank(names[l.AssistantID], l.ResidentName), "assistant": l.AssistantID != 0,
			"createdAt": l.CreatedAt, "expiresAt": l.ExpiresAt, "lastUsed": l.LastUsed, "uses": l.Uses})
	}
	c.JSON(http.StatusOK, gin.H{"resident": exact, "telegram": telegram, "presets": presetsJSON(),
		"assistants": assistJSON(list), "links": lj, "sessions": sessJSON(sess, "")})
}

type linkReq struct {
	Name          string `json:"name"`
	AssistantID   int64  `json:"assistantId"`
	AssistantName string `json:"assistantName"`
	Preset        string `json:"preset"`
}

// TeamLink: POST /access/link {name, assistantId?|assistantName?+preset}:
// личная ссылка входа на 30 дней. Показывается один раз.
func (x *AssistAccess) TeamLink(c *gin.Context) {
	if !x.teamOnly(c) {
		return
	}
	var req linkReq
	if err := c.ShouldBindJSON(&req); err != nil || strings.TrimSpace(req.Name) == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "bad request"})
		return
	}
	if !x.rl.allow("link:"+platformUser(c), 30, time.Hour) {
		tooMany(c)
		return
	}
	ctx := c.Request.Context()
	tg, exact, _, err := x.repo.ResidentForAccess(ctx, req.Name)
	if errors.Is(err, pg.ErrAssistNotFound) {
		c.JSON(http.StatusNotFound, gin.H{"error": "not_found", "detail": "Резидент не найден среди действующих"})
		return
	}
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal server error"})
		return
	}
	if tg > 0 {
		if _, active := x.names.get(ctx, tg); !active {
			c.JSON(http.StatusConflict, gin.H{"error": "not_on_platform", "detail": "Резидент с этим Chat ID ещё не в списке платформы. Подождите синхронизацию таблицы (до часа)"})
			return
		}
	}
	var asst *pg.PlatformAssistant
	switch {
	case req.AssistantID > 0:
		a, err := x.repo.AssistantByID(ctx, req.AssistantID)
		if err != nil || a.ResidentTg != tg || !a.Active() {
			c.JSON(http.StatusNotFound, gin.H{"error": "not_found"})
			return
		}
		asst = a
	case strings.TrimSpace(req.AssistantName) != "":
		if req.Preset == "" {
			req.Preset = "tasks"
		}
		if assistPresetName(req.Preset) == "" {
			c.JSON(http.StatusBadRequest, gin.H{"error": "bad_preset"})
			return
		}
		a, err := x.repo.CreateAssistantNoTelegram(ctx, tg, exact, clip(req.AssistantName, 60), req.Preset, platformUser(c))
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "internal server error"})
			return
		}
		asst = a
	}
	tok := newSecretToken()
	exp := x.now().Add(loginLinkTTL)
	var aid int64
	if asst != nil {
		aid = asst.ID
	}
	l, err := x.repo.CreateLoginLink(ctx, hashSecretToken(tok), tg, exact, aid, exp, platformUser(c))
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal server error"})
		return
	}
	who := exact
	if asst != nil {
		who = firstNonBlank(asst.AssistantName, asst.Label)
	}
	c.JSON(http.StatusOK, gin.H{"url": linkBase(c) + "/in/" + tok, "id": l.ID, "for": who, "expiresAt": exp.UTC()})
}

// TeamRevokeLink: POST /access/link/revoke {id}.
func (x *AssistAccess) TeamRevokeLink(c *gin.Context) {
	if !x.teamOnly(c) {
		return
	}
	var req idReq
	if err := c.ShouldBindJSON(&req); err != nil || req.ID <= 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "bad request"})
		return
	}
	if _, err := x.repo.RevokeLink(c.Request.Context(), req.ID, platformUser(c)); err != nil {
		if errors.Is(err, pg.ErrAssistNotFound) {
			c.JSON(http.StatusNotFound, gin.H{"error": "not_found"})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal server error"})
		return
	}
	x.forget()
	c.JSON(http.StatusOK, gin.H{"ok": true})
}

// TeamRevokeSession: POST /access/session/revoke {sid}.
func (x *AssistAccess) TeamRevokeSession(c *gin.Context) {
	if !x.teamOnly(c) {
		return
	}
	var req idReq
	if err := c.ShouldBindJSON(&req); err != nil || req.SID == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "bad request"})
		return
	}
	if err := x.repo.RevokeSession(c.Request.Context(), req.SID, ""); err != nil {
		if errors.Is(err, pg.ErrAssistNotFound) {
			c.JSON(http.StatusNotFound, gin.H{"error": "not_found"})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal server error"})
		return
	}
	x.forget(req.SID)
	c.JSON(http.StatusOK, gin.H{"ok": true})
}

// ── публичное: принять приглашение, войти по ссылке ──

type acceptReq struct {
	Token    string         `json:"token"`
	Widget   map[string]any `json:"widget"`
	InitData string         `json:"initData"`
}

// InviteInfo: GET /api/v1/platform/assist/invite-info?t=: что за приглашение
// (страница приглашения показывает, к кому и с какими правами).
func (x *AssistAccess) InviteInfo(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	t := c.Query("t")
	if !x.rl.allow("peek:"+c.ClientIP(), 30, 10*time.Minute) {
		tooMany(c)
		return
	}
	if !validSecretToken(t) {
		c.JSON(http.StatusNotFound, gin.H{"error": "invalid"})
		return
	}
	a, err := x.repo.InviteByHash(c.Request.Context(), hashSecretToken(t))
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "invalid"})
		return
	}
	first := strings.Fields(a.ResidentName)
	who := a.ResidentName
	if len(first) > 0 {
		who = first[0]
	}
	c.JSON(http.StatusOK, gin.H{"resident": who, "preset": a.Preset, "presetName": assistPresetName(a.Preset), "expiresAt": a.InviteExpires})
}

// Accept: POST /api/v1/platform/assist/accept {token, widget|initData}.
// Telegram подтверждает, кто ассистент; приглашение одноразовое.
func (x *AssistAccess) Accept(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	if x.auth.botToken == "" {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "telegram_not_configured"})
		return
	}
	if !x.rl.allow("accept:"+c.ClientIP(), 10, 10*time.Minute) {
		tooMany(c)
		return
	}
	var req acceptReq
	if err := c.ShouldBindJSON(&req); err != nil || !validSecretToken(req.Token) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "bad request"})
		return
	}
	var u *platformTgUser
	var err error
	switch {
	case req.InitData != "":
		u, err = verifyTelegramInitData(req.InitData, x.auth.botToken, time.Now())
	case req.Widget != nil:
		u, err = verifyTelegramWidget(req.Widget, x.auth.botToken, time.Now())
	default:
		err = errors.New("no telegram proof")
	}
	if err != nil {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "telegram_check_failed", "detail": err.Error()})
		return
	}
	ctx := c.Request.Context()
	h := hashSecretToken(req.Token)
	inv, err := x.repo.InviteByHash(ctx, h)
	if err != nil {
		c.JSON(http.StatusGone, gin.H{"error": "invite_invalid", "detail": "Приглашение недействительно: истекло, уже принято или отозвано. Попросите новое"})
		return
	}
	if inv.ResidentTg == u.ID {
		c.JSON(http.StatusConflict, gin.H{"error": "own_invite", "detail": "Это ваше приглашение. Его открывает ассистент в своём Telegram"})
		return
	}
	name := strings.TrimSpace(u.FirstName + " " + u.LastName)
	if name == "" && u.Username != "" {
		name = "@" + u.Username
	}
	a, err := x.repo.AcceptInvite(ctx, h, u.ID, clip(name, 80))
	if errors.Is(err, pg.ErrAssistNotFound) {
		c.JSON(http.StatusGone, gin.H{"error": "invite_invalid", "detail": "Приглашение уже принято"})
		return
	}
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal server error"})
		return
	}
	x.forget()
	out, err := x.auth.issueAssistant(c, a, "tg:"+strconv.FormatInt(u.ID, 10), a.AssistantName, "приглашение", 0)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal server error"})
		return
	}
	c.JSON(http.StatusOK, out)
}

// LinkLogin: POST /api/v1/platform/auth/link {token}: вход личной ссылкой.
func (x *AssistAccess) LinkLogin(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	if !x.rl.allow("link:"+c.ClientIP(), 20, 10*time.Minute) {
		tooMany(c)
		return
	}
	var req struct {
		Token string `json:"token"`
	}
	if err := c.ShouldBindJSON(&req); err != nil || !validSecretToken(req.Token) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "bad request"})
		return
	}
	ctx := c.Request.Context()
	l, err := x.repo.LinkByHash(ctx, hashSecretToken(req.Token))
	if err != nil {
		c.JSON(http.StatusGone, gin.H{"error": "link_invalid", "detail": "Ссылка недействительна: истекла или отозвана. Напишите куратору, он пришлёт новую"})
		return
	}
	if _, active := x.names.get(ctx, l.ResidentTg); !active {
		c.JSON(http.StatusForbidden, gin.H{"error": "not_resident", "detail": "Доступ к платформе закрыт. Если это ошибка, напишите куратору"})
		return
	}
	x.repo.UseLink(ctx, l.ID)
	if l.AssistantID != 0 {
		a, err := x.repo.AssistantByID(ctx, l.AssistantID)
		if err != nil || !a.Active() {
			c.JSON(http.StatusGone, gin.H{"error": "link_invalid", "detail": "Доступ ассистента отозван"})
			return
		}
		out, err := x.auth.issueAssistant(c, a, "link:"+strconv.FormatInt(l.ID, 10), firstNonBlank(a.AssistantName, a.Label), "личная ссылка", l.ID)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "internal server error"})
			return
		}
		c.JSON(http.StatusOK, out)
		return
	}
	sub := "tg:" + strconv.FormatInt(l.ResidentTg, 10)
	tok, err := x.auth.issueSession(c, pg.PlatformSession{Sub: sub, Actor: "link:" + strconv.FormatInt(l.ID, 10),
		ActorName: l.ResidentName, Kind: "link", LinkID: l.ID}, "resident", nil)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal server error"})
		return
	}
	setSessionCookie(c, tok, int(platformSessionTTL.Seconds()))
	c.JSON(http.StatusOK, gin.H{"token": tok, "expiresAt": time.Now().Add(platformSessionTTL).UTC(),
		"user": gin.H{"id": sub, "name": l.ResidentName, "role": "resident"}, "team": gin.H{}})
}

// Pages: GET /assist/:token, GET /in/:token (assist_pages.go).
func (x *AssistAccess) Register(r *gin.Engine, pub, g *gin.RouterGroup) {
	pub.GET("/assist/invite-info", x.InviteInfo)
	pub.POST("/assist/accept", x.Accept)
	pub.POST("/auth/link", x.LinkLogin)
	g.GET("/assist", x.State)
	g.POST("/assist/invite", x.Invite)
	g.POST("/assist/revoke", x.Revoke)
	g.POST("/assist/preset", x.Preset)
	g.POST("/assist/switch", x.Switch)
	g.GET("/sessions", x.Sessions)
	g.POST("/sessions/revoke", x.RevokeSession)
	g.POST("/sessions/revoke-others", x.RevokeOthers)
	g.GET("/access/resident", x.TeamResident)
	g.POST("/access/link", x.TeamLink)
	g.POST("/access/link/revoke", x.TeamRevokeLink)
	g.POST("/access/session/revoke", x.TeamRevokeSession)
	g.POST("/access/assist/revoke", x.Revoke)
	g.POST("/access/assist/preset", x.Preset)
	r.GET("/assist/:token", serveAssistPage)
	r.GET("/in/:token", serveLinkPage)
	r.GET("/switch", serveSwitchPage)
}
