package tplpdf

// R51 «Итог периода резидента»: a branded A4 report for 3 or 12 months in
// the club. Black cover band, then:
//   1. Главное в цифрах (tiles)
//   2. Что изменилось: метрики было → стало, paired bars drawn in the PDF
//   3. Здоровье бизнеса по органам: before/after bars per organ
//   4. Что диагностировали (closed ones marked)
//   5. Закрытые задачи и инструменты
//   6. Дисциплина: встречи, отчёты, штрафы
//   7. Gallup: сильные стороны
//   8. План на следующий период
//   9. Продление (the prices; the buttons are in the bot and on the platform)

import (
	"bytes"
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/go-pdf/fpdf"
)

// ReportMetric: one figure before and after the period.
type ReportMetric struct {
	Label  string  `json:"label"`
	Unit   string  `json:"unit"` // "₸", "ч", "%", ""
	Before float64 `json:"before"`
	After  float64 `json:"after"`
	// Lower: a smaller value is better (the owner's hours).
	Lower bool `json:"lower,omitempty"`
}

// ReportOrgan: an organ's health 0-10 before and after.
type ReportOrgan struct {
	Name   string  `json:"name"`
	Before float64 `json:"before"`
	After  float64 `json:"after"`
}

// ReportDiag: a diagnosis of the period.
type ReportDiag struct {
	Title  string `json:"title"`
	Organ  string `json:"organ"`
	Closed bool   `json:"closed"`
}

// ReportDoc: everything the period report shows.
type ReportDoc struct {
	Resident string `json:"resident"`
	Niche    string `json:"niche"`
	City     string `json:"city"`
	Months   int    `json:"months"` // 3 or 12
	From     string `json:"from"`   // 01.07.2026
	To       string `json:"to"`
	Draft    bool   `json:"draft"`

	Metrics    []ReportMetric `json:"metrics"`
	Organs     []ReportOrgan  `json:"organs"`
	Diagnoses  []ReportDiag   `json:"diagnoses"`
	TasksDone  int            `json:"tasksDone"`
	TasksTotal int            `json:"tasksTotal"`
	TasksList  []string       `json:"tasksList"`
	Tools      []string       `json:"tools"`
	Cycles     int            `json:"cycles"`
	Meetings   int            `json:"meetings"`
	MeetPlan   int            `json:"meetPlan"`
	ReportDays int            `json:"reportDays"`
	PlanDays   int            `json:"planDays"`
	Fines      int            `json:"fines"`
	Gallup     []string       `json:"gallup"`
	Plan       []string       `json:"plan"`
	Note       string         `json:"note"`

	Q3Price   int64 `json:"q3Price"`
	YearPrice int64 `json:"yearPrice"`
}

// ReportFooter is printed on every page.
const ReportFooter = "Business Surgery · bxclub.kz · итог периода · только для резидента и трекеров"

type rdoc struct {
	*gdoc
	r *ReportDoc
}

// Money: 1 500 000 ₸.
func Money(n int64) string {
	neg := n < 0
	if neg {
		n = -n
	}
	s := fmt.Sprint(n)
	var b strings.Builder
	for i, c := range s {
		if i > 0 && (len(s)-i)%3 == 0 {
			b.WriteByte(' ')
		}
		b.WriteRune(c)
	}
	if neg {
		return "-" + b.String() + " ₸"
	}
	return b.String() + " ₸"
}

// MetricValue: a metric's value as printed.
func MetricValue(v float64, unit string) string {
	switch unit {
	case "₸":
		return Money(int64(math.Round(v)))
	case "":
		if v == math.Trunc(v) {
			return fmt.Sprint(int64(v))
		}
		return strings.Replace(fmt.Sprintf("%.1f", v), ".", ",", 1)
	}
	s := fmt.Sprint(int64(math.Round(v)))
	if v != math.Trunc(v) && math.Abs(v) < 100 {
		s = strings.Replace(fmt.Sprintf("%.1f", v), ".", ",", 1)
	}
	return s + " " + unit
}

// MetricDelta: «+35%» or «-12 ч» (the change, in percent when it makes sense).
func MetricDelta(m ReportMetric) string {
	d := m.After - m.Before
	if d == 0 {
		return "без изменений"
	}
	sign := "+"
	if d < 0 {
		sign = "-"
	}
	if m.Before > 0 && m.Unit != "%" {
		pct := math.Round(math.Abs(d) / m.Before * 100)
		return fmt.Sprintf("%s%d%%", sign, int64(pct))
	}
	return sign + MetricValue(math.Abs(d), m.Unit)
}

func (m ReportMetric) better() bool {
	if m.Lower {
		return m.After < m.Before
	}
	return m.After > m.Before
}

func (r *ReportDoc) clean() {
	fix := func(s *string) { *s = noDash(strings.TrimSpace(*s)) }
	for _, p := range []*string{&r.Resident, &r.Niche, &r.City, &r.From, &r.To, &r.Note} {
		fix(p)
	}
	r.TasksList, r.Tools, r.Gallup, r.Plan = cleanList(r.TasksList), cleanList(r.Tools), cleanList(r.Gallup), cleanList(r.Plan)
	ms := []ReportMetric{}
	for _, m := range r.Metrics {
		fix(&m.Label)
		if m.Label != "" && (m.Before != 0 || m.After != 0) {
			ms = append(ms, m)
		}
	}
	r.Metrics = ms
	ds := []ReportDiag{}
	for _, d := range r.Diagnoses {
		fix(&d.Title)
		fix(&d.Organ)
		if d.Title != "" {
			ds = append(ds, d)
		}
	}
	r.Diagnoses = ds
	if r.Months != 12 {
		r.Months = 3
	}
}

func (r *ReportDoc) periodName() string {
	if r.Months == 12 {
		return "Итог года в клубе"
	}
	return "Итог 3 месяцев в клубе"
}

// RenderReport builds the period report PDF.
func RenderReport(r *ReportDoc) ([]byte, error) {
	if r == nil || strings.TrimSpace(r.Resident) == "" {
		return nil, fmt.Errorf("tplpdf: empty report")
	}
	r.clean()
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
	title := r.periodName() + " · " + r.Resident
	p.SetTitle(title+" · Business Surgery", true)
	p.SetAuthor("Business Surgery", true)
	p.SetSubject("Итог периода резидента: что изменилось в цифрах, диагнозы, задачи, дисциплина, план · bxclub.kz", true)
	p.SetCreator("Business Surgery · bxclub.kz", true)
	p.SetProducer("Business Surgery", true)
	p.SetKeywords("Business Surgery, bxclub.kz, итог, резидент", true)
	now := time.Now().UTC()
	p.SetCreationDate(now)
	p.SetModificationDate(now)
	p.AliasNbPages("{nb}")
	d := &rdoc{gdoc: &gdoc{doc: &doc{p: p}}, r: r}
	p.SetHeaderFuncMode(func() {
		if p.PageNo() == 1 {
			return
		}
		h := 6.0
		p.ImageOptions("lb", mL, 10, h*logoAR, h, false, fpdf.ImageOptions{}, 0, "")
		d.font("s", 7.5)
		d.color(cMute)
		p.SetXY(mL, 11.2)
		hd := r.periodName() + " · " + clip(r.Resident, 40)
		if r.Draft {
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
		p.CellFormat(cW-30, 4, ReportFooter, "", 0, "L", false, 0, "")
		d.font("s", 7)
		p.SetXY(pageW-mL-30, 285)
		p.CellFormat(30, 4, fmt.Sprintf("стр. %d из {nb}", p.PageNo()), "", 0, "R", false, 0, "")
	})
	p.AddPage()
	d.cover()
	d.tiles()
	d.changes()
	d.organs()
	d.diags()
	d.work()
	d.discipline()
	d.gallup()
	d.plan()
	d.renew()
	if err := p.Error(); err != nil {
		return nil, err
	}
	var buf bytes.Buffer
	if err := p.Output(&buf); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func (d *rdoc) cover() {
	p, r := d.p, d.r
	head := r.periodName()
	d.font("x", 21)
	tl := d.lines(head, cW-24)
	bandH := 56.0 + float64(len(tl))*8.5
	d.fill(cInk)
	p.Rect(0, 0, pageW, bandH, "F")
	p.ClipRect(0, 0, pageW, bandH, false)
	cx, cy := pageW-24.0, bandH*0.58
	for i, rr := range []float64{46, 33, 21} {
		v := 44 - i*6
		d.draw(rgb{v, v, v}, 0.3)
		p.Circle(cx, cy, rr, "D")
	}
	p.ClipEnd()
	lh := 11.0
	p.ImageOptions("lw", mL, 13, lh*logoAR, lh, false, fpdf.ImageOptions{}, 0, "")
	d.font("s", 6.8)
	d.color(cSoft)
	p.SetXY(pageW-mL-90, 14.5)
	p.CellFormat(90, 3.5, "БИЗНЕС-ТРЕКИНГ · ИТОГ ПЕРИОДА", "", 0, "R", false, 0, "")
	d.font("s", 8)
	d.color(cWhite)
	p.SetXY(pageW-mL-90, 19)
	p.CellFormat(90, 4, "Business Surgery · bxclub.kz", "", 0, "R", false, 0, "")
	pill := "РЕЗИДЕНТ · " + strings.ToUpper(clip(r.Resident, 40))
	d.font("s", 6.8)
	pw := p.GetStringWidth(pill) + 8
	d.draw(cWhite, 0.3)
	p.RoundedRect(mL, 31, pw, 5.6, 2.8, "1234", "D")
	d.color(cWhite)
	p.SetXY(mL, 31)
	p.CellFormat(pw, 5.6, pill, "", 0, "C", false, 0, "")
	if r.Draft {
		dw := p.GetStringWidth("ЧЕРНОВИК") + 8
		d.fill(cWhite)
		p.RoundedRect(mL+pw+3, 31, dw, 5.6, 2.8, "1234", "F")
		d.color(cInk)
		p.SetXY(mL+pw+3, 31)
		p.CellFormat(dw, 5.6, "ЧЕРНОВИК", "", 0, "C", false, 0, "")
	}
	d.font("x", 21)
	d.color(cWhite)
	y := 41.0
	for _, l := range tl {
		p.SetXY(mL-0.3, y)
		p.CellFormat(cW, 8.5, l, "", 0, "L", false, 0, "")
		y += 8.5
	}
	var meta []string
	if r.From != "" || r.To != "" {
		meta = append(meta, r.From+" - "+r.To)
	}
	for _, s := range []string{r.Niche, r.City} {
		if s != "" {
			meta = append(meta, s)
		}
	}
	if r.Cycles > 0 {
		meta = append(meta, fmt.Sprintf("циклов разбора: %d", r.Cycles))
	}
	d.font("s", 8.6)
	d.color(rgb{170, 170, 170})
	ml := d.lines(strings.Join(meta, " · "), cW-24)
	for i, l := range ml {
		if i > 1 {
			break
		}
		p.SetXY(mL, y+1.5+float64(i)*4.6)
		p.CellFormat(cW, 4.6, l, "", 0, "L", false, 0, "")
	}
	p.SetY(bandH + 7)
}

// tiles: 1. the period in four numbers.
func (d *rdoc) tiles() {
	r := d.r
	closed := 0
	for _, g := range r.Diagnoses {
		if g.Closed {
			closed++
		}
	}
	type tile struct{ v, l string }
	ts := []tile{
		{fmt.Sprintf("%d из %d", r.TasksDone, r.TasksTotal), "задач закрыто"},
		{fmt.Sprintf("%d", r.Meetings), "встреч с трекерами"},
	}
	if r.PlanDays > 0 {
		ts = append(ts, tile{fmt.Sprintf("%d%%", pct(r.ReportDays, r.PlanDays)), "дней с отчётом"})
	}
	ts = append(ts, tile{fmt.Sprintf("%d из %d", closed, len(r.Diagnoses)), "диагнозов закрыто"})
	d.sec("Главное в цифрах", "Период "+r.From+" - "+r.To, 26)
	p := d.p
	gap := 4.0
	n := float64(len(ts))
	tw := (cW - (n-1)*gap) / n
	h := 22.0
	d.need(h + 4)
	y := p.GetY()
	for i, t := range ts {
		x := mL + float64(i)*(tw+gap)
		if i == 0 {
			d.fill(cInk)
		} else {
			d.fill(cFill)
		}
		p.RoundedRect(x, y, tw, h, 2.4, "1234", "F")
		d.font("x", 15)
		if i == 0 {
			d.color(cWhite)
		} else {
			d.color(cInk)
		}
		p.SetXY(x+4, y+4.5)
		p.CellFormat(tw-8, 7, t.v, "", 0, "L", false, 0, "")
		d.font("r", 8)
		if i == 0 {
			d.color(cSoft)
		} else {
			d.color(cMute)
		}
		d.text(x+4, y+13, tw-8, 3.9, t.l, "L")
	}
	p.SetY(y + h + 6)
}

func pct(a, b int) int {
	if b <= 0 {
		return 0
	}
	v := int(math.Round(float64(a) / float64(b) * 100))
	if v > 100 {
		v = 100
	}
	return v
}

// changes: 2. each metric: label, two bars (было, стало) on one scale, the delta.
func (d *rdoc) changes() {
	ms := d.r.Metrics
	if len(ms) == 0 {
		return
	}
	d.sec("Что изменилось", "Было в начале периода → стало сейчас", 30)
	p := d.p
	labelW, deltaW := 50.0, 26.0
	barW := cW - labelW - deltaW - 4
	for _, m := range ms {
		h := 17.0
		d.need(h + 2)
		y := p.GetY()
		mx := math.Max(math.Abs(m.Before), math.Abs(m.After))
		if mx == 0 {
			mx = 1
		}
		d.font("s", 9.4)
		d.color(cInk)
		d.text(mL, y+1, labelW-4, 4.6, m.Label, "L")
		for i, v := range []float64{m.Before, m.After} {
			by := y + 1 + float64(i)*7
			w := math.Max(barW*math.Abs(v)/mx*0.78, 0.8)
			if i == 0 {
				d.fill(cLine)
			} else {
				d.fill(cInk)
			}
			p.RoundedRect(mL+labelW, by, w, 5, 1.2, "1234", "F")
			d.font("s", 7.6)
			if i == 0 {
				d.color(cMute)
			} else {
				d.color(cInk)
			}
			lbl := "было " + MetricValue(v, m.Unit)
			if i == 1 {
				lbl = "стало " + MetricValue(v, m.Unit)
			}
			p.SetXY(mL+labelW+w+2, by+0.4)
			p.CellFormat(barW-w, 4.2, glue(lbl), "", 0, "L", false, 0, "")
		}
		dl := MetricDelta(m)
		d.font("x", 10.5)
		if m.After == m.Before {
			d.color(cMute)
		} else if m.better() {
			d.color(rgb{22, 140, 84})
		} else {
			d.color(rgb{200, 60, 64})
		}
		p.SetXY(mL+cW-deltaW, y+4.5)
		p.CellFormat(deltaW, 5, glue(dl), "", 0, "R", false, 0, "")
		d.draw(cHair, 0.25)
		p.Line(mL, y+h-1, mL+cW, y+h-1)
		p.SetY(y + h + 1)
	}
	p.SetY(p.GetY() + 4)
}

// organs: 3. a bar chart of the 7 organs, before (grey) and after (black).
func (d *rdoc) organs() {
	os := d.r.Organs
	has := false
	for _, o := range os {
		if o.Before > 0 || o.After > 0 {
			has = true
		}
	}
	if !has {
		return
	}
	d.sec("Здоровье бизнеса по органам", "Оценка 0-10: серым начало периода, чёрным сейчас", 74)
	p := d.p
	chartH := 50.0
	d.need(chartH + 22)
	y0 := p.GetY() + 2
	base := y0 + chartH
	axisX := mL + 8
	w := cW - 8
	// grid 0, 5, 10
	for _, v := range []float64{0, 5, 10} {
		yy := base - chartH*v/10
		d.draw(cHair, 0.25)
		p.Line(axisX, yy, mL+cW, yy)
		d.font("r", 6.8)
		d.color(cSoft)
		p.SetXY(mL, yy-1.8)
		p.CellFormat(6, 3.6, fmt.Sprint(int(v)), "", 0, "R", false, 0, "")
	}
	n := float64(len(os))
	slot := w / n
	bw := math.Min(slot*0.28, 7)
	for i, o := range os {
		cx := axisX + slot*float64(i) + slot/2
		for j, v := range []float64{o.Before, o.After} {
			v = math.Max(0, math.Min(10, v))
			hh := chartH * v / 10
			x := cx - bw - 0.6
			if j == 1 {
				x = cx + 0.6
				d.fill(cInk)
			} else {
				d.fill(cLine)
			}
			if hh > 0 {
				p.Rect(x, base-hh, bw, hh, "F")
			}
			d.font("s", 6.6)
			d.color(cMute)
			if j == 1 {
				d.color(cInk)
			}
			p.SetXY(x-1, base-hh-4)
			p.CellFormat(bw+2, 3.4, MetricValue(v, ""), "", 0, "C", false, 0, "")
		}
		d.font("s", 7.2)
		d.color(cInk)
		p.SetXY(cx-slot/2, base+2)
		p.CellFormat(slot, 3.8, o.Name, "", 0, "C", false, 0, "")
		dv := o.After - o.Before
		if o.Before > 0 && dv != 0 {
			d.font("x", 7.2)
			if dv > 0 {
				d.color(rgb{22, 140, 84})
			} else {
				d.color(rgb{200, 60, 64})
			}
			sg := "+"
			if dv < 0 {
				sg = ""
			}
			p.SetXY(cx-slot/2, base+6.2)
			p.CellFormat(slot, 3.6, sg+MetricValue(dv, ""), "", 0, "C", false, 0, "")
		}
	}
	p.SetY(base + 14)
}

// diags: 4. the diagnoses, closed ones marked.
func (d *rdoc) diags() {
	ds := d.r.Diagnoses
	if len(ds) == 0 {
		return
	}
	d.sec("Что диагностировали", "Диагнозы периода: закрытые отмечены", 18)
	p := d.p
	for i, g := range ds {
		tag := "В РАБОТЕ"
		if g.Closed {
			tag = "ЗАКРЫТ"
		}
		d.font("s", 6.4)
		tw := p.GetStringWidth(tag) + 6
		d.font("s", 10)
		tl := d.lines(g.Title, cW-16-tw-4)
		h := 5 + float64(len(tl))*5 + 3
		if g.Organ != "" {
			h += 4.4
		}
		d.need(h + 2.5)
		y := p.GetY()
		d.draw(cLine, 0.3)
		p.RoundedRect(mL, y, cW, h, 2.2, "1234", "D")
		d.fill(cInk)
		p.Rect(mL, y+2.2, 1.2, h-4.4, "F")
		d.font("x", 9)
		d.color(cSoft)
		p.SetXY(mL+4, y+4.4)
		p.CellFormat(8, 4.4, fmt.Sprintf("%02d", i+1), "", 0, "L", false, 0, "")
		d.font("s", 10)
		d.color(cInk)
		yy := d.text(mL+12, y+4, cW-16-tw-4, 5, g.Title, "L")
		if g.Organ != "" {
			d.font("r", 8.2)
			d.color(cMute)
			p.SetXY(mL+12, yy+0.2)
			p.CellFormat(cW-16, 4, "Орган: "+g.Organ, "", 0, "L", false, 0, "")
		}
		d.font("s", 6.4)
		x := mL + cW - 4 - tw
		if g.Closed {
			d.fill(cInk)
			p.RoundedRect(x, y+4.2, tw, 4.8, 2.4, "1234", "F")
			d.color(cWhite)
		} else {
			d.draw(cInk, 0.3)
			p.RoundedRect(x, y+4.2, tw, 4.8, 2.4, "1234", "D")
			d.color(cInk)
		}
		p.SetXY(x, y+4.2)
		p.CellFormat(tw, 4.8, tag, "", 0, "C", false, 0, "")
		p.SetY(y + h + 2.5)
	}
	p.SetY(p.GetY() + 3)
}

func (d *rdoc) bullets(items []string) {
	p := d.p
	for _, it := range items {
		d.font("r", 9.6)
		ls := d.lines(it, cW-7)
		h := float64(len(ls))*4.8 + 1.6
		d.need(h)
		y := p.GetY()
		d.fill(cInk)
		p.Rect(mL+0.4, y+1.6, 1.8, 1.8, "F")
		d.color(cText)
		d.text(mL+5, y, cW-7, 4.8, it, "L")
		p.SetY(y + h)
	}
	p.SetY(p.GetY() + 2)
}

// work: 5. the tasks closed and the tools in work.
func (d *rdoc) work() {
	r := d.r
	if len(r.TasksList) == 0 && len(r.Tools) == 0 {
		return
	}
	d.sec("Закрытые задачи и инструменты", fmt.Sprintf("Закрыто задач: %d из %d", r.TasksDone, r.TasksTotal), 18)
	if len(r.TasksList) > 0 {
		d.labelLine("ЗАКРЫТЫЕ ЗАДАЧИ")
		list := r.TasksList
		if len(list) > 12 {
			list = append(append([]string{}, list[:12]...), fmt.Sprintf("и ещё %d", len(r.TasksList)-12))
		}
		d.bullets(list)
	}
	if len(r.Tools) > 0 {
		d.labelLine("ИНСТРУМЕНТЫ В РАБОТЕ")
		d.chips(r.Tools)
	}
}

func (d *rdoc) labelLine(s string) {
	d.need(10)
	d.font("s", 6.8)
	d.color(cMute)
	d.p.SetXY(mL, d.p.GetY())
	d.p.CellFormat(cW, 4, s, "", 0, "L", false, 0, "")
	d.p.SetY(d.p.GetY() + 5)
}

// chips: short items as rounded chips, wrapped.
func (d *rdoc) chips(items []string) {
	p := d.p
	x, y := mL, p.GetY()
	d.need(8)
	y = p.GetY()
	for _, it := range items {
		d.font("s", 8)
		t := clip(it, 60)
		w := p.GetStringWidth(t) + 8
		if x+w > mL+cW {
			x = mL
			y += 7.4
			if y+7 > bottom {
				p.AddPage()
				y = topNext
			}
		}
		d.fill(cFill)
		p.RoundedRect(x, y, w, 5.8, 2.9, "1234", "F")
		d.color(cInk)
		p.SetXY(x, y)
		p.CellFormat(w, 5.8, t, "", 0, "C", false, 0, "")
		x += w + 2.5
	}
	p.SetY(y + 10)
}

// discipline: 6. meetings, reports and fines as progress bars.
func (d *rdoc) discipline() {
	r := d.r
	d.sec("Дисциплина", "Встречи с трекерами, ежедневные отчёты, штрафы", 34)
	p := d.p
	row := func(label string, a, b int, val string) {
		d.need(12)
		y := p.GetY()
		d.font("s", 9.2)
		d.color(cInk)
		p.SetXY(mL, y)
		p.CellFormat(52, 5, label, "", 0, "L", false, 0, "")
		bx, bw := mL+54, cW-54-34
		d.fill(cFill)
		p.RoundedRect(bx, y+0.6, bw, 4, 2, "1234", "F")
		if b > 0 && a > 0 {
			d.fill(cInk)
			p.RoundedRect(bx, y+0.6, math.Max(bw*math.Min(float64(a)/float64(b), 1), 4), 4, 2, "1234", "F")
		}
		d.font("x", 9.4)
		p.SetXY(mL+cW-32, y)
		p.CellFormat(32, 5, val, "", 0, "R", false, 0, "")
		p.SetY(y + 9)
	}
	mp := r.MeetPlan
	if mp < r.Meetings {
		mp = r.Meetings
	}
	if mp > 0 {
		row("Встречи с трекерами", r.Meetings, mp, fmt.Sprintf("%d из %d", r.Meetings, mp))
	} else {
		row("Встречи с трекерами", r.Meetings, r.Meetings, fmt.Sprint(r.Meetings))
	}
	if r.PlanDays > 0 {
		row("Дни с отчётом", r.ReportDays, r.PlanDays, fmt.Sprintf("%d из %d", r.ReportDays, r.PlanDays))
	}
	d.font("r", 9.2)
	d.color(cText)
	fl := "Штрафов за период нет"
	if r.Fines > 0 {
		fl = fmt.Sprintf("Штрафов за период: %d", r.Fines)
	}
	d.need(8)
	p.SetXY(mL, p.GetY())
	p.CellFormat(cW, 5, fl, "", 0, "L", false, 0, "")
	p.SetY(p.GetY() + 9)
}

// gallup: 7. the resident's top talents.
func (d *rdoc) gallup() {
	g := d.r.Gallup
	if len(g) == 0 {
		return
	}
	d.sec("Gallup: на что опираться", "Ведущие таланты резидента: на них строим следующий период", 16)
	d.chips(g)
}

// plan: 8. what comes next.
func (d *rdoc) plan() {
	r := d.r
	if len(r.Plan) == 0 && r.Note == "" {
		return
	}
	d.sec("План на следующий период", "", 16)
	if r.Note != "" {
		d.takeaway(r.Note)
	}
	if len(r.Plan) > 0 {
		cd := &cdoc{gdoc: d.gdoc, c: &CallDoc{}}
		cd.checks(r.Plan)
	}
}

// renew: 9. the prices of the next period.
func (d *rdoc) renew() {
	r := d.r
	if r.Q3Price <= 0 && r.YearPrice <= 0 {
		return
	}
	p := d.p
	d.need(40)
	y := p.GetY() + 2
	h := 30.0
	d.fill(cInk)
	p.RoundedRect(mL, y, cW, h, 2.6, "1234", "F")
	d.font("s", 6.8)
	d.color(cSoft)
	p.SetXY(mL+8, y+5)
	p.CellFormat(cW-16, 3.8, "ПРОДЛЕНИЕ РЕЗИДЕНТСТВА", "", 0, "L", false, 0, "")
	d.font("s", 10)
	d.color(cWhite)
	p.SetXY(mL+8, y+10.5)
	p.CellFormat(cW-16, 5, "Продлить в 1 клик: кнопки в сообщении бота и на платформе", "", 0, "L", false, 0, "")
	x := mL + 8
	for _, o := range []struct {
		n string
		v int64
	}{{"На 3 месяца", r.Q3Price}, {"На год", r.YearPrice}} {
		if o.v <= 0 {
			continue
		}
		t := o.n + ": " + Money(o.v)
		d.font("x", 10.5)
		w := p.GetStringWidth(glue(t)) + 10
		d.fill(cWhite)
		p.RoundedRect(x, y+18, w, 7, 3.5, "1234", "F")
		d.color(cInk)
		p.SetXY(x, y+18)
		p.CellFormat(w, 7, glue(t), "", 0, "C", false, 0, "")
		x += w + 4
	}
	p.SetY(y + h + 4)
}
