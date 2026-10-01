package http

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	pg "github.com/bnursik/business_surgery_backend/internal/repository/pg"
)

type fakeSync struct {
	boards   []pg.PlatformBoard
	docs     map[string]*pg.PlatformDoc
	conflict int
}

func (f *fakeSync) LiveBoards(context.Context) ([]pg.PlatformBoard, error) { return f.boards, nil }
func (f *fakeSync) PutBoard(_ context.Context, id string, base int, data json.RawMessage, by string) (*pg.PlatformBoard, error) {
	for i := range f.boards {
		if f.boards[i].ID == id {
			if f.conflict > 0 {
				f.conflict--
				f.boards[i].Version++ // someone else saved meanwhile
				return &f.boards[i], pg.ErrPlatformConflict
			}
			if f.boards[i].Version != base {
				return &f.boards[i], pg.ErrPlatformConflict
			}
			f.boards[i].Data, f.boards[i].Version = data, base+1
			return &f.boards[i], nil
		}
	}
	return nil, pg.ErrPlatformConflict
}
func (f *fakeSync) GetDoc(_ context.Context, scope, key string) (*pg.PlatformDoc, error) {
	return f.docs[scope+"/"+key], nil
}
func (f *fakeSync) PutDoc(_ context.Context, scope, key string, base int, value string, del bool, by string) (*pg.PlatformDoc, error) {
	d := f.docs[scope+"/"+key]
	if d != nil && d.Version != base {
		return d, pg.ErrPlatformConflict
	}
	v := 1
	if d != nil {
		v = d.Version + 1
	}
	f.docs[scope+"/"+key] = &pg.PlatformDoc{Scope: scope, Key: key, Value: value, Version: v}
	return f.docs[scope+"/"+key], nil
}

func TestAppSyncTestsAndCalendar(t *testing.T) {
	g, _, r, now := newGateway(t)
	g.Boards = &fakeBoards{byTg: map[int64]string{111: "Альтаир", 222: "Асет"}}
	fs := &fakeSync{docs: map[string]*pg.PlatformDoc{}, conflict: 1, boards: []pg.PlatformBoard{
		{ID: "old", Resident: "Альтаир", Version: 3, Data: []byte(`{"tests":{}}`), UpdatedAt: time.Now().Add(-time.Hour)},
		{ID: "cur", Resident: "Альтаир", Version: 7, Data: []byte(`{"name":"Разбор","tests":{"gallup":{"talents":["Стратег"]}},"wheelHist":[]}`), UpdatedAt: time.Now()},
	}}
	g.Sync = fs
	req := func(method, path, body string, id int64) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		q := url.Values{"_tg": {makeInitData(testBotToken, id, "X", *now)}}
		r.ServeHTTP(w, httptest.NewRequest(method, path+"?"+q.Encode(), strings.NewReader(body)))
		return w
	}
	// A wheel passed in the app lands on the CURRENT board, keeps Gallup, adds history; one conflict is retried.
	w := req("POST", "/api/v1/app/mytests", `{"tests":{"wheel":{"Здоровье":7,"Семья":12}},"date":"01 окт."}`, 111)
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"board":"cur"`) {
		t.Fatalf("mytests: %d %s", w.Code, w.Body.String())
	}
	var b map[string]any
	_ = json.Unmarshal(fs.boards[1].Data, &b)
	tests := b["tests"].(map[string]any)
	wheel := tests["wheel"].(map[string]any)
	if wheel["Семья"].(float64) != 10 || tests["gallup"] == nil || len(b["wheelHist"].([]any)) != 1 || b["name"] != "Разбор" {
		t.Fatalf("merged board: %s", fs.boards[1].Data)
	}
	// unknown test keys are ignored; a lead is refused
	if w = req("POST", "/api/v1/app/mytests", `{"tests":{"hack":{"a":1}}}`, 111); w.Code != 200 {
		t.Fatalf("unknown key: %d", w.Code)
	}
	if w = req("POST", "/api/v1/app/mytests", `{"tests":{"wheel":{"a":1}}}`, 999); w.Code != 403 {
		t.Fatalf("lead: %d", w.Code)
	}
	// No board yet: kept in the resident's own scope
	if w = req("POST", "/api/v1/app/mytests", `{"tests":{"diag":{"Финансы":4}}}`, 222); w.Code != 200 || fs.docs["user:tg:222/bs_apptests"] == nil {
		t.Fatalf("no board: %d %s", w.Code, w.Body.String())
	}
	// Calendar: empty, save, read back, stale version conflicts
	if w = req("GET", "/api/v1/app/mycal", "", 111); !strings.Contains(w.Body.String(), `"version":0`) {
		t.Fatalf("empty cal: %s", w.Body.String())
	}
	if w = req("PUT", "/api/v1/app/mycal", `{"value":"{\"slots\":{\"2026-10-02|10\":\"focus\"}}","version":0}`, 111); w.Code != 200 {
		t.Fatalf("put cal: %d %s", w.Code, w.Body.String())
	}
	if w = req("GET", "/api/v1/app/mycal", "", 111); !strings.Contains(w.Body.String(), "focus") {
		t.Fatalf("read cal: %s", w.Body.String())
	}
	if w = req("PUT", "/api/v1/app/mycal", `{"value":"{}","version":0}`, 111); w.Code != 409 || !strings.Contains(w.Body.String(), "focus") {
		t.Fatalf("stale put: %d %s", w.Code, w.Body.String())
	}
	if fs.docs["user:tg:111/bs_mycal"] == nil {
		t.Fatal("calendar must live in the resident's personal scope (same as the platform)")
	}
}
