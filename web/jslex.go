package web

import (
	"errors"
	"strings"
)

// R45: a small JavaScript tokenizer, only good enough to find where the
// statement-level function declarations of a classic script (or of the body
// of an IIFE) begin and end. Every boundary it finds is checked afterwards by
// esbuild (the stubbed script and each moved function must parse), so a
// wrong guess falls back to the script as it was, never to broken code.

type jsTok struct {
	k    byte // 'i' word, 'n' number, 's' string, 'r' regexp, 'p' punctuation
	s, e int  // byte offsets in the source
	nl   bool // a line break before this token
}

var errJSLex = errors.New("jslex: unsupported or unterminated token")

// Words after which a "/" starts a regular expression, not a division.
var jsRegexAfterWord = map[string]bool{
	"return": true, "typeof": true, "instanceof": true, "in": true, "of": true, "new": true, "delete": true,
	"void": true, "throw": true, "case": true, "do": true, "else": true, "yield": true, "await": true,
}

func isIdentByte(c byte) bool {
	return c == '_' || c == '$' || c >= 0x80 || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9')
}

// jsTokens splits the source into tokens (comments and spaces dropped).
// Template literals are refused: the page has none, and the fallback keeps
// a script that gets one working as before.
func jsTokens(src string) ([]jsTok, error) {
	var out []jsTok
	nl := false
	n := len(src)
	regexOK := func() bool {
		if len(out) == 0 {
			return true
		}
		t := out[len(out)-1]
		switch t.k {
		case 'n', 's', 'r':
			return false
		case 'i':
			return jsRegexAfterWord[src[t.s:t.e]]
		}
		p := src[t.s:t.e]
		return p != ")" && p != "]" && p != "++" && p != "--"
	}
	for i := 0; i < n; {
		c := src[i]
		switch {
		case c == '\n':
			nl = true
			i++
		case c == ' ' || c == '\t' || c == '\r' || c == '\f' || c == '\v':
			i++
		case c == 0xE2 && i+2 < n && src[i+1] == 0x80 && (src[i+2] == 0xA8 || src[i+2] == 0xA9): // U+2028/2029
			nl = true
			i += 3
		case c == 0xC2 && i+1 < n && src[i+1] == 0xA0: // NBSP
			i += 2
		case c == 0xEF && i+2 < n && src[i+1] == 0xBB && src[i+2] == 0xBF: // BOM
			i += 3
		case c == '/' && i+1 < n && src[i+1] == '/':
			j := strings.IndexByte(src[i:], '\n')
			if j < 0 {
				i = n
			} else {
				i += j
			}
		case c == '/' && i+1 < n && src[i+1] == '*':
			j := strings.Index(src[i+2:], "*/")
			if j < 0 {
				return nil, errJSLex
			}
			if strings.IndexByte(src[i:i+2+j], '\n') >= 0 {
				nl = true
			}
			i += j + 4
		case c == '`':
			return nil, errJSLex
		case c == '\'' || c == '"':
			j := i + 1
			for ; j < n && src[j] != c; j++ {
				if src[j] == '\\' {
					j++
				} else if src[j] == '\n' {
					return nil, errJSLex
				}
			}
			if j >= n {
				return nil, errJSLex
			}
			out = append(out, jsTok{'s', i, j + 1, nl})
			nl = false
			i = j + 1
		case c == '/' && regexOK():
			j, cls := i+1, false
			for ; j < n; j++ {
				d := src[j]
				if d == '\\' {
					j++
					continue
				}
				if d == '\n' {
					return nil, errJSLex
				}
				if cls {
					if d == ']' {
						cls = false
					}
					continue
				}
				if d == '[' {
					cls = true
				} else if d == '/' {
					break
				}
			}
			if j >= n {
				return nil, errJSLex
			}
			j++
			for j < n && isIdentByte(src[j]) {
				j++
			}
			out = append(out, jsTok{'r', i, j, nl})
			nl = false
			i = j
		case c >= '0' && c <= '9' || (c == '.' && i+1 < n && src[i+1] >= '0' && src[i+1] <= '9'):
			j := i + 1
			for j < n {
				d := src[j]
				if isIdentByte(d) || d == '.' {
					j++
				} else if (d == '+' || d == '-') && (src[j-1] == 'e' || src[j-1] == 'E') && !strings.HasPrefix(strings.ToLower(src[i:j]), "0x") {
					j++
				} else {
					break
				}
			}
			out = append(out, jsTok{'n', i, j, nl})
			nl = false
			i = j
		case isIdentByte(c):
			j := i + 1
			for j < n && isIdentByte(src[j]) {
				j++
			}
			out = append(out, jsTok{'i', i, j, nl})
			nl = false
			i = j
		default:
			l := 1
			if (c == '+' || c == '-') && i+1 < n && src[i+1] == c {
				l = 2
			}
			out = append(out, jsTok{'p', i, i + l, nl})
			nl = false
			i += l
		}
	}
	return out, nil
}

// jsMatch pairs every bracket token with its partner: m[i] is the index of
// the matching token, or -1. An unbalanced script is refused.
func jsMatch(src string, t []jsTok) ([]int, error) {
	m := make([]int, len(t))
	var st []int
	for i, x := range t {
		m[i] = -1
		if x.k != 'p' {
			continue
		}
		switch src[x.s] {
		case '(', '[', '{':
			st = append(st, i)
		case ')', ']', '}':
			if len(st) == 0 {
				return nil, errJSLex
			}
			o := st[len(st)-1]
			st = st[:len(st)-1]
			want := map[byte]byte{')': '(', ']': '[', '}': '{'}[src[x.s]]
			if src[t[o].s] != want {
				return nil, errJSLex
			}
			m[o], m[i] = i, o
		}
	}
	if len(st) != 0 {
		return nil, errJSLex
	}
	return m, nil
}

// jsFunc is one statement-level `function name(params) { body }`.
type jsFunc struct {
	Name           string
	Start, End     int // the whole declaration
	PStart, PEnd   int // the parameters, without the parentheses
	BStart, BEnd   int // the body, without the braces
	plainParamList bool
}

// jsScope: the token range whose statement-level declarations are looked at.
// For a classic script it is the whole script; for `(function(){ ... })()`
// it is the body of that function.
type jsScope struct {
	IIFE       bool
	BodyStart  int // byte offset just after the IIFE body's "{"
	BodyEnd    int // byte offset of the IIFE body's "}"
	tokFrom    int
	tokTo      int // exclusive
	tokDepth   int // bracket depth of statements in this scope
	directives int // byte offset after the directive prologue ("use strict";) of the IIFE body
}

func tokIs(src string, t jsTok, s string) bool { return src[t.s:t.e] == s }

// jsFindScope: an IIFE when the script is `(function(){...})();` or
// `(function(){...}());` (an optional leading ";"), else the whole script.
func jsFindScope(src string, t []jsTok, m []int) jsScope {
	whole := jsScope{tokFrom: 0, tokTo: len(t), tokDepth: 0}
	i := 0
	if i < len(t) && tokIs(src, t[i], ";") {
		i++
	}
	if i+4 >= len(t) || !tokIs(src, t[i], "(") || !tokIs(src, t[i+1], "function") {
		return whole
	}
	open := i
	j := i + 2
	if t[j].k == 'i' {
		j++
	}
	if !tokIs(src, t[j], "(") || m[j] < 0 {
		return whole
	}
	j = m[j] + 1
	if j >= len(t) || !tokIs(src, t[j], "{") || m[j] < 0 {
		return whole
	}
	body := j
	end := m[body]
	rest := []string{}
	for k := end + 1; k < len(t); k++ {
		rest = append(rest, src[t[k].s:t[k].e])
	}
	tail := strings.Join(rest, "")
	if m[open] == end+1 { // (function(){...})()
		if tail != ")()" && tail != ")();" {
			return whole
		}
	} else if m[open] == len(t)-1 || (m[open] == len(t)-2 && tokIs(src, t[len(t)-1], ";")) { // (function(){...}())
		if tail != "())" && tail != "());" {
			return whole
		}
	} else {
		return whole
	}
	sc := jsScope{IIFE: true, BodyStart: t[body].e, BodyEnd: t[end].s, tokFrom: body + 1, tokTo: end, tokDepth: 2}
	sc.directives = sc.BodyStart
	for k := body + 1; k < end; k++ { // "use strict"; and other string directives
		if t[k].k == 's' && k+1 < end && tokIs(src, t[k+1], ";") {
			sc.directives = t[k+1].e
			k++
			continue
		}
		break
	}
	return sc
}

// jsStatementFuncs lists the statement-level function declarations of the
// scope (not async, not generators).
func jsStatementFuncs(src string, t []jsTok, m []int, sc jsScope) []jsFunc {
	var out []jsFunc
	depth := 0
	// depth of each token: number of open brackets around it
	d := make([]int, len(t))
	for i, x := range t {
		if x.k == 'p' {
			switch src[x.s] {
			case ')', ']', '}':
				depth--
			}
		}
		d[i] = depth
		if x.k == 'p' {
			switch src[x.s] {
			case '(', '[', '{':
				depth++
			}
		}
	}
	for i := sc.tokFrom; i < sc.tokTo; i++ {
		x := t[i]
		if x.k != 'i' || d[i] != sc.tokDepth || !tokIs(src, x, "function") {
			continue
		}
		// statement start: first token of the scope, after ";" or "}", or a
		// line break after a value-like token (automatic semicolon)
		ok := false
		if i == sc.tokFrom {
			ok = true
		} else {
			p := t[i-1]
			ps := src[p.s:p.e]
			switch {
			case p.k == 'p' && (ps == ";" || ps == "}"):
				ok = true
			case x.nl && (p.k == 'n' || p.k == 's' || p.k == 'r' || ps == "]"):
				ok = true
			case x.nl && p.k == 'i' && !jsRegexAfterWord[ps] && ps != "async" && ps != "function" && ps != "export" && ps != "default":
				ok = true
			}
		}
		if !ok || i+3 >= sc.tokTo {
			continue
		}
		nm := t[i+1]
		if nm.k != 'i' || !tokIs(src, t[i+2], "(") {
			continue // generator (function*) or anonymous
		}
		pc := m[i+2]
		if pc < 0 || pc+1 >= sc.tokTo || !tokIs(src, t[pc+1], "{") {
			continue
		}
		bc := m[pc+1]
		if bc < 0 || bc >= sc.tokTo {
			continue
		}
		plain := true
		for k := i + 3; k < pc; k++ {
			if !(t[k].k == 'i' || tokIs(src, t[k], ",")) {
				plain = false
			}
		}
		out = append(out, jsFunc{
			Name: src[nm.s:nm.e], Start: x.s, End: t[bc].e,
			PStart: t[i+2].e, PEnd: t[pc].s, BStart: t[pc+1].e, BEnd: t[bc].s,
			plainParamList: plain,
		})
		i = bc
	}
	return out
}
