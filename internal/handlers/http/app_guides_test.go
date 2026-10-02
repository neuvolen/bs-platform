package http

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/bnursik/business_surgery_backend/internal/content"
)

func TestGuides(t *testing.T) {
	var idx struct {
		Version string           `json:"version"`
		Items   []map[string]any `json:"items"`
	}
	if err := json.Unmarshal(content.GuidesIndex(), &idx); err != nil || len(idx.Items) != 99 {
		t.Fatalf("index: %v %d", err, len(idx.Items))
	}
	written := 0
	for _, it := range idx.Items {
		id := it["id"].(string)
		if it["existing"] == true {
			if _, ok := content.LeadMagnetKey[id]; !ok {
				t.Fatalf("existing without bot file: %s", id)
			}
			continue
		}
		written++
		if content.Guide(id) == nil || len(content.GuidePDF(id)) < 50000 {
			t.Fatalf("guide %s: json or pdf missing", id)
		}
		if strings.Contains(string(content.Guide(id)), "—") {
			t.Fatalf("em dash in %s", id)
		}
	}
	if written != 95 {
		t.Fatalf("written guides: %d", written)
	}

	g, _, r, now := newGateway(t)
	var calls []string
	tg := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, rq *http.Request) {
		b, _ := io.ReadAll(rq.Body)
		calls = append(calls, rq.URL.Path+" "+rq.Header.Get("Content-Type")[:10]+" "+string(b))
		_, _ = w.Write([]byte(`{"ok":true,"result":{"document":{"file_id":"FID1"}}}`))
	}))
	defer tg.Close()
	g.TGBase = tg.URL
	do := func(method, path string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		q := url.Values{"_tg": {makeInitData(testBotToken, 777, "Айдар", *now)}}
		r.ServeHTTP(w, httptest.NewRequest(method, path+"?"+q.Encode(), nil))
		return w
	}
	if w := do("GET", "/api/v1/app/guides"); !strings.Contains(w.Body.String(), "Платёжный календарь за один вечер") {
		t.Fatalf("guides: %d", w.Code)
	}
	if w := do("GET", "/api/v1/app/checklists"); !strings.Contains(w.Body.String(), `"items"`) {
		t.Fatalf("old address: %d", w.Code)
	}
	if w := do("GET", "/api/v1/app/guide/g003"); !strings.Contains(w.Body.String(), `"steps"`) {
		t.Fatalf("guide: %d", w.Code)
	}
	if w := do("GET", "/api/v1/app/guide/../x"); w.Code == 200 {
		t.Fatal("bad id")
	}
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest("GET", "/api/v1/public/guide/g003.pdf", nil))
	if w.Code != 200 || !strings.HasPrefix(w.Body.String(), "%PDF") {
		t.Fatalf("public pdf: %d", w.Code)
	}
	if w := do("POST", "/api/v1/app/guide/g003/send"); !strings.Contains(w.Body.String(), `"ok":true`) {
		t.Fatalf("send: %s", w.Body.String())
	}
	if w := do("POST", "/api/v1/app/guide/g003/send"); !strings.Contains(w.Body.String(), `"ok":true`) {
		t.Fatalf("send 2: %s", w.Body.String())
	}
	if len(calls) != 2 || !strings.Contains(calls[0], "multipart") || !strings.Contains(calls[1], `"document":"FID1"`) {
		t.Fatalf("telegram calls: %v", calls)
	}
}
