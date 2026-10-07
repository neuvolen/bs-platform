package web

import (
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"embed"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"path"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/andybalholm/brotli"
	"github.com/bnursik/business_surgery_backend/internal/middleware"
	"github.com/gin-gonic/gin"
)

// R45: the platform page ships as a small shell plus cached files.
//
// web/platform.html stays the one readable source (the browser suites open
// it as a file). When the server starts, the page is taken apart:
//   - every <style> goes into one minified stylesheet;
//   - every classic <script> becomes its own minified file, in the same
//     place and order (still blocking, so it runs exactly as before);
//   - statement-level functions the start does not need move to chunk files
//     loaded on first use (jslazy.go), big data objects get getters
//     (cutLazyData), both fetched quietly once the page has drawn;
//   - pictures in data: URLs become files;
//   - Google Fonts give way to our own Manrope and Oswald (web/fonts).
// Each file is served at /a/<name>.<content hash>.<ext> with a one-year
// immutable cache, brotli or gzip made once at start. The shell itself is a
// few dozen KB and revalidates every time (ETag, 304).
//
// Anything that goes wrong while taking the page apart leaves the whole page
// as it was (the old single file, compressed): never a broken page.

//go:embed fonts/*.woff2 fonts/fonts.css
var fontsFS embed.FS

type asset struct {
	name   string // file name after /a/
	ctype  string
	plain  []byte
	gz     []byte
	br     atomic.Pointer[[]byte] // made in the background; gzip until then
	hash   string
	public bool // served without a session: fonts, pictures, the fonts' CSS
}

func (a *asset) brotli() []byte {
	if p := a.br.Load(); p != nil {
		return *p
	}
	return nil
}

func (a *asset) URL() string { return "/a/" + a.name }

var (
	assetsMu sync.RWMutex
	assets   = map[string]*asset{}
)

func contentHash(b []byte) string {
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:])[:16]
}

// compressible types get brotli (best) and gzip copies made once.
func newAsset(base, ext, ctype string, data []byte, public bool) *asset {
	h := contentHash(data)
	a := &asset{name: base + "." + h + "." + ext, ctype: ctype, plain: data, hash: h, public: public}
	assetsMu.Lock()
	if old, ok := assets[a.name]; ok {
		assetsMu.Unlock()
		return old
	}
	assets[a.name] = a
	assetsMu.Unlock()
	if !strings.HasPrefix(ctype, "font/") && !strings.HasPrefix(ctype, "image/png") && len(data) > 512 {
		a.gz = gzipBytes(data)
		brWork.Add(1)
		brQueue <- a
	}
	return a
}

// Brotli at its best level takes seconds for the whole page, so it is made
// by one background worker after start; the files go gzipped until then.
var (
	brQueue = make(chan *asset, 4096)
	brWork  sync.WaitGroup
)

func init() {
	go func() {
		for a := range brQueue {
			b := brotliBytes(a.plain, brotli.BestCompression)
			if len(b) < len(a.gz) {
				a.br.Store(&b)
			}
			brWork.Done()
		}
	}()
}

// WaitCompressed blocks until every file has its brotli copy (tests, measurements).
func WaitCompressed() { brWork.Wait() }

func gzipBytes(b []byte) []byte {
	var buf bytes.Buffer
	zw, _ := gzip.NewWriterLevel(&buf, gzip.BestCompression)
	_, _ = zw.Write(b)
	_ = zw.Close()
	return buf.Bytes()
}

func brotliBytes(b []byte, q int) []byte {
	var buf bytes.Buffer
	bw := brotli.NewWriterOptions(&buf, brotli.WriterOptions{Quality: q, LGWin: 22})
	_, _ = bw.Write(b)
	_ = bw.Close()
	return buf.Bytes()
}

// ── Fonts ──

var fontsCSS *asset

// fontPreload: the Cyrillic Manrope (most of the text) starts with the page,
// not after the stylesheet is read: the text gets its font without a swap
var fontPreload string

var googleFontsRe = regexp.MustCompile(`(?m)^[ \t]*<link[^>]+(fonts\.googleapis\.com|fonts\.gstatic\.com)[^>]*>[ \t]*\r?\n?`)

func buildFonts() *asset {
	css, err := fontsFS.ReadFile("fonts/fonts.css")
	if err != nil {
		return nil
	}
	out := regexp.MustCompile(`url\(([\w.-]+\.woff2)\)`).ReplaceAllStringFunc(string(css), func(m string) string {
		f := m[4 : len(m)-1]
		b, err := fontsFS.ReadFile("fonts/" + f)
		if err != nil {
			return m
		}
		a := newAsset(strings.TrimSuffix(f, ".woff2"), "woff2", "font/woff2", b, true)
		if strings.HasPrefix(f, "manrope-cyrillic-wght") {
			fontPreload = `<link rel="preload" as="font" type="font/woff2" href="` + a.URL() + `" crossorigin>` + "\n"
		}
		return "url(" + a.URL() + ")"
	})
	return newAsset("fonts", "css", "text/css; charset=utf-8", []byte(minifyCSS(out)), true)
}

// useOwnFonts replaces the Google Fonts links with our fonts' stylesheet.
func useOwnFonts(html string) string {
	if fontsCSS == nil || !googleFontsRe.MatchString(html) {
		return html
	}
	first := true
	return googleFontsRe.ReplaceAllStringFunc(html, func(string) string {
		if !first {
			return ""
		}
		first = false
		return fontPreload + `<link rel="stylesheet" href="` + fontsCSS.URL() + `">` + "\n"
	})
}

// ── Taking the page apart ──

type htmlBlock struct {
	tag          string // "script" or "style"
	start, end   int    // the whole element
	cStart, cEnd int    // its content
	attrs        string
}

// scanBlocks finds the top-level <script> and <style> elements the way a
// browser does: the content ends at the first </script or </style.
func scanBlocks(html string) ([]htmlBlock, error) {
	var out []htmlBlock
	low := strings.ToLower(html)
	for i := 0; i < len(html); {
		j := strings.IndexByte(html[i:], '<')
		if j < 0 {
			break
		}
		i += j
		if strings.HasPrefix(html[i:], "<!--") {
			k := strings.Index(html[i+4:], "-->")
			if k < 0 {
				return nil, fmt.Errorf("unterminated comment at %d", i)
			}
			i += 4 + k + 3
			continue
		}
		tag := ""
		for _, t := range []string{"script", "style"} {
			if strings.HasPrefix(low[i+1:], t) && i+1+len(t) < len(html) {
				c := html[i+1+len(t)]
				if c == ' ' || c == '>' || c == '\t' || c == '\n' {
					tag = t
				}
			}
		}
		if tag == "" {
			i++
			continue
		}
		// the open tag ends at the first ">" outside quotes
		k, q := i+1+len(tag), byte(0)
		for ; k < len(html); k++ {
			c := html[k]
			if q != 0 {
				if c == q {
					q = 0
				}
			} else if c == '"' || c == '\'' {
				q = c
			} else if c == '>' {
				break
			}
		}
		if k >= len(html) {
			return nil, fmt.Errorf("unterminated <%s at %d", tag, i)
		}
		cs := k + 1
		ce := strings.Index(low[cs:], "</"+tag)
		if ce < 0 {
			return nil, fmt.Errorf("no </%s for %d", tag, i)
		}
		ce += cs
		e := strings.IndexByte(html[ce:], '>')
		if e < 0 {
			return nil, fmt.Errorf("unterminated </%s at %d", tag, ce)
		}
		out = append(out, htmlBlock{tag: tag, start: i, end: ce + e + 1, cStart: cs, cEnd: ce, attrs: html[i+1+len(tag) : k]})
		i = ce + e + 1
	}
	return out, nil
}

var attrRe = regexp.MustCompile(`([a-zA-Z-]+)\s*=\s*"([^"]*)"|([a-zA-Z-]+)\s*=\s*'([^']*)'`)

func attrOf(attrs, name string) string {
	for _, m := range attrRe.FindAllStringSubmatch(attrs, -1) {
		if strings.EqualFold(m[1], name) {
			return m[2]
		}
		if strings.EqualFold(m[3], name) {
			return m[4]
		}
	}
	return ""
}

var dataImgRe = regexp.MustCompile(`(src|href)="data:(image/(?:png|jpeg|gif|webp));base64,([A-Za-z0-9+/=]+)"`)

// manifest of the last build (for tests and the measurement scripts)
type siteManifest struct {
	Scripts []string          `json:"scripts"`
	CSS     string            `json:"css"`
	Fonts   string            `json:"fonts"`
	Chunks  []manifestChunk   `json:"chunks"`
	Data    map[string]string `json:"data"`
	Shell   int               `json:"shell"`
	Errors  []string          `json:"errors,omitempty"`
}

type manifestChunk struct {
	URL   string   `json:"url"`
	Key   string   `json:"key"`
	Names []string `json:"names"`
}

var lastManifest siteManifest

// SiteManifest describes how the page was taken apart (JSON).
func SiteManifest() []byte { b, _ := json.MarshalIndent(lastManifest, "", " "); return b }

// buildOpts: the server takes the page apart into files (the default). The
// r45 suites also build an "inline" copy: the same transformed code, nothing
// minified, every file back inside one HTML that opens as a file, so the
// browser suites can run on what the server ships.
type buildOpts struct {
	Inline bool
}

// buildSite returns the shell (the page without the server marker and the
// voice map) and registers its files. On any error it returns the error and
// the caller keeps the page whole.
func buildSite(html string) (string, error) { return buildSiteWith(html, buildOpts{}) }

func buildSiteWith(html string, opt buildOpts) (string, error) {
	minJS, minCSS, minChunk := minifyJS, minifyCSS, minifyChunk
	if opt.Inline {
		same := func(s string) string { return s }
		minJS, minCSS, minChunk = same, same, same
	}
	blocks, err := scanBlocks(html)
	if err != nil {
		return "", err
	}
	man := siteManifest{Data: map[string]string{}}
	var css strings.Builder
	type scr struct {
		b    htmlBlock
		repl string
	}
	var scripts []scr
	var chunks []lazyChunk
	var dataURLs, dataTexts []string
	scope := 0
	nScript := 0
	firstStyle := -1
	for _, b := range blocks {
		switch b.tag {
		case "style":
			if firstStyle < 0 {
				firstStyle = b.start
			}
			css.WriteString(html[b.cStart:b.cEnd])
			css.WriteString("\n")
		case "script":
			typ := strings.ToLower(attrOf(b.attrs, "type"))
			if attrOf(b.attrs, "src") != "" || (typ != "" && typ != "text/javascript" && typ != "application/javascript") {
				continue // JSON blocks and outside scripts stay as they are
			}
			code := cutLazyData(html[b.cStart:b.cEnd], &dataURLs, man.Data, &dataTexts)
			id := attrOf(b.attrs, "id")
			key := id
			if key == "" {
				key = "s" + strconv.Itoa(nScript)
			}
			nScript++
			scope++
			lz, err := lazify(code, key, len(chunks), scope, lazyKeepFn)
			if err != nil {
				man.Errors = append(man.Errors, err.Error())
				log.Printf("web: %s stays whole: %v", key, err)
			}
			for _, c := range lz.Chunks {
				chunks = append(chunks, c)
			}
			body := lz.Script
			if strings.Contains(body, "</script") {
				return "", fmt.Errorf("%s: </script inside", key)
			}
			idAttr := ""
			if id != "" {
				idAttr = ` id="` + id + `"`
			}
			if opt.Inline {
				scripts = append(scripts, scr{b, `<script` + idAttr + `>` + body + `</script>`})
				man.Scripts = append(man.Scripts, key)
				continue
			}
			a := newAsset(assetBase(key), "js", "text/javascript; charset=utf-8", []byte(minJS(body)), false)
			scripts = append(scripts, scr{b, `<script src="` + a.URL() + `"` + idAttr + `></script>`})
			man.Scripts = append(man.Scripts, a.URL())
		}
	}
	if firstStyle < 0 || len(scripts) == 0 {
		return "", fmt.Errorf("no styles or scripts")
	}
	cssLink := "<style>" + css.String() + "</style>\n"
	if !opt.Inline {
		cssAsset := newAsset("app", "css", "text/css; charset=utf-8", []byte(minCSS(css.String())), false)
		man.CSS = cssAsset.URL()
		cssLink = `<link rel="stylesheet" href="` + cssAsset.URL() + `">` + "\n"
	}
	var chunkURLs []string
	var chunkScopes []int
	var inlineChunks strings.Builder
	for i, c := range chunks {
		text := minChunk(chunkText(c))
		u := "lz" + strconv.Itoa(i)
		if opt.Inline {
			// the chunk sits in the page as text; the loader takes it from there
			inlineChunks.WriteString(`<script type="text/plain" id="lzc-` + strconv.Itoa(i) + `">` + text + "</script>\n")
		} else {
			u = newAsset("lz"+strconv.Itoa(i), "js", "text/javascript; charset=utf-8", []byte(text), false).URL()
		}
		chunkURLs = append(chunkURLs, u)
		chunkScopes = append(chunkScopes, c.Scope)
		man.Chunks = append(man.Chunks, manifestChunk{URL: u, Key: c.Key, Names: c.Names})
	}
	if opt.Inline {
		for i, t := range dataTexts {
			inlineChunks.WriteString(`<script type="application/json" id="lzc-` + strconv.Itoa(len(chunks)+i) + `">` + t + "</script>\n")
		}
	}

	// the shell: styles out, scripts as files, preloads and the chunk loader in <head>
	var sb strings.Builder
	last := 0
	si := 0
	for _, b := range blocks {
		if b.tag == "style" {
			sb.WriteString(html[last:b.start])
			if b.start == firstStyle {
				sb.WriteString(inlineChunks.String() + cssLink + lzLoader(chunkURLs, chunkScopes, dataURLs))
			}
			last = b.end
			// the line the block stood on goes too
			if last < len(html) && html[last] == '\n' {
				last++
			}
			continue
		}
		if si < len(scripts) && scripts[si].b.start == b.start {
			sb.WriteString(html[last:b.start])
			sb.WriteString(scripts[si].repl)
			last = b.end
			si++
		}
	}
	sb.WriteString(html[last:])
	shell := sb.String()

	if opt.Inline {
		return shell, nil // lastManifest stays the server's
	}
	// pictures in data: URLs become files (the logo is in the page three times)
	shell = dataImgRe.ReplaceAllStringFunc(shell, func(m string) string {
		p := dataImgRe.FindStringSubmatch(m)
		if len(p[3]) < 1500 {
			return m
		}
		raw, err := base64.StdEncoding.DecodeString(p[3])
		if err != nil {
			return m
		}
		ext := strings.TrimPrefix(p[2], "image/")
		return p[1] + `="` + newAsset("img", ext, p[2], raw, true).URL() + `"`
	})
	shell = useOwnFonts(shell)
	if fontsCSS != nil {
		man.Fonts = fontsCSS.URL()
	}
	man.Shell = len(shell)
	lastManifest = man
	return shell, nil
}

func assetBase(key string) string {
	key = strings.TrimSuffix(strings.TrimSuffix(key, "Script"), "Js")
	var b strings.Builder
	for _, r := range strings.ToLower(key) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
		}
	}
	if b.Len() == 0 {
		return "s"
	}
	return b.String()
}

// lzLoader: the chunk loader with the chunks' and data files' addresses.
func lzLoader(chunkURLs []string, scopes []int, dataURLs []string) string {
	u, _ := json.Marshal(append(append([]string{}, chunkURLs...), dataURLs...))
	if scopes == nil {
		scopes = []int{}
	}
	s, _ := json.Marshal(scopes)
	r := strings.NewReplacer("__URLS__", string(u), "__SCOPES__", string(s), "__NCODE__", strconv.Itoa(len(chunkURLs)))
	return "<script>" + r.Replace(lzRuntime) + "</script>\n"
}

// R45: big data literals (`var NAME = {JSON};` on one line) keep their
// name and their keys, but the values of the big keys come from a file on
// first use: the object gets a getter per such key (__LZ.o). The keys the
// start reads stay in the page (lazyData: name -> keys kept in the page;
// found by the r45 start-up trace).
var lazyData = map[string][]string{
	"R8DOC":     {"ver", "goals", "cards", "demo", "prompt"}, // the rest (kb, tools, custdev…): knowledge base pages and the team's first merge
	"GALLUP_DB": nil,                                         // Gallup strengths: the Gallup section only
}

func cutLazyData(code string, urls *[]string, man map[string]string, texts *[]string) string {
	names := make([]string, 0, len(lazyData))
	for n := range lazyData {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, name := range names {
		re := regexp.MustCompile(`(?m)^var ` + name + ` = (\{.*\});[ \t]*\r?$`)
		m := re.FindStringSubmatchIndex(code)
		if m == nil {
			continue
		}
		raw := []byte(code[m[2]:m[3]])
		keys, err := jsonObjectKeys(raw)
		if err != nil {
			continue
		}
		var all map[string]json.RawMessage
		if json.Unmarshal(raw, &all) != nil {
			continue
		}
		eager := map[string]json.RawMessage{}
		lazy := map[string]json.RawMessage{}
		for _, k := range keys {
			if contains(lazyData[name], k) {
				eager[k] = all[k]
			} else {
				lazy[k] = all[k]
			}
		}
		lb, _ := json.Marshal(lazy)
		eb, _ := json.Marshal(eager)
		kb, _ := json.Marshal(keys)
		a := newAsset("d-"+strings.ToLower(strings.ReplaceAll(name, "_", "")), "json", "application/json; charset=utf-8", lb, false)
		*urls = append(*urls, a.URL())
		*texts = append(*texts, string(lb))
		man[name] = a.URL()
		code = code[:m[0]] + "var " + name + " = __LZ.o(__LZ.n+" + strconv.Itoa(len(*urls)-1) + "," + string(kb) + "," + string(eb) + ");" + code[m[1]:]
	}
	return code
}

func contains(l []string, s string) bool {
	for _, x := range l {
		if x == s {
			return true
		}
	}
	return false
}

// jsonObjectKeys: the top-level keys of a JSON object, in order.
func jsonObjectKeys(raw []byte) ([]string, error) {
	dec := json.NewDecoder(bytes.NewReader(raw))
	if t, err := dec.Token(); err != nil || t != json.Delim('{') {
		return nil, fmt.Errorf("not an object")
	}
	var keys []string
	for dec.More() {
		t, err := dec.Token()
		if err != nil {
			return nil, err
		}
		k, _ := t.(string)
		keys = append(keys, k)
		var skip json.RawMessage
		if err := dec.Decode(&skip); err != nil {
			return nil, err
		}
	}
	return keys, nil
}

// lzRuntime: __LZ.f runs a function from a chunk, __LZ.s keeps an IIFE's
// eval hook, __LZ.pre fetches all chunks (data files first) as soon as the
// page has parsed, so the start's own requests keep the bandwidth (the page
// calls none of them while it starts, see lazy_keep.go). Nothing is read
// synchronously (R56: «Synchronous XMLHttpRequest on the main thread is
// deprecated»): until every chunk is here, a click, a submit, a key outside
// a field, hashchange and popstate are held (not passed to the page) and
// played again, in order, when the chunks arrive; __LZ.when(fn) runs fn then
// (the start's #section link); any other call that comes before its chunk is queued,
// the chunk is fetched at once and the queued calls run in their order when
// it arrives (that call returns undefined). A lazy data key read before its
// file arrives is undefined. __LZ.all() returns a promise of the number of
// code chunks, all evaluated.
const lzRuntime = `(function(){var U=__URLS__,S=__SCOPES__,H={},D={},T={},P={},R={},Q=[],fl=0,ins=0,ok=0,HE=[],L=window.__LZ={n:__NCODE__,s:function(i,e){H[i]=e},log:[],q:0,held:0};
function el(c){return document.getElementById('lzc-'+c)}
function ready(c){return !!(D[c]||T[c]!=null||el(c))}
function all(){if(ok)return 1;for(var c=0;c<U.length;c++)if(!ready(c))return 0;return ok=1}
function tx(c){var e=el(c),t=e?e.textContent:T[c];L.log.push([c,Math.round(performance.now()),0]);T[c]=null;return t}
function get(c,hi){if(ready(c))return Promise.resolve(1);if(P[c])return P[c];return P[c]=fetch(U[c],{credentials:'same-origin',priority:hi?'high':'low'}).then(function(r){if(!r.ok||!/javascript|json/.test(r.headers.get('content-type')||''))throw new Error('Не загрузилась часть платформы ('+r.status+'). Обновите страницу');return r.text()}).then(function(t){P[c]=0;if(!ready(c))T[c]=t;run();return 1},function(e){P[c]=0;throw e})}
function need(c){get(c,1).catch(function(e){R[c]=(R[c]||0)+1;if(R[c]<3)return setTimeout(function(){need(c)},800*R[c]);Q=Q.filter(function(q){return q[0]!==c});run();setTimeout(function(){throw e})})}
function ld(c){var t=tx(c);var e=S[c]?H[S[c]]:0;if(S[c]&&!e)throw new Error('lazy scope '+S[c]);t+='\n//# sourceURL='+U[c];var f=e?e(t):(0,eval)(t);D[c]=f;return f}
function call(c,i,self,args,nt){var f=(D[c]||ld(c))[i];return nt?Reflect.construct(f,args,nt):f.apply(self,args)}
function run(){if(!fl){fl=1;try{while(Q.length&&ready(Q[0][0])){var q=Q.shift();ins=0;try{call(q[0],q[1],q[2],q[3],q[4])}catch(e){setTimeout(function(){throw e})}}}finally{fl=0}}if((HE.length||WH.length)&&all())replay()}
L.f=function(c,i,self,args,nt){if(window.__LZ_TRACE)__LZ_TRACE(c,i,!D[c]&&T[c]==null);if(!ready(c)||(!fl&&Q.length)){var q=[c,i,self,args,nt];if(fl)Q.splice(ins++,0,q);else Q.push(q);L.q++;L.log.push([c,Math.round(performance.now()),2]);need(c);return}return call(c,i,self,args,nt)};
L.all=function(){var a=[];for(var c=0;c<U.length;c++)a.push(get(c,1));return Promise.all(a).then(function(){for(var c=0;c<L.n;c++)if(!D[c])ld(c);run();return L.n})};
L.o=function(c,ks,e){var o={},done=0,W={};function val(k,v){Object.defineProperty(o,k,{value:v,writable:true,configurable:true,enumerable:true})}
function fill(){if(done)return 1;if(!ready(c)){need(c);return 0}done=1;if(window.__LZ_TRACE)__LZ_TRACE(c,-1,T[c]==null);var v=JSON.parse(tx(c));D[c]=1;ks.forEach(function(k){if(!(k in e)&&!W[k])val(k,v[k])});return 1}
ks.forEach(function(k){if(k in e)val(k,e[k]);else Object.defineProperty(o,k,{configurable:true,enumerable:true,get:function(){return fill()?o[k]:void 0},set:function(x){W[k]=1;val(k,x);fill()}})});return o};
L.pre=function(hi){if(L.pre.on)return;L.pre.on=1;var O=[],j=0,act=0,c;for(c=L.n;c<U.length;c++)O.push(c);for(c=0;c<L.n;c++)O.push(c);(function nx(){while(act<6){while(j<O.length&&ready(O[j]))j++;if(j>=O.length){if(!act){L.pre.on=0;if(!all()&&!U.some(function(u,k){return P[k]})){if(++L.pre.fail<3)setTimeout(L.pre,2000*L.pre.fail);else replay()}}return}act++;get(O[j++],hi||0).catch(function(){}).then(function(){act--;nx()})}})()};
var EV=['click','dblclick','auxclick','contextmenu','submit','keydown','hashchange','popstate'],WH=[];
L.when=function(fn){if(all())fn();else{WH.push(fn);L.pre(1)}};
function field(t){return t&&(t.isContentEditable||/^(INPUT|TEXTAREA|SELECT)$/.test(t.tagName))}
function hold(e){if(all()||L.pre.fail>=3){off();return}if(e.type==='keydown'&&(field(e.target)||/^(Shift|Control|Alt|Meta|Tab)$/.test(e.key)))return;e.preventDefault();e.stopImmediatePropagation();L.held++;HE.push([e,Date.now()]);if(e.target!==window)document.documentElement.style.cursor='progress';L.pre.on=0;L.pre(1)}
L.pre.fail=0;
function off(){EV.forEach(function(t){removeEventListener(t,hold,true)})}
function replay(){off();document.documentElement.style.cursor='';var h=HE,w=WH;HE=[];WH=[];w.forEach(function(f){try{f()}catch(e){setTimeout(function(){throw e})}});h.forEach(function(x){var e=x[0],t=e.target,W=t===window;if(!t||!W&&(Date.now()-x[1]>8000||!t.isConnected))return;try{if(e.type==='submit'){t.requestSubmit?t.requestSubmit(e.submitter||undefined):t.submit();return}t.dispatchEvent(new e.constructor(e.type,e))}catch(x){}})}
EV.forEach(function(t){addEventListener(t,hold,true)});
if(document.readyState!=='loading')L.pre(1);else document.addEventListener('DOMContentLoaded',function(){L.pre(1)})})();`

// ── Serving ──

func serveAsset(c *gin.Context, secret []byte, cookie string) {
	name := c.Param("file")
	assetsMu.RLock()
	a := assets[name]
	assetsMu.RUnlock()
	h := c.Writer.Header()
	if a == nil {
		h.Set("Cache-Control", "no-store")
		c.String(http.StatusNotFound, "Файл не найден: обновите страницу")
		return
	}
	if !a.public && !validSession(c, secret, cookie) {
		h.Set("Cache-Control", "no-store")
		c.String(http.StatusUnauthorized, "Войдите в платформу Business Surgery")
		return
	}
	cc := "public, max-age=31536000, immutable"
	if !a.public {
		cc = "private, max-age=31536000, immutable"
	}
	sendBytes(c, a.ctype, cc, a.hash, a.plain, a.gz, a.brotli())
}

// sendBytes picks brotli, gzip or plain by Accept-Encoding, with an ETag per
// encoding and 304 on a match.
func sendBytes(c *gin.Context, ctype, cacheControl, hash string, plain, gz, br []byte) {
	h := c.Writer.Header()
	ae := c.GetHeader("Accept-Encoding")
	enc, body := "", plain
	if br != nil && middleware.AcceptsEncoding(ae, "br") {
		enc, body = "br", br
	} else if gz != nil && middleware.AcceptsEncoding(ae, "gzip") {
		enc, body = "gzip", gz
	}
	etag := `"` + hash + `"`
	if enc != "" {
		etag = `"` + hash + "-" + enc + `"`
	}
	h.Set("Cache-Control", cacheControl)
	h.Set("ETag", etag)
	h.Set("X-Content-Type-Options", "nosniff")
	if gz != nil || br != nil {
		h.Add("Vary", "Accept-Encoding")
	}
	if m := c.GetHeader("If-None-Match"); m != "" && strings.Contains(m, `"`+hash) {
		c.Status(http.StatusNotModified)
		return
	}
	if enc != "" {
		h.Set("Content-Encoding", enc)
	}
	h.Set("Content-Length", strconv.Itoa(len(body)))
	if c.Request.Method == http.MethodHead {
		c.Status(http.StatusOK)
		h.Set("Content-Type", ctype)
		return
	}
	c.Data(http.StatusOK, ctype, body)
}

// AssetNames lists the built files (tests).
func AssetNames() []string {
	assetsMu.RLock()
	defer assetsMu.RUnlock()
	var out []string
	for k := range assets {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func assetByName(name string) *asset {
	assetsMu.RLock()
	defer assetsMu.RUnlock()
	return assets[path.Base(name)]
}
