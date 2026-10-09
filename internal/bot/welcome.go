package bot

import (
	"context"
	"encoding/json"
	"log"
	"strconv"
	"strings"
	"time"
)

// R70: the owner confirms a new resident with one tap (resident_claim.go),
// and the bot sends the welcome as the script's _miniApproveResident did:
// a greeting with the platform and the Mini App, a personal invite link to
// the club group, the offer (if not accepted yet), and the five onboarding
// days start at once.

// ClubGroupLink: the group's old shared link, used when the bot cannot make
// a personal one (not an admin of the group, Telegram error).
const ClubGroupLink = "https://t.me/+M88HVtcZNghjNTM6"

// ClubInvite makes a one-person invite link to the club group (7 days).
func (s *Service) ClubInvite(ctx context.Context, name string) string {
	n := []rune(strings.TrimSpace("Резидент " + name))
	if len(n) > 32 {
		n = n[:32]
	}
	raw, err := s.call(ctx, "createChatInviteLink", map[string]any{"chat_id": DefaultGroupID, "name": string(n),
		"member_limit": 1, "expire_date": time.Now().Add(7 * 24 * time.Hour).Unix()})
	if err == nil {
		var r struct {
			Link string `json:"invite_link"`
		}
		if json.Unmarshal(raw, &r) == nil && strings.HasPrefix(r.Link, "https://") {
			return r.Link
		}
	}
	log.Printf("bot welcome: personal group link: %v (the shared one is sent)", err)
	return ClubGroupLink
}

// WelcomeText: the first message of a confirmed resident.
func WelcomeText(first, platform string) string {
	hi := "🎉 Добро пожаловать в Business Surgery"
	if r := []rune(strings.TrimSpace(first)); len(r) > 0 {
		hi += ", " + strings.ToUpper(string(r[:1])) + string(r[1:])
	}
	host := strings.TrimPrefix(strings.TrimPrefix(platform, "https://"), "http://")
	return hi + "!\n\nКоманда подтвердила: вы резидент клуба.\n\n" +
		"💬 Закрытый чат резидентов: кнопка «Вступить в чат резидентов» ниже, ссылка личная.\n" +
		"📱 Приложение клуба: встречи, отчёты, штрафы, резиденты. Кнопка «Открыть приложение BS».\n" +
		"💻 Платформа BS: " + host + "\nРазборы, задачи, отчёты и прогресс, удобнее с ноутбука. Вход в один тап: «Войти через Telegram» и подтвердить в Telegram, пароль не нужен.\n\n" +
		"Ниже оферта клуба. Дальше 5 коротких сообщений, по одному в день: как получить от клуба максимум."
}

// WelcomeResident sends the welcome package to tg and starts the onboarding.
// Returns the group link that was sent.
func (s *Service) WelcomeResident(ctx context.Context, tg int64, name string) (string, error) {
	first := firstName(strings.TrimSpace(name))
	platform := s.platformURL(ctx)
	link := s.ClubInvite(ctx, name)
	keys := kb(
		[]map[string]any{{"text": "👥 Вступить в чат резидентов", "url": link}},
		appBtn("📱 Открыть приложение BS", ""),
		[]map[string]any{{"text": "💻 Платформа BS", "url": platform}},
	)
	if err := s.SendMessageKB(ctx, tg, WelcomeText(first, platform), keys); err != nil {
		return link, err
	}
	if !s.accepted(ctx, tg) {
		_ = s.SendMessageKB(ctx, tg, PolicyText, kb([]map[string]any{{"text": "✅ Принять и продолжить", "callback_data": "accept_terms"}}))
	}
	s.StartOnboarding(ctx, tg, name)
	_ = s.repo.SetMeta(ctx, "welcome:"+strconv.FormatInt(tg, 10), time.Now().UTC().Format(time.RFC3339))
	return link, nil
}
