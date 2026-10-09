package tplpdf

// R71: the second half of the Gallup analysis. The server builds it from the
// order of the talents (rules, no AI needed; AI only rewrites some lines):
// the top-5 in the owner's work (sales, team, money, hiring, blind spot,
// burnout, how to talk to you), where all 34 talents sit by domain, the ten
// pairs of the top-5, who should own which function and whom to hire, the
// 10-day experiment and «Чем Business Surgery будет полезен именно вам».

import (
	"fmt"
	"strings"
)

type GallupPlusRow struct {
	L string `json:"l"` // label
	T string `json:"t"` // text
}

type GallupPlusTalent struct {
	Rank   int             `json:"rank"`
	Key    string          `json:"key"`
	Ru     string          `json:"ru"`
	Name   string          `json:"name"`
	Domain string          `json:"domain"`
	Rows   []GallupPlusRow `json:"rows"`
}

type GallupPair struct {
	A     string `json:"a"` // «Стратегия (1)»
	B     string `json:"b"`
	Kind  string `json:"kind"` // synergy | tension
	Title string `json:"title"`
	Text  string `json:"text"`
	Rule  string `json:"rule"`
}

// GallupStrip: one domain and the places of its talents in the profile.
type GallupStrip struct {
	Key   string `json:"key"`
	Ru    string `json:"ru"`
	Ranks []int  `json:"ranks"`
	Score int    `json:"score"`
	Top5  int    `json:"top5"`
	Top10 int    `json:"top10"`
}

type GallupSplit struct {
	Area  string `json:"area"`
	Who   string `json:"who"`
	Level int    `json:"level"` // 1 вы, 2 вы со страховкой, 3 партнёр, 4 нанятый
	Why   string `json:"why"`
}

type GallupHireProfile struct {
	Role    string   `json:"role"`
	Talents []string `json:"talents"`
	Why     string   `json:"why"`
	Ask     []string `json:"ask"`
	Red     string   `json:"red"`
}

type GallupMech struct {
	Key   string `json:"key"`
	Title string `json:"title"`
	What  string `json:"what"`
	You   string `json:"you"`
}

type GallupDiag struct {
	ID    string   `json:"id"`
	Title string   `json:"title"`
	Organ string   `json:"organ"`
	Sub   string   `json:"sub"`
	Why   string   `json:"why"`
	Tools []string `json:"tools"`
}

type GallupClub struct {
	Intro     string         `json:"intro"`
	Mechanics []GallupMech   `json:"mechanics"`
	Diags     []GallupDiag   `json:"diags"`
	Tools     []string       `json:"tools"`
	First     []GallupTriple `json:"first"` // a task, b talent, c result
}

type GallupPlus struct {
	Top   []GallupPlusTalent `json:"top"`
	Strip []GallupStrip      `json:"strip"`
	Pairs []GallupPair       `json:"pairs"`
	Split []GallupSplit      `json:"split"`
	Hire  *GallupHireProfile `json:"hire,omitempty"`
	Talk  []string           `json:"talk"`
	Exp   []GallupTriple     `json:"exp"` // a when, b action, c result
	Club  *GallupClub        `json:"club,omitempty"`
	N     int                `json:"n"` // talents in the profile
	AI    bool               `json:"ai"`
}

func (d *gdoc) plus() *GallupPlus { return d.g.Plus }

// topWork: the top-5 in the owner's work, one card per talent.
func (d *gdoc) topWork() {
	x := d.plus()
	if x == nil || len(x.Top) == 0 {
		return
	}
	d.sec("Пятёрка в работе собственника", "Как каждый из первых пяти талантов проявляется в продажах, команде, деньгах и найме, где его слепое пятно и что вас выжигает", 60)
	for _, t := range x.Top {
		var parts []part
		for _, r := range t.Rows {
			pt := part{label: r.L, text: r.T}
			if r.L == "Как с вами говорить" {
				pt.fam, pt.size, pt.shade = "s", 8.6, true
			}
			parts = append(parts, pt)
		}
		d.card(mL, cW, fmt.Sprintf("%d. %s", t.Rank, t.Ru), t.Name+" · "+t.Domain, parts)
	}
}

// strip: all talents by domain, one row per domain, a cell per place 1-34.
func (d *gdoc) strip() {
	x := d.plus()
	if x == nil || len(x.Strip) == 0 || x.N < 10 {
		return
	}
	p := d.p
	n := x.N
	labW := 44.0
	cell := (cW - labW) / float64(n)
	h := float64(len(x.Strip))*8.2 + 19
	d.need(h + 6)
	d.font("x", 10.4)
	d.color(cInk)
	p.SetXY(mL, p.GetY()+1)
	p.CellFormat(cW, 5, "Где стоят ваши таланты: места 1-"+fmt.Sprint(n)+" по доменам", "", 0, "L", false, 0, "")
	y := p.GetY() + 11
	// zones: 1-10 опора, 11-24 по ситуации, 25-34 отдать
	zones := []struct {
		from, to int
		t        string
	}{{1, 10, "ОПОРА 1-10"}, {11, 24, "ПО СИТУАЦИИ"}, {25, n, "ОТДАТЬ ДРУГИМ"}}
	for _, z := range zones {
		if z.from > n {
			continue
		}
		x0 := mL + labW + float64(z.from-1)*cell
		w := float64(z.to-z.from+1) * cell
		if z.from == 1 {
			d.fill(cFill)
			p.Rect(x0, y, w, float64(len(x.Strip))*8.2, "F")
		}
		d.font("s", 6.2)
		d.color(cMute)
		p.SetXY(x0, y-4)
		p.CellFormat(w, 3.4, z.t, "", 0, "C", false, 0, "")
	}
	for i, s := range x.Strip {
		yy := y + float64(i)*8.2
		d.font("x", 8.4)
		d.color(cInk)
		p.SetXY(mL, yy+0.6)
		p.CellFormat(labW, 4, s.Ru, "", 0, "L", false, 0, "")
		d.font("r", 6.6)
		d.color(cMute)
		p.SetXY(mL, yy+4.4)
		p.CellFormat(labW, 3.2, fmt.Sprintf("в топ-5: %d · в топ-10: %d", s.Top5, s.Top10), "", 0, "L", false, 0, "")
		d.draw(cHair, 0.2)
		p.Line(mL+labW, yy+7.6, mL+cW, yy+7.6)
		for _, r := range s.Ranks {
			if r < 1 || r > n {
				continue
			}
			cx := mL + labW + (float64(r)-0.5)*cell
			rr := minf(1.8, cell/2-0.15)
			if r > 10 {
				rr = minf(1.6, cell/2-0.25)
			}
			if r <= 10 {
				d.fill(cInk)
				p.Circle(cx, yy+3.8, rr, "F")
				d.font("x", 4.8)
				d.color(cWhite)
			} else {
				d.fill(cWhite)
				d.draw(cSoft, 0.3)
				p.Circle(cx, yy+3.8, rr, "FD")
				d.font("s", 4.4)
				d.color(cMute)
			}
			p.SetXY(cx-rr, yy+2.2)
			p.CellFormat(2*rr, 3.2, fmt.Sprint(r), "", 0, "C", false, 0, "")
		}
	}
	p.SetY(y + float64(len(x.Strip))*8.2 + 4)
}

// pairs: the ten pairs of the top-5.
func (d *gdoc) pairs() {
	x := d.plus()
	if x == nil || len(x.Pairs) == 0 {
		return
	}
	d.sec("Сочетания вашей пятёрки", "Каждая пара из первых пяти талантов: где они усиливают друг друга, где спорят, и правило на каждый случай", 40)
	gw := 4.0
	w := (cW - gw) / 2
	for i := 0; i < len(x.Pairs); i += 2 {
		row := x.Pairs[i:min(i+2, len(x.Pairs))]
		hs := make([]float64, len(row))
		for j, pr := range row {
			hs[j] = d.pairH(w, pr)
		}
		h := hs[0]
		if len(hs) > 1 {
			h = maxf(h, hs[1])
		}
		d.need(h + 3)
		y := d.p.GetY()
		for j, pr := range row {
			d.pairCard(mL+float64(j)*(w+gw), y, w, h, pr)
		}
		d.p.SetY(y + h + 3)
	}
}

func pairKind(k string) string {
	if k == "tension" {
		return "СПОРЯТ"
	}
	return "УСИЛИВАЮТ"
}

func (d *gdoc) pairH(w float64, pr GallupPair) float64 {
	iw := w - 8
	h := 4.0 + 4 + 1
	d.font("s", 7.4)
	h += float64(len(d.lines(pr.A+" + "+pr.B, iw))) * 3.6
	d.font("x", 9.2)
	h += float64(len(d.lines(pr.Title, iw)))*4.4 + 1
	d.font("r", 8)
	h += float64(len(d.lines(pr.Text, iw)))*3.9 + 1.5
	d.font("s", 7.8)
	h += float64(len(d.lines(pr.Rule, iw-3)))*3.8 + 4
	return h
}

func (d *gdoc) pairCard(x, y, w, h float64, pr GallupPair) {
	p := d.p
	iw := w - 8
	if pr.Kind == "tension" {
		d.draw(cSoft, 0.35)
		p.SetDashPattern([]float64{1.6, 1.2}, 0)
		p.RoundedRect(x, y, w, h, 2.2, "1234", "D")
		p.SetDashPattern([]float64{}, 0)
	} else {
		d.draw(cInk, 0.45)
		p.RoundedRect(x, y, w, h, 2.2, "1234", "D")
	}
	yy := y + 4
	d.font("s", 6.4)
	d.color(cMute)
	p.SetXY(x+4, yy)
	p.CellFormat(iw, 3.4, pairKind(pr.Kind), "", 0, "L", false, 0, "")
	yy += 4
	d.font("s", 7.4)
	d.color(cMute)
	yy = d.text(x+4, yy, iw, 3.6, pr.A+" + "+pr.B, "L")
	d.font("x", 9.2)
	d.color(cInk)
	yy = d.text(x+4, yy+0.6, iw, 4.4, pr.Title, "L") + 0.4
	d.font("r", 8)
	d.color(cText)
	yy = d.text(x+4, yy, iw, 3.9, pr.Text, "L") + 1.5
	d.font("s", 7.8)
	ls := d.lines(pr.Rule, iw-3)
	d.fill(cFill)
	p.Rect(x+4, yy, iw, float64(len(ls))*3.8+2, "F")
	d.color(cInk)
	d.text(x+5.5, yy+1, iw-3, 3.8, pr.Rule, "L")
}

// split: who owns which function, the person to hire, how to talk to you.
func (d *gdoc) split() {
	x := d.plus()
	if x == nil || (len(x.Split) == 0 && x.Hire == nil) {
		return
	}
	p := d.p
	d.sec("Распределение ролей и кого искать", "Какие функции компании держать самому, какие со страховкой, а какие отдать партнёру или нанятому человеку", 50)
	if len(x.Split) > 0 {
		lw, ww := 62.0, 38.0
		tw := cW - lw - ww
		for _, s := range x.Split {
			d.font("r", 8.4)
			h := maxf(float64(len(d.lines(s.Why, tw-4)))*4.1, 8) + 4
			d.need(h + 1)
			y := p.GetY()
			d.font("x", 8.8)
			d.color(cInk)
			d.text(mL, y+2, lw-4, 4.2, s.Area, "L")
			// level: four cells, filled up to the owner's share
			for i := 0; i < 4; i++ {
				cx := mL + lw + float64(i)*5.2
				if 4-i >= s.Level {
					d.fill(cInk)
				} else {
					d.fill(cHair)
				}
				p.Rect(cx, y+2.4, 4.2, 3, "F")
			}
			d.font("s", 7.4)
			d.color(cText)
			p.SetXY(mL+lw, y+6.2)
			p.CellFormat(ww, 3.6, s.Who, "", 0, "L", false, 0, "")
			d.font("r", 8.4)
			d.color(cText)
			d.text(mL+lw+ww, y+2, tw, 4.1, s.Why, "L")
			d.draw(cHair, 0.25)
			p.Line(mL, y+h, pageW-mL, y+h)
			p.SetY(y + h + 1)
		}
		p.SetY(p.GetY() + 3)
	}
	if h := x.Hire; h != nil {
		parts := []part{{text: h.Why}}
		if len(h.Ask) > 0 {
			parts = append(parts, part{label: "Вопросы на собеседовании", text: "1. " + strings.Join(h.Ask, "\n2. ")})
		}
		if h.Red != "" {
			parts = append(parts, part{label: "Красный флаг", text: h.Red, fam: "s", size: 8.6, shade: true})
		}
		chips := ""
		if len(h.Talents) > 0 {
			chips = "Таланты в топе у кандидата: " + strings.Join(h.Talents, " · ")
		}
		d.card(mL, cW, "Кого взять рядом: "+h.Role, chips, parts)
	}
	if len(x.Talk) > 0 {
		d.need(20)
		d.font("x", 10.4)
		d.color(cInk)
		p.SetXY(mL, p.GetY()+1)
		p.CellFormat(cW, 5, "Как с вами говорить команде и партнёрам", "", 0, "L", false, 0, "")
		p.SetY(p.GetY() + 7)
		d.bullets(x.Talk, mL, cW)
	}
}

// experiment: the 10-day plan built on the top talents.
func (d *gdoc) experiment() {
	x := d.plus()
	if x == nil || len(x.Exp) == 0 {
		return
	}
	p := d.p
	d.sec("Эксперимент на 10 дней", "Один цикл клуба, в котором вы работаете через свои сильные таланты и проверяете результат цифрой", 40)
	// a timeline of the ten days
	d.need(16)
	y := p.GetY() + 2
	cw := cW / 10
	for i := 0; i < 10; i++ {
		cx := mL + float64(i)*cw
		if i == 0 || i == 4 || i == 9 {
			d.fill(cInk)
			d.color(cWhite)
		} else {
			d.fill(cFill)
			d.color(cInk)
		}
		p.Rect(cx+0.4, y, cw-0.8, 6, "F")
		d.font("x", 7.4)
		p.SetXY(cx, y+1.2)
		p.CellFormat(cw, 3.6, fmt.Sprintf("день %d", i+1), "", 0, "C", false, 0, "")
	}
	d.font("s", 6.6)
	d.color(cMute)
	for _, m := range []struct {
		i int
		t string
	}{{0, "старт"}, {4, "середина"}, {9, "разбор"}} {
		p.SetXY(mL+float64(m.i)*cw, y+6.8)
		p.CellFormat(cw, 3.4, m.t, "", 0, "C", false, 0, "")
	}
	p.SetY(y + 13)
	for i, s := range x.Exp {
		d.font("s", 9)
		al := d.lines(s.B, cW-44)
		d.font("r", 8.2)
		rl := d.lines(s.C, cW-44)
		h := float64(len(al))*4.5 + float64(len(rl))*4 + 6
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
		d.font("s", 9)
		d.color(cInk)
		yy := d.text(mL+40, y+1, cW-44, 4.5, s.B, "L")
		if s.C != "" {
			d.font("r", 8.2)
			d.color(cMute)
			yy = d.text(mL+40, yy+0.6, cW-44, 4, s.C, "L")
		}
		d.draw(cHair, 0.25)
		p.Line(mL, maxf(yy, y+7)+2.4, pageW-mL, maxf(yy, y+7)+2.4)
		p.SetY(maxf(yy, y+7) + 4.4)
	}
}

// club: «Чем Business Surgery будет полезен именно вам».
func (d *gdoc) club() {
	x := d.plus()
	if x == nil || x.Club == nil {
		return
	}
	c := x.Club
	p := d.p
	d.n++
	// a black band: the section the resident reads first
	d.font("r", 9.4)
	il := d.lines(c.Intro, cW-16)
	bh := 24 + float64(len(il))*4.8
	d.need(bh + 40)
	y := p.GetY() + 2
	d.fill(cInk)
	p.RoundedRect(mL, y, cW, bh, 2.6, "1234", "F")
	d.font("s", 6.8)
	d.color(cSoft)
	p.SetXY(mL+8, y+6)
	p.CellFormat(cW-16, 3.4, fmt.Sprintf("%d · ДЛЯ ВАС ЛИЧНО", d.n), "", 0, "L", false, 0, "")
	d.font("x", 14)
	d.color(cWhite)
	p.SetXY(mL+8, y+10.5)
	p.CellFormat(cW-16, 6.5, "Чем Business Surgery будет полезен именно вам", "", 0, "L", false, 0, "")
	d.font("r", 9.4)
	d.color(rgb{225, 225, 225})
	d.text(mL+8, y+19, cW-16, 4.8, c.Intro, "L")
	p.SetY(y + bh + 5)
	for _, m := range c.Mechanics {
		d.card(mL, cW, m.Title, "", []part{{text: m.What, size: 8.4, fam: "r"}, {label: "Для вашего профиля", text: m.You, fam: "s", size: 8.8, shade: true}})
	}
	if len(c.Diags) > 0 {
		d.need(40)
		d.font("x", 10.4)
		d.color(cInk)
		p.SetXY(mL, p.GetY()+2)
		p.CellFormat(cW, 5, "Диагнозы, к которым склонен ваш профиль", "", 0, "L", false, 0, "")
		d.font("r", 8)
		d.color(cMute)
		p.SetY(p.GetY() + 6)
		d.text(mL, p.GetY(), cW, 4, "Из библиотеки клуба. Это вероятности по талантам: трекеры проверят их на разборе по вашим цифрам.", "L")
		p.SetY(p.GetY() + 6)
		for _, g := range c.Diags {
			parts := []part{{label: "Почему у вас", text: g.Why}}
			if len(g.Tools) > 0 {
				parts = append(parts, part{label: "Первые инструменты", text: strings.Join(g.Tools, " · "), fam: "s", size: 8.6, shade: true})
			}
			d.card(mL, cW, g.Title, strings.ToUpper(g.Organ), parts)
		}
	}
	if len(c.First) > 0 {
		d.need(30)
		d.font("x", 10.4)
		d.color(cInk)
		p.SetXY(mL, p.GetY()+2)
		p.CellFormat(cW, 5, "Как может выглядеть ваш первый цикл", "", 0, "L", false, 0, "")
		p.SetY(p.GetY() + 8)
		for i, s := range c.First {
			d.font("s", 9)
			al := d.lines(s.A, cW-12)
			d.font("r", 8.2)
			rl := d.lines(strings.TrimSpace(s.B+" "+s.C), cW-12)
			h := float64(len(al))*4.5 + float64(len(rl))*4 + 4
			d.need(h + 2)
			yy := p.GetY()
			d.draw(cInk, 0.4)
			p.Circle(mL+3, yy+2.4, 2.6, "D")
			d.font("x", 7.6)
			d.color(cInk)
			p.SetXY(mL+0.4, yy+0.6)
			p.CellFormat(5.2, 3.6, fmt.Sprint(i+1), "", 0, "C", false, 0, "")
			d.font("s", 9)
			ny := d.text(mL+9, yy, cW-12, 4.5, s.A, "L")
			d.font("r", 8.2)
			d.color(cMute)
			ny = d.text(mL+9, ny+0.4, cW-12, 4, strings.TrimSpace(s.B+" "+s.C), "L")
			p.SetY(ny + 3)
		}
	}
}
