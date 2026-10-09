package tplpdf

// R29: the Gallup analysis of a resident as a branded A4 PDF: black cover
// with the logo, portrait and top-5, the domain balance, the map of talent
// interactions, how to use the profile in the business, risks, the 30-day
// plan and all talents. The platform sends the analysis it shows (the
// visuals are computed there), so the PDF matches the screen.

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/go-pdf/fpdf"
)

type GallupTop struct {
	Rank   int    `json:"rank"`
	Name   string `json:"name"`
	Ru     string `json:"ru"`
	Domain string `json:"domain"`
	Line   string `json:"line"`
}

type GallupDomain struct {
	Ru    string `json:"ru"`
	Score int    `json:"score"` // 0..100
	Top10 int    `json:"top10"`
	Note  string `json:"note"`
}

type GallupNode struct {
	Ru   string  `json:"ru"`
	Rank int     `json:"rank"`
	X    float64 `json:"x"` // 0..1 of the wide layout (600 x 380)
	Y    float64 `json:"y"`
	R    float64 `json:"r"`    // radius in layout px
	Kind string  `json:"kind"` // top | anchor
	Lab  string  `json:"lab"`  // a: the label above the circle, else below
}

type GallupEdge struct {
	A    int    `json:"a"` // node indexes
	B    int    `json:"b"`
	Type string `json:"type"` // amp | conf | anchor
}

type GallupMap struct {
	Nodes []GallupNode `json:"nodes"`
	Edges []GallupEdge `json:"edges"`
	Line  string       `json:"line"`
}

type GallupLink struct {
	Names  []string `json:"names"`
	Title  string   `json:"title"`
	Effect string   `json:"effect"`
	Scene  string   `json:"scene"`
	Fix    string   `json:"fix"`
}

type GallupRole struct {
	N      int    `json:"n"`
	Name   string `json:"name"`
	Energy int    `json:"energy"`
	Risk   int    `json:"risk"`
	Zone   string `json:"zone"` // own | guard | give | low
	Why    string `json:"why"`
}

type GallupTriple struct {
	A string `json:"a"`
	B string `json:"b"`
	C string `json:"c"`
	D string `json:"d"`
}

type GallupTalent struct {
	Rank     int    `json:"rank"`
	Name     string `json:"name"`
	Ru       string `json:"ru"`
	DomainRu string `json:"domainRu"`
	Essence  string `json:"essence"`
	Business string `json:"business"`
	Blind    string `json:"blind"`
	Manage   string `json:"manage"`
}

// GallupDoc: everything the PDF shows, ready for print.
type GallupDoc struct {
	Name       string         `json:"name"`
	Date       string         `json:"date"`
	Headline   string         `json:"headline"`
	Plain      string         `json:"plain"`
	Top5       []GallupTop    `json:"top5"`
	Top10      string         `json:"top10"`
	Domains    []GallupDomain `json:"domains"`
	DomainLine string         `json:"domainLine"`
	Map        GallupMap      `json:"map"`
	Amplify    []GallupLink   `json:"amplify"`
	Conflict   []GallupLink   `json:"conflict"`
	Anchors    []GallupLink   `json:"anchors"`
	Best       []string       `json:"best"`
	Stress     []string       `json:"stress"`
	Reset      []string       `json:"reset"`
	Blind      []GallupTriple `json:"blind"` // a title, b text, c check
	Keep       []string       `json:"keep"`
	Delegate   []string       `json:"delegate"`
	Hires      []GallupTriple `json:"hires"` // a role, b talents, c why
	Areas      []GallupTriple `json:"areas"` // a title, b how, c action
	Roles      []GallupRole   `json:"roles"`
	RolesLine  string         `json:"rolesLine"`
	Risks      []GallupTriple `json:"risks"` // a title, b trigger, c signs, d rule
	Plan       []GallupTriple `json:"plan"`  // a when, b action, c result
	WorkWith   []string       `json:"workWithMe"`
	Business   []string       `json:"business"` // the summary of a profile without the deep part
	Partners   []string       `json:"partners"`
	Talents    []GallupTalent `json:"talents"`
	// R71: the order of the talents (keys) and the AI lines of «Чем клуб
	// полезен»; the server builds Plus from them (gallup_plus.go)
	Order  []string        `json:"order,omitempty"`
	ClubAI json.RawMessage `json:"clubAI,omitempty"`
	Plus   *GallupPlus     `json:"plus,omitempty"`
}

// GallupFooter is printed on every page.
const GallupFooter = "Business Surgery · bxclub.kz · разбор Gallup CliftonStrengths"

type gdoc struct {
	*doc
	g *GallupDoc
	n int
}

// RenderGallup builds the PDF of one analysis.
func RenderGallup(g *GallupDoc) ([]byte, error) {
	if g == nil || len(g.Talents) == 0 && len(g.Top5) == 0 {
		return nil, fmt.Errorf("tplpdf: empty gallup")
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
	name := strings.TrimSpace(g.Name)
	title := "Разбор талантов Gallup"
	if name != "" {
		title += " · " + name
	}
	p.SetTitle(title+" · Business Surgery", true)
	p.SetAuthor("Business Surgery", true)
	p.SetSubject("Разбор CliftonStrengths 34 для собственника бизнеса · bxclub.kz", true)
	p.SetCreator("Business Surgery · bxclub.kz", true)
	p.SetProducer("Business Surgery", true)
	p.SetKeywords("Business Surgery, bxclub.kz, Gallup, CliftonStrengths", true)
	now := time.Now().UTC()
	p.SetCreationDate(now)
	p.SetModificationDate(now)
	p.AliasNbPages("{nb}")
	d := &gdoc{doc: &doc{p: p}, g: g}
	p.SetHeaderFuncMode(func() {
		if p.PageNo() == 1 {
			return
		}
		h := 6.0
		p.ImageOptions("lb", mL, 10, h*logoAR, h, false, fpdf.ImageOptions{}, 0, "")
		d.font("s", 7.5)
		d.color(cMute)
		p.SetXY(mL, 11.2)
		hd := "Gallup · разбор талантов"
		if name != "" {
			hd += " · " + clip(name, 50)
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
		p.CellFormat(cW-30, 4, GallupFooter, "", 0, "L", false, 0, "")
		d.font("s", 7)
		p.SetXY(pageW-mL-30, 285)
		p.CellFormat(30, 4, fmt.Sprintf("стр. %d из {nb}", p.PageNo()), "", 0, "R", false, 0, "")
	})
	p.AddPage()
	d.cover(name)
	d.portrait()
	d.topWork() // R71
	d.domains()
	d.strip() // R71
	d.interactions()
	d.pairs() // R71
	d.state()
	d.business()
	d.split() // R71
	d.risks()
	d.plan()
	d.experiment() // R71
	d.club()       // R71
	d.talents()
	if err := p.Error(); err != nil {
		return nil, err
	}
	var buf bytes.Buffer
	if err := p.Output(&buf); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func clip(s string, n int) string {
	r := []rune(strings.TrimSpace(s))
	if len(r) > n {
		return string(r[:n-1]) + "…"
	}
	return string(r)
}

func (d *gdoc) cover(name string) {
	p, g := d.p, d.g
	head := g.Headline
	if head == "" {
		head = "Разбор талантов"
	}
	d.font("x", 19)
	tl := d.lines(head, cW-18)
	bandH := 50.0 + float64(len(tl))*8
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
	p.CellFormat(90, 3.5, "GALLUP · CLIFTONSTRENGTHS 34", "", 0, "R", false, 0, "")
	d.font("s", 8)
	d.color(cWhite)
	p.SetXY(pageW-mL-90, 19)
	p.CellFormat(90, 4, "Business Surgery · bxclub.kz", "", 0, "R", false, 0, "")
	pill := "РАЗБОР ТАЛАНТОВ"
	if name != "" {
		pill += " · " + strings.ToUpper(clip(name, 40))
	}
	d.font("s", 6.8)
	pw := p.GetStringWidth(pill) + 8
	d.draw(cWhite, 0.3)
	p.RoundedRect(mL, 31, pw, 5.6, 2.8, "1234", "D")
	d.color(cWhite)
	p.SetXY(mL, 31)
	p.CellFormat(pw, 5.6, pill, "", 0, "C", false, 0, "")
	d.font("x", 19)
	y := 40.0
	for _, l := range tl {
		p.SetXY(mL-0.3, y)
		p.CellFormat(cW, 8, l, "", 0, "L", false, 0, "")
		y += 8
	}
	meta := fmt.Sprintf("Талантов в профиле: %d", len(g.Talents))
	if g.Date != "" {
		meta += " · " + g.Date
	}
	d.font("s", 8.4)
	d.color(rgb{170, 170, 170})
	p.SetXY(mL, y+1)
	p.CellFormat(cW, 4.5, meta, "", 0, "L", false, 0, "")
	p.SetY(bandH + 7)
}

// sec: a numbered section heading.
func (d *gdoc) sec(title, sub string, keep float64) {
	p := d.p
	d.n++
	d.need(16 + keep)
	y := p.GetY() + 2
	d.fill(cInk)
	p.RoundedRect(mL, y, 6.4, 6.4, 1.4, "1234", "F")
	d.font("x", 8)
	d.color(cWhite)
	p.SetXY(mL, y)
	p.CellFormat(6.4, 6.4, fmt.Sprintf("%d", d.n), "", 0, "C", false, 0, "")
	d.font("x", 12.5)
	d.color(cInk)
	yy := d.text(mL+9.5, y+0.4, cW-9.5, 5.8, title, "L")
	if sub != "" {
		d.font("r", 8.4)
		d.color(cMute)
		yy = d.text(mL+9.5, yy+0.4, cW-9.5, 4.2, sub, "L")
	}
	p.SetY(maxf(yy, y+6.4) + 3.5)
}

// para writes a paragraph across pages.
func (d *gdoc) para(s string, fam string, size, lh float64, c rgb, x, w float64) {
	if strings.TrimSpace(s) == "" {
		return
	}
	d.font(fam, size)
	for _, l := range d.lines(s, w) {
		d.need(lh)
		d.font(fam, size)
		d.color(c)
		d.p.SetXY(x, d.p.GetY())
		d.p.CellFormat(w, lh, l, "", 0, "L", false, 0, "")
		d.p.SetY(d.p.GetY() + lh)
	}
}

// takeaway: the one line under a visual.
func (d *gdoc) takeaway(s string) {
	if strings.TrimSpace(s) == "" {
		return
	}
	p := d.p
	d.font("s", 9)
	ls := d.lines(s, cW-10)
	h := float64(len(ls))*4.6 + 5
	d.need(h + 2)
	y := p.GetY()
	d.fill(cFill)
	p.Rect(mL, y, cW, h, "F")
	d.fill(cInk)
	p.Rect(mL, y, 1.2, h, "F")
	d.color(cInk)
	d.text(mL+5, y+2.5, cW-10, 4.6, s, "L")
	p.SetY(y + h + 4)
}

type part struct {
	label string
	text  string
	fam   string
	size  float64
	shade bool
}

// card: a bordered block with a title, an optional chip line and labelled
// parts; measured first so it never breaks across pages.
func (d *gdoc) card(x, w float64, title, chips string, parts []part) float64 {
	p := d.p
	pad := 4.0
	iw := w - 2*pad
	d.font("x", 10)
	tl := d.lines(title, iw)
	h := pad + float64(len(tl))*5
	var cl []string
	if chips != "" {
		d.font("s", 7.6)
		cl = d.lines(chips, iw)
		h += float64(len(cl))*3.8 + 1
	}
	type m struct {
		ll, tl []string
		part
	}
	var ms []m
	for _, pt := range parts {
		if strings.TrimSpace(pt.text) == "" {
			continue
		}
		if pt.fam == "" {
			pt.fam, pt.size = "r", 8.8
		}
		iw2 := iw
		if pt.shade {
			iw2 = iw - 6
		}
		d.font(pt.fam, pt.size)
		mm := m{tl: d.lines(pt.text, iw2), part: pt}
		h += 2
		if pt.label != "" {
			h += 3.8
		}
		if pt.shade {
			h += 3
		}
		h += float64(len(mm.tl)) * pt.size * 0.5
		ms = append(ms, mm)
	}
	h += pad - 0.5
	d.need(h + 3)
	y := p.GetY()
	d.draw(cLine, 0.3)
	p.RoundedRect(x, y, w, h, 2.4, "1234", "D")
	yy := y + pad
	d.font("x", 10)
	d.color(cInk)
	for _, l := range tl {
		p.SetXY(x+pad, yy)
		p.CellFormat(iw, 5, l, "", 0, "L", false, 0, "")
		yy += 5
	}
	if len(cl) > 0 {
		d.font("s", 7.6)
		d.color(cMute)
		for _, l := range cl {
			p.SetXY(x+pad, yy)
			p.CellFormat(iw, 3.8, l, "", 0, "L", false, 0, "")
			yy += 3.8
		}
		yy += 1
	}
	for _, mm := range ms {
		yy += 2
		lh := mm.size * 0.5
		if mm.shade {
			bh := float64(len(mm.tl))*lh + 3
			if mm.label != "" {
				bh += 3.8
			}
			d.fill(cSample)
			p.Rect(x+pad, yy, iw, bh, "F")
			d.fill(cLine)
			p.Rect(x+pad, yy, 0.8, bh, "F")
			yy += 1.5
		}
		xi, wi := x+pad, iw
		if mm.shade {
			xi, wi = x+pad+3.5, iw-6
		}
		if mm.label != "" {
			d.font("s", 6.6)
			d.color(cMute)
			p.SetXY(xi, yy)
			p.CellFormat(wi, 3.8, strings.ToUpper(mm.label), "", 0, "L", false, 0, "")
			yy += 3.8
		}
		d.font(mm.fam, mm.size)
		d.color(cText)
		for _, l := range mm.tl {
			p.SetXY(xi, yy)
			p.CellFormat(wi, lh, l, "", 0, "L", false, 0, "")
			yy += lh
		}
		if mm.shade {
			yy += 1.5
		}
	}
	p.SetY(y + h + 3)
	return h
}

// bullets: a list with dots, page by page.
func (d *gdoc) bullets(items []string, x, w float64) {
	p := d.p
	for _, it := range items {
		d.font("r", 9)
		ls := d.lines(it, w-5)
		d.need(float64(len(ls))*4.5 + 1.5)
		y := p.GetY()
		d.fill(cInk)
		p.Circle(x+1.2, y+2.2, 0.7, "F")
		d.color(cText)
		d.text(x+4.5, y, w-5, 4.5, it, "L")
		p.SetY(y + float64(len(ls))*4.5 + 1.6)
	}
}

// colH: the height of two columns of lists.
func (d *gdoc) colH(a, b []string) float64 {
	cw := (cW - 6) / 2
	meas := func(items []string) float64 {
		d.font("r", 9)
		h := 6.0
		for _, it := range items {
			h += float64(len(d.lines(it, cw-5)))*4.5 + 1.6
		}
		return h
	}
	return maxf(meas(a), meas(b))
}

// columns: two lists side by side with headings (each measured).
func (d *gdoc) columns(h1 string, a []string, h2 string, b []string) {
	p := d.p
	gw := 6.0
	cw := (cW - gw) / 2
	meas := func(items []string) float64 {
		d.font("r", 9)
		h := 6.0
		for _, it := range items {
			h += float64(len(d.lines(it, cw-5)))*4.5 + 1.6
		}
		return h
	}
	h := maxf(meas(a), meas(b))
	d.need(h + 2)
	y := p.GetY()
	for i, col := range []struct {
		h  string
		it []string
	}{{h1, a}, {h2, b}} {
		x := mL + float64(i)*(cw+gw)
		d.font("x", 9.6)
		d.color(cInk)
		p.SetXY(x, y)
		p.CellFormat(cw, 4.6, col.h, "", 0, "L", false, 0, "")
		d.draw(cInk, 0.4)
		p.Line(x, y+5.4, x+cw, y+5.4)
		yy := y + 7.2
		for _, it := range col.it {
			d.font("r", 9)
			ls := d.lines(it, cw-5)
			d.fill(cInk)
			p.Circle(x+1.2, yy+2.2, 0.7, "F")
			d.color(cText)
			d.text(x+4.5, yy, cw-5, 4.5, it, "L")
			yy += float64(len(ls))*4.5 + 1.6
		}
	}
	p.SetY(y + h + 3)
}

func (d *gdoc) portrait() {
	p, g := d.p, d.g
	if g.Plain != "" || len(g.Top5) > 0 {
		d.sec("Портрет простыми словами", "Как вы думаете, решаете и действуете", 30)
		d.para(g.Plain, "r", 10, 5.1, cText, mL, cW)
		p.SetY(p.GetY() + 3)
	}
	if len(g.Top5) > 0 {
		n := len(g.Top5)
		gw := 2.6
		w := (cW - gw*float64(n-1)) / float64(n)
		h := 0.0
		for _, t := range g.Top5 {
			d.font("r", 7.6)
			hh := 24 + float64(len(d.lines(t.Line, w-6)))*3.6
			fsz := 9.4
			for d.font("x", fsz); fsz > 6.6 && p.GetStringWidth(t.Ru) > w-6; d.font("x", fsz) {
				fsz -= 0.4
			}
			hh += float64(len(d.lines(t.Ru, w-6))-1) * 4.4
			h = maxf(h, hh)
		}
		d.need(h + 4)
		y := p.GetY()
		for i, t := range g.Top5 {
			x := mL + float64(i)*(w+gw)
			d.fill(cFill)
			p.RoundedRect(x, y, w, h, 2.2, "1234", "F")
			d.fill(cInk)
			p.Rect(x+3, y, w-6, 0.9, "F")
			d.font("x", 17)
			d.color(cInk)
			p.SetXY(x+3, y+3)
			p.CellFormat(w-6, 7, fmt.Sprintf("%02d", t.Rank), "", 0, "L", false, 0, "")
			fsz := 9.4
			for d.font("x", fsz); fsz > 6.6 && p.GetStringWidth(t.Ru) > w-6; d.font("x", fsz) {
				fsz -= 0.4
			}
			yy := d.text(x+3, y+11.5, w-6, 4.4, t.Ru, "L")
			d.font("s", 7)
			d.color(cMute)
			p.SetXY(x+3, yy)
			p.CellFormat(w-6, 3.4, t.Name, "", 0, "L", false, 0, "")
			d.font("r", 7.6)
			d.color(cText)
			d.text(x+3, yy+4.6, w-6, 3.6, t.Line, "L")
		}
		p.SetY(y + h + 4)
	}
	if g.Top10 != "" {
		d.need(14)
		d.font("s", 6.8)
		d.color(cMute)
		p.SetXY(mL, p.GetY())
		p.CellFormat(cW, 3.6, "МЕСТА 6-10", "", 0, "L", false, 0, "")
		p.SetY(p.GetY() + 4.4)
		d.para(g.Top10, "r", 9.4, 4.8, cText, mL, cW)
		p.SetY(p.GetY() + 2)
	}
}

func (d *gdoc) domains() {
	p, g := d.p, d.g
	if len(g.Domains) == 0 {
		return
	}
	d.font("s", 9)
	keep := float64(len(g.Domains))*12 + 4 + float64(len(d.lines(g.DomainLine, cW-10)))*4.6 + 8
	d.sec("Баланс четырёх доменов", "Сила домена: где стоят все его темы в вашем списке, 100 - все темы домена на первых местах", keep)
	labW, barW := 50.0, cW-50-34
	d.need(keep)
	y := p.GetY()
	for _, dm := range g.Domains {
		d.font("x", 9.4)
		d.color(cInk)
		p.SetXY(mL, y)
		p.CellFormat(labW, 4.6, dm.Ru, "", 0, "L", false, 0, "")
		d.font("r", 7.2)
		d.color(cMute)
		p.SetXY(mL, y+4.8)
		p.CellFormat(labW, 3.6, clip(dm.Note, 40), "", 0, "L", false, 0, "")
		d.fill(cFill)
		p.RoundedRect(mL+labW, y+1, barW, 6, 1.2, "1234", "F")
		v := math.Max(0, math.Min(100, float64(dm.Score)))
		if v > 0 {
			d.fill(cInk)
			p.RoundedRect(mL+labW, y+1, math.Max(2.4, barW*v/100), 6, 1.2, "1234", "F")
		}
		// the 50 mark: the middle of the list
		d.draw(cSoft, 0.2)
		p.Line(mL+labW+barW/2, y+0.2, mL+labW+barW/2, y+7.8)
		d.font("x", 11)
		d.color(cInk)
		p.SetXY(mL+labW+barW+3, y+0.4)
		p.CellFormat(14, 5, fmt.Sprintf("%d", dm.Score), "", 0, "R", false, 0, "")
		d.font("r", 6.8)
		d.color(cMute)
		p.SetXY(mL+labW+barW+3, y+5.2)
		p.CellFormat(31, 3.4, fmt.Sprintf("%d в топ-10", dm.Top10), "", 0, "L", false, 0, "")
		y += 12
	}
	p.SetY(y + 1)
	d.takeaway(g.DomainLine)
}

// mapImage draws the interaction map in a box: amplify - solid, conflict -
// dashed, anchors - dotted with an arrow down to the weak talent.
func (d *gdoc) mapImage(x0, y0, w float64) float64 {
	p, m := d.p, d.g.Map
	h := w * 380 / 600
	sx := func(v float64) float64 { return x0 + v*w }
	sy := func(v float64) float64 { return y0 + v*h }
	k := w / 600
	d.fill(cSample)
	p.RoundedRect(x0, y0, w, h, 2.4, "1234", "F")
	for _, e := range m.Edges {
		if e.A < 0 || e.B < 0 || e.A >= len(m.Nodes) || e.B >= len(m.Nodes) {
			continue
		}
		a, b := m.Nodes[e.A], m.Nodes[e.B]
		x1, y1, x2, y2 := sx(a.X), sy(a.Y), sx(b.X), sy(b.Y)
		switch e.Type {
		case "conf":
			d.draw(rgb{90, 90, 90}, 0.55)
			p.SetDashPattern([]float64{2.2, 1.6}, 0)
		case "anchor":
			d.draw(rgb{120, 120, 120}, 0.45)
			p.SetDashPattern([]float64{0.5, 1.2}, 0)
		default:
			d.draw(cInk, 0.9)
			p.SetDashPattern([]float64{}, 0)
		}
		// stop at the circles
		dx, dy := x2-x1, y2-y1
		l := math.Hypot(dx, dy)
		if l < 1 {
			continue
		}
		ra, rb := a.R*k+0.6, b.R*k+0.6
		ax, ay := x1+dx/l*ra, y1+dy/l*ra
		bx, by := x2-dx/l*rb, y2-dy/l*rb
		p.Line(ax, ay, bx, by)
		p.SetDashPattern([]float64{}, 0)
		if e.Type == "anchor" { // arrow toward the weak talent (B)
			ux, uy := dx/l, dy/l
			d.fill(rgb{120, 120, 120})
			p.Polygon([]fpdf.PointType{{X: bx, Y: by}, {X: bx - ux*2.2 - uy*1.1, Y: by - uy*2.2 + ux*1.1}, {X: bx - ux*2.2 + uy*1.1, Y: by - uy*2.2 - ux*1.1}}, "F")
		}
	}
	p.SetDashPattern([]float64{}, 0)
	for _, n := range m.Nodes {
		cx, cy, r := sx(n.X), sy(n.Y), math.Max(2.4, n.R*k)
		if n.Kind == "anchor" {
			d.fill(cWhite)
			d.draw(rgb{120, 120, 120}, 0.4)
			p.SetDashPattern([]float64{0.8, 0.8}, 0)
			p.Circle(cx, cy, r, "FD")
			p.SetDashPattern([]float64{}, 0)
			d.color(cMute)
		} else {
			d.fill(cInk)
			p.Circle(cx, cy, r, "F")
			d.color(cWhite)
		}
		d.font("x", 7.4)
		p.SetXY(cx-r, cy-2)
		p.CellFormat(2*r, 4, fmt.Sprintf("%d", n.Rank), "", 0, "C", false, 0, "")
		d.font("s", 7)
		d.color(cInk)
		if n.Kind == "anchor" {
			d.color(cMute)
		}
		lw := 34.0
		ly := cy + r + 0.6
		if n.Lab == "a" {
			ly = cy - r - 4
		}
		// a halo under the label: lines do not cut through the text
		tw := p.GetStringWidth(n.Ru) + 1.6
		d.fill(cSample)
		p.Rect(cx-tw/2, ly+0.2, tw, 3.2, "F")
		p.SetXY(cx-lw/2, ly)
		p.CellFormat(lw, 3.4, n.Ru, "", 0, "C", false, 0, "")
	}
	// legend
	ly := y0 + 4
	lx := x0 + 4
	items := []struct {
		t    string
		dash []float64
		c    rgb
		w    float64
	}{{"усиливают", nil, cInk, 0.9}, {"конфликтуют", []float64{2.2, 1.6}, rgb{90, 90, 90}, 0.55}, {"якорь тянет вниз", []float64{0.5, 1.2}, rgb{120, 120, 120}, 0.45}}
	for _, it := range items {
		d.draw(it.c, it.w)
		p.SetDashPattern(it.dash, 0)
		p.Line(lx, ly+1.6, lx+8, ly+1.6)
		p.SetDashPattern([]float64{}, 0)
		d.font("r", 6.8)
		d.color(cMute)
		p.SetXY(lx+10, ly)
		p.CellFormat(30, 3.4, it.t, "", 0, "L", false, 0, "")
		ly += 4.2
	}
	return h
}

func (d *gdoc) links(head string, items []GallupLink, sceneLabel string) {
	if len(items) == 0 {
		return
	}
	d.need(75) // the heading stays with its first card
	d.font("x", 10.4)
	d.color(cInk)
	d.p.SetXY(mL, d.p.GetY()+1)
	d.p.CellFormat(cW, 5, head, "", 0, "L", false, 0, "")
	d.p.SetY(d.p.GetY() + 7)
	for _, l := range items {
		d.card(mL, cW, l.Title, strings.Join(l.Names, " · "), []part{
			{text: l.Effect},
			{label: sceneLabel, text: l.Scene, shade: true},
			{label: "Что делать", text: l.Fix, fam: "s", size: 8.8},
		})
	}
}

func (d *gdoc) interactions() {
	p, g := d.p, d.g
	if len(g.Map.Nodes) == 0 && len(g.Amplify) == 0 {
		return
	}
	d.sec("Как таланты работают вместе", "Связки, конфликты и слабые таланты, которые подвешивают сильные", cW*380/600)
	if len(g.Map.Nodes) > 0 {
		y := p.GetY()
		h := d.mapImage(mL, y, cW)
		p.SetY(y + h + 3)
		d.takeaway(g.Map.Line)
	}
	d.links("Усиливают друг друга", g.Amplify, "Как это выглядит")
	d.links("Тянут в разные стороны", g.Conflict, "Как это выглядит")
	d.links("Что подвешивает сильные стороны", g.Anchors, "Сцена")
}

func (d *gdoc) state() {
	g := d.g
	if len(g.Best) == 0 && len(g.Stress) == 0 && len(g.Blind) == 0 {
		return
	}
	if len(g.Best) > 0 || len(g.Stress) > 0 {
		d.sec("В лучшей форме и под стрессом", "По этим признакам вы и команда замечаете, в каком вы состоянии", d.colH(g.Best, g.Stress))
		d.columns("В лучшей форме", g.Best, "Под стрессом", g.Stress)
		if len(g.Reset) > 0 {
			d.need(12)
			d.font("x", 9.6)
			d.color(cInk)
			d.p.SetXY(mL, d.p.GetY())
			d.p.CellFormat(cW, 4.6, "Как быстро вернуться в форму", "", 0, "L", false, 0, "")
			d.p.SetY(d.p.GetY() + 6)
			d.bullets(g.Reset, mL, cW)
		}
	}
	if len(g.Blind) > 0 {
		d.sec("Слепые зоны", "Что вы не замечаете и как это проверить", 30)
		for _, b := range g.Blind {
			d.card(mL, cW, b.A, "", []part{{text: b.B}, {label: "Проверка", text: b.C, fam: "s", size: 8.8}})
		}
	}
}

func (d *gdoc) rolesChart(x0, y0, w float64) float64 {
	p, rs := d.p, d.g.Roles
	h := w * 0.62
	pl, pb := 8.0, 11.0
	cw, ch := w-pl, h-pb-5
	ox, oy := x0+pl, y0+5
	// quadrants: right-bottom «ведите сами», right-top «со страховкой»,
	// left-top «отдайте», left-bottom «не приоритет»
	d.fill(cSample)
	p.Rect(ox, oy, cw, ch, "F")
	d.fill(cFill)
	p.Rect(ox+cw/2, oy+ch/2, cw/2, ch/2, "F")
	d.draw(cLine, 0.3)
	p.Rect(ox, oy, cw, ch, "D")
	p.Line(ox+cw/2, oy, ox+cw/2, oy+ch)
	p.Line(ox, oy+ch/2, ox+cw, oy+ch/2)
	q := []struct {
		t    string
		x, y float64
		al   string
	}{{"Отдайте", ox, oy - 4.4, "L"}, {"Ведите со страховкой", ox + cw, oy - 4.4, "R"}, {"Не приоритет", ox, oy + ch + 0.8, "L"}, {"Ведите сами", ox + cw, oy + ch + 0.8, "R"}}
	for _, t := range q {
		d.font("x", 7.2)
		d.color(cMute)
		x := t.x
		if t.al == "R" {
			x -= 50
		}
		p.SetXY(x, t.y)
		p.CellFormat(50, 3.6, strings.ToUpper(t.t), "", 0, t.al, false, 0, "")
	}
	d.font("s", 6.8)
	d.color(cMute)
	p.SetXY(ox, oy+ch+5.4)
	p.CellFormat(cw, 3.4, "Энергия: насколько роль опирается на ваши сильные таланты →", "", 0, "C", false, 0, "")
	p.TransformBegin()
	p.TransformRotate(90, x0+3.2, oy+ch/2)
	p.SetXY(x0+3.2-ch/2, oy+ch/2-1.7)
	p.CellFormat(ch, 3.4, "Риск срыва →", "", 0, "C", false, 0, "")
	p.TransformEnd()
	for _, r := range rs {
		e := math.Max(3, math.Min(97, float64(r.Energy))) // the platform sends the plotted position
		k := math.Max(4, math.Min(96, float64(r.Risk)))
		cx, cy := ox+cw*e/100, oy+ch*(1-k/100)
		d.fill(cInk)
		p.Circle(cx, cy, 2.6, "F")
		d.font("x", 7)
		d.color(cWhite)
		p.SetXY(cx-2.6, cy-1.8)
		p.CellFormat(5.2, 3.6, fmt.Sprintf("%d", r.N), "", 0, "C", false, 0, "")
	}
	return h
}

func (d *gdoc) business() {
	p, g := d.p, d.g
	has := len(g.Keep) > 0 || len(g.Areas) > 0 || len(g.Roles) > 0 || len(g.Business) > 0
	if !has {
		return
	}
	d.sec("Как применять в бизнесе", "Ваша роль собственника: что оставить себе, что отдать, кого взять рядом", 30)
	if len(g.Keep) > 0 || len(g.Delegate) > 0 {
		d.columns("Оставьте себе", g.Keep, "Отдайте", g.Delegate)
	}
	if len(g.Business) > 0 && len(g.Areas) == 0 {
		d.bullets(g.Business, mL, cW)
		p.SetY(p.GetY() + 2)
	}
	if len(g.Areas) > 0 {
		for _, a := range g.Areas {
			d.card(mL, cW, a.A, "", []part{{text: a.B}, {label: "Действие", text: a.C, fam: "s", size: 8.8}})
		}
	}
	if len(g.Hires) > 0 {
		d.need(26)
		d.font("x", 10.4)
		d.color(cInk)
		p.SetXY(mL, p.GetY()+1)
		p.CellFormat(cW, 5, "Кого взять рядом", "", 0, "L", false, 0, "")
		p.SetY(p.GetY() + 7)
		for _, hr := range g.Hires {
			d.card(mL, cW, hr.A, hr.B, []part{{text: hr.C}})
		}
	} else if len(g.Partners) > 0 {
		d.need(20)
		d.font("x", 10.4)
		d.color(cInk)
		p.SetXY(mL, p.GetY()+1)
		p.CellFormat(cW, 5, "Кого взять рядом", "", 0, "L", false, 0, "")
		p.SetY(p.GetY() + 7)
		d.bullets(g.Partners, mL, cW)
	}
	if len(g.Roles) > 0 {
		w := cW * 0.56
		hh := w * 0.62
		d.font("r", 8)
		listH := 0.0
		for _, r := range g.Roles {
			listH += float64(len(d.lines(r.Name+" "+r.Why, cW-w-12)))*3.8 + 1.6
		}
		d.need(maxf(hh, listH) + 22)
		d.font("x", 10.4)
		d.color(cInk)
		p.SetXY(mL, p.GetY()+2)
		p.CellFormat(cW, 5, "Энергия и риск по ролям собственника", "", 0, "L", false, 0, "")
		y := p.GetY() + 8
		d.rolesChart(mL, y, w)
		lx, ly := mL+w+6, y
		for _, r := range g.Roles {
			d.fill(cInk)
			p.Circle(lx+2, ly+1.9, 2, "F")
			d.font("x", 6.4)
			d.color(cWhite)
			p.SetXY(lx, ly+0.3)
			p.CellFormat(4, 3.2, fmt.Sprintf("%d", r.N), "", 0, "C", false, 0, "")
			d.font("s", 8)
			d.color(cInk)
			ny := d.text(lx+5.6, ly, cW-w-12, 3.8, r.Name, "L")
			if r.Why != "" {
				d.font("r", 7.2)
				d.color(cMute)
				ny = d.text(lx+5.6, ny, cW-w-12, 3.4, r.Why, "L")
			}
			ly = ny + 1.6
		}
		p.SetY(maxf(y+hh, ly) + 3)
		d.takeaway(g.RolesLine)
	}
}

func (d *gdoc) risks() {
	g := d.g
	if len(g.Risks) > 0 {
		d.sec("Риски для бизнеса", "Когда включаются, по каким признакам видно заранее и правило профилактики", 30)
		for _, r := range g.Risks {
			d.card(mL, cW, r.A, "", []part{
				{label: "Когда включается", text: r.B},
				{label: "Ранние признаки", text: r.C},
				{label: "Правило", text: r.D, fam: "s", size: 8.8, shade: true},
			})
		}
	}
}

func (d *gdoc) plan() {
	p, g := d.p, d.g
	if len(g.Plan) > 0 {
		d.sec("План на 30 дней", "", 30)
		for i, s := range g.Plan {
			d.font("s", 9.2)
			al := d.lines(s.B, cW-44)
			d.font("r", 8.4)
			rl := d.lines(s.C, cW-44)
			h := float64(len(al))*4.6 + float64(len(rl))*4.1 + 6
			d.need(h + 2)
			y := p.GetY()
			d.fill(cInk)
			p.RoundedRect(mL, y, 7, 7, 1.4, "1234", "F")
			d.font("x", 8.4)
			d.color(cWhite)
			p.SetXY(mL, y+0.2)
			p.CellFormat(7, 6.6, fmt.Sprintf("%d", i+1), "", 0, "C", false, 0, "")
			d.font("s", 8)
			d.color(cMute)
			p.SetXY(mL+10, y+1.4)
			p.CellFormat(28, 4, clip(s.A, 22), "", 0, "L", false, 0, "")
			d.font("s", 9.2)
			d.color(cInk)
			yy := d.text(mL+40, y+1, cW-44, 4.6, s.B, "L")
			if s.C != "" {
				d.font("r", 8.4)
				d.color(cMute)
				yy = d.text(mL+40, yy+0.6, cW-44, 4.1, "Результат: "+s.C, "L")
			}
			d.draw(cHair, 0.25)
			p.Line(mL, maxf(yy, y+7)+2.4, pageW-mL, maxf(yy, y+7)+2.4)
			p.SetY(maxf(yy, y+7) + 4.4)
		}
	}
	if len(g.WorkWith) > 0 {
		d.sec("Как работать со мной", "Для партнёров и команды", 24)
		d.bullets(g.WorkWith, mL, cW)
	}
}

func (d *gdoc) talents() {
	p, g := d.p, d.g
	if len(g.Talents) == 0 {
		return
	}
	d.sec(fmt.Sprintf("Все таланты: %d по силе", len(g.Talents)), "Места 1-10 - опора, 11-24 - по ситуации, 25-34 - закрывают другие люди", 26)
	for _, t := range g.Talents {
		type row struct{ l, t string }
		rows := []row{{"Суть", t.Essence}, {"В бизнесе", t.Business}, {"Слепая зона", t.Blind}, {"Как использовать", t.Manage}}
		lw, tw := 26.0, cW-12-26
		h := 6.0
		d.font("r", 8)
		for _, r := range rows {
			if strings.TrimSpace(r.t) != "" {
				h += float64(len(d.lines(r.t, tw)))*3.8 + 0.8
			}
		}
		d.need(h + 3)
		y := p.GetY()
		if t.Rank <= 10 {
			d.fill(cInk)
			p.RoundedRect(mL, y, 8, 6, 1.2, "1234", "F")
			d.color(cWhite)
		} else {
			d.draw(cLine, 0.3)
			p.RoundedRect(mL, y, 8, 6, 1.2, "1234", "D")
			d.color(cMute)
		}
		d.font("x", 8)
		p.SetXY(mL, y)
		p.CellFormat(8, 6, fmt.Sprintf("%d", t.Rank), "", 0, "C", false, 0, "")
		d.font("x", 10)
		d.color(cInk)
		p.SetXY(mL+12, y+0.6)
		nm := t.Ru
		p.CellFormat(p.GetStringWidth(nm)+2, 5, nm, "", 0, "L", false, 0, "")
		w0 := p.GetStringWidth(nm) + 3
		d.font("s", 7.4)
		d.color(cMute)
		p.SetXY(mL+12+w0, y+1.4)
		p.CellFormat(cW-12-w0, 4, t.Name+" · "+t.DomainRu, "", 0, "L", false, 0, "")
		yy := y + 6.6
		for _, r := range rows {
			if strings.TrimSpace(r.t) == "" {
				continue
			}
			d.font("s", 6.8)
			d.color(cMute)
			p.SetXY(mL+12, yy+0.3)
			p.CellFormat(lw, 3.6, strings.ToUpper(r.l), "", 0, "L", false, 0, "")
			d.font("r", 8)
			d.color(cText)
			yy = d.text(mL+12+lw, yy, tw, 3.8, r.t, "L") + 0.8
		}
		d.draw(cHair, 0.25)
		p.Line(mL, yy+1, pageW-mL, yy+1)
		p.SetY(yy + 2.6)
	}
}
