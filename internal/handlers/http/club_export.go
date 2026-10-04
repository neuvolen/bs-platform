package http

import (
	"archive/zip"
	"bytes"
	"encoding/csv"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/bnursik/business_surgery_backend/internal/club"
	"github.com/gin-gonic/gin"
)

// Export of the club's data (R32): the sheet is no longer the backup, so the
// team downloads its own copy from the platform («Клуб → Экспорт»). One way:
// nothing comes back from these files.

type exportTable struct {
	Name string
	Head []string
	Rows [][]any // string, int64 or float64 per cell
}

func dmyOr(t *time.Time) string {
	if t == nil || t.IsZero() {
		return ""
	}
	return t.In(club.Almaty).Format("02.01.2006")
}

func yesNo(b bool) string {
	if b {
		return "Да"
	}
	return ""
}

// ExportTables builds the tables of an export: residents, payments, fines,
// meetings, reports; "all" adds the meeting log and every kept sheet.
func ExportTables(s *club.Snapshot, what string) []exportTable {
	var out []exportTable
	want := func(k string) bool { return what == "all" || what == k }
	if want("residents") {
		t := exportTable{Name: "Резиденты", Head: []string{"Имя", "Telegram ID", "Формат", "Тариф", "Встреч оплачено", "Встреч проведено",
			"Осталось встреч", "Оплачено (вход)", "Остаток (вход)", "Долг продление", "Штрафы", "Общий долг", "Бывший", "Исключение",
			"Команда", "Источник", "Дата входа", "Дата выхода", "Месяцев", "Партнёр", "Заметка", "Архив"}}
		debt := map[string]club.DebetRow{}
		for _, d := range club.Debet(s.Residents, s.Fines) {
			debt[d.Name] = d
		}
		for _, r := range s.Residents {
			d := debt[r.Name]
			tg := ""
			if r.TgID != 0 {
				tg = strconv.FormatInt(r.TgID, 10)
			}
			t.Rows = append(t.Rows, []any{r.Name, tg, r.Format, r.Tariff, r.Granted, r.Done, r.Granted - r.Done, r.PaidEntry, r.RestEntry,
				r.RenewDebt, d.FinesUnpaid, d.TotalDebt, yesNo(r.Former), yesNo(r.Exception), yesNo(r.Admin), r.Source,
				dmyOr(r.JoinedAt), dmyOr(r.LeftAt), r.Months, r.Partner, r.Note, yesNo(r.Archived)})
		}
		out = append(out, t)
	}
	if want("payments") {
		t := exportTable{Name: "ДДС", Head: []string{"Дата", "Приход", "Расход", "Статья дохода", "Резидент", "Статья расхода", "Учтено"}}
		for _, p := range s.Payments {
			d := p.Date
			t.Rows = append(t.Rows, []any{dmyOr(&d), p.Income, p.Expense, p.IncomeCat, p.Resident, p.ExpenseCat, yesNo(p.Applied)})
		}
		out = append(out, t)
	}
	if want("fines") {
		t := exportTable{Name: "Штрафы", Head: []string{"Резидент", "Тип", "Сумма", "Дата", "Статус"}}
		for _, f := range s.Fines {
			d := f.Date
			st := f.Status
			if st == "" {
				st = "Не оплатил"
				if f.Paid {
					st = "Оплатил"
				}
			}
			t.Rows = append(t.Rows, []any{f.Name, f.Type, f.Amount, dmyOr(&d), st})
		}
		out = append(out, t)
	}
	if want("meetings") {
		t := exportTable{Name: "Встречи", Head: []string{"Резидент", "Дата", "Время", "Место", "Ссылка", "Проведена"}}
		for _, m := range s.Meetings {
			d := m.Date
			t.Rows = append(t.Rows, []any{m.Resident, dmyOr(&d), m.Time, m.Place, m.Link, yesNo(m.Done)})
		}
		out = append(out, t)
	}
	if want("reports") {
		t := exportTable{Name: "Отчёты", Head: []string{"Дата", "Резидент", "Username", "Telegram ID", "Поздний", "Текст"}}
		for _, r := range s.Reports {
			tg := ""
			if r.TgUserID != 0 {
				tg = strconv.FormatInt(r.TgUserID, 10)
			}
			t.Rows = append(t.Rows, []any{r.At.In(club.Almaty).Format("02.01.2006 15:04"), r.Name, r.Username, tg, yesNo(r.Late), r.Text})
		}
		out = append(out, t)
	}
	if what == "all" {
		t := exportTable{Name: "Лог встреч", Head: []string{"Дата", "Резидент", "Время записи"}}
		for _, m := range s.MeetingLog {
			d := m.Date
			t.Rows = append(t.Rows, []any{dmyOr(&d), m.Resident, m.Time})
		}
		out = append(out, t)
		// Every sheet the server keeps as displayed (PL, the app's sections,
		// the script's archives), as it is.
		var names []string
		for n := range s.Raw {
			if n != club.SheetScriptProps && n != club.SheetDebet {
				names = append(names, n)
			}
		}
		sort.Strings(names)
		for _, n := range names {
			rows := s.Raw[n]
			t := exportTable{Name: n}
			for i, r := range rows {
				if i == 0 {
					t.Head = r
					continue
				}
				line := make([]any, len(r))
				for j, c := range r {
					line[j] = c
				}
				t.Rows = append(t.Rows, line)
			}
			out = append(out, t)
		}
	}
	return out
}

func cellText(v any) string {
	switch x := v.(type) {
	case string:
		return x
	case int64:
		return strconv.FormatInt(x, 10)
	case float64:
		return strconv.FormatFloat(x, 'f', -1, 64)
	}
	return fmt.Sprint(v)
}

// ExportCSV: UTF-8 with BOM and «;», as Excel in Russian opens it.
func ExportCSV(t exportTable) []byte {
	var b bytes.Buffer
	b.WriteString("\xef\xbb\xbf")
	w := csv.NewWriter(&b)
	w.Comma = ';'
	_ = w.Write(t.Head)
	for _, r := range t.Rows {
		line := make([]string, len(r))
		for i, c := range r {
			line[i] = cellText(c)
		}
		_ = w.Write(line)
	}
	w.Flush()
	return b.Bytes()
}

func xmlEsc(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch {
		case r == '&':
			b.WriteString("&amp;")
		case r == '<':
			b.WriteString("&lt;")
		case r == '>':
			b.WriteString("&gt;")
		case r == '"':
			b.WriteString("&quot;")
		case r == '\t' || r == '\n' || r == '\r' || r >= 0x20 && r != 0xFFFE && r != 0xFFFF:
			b.WriteRune(r)
		}
	}
	return b.String()
}

func colName(i int) string {
	s := ""
	for i++; i > 0; i = (i - 1) / 26 {
		s = string(rune('A'+(i-1)%26)) + s
	}
	return s
}

// sheetTitle: Excel allows 31 characters and no []:*?/\ in a sheet name.
func sheetTitle(n string, used map[string]bool) string {
	n = strings.Map(func(r rune) rune {
		if strings.ContainsRune(`[]:*?/\`, r) {
			return ' '
		}
		return r
	}, strings.TrimSpace(n))
	if n == "" {
		n = "Лист"
	}
	if r := []rune(n); len(r) > 31 {
		n = string(r[:31])
	}
	base, k := n, 2
	for used[strings.ToLower(n)] {
		suf := fmt.Sprintf(" %d", k)
		r := []rune(base)
		if len(r)+len(suf) > 31 {
			r = r[:31-len(suf)]
		}
		n = string(r) + suf
		k++
	}
	used[strings.ToLower(n)] = true
	return n
}

// ExportXLSX writes the tables as one workbook (a sheet per table).
func ExportXLSX(tables []exportTable) ([]byte, error) {
	var buf bytes.Buffer
	z := zip.NewWriter(&buf)
	put := func(name, body string) error {
		w, err := z.Create(name)
		if err != nil {
			return err
		}
		_, err = w.Write([]byte(body))
		return err
	}
	var ct, wb, rels strings.Builder
	ct.WriteString(`<?xml version="1.0" encoding="UTF-8" standalone="yes"?><Types xmlns="http://schemas.openxmlformats.org/package/2006/content-types">` +
		`<Default Extension="rels" ContentType="application/vnd.openxmlformats-package.relationships+xml"/><Default Extension="xml" ContentType="application/xml"/>` +
		`<Override PartName="/xl/workbook.xml" ContentType="application/vnd.openxmlformats-officedocument.spreadsheetml.sheet.main+xml"/>` +
		`<Override PartName="/xl/styles.xml" ContentType="application/vnd.openxmlformats-officedocument.spreadsheetml.styles+xml"/>`)
	wb.WriteString(`<?xml version="1.0" encoding="UTF-8" standalone="yes"?><workbook xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main" xmlns:r="http://schemas.openxmlformats.org/officeDocument/2006/relationships"><sheets>`)
	rels.WriteString(`<?xml version="1.0" encoding="UTF-8" standalone="yes"?><Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships">`)
	used := map[string]bool{}
	if len(tables) == 0 {
		tables = []exportTable{{Name: "Пусто"}}
	}
	for i, t := range tables {
		n := i + 1
		fmt.Fprintf(&ct, `<Override PartName="/xl/worksheets/sheet%d.xml" ContentType="application/vnd.openxmlformats-officedocument.spreadsheetml.worksheet+xml"/>`, n)
		fmt.Fprintf(&wb, `<sheet name="%s" sheetId="%d" r:id="rId%d"/>`, xmlEsc(sheetTitle(t.Name, used)), n, n)
		fmt.Fprintf(&rels, `<Relationship Id="rId%d" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/worksheet" Target="worksheets/sheet%d.xml"/>`, n, n)
		var sh strings.Builder
		sh.WriteString(`<?xml version="1.0" encoding="UTF-8" standalone="yes"?><worksheet xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main"><sheetViews><sheetView workbookViewId="0"><pane ySplit="1" topLeftCell="A2" activePane="bottomLeft" state="frozen"/></sheetView></sheetViews><sheetData>`)
		row := func(r int, cells []any, bold bool) {
			fmt.Fprintf(&sh, `<row r="%d">`, r)
			for j, c := range cells {
				ref := colName(j) + strconv.Itoa(r)
				st := ""
				if bold {
					st = ` s="1"`
				}
				switch x := c.(type) {
				case int64:
					fmt.Fprintf(&sh, `<c r="%s"%s><v>%d</v></c>`, ref, st, x)
				case float64:
					fmt.Fprintf(&sh, `<c r="%s"%s><v>%s</v></c>`, ref, st, strconv.FormatFloat(x, 'f', -1, 64))
				default:
					v := cellText(c)
					if v == "" {
						continue
					}
					fmt.Fprintf(&sh, `<c r="%s" t="inlineStr"%s><is><t xml:space="preserve">%s</t></is></c>`, ref, st, xmlEsc(v))
				}
			}
			sh.WriteString(`</row>`)
		}
		head := make([]any, len(t.Head))
		for j, h := range t.Head {
			head[j] = h
		}
		row(1, head, true)
		for k, r := range t.Rows {
			row(k+2, r, false)
		}
		sh.WriteString(`</sheetData></worksheet>`)
		if err := put(fmt.Sprintf("xl/worksheets/sheet%d.xml", n), sh.String()); err != nil {
			return nil, err
		}
	}
	ct.WriteString(`</Types>`)
	wb.WriteString(`</sheets></workbook>`)
	fmt.Fprintf(&rels, `<Relationship Id="rId%d" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/styles" Target="styles.xml"/></Relationships>`, len(tables)+1)
	for _, f := range [][2]string{
		{"[Content_Types].xml", ct.String()},
		{"_rels/.rels", `<?xml version="1.0" encoding="UTF-8" standalone="yes"?><Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships"><Relationship Id="rId1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/officeDocument" Target="xl/workbook.xml"/></Relationships>`},
		{"xl/workbook.xml", wb.String()},
		{"xl/_rels/workbook.xml.rels", rels.String()},
		{"xl/styles.xml", `<?xml version="1.0" encoding="UTF-8" standalone="yes"?><styleSheet xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main"><fonts count="2"><font><sz val="11"/><name val="Calibri"/></font><font><b/><sz val="11"/><name val="Calibri"/></font></fonts><fills count="2"><fill><patternFill patternType="none"/></fill><fill><patternFill patternType="gray125"/></fill></fills><borders count="1"><border><left/><right/><top/><bottom/><diagonal/></border></borders><cellStyleXfs count="1"><xf numFmtId="0" fontId="0" fillId="0" borderId="0"/></cellStyleXfs><cellXfs count="2"><xf numFmtId="0" fontId="0" fillId="0" borderId="0" xfId="0"/><xf numFmtId="0" fontId="1" fillId="0" borderId="0" xfId="0" applyFont="1"/></cellXfs><cellStyles count="1"><cellStyle name="Normal" xfId="0" builtinId="0"/></cellStyles></styleSheet>`},
	} {
		if err := put(f[0], f[1]); err != nil {
			return nil, err
		}
	}
	if err := z.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

var exportKinds = map[string]string{"residents": "rezidenty", "payments": "dds", "fines": "shtrafy", "meetings": "vstrechi", "reports": "otchety", "all": "bs-dannye"}

// DataExport godoc
// @Summary  Download the club's data (CSV or Excel)
// @Description  Team only. ?what=residents|payments|fines|meetings|reports|all, ?format=csv|xlsx (all: xlsx only, a sheet per table plus every kept sheet). A one-way copy: the backup instead of the Google Sheet.
// @Tags     club
// @Security BearerAuth
// @Router   /api/v1/club/data/export [get]
func (h *ClubHandler) DataExport(c *gin.Context) {
	what := strings.ToLower(strings.TrimSpace(c.DefaultQuery("what", "all")))
	format := strings.ToLower(strings.TrimSpace(c.DefaultQuery("format", "xlsx")))
	slug, ok := exportKinds[what]
	if !ok || (format != "csv" && format != "xlsx") {
		c.JSON(http.StatusBadRequest, gin.H{"error": "what: residents|payments|fines|meetings|reports|all, format: csv|xlsx"})
		return
	}
	if what == "all" {
		format = "xlsx"
	}
	s, err := h.repo.LoadBundle(c.Request.Context(), time.Time{})
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal server error"})
		return
	}
	tables := ExportTables(s, what)
	name := fmt.Sprintf("%s-%s.%s", slug, time.Now().In(club.Almaty).Format("2006-01-02"), format)
	c.Header("Content-Disposition", `attachment; filename="`+name+`"`)
	c.Header("Cache-Control", "no-store")
	if format == "csv" {
		c.Data(http.StatusOK, "text/csv; charset=utf-8", ExportCSV(tables[0]))
		return
	}
	b, err := ExportXLSX(tables)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal server error"})
		return
	}
	c.Data(http.StatusOK, "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet", b)
}
