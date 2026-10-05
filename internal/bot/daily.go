package bot

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/bnursik/business_surgery_backend/internal/club"
)

// The daily check at 10:00 Almaty: yesterday's reports (a report counts for
// the day it was sent, deadline 23:59, so by the morning the day is closed),
// the stop rule, fines for the rest, a note to each fined resident and one
// summary to the admins. The fines go into the sheet's «Штрафы» through the
// script (the sheet still holds the club's money). While the server does the
// check (daily_check), the script does not: one sender, no duplicates.
//
// Exceptions, admins and former residents are never checked. A resident
// without a Chat ID is not checked either and is not named in the summary:
// the bot cannot see his reports, so that is not news for the daily note
// (the sheet's «Диагностика бота» lists them for the team).
const (
	FineAmount    = 10000
	FineType      = "Не сдан отчёт"
	KaspiLink     = "https://pay.kaspi.kz/pay/ri6h2lj5"
	WebAppBase    = "https://neuvolen.github.io/bs-app/"
	dailyFromHour = 10
	dailyFromMin  = 0
	dailyUntil    = 14 // after this the day is left alone
)

func webApp(page string) string { return WebAppBase + "?p=" + page }

func appButton(page string) map[string]any {
	return map[string]any{"inline_keyboard": [][]map[string]any{{{"text": "📱 Открыть в BS", "web_app": map[string]string{"url": webApp(page)}}}}}
}

// SendMessageKB sends text with an inline keyboard.
func (s *Service) SendMessageKB(ctx context.Context, chatID int64, text string, kb map[string]any) error {
	p := map[string]any{"chat_id": chatID, "text": text, "disable_web_page_preview": true}
	if kb != nil {
		p["reply_markup"] = kb
	}
	_, err := s.call(ctx, "sendMessage", p)
	return err
}

// FineRow is one fine written into the sheet.
type FineRow struct {
	Name   string `json:"name"`
	Type   string `json:"type"`
	Amount int64  `json:"amount"`
	Date   string `json:"date"` // dd.MM.yyyy
}

// ScriptCall runs an operation in the Apps Script (doPost, bsAction "srv"),
// signed with the bot token.
func (s *Service) ScriptCall(ctx context.Context, op string, args map[string]any, out any) error {
	if !club.SheetLegacy() {
		return errors.New("таблица отключена")
	}
	url := s.RelayURL()
	if url == "" {
		return errors.New("no script address")
	}
	data := map[string]any{"ts": time.Now().Unix(), "op": op}
	for k, v := range args {
		data[k] = v
	}
	db, _ := json.Marshal(data)
	m := hmac.New(sha256.New, []byte(s.token))
	m.Write(db)
	body, _ := json.Marshal(map[string]string{"bsAction": "srv", "data": string(db), "sig": hex.EncodeToString(m.Sum(nil))})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	// Apps Script answers with a redirect to the result: follow it.
	resp, err := (&http.Client{Timeout: 120 * time.Second}).Do(req)
	if err != nil {
		return errors.New("script unreachable")
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	var r struct {
		OK    bool   `json:"ok"`
		Error string `json:"error"`
	}
	if json.Unmarshal(raw, &r) != nil {
		return fmt.Errorf("script answered %d without a result", resp.StatusCode)
	}
	if !r.OK {
		return fmt.Errorf("script: %s", r.Error)
	}
	if out != nil {
		return json.Unmarshal(raw, out)
	}
	return nil
}

// DailyOutcome is what one run of the daily check did.
type DailyOutcome struct {
	Day     string   `json:"day"`
	Stopped bool     `json:"stopped"`
	Fined   []string `json:"fined"`
	Summary string   `json:"summary"`
}

func fill(tpl string, vars map[string]string) string {
	for k, v := range vars {
		tpl = strings.ReplaceAll(tpl, "{"+k+"}", v)
	}
	return tpl
}

// DailySummary is the admins' note: who submitted, who did not. Residents
// without a Chat ID are left out of it altogether (not checked, not counted).
func DailySummary(r DayResult, fined []string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "📋 Проверка отчётов за %s\n\n", r.Day)
	total := r.Active - len(r.NoChatID)
	done := total - len(r.WouldFine)
	if done < 0 {
		done = 0
	}
	fmt.Fprintf(&b, "Сдали отчёт: %d из %d\n\n", done, total)
	// Everyone who did not submit is named, also when the fine was already
	// in the sheet (fined holds only the rows added now).
	missed := r.WouldFine
	if len(missed) == 0 {
		missed = fined
	}
	if len(missed) > 0 {
		b.WriteString("❌ Не сдали (штраф 10 000 тг):\n")
		for _, n := range missed {
			fmt.Fprintf(&b, "  • %s\n", n)
		}
	} else {
		b.WriteString("✅ Все сдали отчёт!")
	}
	if len(r.Meeting) > 0 {
		fmt.Fprintf(&b, "\n\nБыла встреча, отчёт не нужен: %s", strings.Join(r.Meeting, ", "))
	}
	return strings.TrimRight(b.String(), "\n")
}

func (s *Service) maybeDailyCheck(ctx context.Context, now time.Time) (*DailyOutcome, error) {
	if !s.Has(FeatureDailyCheck) {
		return nil, nil
	}
	a := now.In(club.Almaty)
	start := time.Date(a.Year(), a.Month(), a.Day(), dailyFromHour, dailyFromMin, 0, 0, club.Almaty)
	if a.Before(start) || a.Hour() >= dailyUntil {
		return nil, nil
	}
	today := time.Date(a.Year(), a.Month(), a.Day(), 0, 0, 0, 0, club.Almaty)
	day := today.AddDate(0, 0, -1)
	key := "daily_done:" + day.Format("2006-01-02")
	if v, _ := s.repo.GetMeta(ctx, key); v != "" {
		return nil, nil
	}
	// A failed run is tried again every 10 minutes, not every minute.
	if !s.once(ctx, "daily_try:"+day.Format("2006-01-02"), 10*time.Minute) {
		return nil, nil
	}
	res, err := s.CompareDay(ctx, day, now)
	if err != nil {
		return nil, err
	}
	out := &DailyOutcome{Day: res.Day}
	if res.StopCrane || res.Partial {
		out.Stopped = true
		out.Summary = "🚨 ШТРАФЫ НЕ ВЫСТАВЛЕНЫ. ПОХОЖЕ НА СБОЙ\n\nПроверка отчётов за " + res.Day + " остановлена.\n\n" +
			fmt.Sprintf("Почему: за день ни одного отчёта (активных резидентов %d), или сервер получал сообщения бота не весь день.\n\n", res.Active) +
			"Штрафовать вслепую нельзя: если бот не получает сообщения, резиденты не виноваты.\n\n" +
			"Что сделать: в таблице меню BS → «Диагностика бота»."
		s.sysNote(out.Summary)
		_ = s.repo.SetMeta(ctx, key, "stopped")
		return out, nil
	}
	var fines []FineRow
	for _, n := range res.WouldFine {
		fines = append(fines, FineRow{Name: n, Type: FineType, Amount: FineAmount, Date: res.Day})
	}
	var added struct {
		Added []string `json:"added"`
	}
	if len(fines) > 0 && !club.SheetLegacy() {
		// After the cutover the fines go straight into the server's club tables.
		if s.fineSink == nil {
			return nil, errors.New("no place to keep the fines")
		}
		list, err := s.fineSink(ctx, fines)
		if err != nil {
			if s.once(ctx, "daily_fail:"+day.Format("2006-01-02"), 3*time.Hour) {
				s.sysNote("Проверка отчётов за " + res.Day + ": штрафы не записались (" + err.Error() + "), повтор через 10 минут")
			}
			return nil, err
		}
		added.Added = list
	} else if len(fines) > 0 {
		if err := s.ScriptCall(ctx, "addFines", map[string]any{"fines": fines}, &added); err != nil {
			if s.once(ctx, "daily_fail:"+day.Format("2006-01-02"), 3*time.Hour) {
				s.sysNote("Проверка отчётов за " + res.Day + ": штрафы не записались в таблицу (" + err.Error() + "), повтор через 10 минут")
			}
			return nil, err
		}
	}
	out.Fined = added.Added
	chat := map[string]int64{}
	snap, _ := s.repo.Club().Load(ctx)
	if snap != nil {
		for _, r := range snap.Residents {
			if r.TgID != 0 && !r.Former {
				chat[club.NormName(r.Name)] = r.TgID
			}
		}
	}
	tpl, _ := s.repo.Setting(ctx, "fine_reminder")
	if strings.TrimSpace(tpl) == "" {
		tpl = "⚠️ {имя}, штраф за несдачу отчёта!\n\nСумма: 10 000 тг\nДата: {дата}\n\nОплатить: {kaspi}"
	}
	for _, n := range out.Fined {
		id := chat[club.NormName(n)]
		if id == 0 && s.WhatsAppPhone(ctx, n) == "" {
			continue
		}
		txt := fill(tpl, map[string]string{"имя": strings.Fields(n)[0], "дата": res.Day, "kaspi": KaspiLink})
		// R38c: WhatsApp for those who chose it (outreach.go)
		if err := s.SendResident(ctx, "fine", res.Day, n, id, txt, appButton("fines")); err != nil {
			log.Printf("bot daily: note to %s: %v", n, err)
		}
	}
	out.Summary = DailySummary(res, out.Fined)
	for _, id := range s.admins {
		_ = s.SendMessageKB(ctx, id, out.Summary, appButton("reports"))
	}
	_ = s.repo.SetMeta(ctx, key, time.Now().UTC().Format(time.RFC3339))
	log.Printf("bot daily check %s: fined %v", res.Day, out.Fined)
	return out, nil
}
