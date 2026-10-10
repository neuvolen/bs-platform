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
// R75 call: лобби. Команда (admin, трекер) входит сразу; резидент ждёт в лобби,
// пока кто-то из команды не впустит («Впустить» / «Отклонить»), или сразу, если
// у созвона включено «Впускать всех автоматически». Гости без аккаунта (лид,
// партнёр по ссылке /call/<id>, callroom_r75.go) всегда ждут в лобби. Впущенный
// раз входит снова без лобби, пока созвон жив. Команда может остановить показ
// экрана гостя (письмо stopshare его вкладке).
//
//   POST /call/room    {board, tab, op: peek|join|leave|present|unpresent|rec|unrec|cam|nocam
//                       |admit|deny|auto|noauto|stopshare, cam, who}
//   POST /call/signal  {board, tab, to, kind, data}
//   GET  /call/stream  ?board&tab  состояние созвона и письма этой вкладке (SSE)

const (
	callMemberTTL  = 20 * time.Second
	callMsgMax     = 200      // писем в очереди одной вкладки
	callMsgSize    = 64 << 10 // одно письмо (offer с кандидатами) не больше
	callMaxMembers = 4        // R74: сетка браузер в браузер: каждый шлёт каждому, больше четырёх тяжело
	callMaxLobby   = 12       // R75 call: ждут в лобби одного созвона не больше
	callDeniedTTL  = 10 * time.Minute
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
	Cam   bool      `json:"cam"`             // R74: камера включена
	Team  bool      `json:"team"`            // R75 call: команда (входит сразу, впускает других)
	Guest bool      `json:"guest,omitempty"` // R75 call: гость по ссылке, без аккаунта
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
	// R75 call: лобби
	lobby    map[string]*callMember
	admitted map[string]bool      // id впущенных (вкладка перезагрузилась: входит снова сразу)
	denied   map[string]time.Time // ключи вкладок, которым отказали
	auto     bool                 // «Впускать всех автоматически» (гостей нет: их впускают руками)
}

func newCallRoomState() *callRoomState {
	return &callRoomState{members: map[string]*callMember{}, lobby: map[string]*callMember{}, admitted: map[string]bool{}, denied: map[string]time.Time{}}
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
	for k, m := range r.lobby {
		if m.live == 0 && h.now().Sub(m.At) > callMemberTTL {
			delete(r.lobby, k)
			gone = true
		}
	}
	for k, t := range r.denied {
		if h.now().Sub(t) > callDeniedTTL {
			delete(r.denied, k)
		}
	}
	if len(r.members) == 0 && len(r.lobby) == 0 {
		delete(h.rooms, board)
	}
	return gone
}

func (h *callHub) dropLocked(r *callRoomState, key string) {
	delete(r.members, key)
	delete(r.lobby, key)
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
	// R75 call: лобби. Команда видит, кто ждёт; ждущий видит wait, получивший отказ denied
	Lobby  []callMember `json:"lobby,omitempty"`
	Wait   bool         `json:"wait,omitempty"`
	Denied bool         `json:"denied,omitempty"`
	Auto   bool         `json:"auto"`
	Waits  int          `json:"waits,omitempty"` // сколько ждут (видно всем, имена только команде)
}

func (h *callHub) viewLocked(board, you string, team bool) callView {
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
	v.Auto, v.Waits = r.auto, len(r.lobby)
	if _, ok := r.lobby[you]; ok {
		v.Wait = true
	}
	if _, ok := r.denied[you]; ok && !v.In && !v.Wait {
		v.Denied = true
	}
	if team && len(r.lobby) > 0 {
		for _, m := range r.lobby {
			x := *m
			x.box = nil
			v.Lobby = append(v.Lobby, x)
		}
		sort.Slice(v.Lobby, func(i, j int) bool { return v.Lobby[i].At.Before(v.Lobby[j].At) })
	}
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
	v, err := h.OpWho(board, me, op, "")
	return v, err == ""
}

// admitLocked: из лобби в созвон (false: мест нет).
func (h *callHub) admitLocked(r *callRoomState, key string) bool {
	m := r.lobby[key]
	if m == nil {
		return true
	}
	if len(r.members) >= callMaxMembers {
		return false
	}
	delete(r.lobby, key)
	m.At = h.now()
	r.members[key] = m
	r.admitted[m.ID] = true
	return true
}

// OpWho: Op с адресатом (впустить, отклонить, остановить показ: who, R75 call).
// Ошибка: "" (готово), not_in_call, room_full, lobby_full, team_only, no_one.
func (h *callHub) OpWho(board string, me callMember, op, who string) (callView, string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.expireLocked(board)
	r := h.rooms[board]
	in := r != nil && r.members[me.Key] != nil
	waiting := r != nil && r.lobby[me.Key] != nil
	changed := false
	view := func() callView { return h.viewLocked(board, me.Key, me.Team) }
	switch op {
	case "peek":
		if in {
			r.members[me.Key].At = h.now()
		} else if waiting {
			r.lobby[me.Key].At = h.now()
		}
	case "join":
		if r == nil {
			r = newCallRoomState()
			h.rooms[board] = r
		}
		delete(r.denied, me.Key)
		if old := r.members[me.Key]; old != nil {
			old.At, old.Name = h.now(), me.Name
			if old.Cam != me.Cam {
				old.Cam, changed = me.Cam, true
			}
			break
		}
		direct := me.Team || r.admitted[me.ID] || (r.auto && !me.Guest)
		if !direct {
			if old := r.lobby[me.Key]; old != nil {
				old.At, old.Name, old.Cam = h.now(), me.Name, me.Cam
				break
			}
			if len(r.lobby) >= callMaxLobby {
				return view(), "lobby_full"
			}
			m := me
			m.At = h.now()
			r.lobby[me.Key] = &m
			changed = true
			break
		}
		if len(r.members) >= callMaxMembers {
			return view(), "room_full"
		}
		delete(r.lobby, me.Key)
		m := me
		m.At = h.now()
		r.members[me.Key] = &m
		r.admitted[me.ID] = true
		changed = true
	case "leave":
		if in || waiting {
			h.dropLocked(r, me.Key)
			if len(r.members) == 0 && len(r.lobby) == 0 {
				delete(h.rooms, board)
			}
			changed = true
		}
	case "cam", "nocam": // R74
		if !in {
			return view(), "not_in_call"
		}
		m := r.members[me.Key]
		m.At = h.now()
		if on := op == "cam"; m.Cam != on {
			m.Cam, changed = on, true
		}
	case "present", "unpresent", "rec", "unrec":
		if !in {
			return view(), "not_in_call"
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
	case "admit", "deny", "auto", "noauto", "stopshare": // R75 call: только команда
		if !me.Team {
			return view(), "team_only"
		}
		if r == nil {
			if op == "auto" || op == "noauto" {
				r = newCallRoomState()
				h.rooms[board] = r
			} else {
				return view(), "no_one"
			}
		}
		if in {
			r.members[me.Key].At = h.now()
		}
		switch op {
		case "admit":
			if who == "*" { // впустить всех, сколько поместится
				keys := make([]string, 0, len(r.lobby))
				for k := range r.lobby {
					keys = append(keys, k)
				}
				sort.Slice(keys, func(i, j int) bool { return r.lobby[keys[i]].At.Before(r.lobby[keys[j]].At) })
				for _, k := range keys {
					if !h.admitLocked(r, k) {
						h.wakeLocked(board, "")
						return view(), "room_full"
					}
					changed = true
				}
				break
			}
			if r.lobby[who] == nil {
				return view(), "no_one"
			}
			if !h.admitLocked(r, who) {
				return view(), "room_full"
			}
			changed = true
		case "deny":
			if r.lobby[who] == nil {
				return view(), "no_one"
			}
			delete(r.lobby, who)
			r.denied[who] = h.now()
			changed = true
		case "auto", "noauto":
			if on := op == "auto"; r.auto != on {
				r.auto, changed = on, true
			}
			if r.auto { // кто уже ждёт с аккаунтом, входит
				for k, m := range r.lobby {
					if !m.Guest && !h.admitLocked(r, k) {
						break
					}
				}
			}
			if len(r.members) == 0 && len(r.lobby) == 0 && !r.auto {
				delete(h.rooms, board)
			}
		case "stopshare":
			if r.presenter == nil || r.presenter.Key != who {
				return view(), "no_one"
			}
			r.presenter = nil
			if dst := r.members[who]; dst != nil {
				dst.box = append(dst.box, callMsg{From: me.Key, Kind: "stopshare"})
			}
			changed = true
		}
	}
	if changed {
		h.wakeLocked(board, "")
	}
	return view(), ""
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
	team := false
	if r := h.rooms[board]; r != nil {
		if m := r.members[key]; m != nil {
			m.At = h.now()
			msgs, m.box = m.box, nil
			team = m.Team
		} else if m := r.lobby[key]; m != nil {
			m.At = h.now()
		}
	}
	return h.viewLocked(board, key, team), msgs
}

func (h *callHub) subscribe(board, key string) (chan struct{}, func()) {
	ch := make(chan struct{}, 1)
	h.mu.Lock()
	if h.subs[board] == nil {
		h.subs[board] = map[chan struct{}]string{}
	}
	h.subs[board][ch] = key
	if r := h.rooms[board]; r != nil {
		if m := r.members[key]; m != nil {
			m.live++
		} else if m := r.lobby[key]; m != nil {
			m.live++
		}
	}
	h.mu.Unlock()
	return ch, func() {
		h.mu.Lock()
		delete(h.subs[board], ch)
		if len(h.subs[board]) == 0 {
			delete(h.subs, board)
		}
		if r := h.rooms[board]; r != nil {
			for _, m := range []*callMember{r.members[key], r.lobby[key]} {
				if m == nil {
					continue
				}
				if m.live > 0 {
					m.live--
				}
				m.At = h.now()
			}
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
	m := callMember{ID: uid, Key: uid + "|" + tab, Role: platformRole(c), Color: presenceColor(uid), Name: rname, Team: !isResident(c) && !isLead(c)}
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
		Who   string `json:"who"` // R75 call: кого впустить, отклонить, чей показ остановить
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "bad request"})
		return
	}
	if !callOpOK(req.Op) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "bad op"})
		return
	}
	me, ok := pp.callWho(c, req.Board, req.Tab, req.Name)
	if !ok {
		forbidden(c, "not_your_board")
		return
	}
	me.Cam = req.Cam
	callRoomReply(c, req.Board, me, req.Op, req.Who)
}

// callOpOK: ops of /call/room (R75 call: и лобби).
func callOpOK(op string) bool {
	switch op {
	case "peek", "join", "leave", "present", "unpresent", "rec", "unrec", "cam", "nocam", "admit", "deny", "auto", "noauto", "stopshare":
		return true
	}
	return false
}

// callRoomReply: op и ответ (команде и гостю одинаково).
func callRoomReply(c *gin.Context, board string, me callMember, op, who string) {
	v, why := theCallHub.OpWho(board, me, op, who)
	if why != "" {
		st := http.StatusConflict
		if why == "team_only" {
			st = http.StatusForbidden
		}
		c.JSON(st, gin.H{"error": why, "room": v, "max": callMaxMembers})
		return
	}
	out := gin.H{"room": v}
	if op == "join" {
		out["ice"] = callIce()
		if u := callLinkOfBoard(c.Request.Context(), board); u != "" {
			out["link"] = u // R75 call: ссылка на созвон для приглашения
		}
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
	callStreamServe(c, board, me.Key)
}

// callStreamServe: состояние созвона и письма вкладки key, пока она слушает
// (команде, резиденту и гостю одинаково, R75 call).
func callStreamServe(c *gin.Context, board, key string) {
	me := callMember{Key: key}
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
