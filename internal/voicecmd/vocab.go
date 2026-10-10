package voicecmd

import (
	"strings"
)

// Levenshtein distance on runes.
func Lev(a, b string) int {
	ra, rb := []rune(a), []rune(b)
	if len(ra) == 0 {
		return len(rb)
	}
	if len(rb) == 0 {
		return len(ra)
	}
	prev := make([]int, len(rb)+1)
	cur := make([]int, len(rb)+1)
	for j := range prev {
		prev[j] = j
	}
	for i := 1; i <= len(ra); i++ {
		cur[0] = i
		for j := 1; j <= len(rb); j++ {
			c := 1
			if ra[i-1] == rb[j-1] {
				c = 0
			}
			cur[j] = min(prev[j]+1, cur[j-1]+1, prev[j-1]+c)
		}
		prev, cur = cur, prev
	}
	return prev[len(rb)]
}

// phon: a rough spoken form, so that Whisper's spellings meet the board's
// («касовые» = «кассовые», «тренинг» = «трэнинг»): doubled letters once,
// voiced/unvoiced pairs and similar vowels merged.
func phon(s string) string {
	r := strings.NewReplacer("ться", "ца", "тся", "ца", "тс", "ц", "дж", "ш", "ё", "е", "э", "е", "ъ", "", "ь", "", "й", "и", "ы", "и", "о", "а", "я", "а", "ю", "у",
		"б", "п", "в", "ф", "г", "к", "д", "т", "ж", "ш", "з", "с", "щ", "ш")
	s = r.Replace(strings.ToLower(s))
	var b strings.Builder
	var last rune
	for _, c := range s {
		if c == last && c != ' ' {
			continue
		}
		b.WriteRune(c)
		last = c
	}
	return b.String()
}

// sim: 1 for the same spoken form, falling with the edit distance.
func sim(a, b string) float64 {
	pa, pb := phon(a), phon(b)
	n := len([]rune(pa))
	if m := len([]rune(pb)); m > n {
		n = m
	}
	if n == 0 {
		return 0
	}
	return 1 - float64(Lev(pa, pb))/float64(n)
}

// Vocab: the board's words a command may name.
type Vocab struct {
	Diag, Tools, Residents, Nodes, Team []string
}

// Best: the vocabulary entry that best matches q (a word group of a
// command), with its similarity. Whole titles and their leading words count:
// «платежный календарь» finds «Платёжный календарь на 30 дней».
func Best(q string, list []string) (string, float64) {
	q = Norm(q)
	if q == "" {
		return "", 0
	}
	best, bs := "", 0.0
	for _, t := range list {
		tn := Norm(t)
		if tn == "" {
			continue
		}
		s := sim(q, tn)
		if tn == q {
			s = 1.01
		} else if strings.HasPrefix(tn, q+" ") && len([]rune(q)) >= 6 {
			s = max(s, 0.93)
		} else if strings.Contains(tn, q) && len([]rune(q)) >= 6 {
			s = max(s, 0.86)
		} else {
			// the title's first words as long as the query
			tw := strings.Fields(tn)
			qn := len(strings.Fields(q))
			if qn < len(tw) {
				s = max(s, sim(q, strings.Join(tw[:qn], " "))-0.04)
			}
		}
		if s > bs {
			best, bs = t, s
		}
	}
	return best, bs
}

// Correct replaces word groups of the text that sound like a vocabulary
// entry (Whisper writes «касовые разрыв», «Даулед») with the entry itself.
// Names (residents, team) are single or double words; library titles up to
// five. Only near matches are replaced (similarity ≥ 0.8), never the
// command words.
func Correct(text string, v Vocab) string {
	ws := strings.Fields(Norm(text))
	if len(ws) == 0 {
		return ""
	}
	type cand struct {
		list []string
		min  float64
	}
	sets := []cand{{v.Diag, 0.8}, {v.Tools, 0.8}, {v.Nodes, 0.82}, {v.Residents, 0.78}, {v.Team, 0.78}}
	out := []string{}
	for i := 0; i < len(ws); {
		if cmdWords[ws[i]] {
			out = append(out, ws[i])
			i++
			continue
		}
		bestT, bestS, bestN := "", 0.0, 0
		for n := 5; n >= 1; n-- {
			if i+n > len(ws) {
				continue
			}
			stop := false
			for _, w := range ws[i : i+n] {
				if cmdWords[w] {
					stop = true
				}
			}
			if stop {
				continue
			}
			q := strings.Join(ws[i:i+n], " ")
			if len([]rune(q)) < 4 {
				continue
			}
			for _, c := range sets {
				t, s := bestWhole(q, c.list)
				if s >= c.min && s > bestS+0.001 {
					bestT, bestS, bestN = t, s, n
				}
			}
		}
		if bestN > 0 {
			out = append(out, strings.Fields(Norm(bestT))...)
			i += bestN
			continue
		}
		out = append(out, ws[i])
		i++
	}
	return strings.Join(out, " ")
}

// bestWhole: like Best but only whole entries or a person's first name
// (so that «кассовые» alone is not turned into a two-word title).
func bestWhole(q string, list []string) (string, float64) {
	best, bs := "", 0.0
	qn := len(strings.Fields(q))
	for _, t := range list {
		tn := Norm(t)
		tw := strings.Fields(tn)
		if len(tw) == 0 {
			continue
		}
		cands := []string{tn}
		if len(tw) > qn && qn >= 1 && len(tw) <= 3 && qn == 1 {
			cands = append(cands, tw[0]) // a name: «Даулет» of «Даулет Сайты»
		}
		for _, c := range cands {
			if len(strings.Fields(c)) != qn {
				continue
			}
			s := sim(q, c)
			if s > bs {
				best, bs = c, s
				if c == tn {
					best = t
				} else {
					best = tw[0]
				}
			}
		}
	}
	return best, bs
}

// command words are never «corrected» into a title
var cmdWords = map[string]bool{}

func init() {
	for _, w := range strings.Fields(`добавь добавить создай создать поставь поставить запиши записать сделай назначь назначить
		задача задачу задачи заметка заметку диагноз диагнозы инструмент инструменты цель вопрос точка точку а б
		открой открыть покажи перейди отметь пометь закрой выполнена выполнено выполнил сделана сделано готово готова
		таймер минут минуты минуту секунд час часа план в на к до под и с по для от из у это этот эту этого нему ней
		привяжи прикрепи перенеси перемести отмени отмена стоп хватит прочитай озвучь итог итоги сводку резюме
		ответственный ответственная поручи срок завтра сегодня послезавтра пятницу понедельник вторник среду четверг субботу
		доску доска разбор резидента`) {
		cmdWords[w] = true
	}
}
