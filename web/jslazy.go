package web

import (
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/evanw/esbuild/pkg/api"
)

// R45: functions loaded on first use.
//
// A statement-level `function name(a, b) { body }` of a classic script (or of
// the body of an IIFE block) stays declared where it was, with the same
// name, parameters and hoisting, but its body becomes one call:
//
//	function name(a, b){return __LZ.f(chunk, index, this, arguments, new.target)}
//
// The real function is `function(a, b) { body }` in a chunk file
// (/a/lz….js), evaluated in the same scope as before: the global scope for
// a classic script (indirect eval), the IIFE's own scope through a small
// hook declared at the top of the IIFE (direct eval), so every name in the
// body means what it meant. The page fetches all chunks quietly after it
// has loaded (__LZ.pre); a call before that is queued until its chunk
// arrives (never a synchronous request).
//
// Functions the page calls while it starts stay as they are (lazyKeep).

const (
	lazyMinBody   = 300      // smaller bodies are not worth a stub
	lazyChunkSize = 48 << 10 // bytes of source per chunk at most
	lazySmallSect = 12 << 10 // sections smaller than this join the next one
)

type lazyChunk struct {
	Scope  int      // 0: global scope; else the IIFE hook's number
	Funcs  []string // "function(a,b){...}" in stub index order
	Names  []string
	Key    string // where it comes from, for the manifest
	Strict bool   // a global script with "use strict": the chunk is strict too
}

type lazyOut struct {
	Script string
	Chunks []lazyChunk
}

// lazify rewrites one classic script. chunkBase is the number of the first
// chunk this script gets, scopeID the hook number when it is an IIFE.
// key names the script in lazyKeep ("main", "r16Script", …).
func lazify(src, key string, chunkBase, scopeID int, keep func(key, name string) bool) (lazyOut, error) {
	toks, err := jsTokens(src)
	if err != nil {
		return lazyOut{Script: src}, err
	}
	m, err := jsMatch(src, toks)
	if err != nil {
		return lazyOut{Script: src}, err
	}
	sc := jsFindScope(src, toks, m)
	fns := jsStatementFuncs(src, toks, m, sc)
	strict := false
	if !sc.IIFE {
		strict = hasUseStrict(src, toks)
	}
	// sections ("// ═══" lines) group functions that go together
	var secs []int
	for i := 0; ; {
		j := strings.Index(src[i:], "\n// ═══")
		if j < 0 {
			break
		}
		secs = append(secs, i+j)
		i += j + 1
	}
	secAt := func(off int) int { return sort.SearchInts(secs, off) }
	type pick struct {
		f   jsFunc
		sec int
	}
	var picks []pick
	for _, f := range fns {
		if !f.plainParamList || f.BEnd-f.BStart < lazyMinBody || keep(key, f.Name) {
			continue
		}
		body := src[f.BStart:f.BEnd]
		if strings.Contains(body, "arguments.callee") || strings.Contains(body, "new.target") {
			continue
		}
		picks = append(picks, pick{f, secAt(f.Start)})
	}
	if len(picks) == 0 {
		return lazyOut{Script: src}, nil
	}
	// chunks: by section, small sections joined, big ones split
	var groups [][]jsFunc
	var cur []jsFunc
	curSize, curSec := 0, picks[0].sec
	for _, p := range picks {
		sz := p.f.End - p.f.Start
		if len(cur) > 0 && ((p.sec != curSec && curSize >= lazySmallSect) || curSize+sz > lazyChunkSize) {
			groups = append(groups, cur)
			cur, curSize = nil, 0
		}
		cur = append(cur, p.f)
		curSize += sz
		curSec = p.sec
	}
	if len(cur) > 0 {
		groups = append(groups, cur)
	}

	var b strings.Builder
	var chunks []lazyChunk
	last := 0
	if sc.IIFE {
		b.WriteString(src[:sc.directives])
		b.WriteString("function __lzE(s){return eval(s)}__LZ.s(" + strconv.Itoa(scopeID) + ",__lzE);")
		last = sc.directives
	}
	for gi, g := range groups {
		ch := lazyChunk{Key: key, Strict: strict}
		if sc.IIFE {
			ch.Scope = scopeID
		}
		for fi, f := range g {
			params := src[f.PStart:f.PEnd]
			b.WriteString(src[last:f.Start])
			fmt.Fprintf(&b, "function %s(%s){return __LZ.f(%d,%d,this,arguments,new.target)}", f.Name, params, chunkBase+gi, fi)
			last = f.End
			ch.Funcs = append(ch.Funcs, "function("+params+"){"+src[f.BStart:f.BEnd]+"}")
			ch.Names = append(ch.Names, f.Name)
		}
		chunks = append(chunks, ch)
	}
	b.WriteString(src[last:])
	out := lazyOut{Script: b.String(), Chunks: chunks}
	// Every boundary is checked: the stubbed script and each chunk must parse
	if err := jsParses(out.Script); err != nil {
		return lazyOut{Script: src}, fmt.Errorf("%s: stubbed script: %v", key, err)
	}
	for i := range out.Chunks {
		if err := jsParses(chunkText(out.Chunks[i])); err != nil {
			return lazyOut{Script: src}, fmt.Errorf("%s: chunk %d: %v", key, i, err)
		}
	}
	return out, nil
}

func hasUseStrict(src string, t []jsTok) bool {
	return len(t) > 0 && t[0].k == 's' && (src[t[0].s:t[0].e] == `'use strict'` || src[t[0].s:t[0].e] == `"use strict"`)
}

// chunkText is what eval gets: an array of the chunk's functions.
func chunkText(c lazyChunk) string {
	s := "[" + strings.Join(c.Funcs, ",\n") + "]"
	if c.Strict && c.Scope == 0 {
		s = "'use strict';" + s
	}
	return s
}

func jsParses(code string) error {
	r := api.Transform(code, api.TransformOptions{Loader: api.LoaderJS, LogLevel: api.LogLevelSilent})
	if len(r.Errors) > 0 {
		e := r.Errors[0]
		if e.Location != nil {
			return fmt.Errorf("%s (line %d: %q)", e.Text, e.Location.Line, e.Location.LineText)
		}
		return fmt.Errorf("%s", e.Text)
	}
	return nil
}

// minifyJS: esbuild minification; the code as it was when esbuild refuses it.
func minifyJS(code string) string {
	r := api.Transform(code, api.TransformOptions{
		Loader: api.LoaderJS, MinifyWhitespace: true, MinifySyntax: true, MinifyIdentifiers: true,
		Charset: api.CharsetUTF8, LogLevel: api.LogLevelSilent, LegalComments: api.LegalCommentsNone,
	})
	if len(r.Errors) > 0 {
		return code
	}
	return string(r.Code)
}

// minifyChunk minifies `[function(){…},…]` (esbuild would drop a bare
// expression without side effects, so it is minified as an assignment).
func minifyChunk(text string) string {
	const pre = "__LZ_C="
	strictPre := ""
	if strings.HasPrefix(text, "'use strict';") {
		strictPre = "'use strict';"
		text = strings.TrimPrefix(text, "'use strict';")
	}
	m := minifyJS(pre + text)
	m = strings.TrimSpace(m)
	if !strings.HasPrefix(m, pre) {
		return strictPre + text
	}
	m = strings.TrimSuffix(strings.TrimPrefix(m, pre), ";")
	if jsParses(strictPre+m) != nil {
		return strictPre + text
	}
	return strictPre + m
}

func minifyCSS(css string) string {
	r := api.Transform(css, api.TransformOptions{
		Loader: api.LoaderCSS, MinifyWhitespace: true, MinifySyntax: true,
		Charset: api.CharsetUTF8, LogLevel: api.LogLevelSilent, LegalComments: api.LegalCommentsNone,
	})
	if len(r.Errors) > 0 {
		return css
	}
	return string(r.Code)
}
