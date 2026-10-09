package http

import (
	"os"
	"testing"

	"github.com/bnursik/business_surgery_backend/internal/club"
)

// The older tests describe the bot and the app with the sheet in the loop
// (SHEET_MODE=legacy); the cutover tests switch to off themselves.
func TestMain(m *testing.M) {
	club.SetSheetMode(club.SheetModeLegacy)
	// R70: no test reads the real Telegram channels; the channel tests set their own
	tgSources = nil
	os.Exit(m.Run())
}
