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
	h.MigrateMktActions(ctx) // r32_mkt_actions.go
	h.MigrateMktR32e(ctx)    // r32e_mkt.go
}

// MigrateDirectCompetitors runs once (marker in the server doc).
func (h *PlatformAI) MigrateDirectCompetitors(ctx context.Context) {
	if h.repo == nil {
		return
	}
	if d, err := h.repo.GetDoc(ctx, "server", mktDirectKey); err == nil && d != nil && !d.Deleted {
		return
	}
	list := mktDirectList()
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
		if added = mergeDirectCompetitors(doc, list); added == 0 {
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
	if _, err := h.repo.PutDoc(ctx, "server", mktDirectKey, 0, `{"v":1}`, false, "server:competitors"); err == nil {
		log.Printf("marketing: %d direct competitors added", added)
	}
}
