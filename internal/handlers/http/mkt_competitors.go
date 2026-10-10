package http

import (
	"context"
	"encoding/json"
	"log"
	"strings"

	"github.com/bnursik/business_surgery_backend/internal/content"
)

// Прямые конкуренты (октябрь 2026): наставники, бизнес-школы и клубы для
// собственников (Монста, Система успеха и другие). Документ bs_mkt_analysis
// уже правит команда, поэтому один раз дописываем в конец только тех, кого
// там ещё нет (совпадение по ключу в названии). Ничего не удаляем и не
// меняем; удалённого командой после миграции не возвращаем.

const mktDirectKey = "mkt_competitors_direct_v1" // scope server: done marker

func mktDirectList() []map[string]any {
	var list []map[string]any
	_ = json.Unmarshal(content.CompetitorsDirect, &list)
	return list
}

// mergeDirectCompetitors appends missing competitors, returns how many.
func mergeDirectCompetitors(doc map[string]any, list []map[string]any) int {
	comp, _ := doc["competitors"].([]any)
	have := ""
	for _, c := range comp {
		if m, ok := c.(map[string]any); ok {
			n, _ := m["name"].(string)
			have += " " + strings.ToLower(n)
		}
	}
	n := 0
	for _, c := range list {
		key, _ := c["key"].(string)
		name, _ := c["name"].(string)
		if key == "" || strings.Contains(have, strings.ToLower(key)) || strings.Contains(have, strings.ToLower(name)) {
			continue
		}
		item := map[string]any{}
		for k, v := range c {
			if k != "key" {
				item[k] = v
			}
		}
		comp = append(comp, item)
		have += " " + strings.ToLower(name)
		n++
	}
	if n > 0 {
		doc["competitors"] = comp
	}
	return n
}

// SeedMarketingAll: the first version, then the one-time merge (in order).
func (h *PlatformAI) SeedMarketingAll(ctx context.Context) {
	h.SeedMarketing(ctx)
	h.MigrateDirectCompetitors(ctx)
	h.MigrateMktActions(ctx)     // r32_mkt_actions.go
	h.MigrateMktR32e(ctx)        // r32e_mkt.go
	h.MigrateCompetitorsR77(ctx) // R77: Аномалия, Школа трекеров, BURN, Business Booster
}

// R77: «В анализ конкурентов добавь: Аномалия, Школа трекеров, Burn,
// Business Booster». Once: BURN is new and is added; Аномалия, Школа
// трекеров and Business Booster were already in the analysis with a short
// card, so their card takes the research (who it is for, format, what to
// borrow, links, sources, the date) without losing the team's text.
const mktR77Key = "mkt_competitors_r77_v1"

func (h *PlatformAI) MigrateCompetitorsR77(ctx context.Context) {
	var list []map[string]any
	_ = json.Unmarshal(content.CompetitorsR77, &list)
	h.migrateCompetitors(ctx, mktR77Key, list, true)
}

// MigrateDirectCompetitors runs once (marker in the server doc).
func (h *PlatformAI) MigrateDirectCompetitors(ctx context.Context) {
	h.migrateCompetitors(ctx, mktDirectKey, mktDirectList(), false)
}

// enrichCompetitors fills the cards that match (by key or name) with the
// research: empty fields and the new ones are set, strengths and weaknesses
// get the points they lack, a short offer gives way to the researched one.
func enrichCompetitors(doc map[string]any, list []map[string]any) int {
	comp, _ := doc["competitors"].([]any)
	n := 0
	for _, r := range list {
		key, _ := r["key"].(string)
		name, _ := r["name"].(string)
		for _, c := range comp {
			m, ok := c.(map[string]any)
			if !ok {
				continue
			}
			have, _ := m["name"].(string)
			lh := strings.ToLower(have)
			if key == "" || !(strings.Contains(lh, strings.ToLower(key)) || strings.Contains(lh, strings.ToLower(name))) {
				continue
			}
			if _, done := m["asof"]; done {
				break
			}
			for k, v := range r {
				if k == "key" || k == "name" || k == "type" {
					continue
				}
				cur, has := m[k]
				switch k {
				case "strengths", "weaknesses":
					old, _ := cur.([]any)
					seen := map[string]bool{}
					for _, x := range old {
						if s, ok := x.(string); ok {
							seen[strings.ToLower(s)] = true
						}
					}
					add, _ := v.([]any)
					for _, x := range add {
						if s, ok := x.(string); ok && !seen[strings.ToLower(s)] {
							old = append(old, s)
						}
					}
					m[k] = old
				case "offer":
					cs, _ := cur.(string)
					vs, _ := v.(string)
					if len([]rune(cs))*2 < len([]rune(vs)) {
						m[k] = v
					}
				default:
					if s, isStr := cur.(string); !has || cur == nil || (isStr && strings.TrimSpace(s) == "") {
						m[k] = v
					}
				}
			}
			n++
			break
		}
	}
	return n
}

func (h *PlatformAI) migrateCompetitors(ctx context.Context, marker string, list []map[string]any, enrich bool) {
	if h.repo == nil {
		return
	}
	if d, err := h.repo.GetDoc(ctx, "server", marker); err == nil && d != nil && !d.Deleted {
		return
	}
	if len(list) == 0 {
		return
	}
	added, ok := 0, false
	for try := 0; try < 4; try++ {
		d, err := h.repo.GetDoc(ctx, "club", "bs_mkt_analysis")
		if err != nil || d == nil || d.Deleted {
			return // no team copy yet: SeedMarketing ships them with marketing.json
		}
		doc := map[string]any{}
		if json.Unmarshal([]byte(d.Value), &doc) != nil {
			return
		}
		enriched := 0
		if enrich {
			enriched = enrichCompetitors(doc, list)
		}
		if added = mergeDirectCompetitors(doc, list); added == 0 && enriched == 0 {
			ok = true
			break
		}
		val, _ := json.Marshal(doc)
		if _, err := h.repo.PutDoc(ctx, "club", "bs_mkt_analysis", d.Version, string(val), false, "server:competitors"); err == nil {
			ok = true
			break
		}
	}
	if !ok {
		return // try again on the next start
	}
	if _, err := h.repo.PutDoc(ctx, "server", marker, 0, `{"v":1}`, false, "server:competitors"); err == nil {
		log.Printf("marketing: %d competitors added (%s)", added, marker)
	}
}
