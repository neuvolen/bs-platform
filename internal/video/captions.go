package video

import (
	"fmt"
	"math"
	"strings"
	"unicode"
	"unicode/utf8"
)

// Seg: a phrase from the speech recogniser, its time in the cut video.
type Seg struct {
	Start, End float64
	Text       string
}

// Word: one word on screen with its time.
type Word struct {
	Text       string
	Start, End float64
}

// Page: the words shown together (2-4, one line).
type Page struct {
	Words      []Word
	Start, End float64
}

// Words spreads each phrase's words over its time by their length: Whisper in
// sherpa-onnx gives phrase times only, and spoken length follows letters well
// enough for a word-by-word highlight.
func Words(segs []Seg) []Word {
	var out []Word
	for _, s := range segs {
		ws := strings.Fields(s.Text)
		if len(ws) == 0 || s.End <= s.Start {
			continue
		}
		total := 0.0
		for _, w := range ws {
			total += float64(utf8.RuneCountInString(w)) + 2
		}
		t := s.Start
		span := s.End - s.Start
		for _, w := range ws {
			d := span * (float64(utf8.RuneCountInString(w)) + 2) / total
			out = append(out, Word{Text: w, Start: t, End: t + d})
			t += d
		}
	}
	return out
}

const (
	pageWords = 3  // words on a caption page (2-4 reads best at Reels speed)
	pageRunes = 16 // and at most this many letters, so one line fits 1080 px
)

// Pages groups words into caption pages: up to pageWords words and pageRunes
// letters, a new page after the end of a sentence, a comma or a pause.
func Pages(ws []Word) []Page {
	var pages []Page
	var cur []Word
	flush := func() {
		if len(cur) > 0 {
			pages = append(pages, Page{Words: cur, Start: cur[0].Start, End: cur[len(cur)-1].End})
			cur = nil
		}
	}
	runes := 0
	for i, w := range ws {
		n := utf8.RuneCountInString(cleanWord(w.Text))
		if len(cur) > 0 && (len(cur) >= pageWords || runes+1+n > pageRunes || w.Start-cur[len(cur)-1].End > 0.45) {
			flush()
			runes = 0
		}
		if len(cur) > 0 {
			runes++
		}
		cur = append(cur, w)
		runes += n
		if endsClause(w.Text) && (len(cur) >= 2 || strings.ContainsAny(lastRune(w.Text), ".?!…") || i+1 == len(ws)) {
			flush()
			runes = 0
		}
	}
	flush()
	// a page stays on screen until the next one starts (short gaps only)
	for i := range pages {
		if i+1 < len(pages) {
			if g := pages[i+1].Start - pages[i].End; g > 0 && g < 0.6 {
				pages[i].End = pages[i+1].Start
			}
		} else {
			pages[i].End += 0.3
		}
	}
	return pages
}

func lastRune(s string) string {
	r, _ := utf8.DecodeLastRuneInString(s)
	return string(r)
}

func endsClause(s string) bool { return strings.ContainsAny(lastRune(s), ".,!?;:…") }

// cleanWord: commas and full stops go (captions read cleaner), ? and ! stay.
func cleanWord(s string) string {
	return strings.TrimRightFunc(strings.TrimLeft(s, "«\"("), func(r rune) bool {
		return r == '.' || r == ',' || r == ';' || r == ':' || r == '»' || r == '"' || r == ')' || r == '…'
	})
}

// Emphasis: the pages to punch in on: a number, a question or exclamation,
// and every third page otherwise; at least 0.6 s long, at most 40.
func Emphasis(pages []Page) []Span {
	var sp []Span
	for i, p := range pages {
		if p.End-p.Start < 0.6 || len(sp) >= 40 {
			continue
		}
		txt := ""
		for _, w := range p.Words {
			txt += w.Text + " "
		}
		if strings.ContainsAny(txt, "0123456789?!") || i%3 == 2 {
			sp = append(sp, Span{p.Start, p.End})
		}
	}
	return sp
}

// Style: the look of the captions and cards, in 1080×1920 pixels.
type Style struct {
	Font     string  // the family libass looks up in fontsdir
	Size     int     // caption size
	Bottom   int     // caption bottom from the frame's bottom (safe zone 350 + room)
	Top      int     // hook top (safe zone 250 + room)
	HookSize int     // hook card text size
	HookFor  float64 // hook seconds
}

// DefaultStyle: Manrope ExtraBold, white words with a black outline, the
// spoken word inverted (black on white), the hook as a white card with black
// text; all inside the Reels safe zones (250 px top, 350 px bottom).
var DefaultStyle = Style{Font: "Manrope ExtraBold", Size: 112, Bottom: 560, Top: 300, HookSize: 100, HookFor: 1.5}

func assTime(t float64) string {
	if t < 0 {
		t = 0
	}
	cs := int(math.Round(t * 100))
	return fmt.Sprintf("%d:%02d:%02d.%02d", cs/360000, cs/6000%60, cs/100%60, cs%100)
}

func assText(s string) string {
	s = strings.NewReplacer("\\", "/", "{", "(", "}", ")", "\n", " ", "\r", "").Replace(s)
	return s
}

// Wrap breaks text into lines of at most n letters, by words.
func Wrap(s string, n int) []string {
	var lines []string
	cur := ""
	for _, w := range strings.Fields(s) {
		if cur != "" && utf8.RuneCountInString(cur)+1+utf8.RuneCountInString(w) > n {
			lines = append(lines, cur)
			cur = w
		} else if cur == "" {
			cur = w
		} else {
			cur += " " + w
		}
	}
	if cur != "" {
		lines = append(lines, cur)
	}
	return lines
}

func assHeader(st Style) string {
	return "[Script Info]\nScriptType: v4.00+\nPlayResX: 1080\nPlayResY: 1920\nWrapStyle: 2\nScaledBorderAndShadow: yes\nYCbCr Matrix: TV.709\n\n" +
		"[V4+ Styles]\nFormat: Name, Fontname, Fontsize, PrimaryColour, SecondaryColour, OutlineColour, BackColour, Bold, Italic, Underline, StrikeOut, ScaleX, ScaleY, Spacing, Angle, BorderStyle, Outline, Shadow, Alignment, MarginL, MarginR, MarginV, Encoding\n" +
		fmt.Sprintf("Style: Cap,%s,%d,&H00FFFFFF,&H00FFFFFF,&H00000000,&H64000000,0,0,0,0,100,100,0,0,1,6,3,2,70,70,%d,1\n", st.Font, st.Size, st.Bottom) +
		fmt.Sprintf("Style: Hook,%s,%d,&H00000000,&H00000000,&H00FFFFFF,&H00FFFFFF,0,0,0,0,100,100,0,0,3,26,0,8,90,90,%d,1\n", st.Font, st.HookSize, st.Top) +
		fmt.Sprintf("Style: Cta,%s,%d,&H00FFFFFF,&H00FFFFFF,&H00000000,&H00000000,0,0,0,0,100,100,0,0,1,0,0,5,90,90,0,1\n", st.Font, 80) +
		"\n[Events]\nFormat: Layer, Start, End, Style, Name, MarginL, MarginR, MarginV, Effect, Text\n"
}

// ASS: the captions file: the hook card and the captions page by page, one
// event per spoken word so the word being said is highlighted.
func ASS(st Style, pages []Page, hook string) string {
	var b strings.Builder
	b.WriteString(assHeader(st))
	if h := strings.TrimSpace(hook); h != "" {
		lines := Wrap(assText(h), 17)
		if len(lines) > 3 {
			lines = lines[:3]
		}
		fmt.Fprintf(&b, "Dialogue: 2,%s,%s,Hook,,0,0,0,,{\\fad(0,180)\\fscx86\\fscy86\\t(0,140,\\fscx100\\fscy100)}%s\n",
			assTime(0), assTime(st.HookFor), strings.Join(lines, "\\N"))
	}
	for _, p := range pages {
		for i, w := range p.Words {
			a := w.Start
			if i == 0 {
				a = p.Start
			}
			e := p.End
			if i+1 < len(p.Words) {
				e = p.Words[i+1].Start
			}
			if e-a < 0.01 {
				continue
			}
			var line []string
			for j, x := range p.Words {
				t := assText(cleanWord(x.Text))
				if j == i {
					t = "{\\1c&H000000&\\3c&HFFFFFF&\\4a&HFF&\\bord12}" + t + "{\\r}"
				}
				line = append(line, t)
			}
			pop := ""
			if i == 0 {
				pop = "{\\fscx92\\fscy92\\t(0,90,\\fscx100\\fscy100)}"
			}
			fmt.Fprintf(&b, "Dialogue: 1,%s,%s,Cap,,0,0,0,,%s%s\n", assTime(a), assTime(e), pop, strings.Join(line, " \\h"))
		}
	}
	return b.String()
}

// OutroASS: the CTA under the logo on the black outro card.
func OutroASS(st Style, cta string, dur float64) string {
	var b strings.Builder
	b.WriteString(assHeader(st))
	lines := Wrap(assText(strings.TrimSpace(cta)), 18)
	if len(lines) > 4 {
		lines = lines[:4]
	}
	if len(lines) > 0 {
		fmt.Fprintf(&b, "Dialogue: 1,%s,%s,Cta,,0,0,0,,{\\pos(540,1080)\\fad(250,0)}%s\n", assTime(0.15), assTime(dur), strings.Join(lines, "\\N"))
	}
	return b.String()
}

// Plain: the words as text (shown under the result, used for the post caption).
func Plain(segs []Seg) string {
	var parts []string
	for _, s := range segs {
		if t := strings.TrimSpace(s.Text); t != "" {
			parts = append(parts, t)
		}
	}
	out := strings.Join(parts, " ")
	if r := []rune(out); len(r) > 0 && unicode.IsLower(r[0]) {
		r[0] = unicode.ToUpper(r[0])
		out = string(r)
	}
	return out
}
