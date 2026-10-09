package http

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"

	pg "github.com/bnursik/business_surgery_backend/internal/repository/pg"
)

// R66: «Реши эту проблему» (the owner refuses paid AI APIs). The daily
// recommendation works on the free light model without web search:
//
//   - duplicates are found here, locally, with no model call: normalized
//     titles, then the Jaccard similarity of character trigrams (title) and
//     of word stems (title + summary). A near copy of a library item is
//     dropped before anyone is asked; a close one is put to the owner on the
//     platform («Решения ИИ») with the item side by side;
//   - the model gets only the library items close to the candidate (not the
//     whole library of 430+ cards), so the prompts are small and the light
//     model answers within its free limits;
//   - the day's organ comes from the library's gaps (the organs with the
//     fewest items, diagnoses with no tool to cure them).

const (
	recDupHigh  = 0.72 // a near copy: dropped, nobody is asked
	recSimMid   = 0.34 // close enough to show side by side
	recTopJudge = 60   // library items the judge sees
	recTopCands = 3    // close items kept with the recommendation
)

// recTrigrams: the character trigrams of the normalized text (words padded).
func recTrigrams(s string) map[string]bool {
	out := map[string]bool{}
	for _, w := range strings.Fields(libNorm(s)) {
		r := []rune(" " + w + " ")
		for i := 0; i+3 <= len(r); i++ {
			out[string(r[i:i+3])] = true
		}
	}
	return out
}

// recStems: word stems (the first 5 letters of words of 4+ letters, stop words out).
func recStems(s string) map[string]bool {
	out := map[string]bool{}
	for _, w := range strings.Fields(libNorm(s)) {
		r := []rune(w)
		if len(r) < 4 || recStop[w] {
			continue
		}
		if len(r) > 5 {
			r = r[:5]
		}
		out[string(r)] = true
	}
	return out
}

var recStop = map[string]bool{"для": true, "как": true, "это": true, "когда": true, "бизнес": true, "бизнеса": true, "метод": true,
	"нет": true, "или": true, "что": true, "без": true, "всех": true, "весь": true, "своих": true, "свой": true, "малого": true, "малый": true}

// jaccard: content_threads.go.

// recSimilarity: how close a candidate (title, summary) is to a library item
// (title, its line): 1 for the same title.
func recSimilarity(title, summary, itTitle, itLine string) float64 {
	if libNorm(title) != "" && libNorm(title) == libNorm(itTitle) {
		return 1
	}
	tt := jaccard(recTrigrams(title), recTrigrams(itTitle))
	ws := jaccard(recStems(title+" "+summary), recStems(itTitle+" "+itLine))
	wt := jaccard(recStems(title), recStems(itTitle))
	s := 0.55*tt + 0.25*wt + 0.2*ws
	if tt > s {
		s = tt
	}
	return s
}

type recScored struct {
	recLibItem
	Score float64
}

// recClosest: the library items closest to the candidate, best first.
func recClosest(title, summary string, lib []recLibItem, n int) []recScored {
	out := make([]recScored, 0, len(lib))
	for _, it := range lib {
		out = append(out, recScored{it, recSimilarity(title, summary, it.Title, it.Line)})
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Score > out[j].Score })
	if n > 0 && len(out) > n {
		out = out[:n]
	}
	return out
}

// recLocalDup: the library item the candidate copies ("" when none).
func recLocalDup(title, summary string, lib []recLibItem) (string, float64) {
	if b := recClosest(title, summary, lib, 1); len(b) > 0 && b[0].Score >= recDupHigh {
		return b[0].Title, b[0].Score
	}
	return "", 0
}

// recCands: the close items kept with a recommendation for the platform's
// side-by-side view: [{t, k, s}].
func recCands(title, summary string, lib []recLibItem) []any {
	var out []any
	for _, c := range recClosest(title, summary, lib, recTopCands) {
		if c.Score < recSimMid {
			break
		}
		out = append(out, map[string]any{"t": c.Title, "k": c.Kind, "s": float64(int(c.Score*100+0.5)) / 100})
	}
	return out
}

// ── the library's gaps ──

// recPickOrgan: the day's organ: among the three organs with the fewest
// items, the next one after the last recommendation's organ (so the gaps fill
// first and the days still vary).
func recPickOrgan(items []any, lib []recLibItem) string {
	cnt := map[string]int{}
	for _, o := range aiRecOrgans {
		cnt[o] = 0
	}
	for _, it := range lib {
		if _, ok := cnt[recOrganName(it.Organ)]; ok {
			cnt[recOrganName(it.Organ)]++
		}
	}
	order := append([]string{}, aiRecOrgans...)
	sort.SliceStable(order, func(i, j int) bool { return cnt[order[i]] < cnt[order[j]] })
	low := order[:3]
	last := ""
	if len(items) > 0 {
		last = recStr(items[0], "organ")
	}
	// the rotation order, starting after the last organ
	start := 0
	for i, o := range aiRecOrgans {
		if o == last {
			start = i + 1
		}
	}
	for k := 0; k < len(aiRecOrgans); k++ {
		o := aiRecOrgans[(start+k)%len(aiRecOrgans)]
		for _, l := range low {
			if l == o {
				return o
			}
		}
	}
	return low[0]
}

// recOrganName: «🧠 Стратегия» → «Стратегия».
func recOrganName(s string) string {
	return strings.TrimSpace(strings.TrimLeftFunc(s, func(r rune) bool {
		return !(r >= 'А' && r <= 'я' || r == 'Ё' || r == 'ё' || r >= 'A' && r <= 'z')
	}))
}

// recGapText: the gaps of the organ in words for the candidate prompt.
func recGapText(organ string, lib []recLibItem) string {
	tools := map[string]bool{}
	nt, nd := 0, 0
	for _, it := range lib {
		if it.Kind == "tool" {
			tools[libNorm(it.Title)] = true
		}
		if recOrganName(it.Organ) == organ {
			if it.Kind == "tool" {
				nt++
			} else {
				nd++
			}
		}
	}
	var bare []string
	for _, it := range lib {
		if it.Kind != "diag" || recOrganName(it.Organ) != organ || len(it.Cure) == 0 {
			continue
		}
		ok := false
		for _, c := range it.Cure {
			if tools[libNorm(c)] {
				ok = true
				break
			}
		}
		if !ok && len(bare) < 5 {
			bare = append(bare, "«"+it.Title+"»")
		}
	}
	t := fmt.Sprintf("Пробелы библиотеки по органу «%s»: инструментов %d, диагнозов %d.", organ, nt, nd)
	if nt <= nd {
		t += " Инструментов не хватает больше, чем диагнозов: лучше предложить инструмент."
	} else {
		t += " Диагнозов меньше, чем инструментов: можно предложить диагноз."
	}
	if len(bare) > 0 {
		t += " Диагнозы, которым нечем лечить (инструмента из их лечения нет в библиотеке): " + strings.Join(bare, ", ") + ". Инструмент для одного из них особенно нужен."
	}
	return t
}

// ── «Слить с существующим» ──

// mergeLibItem adds what the recommendation brings into the existing card
// titled target: the new steps (tool) or signs and questions (diagnosis), the
// source, and a line in its «merged» history. The card keeps its title.
func (h *PlatformAI) mergeLibItem(ctx context.Context, key, target string, rec map[string]any) error {
	item, _ := rec["item"].(map[string]any)
	for try := 0; try < 4; try++ {
		d, err := h.repo.GetDoc(ctx, "club", key)
		if err != nil {
			return err
		}
		if d == nil || d.Deleted {
			return errors.New("библиотеки нет на сервере")
		}
		var list []map[string]any
		if json.Unmarshal([]byte(d.Value), &list) != nil {
			return errors.New("библиотека не читается")
		}
		at := -1
		for i, it := range list {
			if t, _ := it["title"].(string); libNorm(t) == libNorm(target) {
				at = i
				break
			}
		}
		if at < 0 {
			return fmt.Errorf("не нашёл «%s» в библиотеке", target)
		}
		cur := list[at]
		addList := func(field string, from any, max int) {
			have := map[string]bool{}
			var out []any
			if xs, ok := cur[field].([]any); ok {
				for _, x := range xs {
					have[libNorm(fmt.Sprint(x))] = true
					out = append(out, x)
				}
			}
			for _, x := range toAnyList(from) {
				s := strings.TrimSpace(fmt.Sprint(x))
				if s == "" || have[libNorm(s)] || len(out) >= max {
					continue
				}
				have[libNorm(s)] = true
				out = append(out, s)
			}
			if len(out) > 0 {
				cur[field] = out
			}
		}
		if key == "bs_diag" {
			addList("signs", item["signs"], 10)
			addList("questions", item["questions"], 8)
		} else {
			addList("how", item["how"], 10)
			addList("check", item["check"], 8)
		}
		if src, _ := item["source"].(string); src != "" {
			old, _ := cur["source"].(string)
			if !strings.Contains(libNorm(old), libNorm(src)) {
				cur["source"] = strings.Trim(strings.TrimSpace(old+"; "+src), "; ")
			}
		}
		hist := toAnyList(cur["merged"])
		hist = append(hist, map[string]any{"title": recStr(rec, "title"), "rec": recStr(rec, "id"), "url": recStr(rec, "url")})
		cur["merged"] = hist
		val, _ := json.Marshal(list)
		if _, err := h.repo.PutDoc(ctx, "club", key, d.Version, string(val), false, "server:ai_recs"); err == nil {
			return nil
		} else if err != pg.ErrPlatformConflict {
			return err
		}
	}
	return pg.ErrPlatformConflict
}

func toAnyList(v any) []any {
	switch x := v.(type) {
	case []any:
		return append([]any{}, x...)
	case []string:
		out := make([]any, len(x))
		for i, s := range x {
			out[i] = s
		}
		return out
	}
	return nil
}

// recWaiting: the recommendations waiting for the owner's decision.
func recWaiting(items []any) int {
	n := 0
	for _, it := range items {
		if st := recStr(it, "status"); st == "ask" {
			n++
		}
	}
	return n
}

// recNearTools: the n tool titles closest to the recommendation (a diagnosis's
// cure is picked from them), in the library's order.
func recNearTools(rec map[string]any, tools []string, n int) []string {
	if len(tools) <= n {
		return tools
	}
	lib := make([]recLibItem, len(tools))
	for i, t := range tools {
		lib[i] = recLibItem{Kind: "tool", Title: t}
	}
	keep := map[string]bool{}
	for _, c := range recClosest(recStr(rec, "title"), recStr(rec, "summary")+" "+recStr(rec, "why"), lib, n) {
		keep[c.Title] = true
	}
	var out []string
	for _, t := range tools {
		if keep[t] {
			out = append(out, t)
		}
	}
	return out
}

// recNearTitles: the n titles closest to the candidate (the model's meaning
// check sees only these, not the whole library).
func recNearTitles(c *aiRecCand, lib []recLibItem, n int) []string {
	var out []string
	for _, x := range recClosest(c.Title, c.Summary+" "+c.Why, lib, n) {
		out = append(out, x.Title)
	}
	return out
}

// recJudgeLib: what the judge sees: every item of the recommendation's organ
// (the same method under another name is most likely there) and the
// recTopJudge items closest to it from the other organs.
func recJudgeLib(rec map[string]any, lib []recLibItem) []recLibItem {
	organ := recOrganName(recStr(rec, "organ"))
	var out []recLibItem
	seen := map[string]bool{}
	for _, it := range lib {
		if organ != "" && recOrganName(it.Organ) == organ {
			out = append(out, it)
			seen[it.Kind+":"+libNorm(it.Title)] = true
		}
	}
	for _, c := range recClosest(recStr(rec, "title"), recStr(rec, "summary"), lib, recTopJudge+len(out)) {
		if k := c.Kind + ":" + libNorm(c.Title); !seen[k] && len(out) < recTopJudge*2 {
			seen[k] = true
			out = append(out, c.recLibItem)
		}
	}
	return out
}
