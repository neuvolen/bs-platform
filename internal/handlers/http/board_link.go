package http

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/golang-jwt/jwt/v5"

	"github.com/bnursik/business_surgery_backend/internal/middleware"
	"github.com/bnursik/business_surgery_backend/internal/repository/pg"
)

// R57: «Ссылка для клиента». После разбора команда отправляет клиенту (ещё не
// резиденту) ссылку на его доску: https://app.bxclub.kz/b/<id>. Без входа,
// только чтение, только эта доска. Внизу страницы предложение клуба из
// «Продажи клуба» (цены, Kaspi, бонус, кейсы с согласием), кнопки «Хочу в
// клуб», «Есть вопрос», «Пока подумаю». Команда видит открытия, время на
// странице, разделы и нажатия; бот пишет команде один раз в день на ссылку
// «Клиент открыл доску» и один раз «Хочет в клуб».
//
// id: 128 случайных бит (hex), хранится в board_links с доской, автором,
// сроком (30 дней) и флагом отключения. Пока ссылка жива, доска получает её
// же. Страница отдаёт только проекцию доски (диагнозы, причины, стратегия,
// цели, задачи, инструменты): заметки трекера, штрафы, история, тесты,
// здоровье и всё прочее в неё не попадает.

const (
	boardLinkTTL  = 30 * 24 * time.Hour
	boardLinkPath = "/b/"
)

var boardLinkIDRe = regexp.MustCompile(`^[0-9a-f]{32}$`)

// BoardLinkStore: pg.BoardLinksRepo.
type BoardLinkStore interface {
	Get(ctx context.Context, id string) (*pg.BoardLink, error)
	Live(ctx context.Context, boardID string, now time.Time) (*pg.BoardLink, error)
	Latest(ctx context.Context, boardID string) (*pg.BoardLink, error)
	Create(ctx context.Context, id, boardID, by string, now, expires time.Time) (*pg.BoardLink, error)
	Revoke(ctx context.Context, boardID string, now time.Time) (int64, error)
	Open(ctx context.Context, id string, now time.Time, day string) (int, bool, error)
	Track(ctx context.Context, id string, seconds int, sections, clicks []string) error
	Intent(ctx context.Context, id, intent string, now time.Time) (bool, error)
}

// BoardGetter: pg.PlatformRepo.
type BoardGetter interface {
	GetBoard(ctx context.Context, id string) (*pg.PlatformBoard, error)
}

type BoardLinks struct {
	Store  BoardLinkStore
	Boards BoardGetter
	S      *ClubSales // settings, cases, the team's bot, the CRM
	Secret []byte
	Now    func() time.Time
	// Notify: a message to the team (nil: S.team).
	Notify func(ctx context.Context, text string)

	rl *ipLimiter
}

func NewBoardLinks(store BoardLinkStore, boards BoardGetter, s *ClubSales, secret []byte) *BoardLinks {
	return &BoardLinks{Store: store, Boards: boards, S: s, Secret: secret, rl: newIPLimiter(90, time.Minute)}
}

func (h *BoardLinks) now() time.Time {
	if h.Now != nil {
		return h.Now()
	}
	return time.Now()
}

func (h *BoardLinks) notify(ctx context.Context, text string) {
	if h.Notify != nil {
		h.Notify(ctx, text)
		return
	}
	if h.S != nil {
		h.S.team(ctx, text, nil)
	}
}

func boardLinkURL(id string) string {
	base := publicBase()
	if base == "" {
		base = "https://app.bxclub.kz"
	}
	return base + boardLinkPath + id
}

func newBoardLinkID() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

func (h *BoardLinks) Register(r *gin.Engine) {
	pub := r.Group(boardLinkPath)
	pub.Use(h.publicGuard)
	pub.GET(":token", h.Page)
	pub.GET(":token/data", h.Data)
	pub.POST(":token/ev", h.Event)
	pub.POST(":token/intent", h.IntentHTTP)

	g := r.Group("/api/v1/platform/clientlink")
	g.Use(middleware.AuthJWT(h.Secret))
	g.Use(middleware.RequireRole("admin", "moderator"))
	g.Use(func(c *gin.Context) {
		if !teamOnly(c) {
			c.Abort()
			return
		}
		c.Next()
	})
	g.GET("/:board", h.AdminGet)
	g.POST("/:board", h.AdminCreate)
	g.DELETE("/:board", h.AdminRevoke)
}

// ── команда ──

type boardLinkView struct {
	URL       string         `json:"url"`
	Live      bool           `json:"live"`
	State     string         `json:"state"` // live | revoked | expired
	CreatedAt time.Time      `json:"createdAt"`
	ExpiresAt time.Time      `json:"expiresAt"`
	Opens     int            `json:"opens"`
	FirstOpen *time.Time     `json:"firstOpen,omitempty"`
	LastOpen  *time.Time     `json:"lastOpen,omitempty"`
	Seconds   int            `json:"seconds"`
	Sections  map[string]int `json:"sections"`
	Clicks    map[string]int `json:"clicks"`
	Intent    string         `json:"intent"`
	IntentAt  *time.Time     `json:"intentAt,omitempty"`
	WA        string         `json:"wa"`
	TG        string         `json:"tg"`
}

func (h *BoardLinks) view(l *pg.BoardLink, clientName string) *boardLinkView {
	if l == nil {
		return nil
	}
	u := boardLinkURL(l.ID)
	st := "live"
	switch {
	case l.Revoked:
		st = "revoked"
	case !l.ExpiresAt.After(h.now()):
		st = "expired"
	}
	msg := "Ваш разбор в Business Surgery: доска с диагнозами и планом на 10 дней"
	if fn := firstName(clientName); fn != "" {
		msg = fn + ", ваш разбор в Business Surgery: доска с диагнозами и планом на 10 дней"
	}
	return &boardLinkView{URL: u, Live: st == "live", State: st, CreatedAt: l.CreatedAt, ExpiresAt: l.ExpiresAt, Opens: l.Opens,
		FirstOpen: l.FirstOpen, LastOpen: l.LastOpen, Seconds: l.Seconds, Sections: l.Sections, Clicks: l.Clicks,
		Intent: l.Intent, IntentAt: l.IntentAt,
		WA: "https://wa.me/?text=" + url.QueryEscape(msg+"\n"+u),
		TG: "https://t.me/share/url?url=" + url.QueryEscape(u) + "&text=" + url.QueryEscape(msg)}
}

func (h *BoardLinks) board(c *gin.Context) (*pg.PlatformBoard, bool) {
	id := c.Param("board")
	if !platformIDRe.MatchString(id) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "bad_board"})
		return nil, false
	}
	b, err := h.Boards.GetBoard(c.Request.Context(), id)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "board_failed"})
		return nil, false
	}
	if b == nil || b.Deleted {
		c.JSON(http.StatusNotFound, gin.H{"error": "no_board", "message": "Доска ещё не сохранена на сервере: сохраните её и попробуйте снова"})
		return nil, false
	}
	return b, true
}

// AdminGet: GET /platform/clientlink/:board → the newest link with its stats (or null).
func (h *BoardLinks) AdminGet(c *gin.Context) {
	b, ok := h.board(c)
	if !ok {
		return
	}
	l, err := h.Store.Latest(c.Request.Context(), b.ID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "link_failed"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"link": h.view(l, clientBoardOf(b).Name)})
}

// AdminCreate: POST /platform/clientlink/:board → the live link (made if there is none).
func (h *BoardLinks) AdminCreate(c *gin.Context) {
	b, ok := h.board(c)
	if !ok {
		return
	}
	ctx := c.Request.Context()
	now := h.now()
	l, err := h.Store.Live(ctx, b.ID, now)
	if err == nil && l == nil {
		var id string
		if id, err = newBoardLinkID(); err == nil {
			l, err = h.Store.Create(ctx, id, b.ID, platformUser(c), now, now.Add(boardLinkTTL))
		}
		if err == nil && l != nil {
			h.linkLead(ctx, b, boardLinkURL(l.ID), now)
		}
	}
	if err != nil || l == nil {
		log.Printf("clientlink: create %s: %v", b.ID, err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "link_failed", "message": "Не получилось создать ссылку"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"link": h.view(l, clientBoardOf(b).Name)})
}

// AdminRevoke: DELETE /platform/clientlink/:board → every live link of the board stops working.
func (h *BoardLinks) AdminRevoke(c *gin.Context) {
	b, ok := h.board(c)
	if !ok {
		return
	}
	ctx := c.Request.Context()
	if _, err := h.Store.Revoke(ctx, b.ID, h.now()); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "revoke_failed"})
		return
	}
	h.linkLead(ctx, b, "", h.now())
	l, _ := h.Store.Latest(ctx, b.ID)
	c.JSON(http.StatusOK, gin.H{"link": h.view(l, clientBoardOf(b).Name)})
}

// ── CRM: the lead of the board's client ──

// leadOfBoard: the CRM lead (not won yet) named as the board's client.
func (h *BoardLinks) mutateLeadOf(ctx context.Context, b *pg.PlatformBoard, fn func(l map[string]any) bool) {
	if h.S == nil || h.S.docs == nil {
		return
	}
	names := map[string]bool{}
	for _, n := range []string{b.Resident, clientBoardOf(b).Name} {
		if k := normName(n); k != "" {
			names[k] = true
		}
	}
	if len(names) == 0 {
		return
	}
	if err := h.S.mutateLead(ctx, func(l map[string]any) bool { return names[normName(sStr(l, "name"))] }, fn); err != nil {
		log.Printf("clientlink: lead of %s: %v", b.ID, err)
	}
}

// linkLead keeps the link on the lead card (the day 2/5/10 messages add it); "" removes it.
func (h *BoardLinks) linkLead(ctx context.Context, b *pg.PlatformBoard, u string, now time.Time) {
	h.mutateLeadOf(ctx, b, func(l map[string]any) bool {
		if sStr(l, "boardLink") == u {
			return false
		}
		if u == "" {
			delete(l, "boardLink")
			addLog(l, now, "Ссылка на доску для клиента отключена")
		} else {
			l["boardLink"] = u
			addLog(l, now, "Создана ссылка на доску для клиента")
		}
		return true
	})
}

// ── открытая страница ──

// publicGuard: rate limit, no indexing, no shared caches, no referrer.
func (h *BoardLinks) publicGuard(c *gin.Context) {
	hd := c.Writer.Header()
	hd.Set("X-Robots-Tag", "noindex, nofollow, noarchive")
	hd.Set("Cache-Control", "private, no-store")
	hd.Set("Referrer-Policy", "no-referrer")
	hd.Set("X-Content-Type-Options", "nosniff")
	hd.Set("X-Frame-Options", "DENY")
	if h.rl != nil && !h.rl.allow(clientIP(c)) {
		c.AbortWithStatusJSON(http.StatusTooManyRequests, gin.H{"error": "too_many"})
		return
	}
	c.Next()
}

func clientIP(c *gin.Context) string {
	if ip := c.ClientIP(); ip != "" {
		return ip
	}
	host, _, _ := net.SplitHostPort(c.Request.RemoteAddr)
	return host
}

// live: the link and its board, or "" with the reason (gone | revoked | expired).
func (h *BoardLinks) live(ctx context.Context, token string) (*pg.BoardLink, *pg.PlatformBoard, string) {
	if !boardLinkIDRe.MatchString(token) {
		return nil, nil, "gone"
	}
	l, err := h.Store.Get(ctx, token)
	if err != nil || l == nil {
		return nil, nil, "gone"
	}
	if l.Revoked {
		return nil, nil, "revoked"
	}
	if !l.ExpiresAt.After(h.now()) {
		return nil, nil, "expired"
	}
	b, err := h.Boards.GetBoard(ctx, l.BoardID)
	if err != nil || b == nil || b.Deleted {
		return nil, nil, "gone"
	}
	return l, b, ""
}

// teamViewer: the visitor is signed in to the platform as the team (their opens are not counted).
func (h *BoardLinks) teamViewer(c *gin.Context) bool {
	raw, err := c.Cookie(PlatformSessionCookie)
	if err != nil || raw == "" {
		return false
	}
	t, err := jwt.Parse(raw, func(t *jwt.Token) (any, error) {
		if _, ok := t.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, jwt.ErrSignatureInvalid
		}
		return h.Secret, nil
	})
	if err != nil || !t.Valid {
		return false
	}
	cl, _ := t.Claims.(jwt.MapClaims)
	role, _ := cl["role"].(string)
	return role == "admin" || role == "moderator"
}

// Data: GET /b/:token/data → this board's client view as JSON (nothing else).
func (h *BoardLinks) Data(c *gin.Context) {
	_, b, why := h.live(c.Request.Context(), c.Param("token"))
	if b == nil {
		c.JSON(http.StatusNotFound, gin.H{"error": why})
		return
	}
	c.JSON(http.StatusOK, clientBoardOf(b))
}

var boardSections = map[string]bool{"top": true, "diag": true, "cause": true, "strat": true, "plan": true, "tools": true, "club": true, "cases": true, "price": true, "pay": true}
var boardClicks = map[string]bool{"join": true, "ask": true, "think": true, "kaspi": true, "wa": true, "tg": true, "sticky_join": true, "sticky_ask": true}

// Event: POST /b/:token/ev {open, s, sec[], click[]} (the page's beacons).
func (h *BoardLinks) Event(c *gin.Context) {
	var in struct {
		Open  bool     `json:"open"`
		S     int      `json:"s"`
		Sec   []string `json:"sec"`
		Click []string `json:"click"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(c.Writer, c.Request.Body, 4096)).Decode(&in); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "bad_json"})
		return
	}
	ctx := c.Request.Context()
	l, b, why := h.live(ctx, c.Param("token"))
	if l == nil {
		c.JSON(http.StatusNotFound, gin.H{"error": why})
		return
	}
	if h.teamViewer(c) {
		c.JSON(http.StatusOK, gin.H{"ok": true, "team": true})
		return
	}
	now := h.now()
	if in.Open {
		day := now.In(almaty).Format("2006-01-02")
		n, note, err := h.Store.Open(ctx, l.ID, now, day)
		if err != nil {
			log.Printf("clientlink: open %s: %v", l.ID, err)
		}
		if note {
			name := clientBoardOf(b).Name
			if name == "" {
				name = b.Name
			}
			txt := "👀 Клиент открыл доску: " + name
			if n > 1 {
				txt += fmt.Sprintf(" (уже %d раз)", n)
			}
			h.notify(ctx, txt+"\n"+boardLinkURL(l.ID))
			if n == 1 {
				h.mutateLeadOf(ctx, b, func(ld map[string]any) bool {
					addLog(ld, now, "Клиент открыл ссылку на доску")
					return true
				})
			}
		}
	}
	s := in.S
	if s < 0 {
		s = 0
	}
	if s > 120 { // one beacon covers at most two minutes
		s = 120
	}
	var sec, clk []string
	for _, x := range in.Sec {
		if boardSections[x] && len(sec) < 12 {
			sec = append(sec, x)
		}
	}
	for _, x := range in.Click {
		if boardClicks[x] && len(clk) < 12 {
			clk = append(clk, x)
		}
	}
	if s > 0 || len(sec) > 0 || len(clk) > 0 {
		if err := h.Store.Track(ctx, l.ID, s, sec, clk); err != nil {
			log.Printf("clientlink: track %s: %v", l.ID, err)
		}
	}
	c.JSON(http.StatusOK, gin.H{"ok": true})
}

// IntentHTTP: POST /b/:token/intent {intent: join|ask|think}.
func (h *BoardLinks) IntentHTTP(c *gin.Context) {
	var in struct {
		Intent string `json:"intent"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(c.Writer, c.Request.Body, 1024)).Decode(&in); err != nil || (in.Intent != "join" && in.Intent != "ask" && in.Intent != "think") {
		c.JSON(http.StatusBadRequest, gin.H{"error": "bad_intent"})
		return
	}
	ctx := c.Request.Context()
	l, b, why := h.live(ctx, c.Param("token"))
	if l == nil {
		c.JSON(http.StatusNotFound, gin.H{"error": why})
		return
	}
	if h.teamViewer(c) {
		c.JSON(http.StatusOK, gin.H{"ok": true, "team": true})
		return
	}
	now := h.now()
	first, err := h.Store.Intent(ctx, l.ID, in.Intent, now)
	if err != nil {
		log.Printf("clientlink: intent %s: %v", l.ID, err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed"})
		return
	}
	_ = h.Store.Track(ctx, l.ID, 0, nil, []string{in.Intent})
	name := clientBoardOf(b).Name
	if name == "" {
		name = b.Name
	}
	switch in.Intent {
	case "join":
		if first {
			h.notify(ctx, "🔥 "+name+" нажал «Хочу в клуб» на своей доске.\nСвяжитесь сегодня: договор и оплата.\n"+boardLinkURL(l.ID))
			h.mutateLeadOf(ctx, b, func(ld map[string]any) bool {
				if pz := sMap(ld, "pz"); pz != nil && (pz["st"] == "active" || pz["st"] == "wait") {
					pzStop(ld, pz, now, "хочет в клуб (доска)")
				}
				if sStr(ld, "col") != "won" {
					ld["col"] = "decide"
				}
				ld["next"], ld["nextAt"] = "Связаться: хочет в клуб", now.In(almaty).Format("2006-01-02")
				ld["hot"] = true
				addLog(ld, now, "Нажал «Хочу в клуб» на доске: этап «Решение»")
				return true
			})
		}
	case "think":
		h.mutateLeadOf(ctx, b, func(ld map[string]any) bool {
			addLog(ld, now, "На доске: «Пока подумаю»")
			return true
		})
	case "ask":
		h.mutateLeadOf(ctx, b, func(ld map[string]any) bool {
			addLog(ld, now, "На доске: «Есть вопрос»")
			return true
		})
	}
	c.JSON(http.StatusOK, gin.H{"ok": true})
}

// ── rate limit ──

type ipLimiter struct {
	mu     sync.Mutex
	max    int
	window time.Duration
	hits   map[string]*ipHits
	sweep  time.Time
}

type ipHits struct {
	n     int
	start time.Time
}

func newIPLimiter(max int, window time.Duration) *ipLimiter {
	return &ipLimiter{max: max, window: window, hits: map[string]*ipHits{}}
}

func (l *ipLimiter) allow(ip string) bool {
	now := time.Now()
	l.mu.Lock()
	defer l.mu.Unlock()
	if now.Sub(l.sweep) > l.window {
		for k, v := range l.hits {
			if now.Sub(v.start) > l.window {
				delete(l.hits, k)
			}
		}
		l.sweep = now
	}
	x := l.hits[ip]
	if x == nil || now.Sub(x.start) > l.window {
		x = &ipHits{start: now}
		l.hits[ip] = x
	}
	x.n++
	return x.n <= l.max
}
