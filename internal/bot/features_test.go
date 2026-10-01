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

type tgCall struct {
	Method string
	P      map[string]any
}

type featEnv struct {
	s    *Service
	db   *pg.DB
	mu   sync.Mutex
	got  []tgCall
	fail map[string]string // method -> error description
}

func (e *featEnv) calls(method string) []tgCall {
	e.mu.Lock()
	defer e.mu.Unlock()
	var out []tgCall
	for _, c := range e.got {
		if c.Method == method {
			out = append(out, c)
		}
	}
	return out
}

func (e *featEnv) reset() { e.mu.Lock(); e.got = nil; e.mu.Unlock() }

func newFeatEnv(t *testing.T) *featEnv {
	dsn := os.Getenv("BS_TEST_DSN")
	if dsn == "" {
		t.Skip("BS_TEST_DSN not set")
	}
	ctx := context.Background()
	db, err := pg.NewDB(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(db.Pool.Close)
	if err := pg.Migrate(ctx, db, migrations.FS); err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{`TRUNCATE bot_updates, bot_meta, bot_reports, bot_shadow_days`,
		`TRUNCATE club_residents, club_reports, club_fines, club_meetings, club_meeting_log, club_imports, club_settings RESTART IDENTITY`,
		`INSERT INTO club_residents (name, tg_id) VALUES ('Альтаир Тестов', 1001), ('Асет', 1002), ('Марат', 1004)`,
		`INSERT INTO club_residents (name, tg_id, exception) VALUES ('Исключение', 1005, true)`,
		`INSERT INTO club_residents (name, tg_id, former) VALUES ('Бывший', 1006, true)`,
		`INSERT INTO club_residents (name) VALUES ('Без Айди')`,
		`INSERT INTO club_residents (name, tg_id, admin) VALUES ('Рустам', 453800951, true)`,
		`INSERT INTO club_settings (key, value) VALUES ('report_reminder', '⏰ {имя}, отчёт до 23:59!')`,
	} {
		if _, err := db.Pool.Exec(ctx, q); err != nil {
			t.Fatal(q, err)
		}
	}
	e := &featEnv{db: db, fail: map[string]string{}}
	tg := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		m := r.URL.Path[strings.LastIndex(r.URL.Path, "/")+1:]
		var p map[string]any
		_ = json.NewDecoder(r.Body).Decode(&p)
		e.mu.Lock()
		e.got = append(e.got, tgCall{m, p})
		desc := e.fail[m]
		e.mu.Unlock()
		if desc != "" {
			_, _ = w.Write([]byte(`{"ok":false,"description":"` + desc + `"}`))
			return
		}
		_, _ = w.Write([]byte(`{"ok":true,"result":true}`))
	}))
	t.Cleanup(tg.Close)
	e.s = New(pg.NewBotRepo(db), Options{Token: "1:x", APIBase: tg.URL, Admins: []int64{453800951}})
	_ = e.s.SetRelayURL(ctx, "http://127.0.0.1:1/exec")
	return e
}

var fid int64 = 5000

func (e *featEnv) group(t *testing.T, from int64, thread int, text string, at time.Time, extra string) {
	fid++
	b := fmt.Sprintf(`{"update_id":%d,"message":{"message_id":%d,"date":%d,"chat":{"id":-1002494126345,"type":"supergroup"},"message_thread_id":%d,"from":{"id":%d,"first_name":"Имя"},"text":%q%s}}`,
		fid, fid, at.Unix(), thread, from, text, extra)
	if _, err := e.s.Receive(context.Background(), []byte(b)); err != nil {
		t.Fatal(err)
	}
	time.Sleep(150 * time.Millisecond)
}

func TestReportFeedback(t *testing.T) {
	e := newFeatEnv(t)
	ctx := context.Background()
	long := strings.Repeat("Сделал три встречи. ", 6)
	evening := time.Date(club.Today().Year(), club.Today().Month(), club.Today().Day(), 21, 0, 0, 0, club.Almaty)

	// Off: the server says nothing.
	e.group(t, 1001, 9, long, evening, "")
	if len(e.got) != 0 {
		t.Fatalf("feature off, calls %v", e.got)
	}
	if _, err := e.s.SetFeatures(ctx, []string{"nope"}); err == nil {
		t.Fatal("unknown feature accepted")
	}
	if _, err := e.s.SetFeatures(ctx, []string{FeatureReportFeedback}); err != nil {
		t.Fatal(err)
	}

	e.group(t, 1001, 9, long, evening, "")
	r := e.calls("setMessageReaction")
	if len(r) != 1 || !strings.Contains(fmt.Sprint(r[0].P["reaction"]), "🔥") || r[0].P["chat_id"].(float64) != -1002494126345 {
		t.Fatalf("report reaction %v", e.got)
	}
	e.reset()

	e.group(t, 1002, 9, "Сделал обзвон", evening, "")
	e.group(t, 1002, 9, "Ещё коротко", evening, "")
	m := e.calls("sendMessage")
	if len(m) != 1 || m[0].P["chat_id"].(float64) != 1002 || !strings.Contains(m[0].P["text"].(string), "слишком короткое (13 симв.)") {
		t.Fatalf("short note %v", e.got)
	}
	e.reset()

	// No note: the team writes feedback, a resident answers someone, or writes after his report.
	e.group(t, 453800951, 9, "Отлично, молодец", evening, "")
	e.group(t, 1004, 9, "Спасибо!", evening, `,"reply_to_message":{"message_id":777}`)
	e.group(t, 1001, 9, "Да, сделаю завтра", evening, "") // 1001 sent a report above
	if m = e.calls("sendMessage"); len(m) != 0 {
		t.Fatalf("short note to the team / a reply / after a report: %v", m)
	}
	// A plain topic message (reply_to = the topic itself) is not a reply: still warned.
	e.group(t, 1004, 9, "Коротко", evening, `,"reply_to_message":{"message_id":9}`)
	if m = e.calls("sendMessage"); len(m) != 1 || m[0].P["chat_id"].(float64) != 1004 {
		t.Fatalf("topic message must be judged as before: %v", e.got)
	}
	e.reset()

	e.group(t, 1004, 2, long, evening, "")
	e.group(t, 555, 2, long, evening, "") // not a resident: no note
	m = e.calls("sendMessage")
	if len(m) != 1 || m[0].P["chat_id"].(float64) != 1004 || !strings.Contains(m[0].P["text"].(string), "не в том разделе") {
		t.Fatalf("wrong topic note %v", e.got)
	}
	e.reset()

	e.group(t, 1004, 9, long, evening.Add(3*time.Hour+30*time.Minute), "") // 00:30
	m = e.calls("sendMessage")
	if len(m) != 1 || !strings.Contains(m[0].P["text"].(string), "после полуночи") || len(e.calls("setMessageReaction")) != 0 {
		t.Fatalf("late note %v", e.got)
	}
	e.reset()

	e.group(t, 1001, 5, "", evening, `,"video_note":{"file_id":"v"}`)
	r = e.calls("setMessageReaction")
	if len(r) != 1 || !strings.Contains(fmt.Sprint(r[0].P["reaction"]), "👍") {
		t.Fatalf("video reaction %v", e.got)
	}
	e.reset()

	// Reaction refused: a system event, it goes to the log, not to the team's chat.
	e.fail["setMessageReaction"] = "Bad Request: not enough rights"
	e.group(t, 1001, 9, long, evening, "")
	e.group(t, 1001, 9, long, evening, "")
	if m = e.calls("sendMessage"); len(m) != 0 {
		t.Fatalf("reaction warning must not reach the chat: %v", m)
	}
}

func TestEveningReminder(t *testing.T) {
	e := newFeatEnv(t)
	ctx := context.Background()
	today := club.Today()
	at := func(h, mi int) time.Time {
		return time.Date(today.Year(), today.Month(), today.Day(), h, mi, 0, 0, club.Almaty)
	}
	long := strings.Repeat("Сделал три встречи. ", 6)
	e.group(t, 1001, 9, long, at(12, 0), "") // Альтаир сдал
	e.group(t, 1004, 9, "коротко", at(12, 0), "")

	// The bot comes through the server: messages keep arriving in the evening.
	_, _ = e.db.Pool.Exec(ctx, `UPDATE bot_updates SET received_at = $1`, at(21, 50))
	if s, _ := e.s.maybeEveningReminder(ctx, at(22, 1)); s != nil {
		t.Fatal("sent while the feature is off")
	}
	_, _ = e.s.SetFeatures(ctx, []string{FeatureEveningReminder})
	if s, _ := e.s.maybeEveningReminder(ctx, at(21, 59)); s != nil {
		t.Fatal("sent before 22:00")
	}
	e.reset()
	sent, err := e.s.maybeEveningReminder(ctx, at(22, 1))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(sent, ",") != "Асет,Марат" {
		t.Fatalf("reminded %v", sent)
	}
	m := e.calls("sendMessage")
	if len(m) != 2 || m[0].P["text"] != "⏰ Асет, отчёт до 23:59!" {
		t.Fatalf("texts %v", m)
	}
	if s, _ := e.s.maybeEveningReminder(ctx, at(22, 30)); s != nil {
		t.Fatal("sent twice")
	}
	if s, _ := e.s.maybeEveningReminder(ctx, at(23, 10)); s != nil {
		t.Fatal("sent late")
	}

	// The bot went back to the sheet: the server knows nothing about today.
	_, _ = e.db.Pool.Exec(ctx, `DELETE FROM bot_meta WHERE key LIKE 'once:evening%'`)
	_, _ = e.db.Pool.Exec(ctx, `UPDATE bot_updates SET received_at = $1`, at(15, 0))
	e.reset()
	sent, _ = e.s.maybeEveningReminder(ctx, at(22, 5))
	m = e.calls("sendMessage")
	if len(sent) != 0 || len(m) != 0 {
		t.Fatalf("silent server: sent %v, calls %v", sent, m)
	}
}
