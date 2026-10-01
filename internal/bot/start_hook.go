package bot

import (
	"context"
	"encoding/json"
	"log"
	"strings"
	"time"
)

// The lead funnel on the server: a new person pressing «Старт» gets the
// server's welcome (straight into the app with the 99 checklists) and lands
// in the platform CRM. Admins and anyone the club knows as a resident (also a
// former one) still go to the Apps Script bot as before.

// StartUpdate is a private /start from someone the club does not know yet.
type StartUpdate struct {
	ChatID    int64
	Param     string // t.me/bsurgery_bot?start=<param>
	FirstName string
	LastName  string
	Username  string
}

// StartHook handles the /start; false leaves it to the script.
type StartHook func(ctx context.Context, st StartUpdate) bool

func (s *Service) SetStartHook(h StartHook) {
	s.mu.Lock()
	s.startHook = h
	s.mu.Unlock()
}

// ReadStart: the private /start in an update, if it is one.
func ReadStart(body []byte) (StartUpdate, bool) {
	var u struct {
		Message *struct {
			Text string `json:"text"`
			Chat struct {
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
		return StartUpdate{}, false
	}
	m := u.Message
	if m.Chat.Type != "private" {
		return StartUpdate{}, false
	}
	t := strings.TrimSpace(m.Text)
	if t != "/start" && !strings.HasPrefix(t, "/start ") {
		return StartUpdate{}, false
	}
	p := strings.TrimSpace(strings.TrimPrefix(t, "/start"))
	if r := []rune(p); len(r) > 64 {
		p = string(r[:64])
	}
	return StartUpdate{ChatID: m.Chat.ID, Param: p, FirstName: m.From.FirstName, LastName: m.From.LastName, Username: m.From.Username}, true
}

// takeStart: true when the server answered the /start itself.
func (s *Service) takeStart(ctx context.Context, body []byte) bool {
	s.mu.RLock()
	h := s.startHook
	s.mu.RUnlock()
	if h == nil {
		return false
	}
	st, ok := ReadStart(body)
	if !ok || s.isAdmin(st.ChatID) {
		return false
	}
	// Referral links stay with the script: it pays the bonus and tells the inviter.
	if strings.HasPrefix(st.Param, "ref_") {
		return false
	}
	if _, known, err := s.repo.ResidentByTgID(ctx, st.ChatID); err != nil || known {
		return false
	}
	c, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	if !h(c, st) {
		return false
	}
	log.Printf("bot: /start of %d answered by the server (lead funnel)", st.ChatID)
	return true
}
