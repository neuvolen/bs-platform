package http

import (
	"math"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

// R39: «NPS неправильно считает, что там за огромная цифра». The sheet «NPS
// ответы» has Chat ID in the third column, and the seed took it as the
// score: the insight averaged 354788446. Now:
//
//	NPS = % promoters (9-10) − % detractors (0-6), from −100 to 100
//
// on the latest survey only (answers within npsSurveyWindow of the newest
// one), one answer per respondent (the newest), the score parsed from a
// string ("9", "9/10", "8 из 10", "оценка: 7"), and a value outside 0..10
// (a Chat ID) is not a score. The platform (renderNPS) counts the same way.

const npsSurveyWindow = 21 * 24 * time.Hour

// NPSResult: the latest survey.
type NPSResult struct {
	NPS        int     `json:"nps"`
	Avg        float64 `json:"avg"`
	Count      int     `json:"count"` // respondents with a score
	Answers    int     `json:"answers"`
	Promoters  int     `json:"promoters"`
	Passives   int     `json:"passives"`
	Detractors int     `json:"detractors"`
	From, To   string  `json:"-"`
}

var (
	npsOfTen  = regexp.MustCompile(`(?i)\b(10|[0-9])\s*(?:/|из|из\s+10|of)\s*10\b`)
	npsLabel  = regexp.MustCompile(`(?i)(?:оценк[аиу]|балл|nps|score)\D{0,12}?\b(10|[0-9])\b`)
	npsLeadNo = regexp.MustCompile(`^\s*(10|[0-9])\s*(?:$|[.,!)\-:\s])`)
)

// npsScore: a 0..10 score from a number or a string; ok false when none.
func npsScore(v any) (int, bool) {
	switch x := v.(type) {
	case float64:
		if x >= 0 && x <= 10 && x == math.Trunc(x) {
			return int(x), true
		}
		return 0, false
	case int:
		if x >= 0 && x <= 10 {
			return x, true
		}
		return 0, false
	case string:
		s := strings.TrimSpace(x)
		if s == "" {
			return 0, false
		}
		if n, err := strconv.ParseFloat(strings.Replace(s, ",", ".", 1), 64); err == nil {
			return npsScore(n)
		}
		for _, re := range []*regexp.Regexp{npsOfTen, npsLabel, npsLeadNo} {
			if m := re.FindStringSubmatch(s); m != nil {
				n, _ := strconv.Atoi(m[1])
				return n, true
			}
		}
	}
	return 0, false
}

func npsDate(s string) time.Time {
	s = strings.TrimSpace(s)
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return t
	}
	head := s
	if len(head) > 10 {
		head = head[:10]
	}
	for _, f := range []string{"02.01.2006", "2006-01-02", "2.1.2006"} {
		if t, err := time.ParseInLocation(f, head, almaty); err == nil {
			return t
		}
	}
	return time.Time{}
}

func npsStr(m map[string]any, keys ...string) string {
	for _, k := range keys {
		switch v := m[k].(type) {
		case string:
			if strings.TrimSpace(v) != "" {
				return strings.TrimSpace(v)
			}
		case float64:
			return strconv.FormatFloat(v, 'f', -1, 64)
		}
	}
	return ""
}

// ComputeNPS: the latest survey of the answers (SDATA.nps rows: date, res,
// score, comment; or the sheet's: date, name, chatId, text).
func ComputeNPS(rows []map[string]any) NPSResult {
	type ans struct {
		who   string
		at    time.Time
		i     int
		score int
		ok    bool
	}
	var all []ans
	newest := time.Time{}
	for i, r := range rows {
		if r == nil {
			continue
		}
		a := ans{i: i, at: npsDate(npsStr(r, "date"))}
		a.who = strings.ToLower(npsStr(r, "chatId", "tg", "res", "name"))
		if a.who == "" {
			a.who = "#" + strconv.Itoa(i)
		}
		if s, ok := npsScore(r["score"]); ok {
			a.score, a.ok = s, true
		} else if s, ok := npsScore(npsStr(r, "text", "comment", "answer")); ok {
			a.score, a.ok = s, true
		}
		if a.at.After(newest) {
			newest = a.at
		}
		all = append(all, a)
	}
	var res NPSResult
	if !newest.IsZero() {
		res.From, res.To = newest.Add(-npsSurveyWindow).Format("02.01.2006"), newest.Format("02.01.2006")
	}
	// newest first, then one per respondent
	sort.SliceStable(all, func(i, j int) bool {
		if !all[i].at.Equal(all[j].at) {
			return all[i].at.After(all[j].at)
		}
		return all[i].i > all[j].i
	})
	seen := map[string]bool{}
	sum := 0
	for _, a := range all {
		if !newest.IsZero() && !a.at.IsZero() && newest.Sub(a.at) > npsSurveyWindow {
			continue
		}
		if seen[a.who] {
			continue
		}
		seen[a.who] = true
		res.Answers++
		if !a.ok {
			continue
		}
		res.Count++
		sum += a.score
		switch {
		case a.score >= 9:
			res.Promoters++
		case a.score >= 7:
			res.Passives++
		default:
			res.Detractors++
		}
	}
	if res.Count > 0 {
		res.NPS = int(math.Round(float64(res.Promoters-res.Detractors) * 100 / float64(res.Count)))
		res.Avg = math.Round(float64(sum)/float64(res.Count)*10) / 10
	}
	return res
}
