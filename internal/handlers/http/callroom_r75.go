package http

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/golang-jwt/jwt/v5"

	"github.com/bnursik/business_surgery_backend/internal/calllink"
	"github.com/bnursik/business_surgery_backend/internal/repository/pg"
	"github.com/bnursik/business_surgery_backend/web"
)

// R75 call: созвон по ссылке платформы вместо Google Meet.
//
// Ссылка встречи: https://app.bxclub.kz/call/<id> (calllink): одна на
// человека. Сервер находит по id человека и его доску (последнюю), созвон
// идёт на этой доске (тот же хаб, что R65/R74, platform_callroom.go).
//
//   - Команда и резидент со своей доской: страница платформы, доска и созвон
//     (/#callroom=<id>, платформа спрашивает GET /call/link?rid=).
//   - Все остальные (лид, партнёр, гость без аккаунта): маленькая страница
//     созвона (web/call.html), вход с именем, всегда через лобби: впускает
//     команда. Гость видит только этот созвон, не доску.
//
// Гостю сервер выдаёт подпись (rid, гость, имя, срок), с ней он ходит в
// /api/v1/platform/callg/{room,signal,stream}.

const (
	callGuestTTL   = 12 * time.Hour
	callResolveTTL = 30 * time.Second
)

var callGuestIDRe = regexp.MustCompile(`^[a-z0-9]{8,32}$`)

// CallName: a person the call links are made for, with their partner (the
// partner without a board of their own is on the partner's board).
type CallName struct {
	Name    string
	Partner string
}

// CallLinks finds a call link's person and board.
type CallLinks struct {
	Boards func(ctx context.Context) ([]pg.CallBoard, error)
	Names  func(ctx context.Context) []CallName
	Secret []byte

	mu    sync.Mutex
	at    time.Time
	tried time.Time
	byRID map[string]callTarget
	resOf map[string]string // board -> resident
	rl    *ipLimiter
}

type callTarget struct {
	Name  string
	Board string
}

var theCallLinks *CallLinks

func (l *CallLinks) refreshLocked(ctx context.Context, force bool) {
	now := time.Now()
	if l.byRID != nil && now.Sub(l.at) < callResolveTTL && !force {
		return
	}
	if force && now.Sub(l.tried) < 3*time.Second && l.byRID != nil {
		return
	}
	l.tried = now
	byRID, resOf := map[string]callTarget{}, map[string]string{}
	latest := map[string]pg.CallBoard{} // normalized name -> latest board
	if l.Boards != nil {
		bs, err := l.Boards(ctx)
		if err != nil && l.byRID != nil {
			return
		}
		for _, b := range bs {
			res := strings.TrimSpace(b.Resident)
			resOf[b.ID] = res
			n := calllink.Norm(res)
			if n == "" {
				continue
			}
			if old, ok := latest[n]; !ok || b.UpdatedAt.After(old.UpdatedAt) {
				latest[n] = b
			}
		}
	}
	for n, b := range latest {
		byRID[calllink.ID(n)] = callTarget{Name: strings.TrimSpace(b.Resident), Board: b.ID}
	}
	if l.Names != nil {
		for _, cn := range l.Names(ctx) {
			id := calllink.ID(cn.Name)
			if id == "" {
				continue
			}
			t := callTarget{Name: strings.TrimSpace(cn.Name)}
			if b, ok := latest[calllink.Norm(cn.Partner)]; ok && cn.Partner != "" {
				t.Board = b.ID // партнёр без своей доски: на доске пары
			}
			if old, ok := byRID[id]; ok && (old.Board != "" || t.Board == "") {
				continue
			}
			byRID[id] = t
		}
	}
	l.byRID, l.resOf, l.at = byRID, resOf, now
}

// Resolve: the link's person and board ("" board: no board yet).
func (l *CallLinks) Resolve(ctx context.Context, rid string) (callTarget, bool) {
	if l == nil || !calllink.IDRe.MatchString(rid) {
		return callTarget{}, false
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	l.refreshLocked(ctx, false)
	t, ok := l.byRID[rid]
	if !ok || t.Board == "" {
		l.refreshLocked(ctx, true) // доску только что сделали
		t, ok = l.byRID[rid]
	}
	return t, ok
}

// callLinkOfBoard: the call link of the board's resident ("" if unknown).
func callLinkOfBoard(ctx context.Context, board string) string {
	l := theCallLinks
	if l == nil {
		return ""
	}
	l.mu.Lock()
	l.refreshLocked(ctx, false)
	res, ok := l.resOf[board]
	if !ok {
		l.refreshLocked(ctx, true)
		res = l.resOf[board]
	}
	l.mu.Unlock()
	return calllink.URL(res)
}

// ── гость ──

type callGuestClaims struct {
	R string `json:"r"` // id ссылки
	G string `json:"g"` // id гостя
	N string `json:"n"` // имя
	E int64  `json:"e"` // до (unix)
}

func (l *CallLinks) sign(cl callGuestClaims) string {
	b, _ := json.Marshal(cl)
	p := base64.RawURLEncoding.EncodeToString(b)
	m := hmac.New(sha256.New, append([]byte("bs-call-guest|"), l.Secret...))
	m.Write([]byte(p))
	return p + "." + base64.RawURLEncoding.EncodeToString(m.Sum(nil))
}

func (l *CallLinks) parse(tok string) (callGuestClaims, bool) {
	var cl callGuestClaims
	p, sig, ok := strings.Cut(tok, ".")
	if !ok || len(tok) > 1024 {
		return cl, false
	}
	m := hmac.New(sha256.New, append([]byte("bs-call-guest|"), l.Secret...))
	m.Write([]byte(p))
	want := base64.RawURLEncoding.EncodeToString(m.Sum(nil))
	if !hmac.Equal([]byte(want), []byte(sig)) {
		return cl, false
	}
	b, err := base64.RawURLEncoding.DecodeString(p)
	if err != nil || json.Unmarshal(b, &cl) != nil || time.Now().Unix() > cl.E {
		return cl, false
	}
	return cl, true
}

func callGuestName(s string) string {
	s = cleanPresenceName(s)
	if r := []rune(s); len(r) > 40 {
		s = string(r[:40])
	}
	return strings.TrimSpace(s)
}

// Enter: a guest says their name and gets the pass to this link's call.
func (l *CallLinks) Enter(c *gin.Context) {
	if !l.rl.allow(clientIP(c)) {
		c.JSON(http.StatusTooManyRequests, gin.H{"error": "slow_down"})
		return
	}
	var req struct {
		RID  string `json:"rid"`
		Name string `json:"name"`
		GID  string `json:"gid"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "bad request"})
		return
	}
	name := callGuestName(req.Name)
	if name == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "name"})
		return
	}
	t, ok := l.Resolve(c.Request.Context(), req.RID)
	if !ok {
		c.JSON(http.StatusNotFound, gin.H{"error": "no_call"})
		return
	}
	gid := strings.ToLower(req.GID)
	if !callGuestIDRe.MatchString(gid) {
		b := make([]byte, 8)
		_, _ = rand.Read(b)
		gid = hex.EncodeToString(b)
	}
	tok := l.sign(callGuestClaims{R: req.RID, G: gid, N: name, E: time.Now().Add(callGuestTTL).Unix()})
	c.JSON(http.StatusOK, gin.H{"tok": tok, "gid": gid, "name": name, "ready": t.Board != "", "ice": callIce()})
}

// guest: who calls (from the pass) and the link's board.
func (l *CallLinks) guest(c *gin.Context, tok, tab string) (callMember, string, bool) {
	if tok == "" {
		tok = c.GetHeader("X-Call-Guest")
	}
	cl, ok := l.parse(tok)
	if !ok || !presenceTabRe.MatchString(tab) {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "bad_pass"})
		return callMember{}, "", false
	}
	t, ok := l.Resolve(c.Request.Context(), cl.R)
	if !ok {
		c.JSON(http.StatusNotFound, gin.H{"error": "no_call"})
		return callMember{}, "", false
	}
	id := "g:" + cl.G
	m := callMember{ID: id, Key: id + "|" + tab, Name: cl.N, Role: "guest", Color: presenceColor(id), Guest: true}
	return m, t.Board, true
}

// Room: the guest's ops (no admit, no deny, no recording).
func (l *CallLinks) Room(c *gin.Context) {
	var req struct {
		Tok string `json:"tok"`
		Tab string `json:"tab"`
		Op  string `json:"op"`
		Cam bool   `json:"cam"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "bad request"})
		return
	}
	switch req.Op {
	case "peek", "join", "leave", "present", "unpresent", "cam", "nocam":
	default:
		c.JSON(http.StatusBadRequest, gin.H{"error": "bad op"})
		return
	}
	me, board, ok := l.guest(c, req.Tok, req.Tab)
	if !ok {
		return
	}
	if board == "" { // доски ещё нет: команда откроет ссылку и сделает её, гость ждёт
		c.JSON(http.StatusOK, gin.H{"room": gin.H{"members": []any{}, "wait": req.Op == "join" || req.Op == "peek", "in": false, "auto": false}, "noboard": true})
		return
	}
	me.Cam = req.Cam
	callRoomReply(c, board, me, req.Op, "")
}

// Signal: the guest's connection letters.
func (l *CallLinks) Signal(c *gin.Context) {
	var req struct {
		Tok  string          `json:"tok"`
		Tab  string          `json:"tab"`
		To   string          `json:"to"`
		Kind string          `json:"kind"`
		Data json.RawMessage `json:"data"`
	}
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, callMsgSize+2048)
	if err := c.ShouldBindJSON(&req); err != nil || len(req.Data) > callMsgSize {
		c.JSON(http.StatusBadRequest, gin.H{"error": "bad request"})
		return
	}
	switch req.Kind {
	case "offer", "answer", "ice", "hello", "bye":
	default:
		c.JSON(http.StatusBadRequest, gin.H{"error": "bad kind"})
		return
	}
	me, board, ok := l.guest(c, req.Tok, req.Tab)
	if !ok {
		return
	}
	if board == "" || !theCallHub.Send(board, me.Key, req.To, callMsg{Kind: req.Kind, Data: req.Data}) {
		c.JSON(http.StatusNotFound, gin.H{"error": "not_in_call"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"ok": true})
}

// Stream: the guest's call state and letters (server-sent events).
func (l *CallLinks) Stream(c *gin.Context) {
	me, board, ok := l.guest(c, c.Query("tok"), c.Query("tab"))
	if !ok {
		return
	}
	if board == "" {
		c.JSON(http.StatusConflict, gin.H{"error": "no_board"})
		return
	}
	callStreamServe(c, board, me.Key)
}

// ── платформа ──

// Link: GET /call/link?rid= (the platform opens a link: whose board) or
// ?board= (the link of a board's call, to send).
func (l *CallLinks) Link(c *gin.Context) {
	if b := c.Query("board"); b != "" {
		if !platformIDRe.MatchString(b) {
			c.JSON(http.StatusBadRequest, gin.H{"error": "bad board"})
			return
		}
		if isResident(c) {
			if theCallPresence == nil || !callAllowed(c, b) {
				forbidden(c, "not_your_board")
				return
			}
		}
		c.JSON(http.StatusOK, gin.H{"url": callLinkOfBoard(c.Request.Context(), b)})
		return
	}
	rid := c.Query("rid")
	t, ok := l.Resolve(c.Request.Context(), rid)
	if !ok {
		c.JSON(http.StatusNotFound, gin.H{"error": "no_call"})
		return
	}
	if isResident(c) {
		// резидент со своей доской: на доске; чужая ссылка (партнёр): как гость
		if t.Board == "" {
			c.JSON(http.StatusForbidden, gin.H{"error": "guest", "name": ""})
			return
		}
		if theCallPresence == nil || !callAllowed(c, t.Board) {
			c.JSON(http.StatusForbidden, gin.H{"error": "guest"})
			return
		}
	}
	c.JSON(http.StatusOK, gin.H{"board": t.Board, "name": t.Name, "url": calllink.Base() + calllink.Path + rid})
}

// Page: GET /call/:rid. The team and a resident with a session go to the
// platform (their board); everyone else gets the guest page.
func (l *CallLinks) Page(c *gin.Context) {
	rid := c.Param("rid")
	if !calllink.IDRe.MatchString(rid) {
		c.String(http.StatusNotFound, "Ссылка на созвон не найдена")
		return
	}
	if c.Query("guest") != "1" {
		if role := l.sessionRole(c); role != "" && role != "lead" {
			c.Redirect(http.StatusFound, "/#callroom="+rid)
			return
		}
	}
	h := c.Writer.Header()
	h.Set("X-Robots-Tag", "noindex, nofollow")
	h.Set("Referrer-Policy", "no-referrer")
	h.Set("Cache-Control", "no-cache")
	h.Set("Permissions-Policy", "camera=(self), microphone=(self), display-capture=(self)")
	c.Data(http.StatusOK, "text/html; charset=utf-8", web.CallPage())
}

func (l *CallLinks) sessionRole(c *gin.Context) string {
	raw, err := c.Cookie(PlatformSessionCookie)
	if err != nil || raw == "" {
		return ""
	}
	tkn, err := jwt.Parse(raw, func(t *jwt.Token) (any, error) {
		if _, ok := t.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, jwt.ErrSignatureInvalid
		}
		return l.Secret, nil
	})
	if err != nil || !tkn.Valid {
		return ""
	}
	cl, ok := tkn.Claims.(jwt.MapClaims)
	if !ok {
		return ""
	}
	if typ, _ := cl["typ"].(string); typ != "access" {
		return ""
	}
	role, _ := cl["role"].(string)
	return role
}

// theCallPresence: the module's presence (the resident's own board check).
var theCallPresence *PlatformPresence

// RegisterCalls: the call link's routes (public ones and the platform's).
func (l *CallLinks) Register(r *gin.Engine, g *gin.RouterGroup) {
	r.GET(calllink.Path+":rid", l.Page)
	r.HEAD(calllink.Path+":rid", l.Page)
	pub := r.Group("/api/v1/platform/callg")
	pub.POST("/enter", l.Enter)
	pub.POST("/room", l.Room)
	pub.POST("/signal", l.Signal)
	pub.GET("/stream", l.Stream)
	g.GET("/call/link", l.Link)
}

// NewCallLinks: the resolver over the platform's boards and residents.
func NewCallLinks(repo *pg.PlatformRepo, secret []byte) *CallLinks {
	l := &CallLinks{Secret: secret, rl: newIPLimiter(40, time.Minute)}
	if repo != nil {
		l.Boards = repo.CallBoards
		l.Names = func(ctx context.Context) []CallName {
			ns, _ := repo.CallResidentNames(ctx)
			out := make([]CallName, 0, len(ns))
			for _, n := range ns {
				out = append(out, CallName{Name: n})
			}
			return out
		}
	}
	return l
}

func callAllowed(c *gin.Context, board string) bool {
	_, ok := theCallPresence.allowed(c, board)
	return ok
}
