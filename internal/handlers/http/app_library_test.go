package http

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	pg "github.com/bnursik/business_surgery_backend/internal/repository/pg"
)

type fakeLib struct{ docs map[string]string }

func (f *fakeLib) GetDoc(_ context.Context, scope, key string) (*pg.PlatformDoc, error) {
	v, ok := f.docs[key]
	if !ok {
		return nil, nil
	}
	return &pg.PlatformDoc{Scope: scope, Key: key, Value: v}, nil
}
func (f *fakeLib) GetFile(_ context.Context, id string) (*pg.PlatformFile, error) {
	if id != "f1" {
		return nil, nil
	}
	return &pg.PlatformFile{ID: id, Name: "Принципы.pdf", Mime: "application/pdf", Data: []byte("%PDF")}, nil
}

func TestAppLibrary(t *testing.T) {
	g, _, r, now := newGateway(t)
	g.Boards = &fakeBoards{byTg: map[int64]string{111: "Альтаир"}}
	g.Library = &fakeLib{docs: map[string]string{
		"bs_tools":  `[{"title":"Платёжный календарь","organ":"Финансы","how":["шаг"]},{"title":"Книга: «Принципы»","isBook":true,"file":"f1","fileName":"Принципы.pdf"}]`,
		"bs_diag":   `[{"title":"Кассовые разрывы","organ":"Финансы","desc":"Деньги есть","signs":["a"]}]`,
		"bs_reslib": `[{"cat":"Сервисы","t":"Kaspi","d":"оплата","url":"https://kaspi.kz"}]`,
	}}
	call := func(path string, id int64) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		r.ServeHTTP(w, httptest.NewRequest("GET", path+"?"+url.Values{"_tg": {makeInitData(testBotToken, id, "X", *now)}}.Encode(), nil))
		return w
	}
	w := call("/api/v1/app/library", 111)
	var out struct{ Items []libItem }
	_ = json.Unmarshal(w.Body.Bytes(), &out)
	kinds := []string{}
	for _, it := range out.Items {
		kinds = append(kinds, it.Kind)
	}
	if w.Code != 200 || strings.Join(kinds, ",") != "book,tool,diag,resource" || out.Items[2].Short != "Деньги есть" || out.Items[3].URL != "https://kaspi.kz" {
		t.Fatalf("library: %d %s", w.Code, w.Body.String())
	}
	if w = call("/api/v1/app/library", 999); w.Code != 403 {
		t.Fatalf("a lead must not read the library: %d", w.Code)
	}
	if w = call("/api/v1/app/file/f1", 111); w.Code != 200 || w.Body.String() != "%PDF" {
		t.Fatalf("file: %d", w.Code)
	}
	if w = call("/api/v1/app/file/f1", 999); w.Code != 403 {
		t.Fatalf("file for a lead: %d", w.Code)
	}
	if w = call("/api/v1/app/library", 453800951); w.Code != 200 {
		t.Fatalf("team: %d", w.Code)
	}
}
