package middleware

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
)

func TestRedactURL(t *testing.T) {
	initData := "query_id%3DAAH%26user%3D%257B%2522id%2522%253A42%257D%26auth_date%3D1790913669%26signature%3DjY8Bia7ZGtRglVysW2bfbErZUnPLAPi2u8%26hash%3D8dce6a5ce87c998fd1cfe75ea390ac02e58de32c"
	cases := []struct{ in, want string }{
		{"/api/v1/app/call?action=getBotCache&chatId=42&_tg=" + initData, "/api/v1/app/call?action=getBotCache&chatId=***&_tg=***"},
		{"/api/v1/app/call?action=getBotCache&_et=W%2F%22abc123%22&_tg=x", "/api/v1/app/call?action=getBotCache&_et=W%2F%22abc123%22&_tg=***"},
		{"/api/v1/platform/sync?since=2839", "/api/v1/platform/sync?since=2839"},
		{"/sum/abc123.9f8e7d6c5b4a39281706f5e4d3c2b1a0", "/sum/***"},
		{"/sum/abc123.9f8e7d6c5b4a.pdf", "/sum/***.pdf"},
		{"/b/0123456789abcdef0123456789abcdef/data", "/b/***/data"},
		{"/api/v1/public/sales/report/77/sigsig", "/api/v1/public/sales/report/77/***"},
		{"/api/v1/wa/open/15/abc", "/api/v1/wa/open/15/***"},
		{"/api/v1/wa/webhook/supersecret", "/api/v1/wa/webhook/***"},
		{"/gcal?k=abcdef&state=xyz&code=4/0AQ", "/gcal?k=***&state=***&code=***"},
		{"/api/v1/app/team/doc?key=bs_kanban&_tg=a%3Db", "/api/v1/app/team/doc?key=***&_tg=***"},
		{"/api/v1/platform/x/AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA", "/api/v1/platform/x/AAAA***"},
		{"/api/v1/platform/boards/mv0zxzdf8o2lq", "/api/v1/platform/boards/mv0zxzdf8o2lq"},
		{"/api/v1/app/call?action=x&flag&empty=", "/api/v1/app/call?action=x&flag&empty="},
		{"/api/v1/app/call?action=" + strings.Repeat("a", 30), "/api/v1/app/call?action=***"},
	}
	for _, c := range cases {
		if got := RedactURL(c.in); got != c.want {
			t.Errorf("RedactURL(%q)\n got %q\nwant %q", c.in, got, c.want)
		}
	}
}

func TestRequestLogMasksInitData(t *testing.T) {
	gin.SetMode(gin.TestMode)
	var buf bytes.Buffer
	old := gin.DefaultWriter
	gin.DefaultWriter = &buf
	defer func() { gin.DefaultWriter = old }()
	r := gin.New()
	r.Use(RequestLog())
	r.GET("/api/v1/app/call", func(c *gin.Context) { c.Status(http.StatusUnauthorized) })
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest("GET", "/api/v1/app/call?action=getBotCache&_tg=user%3D%257B%2522id%2522%253A42%257D%26hash%3Ddeadbeef", nil))
	out := buf.String()
	if !strings.Contains(out, "401") || !strings.Contains(out, "action=getBotCache") {
		t.Fatalf("log line lost its shape: %q", out)
	}
	for _, leak := range []string{"hash", "deadbeef", "user%3D", "%2522id"} {
		if strings.Contains(out, leak) {
			t.Fatalf("log line leaks %q: %q", leak, out)
		}
	}
}
