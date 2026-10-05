package bot

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"mime/multipart"
	"net/http"
	"strconv"
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
	// Referral links (ref_<chatId>) of new people are the server's too (referral.go).
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

// CallbackUpdate: a button pressed in the bot, by a lead (the script's old
// menus) or by the team (the server's own buttons, see TeamCallbackHook).
type CallbackUpdate struct {
	ID        string
	ChatID    int64
	MessageID int64
	FromID    int64
	Data      string
	FirstName string
	LastName  string
	Username  string
}

// CallbackHook answers it; false leaves it to the script.
type CallbackHook func(ctx context.Context, cb CallbackUpdate) bool

func (s *Service) SetCallbackHook(h CallbackHook) {
	s.mu.Lock()
	s.cbHook = h
	s.mu.Unlock()
}

// TeamCallbackHook answers a button of the server's own messages to the team
// (callback_data with the hook's prefix, pressed by someone in PLATFORM_TEAM).
// toast is shown to the person who pressed; ok false leaves it to the script.
type TeamCallbackHook func(ctx context.Context, cb CallbackUpdate) (toast string, ok bool)

// SetTeamCallbackHook registers h for callback_data starting with prefix (e.g. "cnt_").
func (s *Service) SetTeamCallbackHook(prefix string, h TeamCallbackHook) {
	s.mu.Lock()
	if s.teamCb == nil {
		s.teamCb = map[string]TeamCallbackHook{}
	}
	if h == nil {
		delete(s.teamCb, prefix)
	} else {
		s.teamCb[prefix] = h
	}
	s.mu.Unlock()
}

func (s *Service) teamHook(data string) TeamCallbackHook {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for p, h := range s.teamCb {
		if strings.HasPrefix(data, p) {
			return h
		}
	}
	return nil
}

// ClaimCallbacks: «Я резидент BS» (the welcome's button, also the script's
// old one) and «Я уже в клубе» under the server's answer.
func ClaimCallbacks(data string) bool { return data == "i_am_resident" || data == "claim_recheck" }

// SetClaimHook answers ClaimCallbacks; false leaves the button to the script.
func (s *Service) SetClaimHook(h CallbackHook) {
	s.mu.Lock()
	s.claimHook = h
	s.mu.Unlock()
}

// LeadCallbacks: the script's lead-magnet menu buttons the server now answers.
func LeadCallbacks(data string) bool {
	return data == "sub_leadmagnets" || data == "sub_menu" || strings.HasPrefix(data, "lm_")
}

func (s *Service) takeCallback(ctx context.Context, body []byte) bool {
	var u struct {
		Callback *struct {
			ID   string `json:"id"`
			Data string `json:"data"`
			From struct {
				ID        int64  `json:"id"`
				FirstName string `json:"first_name"`
				LastName  string `json:"last_name"`
				Username  string `json:"username"`
			} `json:"from"`
			Message *struct {
				MessageID int64 `json:"message_id"`
				Chat      struct {
					ID   int64  `json:"id"`
					Type string `json:"type"`
				} `json:"chat"`
			} `json:"message"`
		} `json:"callback_query"`
	}
	if json.Unmarshal(body, &u) != nil || u.Callback == nil || u.Callback.Message == nil {
		return false
	}
	cb := u.Callback
	up := CallbackUpdate{ID: cb.ID, ChatID: cb.Message.Chat.ID, MessageID: cb.Message.MessageID, FromID: cb.From.ID,
		Data: cb.Data, FirstName: cb.From.FirstName, LastName: cb.From.LastName, Username: cb.From.Username}
	// R38c: buttons anyone may press (an event's «Иду»), in a private chat.
	if ph := s.publicHook(cb.Data); ph != nil && cb.Message.Chat.Type == "private" {
		c, cancel := context.WithTimeout(ctx, 60*time.Second)
		defer cancel()
		if toast, ok := ph(c, up); ok {
			p := map[string]any{"callback_query_id": cb.ID}
			if toast != "" {
				p["text"] = toast
			}
			_, _ = s.call(ctx, "answerCallbackQuery", p)
			return true
		}
	}
	// The team's buttons on the server's own messages.
	if th := s.teamHook(cb.Data); th != nil && s.isAdmin(cb.From.ID) {
		c, cancel := context.WithTimeout(ctx, 60*time.Second)
		defer cancel()
		toast, ok := th(c, up)
		if !ok {
			return false
		}
		p := map[string]any{"callback_query_id": cb.ID}
		if toast != "" {
			p["text"] = toast
		}
		_, _ = s.call(ctx, "answerCallbackQuery", p)
		return true
	}
	// «Я резидент BS» and «Я уже в клубе»: the server checks the list itself
	// (residents included: a resident pressing it gets «вы уже резидент»).
	if ClaimCallbacks(cb.Data) && cb.Message.Chat.Type == "private" && !s.isAdmin(cb.From.ID) {
		s.mu.RLock()
		ch := s.claimHook
		s.mu.RUnlock()
		if ch == nil {
			return false
		}
		c, cancel := context.WithTimeout(ctx, 60*time.Second)
		defer cancel()
		if !ch(c, up) {
			return false
		}
		_, _ = s.call(ctx, "answerCallbackQuery", map[string]any{"callback_query_id": cb.ID})
		return true
	}
	s.mu.RLock()
	h := s.cbHook
	s.mu.RUnlock()
	if h == nil || cb.Message.Chat.Type != "private" {
		return false
	}
	if !LeadCallbacks(cb.Data) || s.isAdmin(cb.From.ID) {
		return false
	}
	if _, known, err := s.repo.ResidentByTgID(ctx, cb.From.ID); err != nil || known {
		return false
	}
	c, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	if !h(c, up) {
		return false
	}
	_, _ = s.call(ctx, "answerCallbackQuery", map[string]any{"callback_query_id": cb.ID})
	return true
}

// SendMessageID sends text with an inline keyboard and returns the message id.
func (s *Service) SendMessageID(ctx context.Context, chatID int64, text string, kb map[string]any) (int64, error) {
	p := map[string]any{"chat_id": chatID, "text": text, "disable_web_page_preview": true}
	if kb != nil {
		p["reply_markup"] = kb
	}
	raw, err := s.call(ctx, "sendMessage", p)
	if err != nil {
		return 0, err
	}
	var m struct {
		MessageID int64 `json:"message_id"`
	}
	_ = json.Unmarshal(raw, &m)
	return m.MessageID, nil
}

// SendChannel posts text to a channel (chat "@name" or "-100…"); the bot must
// be its admin. Returns the message id.
func (s *Service) SendChannel(ctx context.Context, chat, text string) (int64, error) {
	var id any = chat
	if n, err := strconv.ParseInt(chat, 10, 64); err == nil {
		id = n
	}
	raw, err := s.call(ctx, "sendMessage", map[string]any{"chat_id": id, "text": text})
	if err != nil {
		return 0, err
	}
	var m struct {
		MessageID int64 `json:"message_id"`
	}
	_ = json.Unmarshal(raw, &m)
	return m.MessageID, nil
}

// EditMessageKB replaces the text and the buttons of a message the bot sent.
func (s *Service) EditMessageKB(ctx context.Context, chatID, msgID int64, text string, kb map[string]any) error {
	p := map[string]any{"chat_id": chatID, "message_id": msgID, "text": text, "disable_web_page_preview": true}
	if kb != nil {
		p["reply_markup"] = kb
	}
	_, err := s.call(ctx, "editMessageText", p)
	if err != nil && strings.Contains(err.Error(), "message is not modified") {
		return nil
	}
	return err
}

// SendPhotoKB sends a picture (uploaded once, then by file_id) with a caption.
func (s *Service) SendPhotoKB(ctx context.Context, chatID int64, key string, photo []byte, caption string, kb map[string]any) error {
	if v, ok := s.files.Load(key); ok {
		p := map[string]any{"chat_id": chatID, "photo": v, "caption": caption, "parse_mode": "HTML"}
		if kb != nil {
			p["reply_markup"] = kb
		}
		if _, err := s.call(ctx, "sendPhoto", p); err == nil {
			return nil
		}
		s.files.Delete(key)
	}
	return s.upload(ctx, "sendPhoto", "photo", key, key+".jpg", photo, map[string]any{"chat_id": chatID, "caption": caption, "parse_mode": "HTML", "reply_markup": kb})
}

// SendDocumentKB sends a file (uploaded once, then by file_id); fileID may be a known Telegram file.
func (s *Service) SendDocumentKB(ctx context.Context, chatID int64, key, name string, data []byte, fileID, caption string, kb map[string]any) error {
	if fileID == "" {
		if v, ok := s.files.Load(key); ok {
			fileID = v.(string)
		}
	}
	if fileID != "" {
		p := map[string]any{"chat_id": chatID, "document": fileID, "caption": caption}
		if kb != nil {
			p["reply_markup"] = kb
		}
		_, err := s.call(ctx, "sendDocument", p)
		return err
	}
	return s.upload(ctx, "sendDocument", "document", key, name, data, map[string]any{"chat_id": chatID, "caption": caption, "reply_markup": kb})
}

func (s *Service) upload(ctx context.Context, method, field, key, name string, data []byte, params map[string]any) error {
	var body bytes.Buffer
	w := multipart.NewWriter(&body)
	for k, v := range params {
		if v == nil {
			continue
		}
		if m, ok := v.(map[string]any); ok {
			if m == nil {
				continue
			}
			b, _ := json.Marshal(m)
			_ = w.WriteField(k, string(b))
			continue
		}
		_ = w.WriteField(k, fmt.Sprint(v))
	}
	fw, _ := w.CreateFormFile(field, name)
	_, _ = fw.Write(data)
	_ = w.Close()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.apiBase+"/bot"+s.token+"/"+method, &body)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", w.FormDataContentType())
	resp, err := s.tgClient.Do(req)
	if err != nil {
		return fmt.Errorf("telegram %s: request failed", method)
	}
	defer resp.Body.Close()
	var r struct {
		OK          bool   `json:"ok"`
		Description string `json:"description"`
		Result      struct {
			Photo []struct {
				FileID string `json:"file_id"`
			} `json:"photo"`
			Document struct {
				FileID string `json:"file_id"`
			} `json:"document"`
		} `json:"result"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&r); err != nil {
		return fmt.Errorf("telegram %s: bad answer", method)
	}
	if !r.OK {
		return fmt.Errorf("telegram %s: %s", method, r.Description)
	}
	if n := len(r.Result.Photo); n > 0 {
		s.files.Store(key, r.Result.Photo[n-1].FileID)
	} else if r.Result.Document.FileID != "" {
		s.files.Store(key, r.Result.Document.FileID)
	}
	return nil
}

// EnsureMenuButton: the button left of the input field opens the app and is called «BS».
func (s *Service) EnsureMenuButton(ctx context.Context) {
	raw, err := s.call(ctx, "getChatMenuButton", map[string]any{})
	if err != nil {
		return
	}
	var cur struct {
		Type   string `json:"type"`
		Text   string `json:"text"`
		WebApp struct {
			URL string `json:"url"`
		} `json:"web_app"`
	}
	_ = json.Unmarshal(raw, &cur)
	url := cur.WebApp.URL
	if url == "" {
		url = WebAppBase
	}
	if cur.Type == "web_app" && cur.Text == "BS" {
		return
	}
	_, err = s.call(ctx, "setChatMenuButton", map[string]any{"menu_button": map[string]any{"type": "web_app", "text": "BS", "web_app": map[string]string{"url": url}}})
	log.Printf("bot: menu button «BS» → %s err=%v", url, err)
}
