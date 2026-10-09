package http

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
)

// R65: онлайн-разбор прямо на доске, в одной вкладке.
//
// Трекер и резидент открывают одну доску и созваниваются браузер в браузер
// (WebRTC). Сервер только сводит их: кто в созвоне, кто сейчас показывает
// экран, идёт ли запись, и передаёт письма соединения (offer, answer, ice)
// от вкладки к вкладке. Звук и картинка идут напрямую, сервер их не видит.
//
// Показывает один: «Показать мой экран» делает показывающим того, кто нажал,
// у прежнего показ останавливается сам (его страница видит, что показывает
// уже другой). Всё в памяти, как присутствие на доске (platform_presence.go):
// перезапуск сервера стоит одного переподключения.
//
// R74: «Мне видео же тоже нужно»: у каждого камера (вкл/выкл, отметка cam у
// участника, чтобы другие видели аватар, когда камера выключена) и созвон до
// четырёх вкладок (трекер, резидент, партнёр, второй основатель): каждая
// соединена с каждой (маленькая сетка), пятая получает room_full.
//
//   POST /call/room    {board, tab, op: peek|join|leave|present|unpresent|rec|unrec|cam|nocam, cam}
//   POST /call/signal  {board, tab, to, kind, data}
//   GET  /call/stream  ?board&tab  состояние созвона и письма этой вкладке (SSE)

const (
	callMemberTTL  = 20 * time.Second
	callMsgMax     = 200      // писем в очереди одной вкладки
	callMsgSize    = 64 << 10 // одно письмо (offer с кандидатами) не больше
	callMaxMembers = 4        // R74: сетка браузер в браузер: каждый шлёт каждому, больше четырёх тяжело
)

type callMsg struct {
	From string          `json:"from"`
	Kind string          `json:"kind"`
	Data json.RawMessage `json:"data,omitempty"`
}

type callMember struct {
	Key   string    `json:"key"` // id|tab
	ID    string    `json:"id"`
	Name  string    `json:"name"`
	Role  string    `json:"role"`
	Color string    `json:"color"`
	Cam   bool      `json:"cam"` // R74: камера включена
	At    time.Time `json:"-"`
	box   []callMsg
	live  int // открытые потоки этой вкладки
}

type callMark struct {
	Key  string    `json:"key"`
	Name string    `json:"name"`
	At   time.Time `json:"at"`
}

type callRoomState struct {
	members   map[string]*callMember
	presenter *callMark
	rec       *callMark
}

type callHub struct {
	mu    sync.Mutex
	rooms map[string]*callRoomState
	subs  map[string]map[chan struct{}]string // board -> stream -> member key
	now   func() time.Time
}

func newCallHub() *callHub {
	return &callHub{rooms: map[string]*callRoomState{}, subs: map[string]map[chan struct{}]string{}, now: time.Now}
}

var theCallHub = newCallHub()

func (h *callHub) wakeLocked(board, only string) {
	for ch, k := range h.subs[board] {
		if only != "" && k != only {
			continue
		}
		select {
		case ch <- struct{}{}:
		default:
		}
	}
}

// expireLocked: вкладки без потока и без запросов дольше 20 с выходят из созвона.
func (h *callHub) expireLocked(board string) bool {
	r := h.rooms[board]
	if r == nil {
		return false
	}
	gone := false
	for k, m := range r.members {
		if m.live == 0 && h.now().Sub(m.At) > callMemberTTL {
			h.dropLocked(r, k)
			gone = true
		}
	}
	if len(r.members) == 0 {
		delete(h.rooms, board)
	}
	return gone
}

func (h *callHub) dropLocked(r *callRoomState, key string) {
	delete(r.members, key)
	if r.presenter != nil && r.presenter.Key == key {
		r.presenter = nil
	}
	if r.rec != nil && r.rec.Key == key {
		r.rec = nil
	}
}

type callView struct {
	Members   []callMember `json:"members"`
	Presenter *callMark    `json:"presenter"`
	Rec       *callMark    `json:"rec"`
	You       string       `json:"you,omitempty"`
	In        bool         `json:"in"`
}

func (h *callHub) viewLocked(board, you string) callView {
	v := callView{Members: []callMember{}, You: you}
	r := h.rooms[board]
	if r == nil {
		return v
	}
	for k, m := range r.members {
		v.Members = append(v.Members, *m)
		if k == you {
			v.In = true
		}
	}
	sort.Slice(v.Members, func(i, j int) bool { return v.Members[i].Key < v.Members[j].Key })
	if r.presenter != nil {
		p := *r.presenter
		v.Presenter = &p
	}
	if r.rec != nil {
		p := *r.rec
		v.Rec = &p
	}
	return v
}

// Op меняет созвон доски и возвращает его состояние для вкладки me.
func (h *callHub) Op(board string, me callMember, op string) (callView, bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.expireLocked(board)
	r := h.rooms[board]
	in := r != nil && r.members[me.Key] != nil
	changed := false
	switch op {
	case "peek":
		if in {
			r.members[me.Key].At = h.now()
		}
	case "join":
		if r == nil {
			r = &callRoomState{members: map[string]*callMember{}}
			h.rooms[board] = r
		}
		if old := r.members[me.Key]; old != nil {
			old.At, old.Name = h.now(), me.Name
			if old.Cam != me.Cam {
				old.Cam, changed = me.Cam, true
			}
		} else if len(r.members) >= callMaxMembers {
			return h.viewLocked(board, me.Key), false
		} else {
			m := me
			m.At = h.now()
			r.members[me.Key] = &m
			changed = true
		}
	case "leave":
		if in {
			h.dropLocked(r, me.Key)
			if len(r.members) == 0 {
				delete(h.rooms, board)
			}
			changed = true
		}
	case "cam", "nocam": // R74
		if !in {
			return h.viewLocked(board, me.Key), false
		}
		m := r.members[me.Key]
		m.At = h.now()
		if on := op == "cam"; m.Cam != on {
			m.Cam, changed = on, true
		}
	case "present", "unpresent", "rec", "unrec":
		if !in {
			return h.viewLocked(board, me.Key), false
		}
		r.members[me.Key].At = h.now()
		mark := &callMark{Key: me.Key, Name: r.members[me.Key].Name, At: h.now()}
		switch op {
		case "present": // показывает тот, кто нажал последним
			r.presenter, changed = mark, true
		case "unpresent":
			if r.presenter != nil && r.presenter.Key == me.Key {
				r.presenter, changed = nil, true
			}
		case "rec":
			if r.rec == nil || r.rec.Key != me.Key {
				r.rec, changed = mark, true
			}
		case "unrec":
			if r.rec != nil && r.rec.Key == me.Key {
				r.rec, changed = nil, true
			}
		}
	}
	if changed {
		h.wakeLocked(board, "")
	}
	return h.viewLocked(board, me.Key), true
}

// Send кладёт письмо в очередь вкладки to; false: её нет в созвоне.
func (h *callHub) Send(board, from, to string, m callMsg) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	r := h.rooms[board]
	if r == nil || r.members[from] == nil || r.members[to] == nil {
		return false
	}
	r.members[from].At = h.now()
	dst := r.members[to]
	m.From = from
	dst.box = append(dst.box, m)
	if len(dst.box) > callMsgMax {
		dst.box = dst.box[len(dst.box)-callMsgMax:]
	}
	h.wakeLocked(board, to)
	return true
}

// take: состояние и письма вкладки (письма уходят из очереди).
func (h *callHub) take(board, key string) (callView, []callMsg) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.expireLocked(board) {
		h.wakeLocked(board, "")
	}
	var msgs []callMsg
	if r := h.rooms[board]; r != nil {
		if m := r.members[key]; m != nil {
			m.At = h.now()
			msgs, m.box = m.box, nil
		}
	}
	return h.viewLocked(board, key), msgs
}

func (h *callHub) subscribe(board, key string) (chan struct{}, func()) {
	ch := make(chan struct{}, 1)
	h.mu.Lock()
	if h.subs[board] == nil {
		h.subs[board] = map[chan struct{}]string{}
	}
	h.subs[board][ch] = key
	if r := h.rooms[board]; r != nil && r.members[key] != nil {
		r.members[key].live++
	}
	h.mu.Unlock()
	return ch, func() {
		h.mu.Lock()
		delete(h.subs[board], ch)
		if len(h.subs[board]) == 0 {
			delete(h.subs, board)
		}
		if r := h.rooms[board]; r != nil && r.members[key] != nil {
			if r.members[key].live > 0 {
				r.members[key].live--
			}
			r.members[key].At = h.now()
		}
		h.mu.Unlock()
	}
}

// callIce: STUN Google и, если задан, свой TURN (BS_TURN_URLS через запятую,
// BS_TURN_USER, BS_TURN_PASS) для сетей, где напрямую не соединиться.
func callIce() []gin.H {
	out := []gin.H{{"urls": []string{"stun:stun.l.google.com:19302", "stun:stun1.l.google.com:19302"}}}
	if u := strings.TrimSpace(os.Getenv("BS_TURN_URLS")); u != "" {
		var urls []string
		for _, x := range strings.Split(u, ",") {
			if x = strings.TrimSpace(x); x != "" {
				urls = append(urls, x)
			}
		}
		if len(urls) > 0 {
			out = append(out, gin.H{"urls": urls, "username": os.Getenv("BS_TURN_USER"), "credential": os.Getenv("BS_TURN_PASS")})
		}
	}
	return out
}

// callWho: кто зовёт (имя от сервера важнее имени со страницы).
func (pp *PlatformPresence) callWho(c *gin.Context, board, tab, name string) (callMember, bool) {
	uid := platformUser(c)
	if uid == "" || !presenceTabRe.MatchString(tab) || !platformIDRe.MatchString(board) {
		return callMember{}, false
	}
	rname, ok := pp.allowed(c, board)
	if !ok {
		return callMember{}, false
	}
	m := callMember{ID: uid, Key: uid + "|" + tab, Role: platformRole(c), Color: presenceColor(uid), Name: rname}
	if m.Name == "" && pp.teamName != nil {
		m.Name = pp.teamName(platformTgID(c))
	}
	if m.Name == "" {
		m.Name = cleanPresenceName(name)
	}
	if m.Name == "" {
		m.Name = "Участник"
	}
	return m, true
}

// CallRoom godoc
// @Summary      Онлайн-разбор на доске: войти, выйти, показать экран, отметить запись
// @Tags         platform
// @Security     BearerAuth
// @Router       /api/v1/platform/call/room [post]
func (pp *PlatformPresence) CallRoom(c *gin.Context) {
	var req struct {
		Board string `json:"board"`
		Tab   string `json:"tab"`
		Op    string `json:"op"`
		Name  string `json:"name"`
		Cam   bool   `json:"cam"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "bad request"})
		return
	}
	switch req.Op {
	case "peek", "join", "leave", "present", "unpresent", "rec", "unrec", "cam", "nocam":
	default:
		c.JSON(http.StatusBadRequest, gin.H{"error": "bad op"})
		return
	}
	me, ok := pp.callWho(c, req.Board, req.Tab, req.Name)
	if !ok {
		forbidden(c, "not_your_board")
		return
	}
	me.Cam = req.Cam
	v, ok := theCallHub.Op(req.Board, me, req.Op)
	if !ok {
		why := "not_in_call"
		if req.Op == "join" {
			why = "room_full" // R74: уже четверо
		}
		c.JSON(http.StatusConflict, gin.H{"error": why, "room": v, "max": callMaxMembers})
		return
	}
	out := gin.H{"room": v}
	if req.Op == "join" {
		out["ice"] = callIce()
	}
	c.JSON(http.StatusOK, out)
}

// CallSignal godoc
// @Summary      Письмо соединения другой вкладке созвона (offer, answer, ice)
// @Tags         platform
// @Security     BearerAuth
// @Router       /api/v1/platform/call/signal [post]
func (pp *PlatformPresence) CallSignal(c *gin.Context) {
	var req struct {
		Board string          `json:"board"`
		Tab   string          `json:"tab"`
		To    string          `json:"to"`
		Kind  string          `json:"kind"`
		Data  json.RawMessage `json:"data"`
	}
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, callMsgSize+1024)
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
	me, ok := pp.callWho(c, req.Board, req.Tab, "")
	if !ok {
		forbidden(c, "not_your_board")
		return
	}
	if !theCallHub.Send(req.Board, me.Key, req.To, callMsg{Kind: req.Kind, Data: req.Data}) {
		c.JSON(http.StatusNotFound, gin.H{"error": "not_in_call"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"ok": true})
}

// CallStream godoc
// @Summary      Созвон доски и письма этой вкладке (server-sent events)
// @Tags         platform
// @Security     BearerAuth
// @Produce      text/event-stream
// @Router       /api/v1/platform/call/stream [get]
func (pp *PlatformPresence) CallStream(c *gin.Context) {
	board, tab := c.Query("board"), c.Query("tab")
	me, ok := pp.callWho(c, board, tab, "")
	if !ok {
		forbidden(c, "not_your_board")
		return
	}
	w := c.Writer
	hd := w.Header()
	hd.Set("Content-Type", "text/event-stream; charset=utf-8")
	hd.Set("Cache-Control", "no-cache, no-transform")
	hd.Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	wake, stop := theCallHub.subscribe(board, me.Key)
	defer stop()
	last := ""
	send := func(force bool) bool {
		v, msgs := theCallHub.take(board, me.Key)
		if msgs == nil {
			msgs = []callMsg{}
		}
		rb, _ := json.Marshal(v)
		if !force && len(msgs) == 0 && string(rb) == last {
			return true
		}
		last = string(rb)
		b, _ := json.Marshal(gin.H{"room": json.RawMessage(rb), "msgs": msgs})
		if _, err := fmt.Fprintf(w, "data: %s\n\n", b); err != nil {
			return false
		}
		w.Flush()
		return true
	}
	if !send(true) {
		return
	}
	ctx := c.Request.Context()
	end := time.NewTimer(presenceStreamFor)
	defer end.Stop()
	tick := time.NewTicker(5 * time.Second)
	defer tick.Stop()
	n := 0
	for {
		select {
		case <-ctx.Done():
			return
		case <-end.C:
			_, _ = fmt.Fprint(w, "event: bye\ndata: {}\n\n")
			w.Flush()
			return
		case <-tick.C:
			n++
			if !send(false) {
				return
			}
			if n%3 == 0 {
				if _, err := fmt.Fprint(w, ": ping "+strconv.FormatInt(time.Now().Unix(), 10)+"\n\n"); err != nil {
					return
				}
				w.Flush()
			}
		case <-wake:
			if !send(false) {
				return
			}
		}
	}
}
