package http

import (
	"context"
	"encoding/json"
	"math"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/bnursik/business_surgery_backend/internal/club"
	pg "github.com/bnursik/business_surgery_backend/internal/repository/pg"
	"github.com/bnursik/business_surgery_backend/internal/tplpdf"
)

// R51: what a resident's boards and the club say about a period: the
// figures before and after (выручка, прибыль, часы собственника, индекс
// здоровья), the organs, the diagnoses (closed: on an earlier cycle, gone
// now), the tasks, the tools, Gallup. Cases (sales_cases.go) and the period
// report (sales_report.go) are both made from it.

type bNode struct {
	ID    any            `json:"id"`
	Type  string         `json:"type"`
	Role  string         `json:"role"`
	Title string         `json:"title"`
	Organ string         `json:"organ"`
	Task  map[string]any `json:"task"`
	Point map[string]any `json:"point"`
}

type bSnap struct {
	Version int            `json:"version"`
	Date    string         `json:"date"`
	Nodes   []bNode        `json:"nodes"`
	Health  map[string]any `json:"health"`
	Summary map[string]any `json:"summary"`
}

type bState struct {
	Date    string         `json:"date"`
	Updated string         `json:"updated"`
	Version int            `json:"version"`
	Info    map[string]any `json:"info"`
	Health  map[string]any `json:"health"`
	Summary map[string]any `json:"summary"`
	Nodes   []bNode        `json:"nodes"`
	History []bSnap        `json:"history"`
	Gallup  map[string]any `json:"gallup"`
}

// organs as the board keeps them (ORG_MAP on the page).
var salesOrgans = []struct{ Key, Name, Label string }{
	{"brain", "Стратегия", "Мозг"}, {"heart", "Маркетинг", "Сердце"}, {"hands", "Продажи", "Руки"},
	{"spine", "Команда", "Костяк"}, {"blood", "Финансы", "Кровь"}, {"dna", "Процессы", "ДНК"}, {"eyes", "Аналитика", "Зрение"},
}

var numRe = regexp.MustCompile(`-?\d[\d\s\x{00A0},.]*`)

// sNum reads «1 500 000», "1,500,000 ₸", 12.5, "7,5".
func sNum(v any) (float64, bool) {
	switch x := v.(type) {
	case float64:
		return x, true
	case int:
		return float64(x), true
	case int64:
		return float64(x), true
	case string:
		m := numRe.FindString(strings.TrimSpace(x))
		if m == "" {
			return 0, false
		}
		m = strings.Map(func(r rune) rune {
			if r == ' ' || r == ' ' {
				return -1
			}
			return r
		}, m)
		m = strings.TrimRight(m, ",.")
		// "1,500,000" (thousands) vs "7,5" (decimal)
		if strings.Count(m, ",") >= 1 && strings.Count(m, ".") == 0 {
			parts := strings.Split(m, ",")
			if len(parts) == 2 && len(parts[1]) != 3 {
				m = parts[0] + "." + parts[1]
			} else {
				m = strings.ReplaceAll(m, ",", "")
			}
		} else {
			m = strings.ReplaceAll(m, ",", "")
		}
		f, err := strconv.ParseFloat(m, 64)
		return f, err == nil
	}
	return 0, false
}

func healthOf(h map[string]any) map[string]float64 {
	out := map[string]float64{}
	for _, o := range salesOrgans {
		for _, k := range []string{o.Key, o.Name, o.Label} {
			if v, ok := sNum(h[k]); ok && v > 0 {
				out[o.Key] = math.Min(v, 10)
				break
			}
		}
	}
	return out
}

func healthIndex(h map[string]float64) float64 {
	if len(h) == 0 {
		return 0
	}
	sum := 0.0
	for _, v := range h {
		sum += v
	}
	return math.Round(sum / float64(len(salesOrgans)) * 10)
}

func taskDone(t map[string]any) bool {
	if t == nil {
		return false
	}
	if t["completed"] == true {
		return true
	}
	if sStr(t, "kind") == "repeat" {
		d, _ := sNum(t["done"])
		n, ok := sNum(t["times"])
		if !ok || n <= 0 {
			n = 3
		}
		return d >= n
	}
	return false
}

func parseAnyTime(s string) time.Time {
	s = strings.TrimSpace(s)
	for _, l := range []string{time.RFC3339Nano, time.RFC3339, "2006-01-02T15:04:05", "2006-01-02"} {
		if t, err := time.Parse(l, s); err == nil {
			return t
		}
	}
	for _, l := range []string{"02.01.2006 15:04:05", "02.01.2006 15:04", "02.01.2006"} {
		if t, err := time.ParseInLocation(l, s, almaty); err == nil {
			return t
		}
	}
	return time.Time{}
}

type datedVal struct {
	at time.Time
	v  float64
}

// ResPeriod: what the period shows.
type ResPeriod struct {
	Name, Niche, City string
	First             time.Time // the first board
	Cycles            int
	Metrics           []tplpdf.ReportMetric
	Organs            []tplpdf.ReportOrgan
	Diagnoses         []tplpdf.ReportDiag
	TasksDone         int
	TasksTotal        int
	TasksList         []string
	Tools             []string
	Gallup            []string
	OpenTasks         []string
	PointB            string
}

// Closed: the closed diagnoses' titles.
func (p *ResPeriod) Closed() []string {
	var out []string
	for _, d := range p.Diagnoses {
		if d.Closed {
			out = append(out, d.Title)
		}
	}
	return out
}

// MainOrgan: the organ most of the diagnoses are about.
func (p *ResPeriod) MainOrgan() string {
	cnt := map[string]int{}
	for _, d := range p.Diagnoses {
		if d.Organ != "" {
			cnt[d.Organ]++
		}
	}
	ks := sortedKeys(cnt)
	if len(ks) == 0 {
		return ""
	}
	return ks[0]
}

// residentPeriod reads the resident's boards and the profit sheet for [from, to].
func residentPeriod(boards []pg.PlatformBoard, profit [][]string, name string, from, to time.Time) *ResPeriod {
	p := &ResPeriod{Name: name}
	var rev, prof, hours []datedVal
	type hsnap struct {
		at time.Time
		h  map[string]float64
	}
	var hs []hsnap
	type nsnap struct {
		at    time.Time
		nodes []bNode
	}
	var ns []nsnap
	var cur *bState
	var curAt time.Time
	inside := func(t time.Time) bool { return !t.IsZero() && !t.Before(from) && !t.After(to) }
	for i := range boards {
		b := boards[i]
		if b.Deleted || !boardBelongsTo(&b, name) {
			continue
		}
		var st bState
		if json.Unmarshal(b.Data, &st) != nil {
			continue
		}
		created := parseAnyTime(st.Date)
		updated := parseAnyTime(st.Updated)
		if updated.IsZero() {
			updated = b.UpdatedAt
		}
		if created.IsZero() {
			created = updated
		}
		if p.First.IsZero() || created.Before(p.First) {
			p.First = created
		}
		if cur == nil || updated.After(curAt) {
			c := st
			cur, curAt = &c, updated
		}
		if st.Version > p.Cycles {
			p.Cycles = st.Version
		}
		for _, h := range st.History {
			at := parseAnyTime(h.Date)
			if hh := healthOf(h.Health); len(hh) > 0 {
				hs = append(hs, hsnap{at, hh})
			}
			ns = append(ns, nsnap{at, h.Nodes})
			if v, ok := sNum(h.Summary["rev"]); ok && v > 0 {
				rev = append(rev, datedVal{at, v})
			}
			if v, ok := sNum(h.Summary["profit"]); ok && v != 0 {
				prof = append(prof, datedVal{at, v})
			}
			if v, ok := sNum(h.Summary["hours"]); ok && v > 0 {
				hours = append(hours, datedVal{at, v})
			}
		}
		if hh := healthOf(st.Health); len(hh) > 0 {
			hs = append(hs, hsnap{updated, hh})
		}
		ns = append(ns, nsnap{updated, st.Nodes})
		if v, ok := sNum(st.Summary["rev"]); ok && v > 0 {
			rev = append(rev, datedVal{updated, v})
		}
		if v, ok := sNum(st.Summary["profit"]); ok && v != 0 {
			prof = append(prof, datedVal{updated, v})
		}
		if v, ok := sNum(st.Summary["hours"]); ok && v > 0 {
			hours = append(hours, datedVal{updated, v})
		}
		for _, n := range st.Nodes {
			if n.Role == "pointA" && n.Point != nil {
				if v, ok := sNum(n.Point["rev"]); ok && v > 0 {
					rev = append(rev, datedVal{created, v})
				}
				if v, ok := sNum(n.Point["profit"]); ok && v != 0 {
					prof = append(prof, datedVal{created, v})
				}
				if v, ok := sNum(n.Point["hours"]); ok && v > 0 {
					hours = append(hours, datedVal{created, v})
				}
			}
			if n.Role == "pointB" && strings.TrimSpace(n.Title) != "" {
				p.PointB = strings.TrimSpace(n.Title)
			}
		}
	}
	// «Прибыль резидентов»: Дата, Резидент, Выручка, Прибыль
	for i, r := range profit {
		if i == 0 || len(r) < 3 || normName(r[1]) != normName(name) {
			continue
		}
		at := parseAnyTime(r[0])
		if v, ok := sNum(r[2]); ok && v > 0 {
			rev = append(rev, datedVal{at, v})
		}
		if len(r) > 3 {
			if v, ok := sNum(r[3]); ok && v != 0 {
				prof = append(prof, datedVal{at, v})
			}
		}
	}
	if cur == nil {
		return p
	}
	p.Niche, p.City = strings.TrimSpace(sStr(cur.Info, "biz")), strings.TrimSpace(sStr(cur.Info, "city"))

	pick := func(vals []datedVal) (float64, float64, bool) {
		var in []datedVal
		for _, v := range vals {
			if inside(v.at) {
				in = append(in, v)
			}
		}
		if len(in) < 2 {
			// the value right before the period counts as its start
			var before *datedVal
			for i := range vals {
				if !vals[i].at.IsZero() && vals[i].at.Before(from) && (before == nil || vals[i].at.After(before.at)) {
					before = &vals[i]
				}
			}
			if before != nil {
				in = append([]datedVal{*before}, in...)
			}
		}
		if len(in) < 2 {
			return 0, 0, false
		}
		sort.SliceStable(in, func(i, j int) bool { return in[i].at.Before(in[j].at) })
		return in[0].v, in[len(in)-1].v, true
	}
	if a, b, ok := pick(rev); ok {
		p.Metrics = append(p.Metrics, tplpdf.ReportMetric{Label: "Выручка в месяц", Unit: "₸", Before: a, After: b})
	}
	if a, b, ok := pick(prof); ok {
		p.Metrics = append(p.Metrics, tplpdf.ReportMetric{Label: "Чистая прибыль в месяц", Unit: "₸", Before: a, After: b})
	}
	if a, b, ok := pick(hours); ok {
		p.Metrics = append(p.Metrics, tplpdf.ReportMetric{Label: "Часы собственника в неделю", Unit: "ч", Before: a, After: b, Lower: true})
	}
	// health: the first snapshot of the period (or right before it) and the latest
	sort.SliceStable(hs, func(i, j int) bool { return hs[i].at.Before(hs[j].at) })
	var h0, h1 map[string]float64
	for _, h := range hs {
		if h.at.After(to) {
			break
		}
		if !h.at.After(from) {
			h0 = h.h // the latest one at the start
		} else if h0 == nil {
			h0 = h.h // nothing before: the first one inside
		}
		h1 = h.h
	}
	if h0 != nil && h1 != nil {
		a, b := healthIndex(h0), healthIndex(h1)
		if a > 0 || b > 0 {
			p.Metrics = append(p.Metrics, tplpdf.ReportMetric{Label: "Индекс здоровья бизнеса", Unit: "", Before: a, After: b})
		}
		for _, o := range salesOrgans {
			p.Organs = append(p.Organs, tplpdf.ReportOrgan{Name: o.Name, Before: h0[o.Key], After: h1[o.Key]})
		}
	}
	// diagnoses: all of the period; closed = on an earlier snapshot, gone now
	now := map[string]bool{}
	for _, n := range cur.Nodes {
		if n.Type == "diag" {
			now[csNorm(n.Title)] = true
		}
	}
	seen := map[string]bool{}
	sort.SliceStable(ns, func(i, j int) bool { return ns[i].at.Before(ns[j].at) })
	taskSeen := map[string]bool{}
	var taskOrder []string
	toolSeen := map[string]bool{}
	for _, s := range ns {
		if !s.at.IsZero() && s.at.Before(from.AddDate(0, 0, -31)) {
			continue
		}
		for _, n := range s.nodes {
			t := strings.TrimSpace(n.Title)
			if t == "" {
				continue
			}
			switch n.Type {
			case "diag":
				k := csNorm(t)
				if seen[k] {
					continue
				}
				seen[k] = true
				org := n.Organ
				if org == "" {
					org = planForDiag(t, "").Organ
				}
				p.Diagnoses = append(p.Diagnoses, tplpdf.ReportDiag{Title: t, Organ: org, Closed: !now[k]})
			case "task":
				if !inside(s.at) && !s.at.Equal(curAt) && !s.at.IsZero() {
					continue
				}
				k := csNorm(t)
				if _, ok := taskSeen[k]; !ok {
					taskSeen[k] = false
					taskOrder = append(taskOrder, t)
				}
				if taskDone(n.Task) {
					taskSeen[k] = true
				}
			case "tool":
				if !toolSeen[csNorm(t)] {
					toolSeen[csNorm(t)] = true
					p.Tools = append(p.Tools, t)
				}
			}
		}
	}
	p.TasksTotal = len(taskOrder)
	for _, t := range taskOrder {
		if taskSeen[csNorm(t)] {
			p.TasksDone++
			p.TasksList = append(p.TasksList, t)
		}
	}
	for _, n := range cur.Nodes {
		if n.Type == "task" && !taskDone(n.Task) && strings.TrimSpace(n.Title) != "" {
			p.OpenTasks = append(p.OpenTasks, strings.TrimSpace(n.Title))
		}
	}
	// Gallup: the first five themes
	if g := cur.Gallup; g != nil {
		ru := map[string]string{}
		for _, t := range gallupThemes {
			ru[t[0]] = t[2]
		}
		for _, x := range sStrs(g["themes"]) {
			if n := ru[x]; n != "" {
				p.Gallup = append(p.Gallup, n)
			} else {
				p.Gallup = append(p.Gallup, x)
			}
			if len(p.Gallup) == 5 {
				break
			}
		}
	}
	return p
}

// salesSheets: the profit sheet (the server keeps the club's sheets).
type salesSheets interface {
	SheetsByName(ctx context.Context, names []string) (map[string][][]string, error)
}

func (s *ClubSales) profitRows(ctx context.Context) [][]string {
	if sh, ok := s.Boards.(salesSheets); ok {
		if m, err := sh.SheetsByName(ctx, []string{club.SheetProfit}); err == nil {
			return m[club.SheetProfit]
		}
	}
	return nil
}

func (s *ClubSales) period(ctx context.Context, name string, from, to time.Time) *ResPeriod {
	if s.Boards == nil {
		return &ResPeriod{Name: name}
	}
	boards, err := s.Boards.LiveBoards(ctx)
	if err != nil {
		return &ResPeriod{Name: name}
	}
	return residentPeriod(boards, s.profitRows(ctx), name, from, to)
}

// activeResidents: the club's current residents (not former, not the team).
func activeResidents(list []club.Resident) []club.Resident {
	var out []club.Resident
	for _, r := range list {
		if r.Former || r.Archived || r.Admin || strings.TrimSpace(r.Name) == "" {
			continue
		}
		out = append(out, r)
	}
	return out
}
