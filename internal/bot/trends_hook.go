package bot

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
)

// R53: «Тренды Threads». Команда присылает боту ссылку на чужой пост Threads
// (из приложения Threads: «Поделиться» → Telegram, или просто текстом,
// можно командой /trend <ссылка>): ссылка попадает во «Входящие» трендов на
// платформе, бот отвечает одной строкой.

// TrendHook takes the team's text with Threads links; false: no links.
type TrendHook func(ctx context.Context, chatID int64, text string) (reply string, ok bool)

var trendHooks sync.Map // *Service → TrendHook

func (s *Service) SetTrendHook(h TrendHook) { trendHooks.Store(s, h) }

func (s *Service) trendHook() TrendHook {
	if v, ok := trendHooks.Load(s); ok {
		return v.(TrendHook)
	}
	return nil
}

// msgAllText: the message's text, caption and the links hidden under words.
func msgAllText(body []byte) string {
	var u struct {
		Message *struct {
			Text     string `json:"text"`
			Caption  string `json:"caption"`
			Entities []struct {
				URL string `json:"url"`
			} `json:"entities"`
			CaptionEntities []struct {
				URL string `json:"url"`
			} `json:"caption_entities"`
		} `json:"message"`
	}
	if json.Unmarshal(body, &u) != nil || u.Message == nil {
		return ""
	}
	parts := []string{u.Message.Text, u.Message.Caption}
	for _, e := range u.Message.Entities {
		parts = append(parts, e.URL)
	}
	for _, e := range u.Message.CaptionEntities {
		parts = append(parts, e.URL)
	}
	return strings.Join(parts, "\n")
}

func hasThreadsHost(s string) bool {
	l := strings.ToLower(s)
	return strings.Contains(l, "threads.com/") || strings.Contains(l, "threads.net/")
}

// takeTrend: a team member's message with a Threads post link (or /trend).
func (s *Service) takeTrend(ctx context.Context, m privMsg, body []byte) bool {
	cmd := command(m.Text)
	if cmd != "" && cmd != "/trend" && cmd != "/тренд" {
		return false
	}
	h := s.trendHook()
	all := msgAllText(body)
	if h == nil || !hasThreadsHost(all) {
		if cmd != "" {
			_ = s.SendMessage(ctx, m.ChatID, "Пришлите ссылку на пост Threads: /trend https://www.threads.com/@автор/post/…\nИли просто перешлите ссылку боту: она попадёт в «Тренды Threads» на платформе.")
			return true
		}
		return false
	}
	reply, ok := h(ctx, m.ChatID, all)
	if !ok {
		if cmd != "" {
			_ = s.SendMessage(ctx, m.ChatID, "Не вижу ссылки на пост: нужна ссылка вида threads.com/@автор/post/…")
			return true
		}
		return false
	}
	url := s.platformURL(ctx) + "?section=mTrends"
	_ = s.SendMessageKB(ctx, m.ChatID, reply, kb([]map[string]any{{"text": "🧵 Тренды Threads", "web_app": map[string]string{"url": url}}}))
	return true
}
