package web

import (
	"encoding/json"
	"regexp"
	"strings"
	"sync"
)

// The platform's sections, as its page lists them (var BLOCKS in
// platform.html). The Telegram app shows the same sections in its «Ещё»
// menu: it asks the server for this list, so a section removed from the
// platform (a link, a page) disappears from the app with the next server
// deploy, without a new version of the app.

type NavTab struct {
	ID   string `json:"id"`
	Name string `json:"n"`
}

type NavBlock struct {
	Key  string   `json:"key"`
	Name string   `json:"name"`
	Tabs []NavTab `json:"tabs"`
}

var (
	navBlockRe = regexp.MustCompile(`(?m)^\s*([A-Za-z]\w*):\s*\{name:\s*'([^']*)',\s*tabs:\s*\[`)
	navTabRe   = regexp.MustCompile(`\{id:\s*'([A-Za-z]\w*)',\s*n:\s*'([^']*)'\}`)
	navRouteRe = regexp.MustCompile(`var MKT_ROUTE = \{([^}]*)\}`)
	navKeyRe   = regexp.MustCompile(`([A-Za-z]\w*):\s*\[`)
	navOnce    sync.Once
	navBlocks  []NavBlock
	navJSON    []byte
)

// parseNav reads var BLOCKS = {...}; out of the page.
func parseNav(page string) []NavBlock {
	i := strings.Index(page, "var BLOCKS = {")
	if i < 0 {
		return nil
	}
	body := page[i:]
	if j := strings.Index(body, "\n};"); j > 0 {
		body = body[:j]
	}
	locs := navBlockRe.FindAllStringSubmatchIndex(body, -1)
	out := make([]NavBlock, 0, len(locs))
	for k, l := range locs {
		end := len(body)
		if k+1 < len(locs) {
			end = locs[k+1][0]
		}
		b := NavBlock{Key: body[l[2]:l[3]], Name: body[l[4]:l[5]], Tabs: []NavTab{}}
		for _, t := range navTabRe.FindAllStringSubmatch(body[l[1]:end], -1) {
			b.Tabs = append(b.Tabs, NavTab{ID: t[1], Name: t[2]})
		}
		out = append(out, b)
	}
	return out
}

// navAliases: old section ids the platform still opens in their new place
// (MKT_ROUTE: sCA, sRub, smm, useful).
func navAliases(page string) []string {
	out := []string{}
	if m := navRouteRe.FindStringSubmatch(page); m != nil {
		for _, k := range navKeyRe.FindAllStringSubmatch(m[1], -1) {
			out = append(out, k[1])
		}
	}
	return out
}

func loadNav() {
	navOnce.Do(func() {
		navBlocks = parseNav(string(platformHTML))
		navJSON, _ = json.Marshal(map[string]any{"blocks": navBlocks, "aliases": navAliases(string(platformHTML))})
	})
}

// PlatformNav: the platform's sections as JSON {"blocks":[{key,name,tabs:[{id,n}]}]}.
func PlatformNav() []byte {
	loadNav()
	return navJSON
}

// PlatformNavBlocks: the same list for Go callers.
func PlatformNavBlocks() []NavBlock {
	loadNav()
	return navBlocks
}
