// Package content holds the club's own materials shipped with the server:
// 99 guides for clients (JSON for reading in the app, PDF for download)
// and the marketing analysis.
package content

import (
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"encoding/json"
	"sort"
	"sync"
)

//go:embed guides/*.json guides/*.pdf
var guidesFS embed.FS

// Marketing: the first version of the club document bs_mkt_analysis.
//
//go:embed marketing.json
var Marketing []byte

// HowToApp: the picture in the bot's welcome showing how to open the app.
//
//go:embed howto_app.jpg
var HowToApp []byte

// Existing guides made earlier as PDF in the bot (sheet «Лид-магниты»).
var existingMeta = map[string]map[string]any{
	"g015": {"subtitle": "7 инструментов, чтобы заявки превращались в деньги", "promise": "Почему заявки есть, а продаж нет, и что с этим делать", "time": "30 дней", "level": "старт", "hero": map[string]any{"name": "Дамир", "business": "digital-агентство"}},
	"g029": {"subtitle": "Как Айгуль выросла с 200 000 ₸ выручки до 4 млн в месяц", "promise": "5 каналов, которые приводят клиентов без рекламного бюджета", "time": "9 месяцев", "level": "старт", "hero": map[string]any{"name": "Айгуль", "business": "онлайн-школа"}},
	"g043": {"subtitle": "6 шагов до устойчивой команды", "promise": "Профиль, воронка кандидатов, интервью, адаптация, зарплата и удержание", "time": "8 месяцев", "level": "рост", "hero": map[string]any{"name": "Жанна", "business": "ресторан казахской кухни"}},
	"g057": {"subtitle": "5 шагов, чтобы выйти из операционки за 60 дней", "promise": "Карта операционки, ассистент, регламенты, адаптация и контроль", "time": "60 дней", "level": "старт", "hero": map[string]any{"name": "Бауржан", "business": "строительная компания"}},
}

// LeadMagnetKey: the bot's key of an existing PDF guide (Telegram file).
var LeadMagnetKey = map[string]string{"g015": "sales", "g029": "marketing", "g043": "hire", "g057": "delegate"}

type topic struct {
	ID       string `json:"id"`
	Organ    string `json:"organ"`
	Title    string `json:"title"`
	Key      string `json:"key"`
	Existing bool   `json:"existing"`
}

var (
	once     sync.Once
	index    []map[string]any
	indexRaw []byte
	version  string
)

func load() {
	once.Do(func() {
		b, _ := guidesFS.ReadFile("guides/topics.json")
		var ts []topic
		_ = json.Unmarshal(b, &ts)
		h := sha256.New()
		for _, t := range ts {
			m := map[string]any{"id": t.ID, "organ": t.Organ, "title": t.Title, "existing": t.Existing}
			if t.Existing {
				for k, v := range existingMeta[t.ID] {
					m[k] = v
				}
			} else if gb, err := guidesFS.ReadFile("guides/" + t.ID + ".json"); err == nil {
				h.Write(gb)
				var g struct {
					Subtitle, Promise, Time, Level string
					Hero                           struct {
						Name     string `json:"name"`
						Age      any    `json:"age"`
						Business string `json:"business"`
						City     string `json:"city"`
					} `json:"hero"`
					Steps []struct {
						Title     string   `json:"title"`
						Checklist []string `json:"checklist"`
					} `json:"steps"`
				}
				_ = json.Unmarshal(gb, &g)
				var steps []string
				items := 0
				for _, s := range g.Steps {
					steps = append(steps, s.Title)
					items += len(s.Checklist)
				}
				m["subtitle"], m["promise"], m["time"], m["level"] = g.Subtitle, g.Promise, g.Time, g.Level
				m["hero"] = map[string]any{"name": g.Hero.Name, "age": g.Hero.Age, "business": g.Hero.Business, "city": g.Hero.City}
				m["steps"], m["items"] = steps, items
			} else {
				continue
			}
			if p, err := guidesFS.ReadFile("guides/" + t.ID + ".pdf"); err == nil {
				m["pdfKB"] = len(p) / 1024
			}
			index = append(index, m)
		}
		sort.SliceStable(index, func(i, j int) bool { return index[i]["id"].(string) < index[j]["id"].(string) })
		s := h.Sum(nil)
		version = hex.EncodeToString(s[:6])
		indexRaw, _ = json.Marshal(map[string]any{"version": version, "items": index})
	})
}

// GuidesIndex: {"version", "items": [...]} for the catalogue.
func GuidesIndex() []byte { load(); return indexRaw }

func GuidesVersion() string { load(); return version }

// Guides: the catalogue entries.
func Guides() []map[string]any { load(); return index }

// Guide: the full guide JSON (nil for the PDF-only ones).
func Guide(id string) []byte {
	if !valid(id) {
		return nil
	}
	b, _ := guidesFS.ReadFile("guides/" + id + ".json")
	return b
}

// GuidePDF: the shipped PDF (nil for the bot's PDF-only ones).
func GuidePDF(id string) []byte {
	if !valid(id) {
		return nil
	}
	b, _ := guidesFS.ReadFile("guides/" + id + ".pdf")
	return b
}

func GuideTitle(id string) string {
	for _, m := range Guides() {
		if m["id"] == id {
			return m["title"].(string)
		}
	}
	return ""
}

func valid(id string) bool {
	if len(id) != 4 || id[0] != 'g' {
		return false
	}
	for _, c := range id[1:] {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}
