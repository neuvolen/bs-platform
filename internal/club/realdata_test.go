package club

import (
	"encoding/json"
	"os"
	"testing"
)

// TestRealSheet checks the server rules against a real export of the Google
// Sheet. Club data never goes into the repository: the test runs only when
// BS_SHEET_JSON points to an export file on the developer's machine.
func TestRealSheet(t *testing.T) {
	path := os.Getenv("BS_SHEET_JSON")
	if path == "" {
		t.Skip("BS_SHEET_JSON not set")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var s Sheets
	if err := json.Unmarshal(raw, &s); err != nil {
		t.Fatal(err)
	}
	snap, warn, err := Parse(s)
	if err != nil {
		t.Fatal(err)
	}
	active, former := 0, 0
	for _, r := range snap.Residents {
		if r.Former {
			former++
		} else {
			active++
		}
	}
	t.Logf("резидентов %d (+%d бывших), ДДС %d, штрафов %d, встреч %d, отчётов %d, лог встреч %d, текстов %d",
		active, former, len(snap.Payments), len(snap.Fines), len(snap.Meetings), len(snap.Reports), len(snap.MeetingLog), len(snap.Settings))
	for _, w := range warn {
		t.Logf("предупреждение: %s", w)
	}
	month := 9
	if m := os.Getenv("BS_SHEET_MONTH"); m != "" {
		json.Unmarshal([]byte(m), &month) //nolint:errcheck
	}
	debet, pl, checked := Check(snap, month)
	t.Logf("сверено чисел: %d", checked)
	for _, m := range debet {
		t.Errorf("дебет расходится: %s: таблица %d, сервер %d", m.Where, m.Sheet, m.Server)
	}
	for _, m := range pl {
		t.Errorf("PL расходится: %s: таблица %d, сервер %d", m.Where, m.Sheet, m.Server)
	}
	built := BuildPL(snap.Payments, snap.PL, snap.PL.Year, month)
	for _, u := range built.Unknown {
		t.Logf("без статьи: строка %d %s %s %d «%s» → %q", u.Row, u.Date, u.Kind, u.Amount, u.Cat, u.Placed)
	}
}
