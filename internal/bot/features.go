package bot

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/bnursik/business_surgery_backend/internal/club"
)

// Features the server can take over from the Apps Script bot, one at a time.
// The sheet switches them on and off (menu BS) and stops doing the same work
// itself, so nothing is done twice.
const (
	// FeatureReportFeedback: 🔥 on a report, 👍 on a video note, and the
	// private notes about a short report, a report in the wrong topic or a
	// report after midnight. The sheet still keeps «Лог отчётов».
	FeatureReportFeedback = "report_feedback"
	// FeatureEveningReminder: 22:00 reminder to those without a report today.
	FeatureEveningReminder = "evening_reminder"
)

// KnownFeatures in the order they are handed over.
var KnownFeatures = []string{FeatureReportFeedback, FeatureEveningReminder}

const (
	metaFeatures      = "features"
	eveningHour       = 22
	shortNoticeEvery  = 4 * time.Hour // as the script: once in 4 hours per person
	wrongNoticeEvery  = 6 * time.Hour
	reactionWarnEvery = 24 * time.Hour
)

type featureSet struct {
	mu sync.RWMutex
	on map[string]bool
}

func (f *featureSet) has(name string) bool {
	f.mu.RLock()
	defer f.mu.RUnlock()
	return f.on[name]
}

func (s *Service) Has(feature string) bool { return s.features.has(feature) }

// Features returns what the server does itself now.
func (s *Service) Features() []string {
	s.features.mu.RLock()
	defer s.features.mu.RUnlock()
	var out []string
	for _, k := range KnownFeatures {
		if s.features.on[k] {
			out = append(out, k)
		}
	}
	return out
}

// SetFeatures replaces the set of features the server does itself.
func (s *Service) SetFeatures(ctx context.Context, on []string) ([]string, error) {
	known := map[string]bool{}
	for _, k := range KnownFeatures {
		known[k] = true
	}
	m := map[string]bool{}
	for _, f := range on {
		if !known[f] {
			return nil, fmt.Errorf("unknown feature %q", f)
		}
		m[f] = true
	}
	var list []string
	for _, k := range KnownFeatures {
		if m[k] {
			list = append(list, k)
		}
	}
	b, _ := json.Marshal(list)
	if err := s.repo.SetMeta(ctx, metaFeatures, string(b)); err != nil {
		return nil, err
	}
	s.features.mu.Lock()
	s.features.on = m
	s.features.mu.Unlock()
	return list, nil
}

func (s *Service) loadFeatures(ctx context.Context) {
	v, err := s.repo.GetMeta(ctx, metaFeatures)
	if err != nil || v == "" {
		return
	}
	var list []string
	if json.Unmarshal([]byte(v), &list) != nil {
		return
	}
	m := map[string]bool{}
	for _, f := range list {
		m[f] = true
	}
	s.features.mu.Lock()
	s.features.on = m
	s.features.mu.Unlock()
}

// once returns true the first time key is seen within every; a note to a
// person is not repeated too often, across restarts too.
func (s *Service) once(ctx context.Context, key string, every time.Duration) bool {
	ok, err := s.repo.Once(ctx, key, every)
	if err != nil {
		log.Printf("bot once %s: %v", key, err)
		return false // better silent than spamming
	}
	return ok
}

func (s *Service) react(ctx context.Context, chat, msgID int64, emoji string) error {
	_, err := s.call(ctx, "setMessageReaction", map[string]any{
		"chat_id": chat, "message_id": msgID,
		"reaction": []map[string]string{{"type": "emoji", "emoji": emoji}},
	})
	return err
}

// feedback answers a group message the way the script used to, only at once.
func (s *Service) feedback(ctx context.Context, m *GroupMessage, d Decision) {
	if m.Media {
		if err := s.react(ctx, m.ChatID, m.MessageID, "👍"); err != nil {
			log.Printf("bot feedback: video reaction: %v", err)
		}
		return
	}
	isRes := d.Resident != "" && d.Verdict != VerdictNotResident
	switch d.Verdict {
	case VerdictReport:
		if d.Late {
			_ = s.SendMessage(ctx, m.FromID, "Отчёт пришёл после полуночи и не засчитан.\n"+
				"За вчера будет штраф. Сегодняшний отчёт нужно отправить до 23:59.")
			return
		}
		if err := s.react(ctx, m.ChatID, m.MessageID, "🔥"); err != nil {
			log.Printf("bot feedback: reaction: %v", err)
			if s.once(ctx, "reactwarn", reactionWarnEvery) {
				for _, id := range s.admins {
					_ = s.SendMessage(ctx, id, "Бот не смог поставить реакцию на отчёт.\nПричина: "+err.Error()+
						"\n\nПроверьте, что бот админ группы")
				}
			}
		}
	case VerdictShort:
		if s.once(ctx, fmt.Sprintf("short:%d", m.FromID), shortNoticeEvery) {
			_ = s.SendMessage(ctx, m.FromID, fmt.Sprintf("⚠️ Сообщение слишком короткое (%d симв.), как отчёт оно не засчитано.\n\n"+
				"Нужно минимум 100 символов: что сделал, что не получилось, что завтра.\n"+
				"Шаблон — команда /help. Отправьте полный отчёт, иначе ночью будет штраф.", d.Len))
		}
	case VerdictWrongTopic:
		if isRes && s.once(ctx, fmt.Sprintf("wrongtopic:%d", m.FromID), wrongNoticeEvery) {
			_ = s.SendMessage(ctx, m.FromID, "⚠️ Это похоже на отчёт, но он не в том разделе.\n\n"+
				"Отчёт засчитывается только в топике «ОТЧЁТЫ».\n"+
				"Здесь он не сохранён. Отправьте его в нужный топик, иначе ночью будет штраф.")
		}
	}
}

// ReminderText fills the «Настройки» text report_reminder, as getText does.
func ReminderText(tpl, first string) string {
	if strings.TrimSpace(tpl) == "" {
		tpl = "⏰ {имя}, напоминание!\n\nДо конца дня немного. не забудьте сдать отчёт!\n\nШтраф за пропуск: 10 000 тг"
	}
	return strings.ReplaceAll(tpl, "{имя}", first)
}

// EveningTargets: active residents with a Chat ID and no report today.
// Admins and exceptions are not reminded (the script reminded admins too).
func EveningTargets(residents []club.Resident, reported []DayReport) []club.Resident {
	var out []club.Resident
	for _, r := range residents {
		if r.Name == "" || r.Former || r.Exception || r.Admin || r.TgID == 0 {
			continue
		}
		if submitted(r, reported) {
			continue
		}
		out = append(out, r)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// maybeEveningReminder sends the 22:00 reminder once a day.
func (s *Service) maybeEveningReminder(ctx context.Context, now time.Time) ([]string, error) {
	if !s.Has(FeatureEveningReminder) {
		return nil, nil
	}
	a := now.In(club.Almaty)
	if a.Hour() != eveningHour {
		return nil, nil // only within 22:00–22:59; a restart at 23:10 must not send it late
	}
	day := time.Date(a.Year(), a.Month(), a.Day(), 0, 0, 0, 0, club.Almaty)
	// The server knows today's reports only while the bot comes through it.
	// Silent for 6 hours in the evening means it does not: reminding everyone
	// would be wrong, so the owner is told instead.
	if st, err := s.repo.Stats(ctx); err != nil || st.LastReceivedAt == nil || now.Sub(*st.LastReceivedAt) > 6*time.Hour {
		if s.once(ctx, "evening-skip:"+day.Format("2006-01-02"), 20*time.Hour) {
			for _, id := range s.admins {
				_ = s.SendMessage(ctx, id, "⚠️ Напоминание 22:00 не отправлено: сервер 6 часов не получал сообщений бота и не знает, кто сдал отчёт.\n\nМеню BS → «Диагностика бота».")
			}
		}
		return nil, nil
	}
	if !s.once(ctx, "evening:"+day.Format("2006-01-02"), 20*time.Hour) {
		return nil, nil
	}
	snap, err := s.repo.Club().Load(ctx)
	if err != nil {
		return nil, err
	}
	rows, err := s.repo.ServerReports(ctx, day)
	if err != nil {
		return nil, err
	}
	var rep []DayReport
	for _, x := range rows {
		rep = append(rep, DayReport{TgID: x.TgID, Name: x.Name})
	}
	tpl, _ := s.repo.Setting(ctx, "report_reminder")
	var sent []string
	for _, r := range EveningTargets(snap.Residents, rep) {
		first := strings.Fields(r.Name)[0]
		if err := s.SendMessage(ctx, r.TgID, ReminderText(tpl, first)); err != nil {
			log.Printf("bot evening: %s: %v", r.Name, err)
			continue
		}
		sent = append(sent, r.Name)
	}
	log.Printf("bot evening reminder: %d sent", len(sent))
	return sent, nil
}
