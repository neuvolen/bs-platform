package bot

import (
	"context"
	"strconv"
	"strings"
	"testing"
)

// R36: /status: an admin gets the system check, a resident his balance as
// before; without a check set an admin gets the old answer.
func TestR36StatusCommand(t *testing.T) {
	e := newFeatEnv(t)
	ctx := context.Background()
	msg := func(from int64, text string) []byte {
		return []byte(`{"update_id":1,"message":{"message_id":1,"chat":{"id":` + strconv.FormatInt(from, 10) + `,"type":"private"},"from":{"id":` + strconv.FormatInt(from, 10) + `,"first_name":"Р"},"text":"` + text + `"}}`)
	}
	texts := func() []string {
		var out []string
		for _, c := range e.calls("sendMessage") {
			out = append(out, c.P["text"].(string))
		}
		return out
	}
	// no check wired: the admin gets the old /status
	e.s.private(ctx, msg(453800951, "/status"))
	if got := texts(); len(got) != 1 || strings.Contains(got[0], "Проверка системы") {
		t.Fatalf("no hook: %v", got)
	}
	e.reset()
	calls := 0
	e.s.SetSystemCheck(func(ctx context.Context) string {
		calls++
		return "🩺 Проверка системы\n\n✅ ИИ Claude: отвечает"
	})
	e.s.private(ctx, msg(453800951, "/status"))
	if got := texts(); calls != 1 || len(got) != 2 || !strings.Contains(got[0], "Проверяю") || !strings.HasPrefix(got[1], "🩺 Проверка системы") {
		t.Fatalf("admin: %d %v", calls, got)
	}
	// a resident: his balance, never the system check
	e.reset()
	e.s.private(ctx, msg(1001, "/status"))
	if got := texts(); calls != 1 || len(got) != 1 || strings.Contains(got[0], "Проверка системы") {
		t.Fatalf("resident: %d %v", calls, got)
	}
	// the admin's help lists it
	e.reset()
	e.s.private(ctx, msg(453800951, "/help_admin"))
	if got := texts(); len(got) != 1 || !strings.Contains(got[0], "/status: проверка системы") {
		t.Fatalf("help: %v", got)
	}
}
