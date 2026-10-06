package http

import (
	"context"
	"encoding/json"
	"fmt"
	"hash/fnv"
	"net/http"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/gin-gonic/gin"
)

// R52: who is on a board right now, like several people in one Google Sheet.
//
// Every logged-in person who has a board open sends a heartbeat (every 5 s)
// and their cursor (at most ~10 times a second) with POST /presence; the
// answer carries everyone else on that board, so a browser that cannot keep
// a stream open simply polls with it. Browsers that can, also hold
// GET /presence/stream (server-sent events, read with fetch so the token
// stays in the Authorization header): the hub pushes the board's people the
// moment someone moves, selects or starts typing. Nothing is stored: the hub
// lives in memory, a person who stops sending disappears after 30 s, and a
// restart only costs one heartbeat. The colour comes from the person's id,
// the same on every screen. A resident joins only the boards made for them,
// so they never see who is on someone else's board.

const (
	presenceTTL       = 30 * time.Second
	presenceStreamFor = 50 * time.Second // under the server's WriteTimeout; the browser reconnects
	presencePing      = 15 * time.Second
	presenceCoalesce  = 40 * time.Millisecond
)

// presenceColors: distinct on black and on white, no red (red means errors and diagnoses).
var presenceColors = []string{"#5B9BFF", "#3DD68C", "#F5B544", "#B48CFF", "#4FD1E0", "#FF9F5A", "#E58BD8", "#9BD25B", "#7C8CFF", "#2FB8A6"}

func presenceColor(id string) string {
	h := fnv.New32a()
	_, _ = h.Write([]byte(id))
	return presenceColors[int(h.Sum32()%uint32(len(presenceColors)))]
}

// presencePeer is one open board in one browser tab.
type presencePeer struct {
	ID    string    `json:"id"`  // the person (JWT subject, "tg:…")
	Tab   string    `json:"tab"` // the browser tab: one person may have two
	Name  string    `json:"name"`
	Photo string    `json:"photo,omitempty"`
	Color string    `json:"color"`
	Role  string    `json:"role"`
	X     *float64  `json:"x,omitempty"` // cursor on the board, in board coordinates
	Y     *float64  `json:"y,omitempty"`
	Sel   string    `json:"sel,omitempty"`  // the card or sticker they selected
	Edit  string    `json:"edit,omitempty"` // the card they are typing in
	At    time.Time `json:"at"`
}

func (p *presencePeer) key() string { return p.ID + "|" + p.Tab }

type presenceHub struct {
	mu    sync.Mutex
	rooms map[string]map[string]*presencePeer // board -> peer key -> peer
	subs  map[string]map[chan struct{}]struct{}
	now   func() time.Time
}

func newPresenceHub() *presenceHub {
	return &presenceHub{rooms: map[string]map[string]*presencePeer{}, subs: map[string]map[chan struct{}]struct{}{}, now: time.Now}
}

// notify wakes the board's streams; a stream that is still busy keeps one pending wake.
func (h *presenceHub) notifyLocked(board string) {
	for ch := range h.subs[board] {
		select {
		case ch <- struct{}{}:
		default:
		}
	}
}

// Update puts the peer on the board (and off any other board it was on in that tab).
func (h *presenceHub) Update(board string, p presencePeer) {
	h.mu.Lock()
	defer h.mu.Unlock()
	p.At = h.now()
	k := p.key()
	for b, room := range h.rooms {
		if b == board {
			continue
		}
		if _, ok := room[k]; ok {
			delete(room, k)
			if len(room) == 0 {
				delete(h.rooms, b)
			}
			h.notifyLocked(b)
		}
	}
	room := h.rooms[board]
	if room == nil {
		room = map[string]*presencePeer{}
		h.rooms[board] = room
	}
	old := room[k]
	cp := p
	room[k] = &cp
	if old == nil || (old.X == nil) != (cp.X == nil) || (old.X != nil && cp.X != nil && (*old.X != *cp.X || *old.Y != *cp.Y)) ||
		old.Sel != cp.Sel || old.Edit != cp.Edit || old.Name != cp.Name || old.Photo != cp.Photo {
		h.notifyLocked(board)
	}
}

// Leave takes the tab off the board right away (the page closed or left the board).
func (h *presenceHub) Leave(board, key string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if room := h.rooms[board]; room != nil {
		if _, ok := room[key]; ok {
			delete(room, key)
			if len(room) == 0 {
				delete(h.rooms, board)
			}
			h.notifyLocked(board)
		}
	}
}

// Peers: everyone on the board seen in the last 30 s, except the asking tab, oldest first.
func (h *presenceHub) Peers(board, except string) []presencePeer {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.expireLocked(board)
	out := []presencePeer{}
	for k, p := range h.rooms[board] {
		if k == except {
			continue
		}
		out = append(out, *p)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Name != out[j].Name {
			return out[i].Name < out[j].Name
		}
		return out[i].key() < out[j].key()
	})
	return out
}

func (h *presenceHub) expireLocked(board string) bool {
	room := h.rooms[board]
	gone := false
	for k, p := range room {
		if h.now().Sub(p.At) > presenceTTL {
			delete(room, k)
			gone = true
		}
	}
	if room != nil && len(room) == 0 {
		delete(h.rooms, board)
	}
	return gone
}

// Sweep drops the silent peers of every board and wakes the boards that changed.
func (h *presenceHub) Sweep() {
	h.mu.Lock()
	defer h.mu.Unlock()
	for b := range h.rooms {
		if h.expireLocked(b) {
			h.notifyLocked(b)
		}
	}
}

// Subscribe: a channel that receives a wake whenever the board's people change.
func (h *presenceHub) Subscribe(board string) (<-chan struct{}, func()) {
	ch := make(chan struct{}, 1)
	h.mu.Lock()
	if h.subs[board] == nil {
		h.subs[board] = map[chan struct{}]struct{}{}
	}
	h.subs[board][ch] = struct{}{}
	h.mu.Unlock()
	return ch, func() {
		h.mu.Lock()
		delete(h.subs[board], ch)
		if len(h.subs[board]) == 0 {
			delete(h.subs, board)
		}
		h.mu.Unlock()
	}
}

// PlatformPresence serves /presence and /presence/stream.
type PlatformPresence struct {
	hub *presenceHub
	// teamName: the team member's name from the login list ("" if unknown)
	teamName func(tg int64) string
	// residentName: the calling resident's name ("" when they lost access)
	residentName func(c *gin.Context) string
	// boardResident: whose board it is (info.res); error when there is no such board
	boardResident func(ctx context.Context, id string) (string, error)

	cacheMu sync.Mutex
	cache   map[string]presenceAllow
}

type presenceAllow struct {
	ok bool
	at time.Time
}

func NewPlatformPresence(teamName func(int64) string, residentName func(*gin.Context) string, boardResident func(context.Context, string) (string, error)) *PlatformPresence {
	return &PlatformPresence{hub: newPresenceHub(), teamName: teamName, residentName: residentName, boardResident: boardResident, cache: map[string]presenceAllow{}}
}

// Start sweeps silent peers every 5 s, so the others see them go.
func (pp *PlatformPresence) Start(ctx context.Context) {
	go func() {
		t := time.NewTicker(5 * time.Second)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				pp.hub.Sweep()
			}
		}
	}()
}

var presenceTabRe = regexp.MustCompile(`^[A-Za-z0-9_\-]{1,40}$`)

// allowed: the team sees every board; a resident only the boards made for them.
func (pp *PlatformPresence) allowed(c *gin.Context, board string) (string, bool) {
	if !isResident(c) {
		return "", true
	}
	name := ""
	if pp.residentName != nil {
		name = pp.residentName(c)
	}
	if name == "" {
		return "", false
	}
	ck := platformUser(c) + "|" + board
	pp.cacheMu.Lock()
	if a, ok := pp.cache[ck]; ok && time.Since(a.at) < time.Minute {
		pp.cacheMu.Unlock()
		return name, a.ok
	}
	pp.cacheMu.Unlock()
	ok := false
	if pp.boardResident != nil {
		if res, err := pp.boardResident(c.Request.Context(), board); err == nil {
			ok = normName(res) != "" && normName(res) == normName(name)
		}
	}
	pp.cacheMu.Lock()
	if len(pp.cache) > 5000 {
		pp.cache = map[string]presenceAllow{}
	}
	pp.cache[ck] = presenceAllow{ok: ok, at: time.Now()}
	pp.cacheMu.Unlock()
	return name, ok
}

type presenceReq struct {
	Board string   `json:"board"`
	Tab   string   `json:"tab"`
	X     *float64 `json:"x"`
	Y     *float64 `json:"y"`
	Sel   string   `json:"sel"`
	Edit  string   `json:"edit"`
	Name  string   `json:"name"`
	Photo string   `json:"photo"`
	Leave bool     `json:"leave"`
}

func cleanPresenceName(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	if utf8.RuneCountInString(s) > 40 {
		s = string([]rune(s)[:40])
	}
	return s
}

// Only Telegram's own avatar addresses are shown (the login widget gives t.me/i/userpic/…).
func cleanPresencePhoto(s string) string {
	s = strings.TrimSpace(s)
	if len(s) > 400 || strings.ContainsAny(s, "\"'<> \\") {
		return ""
	}
	for _, p := range []string{"https://t.me/", "https://telegram.org/", "https://cdn4.telegram-cdn.org/", "https://cdn5.telegram-cdn.org/", "/api/v1/"} {
		if strings.HasPrefix(s, p) {
			return s
		}
	}
	return ""
}

func cleanCoord(v *float64) *float64 {
	if v == nil || *v != *v || *v > 1e6 || *v < -1e6 {
		return nil
	}
	r := float64(int64(*v*10)) / 10
	return &r
}

// Post godoc
// @Summary      Heartbeat on a board: cursor, selection, typing
// @Tags         platform
// @Security     BearerAuth
// @Router       /api/v1/platform/presence [post]
func (pp *PlatformPresence) Post(c *gin.Context) {
	var req presenceReq
	if err := c.ShouldBindJSON(&req); err != nil || !platformIDRe.MatchString(req.Board) || !presenceTabRe.MatchString(req.Tab) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "bad request"})
		return
	}
	uid := platformUser(c)
	if uid == "" {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "no user"})
		return
	}
	me := presencePeer{ID: uid, Tab: req.Tab, Color: presenceColor(uid), Role: platformRole(c)}
	if req.Leave {
		pp.hub.Leave(req.Board, me.key())
		c.JSON(http.StatusOK, gin.H{"ok": true})
		return
	}
	rname, ok := pp.allowed(c, req.Board)
	if !ok {
		forbidden(c, "not_your_board")
		return
	}
	// The name the server knows wins over what the page says
	me.Name = rname
	if me.Name == "" && pp.teamName != nil {
		me.Name = pp.teamName(platformTgID(c))
	}
	if me.Name == "" {
		me.Name = cleanPresenceName(req.Name)
	}
	if me.Name == "" {
		me.Name = "Участник"
	}
	me.Photo = cleanPresencePhoto(req.Photo)
	me.X, me.Y = cleanCoord(req.X), cleanCoord(req.Y)
	if me.X == nil || me.Y == nil {
		me.X, me.Y = nil, nil
	}
	if platformIDRe.MatchString(req.Sel) {
		me.Sel = req.Sel
	}
	if platformIDRe.MatchString(req.Edit) {
		me.Edit = req.Edit
	}
	pp.hub.Update(req.Board, me)
	c.JSON(http.StatusOK, gin.H{"you": gin.H{"id": uid, "color": me.Color, "name": me.Name}, "peers": pp.hub.Peers(req.Board, me.key()), "ttl": int(presenceTTL / time.Second)})
}

// Stream godoc
// @Summary      The board's people as server-sent events
// @Tags         platform
// @Security     BearerAuth
// @Produce      text/event-stream
// @Router       /api/v1/platform/presence/stream [get]
func (pp *PlatformPresence) Stream(c *gin.Context) {
	board, tab := c.Query("board"), c.Query("tab")
	if !platformIDRe.MatchString(board) || !presenceTabRe.MatchString(tab) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "bad request"})
		return
	}
	uid := platformUser(c)
	if _, ok := pp.allowed(c, board); !ok || uid == "" {
		forbidden(c, "not_your_board")
		return
	}
	self := uid + "|" + tab
	w := c.Writer
	h := w.Header()
	h.Set("Content-Type", "text/event-stream; charset=utf-8")
	h.Set("Cache-Control", "no-cache, no-transform")
	h.Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	wake, stop := pp.hub.Subscribe(board)
	defer stop()
	send := func() bool {
		b, _ := json.Marshal(gin.H{"peers": pp.hub.Peers(board, self), "at": time.Now().UnixMilli()})
		if _, err := fmt.Fprintf(w, "data: %s\n\n", b); err != nil {
			return false
		}
		w.Flush()
		return true
	}
	if !send() {
		return
	}
	ctx := c.Request.Context()
	end := time.NewTimer(presenceStreamFor)
	defer end.Stop()
	ping := time.NewTicker(presencePing)
	defer ping.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-end.C:
			_, _ = fmt.Fprint(w, "event: bye\ndata: {}\n\n")
			w.Flush()
			return
		case <-ping.C:
			if _, err := fmt.Fprint(w, ": ping "+strconv.FormatInt(time.Now().Unix(), 10)+"\n\n"); err != nil {
				return
			}
			w.Flush()
		case <-wake:
			// Moves of several people at once go out as one message
			time.Sleep(presenceCoalesce)
			select {
			case <-wake:
			default:
			}
			if !send() {
				return
			}
		}
	}
}
