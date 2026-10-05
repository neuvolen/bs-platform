package bot

import (
	"context"
	"encoding/json"
	"log"
	"strings"
	"time"

	"github.com/bnursik/business_surgery_backend/internal/club"
)

// R38c: buttons anyone may press (an event's «Иду»), the phone shared for
// it, and personal messages to residents who chose WhatsApp over Telegram.

// SetPublicCallbackHook registers h for callback_data starting with prefix,
// pressed by anyone in a private chat (residents, leads, the team). It runs
// before every other hook. toast is shown to the person; ok false leaves
// the button to the rest (the script in the legacy mode).
func (s *Service) SetPublicCallbackHook(prefix string, h TeamCallbackHook) {
	s.mu.Lock()
	if s.pubCb == nil {
		s.pubCb = map[string]TeamCallbackHook{}
	}
	if h == nil {
		delete(s.pubCb, prefix)
	} else {
		s.pubCb[prefix] = h
	}
	s.mu.Unlock()
}

func (s *Service) publicHook(data string) TeamCallbackHook {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for p, h := range s.pubCb {
		if strings.HasPrefix(data, p) {
			return h
		}
	}
	return nil
}

// ContactUpdate: a phone a person shared with Telegram's button.
type ContactUpdate struct {
	ChatID    int64
	FromID    int64
	Phone     string // +77071234567
	FirstName string
	LastName  string
	Username  string
}

// ContactHook takes a shared phone (true: answered, nothing else to do).
type ContactHook func(ctx context.Context, cu ContactUpdate) bool

func (s *Service) SetContactHook(h ContactHook) {
	s.mu.Lock()
	s.contactHook = h
	s.mu.Unlock()
}

// takeContact: the hook's turn for a shared phone (both modes).
func (s *Service) takeContact(ctx context.Context, body []byte) bool {
	s.mu.RLock()
	h := s.contactHook
	s.mu.RUnlock()
	if h == nil {
		return false
	}
	phone := readContact(body)
	if phone == "" {
		return false
	}
	m, ok := readPrivMsg(body)
	if !ok || m.ChatType != "private" {
		return false
	}
	if !strings.HasPrefix(phone, "+") {
		phone = "+" + phone
	}
	c, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	return h(c, ContactUpdate{ChatID: m.ChatID, FromID: m.FromID, Phone: phone, FirstName: m.FirstName, LastName: m.LastName, Username: m.Username})
}

// WAMessage: a personal message to a resident who reads WhatsApp.
type WAMessage struct {
	Resident string
	Phone    string // digits
	Kind     string // meeting | report | fine | notice | summary | broadcast | event
	Key      string // what makes it unique: the same key is never sent twice
	Text     string
	Expires  *time.Time // a reminder that is useless later (the report reminder of the day)
}

// WhatsAppRoute: the WhatsApp phone of a resident who chose it ("" for Telegram).
type WhatsAppRoute func(ctx context.Context, name string) string

// WhatsAppSink delivers (Green-API) or queues (one click on the platform).
type WhatsAppSink func(ctx context.Context, m WAMessage) error

// SetWhatsApp: residents with the WhatsApp channel get their personal
// messages through sink instead of Telegram.
func (s *Service) SetWhatsApp(route WhatsAppRoute, sink WhatsAppSink) {
	s.mu.Lock()
	s.waRoute, s.waSink = route, sink
	s.mu.Unlock()
}

func (s *Service) wa() (WhatsAppRoute, WhatsAppSink) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.waRoute, s.waSink
}

// WhatsAppPhone: the resident's WhatsApp when it is the chosen channel.
func (s *Service) WhatsAppPhone(ctx context.Context, name string) string {
	route, sink := s.wa()
	if route == nil || sink == nil || strings.TrimSpace(name) == "" {
		return ""
	}
	return route(ctx, name)
}

// waNames: normalized names of the residents who read WhatsApp.
func (s *Service) waNames(ctx context.Context, residents []club.Resident) map[string]bool {
	out := map[string]bool{}
	for _, r := range residents {
		if r.Name != "" && !r.Former && !r.Archived && s.WhatsAppPhone(ctx, r.Name) != "" {
			out[club.NormName(r.Name)] = true
		}
	}
	return out
}

// SendResident: a personal message to a resident, by the channel the
// resident chose: WhatsApp (sent by Green-API or queued for one click; the
// Telegram buttons become a line with the link) or Telegram (tg). kind and
// key name it for the queue; the same key is never queued twice.
func (s *Service) SendResident(ctx context.Context, kind, key, name string, tg int64, text string, kb map[string]any) error {
	if phone := s.WhatsAppPhone(ctx, name); phone != "" {
		_, sink := s.wa()
		if key == "" {
			key = time.Now().In(club.Almaty).Format("2006-01-02T15:04") + "|" + text
		}
		return sink(ctx, WAMessage{Resident: name, Phone: phone, Kind: kind, Key: kind + "|" + club.NormName(name) + "|" + key, Text: text + kbLinks(kb)})
	}
	if tg == 0 {
		return nil
	}
	return s.SendMessageKB(ctx, tg, text, kb)
}

// SendResidentExpiring: as SendResident, a WhatsApp copy is dropped from the
// queue after until (a reminder useless later).
func (s *Service) SendResidentExpiring(ctx context.Context, kind, key, name string, tg int64, text string, kb map[string]any, until time.Time) error {
	if phone := s.WhatsAppPhone(ctx, name); phone != "" {
		_, sink := s.wa()
		return sink(ctx, WAMessage{Resident: name, Phone: phone, Kind: kind, Key: kind + "|" + club.NormName(name) + "|" + key, Text: text + kbLinks(kb), Expires: &until})
	}
	if tg == 0 {
		return nil
	}
	return s.SendMessageKB(ctx, tg, text, kb)
}

// kbLinks: the links of Telegram buttons as text lines (WhatsApp has no buttons).
func kbLinks(kb map[string]any) string {
	if kb == nil {
		return ""
	}
	b, _ := json.Marshal(kb)
	var k struct {
		Rows [][]struct {
			Text   string `json:"text"`
			URL    string `json:"url"`
			WebApp struct {
				URL string `json:"url"`
			} `json:"web_app"`
		} `json:"inline_keyboard"`
	}
	if json.Unmarshal(b, &k) != nil {
		return ""
	}
	var lines []string
	for _, row := range k.Rows {
		for _, btn := range row {
			u := btn.URL // a web_app button opens only inside Telegram: left out
			if strings.HasPrefix(u, "https://") {
				lines = append(lines, strings.TrimSpace(btn.Text)+": "+u)
			}
		}
	}
	if len(lines) == 0 {
		return ""
	}
	return "\n\n" + strings.Join(lines, "\n")
}

func logWA(err error, what string) {
	if err != nil {
		log.Printf("bot whatsapp %s: %v", what, err)
	}
}
