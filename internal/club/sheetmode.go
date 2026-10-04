package club

import (
	"os"
	"strings"
	"sync"
)

// The Google Sheet after the cutover (R32): the server is the only source of
// truth. One switch, SHEET_MODE:
//
//	off     (default) nothing calls the Apps Script and nothing waits for it:
//	        the bot, the app, the club writes and the timed jobs run on the
//	        server only; the script is dormant (its triggers removed).
//	mirror  the same, and the sheet keeps a one-way copy: the dormant script
//	        pulls ДДС and PL from the server once an hour (bsPullFromServer).
//	legacy  emergency only: the pre-cutover behaviour (the server relays the
//	        bot to the script, writes go on to the sheet, the hourly import).
const (
	SheetModeOff    = "off"
	SheetModeMirror = "mirror"
	SheetModeLegacy = "legacy"
)

var (
	sheetMu       sync.RWMutex
	sheetOverride string
)

// SheetMode is the effective SHEET_MODE.
func SheetMode() string {
	sheetMu.RLock()
	o := sheetOverride
	sheetMu.RUnlock()
	if o != "" {
		return o
	}
	switch v := strings.ToLower(strings.TrimSpace(os.Getenv("SHEET_MODE"))); v {
	case SheetModeMirror, SheetModeLegacy:
		return v
	default:
		return SheetModeOff
	}
}

// SheetLegacy: the script still takes part (relay, writes, imports).
func SheetLegacy() bool { return SheetMode() == SheetModeLegacy }

// SetSheetMode overrides SHEET_MODE (tests); "" goes back to the environment.
// It returns a function that restores the previous override.
func SetSheetMode(m string) func() {
	sheetMu.Lock()
	prev := sheetOverride
	sheetOverride = m
	sheetMu.Unlock()
	return func() {
		sheetMu.Lock()
		sheetOverride = prev
		sheetMu.Unlock()
	}
}
