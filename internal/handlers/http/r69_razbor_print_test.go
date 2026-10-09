package http

import (
	"mime"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
)

func TestR69RazborPrintPDF(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.POST("/razbor/pdf", RazborPrintPDF)
	call := func(body string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		req := httptest.NewRequest("POST", "/razbor/pdf", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		r.ServeHTTP(w, req)
		return w
	}
	w := call(`{"resident":"Асет Нуров","date":"09.10.2026","cycle":2,"pointA":"Выручка 9 000 000 ₸","pointB":"14 000 000 ₸",
		"diags":[{"title":"Кассовые разрывы","symptoms":["Платит из выручки дня"]}],
		"tasks":[{"text":"Собрать платёжный календарь","owner":"Асет","due":"15.10"}],
		"tools":[{"title":"Платёжный календарь","how":"Выпишите все платежи"}]}`)
	if w.Code != 200 || w.Header().Get("Content-Type") != "application/pdf" || !strings.HasPrefix(w.Body.String(), "%PDF-") {
		t.Fatalf("pdf: %d %s", w.Code, w.Body.String()[:minR69(80, w.Body.Len())])
	}
	_, ps, err := mime.ParseMediaType(w.Header().Get("Content-Disposition"))
	if err != nil || ps["filename"] != "Разбор Асет Нуров 09.10.2026.pdf" {
		t.Fatalf("name: %v %q", err, w.Header().Get("Content-Disposition"))
	}
	if w = call(`{"resident":"Асет"}`); w.Code != 400 {
		t.Fatalf("empty: %d", w.Code)
	}
	if w = call(`not json`); w.Code != 400 {
		t.Fatalf("bad: %d", w.Code)
	}
}

func minR69(a, b int) int {
	if a < b {
		return a
	}
	return b
}
