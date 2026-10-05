package bot

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
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

// The server's daily check runs at 10:00 for yesterday, names who did not
// submit, and never mentions exceptions or residents without a Chat ID.
func TestDailyCheckAtTenNoChatIDNoise(t *testing.T) {
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
	// The script: takes the relayed updates and the fines.
	var fines []string
	script := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		var b struct {
			BsAction string `json:"bsAction"`
			Data     string `json:"data"`
		}
		_ = json.Unmarshal(raw, &b)
		if b.BsAction == "srv" {
			var d struct {
				Op    string    `json:"op"`
				Fines []FineRow `json:"fines"`
			}
			_ = json.Unmarshal([]byte(b.Data), &d)
			var added []string
			for _, f := range d.Fines {
				added = append(added, f.Name)
			}
			mu.Lock()
			fines = append(fines, added...)
			mu.Unlock()
			out, _ := json.Marshal(map[string]any{"ok": true, "added": added})
			_, _ = w.Write(out)
			return
		}
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer script.Close()

	s := New(pg.NewBotRepo(db), Options{Token: "1:x", APIBase: tg.URL, Admins: []int64{111}, Notify: []int64{453800951}})
	_ = s.SetRelayURL(ctx, script.URL+"/exec")
	if _, err := s.SetFeatures(ctx, []string{FeatureDailyCheck}); err != nil {
		t.Fatal(err)
	}

	t0 := club.Today()
	day := t0.AddDate(0, 0, -1)
	day = time.Date(day.Year(), day.Month(), day.Day(), 0, 0, 0, 0, club.Almaty)
	ex := func(q string, a ...any) {
		if _, err := db.Pool.Exec(ctx, q, a...); err != nil {
			t.Fatal(q, err)
		}
	}
	ex(`INSERT INTO club_residents (name, tg_id) VALUES ('Альтаир', 1001), ('Асет', 1002), ('Марат', 1004)`)
	ex(`INSERT INTO club_residents (name, tg_id, exception) VALUES ('Исключённый', 1005, true)`)
	ex(`INSERT INTO club_residents (name) VALUES ('Амирхан')`) // no Chat ID, no report
	ex(`INSERT INTO club_residents (name, tg_id, admin) VALUES ('Рустам', 453800951, true)`)

	long := strings.Repeat("Сделал три встречи. ", 6)
	msg := func(id, from int64, first string, at time.Time) string {
		return fmt.Sprintf(`{"update_id":%d,"message":{"message_id":%d,"date":%d,"chat":{"id":-1002494126345,"type":"supergroup"},"message_thread_id":9,"from":{"id":%d,"first_name":%q},"text":%q}}`,
			id, id, at.Unix(), from, first, long)
	}
	for _, b := range []string{
		msg(1, 1001, "Альтаир", day.Add(21*time.Hour)),
		msg(2, 1004, "Марат", day.Add(23*time.Hour+50*time.Minute)), // before the 23:59 deadline
	} {
		if _, err := s.Receive(ctx, []byte(b)); err != nil {
			t.Fatal(err)
		}
	}
	s.shadowWG.Wait()
	ex(`UPDATE bot_updates SET received_at = $1 WHERE update_id = 1`, day.Add(-2*time.Hour))

	at := func(h, m int) time.Time { return time.Date(t0.Year(), t0.Month(), t0.Day(), h, m, 0, 0, club.Almaty) }
	if out, _ := s.maybeDailyCheck(ctx, at(9, 59)); out != nil {
		t.Fatal("ran before 10:00")
	}
	out, err := s.maybeDailyCheck(ctx, at(10, 1))
	if err != nil || out == nil {
		t.Fatalf("no check at 10:01: %v", err)
	}
	if out.Day != day.Format("02.01.2006") || out.Stopped {
		t.Fatalf("%+v", out)
	}
	if strings.Join(out.Fined, ",") != "Асет" || strings.Join(fines, ",") != "Асет" {
		t.Fatalf("fined %v / %v", out.Fined, fines)
	}
	t.Logf("\n%s", out.Summary)
	for _, want := range []string{"Сдали отчёт: 2 из 3", "• Асет"} {
		if !strings.Contains(out.Summary, want) {
			t.Fatalf("%q not in\n%s", want, out.Summary)
		}
	}
	for _, bad := range []string{"Амирхан", "Исключённый", "Chat ID", "Рустам"} {
		if strings.Contains(out.Summary, bad) {
			t.Fatalf("%q in\n%s", bad, out.Summary)
		}
	}
	// One summary per admin, a note to the fined resident, nothing about Chat IDs.
	mu.Lock()
	summaries, notes := 0, 0
	for _, p := range sent {
		txt, _ := p["text"].(string)
		if strings.Contains(txt, "Chat ID") || strings.Contains(txt, "Амирхан") {
			t.Fatalf("noise: %s", txt)
		}
		switch fmt.Sprint(p["chat_id"]) {
		case "111":
			summaries++
		case "1002":
			notes++
		}
	}
	mu.Unlock()
	if summaries != 1 || notes != 1 {
		t.Fatalf("summaries %d notes %d: %v", summaries, notes, sent)
	}
	// Once a day.
	if out, _ := s.maybeDailyCheck(ctx, at(11, 0)); out != nil {
		t.Fatal("ran twice")
	}
}
