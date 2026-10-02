// Package tplpdf renders the printable worksheet of a library tool (R25) as an
// A4 PDF in the Business Surgery style: black cover band with the logo, the
// organ and the title, numbered blocks with empty lines and cells to fill by
// hand, and the club footer on every page. Pure Go (go-pdf/fpdf) with the
// Manrope font embedded, so it works on Railway without a browser.
package tplpdf

import (
	"bytes"
	_ "embed"
	"fmt"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/bnursik/business_surgery_backend/internal/content"
	"github.com/go-pdf/fpdf"
)

//go:embed assets/Manrope-400.ttf
var fontRegular []byte

//go:embed assets/Manrope-600.ttf
var fontSemi []byte

//go:embed assets/Manrope-800.ttf
var fontHeavy []byte

//go:embed assets/logo_white.png
var logoWhite []byte

//go:embed assets/logo_black.png
var logoBlack []byte

// Footer is printed on every page.
const Footer = "© Business Surgery · bxclub.kz · инструмент из библиотеки клуба"

const (
	pageW   = 210.0
	pageH   = 297.0
	mL      = 16.0
	cW      = pageW - 2*mL
	topNext = 25.0
	bottom  = 278.0
	logoAR  = 420.0 / 200.0
)

type rgb struct{ r, g, b int }

var (
	cInk    = rgb{12, 12, 12}
	cText   = rgb{24, 24, 24}
	cMute   = rgb{110, 110, 110}
	cSoft   = rgb{150, 150, 150}
	cLine   = rgb{200, 200, 200}
	cHair   = rgb{226, 226, 226}
	cFill   = rgb{244, 244, 244}
	cSample = rgb{250, 250, 250}
	cWhite  = rgb{255, 255, 255}
)

type doc struct {
	p    *fpdf.Fpdf
	tool *content.RichTool
	n    int // block number
}

func (d *doc) font(fam string, size float64) { d.p.SetFont(fam, "", size) }
func (d *doc) color(c rgb)                   { d.p.SetTextColor(c.r, c.g, c.b) }
func (d *doc) fill(c rgb)                    { d.p.SetFillColor(c.r, c.g, c.b) }
func (d *doc) draw(c rgb, w float64)         { d.p.SetDrawColor(c.r, c.g, c.b); d.p.SetLineWidth(w) }

// Spaces inside numbers («10 000») and before ₸ must not break a line: they
// become U+E000, a glyph of the space's width added to the embedded fonts.
var numGap = regexp.MustCompile(`(\d) (\d{3})`)

func glue(s string) string {
	for {
		t := numGap.ReplaceAllString(s, "$1\uE000$2")
		if t == s {
			break
		}
		s = t
	}
	return strings.ReplaceAll(s, " ₸", "\uE000₸")
}

// lines wraps text to the width with the current font.
func (d *doc) lines(s string, w float64) []string {
	s = glue(strings.TrimSpace(s))
	if s == "" {
		return []string{""}
	}
	var out []string
	for _, para := range strings.Split(s, "\n") {
		out = append(out, d.p.SplitText(para, w)...)
	}
	return out
}

// text writes wrapped lines from (x, y) and returns the y after them.
func (d *doc) text(x, y, w, lh float64, s string, align string) float64 {
	for _, l := range d.lines(s, w) {
		d.p.SetXY(x, y)
		d.p.CellFormat(w, lh, l, "", 0, align, false, 0, "")
		y += lh
	}
	return y
}

func (d *doc) need(h float64) {
	if d.p.GetY()+h > bottom {
		d.p.AddPage()
		d.p.SetY(topNext)
	}
}

// Render builds the PDF of one tool's template.
func Render(t *content.RichTool) ([]byte, error) {
	if t == nil || t.Template == nil {
		return nil, fmt.Errorf("tplpdf: no template")
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
	title := t.Template.Title
	if title == "" {
		title = t.Title
	}
	p.SetTitle(title+" · Business Surgery", true)
	p.SetAuthor("Business Surgery", true)
	p.SetSubject("Шаблон инструмента «"+t.Title+"» из библиотеки клуба Business Surgery · bxclub.kz", true)
	p.SetCreator("Business Surgery · bxclub.kz", true)
	p.SetProducer("Business Surgery", true)
	p.SetKeywords("Business Surgery, bxclub.kz, "+t.Organ, true)
	fixed := time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC)
	p.SetCreationDate(fixed)
	p.SetModificationDate(fixed)
	p.AliasNbPages("{nb}")
	d := &doc{p: p, tool: t}
	p.SetHeaderFuncMode(func() {
		if p.PageNo() == 1 {
			return
		}
		h := 6.0
		p.ImageOptions("lb", mL, 10, h*logoAR, h, false, fpdf.ImageOptions{}, 0, "")
		d.font("s", 7.5)
		d.color(cMute)
		name := t.Title
		if utf8.RuneCountInString(name) > 70 {
			name = string([]rune(name)[:68]) + "…"
		}
		p.SetXY(mL, 11.2)
		p.CellFormat(cW, 4, name+" · шаблон", "", 0, "R", false, 0, "")
		d.draw(cHair, 0.25)
		p.Line(mL, 19, pageW-mL, 19)
	}, false)
	p.SetFooterFunc(func() {
		d.draw(cHair, 0.25)
		p.Line(mL, 283, pageW-mL, 283)
		d.font("r", 7)
		d.color(cMute)
		p.SetXY(mL, 285)
		p.CellFormat(cW-30, 4, Footer, "", 0, "L", false, 0, "")
		d.font("s", 7)
		p.SetXY(pageW-mL-30, 285)
		p.CellFormat(30, 4, fmt.Sprintf("стр. %d из {nb}", p.PageNo()), "", 0, "R", false, 0, "")
	})
	p.AddPage()
	d.cover(title)
	for _, b := range t.Template.Blocks {
		d.block(b)
	}
	d.metrics()
	d.signoff()
	if err := p.Error(); err != nil {
		return nil, err
	}
	var buf bytes.Buffer
	if err := p.Output(&buf); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func (d *doc) cover(title string) {
	p, t := d.p, d.tool
	// title lines first: the band grows with them
	d.font("x", 21)
	tl := d.lines(title, cW)
	d.font("r", 8.6)
	sl := []string{}
	if s := strings.TrimSpace(t.Subtitle); s != "" {
		sl = d.lines(s, cW*0.86)
	}
	bandH := 52.0 + float64(len(tl))*8.6 + float64(len(sl))*4.3
	d.fill(cInk)
	p.Rect(0, 0, pageW, bandH, "F")
	// thin rings on the right, as on the covers of the club's guides
	p.ClipRect(0, 0, pageW, bandH, false)
	cx, cy := pageW-24.0, bandH*0.58
	for i, r := range []float64{46, 33, 21} {
		g := 44 - i*6
		d.draw(rgb{g, g, g}, 0.3)
		p.Circle(cx, cy, r, "D")
	}
	p.ClipEnd()
	lh := 11.0
	p.ImageOptions("lw", mL, 13, lh*logoAR, lh, false, fpdf.ImageOptions{}, 0, "")
	d.font("s", 6.8)
	d.color(cSoft)
	p.SetXY(pageW-mL-80, 14.5)
	p.CellFormat(80, 3.5, "ШАБЛОН · ИНСТРУМЕНТ КЛУБА", "", 0, "R", false, 0, "")
	d.font("s", 8)
	d.color(cWhite)
	p.SetXY(pageW-mL-80, 19)
	p.CellFormat(80, 4, "Business Surgery · bxclub.kz", "", 0, "R", false, 0, "")
	// organ pill
	org := strings.ToUpper(t.Organ)
	d.font("s", 6.8)
	pw := p.GetStringWidth(org) + 8
	d.draw(cWhite, 0.3)
	p.RoundedRect(mL, 32, pw, 5.6, 2.8, "1234", "D")
	d.color(cWhite)
	p.SetXY(mL, 32)
	p.CellFormat(pw, 5.6, org, "", 0, "C", false, 0, "")
	// title
	d.font("x", 21)
	y := 41.0
	for _, l := range tl {
		p.SetXY(mL-0.4, y)
		p.CellFormat(cW, 8.6, l, "", 0, "L", false, 0, "")
		y += 8.6
	}
	d.font("s", 9)
	d.color(rgb{210, 210, 210})
	p.SetXY(mL, y+1.2)
	p.CellFormat(cW, 4.5, "Инструмент: "+t.Title, "", 0, "L", false, 0, "")
	if len(sl) > 0 {
		d.font("r", 8.6)
		d.color(rgb{150, 150, 150})
		yy := y + 6.6
		for _, l := range sl {
			p.SetXY(mL, yy)
			p.CellFormat(cW, 4.3, l, "", 0, "L", false, 0, "")
			yy += 4.3
		}
	}
	// meta strip
	y = bandH + 7
	type kv struct{ k, v string }
	meta := []kv{{"ВРЕМЯ", t.Time}, {"УРОВЕНЬ", t.Level}}
	if strings.TrimSpace(t.Source) != "" {
		meta = append(meta, kv{"МЕТОД", t.Source})
	} else {
		meta = append(meta, kv{"ЗАПОЛНИЛ", ""})
	}
	colW := []float64{cW * 0.42, cW * 0.18, cW * 0.40}
	x := mL
	maxY := y
	for i, m := range meta {
		d.font("s", 6.5)
		d.color(cSoft)
		p.SetXY(x, y)
		p.CellFormat(colW[i]-4, 3.5, m.k, "", 0, "L", false, 0, "")
		d.font("s", 8.6)
		d.color(cText)
		yy := y + 4.6
		if m.v == "" {
			d.draw(cLine, 0.3)
			p.Line(x, yy+5, x+colW[i], yy+5)
			yy += 6
		} else {
			yy = d.text(x, yy, colW[i]-4, 4.2, m.v, "L")
		}
		if yy > maxY {
			maxY = yy
		}
		x += colW[i]
	}
	y = maxY + 4
	d.draw(cHair, 0.25)
	p.Line(mL, y, pageW-mL, y)
	y += 5
	if intro := strings.TrimSpace(d.tool.Template.Intro); intro != "" {
		d.font("r", 9.2)
		il := d.lines(intro, cW-12)
		h := float64(len(il))*4.6 + 7
		d.fill(cFill)
		p.Rect(mL, y, cW, h, "F")
		d.fill(cInk)
		p.Rect(mL, y, 1.2, h, "F")
		d.font("s", 6.5)
		d.color(cMute)
		p.SetXY(mL+6, y+2.6)
		p.CellFormat(cW-12, 3, "КАК ЗАПОЛНЯТЬ", "", 0, "L", false, 0, "")
		d.font("r", 9.2)
		d.color(cText)
		d.text(mL+6, y+6.4, cW-12, 4.6, intro, "L")
		y += h + 1
	}
	p.SetY(y + 3)
}

// head draws the numbered block heading; keep says how much room the first
// part of the block needs so the heading never stays alone at a page end.
func (d *doc) head(title string, keep float64) float64 {
	p := d.p
	d.n++
	d.need(14 + keep)
	y := p.GetY() + 3
	d.fill(cInk)
	p.RoundedRect(mL, y, 6.4, 6.4, 1.4, "1234", "F")
	d.font("x", 8)
	d.color(cWhite)
	p.SetXY(mL, y)
	p.CellFormat(6.4, 6.4, fmt.Sprintf("%d", d.n), "", 0, "C", false, 0, "")
	d.font("x", 11.5)
	d.color(cInk)
	yy := d.text(mL+9.5, y+0.6, cW-9.5, 5.4, title, "L")
	p.SetY(maxf(yy, y+6.4) + 3.2)
	return y
}

// hint: a small grey note at the right end of the heading row.
func (d *doc) hint(y float64, title, s string) {
	d.font("x", 11.5)
	tw := d.p.GetStringWidth(title)
	d.font("r", 6.8)
	if 9.5+tw+d.p.GetStringWidth(s)+8 > cW {
		return
	}
	d.color(cSoft)
	keep := d.p.GetY()
	d.p.SetXY(mL, y+2.2)
	d.p.CellFormat(cW, 3, s, "", 0, "R", false, 0, "")
	d.p.SetY(keep)
}

func maxf(a, b float64) float64 {
	if a > b {
		return a
	}
	return b
}

func (d *doc) block(b content.RichBlock) {
	switch b.Type {
	case "fields":
		d.fields(b)
	case "table":
		d.table(b)
	case "checklist":
		d.checklist(b.Title, b.Items)
	case "scale":
		d.scale(b)
	case "note":
		d.note(b)
	}
}

func (d *doc) fields(b content.RichBlock) {
	p := d.p
	two := len(b.Fields) > 1
	for _, f := range b.Fields {
		if utf8.RuneCountInString(f) > 44 {
			two = false
		}
	}
	d.head(b.Title, 16)
	colW := cW
	if two {
		colW = (cW - 8) / 2
	}
	for i := 0; i < len(b.Fields); {
		row := b.Fields[i:minInt(i+1, len(b.Fields))]
		if two {
			row = b.Fields[i:minInt(i+2, len(b.Fields))]
		}
		d.font("s", 7.4)
		lh := 0
		for _, f := range row {
			if n := len(d.lines(f, colW)); n > lh {
				lh = n
			}
		}
		h := float64(lh)*3.6 + 9.5
		d.need(h)
		y := p.GetY()
		for k, f := range row {
			x := mL + float64(k)*(colW+8)
			d.font("s", 7.4)
			d.color(cMute)
			d.text(x, y, colW, 3.6, f, "L")
			d.draw(cLine, 0.3)
			p.Line(x, y+h-1.5, x+colW, y+h-1.5)
		}
		p.SetY(y + h + 1.2)
		i += len(row)
	}
	p.SetY(p.GetY() + 2)
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// colWidths: every column is at least as wide as its longest word (no word
// is cut in the middle), the rest is shared by the amount of text. When the
// words do not fit, the table's font gets smaller.
func (d *doc) colWidths(b content.RichBlock, fs, pad float64) ([]float64, float64) {
	n := len(b.Columns)
	for ; ; fs -= 0.3 {
		minW := make([]float64, n)
		wt := make([]float64, n)
		sumMin, sumWt := 0.0, 0.0
		for i, c := range b.Columns {
			m := 11.0
			d.font("s", fs)
			for _, w := range strings.Fields(glue(c)) {
				if x := d.p.GetStringWidth(w) + 2*pad + 0.6; x > m {
					m = x
				}
			}
			l := float64(utf8.RuneCountInString(c))
			if l > 24 {
				l = 24
			}
			d.font("r", fs)
			for _, r := range b.Rows {
				if i >= len(r) {
					continue
				}
				for _, w := range strings.Fields(glue(r[i])) {
					if x := d.p.GetStringWidth(w) + 2*pad + 0.6; x > m {
						m = x
					}
				}
				if k := float64(utf8.RuneCountInString(r[i])); k > l {
					l = k
				}
			}
			if l > 34 {
				l = 34
			}
			if l < 6 {
				l = 6
			}
			minW[i], wt[i] = m, l
			sumMin += m
			sumWt += l
		}
		if sumMin <= cW || fs <= 5.8 {
			ws := make([]float64, n)
			extra := cW - sumMin
			if extra < 0 {
				// last resort: squeeze proportionally
				for i := range ws {
					ws[i] = minW[i] * cW / sumMin
				}
				return ws, fs
			}
			// the extra goes first to columns whose text is long
			for i := range ws {
				ws[i] = minW[i] + extra*wt[i]/sumWt
			}
			return ws, fs
		}
	}
}

func (d *doc) table(b content.RichBlock) {
	p := d.p
	n := len(b.Columns)
	if n == 0 {
		return
	}
	fs := 8.0
	switch {
	case n >= 8:
		fs = 7.0
	case n >= 6:
		fs = 7.4
	}
	pad := 1.6
	if n >= 8 {
		pad = 1.3
	}
	ws, fs := d.colWidths(b, fs, pad)
	lh := fs * 0.47
	headH := func() float64 {
		d.font("s", fs)
		m := 1
		for i, c := range b.Columns {
			if k := len(d.lines(c, ws[i]-2*pad)); k > m {
				m = k
			}
		}
		return float64(m)*lh + 2*pad + 0.6
	}
	hh := headH()
	drawHead := func() {
		y := p.GetY()
		d.fill(cInk)
		p.Rect(mL, y, cW, hh, "F")
		d.font("s", fs)
		d.color(cWhite)
		x := mL
		for i, c := range b.Columns {
			d.text(x+pad, y+pad+0.3, ws[i]-2*pad, lh, c, "L")
			x += ws[i]
		}
		p.SetY(y + hh)
	}
	rowLines := func(r []string) int {
		d.font("r", fs)
		m := 1
		for i := 0; i < n; i++ {
			if i < len(r) {
				if k := len(d.lines(r[i], ws[i]-2*pad)); k > m {
					m = k
				}
			}
		}
		return m
	}
	emptyH := 8.0
	if n >= 7 {
		emptyH = 7.6
	}
	hy := d.head(b.Title, hh+emptyH*2)
	if len(b.Rows) > 0 {
		d.hint(hy, b.Title, "серые строки: пример заполнения")
	}
	drawHead()
	cells := func(y, h float64) {
		d.draw(cLine, 0.2)
		x := mL
		for i := 0; i < n; i++ {
			p.Rect(x, y, ws[i], h, "D")
			x += ws[i]
		}
	}
	for _, r := range b.Rows {
		h := float64(rowLines(r))*lh + 2*pad + 0.4
		if p.GetY()+h > bottom {
			d.p.AddPage()
			p.SetY(topNext)
			drawHead()
		}
		y := p.GetY()
		d.fill(cSample)
		p.Rect(mL, y, cW, h, "F")
		cells(y, h)
		d.font("r", fs)
		d.color(cSoft)
		x := mL
		for i := 0; i < n; i++ {
			if i < len(r) {
				d.text(x+pad, y+pad+0.2, ws[i]-2*pad, lh, r[i], "L")
			}
			x += ws[i]
		}
		p.SetY(y + h)
	}
	empty := b.EmptyRows
	if empty <= 0 {
		empty = 6
	}
	if empty > 24 {
		empty = 24
	}
	for k := 0; k < empty; k++ {
		if p.GetY()+emptyH > bottom {
			d.p.AddPage()
			p.SetY(topNext)
			drawHead()
		}
		y := p.GetY()
		cells(y, emptyH)
		p.SetY(y + emptyH)
	}
	// strong bottom rule
	d.draw(cInk, 0.35)
	p.Line(mL, p.GetY(), mL+cW, p.GetY())
	p.SetY(p.GetY() + 4)
}

func (d *doc) checklist(title string, items []string) {
	p := d.p
	d.head(title, 8)
	tw := cW - 8
	for _, it := range items {
		d.font("r", 9.2)
		ls := d.lines(it, tw)
		h := float64(len(ls))*4.5 + 3
		d.need(h)
		y := p.GetY()
		d.draw(cInk, 0.35)
		p.RoundedRect(mL, y+0.4, 4.2, 4.2, 0.8, "1234", "D")
		d.color(cText)
		d.text(mL+8, y+0.3, tw, 4.5, it, "L")
		d.draw(cHair, 0.2)
		p.Line(mL+8, y+h-0.6, mL+cW, y+h-0.6)
		p.SetY(y + h + 0.6)
	}
	p.SetY(p.GetY() + 3)
}

func (d *doc) scale(b content.RichBlock) {
	p := d.p
	lo, hi := b.Min, b.Max
	if hi <= lo {
		lo, hi = 1, 10
	}
	cnt := hi - lo + 1
	box := 7.6
	gap := 1.4
	sw := float64(cnt)*box + float64(cnt-1)*gap
	lw := cW - sw - 6
	hy := d.head(b.Title, 10)
	d.hint(hy, b.Title, fmt.Sprintf("обведи оценку: %d плохо, %d отлично", lo, hi))
	for _, it := range b.Items {
		d.font("s", 8.8)
		ls := d.lines(it, lw)
		h := maxf(float64(len(ls))*4.3, box) + 3.4
		d.need(h)
		y := p.GetY()
		d.color(cText)
		d.text(mL, y+(h-3.4-float64(len(ls))*4.3)/2, lw, 4.3, it, "L")
		x := mL + cW - sw
		by := y + (h-3.4-box)/2
		for v := lo; v <= hi; v++ {
			d.draw(cLine, 0.3)
			p.RoundedRect(x, by, box, box, box/2, "1234", "D")
			d.font("s", 7.4)
			d.color(cMute)
			p.SetXY(x, by)
			p.CellFormat(box, box, fmt.Sprintf("%d", v), "", 0, "C", false, 0, "")
			x += box + gap
		}
		d.draw(cHair, 0.2)
		p.Line(mL, y+h-0.8, mL+cW, y+h-0.8)
		p.SetY(y + h)
	}
	p.SetY(p.GetY() + 4)
}

func (d *doc) note(b content.RichBlock) {
	p := d.p
	n := b.Lines
	if n <= 0 {
		n = 4
	}
	if n > 14 {
		n = 14
	}
	d.head(b.Title, 9*2)
	for k := 0; k < n; k++ {
		d.need(9)
		y := p.GetY() + 9
		d.draw(cLine, 0.25)
		p.Line(mL, y, mL+cW, y)
		p.SetY(y)
	}
	p.SetY(p.GetY() + 5)
}

// metrics: the target numbers of the tool, in a framed box.
func (d *doc) metrics() {
	ms := d.tool.Metrics
	if len(ms) == 0 {
		return
	}
	p := d.p
	d.font("r", 8.8)
	h := 11.0
	for _, m := range ms {
		h += float64(len(d.lines(m, cW-16)))*4.3 + 1.8
	}
	d.need(h + 6)
	y := p.GetY() + 2
	d.draw(cInk, 0.4)
	p.Rect(mL, y, cW, h, "D")
	d.font("s", 6.8)
	d.color(cMute)
	p.SetXY(mL+6, y+3.4)
	p.CellFormat(cW-12, 3, "КОНТРОЛЬНЫЕ ЦИФРЫ · ЦЕЛИ ИНСТРУМЕНТА", "", 0, "L", false, 0, "")
	yy := y + 9
	for _, m := range ms {
		d.fill(cInk)
		p.Rect(mL+6, yy+1.5, 1.6, 1.6, "F")
		d.font("r", 8.8)
		d.color(cText)
		yy = d.text(mL+10.5, yy, cW-16, 4.3, m, "L") + 1.8
	}
	p.SetY(y + h + 5)
}

func (d *doc) signoff() {
	p := d.p
	d.need(12)
	y := p.GetY() + 2
	labels := []string{"Дата заполнения", "Следующая сверка", "Трекер BS"}
	w := (cW - 2*6) / 3
	for i, l := range labels {
		x := mL + float64(i)*(w+6)
		d.font("s", 6.6)
		d.color(cSoft)
		p.SetXY(x, y)
		p.CellFormat(w, 3, strings.ToUpper(l), "", 0, "L", false, 0, "")
		d.draw(cLine, 0.3)
		p.Line(x, y+10, x+w, y+10)
	}
	p.SetY(y + 12)
}
