package bot

import (
	"encoding/json"
	"os"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/bnursik/business_surgery_backend/internal/club"
)

func gm(text, thread string, sent time.Time, from int64) *GroupMessage {
	return &GroupMessage{UpdateID: 1, ChatID: DefaultGroupID, Sent: sent, Thread: thread, FromID: from, FirstName: "Альтаир", Text: text}
}

var long = strings.Repeat("Сделал три встречи. ", 6) // 120 символов

// A report in the wrong topic, and a long chat message that is not a report.
var wrongReport = "Отчёт за день:\n1. Сделал три встречи с клиентами\n2. Закрыл сделку\nНа завтра: звонки по базе и план продаж на неделю"
var chatLong = strings.Repeat("Коллеги, кто был на конференции, поделитесь впечатлениями. ", 3)

func lookupIDs(ids map[int64]string) ResidentLookup {
	return func(tg int64, full string) (string, bool) {
		n, ok := ids[tg]
		return n, ok
	}
}

func TestDecideReportRules(t *testing.T) {
	res := lookupIDs(map[int64]string{7: "Альтаир Тестов"})
	at := func(h, m int) time.Time { return time.Date(2026, 9, 28, h, m, 0, 0, club.Almaty) }
	cases := []struct {
		name    string
		m       *GroupMessage
		verdict string
		record  bool
		day     string
		late    bool
	}{
		{"обычный отчёт вечером", gm(long, "9", at(21, 40), 7), VerdictReport, true, "28.09", false},
		{"в 23:59 ещё за этот день", gm(long, "9", at(23, 59), 7), VerdictReport, true, "28.09", false},
		{"в 00:30 поздний", gm(long, "9", at(0, 30), 7), VerdictReport, true, "28.09", true},
		{"в 06:00 уже не поздний, за новый день", gm(long, "9", at(6, 0), 7), VerdictReport, true, "28.09", false},
		{"99 символов — короткий", gm(strings.Repeat("я", 99), "9", at(20, 0), 7), VerdictShort, true, "", false},
		{"ровно 100 — отчёт", gm(strings.Repeat("я", 100), "9", at(20, 0), 7), VerdictReport, true, "28.09", false},
		{"другой топик, похоже на отчёт", gm(wrongReport, "2", at(20, 0), 7), VerdictWrongTopic, true, "", false},
		{"другой топик, длинное общение, не отчёт", gm(chatLong, "2", at(20, 0), 7), "", false, "", false},
		{"другой топик, короткий — не пишем", gm("спасибо", "2", at(20, 0), 7), "", false, "", false},
		{"команда", gm("/help", "9", at(20, 0), 7), "", false, "", false},
		{"не резидент", gm(long, "9", at(20, 0), 99), VerdictNotResident, true, "", false},
		{"не резидент, коротко — не пишем", gm("ок", "9", at(20, 0), 99), "", false, "", false},
	}
	for _, c := range cases {
		d := Decide(c.m, "9", res)
		if d.Record != c.record || d.Verdict != c.verdict || d.Late != c.late {
			t.Errorf("%s: %+v", c.name, d)
		}
		if c.day != "" && d.Day.Format("02.01") != c.day {
			t.Errorf("%s: day %s", c.name, d.Day.Format("02.01"))
		}
	}
	// Эмодзи считаются как в JavaScript: 🔥 — два символа
	if JSLen("🔥") != 2 || JSLen("отчёт") != 5 {
		t.Fatal("JSLen")
	}
	// 50 эмодзи = 100 по правилам скрипта
	if d := Decide(gm(strings.Repeat("🔥", 50), "9", at(20, 0), 7), "9", res); d.Verdict != VerdictReport {
		t.Fatalf("emoji length: %+v", d)
	}
	// Кружочек — никогда не отчёт
	v := gm(long, "9", at(20, 0), 7)
	v.Media = true
	if d := Decide(v, "9", res); d.Record {
		t.Fatal("video note recorded")
	}
}

func TestReadGroupMessage(t *testing.T) {
	b := []byte(`{"update_id":5,"message":{"message_id":9,"date":1790610000,"chat":{"id":-1002494126345,"type":"supergroup"},"message_thread_id":9,"from":{"id":7,"first_name":"А","last_name":"Б","username":"ab"},"text":"  текст  "}}`)
	m, ok := ReadGroupMessage(b)
	if !ok || m.Thread != "9" || m.Text != "текст" || m.FullName() != "А Б" || m.Sent.Location() != club.Almaty {
		t.Fatalf("%+v", m)
	}
	for _, x := range []string{
		`{"update_id":5,"message":{"chat":{"id":5,"type":"private"},"from":{"id":5},"text":"hi"}}`,
		`{"update_id":5,"edited_message":{"chat":{"id":-1,"type":"supergroup"},"from":{"id":5},"text":"hi"}}`,
		`{"update_id":5,"callback_query":{}}`,
	} {
		if _, ok := ReadGroupMessage([]byte(x)); ok {
			t.Fatalf("read %s", x)
		}
	}
}

func TestMatchByNameAndMeetings(t *testing.T) {
	c := []string{"Даниил Раскрутов", "Асет"}
	if n, ok := MatchByName("даниил  раскрутов", c); !ok || n != "Даниил Раскрутов" {
		t.Fatal("full name")
	}
	if n, ok := MatchByName("Асет Кабдулов", c); !ok || n != "Асет" {
		t.Fatal("first name")
	}
	if _, ok := MatchByName("Ас", c); ok {
		t.Fatal("prefix matched")
	}
	if _, ok := MatchByName("Даниил Другой", []string{"Даниил А", "Даниил Б"}); ok {
		t.Fatal("ambiguous matched")
	}
	if !HadMeeting("Даулет", []string{"Даулёт Сайты"}) || !HadMeeting("Даулет Сайты", []string{"даулет"}) || HadMeeting("Асет", []string{"Альтаир"}) {
		t.Fatal("meetings")
	}
}

func TestCheckDay(t *testing.T) {
	day := time.Date(2026, 9, 28, 0, 0, 0, 0, club.Almaty)
	res := []club.Resident{
		{Name: "Альтаир", TgID: 1}, {Name: "Асет", TgID: 2}, {Name: "Даулет Сайты", TgID: 3},
		{Name: "Без ID"}, {Name: "Исключение", TgID: 5, Exception: true}, {Name: "Бывший", TgID: 6, Former: true},
		{Name: "Админ", TgID: 7, Admin: true}, {Name: "Марат", TgID: 8},
	}
	in := DayInput{Day: day, Residents: res, FullCoverage: true,
		Server:     []DayReport{{TgID: 1, Name: "Альтаир"}, {TgID: 0, Name: "марат"}},
		Sheet:      []DayReport{{TgID: 1, Name: "Альтаир"}, {TgID: 8, Name: "Марат"}},
		Met:        []string{"Даулёт"},
		SheetFined: []string{"Асет"},
	}
	r := CheckDay(in)
	if r.Active != 5 || r.ServerCount != 2 || r.SheetCount != 2 || !r.Match {
		t.Fatalf("%+v", r)
	}
	if strings.Join(r.WouldFine, ",") != "Асет" || strings.Join(r.NoChatID, ",") != "Без ID" || strings.Join(r.Meeting, ",") != "Даулет Сайты" {
		t.Fatalf("%+v", r)
	}
	if !strings.Contains(r.Message(), "✅ Совпадает") {
		t.Fatal(r.Message())
	}
	// Расхождение
	in.SheetFined = []string{"Асет", "Марат"}
	in.Sheet = in.Sheet[:1]
	r = CheckDay(in)
	if r.Match || strings.Join(r.OnlyServer, ",") != "Марат" || strings.Join(r.FineOnlySheet, ",") != "Марат" {
		t.Fatalf("%+v", r)
	}
	if m := r.Message(); !strings.Contains(m, "отчёт засчитал только сервер: Марат") || !strings.Contains(m, "штраф выставила только таблица: Марат") {
		t.Fatal(m)
	}
	// Стоп-кран
	in.Server = nil
	r = CheckDay(in)
	if !r.StopCrane || len(r.WouldFine) != 0 || !strings.Contains(r.Message(), "остановил бы") {
		t.Fatalf("%+v", r)
	}
}

// TestCheckDayOnRealHistory replays the daily fine rules on the real sheet:
// for each recent day, the reports the sheet logged go in, and the fines the
// server would set are compared with the fines the sheet set that day.
func TestCheckDayOnRealHistory(t *testing.T) {
	path := os.Getenv("BS_SHEET_JSON")
	if path == "" {
		t.Skip("BS_SHEET_JSON not set")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var s club.Sheets
	if err := json.Unmarshal(raw, &s); err != nil {
		t.Fatal(err)
	}
	snap, _, err := club.Parse(s)
	if err != nil {
		t.Fatal(err)
	}
	byDay := map[string][]DayReport{}
	for _, e := range snap.Reports {
		if e.Late {
			continue
		}
		k := e.At.In(club.Almaty).Format("2006-01-02")
		byDay[k] = append(byDay[k], DayReport{TgID: e.TgUserID, Name: e.Name})
	}
	met := map[string][]string{}
	for _, m := range snap.MeetingLog {
		met[m.Date.Format("2006-01-02")] = append(met[m.Date.Format("2006-01-02")], m.Resident)
	}
	for _, m := range snap.Meetings {
		met[m.Date.Format("2006-01-02")] = append(met[m.Date.Format("2006-01-02")], m.Resident)
	}
	fined := map[string][]string{}
	for _, f := range snap.Fines {
		if f.Type == "Не сдан отчёт" {
			fined[f.Date.Format("2006-01-02")] = append(fined[f.Date.Format("2006-01-02")], f.Name)
		}
	}
	var days []string
	for k := range byDay {
		days = append(days, k)
	}
	sort.Strings(days)
	if len(days) > 14 {
		days = days[len(days)-14:]
	}
	match := 0
	for _, k := range days {
		d, _ := time.ParseInLocation("2006-01-02", k, club.Almaty)
		r := CheckDay(DayInput{Day: d, Residents: snap.Residents, Server: byDay[k], Sheet: byDay[k], Met: met[k], SheetFined: fined[k], FullCoverage: true})
		fine := len(r.FineOnlyServer) == 0 && len(r.FineOnlySheet) == 0
		if fine {
			match++
		}
		t.Logf("%s: отчётов %d/%d, штраф сервер %v, таблица %v, лишний у сервера %v, лишний у таблицы %v",
			r.Day, r.ServerCount, r.Active, r.WouldFine, r.SheetFined, r.FineOnlyServer, r.FineOnlySheet)
	}
	t.Logf("совпало дней: %d из %d", match, len(days))
}
