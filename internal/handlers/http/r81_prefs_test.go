package http

import (
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/bnursik/business_surgery_backend/internal/club"
	"github.com/gin-gonic/gin"
)

// R81: «Скрыть мероприятия» is the person's own switch (bs_prefs, every device
// of theirs), and the team's resident list gets each resident's Telegram id
// for the avatar; a resident's own seed never carries the others' ids.
func TestR81PrefsPersonalAndSeedTg(t *testing.T) {
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Set("userID", "tg:490685605")
	if s := platformScopeFor(c, "bs_prefs", ""); s != "user:tg:490685605" {
		t.Fatalf("bs_prefs scope = %q, want the person's own", s)
	}
	if !assistDeviceKeys["bs_prefs"] {
		t.Fatal("an assistant must not rewrite the resident's switches")
	}
	snap := &club.Snapshot{Residents: []club.Resident{{Name: "Альтаир", TgID: 490685605, Format: "Офлайн"}, {Name: "Асет", Format: "Онлайн"}}}
	rs := club.SeedResidents(snap)
	if len(rs) != 2 || rs[0].Tg != "490685605" || rs[1].Tg != "" {
		t.Fatalf("seed residents: %+v", rs)
	}
	seed := `{"RESIDENTS":[{"name":"Альтаир","tg":"490685605","format":"Офлайн"},{"name":"Асет","tg":"777100"}],"SDATA":{},"LIVE":{"source":"server"}}`
	out := residentSeed(seed, "Альтаир")
	if strings.Contains(out, `"tg":"777100"`) {
		t.Fatalf("a resident sees another resident's id: %s", out)
	}
}
