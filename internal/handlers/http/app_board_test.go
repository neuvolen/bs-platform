package http

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/bnursik/business_surgery_backend/internal/repository/pg"
)

type fakeBoards struct {
	byTg   map[int64]string
	boards []pg.PlatformBoard
}

func (f *fakeBoards) ResidentByTg(_ context.Context, id int64) (string, bool, error) {
	n, ok := f.byTg[id]
	return n, ok, nil
}
func (f *fakeBoards) LiveBoards(context.Context) ([]pg.PlatformBoard, error) { return f.boards, nil }

const boardAltair = `{"name":"Разбор · Альтаир","updated":"2026-09-28T08:28:44Z",
 "info":{"a":"Оборот 5 млн","b":"Цель 12 млн","name":"Альтаир"},
 "history":[{},{}],
 "nodes":[
  {"type":"root","role":"center","title":"Имя резидента","desc":"Сфера деятельности"},
  {"type":"point","role":"pointA","title":"Оборот, прибыль сейчас"},
  {"type":"strat","role":"strategy","title":"Как дойдём","desc":"Платёжный календарь на 4 недели"},
  {"type":"strat","role":"exp","title":"Что уже пробовал","desc":""},
  {"type":"diag","title":"Кассовые разрывы","desc":"Деньги есть, но не вовремя","organ":"Финансы"},
  {"type":"tool","title":"Платёжный календарь","desc":"Все поступления"},
  {"type":"task","title":"Открыть счёт под налоги","task":{"completed":true}},
  {"type":"task","title":"Собрать отчёт за сентябрь","task":{"kind":"once"}}
 ]}`

func boardGet(r *gin.Engine, q url.Values) (int, map[string]any) {
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest("GET", "/api/v1/app/myboard?"+q.Encode(), nil))
	var m map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &m)
	return w.Code, m
}

func TestAppMyBoard(t *testing.T) {
	g, _, r, now := newGateway(t)
	old := time.Now().Add(-48 * time.Hour)
	g.Boards = &fakeBoards{
		byTg: map[int64]string{111: "Альтаир", 222: "Асет"},
		boards: []pg.PlatformBoard{
			{ID: "old", Resident: "Альтаир", Data: []byte(`{"name":"старая","nodes":[]}`), UpdatedAt: old},
			{ID: "cur", Resident: "Альтаир", Data: []byte(boardAltair), UpdatedAt: time.Now()},
			{ID: "del", Resident: "Альтаир", Data: []byte(`{"name":"удалённая"}`), Deleted: true, UpdatedAt: time.Now().Add(time.Hour)},
			{ID: "aset", Resident: "Асет", Data: []byte(`{"name":"Разбор · Асет","nodes":[]}`), UpdatedAt: time.Now()},
		},
	}

	// no Telegram signature: refused
	if code, _ := boardGet(r, url.Values{}); code != 401 {
		t.Fatalf("no initData: want 401, got %d", code)
	}

	// the resident gets their newest live board, placeholders dropped
	code, m := boardGet(r, url.Values{"_tg": {makeInitData(testBotToken, 111, "Альтаир", *now)}})
	if code != 200 || m["resident"] != "Альтаир" {
		t.Fatalf("resident: %d %v", code, m)
	}
	b := m["board"].(map[string]any)
	if b["name"] != "Разбор · Альтаир" || b["cycle"].(float64) != 3 {
		t.Fatalf("wrong board: %v", b)
	}
	if b["pointA"] != "Оборот 5 млн" || b["pointB"] != "Цель 12 млн" {
		t.Fatalf("points: %v / %v", b["pointA"], b["pointB"])
	}
	if b["strategy"] != "Платёжный календарь на 4 недели" || b["experience"] != "" {
		t.Fatalf("strategy: %q exp: %q", b["strategy"], b["experience"])
	}
	if len(b["diagnoses"].([]any)) != 1 || len(b["tools"].([]any)) != 1 {
		t.Fatalf("diag/tools: %v", b)
	}
	tasks := b["tasks"].([]any)
	if len(tasks) != 2 || tasks[0].(map[string]any)["done"] != true || tasks[1].(map[string]any)["done"] != nil {
		t.Fatalf("tasks: %v", tasks)
	}

	// a resident cannot read another one's board by passing a name
	_, m = boardGet(r, url.Values{"_tg": {makeInitData(testBotToken, 222, "Асет", *now)}, "name": {"Альтаир"}})
	if m["resident"] != "Асет" || m["board"].(map[string]any)["name"] != "Разбор · Асет" {
		t.Fatalf("name override leaked: %v", m)
	}

	// the team can preview a resident
	_, m = boardGet(r, url.Values{"_tg": {makeInitData(testBotToken, 453800951, "Рустам", *now)}, "name": {"Альтаир"}})
	if m["resident"] != "Альтаир" {
		t.Fatalf("admin preview: %v", m)
	}

	// a lead has no board
	_, m = boardGet(r, url.Values{"_tg": {makeInitData(testBotToken, 999, "Лид", *now)}})
	if m["board"] != nil || m["reason"] != "not_resident" {
		t.Fatalf("lead: %v", m)
	}
}
