package middleware

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
)

// R70: a handler under Compress can still lengthen its deadlines.
func TestR70CompressKeepsDeadlines(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(Compress())
	var werr, rerr error
	r.GET("/api/long", func(c *gin.Context) {
		rc := http.NewResponseController(c.Writer)
		werr = rc.SetWriteDeadline(time.Now().Add(time.Minute))
		rerr = rc.SetReadDeadline(time.Now().Add(time.Minute))
		c.JSON(200, gin.H{"t": strings.Repeat("x", 2000)})
	})
	srv := httptest.NewServer(r)
	defer srv.Close()
	req, _ := http.NewRequest("GET", srv.URL+"/api/long", nil)
	req.Header.Set("Accept-Encoding", "gzip")
	resp, err := srv.Client().Transport.RoundTrip(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.Header.Get("Content-Encoding") != "gzip" {
		t.Fatalf("not compressed: %v", resp.Header)
	}
	if werr != nil || rerr != nil {
		t.Fatalf("deadlines: write %v, read %v", werr, rerr)
	}
}
