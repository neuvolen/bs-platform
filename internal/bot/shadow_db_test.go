package bot

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/bnursik/business_surgery_backend/internal/club"
	pg "github.com/bnursik/business_surgery_backend/internal/repository/pg"
	"github.com/bnursik/business_surgery_backend/migrations"
)

// The whole "listen, don't fine" day on a real database: group messages come
// in through Receive, the sheet's data comes in as an import would leave it,
// and after 11:00 the server compares and tells the owner once.
func TestShadowDayEndToEnd(t *testing.T) {
	dsn := os.Getenv("BS_TEST_DSN")
	if dsn == "" {
		t.Skip("BS_TEST_DSN not set")
	}
	ctx := context.Background()
	db, err := pg.NewDB(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Pool.Close()
	if err := pg.Migrate(ctx, db, migrations.FS); err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{`TRUNCATE bot_updates, bot_meta, bot_reports, bot_shadow_days`,
		`TRUNCATE club_residents, club_reports, club_fines, club_meetings, club_meeting_log, club_imports RESTART IDENTITY`} {
		if _, err := db.Pool.Exec(ctx, q); err != nil {
			t.Fatal(err)
		}
	}
	var mu sync.Mutex
	var sent []map[string]any
	tg := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var p map[string]any
		_ = json.NewDecoder(r.Body).Decode(&p)
		mu.Lock()
		sent = append(sent, p)
		mu.Unlock()
		_, _ = w.Write([]byte(`{"ok":true,"result":{}}`))
	}))
	defer tg.Close()
	s := New(pg.NewBotRepo(db), Options{Token: "1:x", APIBase: tg.URL, Admins: []int64{111}, Notify: []int64{453800951}})
	_ = s.SetRelayURL(ctx, "http://127.0.0.1:1/exec") // the bot comes through the server

	t0 := club.Today()
	now := time.Date(t0.Year(), t0.Month(), t0.Day(), 11, 5, 0, 0, club.Almaty)
	day := now.AddDate(0, 0, -1)
	day = time.Date(day.Year(), day.Month(), day.Day(), 0, 0, 0, 0, club.Almaty)
	dd := func(d time.Time) string { return d.Format("02.01") }
	ex := func(q string, a ...any) {
		if _, err := db.Pool.Exec(ctx, q, a...); err != nil {
			t.Fatal(q, err)
		}
	}
	// Residents as the hourly import leaves them.
	ex(`INSERT INTO club_residents (name, tg_id) VALUES ('Альтаир', 1001), ('Асет', 1002), ('Даулет Сайты', 1003), ('Марат', 1004)`)
	ex(`INSERT INTO club_residents (name, tg_id, exception) VALUES ('Исключение', 1005, true)`)
	ex(`INSERT INTO club_residents (name) VALUES ('Даниил Раскрутов')`)
	ex(`INSERT INTO club_residents (name, tg_id, admin) VALUES ('Рустам', 453800951, true)`)

	msg := func(id, from int64, first string, thread int, text string, at time.Time) string {
		return fmt.Sprintf(`{"update_id":%d,"message":{"message_id":%d,"date":%d,"chat":{"id":-1002494126345,"type":"supergroup"},"message_thread_id":%d,"from":{"id":%d,"first_name":%q},"text":%q}}`,
			id, id, at.Unix(), thread, from, first, text)
	}
	long := strings.Repeat("Сделал три встречи. ", 6)
	feed := []string{
		msg(1, 1001, "Альтаир", 9, long, day.Add(21*time.Hour)),              // отчёт
		msg(2, 1002, "Асет", 9, "Сделал обзвон", day.Add(22*time.Hour)),      // короткий
		msg(3, 1004, "Марат", 2, wrongReport, day.Add(20*time.Hour)),         // не тот топик
		msg(4, 1004, "Марат", 9, long, day.Add(24*time.Hour+30*time.Minute)), // после полуночи
		msg(5, 777, "Даниил Раскрутов", 9, long, day.Add(19*time.Hour)),      // без Chat ID, по имени
		msg(6, 555, "Гость", 9, long, day.Add(18*time.Hour)),                 // не резидент
		msg(7, 1001, "Альтаир", 9, "/help", day.Add(18*time.Hour)),           // команда
	}
	for _, b := range feed {
		if _, err := s.Receive(ctx, []byte(b)); err != nil {
			t.Fatal(err)
		}
	}
	s.shadowWG.Wait() // the server bot reads in the background
	rows, _ := db.Pool.Query(ctx, `SELECT update_id, verdict, resident, late, to_char(day,'DD.MM') FROM bot_reports ORDER BY update_id`)
	got := []string{}
	for rows.Next() {
		var id int64
		var v, r, d string
		var late bool
		_ = rows.Scan(&id, &v, &r, &late, &d)
		got = append(got, fmt.Sprintf("%d:%s:%s:%v:%s", id, v, r, late, d))
	}
	rows.Close()
	d0, d1 := dd(day), dd(day.AddDate(0, 0, 1))
	want := "1:report:Альтаир:false:" + d0 + " 2:short:Асет:false:" + d0 + " 3:wrong_topic:Марат:false:" + d0 + " 4:report:Марат:true:" + d1 + " 5:report:Даниил Раскрутов:false:" + d0 + " 6:not_resident::false:" + d0
	if strings.Join(got, " ") != want {
		t.Fatalf("verdicts\n got %s\nwant %s", strings.Join(got, " "), want)
	}

	// The server started before that day.
	ex(`UPDATE bot_updates SET received_at = $1 WHERE update_id = 1`, day.Add(-2*time.Hour))
	// The sheet: its log and the fines its 10:00 check set.
	ex(`INSERT INTO club_reports (at, name, tg_user_id, late) VALUES ($1, 'Альтаир', 1001, false), ($2, 'Даниил Раскрутов', NULL, false)`,
		day.Add(21*time.Hour), day.Add(19*time.Hour))
	ex(`INSERT INTO club_meeting_log (date, resident) VALUES ($1, 'Даулёт')`, day.Format("2006-01-02"))
	ex(`INSERT INTO club_fines (resident, type, amount, date) VALUES ('Асет', 'Не сдан отчёт', 10000, $1), ('Марат', 'Не сдан отчёт', 10000, $1), ('Асет', 'Опоздание', 5000, $1)`, day.Format("2006-01-02"))

	// Before 11:00 nothing happens.
	if r, _ := s.maybeDailyShadow(ctx, now.Add(-2*time.Hour)); r != nil {
		t.Fatal("ran before 11:00")
	}
	// No import since the sheet's check: wait.
	ex(`INSERT INTO club_imports (at, by, dry_run, raw, report) VALUES ($1, 'sheet', false, '{}', '{}')`, now.Add(-3*time.Hour))
	if r, _ := s.maybeDailyShadow(ctx, now); r != nil {
		t.Fatal("ran without the sheet's fines")
	}
	ex(`INSERT INTO club_imports (at, by, dry_run, raw, report) VALUES ($1, 'sheet', false, '{}', '{}')`, now.Add(-20*time.Minute))
	r, err := s.maybeDailyShadow(ctx, now)
	if err != nil || r == nil {
		t.Fatalf("no comparison: %v", err)
	}
	t.Logf("\n%s", r.Message())
	if !r.Match || strings.Join(r.WouldFine, ",") != "Асет, Марат" && strings.Join(r.WouldFine, ",") != "Асет,Марат" {
		t.Fatalf("%+v", r)
	}
	if strings.Join(r.Meeting, ",") != "Даулет Сайты" || r.Active != 5 || r.ServerCount != 2 {
		t.Fatalf("%+v", r)
	}
	mu.Lock()
	if len(sent) != 0 { // a matching day is not reported to the team
		t.Fatalf("sent %v", sent)
	}
	mu.Unlock()
	// Once a day only.
	if r, _ := s.maybeDailyShadow(ctx, now.Add(time.Hour)); r != nil || len(sent) != 0 {
		t.Fatal("compared twice")
	}
}
