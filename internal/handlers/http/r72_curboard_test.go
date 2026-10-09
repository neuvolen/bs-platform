package http

import (
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
)

// R72: «Доска» opens the разбор the person worked on last. The record is the
// person's own (every device of theirs reads it), never the club's.
func TestR72CurBoardIsPersonal(t *testing.T) {
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Set("userID", "tg:453800951")
	if s := platformScopeFor(c, "bs_curboard", ""); s != "user:tg:453800951" {
		t.Fatalf("bs_curboard scope = %q, want the person's own", s)
	}
	if s := platformScopeFor(c, "bs_boards_x", ""); s != "club" {
		t.Fatalf("club key scope = %q", s)
	}
	if platformLocalOnlyKeys["bs_curboard"] {
		t.Fatal("bs_curboard must reach the server")
	}
}
