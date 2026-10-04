package http

import (
	"net/http"
	"time"

	"github.com/bnursik/business_surgery_backend/internal/club"
	"github.com/gin-gonic/gin"
)

// R32d: the read-only copy the dormant script (v41+) writes into the sheet
// once an hour while the export is on: ДДС and PL as before, plus copy tabs
// of residents, fines, meetings and reports (the script writes them into
// tabs «Копия: …», never into the sheets the formulas read).

// sheetCopyReports: the copy tab keeps the latest reports only.
const sheetCopyReports = 3000

// SheetCopyTable is one copy tab.
type SheetCopyTable struct {
	Name string     `json:"name"`
	Head []string   `json:"head"`
	Rows [][]string `json:"rows"`
}

// SheetCopyTables are the copy tabs of a snapshot.
func SheetCopyTables(s *club.Snapshot) []SheetCopyTable {
	if n := len(s.Reports); n > sheetCopyReports {
		s.Reports = s.Reports[n-sheetCopyReports:]
	}
	var out []SheetCopyTable
	for _, k := range []string{"residents", "fines", "meetings", "reports"} {
		for _, t := range ExportTables(s, k) {
			ct := SheetCopyTable{Name: t.Name, Head: t.Head, Rows: make([][]string, 0, len(t.Rows))}
			for _, r := range t.Rows {
				line := make([]string, len(r))
				for i, c := range r {
					line[i] = cellText(c)
				}
				ct.Rows = append(ct.Rows, line)
			}
			out = append(out, ct)
		}
	}
	return out
}

func (h *ClubHandler) exportWithTables(c *gin.Context) {
	s, err := h.repo.LoadBundle(c.Request.Context(), time.Time{})
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal server error"})
		return
	}
	pays := make([]ExportPayment, 0, len(s.Payments))
	for _, p := range s.Payments {
		pays = append(pays, ExportPayment{Date: p.Date.In(club.Almaty).Format("02.01.2006"), Income: p.Income, Expense: p.Expense,
			IncomeCat: p.IncomeCat, Resident: p.Resident, ExpenseCat: p.ExpenseCat, Applied: p.Applied})
	}
	year := club.Today().Year()
	if s.PL != nil && s.PL.Year != 0 {
		year = s.PL.Year
	}
	upTo := 12
	if year == club.Today().Year() {
		upTo = int(club.Today().Month())
	}
	pl := ExportPL(s.PL, club.BuildPL(s.Payments, s.PL, year, upTo))
	c.JSON(http.StatusOK, gin.H{"payments": pays, "pl": pl, "tables": SheetCopyTables(s),
		"at": time.Now().In(club.Almaty).Format("02.01.2006 15:04")})
}
