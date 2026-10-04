package club

import (
	"context"
	"sync"
)

// R32d: the Google Sheet is at most a read-only copy. Whether the dormant
// script keeps that copy (an hourly export from the server) is decided on
// the server (handlers/http/sheet_owner.go: an admin's switch on the
// platform, SHEET_EXPORT, or by default for the first week after the
// cutover). The bot's control call tells the script through ScriptMode.

var (
	hookMu      sync.RWMutex
	exportHook  func(ctx context.Context) bool
	requestHook func(ctx context.Context) bool
)

// SetSheetHooks installs who decides the export and whether the server
// asks the sheet for a reconciliation copy (nil: none).
func SetSheetHooks(export, reconcile func(ctx context.Context) bool) func() {
	hookMu.Lock()
	pe, pr := exportHook, requestHook
	exportHook, requestHook = export, reconcile
	hookMu.Unlock()
	return func() {
		hookMu.Lock()
		exportHook, requestHook = pe, pr
		hookMu.Unlock()
	}
}

// ExportOn: the sheet gets the server's hourly copy. Never in legacy mode
// (the sheet is the master again there).
func ExportOn(ctx context.Context) bool {
	if SheetLegacy() {
		return false
	}
	hookMu.RLock()
	f := exportHook
	hookMu.RUnlock()
	if f == nil {
		return SheetMode() == SheetModeMirror
	}
	return f(ctx)
}

// ReconcileWanted: the server waits for one copy of the sheet to compare.
func ReconcileWanted(ctx context.Context) bool {
	if SheetLegacy() {
		return false
	}
	hookMu.RLock()
	f := requestHook
	hookMu.RUnlock()
	return f != nil && f(ctx)
}

// ScriptMode is the mode the script is told: legacy, mirror (dormant, takes
// the hourly copy) or off (dormant, nothing).
func ScriptMode(ctx context.Context) string {
	if SheetLegacy() {
		return SheetModeLegacy
	}
	if ExportOn(ctx) {
		return SheetModeMirror
	}
	return SheetModeOff
}
