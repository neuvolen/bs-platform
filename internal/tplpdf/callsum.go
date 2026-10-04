package tplpdf

// «Саммари разбора». After a call of an online resident is processed
// (recording → transcript → the model's structured summary) the platform
// gives the resident this branded A4 PDF instead of the recording.
//
// R32e: the owner's structure, in this order:
//   1. Ситуация и цифры (the situation, the figures said, point A and B)
//   2. Диагнозы (a BS library diagnosis is marked «Библиотека BS»)
//   3. Корневая причина
//   4. Решения и инструменты (a BS library tool links to its template)
//   5. План действий на 10 дней (кто, что, срок)
//   6. Метрики контроля (сейчас → цель)
//   7. Домашнее задание
//   8. Следующая встреча
// Black cover band, the club's header and footer «Business Surgery ·
// bxclub.kz», page numbers. A draft (not yet published to the resident)
// carries «ЧЕРНОВИК» on the cover.

import (
	"bytes"
	"fmt"
	"strings"
	"time"

	"github.com/go-pdf/fpdf"
)

type CallTask struct {
	Text  string `json:"text"`
	Owner string `json:"owner"`
	Due   string `json:"due"`
}

type CallNumber struct {
	Label string `json:"label"`
	Value string `json:"value"`
}

// CallDiag: a diagnosis; Lib when its title is one of the BS library's.
type CallDiag struct {
	Title string `json:"title"`
	Why   string `json:"why"`
	Lib   bool   `json:"lib"`
}

// CallSolution: a decision; Tool is a BS library tool (URL: its template).
type CallSolution struct {
	Text    string `json:"text"`
	Tool    string `json:"tool"`
	ToolURL string `json:"toolUrl"`
}

// CallMetric: what is watched in the cycle.
type CallMetric struct {
	Name   string `json:"name"`
	Now    string `json:"now"`
	Target string `json:"target"`
}

// CallDoc: everything the summary shows. The R32d fields (Summary,
// Diagnoses, Decisions, Next) stand in when the structured ones are empty.
type CallDoc struct {
	Resident     string         `json:"resident"`
	Date         string         `json:"date"`
	Title        string         `json:"title"`
	Draft        bool           `json:"draft"`
	Participants []string       `json:"participants"`
	Situation    string         `json:"situation"`
	Numbers      []CallNumber   `json:"numbers"`
	PointA       string         `json:"pointA"`
	PointB       string         `json:"pointB"`
	DiagList     []CallDiag     `json:"diagList"`
	RootCause    string         `json:"rootCause"`
	Solutions    []CallSolution `json:"solutions"`
	Tasks        []CallTask     `json:"tasks"` // План действий на 10 дней: Owner = кто
	Metrics      []CallMetric   `json:"metrics"`
	Homework     []string       `json:"homework"`
	NextDate     string         `json:"nextDate"`
	NextAgenda   []string       `json:"nextAgenda"`

	// R32d shape (older calls)
	Summary   string   `json:"summary"`
	Topics    []string `json:"topics"`
	Problems  []string `json:"problems"`
	Diagnoses []string `json:"diagnoses"`
	Decisions []string `json:"decisions"`
	Next      []string `json:"next"`
	Questions []string `json:"questions"`
	Quotes    []string `json:"quotes"`
}

// CallFooter is printed on every page.
const CallFooter = "Business Surgery · bxclub.kz · саммари разбора · только для резидента и трекеров"

type cdoc struct {
	*gdoc
	c *CallDoc
}

// noDash: the club writes without the long dash.
func noDash(s string) string {
	return strings.ReplaceAll(strings.ReplaceAll(s, "—", "-"), "–", "-")
}

func cleanList(a []string) []string {
	out := []string{}
	for _, s := range a {
		if s = noDash(strings.TrimSpace(s)); s != "" {
			out = append(out, s)
		}
	}
	return out
}

func (c *CallDoc) clean() {
	fix := func(s *string) { *s = noDash(strings.TrimSpace(*s)) }
	for _, p := range []*string{&c.Resident, &c.Date, &c.Title, &c.Summary, &c.Situation, &c.PointA, &c.PointB, &c.RootCause, &c.NextDate} {
		fix(p)
	}
	c.Participants, c.Topics, c.Problems, c.Diagnoses = cleanList(c.Participants), cleanList(c.Topics), cleanList(c.Problems), cleanList(c.Diagnoses)
	c.Decisions, c.Next, c.Questions, c.Quotes = cleanList(c.Decisions), cleanList(c.Next), cleanList(c.Questions), cleanList(c.Quotes)
	c.Homework, c.NextAgenda = cleanList(c.Homework), cleanList(c.NextAgenda)
	// the R32d fields stand in for the structured ones
	if c.Situation == "" {
		c.Situation = c.Summary
	}
	if len(c.DiagList) == 0 {
		for _, d := range c.Diagnoses {
			c.DiagList = append(c.DiagList, CallDiag{Title: d})
		}
	}
	if len(c.Solutions) == 0 {
		for _, d := range c.Decisions {
			c.Solutions = append(c.Solutions, CallSolution{Text: d})
		}
	}
	if len(c.NextAgenda) == 0 && c.NextDate == "" {
		c.NextAgenda = c.Next
	}
	ds := []CallDiag{}
	for _, d := range c.DiagList {
		fix(&d.Title)
		fix(&d.Why)
		if d.Title != "" {
			ds = append(ds, d)
		}
	}
	c.DiagList = ds
	ss := []CallSolution{}
	for _, s := range c.Solutions {
		fix(&s.Text)
		fix(&s.Tool)
		s.ToolURL = strings.TrimSpace(s.ToolURL)
		if s.Text != "" || s.Tool != "" {
			ss = append(ss, s)
		}
	}
	c.Solutions = ss
	ts := []CallTask{}
	for _, t := range c.Tasks {
		fix(&t.Text)
		fix(&t.Owner)
		fix(&t.Due)
		if t.Text != "" {
			ts = append(ts, t)
		}
	}
	c.Tasks = ts
	ns := []CallNumber{}
	for _, n := range c.Numbers {
		fix(&n.Label)
		fix(&n.Value)
		if n.Value != "" {
			ns = append(ns, n)
		}
	}
	c.Numbers = ns
	ms := []CallMetric{}
	for _, m := range c.Metrics {
		fix(&m.Name)
		fix(&m.Now)
		fix(&m.Target)
		if m.Name != "" {
			ms = append(ms, m)
		}
	}
	c.Metrics = ms
}

// RenderCallSummary builds the PDF of one call's summary.
func RenderCallSummary(c *CallDoc) ([]byte, error) {
	if c == nil {
		return nil, fmt.Errorf("tplpdf: empty call")
	}
	c.clean()
	if c.Situation == "" && len(c.Tasks) == 0 && len(c.Solutions) == 0 && len(c.DiagList) == 0 && len(c.Problems) == 0 && c.RootCause == "" {
		return nil, fmt.Errorf("tplpdf: empty call")
	}
	p := fpdf.New("P", "mm", "A4", "")
	p.SetMargins(mL, topNext, mL)
	p.SetAutoPageBreak(false, 0)
	p.SetCellMargin(0)
	p.SetCompression(true)
	p.AddUTF8FontFromBytes("r", "", fontRegular)
	p.AddUTF8FontFromBytes("s", "", fontSemi)
	p.AddUTF8FontFromBytes("x", "", fontHeavy)
	p.RegisterImageOptionsReader("lw", fpdf.ImageOptions{ImageType: "PNG"}, bytes.NewReader(logoWhite))
	p.RegisterImageOptionsReader("lb", fpdf.ImageOptions{ImageType: "PNG"}, bytes.NewReader(logoBlack))
	title := "Саммари разбора"
	if c.Resident != "" {
		title += " · " + c.Resident
	}
	if c.Date != "" {
		title += " · " + c.Date
	}
	p.SetTitle(title+" · Business Surgery", true)
	p.SetAuthor("Business Surgery", true)
	p.SetSubject("Итоги разбора бизнеса: ситуация, диагнозы, решения, план на 10 дней · bxclub.kz", true)
	p.SetCreator("Business Surgery · bxclub.kz", true)
	p.SetProducer("Business Surgery", true)
	p.SetKeywords("Business Surgery, bxclub.kz, разбор, саммари", true)
	now := time.Now().UTC()
	p.SetCreationDate(now)
	p.SetModificationDate(now)
	p.AliasNbPages("{nb}")
	d := &cdoc{gdoc: &gdoc{doc: &doc{p: p}}, c: c}
	p.SetHeaderFuncMode(func() {
		if p.PageNo() == 1 {
			return
		}
		h := 6.0
		p.ImageOptions("lb", mL, 10, h*logoAR, h, false, fpdf.ImageOptions{}, 0, "")
		d.font("s", 7.5)
		d.color(cMute)
		p.SetXY(mL, 11.2)
		hd := "Саммари разбора"
		if c.Resident != "" {
			hd += " · " + clip(c.Resident, 40)
		}
		if c.Date != "" {
			hd += " · " + c.Date
		}
		if c.Draft {
			hd = "ЧЕРНОВИК · " + hd
		}
		p.CellFormat(cW, 4, hd, "", 0, "R", false, 0, "")
		d.draw(cHair, 0.25)
		p.Line(mL, 19, pageW-mL, 19)
	}, false)
	p.SetFooterFunc(func() {
		d.draw(cHair, 0.25)
		p.Line(mL, 283, pageW-mL, 283)
		d.font("r", 7)
		d.color(cMute)
		p.SetXY(mL, 285)
		p.CellFormat(cW-30, 4, CallFooter, "", 0, "L", false, 0, "")
		d.font("s", 7)
		p.SetXY(pageW-mL-30, 285)
		p.CellFormat(30, 4, fmt.Sprintf("стр. %d из {nb}", p.PageNo()), "", 0, "R", false, 0, "")
	})
	p.AddPage()
	d.cover()
	d.situation()
	d.diagnoses()
	d.rootCause()
	d.solutions()
	d.tasks()
	d.metrics()
	if len(c.Homework) > 0 {
		d.sec("Домашнее задание", "К следующей встрече", 14)
		d.checks(c.Homework)
	}
	d.nextMeeting()
	if err := p.Error(); err != nil {
		return nil, err
	}
	var buf bytes.Buffer
	if err := p.Output(&buf); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func (d *cdoc) cover() {
	p, c := d.p, d.c
	head := c.Title
	if head == "" {
		head = "Итоги разбора"
	}
	d.font("x", 19)
	tl := d.lines(head, cW-24)
	bandH := 52.0 + float64(len(tl))*8
	d.fill(cInk)
	p.Rect(0, 0, pageW, bandH, "F")
	p.ClipRect(0, 0, pageW, bandH, false)
	cx, cy := pageW-24.0, bandH*0.58
	for i, r := range []float64{46, 33, 21} {
		v := 44 - i*6
		d.draw(rgb{v, v, v}, 0.3)
		p.Circle(cx, cy, r, "D")
	}
	p.ClipEnd()
	lh := 11.0
	p.ImageOptions("lw", mL, 13, lh*logoAR, lh, false, fpdf.ImageOptions{}, 0, "")
	d.font("s", 6.8)
	d.color(cSoft)
	p.SetXY(pageW-mL-90, 14.5)
	p.CellFormat(90, 3.5, "БИЗНЕС-ТРЕКИНГ · РАЗБОР", "", 0, "R", false, 0, "")
	d.font("s", 8)
	d.color(cWhite)
	p.SetXY(pageW-mL-90, 19)
	p.CellFormat(90, 4, "Business Surgery · bxclub.kz", "", 0, "R", false, 0, "")
	pill := "САММАРИ РАЗБОРА"
	if c.Resident != "" {
		pill += " · " + strings.ToUpper(clip(c.Resident, 40))
	}
	d.font("s", 6.8)
	pw := p.GetStringWidth(pill) + 8
	d.draw(cWhite, 0.3)
	p.RoundedRect(mL, 31, pw, 5.6, 2.8, "1234", "D")
	d.color(cWhite)
	p.SetXY(mL, 31)
	p.CellFormat(pw, 5.6, pill, "", 0, "C", false, 0, "")
	if c.Draft {
		dw := p.GetStringWidth("ЧЕРНОВИК") + 8
		d.fill(cWhite)
		p.RoundedRect(mL+pw+3, 31, dw, 5.6, 2.8, "1234", "F")
		d.color(cInk)
		p.SetXY(mL+pw+3, 31)
		p.CellFormat(dw, 5.6, "ЧЕРНОВИК", "", 0, "C", false, 0, "")
	}
	d.font("x", 19)
	d.color(cWhite)
	y := 40.0
	for _, l := range tl {
		p.SetXY(mL-0.3, y)
		p.CellFormat(cW, 8, l, "", 0, "L", false, 0, "")
		y += 8
	}
	var meta []string
	if c.Date != "" {
		meta = append(meta, c.Date)
	}
	if len(c.Participants) > 0 {
		meta = append(meta, "Участники: "+strings.Join(c.Participants, ", "))
	}
	d.font("s", 8.4)
	d.color(rgb{170, 170, 170})
	ml := d.lines(strings.Join(meta, " · "), cW-24)
	for i, l := range ml {
		if i > 1 {
			break
		}
		p.SetXY(mL, y+1+float64(i)*4.5)
		p.CellFormat(cW, 4.5, l, "", 0, "L", false, 0, "")
	}
	p.SetY(bandH + 7)
}

// label: a small caps line above a block.
func (d *cdoc) label(s string) {
	d.font("s", 6.8)
	d.color(cMute)
	d.p.SetXY(mL, d.p.GetY())
	d.p.CellFormat(cW, 4, s, "", 0, "L", false, 0, "")
	d.p.SetY(d.p.GetY() + 5)
}

// situation: 1. the text, the figures, point A and B.
func (d *cdoc) situation() {
	c := d.c
	if c.Situation == "" && len(c.Numbers) == 0 && c.PointA == "" && c.PointB == "" {
		return
	}
	d.sec("Ситуация и цифры", "С чем пришли на разбор и что прозвучало в цифрах", 18)
	if c.Situation != "" {
		d.para(c.Situation, "r", 10, 5.2, cText, mL, cW)
		d.p.SetY(d.p.GetY() + 4)
	}
	if len(c.Problems) > 0 && len(c.DiagList) == 0 {
		d.label("ЧТО МЕШАЕТ")
		d.bullets(c.Problems, mL, cW)
		d.p.SetY(d.p.GetY() + 2)
	}
	d.numbers()
	d.points()
}

// numbers: the figures that were said, as tiles three in a row.
func (d *cdoc) numbers() {
	ns := d.c.Numbers
	if len(ns) == 0 {
		return
	}
	if len(ns) > 9 {
		ns = ns[:9]
	}
	p := d.p
	gap := 4.0
	tw := (cW - 2*gap) / 3
	for i := 0; i < len(ns); i += 3 {
		row := ns[i:minInt(i+3, len(ns))]
		h := 0.0
		for _, n := range row {
			d.font("x", 14)
			vh := float64(len(d.lines(n.Value, tw-8))) * 6.4
			d.font("r", 8)
			lh := float64(len(d.lines(n.Label, tw-8))) * 3.9
			h = maxf(h, 5+vh+1+lh+4)
		}
		d.need(h + 2)
		y := p.GetY()
		for j, n := range row {
			x := mL + float64(j)*(tw+gap)
			d.fill(cFill)
			p.RoundedRect(x, y, tw, h, 2.2, "1234", "F")
			d.font("x", 14)
			d.color(cInk)
			yy := d.text(x+4, y+4.5, tw-8, 6.4, n.Value, "L")
			d.font("r", 8)
			d.color(cMute)
			d.text(x+4, yy+1, tw-8, 3.9, n.Label, "L")
		}
		p.SetY(y + h + gap)
	}
	p.SetY(p.GetY() + 1)
}

// points: where the resident is and where they go.
func (d *cdoc) points() {
	c := d.c
	if c.PointA == "" && c.PointB == "" {
		return
	}
	p := d.p
	gap := 6.0
	cw := (cW - gap) / 2
	d.font("r", 9.2)
	la, lb := d.lines(orDash(c.PointA), cw-10), d.lines(orDash(c.PointB), cw-10)
	h := 12 + float64(maxInt(len(la), len(lb)))*4.7 + 3
	d.need(h + 3)
	y := p.GetY()
	for i, it := range []struct{ k, v string }{{"ТОЧКА А · СЕЙЧАС", c.PointA}, {"ТОЧКА Б · ЦЕЛЬ", c.PointB}} {
		x := mL + float64(i)*(cw+gap)
		if i == 1 {
			d.fill(cInk)
			p.RoundedRect(x, y, cw, h, 2.4, "1234", "F")
		} else {
			d.draw(cLine, 0.3)
			p.RoundedRect(x, y, cw, h, 2.4, "1234", "D")
		}
		d.font("s", 6.8)
		if i == 1 {
			d.color(cSoft)
		} else {
			d.color(cMute)
		}
		p.SetXY(x+5, y+4.5)
		p.CellFormat(cw-10, 3.8, it.k, "", 0, "L", false, 0, "")
		d.font("r", 9.2)
		if i == 1 {
			d.color(cWhite)
		} else {
			d.color(cText)
		}
		d.text(x+5, y+10, cw-10, 4.7, orDash(it.v), "L")
	}
	p.SetY(y + h + 4)
}

// diagnoses: 2. one card per diagnosis, a library one marked.
func (d *cdoc) diagnoses() {
	ds := d.c.DiagList
	if len(ds) == 0 {
		return
	}
	d.sec("Диагнозы", "Что на самом деле болит в бизнесе", 20)
	p := d.p
	for i, g := range ds {
		tag := ""
		if g.Lib {
			tag = "БИБЛИОТЕКА BS"
		}
		d.font("s", 6.4)
		tw := 0.0
		if tag != "" {
			tw = p.GetStringWidth(tag) + 6
		}
		d.font("s", 10.5)
		tl := d.lines(g.Title, cW-16-tw-4)
		d.font("r", 9)
		wl := []string{}
		if g.Why != "" {
			wl = d.lines(g.Why, cW-16)
		}
		h := 5 + float64(len(tl))*5.2 + float64(len(wl))*4.5 + 4
		if len(wl) > 0 {
			h += 1
		}
		d.need(h + 3)
		y := p.GetY()
		d.draw(cLine, 0.3)
		p.RoundedRect(mL, y, cW, h, 2.2, "1234", "D")
		d.fill(cInk)
		p.Rect(mL, y+2.2, 1.2, h-4.4, "F")
		d.font("x", 9)
		d.color(cSoft)
		p.SetXY(mL+4, y+4.6)
		p.CellFormat(8, 4.4, fmt.Sprintf("%02d", i+1), "", 0, "L", false, 0, "")
		d.font("s", 10.5)
		d.color(cInk)
		yy := d.text(mL+12, y+4.2, cW-16-tw-4, 5.2, g.Title, "L")
		if tag != "" {
			d.font("s", 6.4)
			d.draw(cInk, 0.3)
			p.RoundedRect(mL+cW-4-tw, y+4.4, tw, 4.8, 2.4, "1234", "D")
			d.color(cInk)
			p.SetXY(mL+cW-4-tw, y+4.4)
			p.CellFormat(tw, 4.8, tag, "", 0, "C", false, 0, "")
		}
		if len(wl) > 0 {
			d.font("r", 9)
			d.color(cMute)
			d.text(mL+12, yy+1, cW-16, 4.5, g.Why, "L")
		}
		p.SetY(y + h + 3)
	}
	p.SetY(p.GetY() + 1)
}

// rootCause: 3. the one cause under the diagnoses, on black.
func (d *cdoc) rootCause() {
	rc := d.c.RootCause
	if rc == "" {
		return
	}
	d.sec("Корневая причина", "", 18)
	p := d.p
	d.font("s", 11)
	ls := d.lines(rc, cW-16)
	h := float64(len(ls))*5.8 + 14
	d.need(h + 3)
	y := p.GetY()
	d.fill(cInk)
	p.RoundedRect(mL, y, cW, h, 2.6, "1234", "F")
	d.font("s", 6.8)
	d.color(cSoft)
	p.SetXY(mL+8, y+4.5)
	p.CellFormat(cW-16, 3.8, "ПОЧЕМУ ЭТО ПРОИСХОДИТ", "", 0, "L", false, 0, "")
	d.font("s", 11)
	d.color(cWhite)
	d.text(mL+8, y+9.5, cW-16, 5.8, rc, "L")
	p.SetY(y + h + 5)
}

// solutions: 4. what was decided; a library tool links to its template.
func (d *cdoc) solutions() {
	ss := d.c.Solutions
	if len(ss) == 0 {
		return
	}
	d.sec("Решения и инструменты", "О чём договорились и чем это делать", 16)
	p := d.p
	for _, s := range ss {
		text := s.Text
		if text == "" {
			text = s.Tool
		}
		d.font("r", 9.6)
		ls := d.lines(text, cW-6)
		h := float64(len(ls))*4.8 + 1.5
		if s.Tool != "" {
			h += 7
		}
		d.need(h + 2)
		y := p.GetY()
		d.fill(cInk)
		p.Rect(mL+0.4, y+1.6, 1.8, 1.8, "F")
		d.color(cText)
		yy := d.text(mL+5, y, cW-6, 4.8, text, "L")
		if s.Tool != "" {
			lbl := "Инструмент BS: " + s.Tool
			if s.ToolURL != "" {
				lbl += "  ·  шаблон PDF"
			}
			d.font("s", 7.6)
			w := minf(p.GetStringWidth(lbl)+8, cW-6)
			d.fill(cFill)
			p.RoundedRect(mL+5, yy+1, w, 5.2, 2.6, "1234", "F")
			d.color(cInk)
			p.SetXY(mL+5, yy+1)
			p.CellFormat(w, 5.2, lbl, "", 0, "C", false, 0, "")
			if s.ToolURL != "" {
				p.LinkString(mL+5, yy+1, w, 5.2, s.ToolURL)
			}
		}
		p.SetY(y + h + 2)
	}
	p.SetY(p.GetY() + 2)
}

func minf(a, b float64) float64 {
	if a < b {
		return a
	}
	return b
}

func orDash(s string) string {
	if strings.TrimSpace(s) == "" {
		return "-"
	}
	return s
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

// tasks: 5. the 10-day plan: a box to tick, who, what, by when.
func (d *cdoc) tasks() {
	ts := d.c.Tasks
	if len(ts) == 0 {
		return
	}
	d.sec("План действий на 10 дней", "Кто, что и к какому сроку. Отчёт по плану в конце цикла", 22)
	p := d.p
	wBox, wOwn, wDue := 8.0, 36.0, 22.0
	wText := cW - wBox - wOwn - wDue
	head := func() {
		y := p.GetY()
		d.fill(cInk)
		p.Rect(mL, y, cW, 7, "F")
		d.font("s", 7)
		d.color(cWhite)
		p.SetXY(mL+wBox, y+1.6)
		p.CellFormat(wOwn-3, 4, "КТО", "", 0, "L", false, 0, "")
		p.SetXY(mL+wBox+wOwn, y+1.6)
		p.CellFormat(wText-3, 4, "ЧТО", "", 0, "L", false, 0, "")
		p.SetXY(mL+wBox+wOwn+wText, y+1.6)
		p.CellFormat(wDue-3, 4, "СРОК", "", 0, "L", false, 0, "")
		p.SetY(y + 7)
	}
	d.need(16)
	head()
	for i, t := range ts {
		d.font("r", 9)
		tl := d.lines(t.Text, wText-4)
		d.font("s", 8.4)
		ol := d.lines(orDash(t.Owner), wOwn-4)
		h := float64(maxInt(len(tl), len(ol)))*4.5 + 5
		if p.GetY()+h > bottom {
			p.AddPage()
			p.SetY(topNext)
			head()
		}
		y := p.GetY()
		if i%2 == 1 {
			d.fill(cSample)
			p.Rect(mL, y, cW, h, "F")
		}
		d.draw(cLine, 0.35)
		p.Rect(mL+2.2, y+2.6, 3.4, 3.4, "D")
		d.font("s", 8.4)
		d.color(cInk)
		d.text(mL+wBox, y+2.4, wOwn-4, 4.5, orDash(t.Owner), "L")
		d.font("r", 9)
		d.color(cText)
		d.text(mL+wBox+wOwn, y+2.4, wText-4, 4.5, t.Text, "L")
		d.font("s", 8.4)
		d.color(cInk)
		d.text(mL+wBox+wOwn+wText, y+2.4, wDue-3, 4.5, orDash(t.Due), "L")
		d.draw(cHair, 0.25)
		p.Line(mL, y+h, mL+cW, y+h)
		p.SetY(y + h)
	}
	p.SetY(p.GetY() + 5)
}

// metrics: 6. what is watched: now → target.
func (d *cdoc) metrics() {
	ms := d.c.Metrics
	if len(ms) == 0 {
		return
	}
	d.sec("Метрики контроля", "По ним видно, что план работает", 20)
	p := d.p
	wNow, wT := 40.0, 40.0
	wName := cW - wNow - wT
	y := p.GetY()
	d.font("s", 7)
	d.color(cMute)
	for _, h := range []struct {
		x, w float64
		t    string
	}{{mL, wName, "МЕТРИКА"}, {mL + wName, wNow, "СЕЙЧАС"}, {mL + wName + wNow, wT, "ЦЕЛЬ"}} {
		p.SetXY(h.x, y)
		p.CellFormat(h.w, 4, h.t, "", 0, "L", false, 0, "")
	}
	d.draw(cInk, 0.4)
	p.Line(mL, y+5.5, mL+cW, y+5.5)
	p.SetY(y + 6.5)
	for _, m := range ms {
		d.font("s", 9.4)
		nl := d.lines(m.Name, wName-4)
		d.font("r", 9.2)
		al := d.lines(orDash(m.Now), wNow-4)
		d.font("x", 9.6)
		bl := d.lines(orDash(m.Target), wT-4)
		h := float64(maxInt(len(nl), maxInt(len(al), len(bl))))*4.6 + 4.4
		d.need(h)
		y := p.GetY()
		d.font("s", 9.4)
		d.color(cInk)
		d.text(mL, y+2.2, wName-4, 4.6, m.Name, "L")
		d.font("r", 9.2)
		d.color(cText)
		d.text(mL+wName, y+2.2, wNow-4, 4.6, orDash(m.Now), "L")
		d.font("x", 9.6)
		d.color(cInk)
		d.text(mL+wName+wNow, y+2.2, wT-4, 4.6, orDash(m.Target), "L")
		d.draw(cHair, 0.25)
		p.Line(mL, y+h, mL+cW, y+h)
		p.SetY(y + h)
	}
	p.SetY(p.GetY() + 5)
}

// checks: a list with boxes to tick.
func (d *cdoc) checks(items []string) {
	p := d.p
	for _, it := range items {
		d.font("r", 9.6)
		ls := d.lines(it, cW-9)
		h := float64(len(ls))*4.8 + 2.4
		d.need(h)
		y := p.GetY()
		d.draw(cInk, 0.35)
		p.Rect(mL+0.4, y+0.9, 3.4, 3.4, "D")
		d.color(cText)
		d.text(mL+7, y+0.4, cW-9, 4.8, it, "L")
		p.SetY(y + h)
	}
	p.SetY(p.GetY() + 3)
}

// nextMeeting: 8. the date and what is on the agenda.
func (d *cdoc) nextMeeting() {
	c := d.c
	if c.NextDate == "" && len(c.NextAgenda) == 0 {
		return
	}
	d.sec("Следующая встреча", "", 18)
	p := d.p
	wd := 0.0
	if c.NextDate != "" {
		wd = 52
	}
	d.font("r", 9.4)
	h := 10.0
	for _, a := range c.NextAgenda {
		h += float64(len(d.lines(a, cW-wd-14)))*4.7 + 1.4
	}
	if c.NextDate != "" {
		d.font("x", 15)
		h = maxf(h, 12+float64(len(d.lines(c.NextDate, wd-10)))*7)
	}
	h = maxf(h, 20)
	d.need(h + 3)
	y := p.GetY()
	d.draw(cInk, 0.45)
	p.RoundedRect(mL, y, cW, h, 2.6, "1234", "D")
	if c.NextDate != "" {
		d.fill(cInk)
		p.RoundedRect(mL, y, wd, h, 2.6, "14", "F")
		d.font("s", 6.8)
		d.color(cSoft)
		p.SetXY(mL+5, y+4.5)
		p.CellFormat(wd-10, 3.8, "КОГДА", "", 0, "L", false, 0, "")
		d.font("x", 15)
		d.color(cWhite)
		d.text(mL+5, y+9.5, wd-10, 7, c.NextDate, "L")
	}
	x := mL + wd + 7
	d.font("s", 6.8)
	d.color(cMute)
	p.SetXY(x, y+4.5)
	p.CellFormat(cW-wd-14, 3.8, "ЧТО ОБСУДИМ", "", 0, "L", false, 0, "")
	yy := y + 10
	if len(c.NextAgenda) == 0 {
		d.font("r", 9.4)
		d.color(cText)
		d.text(x, yy, cW-wd-14, 4.7, "Отчёт по плану на 10 дней и следующий шаг", "L")
	}
	for _, a := range c.NextAgenda {
		d.font("r", 9.4)
		d.fill(cInk)
		p.Circle(x+1, yy+2.2, 0.7, "F")
		d.color(cText)
		yy = d.text(x+4, yy, cW-wd-18, 4.7, a, "L") + 1.4
	}
	p.SetY(y + h + 4)
}
