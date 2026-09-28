package bot

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	pg "github.com/bnursik/business_surgery_backend/internal/repository/pg"
	"github.com/bnursik/business_surgery_backend/migrations"
)

func TestParseConversations(t *testing.T) {
	cases := []struct {
		body, kind, key string
		chat            int64
	}{
		{`{"update_id":1,"message":{"chat":{"id":55,"type":"private"},"from":{"id":55},"text":"/start"}}`, "message", "55", 55},
		{`{"update_id":2,"message":{"chat":{"id":-100,"type":"supergroup"},"from":{"id":7},"text":"отчёт"}}`, "message", "-100:7", -100},
		{`{"update_id":3,"edited_message":{"chat":{"id":-100,"type":"supergroup"},"from":{"id":7}}}`, "edited_message", "-100:7", -100},
		{`{"update_id":4,"callback_query":{"from":{"id":9},"message":{"chat":{"id":9,"type":"private"}}}}`, "callback_query", "9", 9},
		{`{"update_id":5,"callback_query":{"from":{"id":9}}}`, "callback_query", "u9", 0},
		{`{"update_id":6,"message_reaction":{"chat":{"id":-100,"type":"supergroup"},"user":{"id":7}}}`, "message_reaction", "-100:7", -100},
		{`{"update_id":7,"my_chat_member":{}}`, "other", "", 0},
	}
	for _, c := range cases {
		p, err := Parse([]byte(c.body))
		if err != nil {
			t.Fatal(err)
		}
		if p.Kind != c.kind || p.OrderKey != c.key || p.ChatID != c.chat {
			t.Fatalf("%s: got %+v", c.body, p)
		}
	}
	for _, bad := range []string{`{}`, `nope`, `{"update_id":0}`} {
		if _, err := Parse([]byte(bad)); err == nil {
			t.Fatalf("%q parsed", bad)
		}
	}
}

func TestBackoff(t *testing.T) {
	if backoff(1) != 5*time.Second || backoff(7) != 8*time.Minute || backoff(30) != 15*time.Minute {
		t.Fatal("backoff steps changed")
	}
}

func TestSecretDependsOnToken(t *testing.T) {
	a := New(nil, Options{Token: "1:a"}).WebhookSecret()
	b := New(nil, Options{Token: "1:b"}).WebhookSecret()
	if a == b || len(a) != 48 || strings.ContainsAny(a, " :/") {
		t.Fatalf("secret %q %q", a, b)
	}
}

// When the script is down the team hears about it, once, not every minute.
func TestAlertWhenUpdatesPileUp(t *testing.T) {
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
	_, _ = db.Pool.Exec(ctx, `TRUNCATE bot_updates, bot_meta`)

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
	s := New(pg.NewBotRepo(db), Options{Token: "1:x", APIBase: tg.URL, Admins: []int64{111, 222}})
	_ = s.SetRelayURL(ctx, "http://127.0.0.1:1/exec")

	// Fresh backlog: no alarm yet.
	_, _ = s.Receive(ctx, []byte(`{"update_id":10,"message":{"chat":{"id":5,"type":"private"},"from":{"id":5}}}`))
	s.checkAlert(ctx)
	if len(sent) != 0 {
		t.Fatalf("alarm on a fresh update: %v", sent)
	}
	// Stuck for 20 minutes: both admins told.
	_, _ = db.Pool.Exec(ctx, `UPDATE bot_updates SET received_at = now() - interval '20 minutes', relay_error = 'script answered 500'`)
	s.checkAlert(ctx)
	if len(sent) != 2 || !strings.Contains(sent[0]["text"].(string), "script answered 500") {
		t.Fatalf("alarm: %v", sent)
	}
	// Still stuck a minute later: silence.
	s.checkAlert(ctx)
	if len(sent) != 2 {
		t.Fatalf("repeated alarm: %d", len(sent))
	}
}
