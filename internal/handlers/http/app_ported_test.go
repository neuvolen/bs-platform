package http

import (
	"context"
	"encoding/json"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/bnursik/business_surgery_backend/internal/club"
	pg "github.com/bnursik/business_surgery_backend/internal/repository/pg"
)

// R32d: the app's last script-only actions are answered by the server; the
// script is never called.
func TestAppPortedActions(t *testing.T) {
	e := newOffEnv(t, "server", nil)
	ctx := context.Background()
	e.g.Ported = &AppPorted{TG: e.svc.API, Meta: pg.NewBotRepo(e.db), Team: []int64{offOwner}}
	if _, err := e.db.Pool.Exec(ctx, `DELETE FROM bot_meta WHERE key = $1`, metaNPSLast); err != nil {
		t.Fatal(err)
	}
	put := func(name string, rows [][]string) {
		b, _ := json.Marshal(rows)
		if _, err := e.db.Pool.Exec(ctx, `INSERT INTO club_sheets (name, rows) VALUES ($1,$2) ON CONFLICT (name) DO UPDATE SET rows = EXCLUDED.rows`, name, b); err != nil {
			t.Fatal(err)
		}
	}
	sheet := func(name string) [][]string {
		var raw []byte
		if err := e.db.Pool.QueryRow(ctx, `SELECT rows FROM club_sheets WHERE name = $1`, name).Scan(&raw); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		var rows [][]string
		_ = json.Unmarshal(raw, &rows)
		return rows
	}
	put(club.SheetLeadmagnets, [][]string{{"Ключ", "Название", "File ID", "Дата загрузки", "Кто загрузил"}, {"a", "Чек-лист A", "FILE_A"}, {"b", "Чек-лист B", "FILE_B"}})
	put(club.SheetSmm, [][]string{{"Дата", "Площадка", "Рубрика", "Заголовок", "Текст", "Статус", "Ссылка"},
		{"01.10.2026", "Telegram", "Кейс", "Рост <втрое>", "Текст & цифры", "Черновик", ""}})

	tgCalls := func(method string) []map[string]any {
		e.tg.mu.Lock()
		defer e.tg.mu.Unlock()
		var out []map[string]any
		for _, c := range e.tg.calls {
			if c["_m"] == method {
				out = append(out, c)
			}
		}
		return out
	}

	// A resident: the wheel's analysis (rules without AI); team actions refused.
	r := e.call(offAset, "analyzeWheel", "axes", "Стратегия|Финансы|Команда", "was", "5,6,4", "now", "7,4,4", "days", "30")
	if txt, _ := r["text"].(string); r["source"] != "rules" || !strings.Contains(txt, "РОСТ: стратегия с 5 до 7") ||
		!strings.Contains(txt, "ПРОСЕДАНИЕ: финансы упало на 2") || !strings.Contains(txt, "ФОКУС: финансы") {
		t.Fatalf("wheel %v", r)
	}
	for _, a := range []string{"banFromChannel", "runNPS", "smmPublished", "swapLeadmagnetFiles", "addTodo"} {
		if r := e.call(offAset, a, "chatId", "111", "row", "2"); r["error"] != "Только для команды" {
			t.Fatalf("%s by a resident: %v", a, r)
		}
	}

	// The team.
	if r := e.call(offOwner, "banFromChannel", "chatId", "777001"); r["ok"] != true {
		t.Fatalf("ban %v", r)
	}
	if b := tgCalls("banChatMember"); len(b) != 1 || b[0]["chat_id"] != "@bsurgery_kz" || b[0]["user_id"].(float64) != 777001 {
		t.Fatalf("ban call %v", b)
	}
	if r := e.call(offOwner, "testLeadmagnet", "key", "a"); r["ok"] != true {
		t.Fatalf("test lm %v", r)
	}
	if d := tgCalls("sendDocument"); len(d) != 1 || d[0]["document"] != "FILE_A" || d[0]["chat_id"].(float64) != float64(offOwner) {
		t.Fatalf("sendDocument %v", d)
	}
	if r := e.call(offOwner, "swapLeadmagnetFiles", "keyA", "a", "keyB", "b"); r["ok"] != true {
		t.Fatalf("swap %v", r)
	}
	if lm := sheet(club.SheetLeadmagnets); lm[1][2] != "FILE_B" || lm[2][2] != "FILE_A" {
		t.Fatalf("swapped %v", lm)
	}
	if r := e.call(offOwner, "publishSmm", "row", "2"); r["ok"] != true {
		t.Fatalf("publish %v", r)
	}
	var post string
	for _, c := range tgCalls("sendMessage") {
		if c["chat_id"] == "@bsurgery_kz" {
			post, _ = c["text"].(string)
		}
	}
	if post != "<b>Рост &lt;втрое&gt;</b>\n\nТекст &amp; цифры" {
		t.Fatalf("channel post %q", post)
	}
	if smm := sheet(club.SheetSmm); smm[1][5] != "Опубликовано" {
		t.Fatalf("smm status %v", smm[1])
	}
	r = e.call(offOwner, "createEvent", "type", "Мастермайнд", "name", "Встреча клуба", "date", "10.10.2026", "time", "18:00")
	if u, _ := r["calendarUrl"].(string); r["ok"] != true || !strings.Contains(u, "calendar.google.com") || !strings.Contains(u, "20261010T180000") {
		t.Fatalf("event %v", r)
	}
	if e.n(`SELECT count(*) FROM club_meetings WHERE resident = '🎬 Встреча клуба' AND date = '2026-10-10' AND time = '18:00'`) != 1 {
		t.Fatal("the event is not in the schedule")
	}
	if r := e.call(offOwner, "addTodo", "text", "Позвонить партнёру", "deadline", "2026-10-12"); r["ok"] != true || r["calendarUrl"] == nil {
		t.Fatalf("todo %v", r)
	}
	if td := sheet(club.SheetTodo); len(td) != 2 || td[1][1] != "Позвонить партнёру" || td[1][2] != "open" || td[1][5] != "12.10.2026" {
		t.Fatalf("todo sheet %v", td)
	}
	// NPS: the active residents get it, once in 30 days.
	if r := e.call(offOwner, "runNPS"); r["ok"] != true || r["sent"].(float64) != 2 {
		t.Fatalf("nps %v", r)
	}
	e.tg.waitText(t, offAset, "Быстрый опрос BS")
	e.tg.waitText(t, offAltair, "Быстрый опрос BS")
	if r := e.call(offOwner, "runNPS"); r["skipped"] != true {
		t.Fatalf("nps again %v", r)
	}
	if n := len(e.tg.to(offAset)); n != 1 {
		t.Fatalf("Асет got %d messages", n)
	}
	if r := e.call(offOwner, "publishCarousel", "urls", "[]"); r["ok"] != false || !strings.Contains(r["error"].(string), "Контент-завод") {
		t.Fatalf("carousel %v", r)
	}
	if r := e.call(offOwner, "generateSmm", "platform", "Instagram"); r["error"] == nil {
		t.Fatalf("smm without AI %v", r)
	}
	if n := atomic.LoadInt64(e.script); n != 0 {
		t.Fatalf("the script was called %d times", n)
	}
}
