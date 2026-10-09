package http

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/bnursik/business_surgery_backend/internal/bot"
	pg "github.com/bnursik/business_surgery_backend/internal/repository/pg"
)

// R73: «Войти через приложение Telegram» on the login page is t.me/bsurgery_bot?start=login.
// Everybody (a lead, a resident, the team) gets the platform as a Mini App button,
// answered by the server; nothing goes to the script.
func TestR73LoginStart(t *testing.T) {
	e := newBotEnv(t)
	ctx := context.Background()
	for _, q := range []string{
		`DELETE FROM club_residents WHERE tg_id IN (888)`,
		`INSERT INTO club_residents (name, tg_id) VALUES ('Резидент Тест', 888)`,
	} {
		if _, err := e.db.Pool.Exec(ctx, q); err != nil {
			t.Fatal(err)
		}
	}
	repo := pg.NewPlatformRepo(e.db)
	f := NewLeadFunnel(repo, e.svc.SendMessageKB, []int64{111})
	e.svc.SetStartHook(f.HandleStart)
	e.useTestRelay()
	for i, from := range []int64{777, 888, 111} {
		id := int64(7301 + i)
		e.hook(fmt.Sprintf(`{"update_id":%d,"message":{"message_id":%d,"date":%d,"chat":{"id":%d,"type":"private"},"from":{"id":%d,"first_name":"Айдар"},"text":"/start login"}}`,
			id, id, time.Now().Unix(), from, from))
	}
	sent := func(chat int64) []map[string]any {
		e.tg.mu.Lock()
		defer e.tg.mu.Unlock()
		var out []map[string]any
		for _, m := range e.tg.sent {
			if int64(m["chat_id"].(float64)) == chat {
				out = append(out, m)
			}
		}
		return out
	}
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) && (len(sent(777)) == 0 || len(sent(888)) == 0 || len(sent(111)) == 0) {
		time.Sleep(100 * time.Millisecond)
	}
	time.Sleep(300 * time.Millisecond)
	for _, chat := range []int64{777, 888, 111} {
		w := sent(chat)
		if len(w) != 1 || w[0]["text"] != bot.LoginStartText {
			t.Fatalf("%d: %v", chat, w)
		}
		kbs, _ := json.Marshal(w[0]["reply_markup"])
		if !strings.Contains(string(kbs), `"web_app"`) || !strings.Contains(string(kbs), "Открыть платформу") {
			t.Fatalf("%d: buttons %s", chat, kbs)
		}
	}
	e.script.mu.Lock()
	defer e.script.mu.Unlock()
	for _, u := range e.script.got {
		if n := int64(u["update_id"].(float64)); n >= 7301 && n <= 7303 {
			t.Fatalf("/start login reached the script: %v", u)
		}
	}
}
