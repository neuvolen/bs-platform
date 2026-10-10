package bot

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/bnursik/business_surgery_backend/internal/club"
)

func TestStreakReady(t *testing.T) {
	now := time.Date(2026, 10, 6, 17, 0, 0, 0, club.Almaty)
	d := func(day int) time.Time { return time.Date(2026, 10, day, 0, 0, 0, 0, club.Almaty) }
	days := []time.Time{d(5), d(4), d(3), d(2), d(1)}
	all := []bool{true, true, true, true, true}
	if !StreakReady(days, all, now, 5) {
		t.Fatal("5 matched days in a row")
	}
	if StreakReady(days[:4], all[:4], now, 5) {
		t.Fatal("4 days are not enough")
	}
	if StreakReady(days, []bool{true, true, false, true, true}, now, 5) {
		t.Fatal("a mismatch inside")
	}
	if StreakReady([]time.Time{d(5), d(4), d(2), d(1), time.Date(2026, 9, 30, 0, 0, 0, 0, club.Almaty)}, all, now, 5) {
		t.Fatal("a gap inside")
	}
	if StreakReady(days, all, now.AddDate(0, 0, 3), 5) {
		t.Fatal("stale streak")
	}
}

func TestFeatureChangeText(t *testing.T) {
	m := featureChangeText([]string{FeatureReportFeedback}, []string{FeatureReportFeedback, FeatureDailyCheck})
	if !strings.Contains(m, "Теперь делает сервер:\n• ночная проверка") || strings.Contains(m, "Снова делает таблица") {
		t.Fatal(m)
	}
	if featureChangeText([]string{"a"}, []string{"a"}) != "" {
		t.Fatal("no change, no note")
	}
}

func TestDueReminders(t *testing.T) {
	res := []club.Resident{{Name: "Альтаир Тестов", TgID: 1}, {Name: "Бывший", TgID: 2, Former: true}, {Name: "Без Айди"}}
	day := func(add int) time.Time {
		return time.Date(2026, 10, 1+add, 0, 0, 0, 0, club.Almaty)
	}
	meet := []club.Meeting{
		{Resident: "Альтаир Тестов", Date: day(3), Time: "15:00", Link: "https://meet.google.com/x"},
		{Resident: "альтаир тестов", Date: day(1), Time: "10:30", Place: "Достык 44"},
		{Resident: "Альтаир Тестов", Date: day(0), Time: "11:00"},
		{Resident: "Альтаир Тестов", Date: day(0), Time: "18:00"}, // later today: not yet
		{Resident: "Альтаир Тестов", Date: day(3), Time: "16:00", Sent3d: true},
		{Resident: "Бывший", Date: day(1), Time: "10:00"},
		{Resident: "Без Айди", Date: day(1), Time: "10:00"},
		{Resident: "Альтаир Тестов", Date: day(1), Time: "12:00", Done: true},
		{Resident: "ВСЕ РЕЗИДЕНТЫ ОФФЛАЙН", Date: day(1), Time: "12:00"},
	}
	now := time.Date(2026, 10, 1, 10, 0, 0, 0, club.Almaty)
	got := DueReminders(meet, res, map[string]string{"schedule_1day": "{имя}: завтра {дата}, {адрес}"}, now)
	var kinds []string
	for _, r := range got {
		kinds = append(kinds, r.Kind)
	}
	if strings.Join(kinds, ",") != "3d,1d,1h" {
		t.Fatalf("%v", got)
	}
	if !strings.Contains(got[0].Text, "через 3 дня") || !strings.Contains(got[0].Text, "/call/") { // R75 call: ссылка платформы вместо Meet
		t.Fatal(got[0].Text)
	}
	if !strings.HasPrefix(got[1].Text, "Альтаир: завтра 02.10.2026 10:30, Достык 44") || !got[1].Wheel {
		t.Fatal(got[1].Text)
	}
	if !strings.Contains(got[2].Text, "встреча через час") {
		t.Fatal(got[2].Text)
	}
	// Before 09:00 only the hour reminder can go.
	early := DueReminders(meet, res, nil, time.Date(2026, 10, 1, 8, 0, 0, 0, club.Almaty))
	if len(early) != 0 {
		t.Fatalf("early %v", early)
	}
}

func TestDailySummaryWording(t *testing.T) {
	r := DayResult{Day: "29.09.2026", Active: 10, WouldFine: []string{"Асет"}, NoChatID: []string{"Даниил"}, Meeting: []string{"Даулет"}}
	m := DailySummary(r, []string{"Асет"})
	for _, want := range []string{"📋 Проверка отчётов за 29.09.2026", "Сдали отчёт: 8 из 9", "• Асет", "штраф 10 000 тг", "Даулет"} {
		if !strings.Contains(m, want) {
			t.Fatalf("%q not in\n%s", want, m)
		}
	}
	// A resident without a Chat ID is not named, and there is no Chat ID noise.
	for _, bad := range []string{"Даниил", "Chat ID", "Ночная", "\u2014"} {
		if strings.Contains(m, bad) {
			t.Fatalf("%q in\n%s", bad, m)
		}
	}
	_ = context.Background()
}
