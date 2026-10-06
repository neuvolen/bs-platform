package http

import (
	"context"
	"crypto/sha1"
	"encoding/csv"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/bnursik/business_surgery_backend/internal/club"
	"github.com/gin-gonic/gin"
)

// R47: импорт экспорта заявок Tilda (CSV).
//
// История заявок сайта через API Tilda не отдаётся (API Tilda: проекты и
// страницы); заявки лежат в разделе сайта «Заявки» (Экспорт CSV) или в Tilda
// CRM (Экспорт / Расширенный экспорт). Владелец скачивает CSV и загружает его
// в «Импорт базы»: платформа узнаёт формат Tilda по колонкам (formname,
// tranid, sent, referer, utm_…) и отдаёт файл сюда.
//
// Колонки сопоставляются сами (английские имена Tilda и русские Tilda CRM),
// дубли отсеиваются по телефону, @нику, почте и номеру заявки (с CRM и
// внутри файла), источник «Сайт (Tilda): <форма>», дата заявки сохраняется
// (date, created, tildaAt). Уже живущие в CRM карточки не перезаписываются:
// им дописывается форма и отметка «заявка с сайта». dry: только просмотр.

type tildaCol struct {
	H string `json:"h"`
	F string `json:"f"`
}

// TildaImportReport: what the file holds and what the import did.
type TildaImportReport struct {
	File    string              `json:"file"`
	Rows    int                 `json:"rows"`
	Fresh   int                 `json:"fresh"`
	Dup     int                 `json:"dup"`     // already in the CRM
	DupFile int                 `json:"dupFile"` // repeated inside the file
	Skip    int                 `json:"skip"`    // no phone, Telegram or e-mail
	Updated int                 `json:"updated"` // CRM cards marked as site leads
	Forms   map[string]int      `json:"forms"`
	From    string              `json:"from,omitempty"`
	To      string              `json:"to,omitempty"`
	Columns []tildaCol          `json:"columns"`
	Sample  []map[string]string `json:"sample"`
	Tilda   bool                `json:"tilda"` // the columns look like Tilda's export
	Done    bool                `json:"done"`
	At      string              `json:"at,omitempty"`
}

// tildaHead: a column of the export → a field.
func tildaHead(h string) string {
	k := strings.ToLower(strings.TrimSpace(strings.Trim(h, "\ufeff\"")))
	k = strings.NewReplacer("ё", "е", "_", " ", "-", " ").Replace(k)
	k = strings.Join(strings.Fields(k), " ")
	switch {
	case k == "":
		return ""
	case strings.HasPrefix(k, "utm "):
		return "utm_" + strings.TrimPrefix(k, "utm ")
	case k == "formname" || k == "form name" || k == "название формы" || k == "форма" || k == "имя формы":
		return "form"
	case k == "formid" || k == "form id" || k == "id формы":
		return "formid"
	case k == "tranid" || k == "requestid" || k == "request id" || k == "id заявки" || k == "номер заявки" || k == "id лида" || k == "lead id":
		return "tranid"
	case k == "sent" || k == "date" || k == "дата" || k == "created" || k == "дата создания" || k == "дата заявки" || k == "время" || k == "дата и время" || k == "date created":
		return "date"
	case k == "referer" || k == "referrer" || k == "page" || k == "url" || k == "страница" || k == "адрес страницы" || k == "источник страницы":
		return "page"
	case k == "cookies" || k == "tildautm" || k == "ip" || k == "user agent" || k == "useragent" || k == "ma name" || k == "ma email" || k == "stage" || k == "этап" || k == "ответственный":
		return "skip"
	case strings.Contains(k, "telegram") || strings.Contains(k, "телеграм") || k == "tg" || k == "тг":
		return "tg"
	case strings.Contains(k, "phone") || strings.Contains(k, "телефон") || k == "tel" || strings.Contains(k, "whatsapp") || strings.Contains(k, "номер"):
		return "phone"
	case strings.Contains(k, "mail") || strings.Contains(k, "почта"):
		return "email"
	case k == "name" || k == "имя" || k == "fio" || k == "фио" || strings.Contains(k, "your name") || k == "имя и фамилия" || k == "контакт" || k == "клиент" || k == "full name":
		return "name"
	case strings.Contains(k, "ниша") || strings.Contains(k, "niche") || strings.Contains(k, "сфера") || strings.Contains(k, "бизнес") || strings.Contains(k, "business"):
		return "niche"
	case strings.Contains(k, "оборот") || strings.Contains(k, "выручк") || strings.Contains(k, "revenue"):
		return "revenue"
	case strings.Contains(k, "comment") || strings.Contains(k, "коммент") || strings.Contains(k, "message") || strings.Contains(k, "сообщени") || strings.Contains(k, "вопрос") || strings.Contains(k, "запрос") || k == "textarea":
		return "comment"
	case k == "source" || k == "источник":
		return "source"
	}
	return "extra"
}

// ParseTildaCSV reads the export (comma, semicolon or tab; UTF-8 or Windows-1251 decoded by the platform).
func ParseTildaCSV(text string) ([][]string, error) {
	text = strings.TrimPrefix(text, "\ufeff")
	first := text
	if i := strings.IndexAny(text, "\r\n"); i >= 0 {
		first = text[:i]
	}
	r := csv.NewReader(strings.NewReader(text))
	r.LazyQuotes, r.FieldsPerRecord = true, -1
	switch {
	case strings.Count(first, "\t") > 0:
		r.Comma = '\t'
	case strings.Count(first, ";") > strings.Count(first, ","):
		r.Comma = ';'
	}
	rows, err := r.ReadAll()
	if err != nil && len(rows) == 0 {
		return nil, err
	}
	var out [][]string
	for _, row := range rows {
		keep := false
		for i := range row {
			row[i] = strings.TrimSpace(row[i])
			if row[i] != "" {
				keep = true
			}
		}
		if keep {
			out = append(out, row)
		}
	}
	return out, nil
}

// LooksTilda: the header has Tilda's own columns.
func LooksTilda(head []string) bool {
	n := 0
	for _, h := range head {
		switch tildaHead(h) {
		case "form", "formid", "tranid", "page":
			n++
		}
		if strings.HasPrefix(strings.ToLower(h), "utm_") {
			n++
		}
	}
	return n >= 2
}

type tildaRow struct {
	f     map[string]string
	extra []string
	at    time.Time
}

func tildaRows(rows [][]string) ([]tildaRow, []tildaCol) {
	if len(rows) == 0 {
		return nil, nil
	}
	head := rows[0]
	cols := make([]tildaCol, len(head))
	for i, h := range head {
		cols[i] = tildaCol{H: h, F: tildaHead(h)}
	}
	var out []tildaRow
	for _, r := range rows[1:] {
		t := tildaRow{f: map[string]string{}}
		for i, v := range r {
			if i >= len(cols) || v == "" {
				continue
			}
			switch f := cols[i].F; f {
			case "", "skip":
			case "extra":
				if len(t.extra) < 6 && utf8.RuneCountInString(v) <= 200 {
					t.extra = append(t.extra, cols[i].H+": "+v)
				}
			case "comment":
				if t.f[f] != "" {
					t.f[f] += "\n" + v
				} else {
					t.f[f] = v
				}
			default:
				if t.f[f] == "" {
					t.f[f] = v
				}
			}
		}
		if d, ok := club.Date(t.f["date"]); ok {
			t.at = d
		}
		out = append(out, t)
	}
	return out, cols
}

func tildaKeys(phone, tg, email, tran string) []string {
	var k []string
	if d := phoneDigits(phone); len(d) >= 10 {
		k = append(k, "ph:"+d[len(d)-10:])
	}
	if t := strings.ToLower(strings.TrimPrefix(tildaTg(tg), "@")); t != "" {
		k = append(k, "@"+t)
	}
	if e := strings.ToLower(strings.TrimSpace(email)); strings.Contains(e, "@") {
		k = append(k, "em:"+e)
	}
	if tran != "" {
		k = append(k, "tr:"+tran)
	}
	return k
}

// ImportTilda merges the export into the CRM document (dry: counts only).
func ImportTilda(crm map[string]any, rows [][]string, file string, now time.Time, dry bool) TildaImportReport {
	rep := TildaImportReport{File: file, Forms: map[string]int{}, Sample: []map[string]string{}}
	if len(rows) > 0 {
		rep.Tilda = LooksTilda(rows[0])
	}
	list, cols := tildaRows(rows)
	rep.Columns = cols
	rep.Rows = len(list)
	leads := asList(crm["leads"])
	byKey := map[string]map[string]any{}
	for _, x := range leads {
		m, _ := x.(map[string]any)
		if m == nil {
			continue
		}
		tran := ""
		if id := pStr(m, "id"); strings.HasPrefix(id, "site") && len(id) > 4 {
			tran = id[4:]
		}
		for _, k := range tildaKeys(pStr(m, "phone"), pStr(m, "tg"), pStr(m, "email"), tran) {
			byKey[k] = m
		}
	}
	deleted := map[string]bool{}
	for _, x := range asList(crm["deleted"]) {
		s := strings.ToLower(fmt.Sprint(x))
		deleted[s] = true
		if strings.HasPrefix(s, "ph:") {
			if d := phoneDigits(s[3:]); len(d) >= 10 {
				deleted["ph:"+d[len(d)-10:]] = true
			}
		}
	}
	// the oldest first: the first request of a person keeps its date
	sort.SliceStable(list, func(i, j int) bool {
		if list[i].at.IsZero() || list[j].at.IsZero() {
			return !list[i].at.IsZero() && list[j].at.IsZero()
		}
		return list[i].at.Before(list[j].at)
	})
	inFile := map[string]bool{}
	var fresh []any
	for _, r := range list {
		f := r.f
		form := strings.TrimSpace(f["form"])
		keys := tildaKeys(f["phone"], f["tg"], f["email"], strings.TrimSpace(f["tranid"]))
		st := "new"
		var hit map[string]any
		contact := false
		for _, k := range keys {
			if !strings.HasPrefix(k, "tr:") {
				contact = true
			}
		}
		switch {
		case !contact:
			st = "skip"
			rep.Skip++
		default:
			for _, k := range keys {
				if inFile[k] {
					st = "dupFile"
				}
			}
			if st == "new" {
				for _, k := range keys {
					if m := byKey[k]; m != nil {
						hit, st = m, "dup"
						break
					}
					if deleted[k] {
						st = "deleted"
					}
				}
			}
		}
		for _, k := range keys {
			inFile[k] = true
		}
		if !r.at.IsZero() && st != "skip" {
			d := r.at.In(almaty).Format("02.01.2006")
			if rep.From == "" || r.at.Before(tildaDay(rep.From)) {
				rep.From = d
			}
			if rep.To == "" || r.at.After(tildaDay(rep.To).Add(24*time.Hour-time.Second)) {
				rep.To = d
			}
		}
		if st != "skip" {
			rep.Forms[firstNonEmptyS(form, "без названия")]++
		}
		if len(rep.Sample) < 10 {
			date := ""
			if !r.at.IsZero() {
				date = r.at.In(almaty).Format("02.01.2006 15:04")
			}
			rep.Sample = append(rep.Sample, map[string]string{"name": f["name"], "phone": f["phone"], "email": f["email"], "tg": f["tg"], "form": form, "date": date, "st": st})
		}
		src := "Сайт (Tilda)"
		if form != "" {
			src += ": " + form
		}
		at := now
		if !r.at.IsZero() {
			at = r.at
		}
		switch st {
		case "dupFile", "deleted":
			rep.DupFile++
		case "dup":
			rep.Dup++
			if !dry && hit != nil && pStr(hit, "tildaAt") == "" {
				if form != "" && pStr(hit, "form") == "" {
					hit["form"] = form
				}
				hit["tildaAt"] = at.UTC().Format(time.RFC3339)
				addLog(hit, now, "Найден в экспорте заявок Tilda ("+file+"): заявка "+at.In(almaty).Format("02.01.2006")+firstNonEmptyS(prefixed(" · форма «", form, "»")))
				rep.Updated++
			} else if dry && hit != nil && pStr(hit, "tildaAt") == "" {
				rep.Updated++
			}
		case "new":
			rep.Fresh++
			if dry {
				continue
			}
			tran := strings.Map(func(c rune) rune {
				if (c >= '0' && c <= '9') || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') {
					return c
				}
				return -1
			}, f["tranid"])
			id := "site" + tran
			if tran == "" {
				h := sha1.Sum([]byte(strings.Join(keys, "|") + "|" + f["date"]))
				id = "tl" + hex.EncodeToString(h[:5])
			}
			name := strings.TrimSpace(f["name"])
			if name == "" {
				name = firstNonEmptyS(f["phone"], f["email"], tildaTg(f["tg"]))
			}
			var note []string
			if c := strings.TrimSpace(f["comment"]); c != "" {
				note = append(note, c)
			}
			if f["email"] != "" {
				note = append(note, "email: "+f["email"])
			}
			if f["page"] != "" {
				note = append(note, "страница: "+f["page"])
			}
			note = append(note, r.extra...)
			phone := tildaPhone(f["phone"])
			lead := map[string]any{"id": id, "col": "new", "name": name, "phone": phone, "tg": tildaTg(f["tg"]), "source": src,
				"niche": f["niche"], "note": strings.Join(note, "\n"), "sum": "", "date": at.In(almaty).Format("02.01.2006"),
				"created": at.UTC().Format(time.RFC3339), "tildaAt": at.UTC().Format(time.RFC3339), "funnel": "site", "imp": "tilda", "impFile": file}
			if form != "" {
				lead["form"] = form
			}
			if f["email"] != "" {
				lead["email"] = f["email"]
			}
			if f["revenue"] != "" {
				lead["revenue"] = f["revenue"]
			}
			u := map[string]any{}
			for k, v := range f {
				if strings.HasPrefix(k, "utm_") && v != "" {
					u[k] = v
				}
			}
			if len(u) > 0 {
				lead["utm"] = u
			}
			addLog(lead, now, "Загружен из экспорта заявок Tilda ("+file+"): заявка "+at.In(almaty).Format("02.01.2006 15:04")+prefixed(", форма «", form, "»"))
			for _, k := range keys {
				byKey[k] = lead
			}
			fresh = append(fresh, lead)
		}
	}
	if !dry && len(fresh) > 0 {
		crm["leads"] = append(leads, fresh...)
	}
	if !dry {
		rep.Done = true
		rep.At = now.UTC().Format(time.RFC3339)
	}
	return rep
}

func prefixed(pre, s, post string) string {
	if s == "" {
		return ""
	}
	return pre + s + post
}

func tildaDay(d string) time.Time {
	t, _ := time.ParseInLocation("02.01.2006", d, almaty)
	return t
}

// TildaImport: POST /api/v1/platform/crm/tilda-import {csv | rows, file, dry}.
func (p *Partners) TildaImport(c *gin.Context) {
	if !teamOnly(c) {
		return
	}
	var in struct {
		CSV  string     `json:"csv"`
		Rows [][]string `json:"rows"`
		File string     `json:"file"`
		Dry  bool       `json:"dry"`
	}
	if err := c.ShouldBindJSON(&in); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "bad_request", "message": "Файл не прочитан"})
		return
	}
	rows := in.Rows
	if len(rows) == 0 && in.CSV != "" {
		var err error
		if rows, err = ParseTildaCSV(in.CSV); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "bad_csv", "message": "CSV не прочитан: " + err.Error()})
			return
		}
	}
	if len(rows) < 2 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "empty", "message": "В файле нет заявок"})
		return
	}
	file := strings.TrimSpace(in.File)
	if file == "" {
		file = "Экспорт Tilda " + p.now().In(almaty).Format("02.01.2006")
	}
	rep, err := p.ImportTildaRows(c.Request.Context(), rows, file, in.Dry)
	if err != nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "store_failed", "message": "CRM не сохранилась, попробуйте ещё раз"})
		return
	}
	c.JSON(http.StatusOK, rep)
}

// ImportTildaRows: the import over bs_crm; then the segments.
func (p *Partners) ImportTildaRows(ctx context.Context, rows [][]string, file string, dry bool) (TildaImportReport, error) {
	var rep TildaImportReport
	if dry {
		crm := map[string]any{}
		if d, err := p.docs.GetDoc(ctx, "club", "bs_crm"); err == nil && d != nil && !d.Deleted {
			_ = json.Unmarshal([]byte(d.Value), &crm)
		}
		return ImportTilda(crm, rows, file, p.now(), true), nil
	}
	err := p.f().mutate(ctx, "bs_crm", func(crm map[string]any) bool {
		rep = ImportTilda(crm, rows, file, p.now(), false)
		if rep.Fresh == 0 && rep.Updated == 0 {
			return false
		}
		hist := asList(crm["tildaImports"])
		hist = append(hist, map[string]any{"file": file, "at": rep.At, "rows": rep.Rows, "added": rep.Fresh, "dup": rep.Dup, "updated": rep.Updated, "from": rep.From, "to": rep.To})
		if len(hist) > 20 {
			hist = hist[len(hist)-20:]
		}
		crm["tildaImports"] = hist
		return true
	})
	if err != nil {
		return rep, err
	}
	if _, err := p.SegmentNow(ctx, false); err != nil {
		return rep, nil
	}
	return rep, nil
}
