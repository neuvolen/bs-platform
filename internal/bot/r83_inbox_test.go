package bot

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	pg "github.com/bnursik/business_surgery_backend/internal/repository/pg"
)

// R83: a message the team forwards to the bot lands in «Быстрые заметки»,
// with a photo or a voice note downloaded; commands, residents and leads keep
// their old way.
func TestR83InboxFromBot(t *testing.T) {
	e := newFeatEnv(t) // the database and the club's people
	var mu sync.Mutex
	var sent []string
	tg := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "/file/bot") {
			_, _ = w.Write([]byte("OggS-voice"))
			return
		}
		m := r.URL.Path[strings.LastIndex(r.URL.Path, "/")+1:]
		var p map[string]any
		_ = json.NewDecoder(r.Body).Decode(&p)
		switch m {
		case "getFile":
			_, _ = w.Write([]byte(`{"ok":true,"result":{"file_path":"voice/f1.oga","file_size":10}}`))
			return
		case "sendMessage":
			mu.Lock()
			sent = append(sent, fmt.Sprint(p["chat_id"])+": "+fmt.Sprint(p["text"]))
			mu.Unlock()
		}
		_, _ = w.Write([]byte(`{"ok":true,"result":{"message_id":1}}`))
	}))
	t.Cleanup(tg.Close)
	s := New(pg.NewBotRepo(e.db), Options{Token: "1:x", APIBase: tg.URL, Admins: []int64{453800951}})
	var got []InboxNote
	s.SetInboxHook(func(ctx context.Context, n InboxNote) (string, bool) {
		got = append(got, n)
		return "📥 Сохранено", true
	})
	ctx := context.Background()
	msg := func(from int64, extra string) []byte {
		return []byte(fmt.Sprintf(`{"update_id":1,"message":{"message_id":2,"date":1,"chat":{"id":%d,"type":"private"},"from":{"id":%d,"first_name":"Рустам"}%s}}`, from, from, extra))
	}
	// a forwarded text
	s.private(ctx, msg(453800951, `,"text":"Позвонить бухгалтеру","forward_origin":{"type":"user","sender_user":{"first_name":"Береке","last_name":"Ерниязов"}}`))
	if len(got) != 1 || got[0].Text != "Позвонить бухгалтеру" || got[0].Forward != "Береке Ерниязов" || got[0].FromID != 453800951 {
		t.Fatalf("forward: %+v", got)
	}
	// a voice note is downloaded for the transcription
	s.private(ctx, msg(453800951, `,"voice":{"file_id":"v1","mime_type":"audio/ogg","file_size":10}`))
	if len(got) != 2 || string(got[1].Voice) != "OggS-voice" || got[1].VoiceMime != "audio/ogg" {
		t.Fatalf("voice: %+v", got[1:])
	}
	// /note with text
	s.private(ctx, msg(453800951, `,"text":"/note идея для рилса"`))
	if len(got) != 3 || got[2].Text != "идея для рилса" {
		t.Fatalf("/note: %+v", got[2:])
	}
	// other commands keep their meaning; a resident's text is not the team's inbox
	s.private(ctx, msg(453800951, `,"text":"/help_admin"`))
	s.private(ctx, msg(1001, `,"text":"Сегодня сделал 3 встречи и закрыл сделку на 200 тысяч"`))
	if len(got) != 3 {
		t.Fatalf("taken by mistake: %+v", got[3:])
	}
	mu.Lock()
	defer mu.Unlock()
	if n := strings.Count(strings.Join(sent, "\n"), "📥 Сохранено"); n != 3 {
		t.Fatalf("replies %d: %v", n, sent)
	}
}
