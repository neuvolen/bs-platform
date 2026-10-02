package club

import (
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"
	"time"
)

// BundleMismatch is one field where the script's bundle and the server's differ.
type BundleMismatch struct {
	Field  string `json:"field"`
	Role   string `json:"role,omitempty"`
	Sheet  string `json:"sheet"`
	Server string `json:"server"`
}

// What identifies a row in each list: the lists are compared as sets, so the
// order rows come in does not matter.
var bundleListKeys = map[string][]string{
	"residents":     {"name"},
	"fines":         {"name", "kind", "amount", "date"},
	"logs":          {"date", "time", "name"},
	"schedule":      {"res", "date", "time"},
	"doneMeetings":  {"res", "date"},
	"adminProfiles": {"chatId"},
}

// Fields not compared, and why:
//   - doneMeetings.time: the script writes the cell's Date as JavaScript's
//     String(date) ("Wed May 27 2026 05:35:00 GMT+0500 (…)"), which depends
//     on the script's locale; the app does not show it.
var bundleSkip = map[string]bool{"doneMeetings.time": true}

// logsMargin: reports this close to the 30-day edge may fall on either side,
// the two bundles are not built at the same second.
const logsMargin = 2 * time.Hour

// DiffBundle compares the given sections of the script's bundle (sheet) with
// the server's, after the usual clean-up: order of rows, spaces around text,
// numbers written differently, null against missing. now is when the
// bundles were built.
func DiffBundle(sheet, server map[string]any, sections []string, now time.Time) []BundleMismatch {
	var out []BundleMismatch
	add := func(field string, a, b any) {
		out = append(out, BundleMismatch{Field: field, Sheet: show(a), Server: show(b)})
	}
	for _, sec := range sections {
		a, b := sheet[sec], server[sec]
		keys, keyed := bundleListKeys[sec]
		la, okA := a.([]any)
		lb, okB := b.([]any)
		if keyed && okA && okB {
			if sec == "logs" {
				edge := now.Add(-30 * 24 * time.Hour)
				la, lb = dropNearEdge(la, edge), dropNearEdge(lb, edge)
			}
			ma, order := keyRows(la, keys)
			mb, orderB := keyRows(lb, keys)
			if sec == "logs" {
				pairNearMinute(ma, mb)
			}
			for _, k := range orderB {
				if _, ok := ma[k]; !ok {
					order = append(order, k)
				}
			}
			for _, k := range order {
				ra, inA := ma[k]
				rb, inB := mb[k]
				field := sec + "[" + k + "]"
				switch {
				case !inA && !inB: // paired under the other side's key
				case !inA:
					add(field, nil, "есть")
				case !inB:
					add(field, "есть", nil)
				default:
					diffValue(sec, field, ra, rb, add)
				}
			}
			continue
		}
		diffValue(sec, sec, a, b, add)
	}
	return out
}

// pairNearMinute: a report shown at 23:09 on one side and 23:08 on the other
// is the same report (the sheet shows the minute rounded, the script cuts the
// seconds). The server's row is put under the sheet's key.
func pairNearMinute(ma, mb map[string]map[string]any) {
	at := func(row map[string]any) (time.Time, bool) {
		d, _ := row["date"].(string)
		t, _ := row["time"].(string)
		return Date(d + " " + t)
	}
	for kb, rb := range mb {
		if _, ok := ma[kb]; ok {
			continue
		}
		tb, ok := at(rb)
		if !ok {
			continue
		}
		for ka, ra := range ma {
			if _, taken := mb[ka]; taken || scalar(ra["name"]) != scalar(rb["name"]) {
				continue
			}
			if ta, ok := at(ra); ok && ta.Sub(tb).Abs() <= time.Minute {
				delete(mb, kb)
				rb2 := map[string]any{}
				for k, v := range rb {
					rb2[k] = v
				}
				rb2["time"] = ra["time"]
				mb[ka] = rb2
				break
			}
		}
	}
}

func dropNearEdge(list []any, edge time.Time) []any {
	out := list[:0:0]
	for _, x := range list {
		m, _ := x.(map[string]any)
		d, _ := m["date"].(string)
		t, _ := m["time"].(string)
		if at, ok := Date(d + " " + t); ok && at.Sub(edge).Abs() < logsMargin {
			continue
		}
		out = append(out, x)
	}
	return out
}

// keyRows indexes rows by their key; repeated keys get "#2", "#3".
func keyRows(list []any, keys []string) (map[string]map[string]any, []string) {
	m := map[string]map[string]any{}
	var order []string
	seen := map[string]int{}
	for _, x := range list {
		row, _ := x.(map[string]any)
		parts := make([]string, len(keys))
		for i, k := range keys {
			parts[i] = scalar(row[k])
		}
		k := strings.Join(parts, "|")
		seen[k]++
		if seen[k] > 1 {
			k += "#" + strconv.Itoa(seen[k])
		}
		m[k] = row
		order = append(order, k)
	}
	return m, order
}

// diffValue compares two values deeply; path is where they are, sec the
// section-relative name used for the skip list.
func diffValue(sec, path string, a, b any, add func(string, any, any)) {
	ma, okA := a.(map[string]any)
	mb, okB := b.(map[string]any)
	if okA && okB {
		keys := map[string]bool{}
		for k := range ma {
			keys[k] = true
		}
		for k := range mb {
			keys[k] = true
		}
		names := make([]string, 0, len(keys))
		for k := range keys {
			names = append(names, k)
		}
		sort.Strings(names)
		for _, k := range names {
			if bundleSkip[sec+"."+k] {
				continue
			}
			diffValue(sec+"."+k, path+"."+k, ma[k], mb[k], add)
		}
		return
	}
	la, okA := a.([]any)
	lb, okB := b.([]any)
	if okA && okB {
		n := len(la)
		if len(lb) > n {
			n = len(lb)
		}
		for i := 0; i < n; i++ {
			var x, y any
			if i < len(la) {
				x = la[i]
			}
			if i < len(lb) {
				y = lb[i]
			}
			diffValue(sec, fmt.Sprintf("%s[%d]", path, i), x, y, add)
		}
		return
	}
	if n := bundleNorm[sec]; n != nil {
		a, b = n(a), n(b)
	}
	if !sameScalar(a, b) {
		add(path, a, b)
	}
}

// bundleNorm cleans fields that are cut differently on each side:
//   - logs.text: the script cuts the cell at 200 UTF-16 units before the
//     spaces around it are dropped, the import drops them first.
var bundleNorm = map[string]func(any) any{
	"logs.text": func(v any) any {
		s, ok := v.(string)
		if !ok {
			return v
		}
		r := []rune(strings.TrimSpace(s))
		if len(r) > 150 {
			r = r[:150]
		}
		return string(r)
	},
}

func sameScalar(a, b any) bool {
	if isEmpty(a) && isEmpty(b) {
		return true
	}
	fa, na := number(a)
	fb, nb := number(b)
	if na && nb {
		return math.Abs(fa-fb) < 0.005
	}
	_, ca := a.(map[string]any)
	_, cb := b.(map[string]any)
	_, la := a.([]any)
	_, lb := b.([]any)
	if ca || cb || la || lb {
		return false
	}
	return scalar(a) == scalar(b)
}

// isEmpty: null and missing are the same; an empty list or object is too
// (the script returns [] or {} where a section has nothing).
func isEmpty(v any) bool {
	switch x := v.(type) {
	case nil:
		return true
	case []any:
		return len(x) == 0
	case map[string]any:
		return len(x) == 0
	}
	return false
}

func number(v any) (float64, bool) {
	switch x := v.(type) {
	case float64:
		return x, true
	case int:
		return float64(x), true
	case int64:
		return float64(x), true
	case string:
		s := strings.TrimSpace(x)
		if s == "" {
			return 0, false
		}
		f, err := strconv.ParseFloat(s, 64)
		return f, err == nil
	}
	return 0, false
}

// scalar is a value as text, cleaned of what does not matter.
func scalar(v any) string {
	switch x := v.(type) {
	case nil:
		return ""
	case string:
		return strings.TrimSpace(strings.ReplaceAll(x, "\r\n", "\n"))
	case float64:
		return strconv.FormatFloat(x, 'f', -1, 64)
	case bool:
		return strconv.FormatBool(x)
	}
	return fmt.Sprint(v)
}

func show(v any) string {
	switch v.(type) {
	case map[string]any:
		return "{…}"
	case []any:
		return "[…]"
	}
	s := scalar(v)
	if v == nil {
		s = "—"
	}
	if r := []rune(s); len(r) > 120 {
		s = string(r[:120]) + "…"
	}
	return s
}
