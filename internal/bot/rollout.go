package bot

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/bnursik/business_surgery_backend/internal/club"
)

// Rollout: what the server takes over, and when. Nobody has to press
// anything: the sheet asks the server every few minutes.
//
//   - report_feedback, evening_reminder, meeting_reminders: as soon as the
//     bot's messages come through the server. Meeting reminders in the
//     script never reached anyone (it read the Chat ID from the «Бывший»
//     column), so the server does them from the start.
//   - daily_check (fines): only after DailyCheckStreak days in a row on which
//     the server's own check matched the sheet's exactly. Once on, it stays on.
//   - bot_private, report_log: not yet.
const DailyCheckStreak = 5

const metaDailySince = "daily_check_since"

// The Telegram app goes through the server (app gateway). Once it has done so
// for AppGatewayDays and was used in the last day, the script refuses calls
// that do not come through the server: nobody can call it as someone else.
const (
	MetaAppFirstOK = "app_first_ok"
	MetaAppLastOK  = "app_last_ok"
	AppGatewayDays = 3
)

// ControlScript is the first script version that asks the server what it
// does (v28). An older script does everything itself, so the server must not
// do the same; only the v27 menu button (old "features" key) is honoured.
const ControlScript = "2026-09-28-6"

const metaLegacyFeatures = "features"

func (s *Service) rollout(ctx context.Context, now time.Time) []string {
	if s.RelayURL() == "" {
		return nil // the bot does not come through the server
	}
	on := []string{FeatureReportFeedback, FeatureEveningReminder, FeatureMeetingReminders}
	if s.dailyCheckReady(ctx, now) {
		on = append(on, FeatureDailyCheck)
	}
	if s.appGatewayReady(ctx, now) {
		on = append(on, FeatureAppGateway)
	}
	return on
}

func (s *Service) dailyCheckReady(ctx context.Context, now time.Time) bool {
	if v, _ := s.repo.GetMeta(ctx, metaDailySince); v != "" {
		return true
	}
	days, ok, err := s.repo.ShadowStreak(ctx, DailyCheckStreak)
	if err != nil || !StreakReady(days, ok, now, DailyCheckStreak) {
		return false
	}
	_ = s.repo.SetMeta(ctx, metaDailySince, now.UTC().Format(time.RFC3339))
	return true
}

// StreakReady: the last n daily comparisons are consecutive days, all
// matched, and the newest is yesterday or the day before.
func StreakReady(days []time.Time, ok []bool, now time.Time, n int) bool {
	if len(days) < n {
		return false
	}
	a := now.In(club.Almaty)
	today := time.Date(a.Year(), a.Month(), a.Day(), 0, 0, 0, 0, club.Almaty)
	newest := time.Date(days[0].Year(), days[0].Month(), days[0].Day(), 0, 0, 0, 0, club.Almaty)
	if today.Sub(newest) > 48*time.Hour {
		return false
	}
	for i := 0; i < n; i++ {
		if !ok[i] {
			return false
		}
		if i > 0 {
			d0 := time.Date(days[i-1].Year(), days[i-1].Month(), days[i-1].Day(), 0, 0, 0, 0, club.Almaty)
			d1 := time.Date(days[i].Year(), days[i].Month(), days[i].Day(), 0, 0, 0, 0, club.Almaty)
			if d0.AddDate(0, 0, -1) != d1 {
				return false
			}
		}
	}
	return true
}

// refreshFeatures works out what the server does now and tells the owner
// when that changes.
func (s *Service) refreshFeatures(ctx context.Context) ([]string, error) {
	var list []string
	if v, err := s.repo.GetMeta(ctx, metaOverride); err != nil {
		return nil, err
	} else if v != "" {
		if err := json.Unmarshal([]byte(v), &list); err != nil {
			list = nil
		}
	} else if v, _ := s.repo.GetMeta(ctx, "script_version"); v < ControlScript {
		if old, _ := s.repo.GetMeta(ctx, metaLegacyFeatures); old != "" {
			_ = json.Unmarshal([]byte(old), &list)
		}
	} else {
		list = s.rollout(ctx, time.Now())
	}
	list, _ = validFeatures(list)
	s.features.set(list)

	b, _ := json.Marshal(list)
	prev, _ := s.repo.GetMeta(ctx, metaEffective)
	if prev != string(b) {
		_ = s.repo.SetMeta(ctx, metaEffective, string(b))
		var old []string
		_ = json.Unmarshal([]byte(prev), &old)
		if msg := featureChangeText(old, list); msg != "" && prev != "" {
			s.sysNote(msg)
		}
	}
	return list, nil
}

var FeatureNames = map[string]string{
	FeatureReportFeedback:   "ответы на отчёты (огонёк, короткий отчёт, не тот топик)",
	FeatureEveningReminder:  "напоминание 22:00",
	FeatureDailyCheck:       "ночная проверка отчётов и штрафы",
	FeatureMeetingReminders: "напоминания о встречах",
	FeatureBotPrivate:       "личные сообщения бота",
	FeatureReportLog:        "запись отчётов",
	FeatureAppGateway:       "приложение в Telegram работает только через сервер",
}

func featureChangeText(old, now []string) string {
	was := map[string]bool{}
	for _, f := range old {
		was[f] = true
	}
	is := map[string]bool{}
	for _, f := range now {
		is[f] = true
	}
	var add, del []string
	for _, f := range KnownFeatures {
		if is[f] && !was[f] {
			add = append(add, "• "+FeatureNames[f])
		}
		if was[f] && !is[f] {
			del = append(del, "• "+FeatureNames[f])
		}
	}
	if len(add) == 0 && len(del) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("🤖 Бот на сервере\n")
	if len(add) > 0 {
		fmt.Fprintf(&b, "\nТеперь делает сервер:\n%s\n", strings.Join(add, "\n"))
	}
	if len(del) > 0 {
		fmt.Fprintf(&b, "\nСнова делает таблица:\n%s\n", strings.Join(del, "\n"))
	}
	b.WriteString("\nВернуть всё таблице: меню BS → «Бот: всё вернуть таблице (аварийно)».")
	return b.String()
}

func (s *Service) appGatewayReady(ctx context.Context, now time.Time) bool {
	first, _ := s.repo.GetMeta(ctx, MetaAppFirstOK)
	last, _ := s.repo.GetMeta(ctx, MetaAppLastOK)
	f, err1 := time.Parse(time.RFC3339, first)
	l, err2 := time.Parse(time.RFC3339, last)
	return err1 == nil && err2 == nil && now.Sub(f) >= AppGatewayDays*24*time.Hour && now.Sub(l) <= 24*time.Hour
}
