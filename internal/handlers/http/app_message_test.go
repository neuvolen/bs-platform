package http

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

func TestAppMessage(t *testing.T) {
	g, _, r, now := newGateway(t)
	var sent []map[string]any
	tg := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, rq *http.Request) {
		b, _ := io.ReadAll(rq.Body)
		var m map[string]any
		_ = json.Unmarshal(b, &m)
		sent = append(sent, m)
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer tg.Close()
	g.TGBase = tg.URL
	post := func(id int64, body string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		q := url.Values{"_tg": {makeInitData(testBotToken, id, "Альтаир", *now)}}
		rq := httptest.NewRequest("POST", "/api/v1/app/message?"+q.Encode(), strings.NewReader(body))
		rq.Header.Set("Content-Type", "text/plain")
		r.ServeHTTP(w, rq)
		return w
	}
	if w := post(453800951, `{"chatId":"490685605","text":"Привет"}`); !strings.Contains(w.Body.String(), `"ok":true`) {
		t.Fatalf("team → resident: %s", w.Body.String())
	}
	if len(sent) != 1 || sent[0]["text"] != "Сообщение от Рустам:\nПривет" || sent[0]["chat_id"].(float64) != 490685605 {
		t.Fatalf("sent: %v", sent)
	}
	if w := post(111, `{"chatId":"490685605","text":"спам"}`); w.Code != 403 {
		t.Fatalf("resident → resident must be refused: %d", w.Code)
	}
	if w := post(111, `{"chatId":453800951,"text":"Вопрос"}`); !strings.Contains(w.Body.String(), `"ok":true`) || !strings.HasPrefix(sent[1]["text"].(string), "Сообщение от Альтаир") {
		t.Fatalf("resident → team: %s %v", w.Body.String(), sent)
	}
}
