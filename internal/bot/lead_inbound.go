package bot

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"time"

	pg "github.com/bnursik/business_surgery_backend/internal/repository/pg"
)

// R40b: автоответ лидам.
//
// Каждое личное сообщение человека, которого клуб не знает (не команда, не
// резидент), записывается во «входящие» сразу при получении, ещё до ответа
// (InboundHook). Воронка сверяет входящие со своими ответами и через
// 2 минуты без ответа шлёт стандартное приветствие один раз (страховка).
// Свободный текст лида больше не теряется: его забирает LeadTextHook
// (раньше бот после перехода на сервер молча его пропускал).

// Inbound: a private message of a lead (a /start or free text).
type Inbound struct {
	ChatID    int64
	Date      int64 // Telegram's message date, unix seconds
	FirstName string
	LastName  string
	Username  string
	Text      string // the text or the caption; "" for a sticker, a voice, a photo without caption
	Media     string // photo, voice, video, document, sticker… ("" for text)
	Start     bool
	Param     string
}

// InboundHook records the message as soon as it arrives (no answer).
type InboundHook func(ctx context.Context, in Inbound)

// LeadTextHook answers a lead's free text; false: nobody answered.
type LeadTextHook func(ctx context.Context, in Inbound) bool

type leadHookSet struct {
	mu      sync.RWMutex
	inbound InboundHook
	text    LeadTextHook
}

var leadHooks sync.Map // *Service → *leadHookSet

func (s *Service) leadSet() *leadHookSet {
	v, _ := leadHooks.LoadOrStore(s, &leadHookSet{})
	return v.(*leadHookSet)
}

func (s *Service) SetInboundHook(h InboundHook) {
	l := s.leadSet()
	l.mu.Lock()
	l.inbound = h
	l.mu.Unlock()
}

func (s *Service) SetLeadTextHook(h LeadTextHook) {
	l := s.leadSet()
	l.mu.Lock()
	l.text = h
	l.mu.Unlock()
}

// ReadInbound: the private message of a person (not a bot) in an update.
func ReadInbound(body []byte) (Inbound, bool) {
	var u struct {
		Message *struct {
			Date    int64           `json:"date"`
			Text    string          `json:"text"`
			Caption string          `json:"caption"`
			Photo   json.RawMessage `json:"photo"`
			Voice   json.RawMessage `json:"voice"`
			Video   json.RawMessage `json:"video"`
			VNote   json.RawMessage `json:"video_note"`
			Doc     json.RawMessage `json:"document"`
			Sticker json.RawMessage `json:"sticker"`
			Contact json.RawMessage `json:"contact"`
			Chat    struct {
				ID   int64  `json:"id"`
				Type string `json:"type"`
			} `json:"chat"`
			From *struct {
				ID        int64  `json:"id"`
				IsBot     bool   `json:"is_bot"`
				FirstName string `json:"first_name"`
				LastName  string `json:"last_name"`
				Username  string `json:"username"`
			} `json:"from"`
		} `json:"message"`
	}
	if json.Unmarshal(body, &u) != nil || u.Message == nil || u.Message.From == nil || u.Message.From.IsBot {
		return Inbound{}, false
	}
	m := u.Message
	if m.Chat.Type != "private" || len(m.Contact) > 0 {
		return Inbound{}, false
	}
	in := Inbound{ChatID: m.Chat.ID, Date: m.Date, FirstName: m.From.FirstName, LastName: m.From.LastName, Username: m.From.Username}
	t := strings.TrimSpace(m.Text)
	switch {
	case t != "":
		in.Text = t
	case len(m.Photo) > 0:
		in.Media, in.Text = "фото", strings.TrimSpace(m.Caption)
	case len(m.Voice) > 0:
		in.Media = "голосовое"
	case len(m.Video) > 0 || len(m.VNote) > 0:
		in.Media, in.Text = "видео", strings.TrimSpace(m.Caption)
	case len(m.Doc) > 0:
		in.Media, in.Text = "файл", strings.TrimSpace(m.Caption)
	case len(m.Sticker) > 0:
		in.Media = "стикер"
	default:
		return Inbound{}, false
	}
	if st, ok := ReadStart(body); ok {
		in.Start, in.Param = true, st.Param
	} else if strings.HasPrefix(in.Text, "/") {
		return Inbound{}, false // the bot's other commands are not a lead's question
	}
	if r := []rune(in.Text); len(r) > 1000 {
		in.Text = string(r[:1000])
	}
	if in.Date == 0 {
		in.Date = time.Now().Unix()
	}
	return in, true
}

// inboundOf: a lead's message (not the team, not a resident); ok false otherwise.
func (s *Service) inboundOf(ctx context.Context, body []byte) (Inbound, bool) {
	in, ok := ReadInbound(body)
	if !ok || s.isAdmin(in.ChatID) {
		return Inbound{}, false
	}
	if _, known, err := s.repo.ResidentByTgID(ctx, in.ChatID); err != nil || known {
		return Inbound{}, false
	}
	return in, true
}

// noteInbound: Receive's background step: the message goes to the inbox.
func (s *Service) noteInbound(ctx context.Context, body []byte) {
	l := s.leadSet()
	l.mu.RLock()
	h := l.inbound
	l.mu.RUnlock()
	if h == nil {
		return
	}
	if in, ok := s.inboundOf(ctx, body); ok {
		h(ctx, in)
	}
}

// leadText: private(): a lead's free text goes to the funnel.
func (s *Service) leadText(ctx context.Context, body []byte) bool {
	l := s.leadSet()
	l.mu.RLock()
	h := l.text
	l.mu.RUnlock()
	if h == nil {
		return false
	}
	in, ok := ReadInbound(body)
	if !ok || in.Start {
		return false
	}
	return h(ctx, in)
}

// leadRetry: an update taken again after a restart (Tries > 1) is still
// answered when it is a lead's message from the last 10 minutes: the funnel
// knows whether it already answered (the message date against its reply).
func (s *Service) leadRetry(ctx context.Context, u *pg.BotUpdate) bool {
	if u.Kind != "message" || time.Since(u.Received) > 10*time.Minute {
		return false
	}
	_, ok := s.inboundOf(ctx, u.Body)
	return ok
}

type retryKey struct{}

// WithRetry marks ctx: the update is handled again after a restart.
func WithRetry(ctx context.Context) context.Context { return context.WithValue(ctx, retryKey{}, true) }

// IsRetry: the update was taken before (the hooks check whether they already answered).
func IsRetry(ctx context.Context) bool { v, _ := ctx.Value(retryKey{}).(bool); return v }
