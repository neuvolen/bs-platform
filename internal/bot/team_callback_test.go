package bot

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// The team's buttons (cnt_…) go to the registered hook; leads and other
// people's presses do not.
func TestTeamCallbackHook(t *testing.T) {
	var mu sync.Mutex
	var answers []map[string]any
	tg := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var p map[string]any
		_ = json.NewDecoder(r.Body).Decode(&p)
		if strings.HasSuffix(r.URL.Path, "/answerCallbackQuery") {
			mu.Lock()
			answers = append(answers, p)
			mu.Unlock()
		}
		_, _ = w.Write([]byte(`{"ok":true,"result":true}`))
	}))
	defer tg.Close()
	s := New(nil, Options{Token: "T", APIBase: tg.URL, Admins: []int64{453800951}})
	var got []CallbackUpdate
	s.SetTeamCallbackHook("cnt_", func(_ context.Context, cb CallbackUpdate) (string, bool) {
		got = append(got, cb)
		return "Пропущено", true
	})
	body := func(from int64, data string) []byte {
		b, _ := json.Marshal(map[string]any{"update_id": 1, "callback_query": map[string]any{
			"id": "cb1", "data": data, "from": map[string]any{"id": from, "first_name": "Рустам"},
			"message": map[string]any{"message_id": 55, "chat": map[string]any{"id": from, "type": "private"}}}})
		return b
	}
	ctx := context.Background()
	if !s.takeCallback(ctx, body(453800951, "cnt_skip_th-20261005-g001")) {
		t.Fatal("the owner's button must be taken")
	}
	if len(got) != 1 || got[0].MessageID != 55 || got[0].FromID != 453800951 || got[0].ChatID != 453800951 || got[0].Data != "cnt_skip_th-20261005-g001" {
		t.Fatalf("hook got %+v", got)
	}
	if len(answers) != 1 || answers[0]["text"] != "Пропущено" || answers[0]["callback_query_id"] != "cb1" {
		t.Fatalf("answer %v", answers)
	}
	if s.takeCallback(ctx, body(777, "cnt_ok_20261005")) {
		t.Fatal("not the team: left to the script")
	}
	if s.takeCallback(ctx, body(453800951, "menu_main")) {
		t.Fatal("other buttons of the team stay with the script")
	}
	if len(got) != 1 {
		t.Fatalf("hook called %d times", len(got))
	}
}
