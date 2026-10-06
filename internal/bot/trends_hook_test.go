package bot

import (
	"context"
	"strconv"
	"strings"
	"testing"
)

// R53: a Threads link from the team lands in «Тренды Threads»; a resident's
// link or the team's ordinary text is not taken.
func TestR53TrendLink(t *testing.T) {
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
	var got []string
	e.s.SetTrendHook(func(ctx context.Context, chat int64, text string) (string, bool) {
		if !strings.Contains(text, "threads.com/@") {
			return "", false
		}
		got = append(got, text)
		return "📥 В «Тренды Threads»: 1 ссылка", true
	})
	e.s.private(ctx, msg(453800951, "глянь https://www.threads.com/@my.twinkles/post/DcObaXSjPe_"))
	if out := texts(); len(got) != 1 || len(out) != 1 || !strings.HasPrefix(out[0], "📥") {
		t.Fatalf("link: %v %v", got, out)
	}
	// a link hidden under a word (entities) and /trend
	e.reset()
	body := []byte(`{"update_id":2,"message":{"message_id":2,"chat":{"id":453800951,"type":"private"},"from":{"id":453800951,"first_name":"Р"},"text":"/trend вот","entities":[{"type":"text_link","url":"https://www.threads.com/@a.b/post/XYZ123abc_"}]}}`)
	e.s.private(ctx, body)
	if len(got) != 2 || len(texts()) != 1 {
		t.Fatalf("entity: %v %v", got, texts())
	}
	// /trend without a link: a hint
	e.reset()
	e.s.private(ctx, msg(453800951, "/trend"))
	if out := texts(); len(out) != 1 || !strings.Contains(out[0], "Пришлите ссылку") {
		t.Fatalf("hint: %v", out)
	}
	// a resident's link is not the team's trend
	e.reset()
	e.s.private(ctx, msg(1001, "https://www.threads.com/@x/post/DzzzYYY1234"))
	if len(got) != 2 {
		t.Fatalf("resident taken: %v", got)
	}
	// other admin commands still work
	e.reset()
	e.s.private(ctx, msg(453800951, "/help_admin"))
	if out := texts(); len(out) != 1 || !strings.Contains(out[0], "/trend ссылка") {
		t.Fatalf("help: %v", out)
	}
}
