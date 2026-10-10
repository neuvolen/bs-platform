package http

// R55: the lead funnel in numbers (Маркетинг → SMM → Воронка, and one log
// line a day «funnel stats 14d: …»): from the CRM (bs_crm) and the разбор
// slots (bs_slots), for the leads who came in the last N days.
//
//	starts → source known → chose a pain → started / finished the check →
//	opened a guide → warm-up touches → videos → booked → paid → разбор done
//	(the club offer went) → resident

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
)

type FunnelStat struct {
	Days       int            `json:"days"`
	Leads      int            `json:"leads"`      // came to the bot (funnel bot)
	Threads    int            `json:"threads"`    // of them from Threads
	ThreadPost int            `json:"threadPost"` // from a post's own link th_p… (R76: or a magnet post's)
	Magnets    int            `json:"magnets"`    // R76: came for a lead magnet
	NoSource   int            `json:"noSource"`   // /start without a label
	Sources    map[string]int `json:"sources"`    // by source group
	PerDay     map[string]int `json:"perDay"`     // YYYY-MM-DD (Almaty) → leads
	Pain       int            `json:"pain"`
	QuizStart  int            `json:"quizStart"`
	QuizFin    int            `json:"quizFin"`
	Guides     int            `json:"guides"`
	Warm1      int            `json:"warm1"` // got the 1st touch
	Warm3      int            `json:"warm3"` // got the day-7 touch (the разбор invitation)
	WarmAll    int            `json:"warmAll"`
	Touches    int            `json:"touches"`
	Blocked    int            `json:"blocked"`
	Videos     map[string]int `json:"videos"` // step → leads who got it
	Booked     int            `json:"booked"`
	Paid       int            `json:"paid"`
	Razbor     int            `json:"razbor"` // the разбор took place: the offer sequence started
	Won        int            `json:"won"`
}

func leadStart(m map[string]any) (time.Time, bool) {
	for _, k := range []string{"startAt", "lastStart"} {
		if t, err := time.Parse(time.RFC3339, fmt.Sprint(m[k])); err == nil {
			return t, true
		}
	}
	if t, err := time.ParseInLocation("02.01.2006", fmt.Sprint(m["date"]), almaty); err == nil {
		return t.Add(12 * time.Hour), true
	}
	return time.Time{}, false
}

func sourceGroup(src string) string {
	switch {
	case src == "" || src == "<nil>" || src == "Telegram: бот":
		return "Без метки"
	case strings.HasPrefix(src, "Threads"):
		return "Threads"
	case strings.HasPrefix(src, "Instagram"):
		return "Instagram"
	case strings.HasPrefix(src, "Гайд") || strings.HasPrefix(src, "PDF-гайд") || src == "Чек-листы":
		return "Гайды"
	case strings.HasPrefix(src, "Партнёр") || strings.Contains(src, "партн"):
		return "Партнёры"
	}
	if i := strings.Index(src, ":"); i > 0 {
		return src[:i]
	}
	return src
}

// FunnelStats: the leads who started in the last days days.
func (f *LeadFunnel) FunnelStats(ctx context.Context, days int) (*FunnelStat, error) {
	if days <= 0 {
		days = 14
	}
	now := f.now()
	from := now.Add(-time.Duration(days) * 24 * time.Hour)
	st := &FunnelStat{Days: days, Sources: map[string]int{}, PerDay: map[string]int{}, Videos: map[string]int{}}
	d, err := f.docs.GetDoc(ctx, "club", "bs_crm")
	if err != nil {
		return nil, err
	}
	var crm map[string]any
	if d != nil && !d.Deleted {
		_ = json.Unmarshal([]byte(d.Value), &crm)
	}
	paid := map[int64]bool{}
	if sd, err := f.docs.GetDoc(ctx, "club", slotsDoc); err == nil && sd != nil && !sd.Deleted {
		var doc map[string]any
		_ = json.Unmarshal([]byte(sd.Value), &doc)
		list, _ := doc["slots"].([]any)
		for _, v := range list {
			if s, ok := readSlot(v); ok {
				if b := s.booking(); b != nil && b["paid"] == true {
					paid[anyInt(b["tgId"])] = true
				}
			}
		}
	}
	nWarm := len(warmSteps())
	leads, _ := crm["leads"].([]any)
	for _, x := range leads {
		m, _ := x.(map[string]any)
		if m == nil || m["funnel"] != "bot" {
			continue
		}
		t, ok := leadStart(m)
		if !ok || t.Before(from) || t.After(now) {
			continue
		}
		st.Leads++
		st.PerDay[t.In(almaty).Format("2006-01-02")]++
		src := fmt.Sprint(m["source"])
		g := sourceGroup(src)
		st.Sources[g]++
		if g == "Без метки" {
			st.NoSource++
		}
		if g == "Threads" {
			st.Threads++
			if strings.HasPrefix(src, "Threads: пост") || strings.HasPrefix(src, "Threads: магнит") {
				st.ThreadPost++
			}
		}
		if s, _ := m["magnet"].(string); s != "" && strings.Contains(src, "агнит") {
			st.Magnets++
		}
		if p := fmt.Sprint(m["pain"]); p != "" && p != "<nil>" {
			st.Pain++
		}
		qs, qf := false, false
		if qz, _ := m["qz"].(map[string]any); qz != nil {
			for _, v := range qz {
				if q, _ := v.(map[string]any); q != nil {
					qs = qs || q["st"] != nil || q["fin"] != nil
					qf = qf || q["fin"] != nil
				}
			}
		}
		if qs {
			st.QuizStart++
		}
		if qf {
			st.QuizFin++
		}
		if ck, _ := m["ck"].(map[string]any); ck != nil {
			if s, _ := ck["started"].([]any); len(s) > 0 {
				st.Guides++
			}
		}
		w := int(anyInt(m["warm"]))
		st.Touches += w
		if w >= 1 {
			st.Warm1++
		}
		if w >= 3 {
			st.Warm3++
		}
		if w >= nWarm {
			st.WarmAll++
		}
		for _, l := range asList(m["log"]) {
			if e, _ := l.(map[string]any); e != nil && strings.Contains(fmt.Sprint(e["text"]), "Заблокировал бота") {
				st.Blocked++
				break
			}
		}
		if fv, _ := m["fv"].(map[string]any); fv != nil {
			for k := range fv {
				st.Videos[k]++
			}
		}
		col := fmt.Sprint(m["col"])
		if m["razborSlot"] != nil || col == "prepay" || col == "meet" || col == "diag" || col == "won" {
			st.Booked++
		}
		if paid[leadTg(m)] {
			st.Paid++
		}
		if m["pz"] != nil {
			st.Razbor++
		}
		if col == "won" {
			st.Won++
		}
	}
	return st, nil
}

// Line: the stats in one log line.
func (s *FunnelStat) Line() string {
	var src []string
	for k, v := range s.Sources {
		src = append(src, k+" "+strconv.Itoa(v))
	}
	sort.Strings(src)
	var vids []string
	for _, st := range fvSteps {
		if n := s.Videos[st.Key]; n > 0 {
			vids = append(vids, st.Key+" "+strconv.Itoa(n))
		}
	}
	return fmt.Sprintf("leads %d (Threads %d, of them post links %d; no label %d) [%s]; pain %d; check started %d, finished %d; guides opened %d; "+
		"warm-up: 1st touch %d, day-7 invitation %d, all touches %d, touches sent %d, blocked %d; videos [%s]; booked %d, paid %d, разбор done %d, residents %d; magnets %d",
		s.Leads, s.Threads, s.ThreadPost, s.NoSource, strings.Join(src, ", "), s.Pain, s.QuizStart, s.QuizFin, s.Guides,
		s.Warm1, s.Warm3, s.WarmAll, s.Touches, s.Blocked, strings.Join(vids, ", "), s.Booked, s.Paid, s.Razbor, s.Won, s.Magnets)
}

// StatsLoop: one line a day in the log (the first a minute after start).
func (f *LeadFunnel) StatsLoop(ctx context.Context) {
	wait := time.Minute
	for {
		select {
		case <-ctx.Done():
			return
		case <-time.After(wait):
		}
		c, cancel := context.WithTimeout(ctx, 30*time.Second)
		if s, err := f.FunnelStats(c, 14); err == nil {
			log.Printf("funnel stats 14d: %s", s.Line())
		} else {
			log.Printf("funnel stats: %v", err)
		}
		cancel()
		wait = 24 * time.Hour
	}
}

// StatsHTTP: GET /api/v1/platform/funnel/stats?days=14
func (f *LeadFunnel) StatsHTTP(c *gin.Context) {
	days, _ := strconv.Atoi(c.DefaultQuery("days", "14"))
	if days <= 0 || days > 366 {
		days = 14
	}
	s, err := f.FunnelStats(c.Request.Context(), days)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "storage"})
		return
	}
	c.JSON(http.StatusOK, s)
}
