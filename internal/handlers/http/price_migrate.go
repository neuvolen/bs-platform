package http

import (
	"context"
	"encoding/json"
	"log"
	"regexp"
	"strings"
)

// Разбор подорожал с 30 000 ₸ до 50 000 ₸ (октябрь 2026). Тексты в коде и
// библиотеке уже новые; эта миграция один раз правит то, что лежит в
// документах клуба: цену в bs_slots, запланированные посты bs_content
// (очередь, опубликованное не трогаем) и маркетинговый анализ
// bs_mkt_analysis. Меняется только «30 000 ₸» рядом со словами о разборе,
// остальные суммы остаются.

const priceMigrateKey = "razbor_price_50k" // scope server: done marker

var (
	oldPriceRe   = regexp.MustCompile(`(^|[^\d\x{00a0}\x{202f} ]|[^\d][\x{00a0}\x{202f} ])30([\x{00a0}\x{202f} ]?)000([\x{00a0}\x{202f} ]?(?:₸|тенге|тг))`)
	priceCtxPre  = regexp.MustCompile(`(?i)(разбор|стоимость|60 минут|подвох|дёшево|дорого|цифр|вход за)`)
	priceCtxPost = regexp.MustCompile(`(?i)^[^\n]{0,30}?(за (час|разговор)|разбор)`)
)

// newRazborPrice rewrites the old разбор price in one text.
func newRazborPrice(s string) string {
	if !strings.Contains(s, "000") {
		return s
	}
	var b strings.Builder
	last := 0
	for _, m := range oldPriceRe.FindAllStringSubmatchIndex(s, -1) {
		numStart := m[3] // after the leading guard group
		end := m[1]
		pre := s[max(0, numStart-120):numStart]
		if r := []rune(pre); len(r) > 60 {
			pre = string(r[len(r)-60:])
		}
		post := s[end:min(len(s), end+120)]
		if !priceCtxPre.MatchString(pre) && !priceCtxPost.MatchString(post) {
			continue
		}
		b.WriteString(s[last:numStart])
		b.WriteString("50")
		last = numStart + 2 // skip "30"
	}
	if last == 0 {
		return s
	}
	b.WriteString(s[last:])
	return b.String()
}

// rewriteStrings applies f to every string inside a JSON value.
func rewriteStrings(v any, f func(string) string) (any, bool) {
	switch x := v.(type) {
	case string:
		y := f(x)
		return y, y != x
	case []any:
		ch := false
		for i := range x {
			var c bool
			if x[i], c = rewriteStrings(x[i], f); c {
				ch = true
			}
		}
		return x, ch
	case map[string]any:
		ch := false
		for k := range x {
			nv, c := rewriteStrings(x[k], f)
			if c {
				x[k], ch = nv, true
			}
		}
		return x, ch
	}
	return v, false
}

// MigrateRazborPrice runs once (marker in the server doc).
func (h *PlatformAI) MigrateRazborPrice(ctx context.Context) {
	if h.repo == nil {
		return
	}
	if d, err := h.repo.GetDoc(ctx, "server", priceMigrateKey); err == nil && d != nil && !d.Deleted {
		return
	}
	n := 0
	edit := func(key string, fn func(doc map[string]any) bool) {
		for try := 0; try < 4; try++ {
			d, err := h.repo.GetDoc(ctx, "club", key)
			if err != nil || d == nil || d.Deleted {
				return
			}
			doc := map[string]any{}
			if json.Unmarshal([]byte(d.Value), &doc) != nil || !fn(doc) {
				return
			}
			val, _ := json.Marshal(doc)
			if _, err := h.repo.PutDoc(ctx, "club", key, d.Version, string(val), false, "server:price"); err == nil {
				n++
				return
			}
		}
	}
	edit(slotsDoc, func(doc map[string]any) bool {
		if anyInt(doc["price"]) == legacyRazborPrice {
			doc["price"] = razborPrice
			return true
		}
		return false
	})
	edit(contentKey, func(doc map[string]any) bool {
		q, ok := doc["queue"]
		if !ok {
			return false
		}
		var ch bool
		doc["queue"], ch = rewriteStrings(q, newRazborPrice)
		return ch
	})
	edit("bs_mkt_analysis", func(doc map[string]any) bool {
		_, ch := rewriteStrings(doc, newRazborPrice)
		return ch
	})
	if _, err := h.repo.PutDoc(ctx, "server", priceMigrateKey, 0, `{"price":50000}`, false, "server:price"); err == nil {
		log.Printf("razbor price: 50 000 ₸, %d docs updated", n)
	}
}
