package content

import (
	"embed"
	"encoding/json"
	"path"
	"regexp"
	"sort"
	"strings"
	"sync"
)

// The content library for sales: ready texts per guide (Threads, Telegram
// channel, Instagram carousel and reels) and the rubric items (cases,
// objections, the format of the club). Each file in library/ is one item or
// a list of items; a guide item has "guide": "g003", a rubric item has "id"
// and "rubric". Any number of files and items works, none too.
//
//go:embed library/*.json
var libraryFS embed.FS

// LibItem is one source of content.
type LibItem struct {
	Src      string          // g003 or a rubric item id (case_isfandiyar)
	Rubric   string          // "" for a guide; case, objection, format
	Organ    string          // the guide's organ; "" for rubric items
	Title    string          // guide title or the rubric item's title / first line
	Threads  []string        // Threads posts
	Telegram string          // Telegram channel post
	Carousel json.RawMessage // {"slides": [...], "caption": "..."}
	Reels    json.RawMessage // {"hook", "beats", "cta", "duration"}
}

// Has: the item has content for the kind (threads, telegram, carousel, reels).
func (it *LibItem) Has(kind string) bool {
	switch kind {
	case "threads":
		return len(it.Threads) > 0
	case "telegram":
		return it.Telegram != ""
	case "carousel":
		return len(it.Carousel) > 0
	case "reels":
		return len(it.Reels) > 0
	}
	return false
}

// Variants: how many texts of the kind there are.
func (it *LibItem) Variants(kind string) int {
	if kind == "threads" {
		return len(it.Threads)
	}
	if it.Has(kind) {
		return 1
	}
	return 0
}

var (
	libOnce  sync.Once
	libItems []*LibItem
	libBySrc map[string]*LibItem
)

// RubricLabels: how a rubric is called on the platform and in lead sources.
var RubricLabels = map[string]string{"case": "Кейсы", "objection": "Возражения", "format": "Формат клуба", "razbor": "Разбор"}

func rubricOf(file string, raw map[string]json.RawMessage) string {
	var r string
	_ = json.Unmarshal(raw["rubric"], &r)
	r = strings.ToLower(strings.TrimSpace(r))
	switch {
	case strings.HasPrefix(r, "case") || strings.HasPrefix(r, "кейс"):
		return "case"
	case strings.HasPrefix(r, "objection") || strings.HasPrefix(r, "возраж"):
		return "objection"
	case strings.HasPrefix(r, "format") || strings.HasPrefix(r, "формат"):
		return "format"
	case r != "":
		return r
	}
	switch base := strings.TrimSuffix(path.Base(file), ".json"); {
	case strings.HasPrefix(base, "case"):
		return "case"
	case strings.HasPrefix(base, "objection"):
		return "objection"
	case strings.HasPrefix(base, "format"):
		return "format"
	}
	return "rubric"
}

// FirstLine: the first non-empty line of a text, at most n runes.
func FirstLine(s string, n int) string {
	for _, l := range strings.Split(s, "\n") {
		if l = strings.TrimSpace(l); l != "" {
			if r := []rune(l); len(r) > n {
				return strings.TrimSpace(string(r[:n-1])) + "…"
			}
			return l
		}
	}
	return ""
}

func parseLibItem(file string, raw map[string]json.RawMessage) *LibItem {
	str := func(k string) string {
		var s string
		_ = json.Unmarshal(raw[k], &s)
		return strings.TrimSpace(s)
	}
	it := &LibItem{}
	if g := str("guide"); g != "" {
		it.Src = g
		it.Title = GuideTitle(g)
		for _, m := range Guides() {
			if m["id"] == g {
				it.Organ, _ = m["organ"].(string)
			}
		}
	} else if id := str("id"); id != "" {
		it.Src = id
		it.Rubric = rubricOf(file, raw)
		it.Title = str("title")
	} else {
		return nil
	}
	var th []struct {
		Text string `json:"text"`
	}
	if json.Unmarshal(raw["threads"], &th) != nil {
		var one struct {
			Text string `json:"text"`
		}
		if json.Unmarshal(raw["threads"], &one) == nil && one.Text != "" {
			th = append(th, one)
		}
	}
	for _, t := range th {
		if s := strings.TrimSpace(t.Text); s != "" {
			it.Threads = append(it.Threads, s)
		}
	}
	var tg struct {
		Text string `json:"text"`
	}
	if json.Unmarshal(raw["telegram"], &tg) == nil {
		it.Telegram = strings.TrimSpace(tg.Text)
	}
	if c := raw["carousel"]; len(c) > 0 && string(c) != "null" {
		var chk struct {
			Slides []json.RawMessage `json:"slides"`
		}
		if json.Unmarshal(c, &chk) == nil && len(chk.Slides) > 0 {
			it.Carousel = c
		}
	}
	if r := raw["reels"]; len(r) > 0 && string(r) != "null" {
		var chk struct {
			Hook string `json:"hook"`
		}
		if json.Unmarshal(r, &chk) == nil && chk.Hook != "" {
			it.Reels = r
		}
	}
	if it.Title == "" {
		for _, s := range append([]string{it.Telegram}, it.Threads...) {
			if it.Title = strings.TrimRight(FirstLine(startLinkRe.ReplaceAllString(s, ""), 80), " :"); it.Title != "" {
				break
			}
		}
	}
	if it.Title == "" {
		it.Title = it.Src
	}
	return it
}

func loadLibrary() {
	libOnce.Do(func() {
		libBySrc = map[string]*LibItem{}
		files, _ := libraryFS.ReadDir("library")
		for _, f := range files {
			if f.IsDir() || !strings.HasSuffix(f.Name(), ".json") {
				continue
			}
			b, err := libraryFS.ReadFile("library/" + f.Name())
			if err != nil {
				continue
			}
			var list []map[string]json.RawMessage
			if json.Unmarshal(b, &list) != nil {
				var one map[string]json.RawMessage
				if json.Unmarshal(b, &one) != nil {
					continue
				}
				// {"rubric": "case", "items": [...]} is accepted too
				var inner []map[string]json.RawMessage
				if json.Unmarshal(one["items"], &inner) == nil && len(inner) > 0 {
					for _, x := range inner {
						if _, ok := x["rubric"]; !ok && one["rubric"] != nil {
							x["rubric"] = one["rubric"]
						}
					}
					list = inner
				} else {
					list = []map[string]json.RawMessage{one}
				}
			}
			for _, raw := range list {
				it := parseLibItem(f.Name(), raw)
				if it == nil || (!it.Has("threads") && !it.Has("telegram") && !it.Has("carousel") && !it.Has("reels")) {
					continue
				}
				if old := libBySrc[it.Src]; old != nil {
					*old = *it // a later file wins
					continue
				}
				libBySrc[it.Src] = it
				libItems = append(libItems, it)
			}
		}
		sort.SliceStable(libItems, func(i, j int) bool {
			if (libItems[i].Rubric == "") != (libItems[j].Rubric == "") {
				return libItems[i].Rubric == ""
			}
			return libItems[i].Src < libItems[j].Src
		})
	})
}

// Library: all items, guides first (by id), then rubric items.
func Library() []*LibItem { loadLibrary(); return libItems }

// LibraryItem: the item of a source, nil if there is none.
func LibraryItem(src string) *LibItem { loadLibrary(); return libBySrc[src] }

var startLinkRe = regexp.MustCompile(`(?:https?://)?t\.me/bsurgery_bot\?start=([A-Za-z0-9_-]+)`)

// StartParam: the bot start parameter of the first bot link in a text.
func StartParam(text string) string {
	if m := startLinkRe.FindStringSubmatch(text); m != nil {
		return m[1]
	}
	return ""
}

// LinkTitle: what a start parameter's tail (g003, case, case_isfandiyar,
// razbor) is called in a lead's source.
func LinkTitle(rest string) string {
	if t := GuideTitle(rest); t != "" {
		return t
	}
	if it := LibraryItem(rest); it != nil {
		return it.Title
	}
	if l, ok := RubricLabels[rest]; ok {
		return l
	}
	return strings.ReplaceAll(rest, "_", " ")
}
