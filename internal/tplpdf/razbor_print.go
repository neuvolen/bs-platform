package tplpdf

// R69: «Печать разбора». The board of a разбор as a one-sheet A4 checklist for
// a printer: white paper, black ink only (no filled bands, no colour), thin
// lines and empty boxes to tick by hand. The platform builds the data from
// the board (client, date, точка А and Б, diagnoses with their symptoms, the
// 10 day plan with owners and deadlines, tools with one line «как применить»,
// the next meeting) and the same structure is printed by the browser from
// the live preview (print CSS), so both look alike.

import (
	"bytes"
	"fmt"
	"strings"
	"time"

	"github.com/go-pdf/fpdf"
)

// PrintDiag: a diagnosis with what shows it (the symptoms said on the board).
type PrintDiag struct {
	Title    string   `json:"title"`
	Symptoms []string `json:"symptoms"`
}

// PrintTask: one line of the 10 day plan.
type PrintTask struct {
	Text  string `json:"text"`
	Owner string `json:"owner"`
	Due   string `json:"due"`
	Done  bool   `json:"done"`
}

// PrintTool: a tool and how to apply it, in one line.
type PrintTool struct {
	Title string `json:"title"`
	How   string `json:"how"`
	Diag  string `json:"diag"`
}

// PrintDoc: everything the printed разбор shows.
type PrintDoc struct {
	Resident string      `json:"resident"`
	Business string      `json:"business"`
	Date     string      `json:"date"`
	Cycle    int         `json:"cycle"`
	Tracker  string      `json:"tracker"`
	PointA   string      `json:"pointA"`
	PointB   string      `json:"pointB"`
	Diags    []PrintDiag `json:"diags"`
	Tasks    []PrintTask `json:"tasks"`
	Tools    []PrintTool `json:"tools"`
	Next     string      `json:"next"`
	Notes    []string    `json:"notes"`
}

// PrintFooter is printed on every page.
const PrintFooter = "Business Surgery · bxclub.kz · разбор бизнеса"

func cleanS(s string, n int) string { return clip(noDash(strings.Join(strings.Fields(s), " ")), n) }

// Clean trims, removes the long dash and limits sizes (the data comes from a browser).
func (d *PrintDoc) Clean() {
	d.Resident, d.Business, d.Date = cleanS(d.Resident, 80), cleanS(d.Business, 120), cleanS(d.Date, 20)
	d.Tracker, d.PointA, d.PointB, d.Next = cleanS(d.Tracker, 60), cleanS(d.PointA, 600), cleanS(d.PointB, 600), cleanS(d.Next, 160)
	if d.Cycle < 0 || d.Cycle > 999 {
		d.Cycle = 0
	}
	ds := []PrintDiag{}
	for _, g := range d.Diags {
		if g.Title = cleanS(g.Title, 160); g.Title == "" || len(ds) >= 24 {
			continue
		}
		ss := []string{}
		for _, s := range g.Symptoms {
			if s = cleanS(s, 300); s != "" && len(ss) < 8 {
				ss = append(ss, s)
			}
		}
		g.Symptoms = ss
		ds = append(ds, g)
	}
	d.Diags = ds
	ts := []PrintTask{}
	for _, t := range d.Tasks {
		if t.Text = cleanS(t.Text, 300); t.Text == "" || len(ts) >= 60 {
			continue
		}
		t.Owner, t.Due = cleanS(t.Owner, 60), cleanS(t.Due, 20)
		ts = append(ts, t)
	}
	d.Tasks = ts
	tl := []PrintTool{}
	for _, t := range d.Tools {
		if t.Title = cleanS(t.Title, 160); t.Title == "" || len(tl) >= 40 {
			continue
		}
		t.How, t.Diag = cleanS(t.How, 300), cleanS(t.Diag, 160)
		tl = append(tl, t)
	}
	d.Tools = tl
	ns := []string{}
	for _, s := range d.Notes {
		if s = cleanS(s, 400); s != "" && len(ns) < 30 {
			ns = append(ns, s)
		}
	}
	d.Notes = ns
}

// Empty: nothing on the board worth printing.
func (d *PrintDoc) Empty() bool {
	return d.PointA == "" && d.PointB == "" && len(d.Diags) == 0 && len(d.Tasks) == 0 && len(d.Tools) == 0 && len(d.Notes) == 0
}

// FileName: «Разбор Имя ДД.ММ.ГГГГ.pdf» (without the characters a file name cannot hold).
func (d *PrintDoc) FileName() string {
	n := "Разбор"
	if d.Resident != "" {
		n += " " + d.Resident
	}
	if d.Date != "" {
		n += " " + d.Date
	}
	n = strings.Map(func(r rune) rune {
		if strings.ContainsRune("\\/:*?\"<>|«»", r) {
			return -1
		}
		return r
	}, n)
	return strings.Join(strings.Fields(n), " ") + ".pdf"
}

type pdoc struct {
	*doc
	c *PrintDoc
	n int
}

// RenderRazborPrint builds the printable checklist of a разбор.
func RenderRazborPrint(c *PrintDoc) ([]byte, error) {
	if c == nil {
		return nil, fmt.Errorf("tplpdf: empty razbor")
	}
	c.Clean()
	if c.Empty() {
		return nil, fmt.Errorf("tplpdf: empty razbor")
	}
	p := fpdf.New("P", "mm", "A4", "")
	p.SetMargins(mL, topNext, mL)
	p.SetAutoPageBreak(false, 0)
	p.SetCellMargin(0)
	p.SetCompression(true)
	p.AddUTF8FontFromBytes("r", "", fontRegular)
	p.AddUTF8FontFromBytes("s", "", fontSemi)
	p.AddUTF8FontFromBytes("x", "", fontHeavy)
	p.RegisterImageOptionsReader("lb", fpdf.ImageOptions{ImageType: "PNG"}, bytes.NewReader(logoBlack))
	title := strings.TrimSuffix(c.FileName(), ".pdf")
	p.SetTitle(title+" · Business Surgery", true)
	p.SetAuthor("Business Surgery", true)
	p.SetSubject("Чек-лист разбора: точка А и Б, диагнозы, план на 10 дней, инструменты · bxclub.kz", true)
	p.SetCreator("Business Surgery · bxclub.kz", true)
	p.SetProducer("Business Surgery", true)
	now := time.Now().UTC()
	p.SetCreationDate(now)
	p.SetModificationDate(now)
	p.AliasNbPages("{nb}")
	d := &pdoc{doc: &doc{p: p}, c: c}
	p.SetHeaderFuncMode(func() {
		if p.PageNo() == 1 {
			return
		}
		h := 5.5
		p.ImageOptions("lb", mL, 10, h*logoAR, h, false, fpdf.ImageOptions{}, 0, "")
		d.font("s", 7.5)
		d.color(cMute)
		p.SetXY(mL, 11)
		p.CellFormat(cW, 4, title, "", 0, "R", false, 0, "")
		d.draw(cLine, 0.25)
		p.Line(mL, 18.5, pageW-mL, 18.5)
	}, false)
	p.SetFooterFunc(func() {
		d.draw(cLine, 0.25)
		p.Line(mL, 283, pageW-mL, 283)
		d.font("r", 7)
		d.color(cMute)
		p.SetXY(mL, 285)
		p.CellFormat(cW-30, 4, PrintFooter, "", 0, "L", false, 0, "")
		d.font("s", 7)
		p.SetXY(pageW-mL-30, 285)
		p.CellFormat(30, 4, fmt.Sprintf("стр. %d из {nb}", p.PageNo()), "", 0, "R", false, 0, "")
	})
	p.AddPage()
	d.head()
	d.points()
	d.diags()
	d.plan()
	d.tools()
	d.next()
	d.notes()
	d.signs()
	if err := p.Error(); err != nil {
		return nil, err
	}
	var buf bytes.Buffer
	if err := p.Output(&buf); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func (d *pdoc) head() {
	p, c := d.p, d.c
	h := 6.6
	p.ImageOptions("lb", mL, 13, h*logoAR, h, false, fpdf.ImageOptions{}, 0, "")
	d.font("s", 7.4)
	d.color(cMute)
	p.SetXY(mL, 14.6)
	p.CellFormat(cW, 4, "ЧЕК-ЛИСТ РАЗБОРА", "", 0, "R", false, 0, "")
	y := 27.0
	d.font("x", 21)
	d.color(cInk)
	name := c.Resident
	if name == "" {
		name = "Резидент"
	}
	y = d.text(mL, y, cW, 9, "Разбор: "+name, "L")
	meta := []string{}
	if c.Business != "" {
		meta = append(meta, c.Business)
	}
	if c.Date != "" {
		meta = append(meta, c.Date)
	}
	if c.Cycle > 0 {
		meta = append(meta, fmt.Sprintf("цикл %d", c.Cycle))
	}
	if c.Tracker != "" {
		meta = append(meta, "трекер "+c.Tracker)
	}
	if len(meta) > 0 {
		d.font("s", 9.4)
		d.color(cMute)
		y = d.text(mL, y+1, cW, 5, strings.Join(meta, " · "), "L")
	}
	d.draw(cInk, 0.6)
	p.Line(mL, y+3, pageW-mL, y+3)
	p.SetY(y + 8)
}

func (d *pdoc) sec(title, sub string, keep float64) {
	p := d.p
	d.n++
	d.need(14 + keep)
	y := p.GetY() + 1
	d.draw(cInk, 0.45)
	p.RoundedRect(mL, y, 6.4, 6.4, 1.4, "1234", "D")
	d.font("x", 8)
	d.color(cInk)
	p.SetXY(mL, y)
	p.CellFormat(6.4, 6.4, fmt.Sprintf("%d", d.n), "", 0, "C", false, 0, "")
	d.font("x", 12.5)
	yy := d.text(mL+9.5, y+0.4, cW-9.5, 5.8, title, "L")
	if sub != "" {
		d.font("r", 8.2)
		d.color(cMute)
		yy = d.text(mL+9.5, yy+0.2, cW-9.5, 4.2, sub, "L")
	}
	p.SetY(maxf(yy, y+6.4) + 3)
}

func (d *pdoc) points() {
	c, p := d.c, d.p
	if c.PointA == "" && c.PointB == "" {
		return
	}
	d.sec("Точка А и точка Б", "Где бизнес сейчас и куда идём за цикл", 24)
	gap := 6.0
	w := (cW - gap) / 2
	d.font("r", 9.4)
	la, lb := d.lines(orDash(c.PointA), w-10), d.lines(orDash(c.PointB), w-10)
	h := float64(maxInt(len(la), len(lb)))*4.8 + 14
	d.need(h + 4)
	y := p.GetY()
	for i, t := range []struct{ k, v string }{{"ТОЧКА А · СЕЙЧАС", c.PointA}, {"ТОЧКА Б · ЦЕЛЬ", c.PointB}} {
		x := mL + float64(i)*(w+gap)
		d.draw(cInk, 0.4)
		p.RoundedRect(x, y, w, h, 2.4, "1234", "D")
		d.font("s", 7)
		d.color(cMute)
		p.SetXY(x+5, y+4)
		p.CellFormat(w-10, 3.6, t.k, "", 0, "L", false, 0, "")
		d.font("s", 9.6)
		d.color(cInk)
		d.text(x+5, y+9, w-10, 4.8, orDash(t.v), "L")
	}
	p.SetY(y + h + 6)
}

func (d *pdoc) diags() {
	c, p := d.c, d.p
	if len(c.Diags) == 0 {
		return
	}
	d.sec("Диагнозы", "Что болит и по каким признакам это видно", 16)
	for i, g := range c.Diags {
		d.font("x", 10.4)
		tl := d.lines(g.Title, cW-12)
		d.font("r", 9)
		h := float64(len(tl))*5.2 + 2
		for _, s := range g.Symptoms {
			h += float64(len(d.lines(s, cW-17)))*4.5 + 0.8
		}
		d.need(minf(h+3, 60))
		y := p.GetY()
		d.font("x", 10.4)
		d.color(cInk)
		p.SetXY(mL, y)
		p.CellFormat(8, 5.2, fmt.Sprintf("%d.", i+1), "", 0, "L", false, 0, "")
		y = d.text(mL+8, y, cW-12, 5.2, g.Title, "L")
		y += 0.8
		for _, s := range g.Symptoms {
			d.font("r", 9)
			ls := d.lines(s, cW-17)
			if y+float64(len(ls))*4.5 > bottom {
				p.AddPage()
				y = topNext
			}
			d.color(cMute)
			p.SetXY(mL+9, y)
			p.CellFormat(4, 4.5, "·", "", 0, "L", false, 0, "")
			d.color(cText)
			y = d.text(mL+13, y, cW-17, 4.5, s, "L") + 0.8
		}
		d.draw(cHair, 0.25)
		p.Line(mL+8, y+1.4, pageW-mL, y+1.4)
		p.SetY(y + 3.6)
	}
	p.SetY(p.GetY() + 2)
}

func (d *pdoc) plan() {
	c, p := d.c, d.p
	d.sec("План на 10 дней", "Отмечайте сделанное галочкой. Пустые строки для того, что добавим на встрече", 24)
	wBox, wOwn, wDue := 9.0, 34.0, 24.0
	wText := cW - wBox - wOwn - wDue
	head := func() {
		y := p.GetY()
		d.font("s", 7)
		d.color(cMute)
		p.SetXY(mL+wBox, y)
		p.CellFormat(wText-3, 4, "ЗАДАЧА", "", 0, "L", false, 0, "")
		p.SetXY(mL+wBox+wText, y)
		p.CellFormat(wOwn-3, 4, "КТО", "", 0, "L", false, 0, "")
		p.SetXY(mL+wBox+wText+wOwn, y)
		p.CellFormat(wDue, 4, "СРОК", "", 0, "L", false, 0, "")
		d.draw(cInk, 0.45)
		p.Line(mL, y+5.4, pageW-mL, y+5.4)
		p.SetY(y + 5.4)
	}
	d.need(18)
	head()
	row := func(t PrintTask) {
		d.font("r", 9.4)
		tl := d.lines(t.Text, wText-4)
		d.font("s", 8.6)
		ol := d.lines(t.Owner, wOwn-4)
		h := float64(maxInt(len(tl), len(ol)))*4.6 + 4.6
		if h < 9 {
			h = 9
		}
		if p.GetY()+h > bottom {
			p.AddPage()
			p.SetY(topNext)
			head()
		}
		y := p.GetY()
		d.draw(cInk, 0.4)
		p.Rect(mL+1.2, y+2.5, 4, 4, "D")
		if t.Done {
			p.Line(mL+1.9, y+4.6, mL+3, y+5.8)
			p.Line(mL+3, y+5.8, mL+4.6, y+3.1)
		}
		d.font("r", 9.4)
		d.color(cText)
		d.text(mL+wBox, y+2.3, wText-4, 4.6, t.Text, "L")
		d.font("s", 8.6)
		d.color(cInk)
		d.text(mL+wBox+wText, y+2.3, wOwn-4, 4.6, t.Owner, "L")
		d.text(mL+wBox+wText+wOwn, y+2.3, wDue, 4.6, t.Due, "L")
		d.draw(cLine, 0.25)
		p.Line(mL, y+h, pageW-mL, y+h)
		p.SetY(y + h)
	}
	for _, t := range c.Tasks {
		row(t)
	}
	blank := 3
	if len(c.Tasks) == 0 {
		blank = 5
	}
	for i := 0; i < blank; i++ {
		row(PrintTask{})
	}
	p.SetY(p.GetY() + 6)
}

func (d *pdoc) tools() {
	c, p := d.c, d.p
	if len(c.Tools) == 0 {
		return
	}
	d.sec("Инструменты", "Чем лечим и как применить", 16)
	for _, t := range c.Tools {
		d.font("s", 10)
		tl := d.lines(t.Title, cW-9)
		d.font("r", 9)
		how := t.How
		if how != "" {
			how = "Как применить: " + how
		}
		hl := []string{}
		if how != "" {
			hl = d.lines(how, cW-9)
		}
		dl := []string{}
		if t.Diag != "" {
			d.font("r", 8)
			dl = d.lines("Лечит: "+t.Diag, cW-9)
		}
		h := float64(len(tl))*5 + float64(len(hl))*4.5 + float64(len(dl))*4 + 3
		d.need(h)
		y := p.GetY()
		d.draw(cInk, 0.4)
		p.Rect(mL+0.6, y+0.8, 3.6, 3.6, "D")
		d.font("s", 10)
		d.color(cInk)
		y = d.text(mL+7, y, cW-9, 5, t.Title, "L")
		if len(hl) > 0 {
			d.font("r", 9)
			d.color(cText)
			y = d.text(mL+7, y+0.2, cW-9, 4.5, how, "L")
		}
		if len(dl) > 0 {
			d.font("r", 8)
			d.color(cMute)
			y = d.text(mL+7, y+0.2, cW-9, 4, "Лечит: "+t.Diag, "L")
		}
		p.SetY(y + 3)
	}
	p.SetY(p.GetY() + 2)
}

func (d *pdoc) next() {
	c, p := d.c, d.p
	d.sec("Следующая встреча", "", 16)
	d.need(14)
	y := p.GetY()
	d.draw(cInk, 0.4)
	p.RoundedRect(mL, y, cW, 12, 2.4, "1234", "D")
	d.font("s", 7)
	d.color(cMute)
	p.SetXY(mL+5, y+4.2)
	p.CellFormat(24, 3.6, "КОГДА", "", 0, "L", false, 0, "")
	if c.Next != "" {
		d.font("x", 11)
		d.color(cInk)
		p.SetXY(mL+26, y+3.4)
		p.CellFormat(cW-31, 5.4, clip(c.Next, 90), "", 0, "L", false, 0, "")
	} else {
		d.draw(cLine, 0.3)
		p.Line(mL+26, y+8, pageW-mL-5, y+8)
	}
	p.SetY(y + 18)
}

func (d *pdoc) notes() {
	c, p := d.c, d.p
	d.sec("Заметки", "", 14)
	for _, s := range c.Notes {
		d.font("r", 9)
		ls := d.lines(s, cW-7)
		h := float64(len(ls))*4.5 + 1.6
		d.need(h)
		y := p.GetY()
		d.color(cMute)
		p.SetXY(mL+1, y)
		p.CellFormat(4, 4.5, "·", "", 0, "L", false, 0, "")
		d.color(cText)
		p.SetY(d.text(mL+5, y, cW-7, 4.5, s, "L") + 1.6)
	}
	lines := 4
	if len(c.Notes) > 0 {
		lines = 3
	}
	d.need(float64(lines)*8 + 2)
	y := p.GetY() + 2
	d.draw(cLine, 0.3)
	for i := 0; i < lines; i++ {
		p.Line(mL, y+float64(i+1)*8, pageW-mL, y+float64(i+1)*8)
	}
	p.SetY(y + float64(lines)*8 + 8)
}

func (d *pdoc) signs() {
	p := d.p
	d.need(22)
	y := p.GetY() + 4
	w := (cW - 12) / 2
	for i, k := range []string{"Резидент", "Трекер"} {
		x := mL + float64(i)*(w+12)
		d.draw(cInk, 0.4)
		p.Line(x, y+10, x+w, y+10)
		d.font("s", 7.4)
		d.color(cMute)
		p.SetXY(x, y+11.6)
		p.CellFormat(w, 3.8, strings.ToUpper(k)+" · ПОДПИСЬ", "", 0, "L", false, 0, "")
	}
	p.SetY(y + 18)
}
