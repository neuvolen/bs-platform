package bot

import (
	"context"
	"log"
	"time"

	"github.com/bnursik/business_surgery_backend/internal/club"
	pg "github.com/bnursik/business_surgery_backend/internal/repository/pg"
)

// The daily comparison runs for yesterday once the sheet's own check (10:00,
// with the hourly dispatcher, so by 11:00) has run and its result has come
// over with an hourly import.
const (
	shadowFromHour = 11
	shadowGiveUpAt = 14 // after this, compare even without a fresh import
	sheetCheckHour = 10
	sheetCheckMin  = 5
)

// shadowUpdate judges one group message by the report rules and keeps the verdict.
func (s *Service) shadowUpdate(ctx context.Context, body []byte) {
	m, ok := ReadGroupMessage(body)
	if !ok {
		return
	}
	d := Decide(m, s.topic, func(tg int64, full string) (string, bool) {
		if name, ok, err := s.repo.ResidentByTgID(ctx, tg); err == nil && ok {
			return name, true
		}
		cands, err := s.repo.ResidentsWithoutTgID(ctx)
		if err != nil {
			return "", false
		}
		return MatchByName(full, cands)
	})
	if s.Has(FeatureReportFeedback) {
		s.feedback(ctx, m, d)
	}
	if !d.Record {
		return
	}
	text := m.Text
	if r := []rune(text); len(r) > 200 {
		text = string(r[:200]) + "..."
	}
	rep := pg.ShadowReport{
		UpdateID: m.UpdateID, MessageID: m.MessageID, At: m.Sent, Late: d.Late, TgUserID: m.FromID,
		Resident: d.Resident, Username: m.Username, Sender: m.FullName(), Thread: m.Thread,
		TextLen: d.Len, Text: text, Verdict: d.Verdict,
	}
	if !d.Day.IsZero() {
		day := d.Day
		rep.Day = &day
	} else {
		a := m.Sent.In(club.Almaty)
		day := time.Date(a.Year(), a.Month(), a.Day(), 0, 0, 0, 0, club.Almaty)
		rep.Day = &day
	}
	if err := s.repo.SaveShadowReport(ctx, rep); err != nil {
		log.Printf("bot shadow: save report %d: %v", m.UpdateID, err)
	}
}

// maybeDailyShadow compares yesterday once a day and tells the team.
func (s *Service) maybeDailyShadow(ctx context.Context, now time.Time) (*DayResult, error) {
	if s.RelayURL() == "" {
		return nil, nil // the bot does not come through the server
	}
	a := now.In(club.Almaty)
	if a.Hour() < shadowFromHour {
		return nil, nil
	}
	today := time.Date(a.Year(), a.Month(), a.Day(), 0, 0, 0, 0, club.Almaty)
	day := today.AddDate(0, 0, -1)
	if done, err := s.repo.ShadowDayDone(ctx, day); err != nil || done {
		return nil, err
	}
	first, err := s.repo.FirstUpdateAt(ctx)
	if err != nil || first == nil {
		return nil, err
	}
	if first.After(today) {
		return nil, nil // the server started today: nothing to compare yet
	}
	sheetDone := time.Date(a.Year(), a.Month(), a.Day(), sheetCheckHour, sheetCheckMin, 0, 0, club.Almaty)
	if imp, err := s.repo.LastImportAt(ctx); err != nil {
		return nil, err
	} else if (imp == nil || imp.Before(sheetDone)) && a.Hour() < shadowGiveUpAt {
		return nil, nil // wait for the sheet's fines to come over
	}
	res, err := s.CompareDay(ctx, day, now)
	if err != nil {
		return nil, err
	}
	msg := res.Message()
	fresh, err := s.repo.SaveShadowDay(ctx, day, res.Match, res, msg)
	if err != nil || !fresh {
		return &res, err
	}
	if res.Match {
		return &res, nil // a matching day needs no note: the team hears only about differences
	}
	for _, id := range s.notify {
		if e := s.SendMessage(ctx, id, msg); e != nil {
			log.Printf("bot shadow: notify %d: %v", id, e)
		}
	}
	return &res, nil
}

// CompareDay runs the server's daily check for day against the sheet's.
func (s *Service) CompareDay(ctx context.Context, day, now time.Time) (DayResult, error) {
	snap, err := s.repo.Club().Load(ctx)
	if err != nil {
		return DayResult{}, err
	}
	in := DayInput{Day: day, Residents: snap.Residents}
	conv := func(v []pg.DayReportRow) []DayReport {
		out := make([]DayReport, 0, len(v))
		for _, x := range v {
			out = append(out, DayReport{TgID: x.TgID, Name: x.Name})
		}
		return out
	}
	srv, err := s.repo.ServerReports(ctx, day)
	if err != nil {
		return DayResult{}, err
	}
	sh, err := s.repo.SheetReports(ctx, day, club.Almaty)
	if err != nil {
		return DayResult{}, err
	}
	in.Server, in.Sheet = conv(srv), conv(sh)
	if in.Met, err = s.repo.MetOn(ctx, day); err != nil {
		return DayResult{}, err
	}
	if in.SheetFined, err = s.repo.SheetReportFines(ctx, day); err != nil {
		return DayResult{}, err
	}
	first, err := s.repo.FirstUpdateAt(ctx)
	if err != nil {
		return DayResult{}, err
	}
	in.FullCoverage = first != nil && !first.After(day)
	if st, err := s.repo.Stats(ctx); err == nil && st.LastReceivedAt != nil {
		in.Silent = now.Sub(*st.LastReceivedAt) > 24*time.Hour
	}
	return CheckDay(in), nil
}
