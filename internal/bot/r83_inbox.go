package bot

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"
)

// R83: «Быстрая заметка» from Telegram. A team member forwards a message to
// the bot (or writes, sends a voice note or a photo): it lands in the
// platform's inbox (handlers/http/r83_inbox.go). Commands and Threads links
// keep their own meaning; residents and leads are never affected.

// InboxNote is one message for the inbox.
type InboxNote struct {
	FromID    int64
	FromName  string
	Text      string // text or caption
	Forward   string // who wrote the forwarded message
	Photo     []byte
	PhotoMime string
	Voice     []byte
	VoiceMime string
}

// InboxHook keeps the note; reply goes back to the chat. ok false: not taken.
type InboxHook func(ctx context.Context, n InboxNote) (reply string, ok bool)

var inboxHooks sync.Map // *Service → InboxHook

func (s *Service) SetInboxHook(h InboxHook) { inboxHooks.Store(s, h) }

func (s *Service) inboxHook() InboxHook {
	if v, ok := inboxHooks.Load(s); ok {
		return v.(InboxHook)
	}
	return nil
}

type inboxMsg struct {
	Text    string `json:"text"`
	Caption string `json:"caption"`
	Photo   []struct {
		FileID   string `json:"file_id"`
		FileSize int64  `json:"file_size"`
		Width    int    `json:"width"`
	} `json:"photo"`
	Voice *struct {
		FileID   string `json:"file_id"`
		MimeType string `json:"mime_type"`
		FileSize int64  `json:"file_size"`
	} `json:"voice"`
	Audio *struct {
		FileID   string `json:"file_id"`
		MimeType string `json:"mime_type"`
		FileSize int64  `json:"file_size"`
	} `json:"audio"`
	Document *struct {
		FileName string `json:"file_name"`
	} `json:"document"`
	ForwardOrigin *struct {
		Type       string `json:"type"`
		SenderName string `json:"sender_user_name"`
		SenderUser *struct {
			FirstName string `json:"first_name"`
			LastName  string `json:"last_name"`
			Username  string `json:"username"`
		} `json:"sender_user"`
		Chat *struct {
			Title string `json:"title"`
		} `json:"chat"`
	} `json:"forward_origin"`
	ForwardFrom *struct {
		FirstName string `json:"first_name"`
		LastName  string `json:"last_name"`
	} `json:"forward_from"`
	ForwardSenderName string `json:"forward_sender_name"`
	ForwardChat       *struct {
		Title string `json:"title"`
	} `json:"forward_from_chat"`
}

func (m inboxMsg) forwardName() string {
	if o := m.ForwardOrigin; o != nil {
		switch {
		case o.SenderUser != nil:
			n := strings.TrimSpace(o.SenderUser.FirstName + " " + o.SenderUser.LastName)
			if n == "" && o.SenderUser.Username != "" {
				n = "@" + o.SenderUser.Username
			}
			return n
		case o.SenderName != "":
			return o.SenderName
		case o.Chat != nil:
			return o.Chat.Title
		}
	}
	if m.ForwardFrom != nil {
		return strings.TrimSpace(m.ForwardFrom.FirstName + " " + m.ForwardFrom.LastName)
	}
	if m.ForwardSenderName != "" {
		return m.ForwardSenderName
	}
	if m.ForwardChat != nil {
		return m.ForwardChat.Title
	}
	return ""
}

// ReadInbox: what of a private message goes to the inbox (no files yet).
func ReadInbox(body []byte) (inboxMsg, bool) {
	var u struct {
		Message *inboxMsg `json:"message"`
	}
	if json.Unmarshal(body, &u) != nil || u.Message == nil {
		return inboxMsg{}, false
	}
	return *u.Message, true
}

const inboxFileMax = 20 << 20

// takeInbox: a team member's message without a command → the inbox.
func (s *Service) takeInbox(ctx context.Context, m privMsg, body []byte) bool {
	h := s.inboxHook()
	if h == nil {
		return false
	}
	cmd := command(m.Text)
	if cmd != "" && cmd != "/note" && cmd != "/заметка" {
		return false
	}
	im, ok := ReadInbox(body)
	if !ok {
		return false
	}
	n := InboxNote{FromID: m.FromID, FromName: m.name(), Text: strings.TrimSpace(im.Text + "\n" + im.Caption), Forward: im.forwardName()}
	if cmd != "" {
		if parts := strings.SplitN(strings.TrimSpace(im.Text), " ", 2); len(parts) == 2 {
			n.Text = strings.TrimSpace(parts[1])
		} else {
			n.Text = ""
		}
		if n.Text == "" {
			_ = s.SendMessage(ctx, m.ChatID, "Напишите заметку после команды: /note позвонить бухгалтеру. Или просто перешлите боту любое сообщение.")
			return true
		}
	}
	if im.Document != nil && n.Text == "" {
		n.Text = "📎 Файл: " + im.Document.FileName + " (откройте в Telegram)"
	}
	if len(im.Photo) > 0 {
		best := im.Photo[len(im.Photo)-1]
		if b, mt, err := s.DownloadFile(ctx, best.FileID, inboxFileMax); err == nil {
			n.Photo, n.PhotoMime = b, mt
		}
	}
	if v := im.Voice; v != nil {
		if b, _, err := s.DownloadFile(ctx, v.FileID, inboxFileMax); err == nil {
			n.Voice, n.VoiceMime = b, firstNonEmpty(v.MimeType, "audio/ogg")
		}
	} else if a := im.Audio; a != nil {
		if b, _, err := s.DownloadFile(ctx, a.FileID, inboxFileMax); err == nil {
			n.Voice, n.VoiceMime = b, firstNonEmpty(a.MimeType, "audio/mpeg")
		}
	}
	if n.Text == "" && len(n.Photo) == 0 && len(n.Voice) == 0 {
		return false
	}
	c, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	reply, ok := h(c, n)
	if !ok {
		return false
	}
	if reply != "" {
		_ = s.SendMessageKB(ctx, m.ChatID, reply, kb([]map[string]any{{"text": "📥 Быстрые заметки", "web_app": map[string]string{"url": s.platformURL(ctx) + "/?note=list"}}}))
	}
	return true
}

func firstNonEmpty(v ...string) string {
	for _, s := range v {
		if strings.TrimSpace(s) != "" {
			return s
		}
	}
	return ""
}

// DownloadFile: a file of a message (getFile, then the file itself).
func (s *Service) DownloadFile(ctx context.Context, fileID string, max int64) ([]byte, string, error) {
	raw, err := s.call(ctx, "getFile", map[string]any{"file_id": fileID})
	if err != nil {
		return nil, "", err
	}
	var f struct {
		FilePath string `json:"file_path"`
		FileSize int64  `json:"file_size"`
	}
	_ = json.Unmarshal(raw, &f)
	if f.FilePath == "" {
		return nil, "", fmt.Errorf("telegram getFile: no path")
	}
	if f.FileSize > max {
		return nil, "", fmt.Errorf("telegram file: too big")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, s.apiBase+"/file/bot"+s.token+"/"+f.FilePath, nil)
	if err != nil {
		return nil, "", err
	}
	resp, err := s.tgClient.Do(req)
	if err != nil {
		return nil, "", fmt.Errorf("telegram file: request failed")
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, "", fmt.Errorf("telegram file: %d", resp.StatusCode)
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, max))
	if err != nil {
		return nil, "", err
	}
	mt := resp.Header.Get("Content-Type")
	if mt == "" || mt == "application/octet-stream" {
		mt = http.DetectContentType(b)
	}
	return b, mt, nil
}
