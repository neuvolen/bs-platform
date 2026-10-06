package http

import (
	"context"
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/bnursik/business_surgery_backend/internal/club"
	"github.com/bnursik/business_surgery_backend/internal/content"
	pg "github.com/bnursik/business_surgery_backend/internal/repository/pg"
	"github.com/gin-gonic/gin"
)

// R38b: воронки CRM и «База (старые лиды)».
//
// Воронка лида выводится из источника по правилам (content/crm_pipes.json,
// платформа держит тот же список и даёт править токены). Первая воронка с
// совпавшим токеном забирает лида, последняя («База») берёт остальных.
//
// Импорт базы: один раз сервер собирает в bs_crm всех людей, которые у него
// есть: «CRM Лиды» (заявки сайта, форм, бота за всю историю таблицы),
// «Заявки на диагностику», «Диагностика», «Акцепты», «История лид-магнитов»,
// старты бота за 30 дней, чаты WhatsApp. Резиденты и команда пропускаются,
// дубли склеиваются по телефону, Telegram id, @нику (и имени с датой, если
// больше ничего нет). Уже живущие в CRM лиды и удалённые командой не
// трогаются; первый источник сохраняется. Итог пишется в bs_crm.baseImport
// (платформа показывает число) и в server doc crm_base_import_v1 (метка).

const crmBaseMarker = "crm_base_import_v1"

type CrmPipe struct {
	ID  string   `json:"id"`
	N   string   `json:"n"`
	Any []string `json:"any"`
}

// CrmPipesDefault: the pipelines with the default rules.
func CrmPipesDefault() []CrmPipe {
	var p []CrmPipe
	_ = json.Unmarshal(content.CrmPipes, &p)
	return p
}

// crmPipeText: what the rules look at: the source, utm_source, and marks
// #partner / #ref for leads of a partner or a referral.
func crmPipeText(l map[string]any) (src, text string) {
	src = strings.ToLower(strings.TrimSpace(pStr(l, "source")))
	text = src
	if u, ok := l["utm"].(map[string]any); ok {
		if s := strings.ToLower(pStr(u, "utm_source")); s != "" {
			text += " utm:" + s
		}
	}
	if pStr(l, "partner") != "" {
		text += " #partner"
	}
	if l["ref"] != nil && fmt.Sprint(l["ref"]) != "" && fmt.Sprint(l["ref"]) != "0" {
		text += " #ref"
	}
	return
}

// CrmPipeOf: the pipeline of a lead (the id of the first matching one, else the last).
func CrmPipeOf(l map[string]any, pipes []CrmPipe) string {
	if len(pipes) == 0 {
		return ""
	}
	src, text := crmPipeText(l)
	// R47: an imported base stays in «База» whatever its file is called
	// («Telegram контакты» is not the Telegram channel's pipeline)
	if strings.HasPrefix(src, "база:") || (pStr(l, "base") != "" && src == "") {
		return pipes[len(pipes)-1].ID
	}
	for _, p := range pipes {
		for _, t := range p.Any {
			t = strings.ToLower(strings.TrimSpace(t))
			if t == "" {
				continue
			}
			if strings.HasPrefix(t, "^") {
				if strings.HasPrefix(src, t[1:]) {
					return p.ID
				}
			} else if strings.Contains(text, t) {
				return p.ID
			}
		}
	}
	return pipes[len(pipes)-1].ID
}

// BaseSources: where the base import reads (pg.PlatformRepo).
type BaseSources interface {
	SheetsByName(ctx context.Context, names []string) (map[string][][]string, error)
	BotStarts(ctx context.Context) ([]pg.BotStart, error)
	CrmChats(ctx context.Context) ([]pg.CrmChat, error)
	ClubPeople(ctx context.Context) (map[int64]bool, map[string]bool, error)
}

// BaseInput: everything the import found, before the merge.
type BaseInput struct {
	Sheets  map[string][][]string
	Starts  []pg.BotStart
	Chats   []pg.CrmChat
	ClubIDs map[int64]bool
	ClubNms map[string]bool
}

var baseSheets = []string{club.SheetLeads, club.SheetDiagRequests, club.SheetDiagnostics, club.SheetAcceptsLog, club.SheetLMHistory}

var sheetStatusCol = map[string]string{"новый": "new", "в работе": "work", "квалифицирован": "qual", "записан": "meet", "записан на разбор": "meet",
	"диагностика проведена": "diag", "разбор проведён": "diag", "разбор проведен": "diag", "резидент": "won", "отказ": "lost"}

func baseCell(r []string, i int) string {
	if i < 0 || i >= len(r) {
		return ""
	}
	return strings.TrimSpace(r[i])
}

func baseDate(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	if d, ok := club.Date(s); ok {
		return d.In(almaty).Format("02.01.2006")
	}
	if len(s) >= 10 && s[2] == '.' && s[5] == '.' {
		return s[:10]
	}
	return ""
}

func baseTg(s string) string {
	s = strings.TrimSpace(s)
	s = strings.TrimPrefix(strings.TrimPrefix(s, "https://t.me/"), "t.me/")
	s = strings.TrimPrefix(s, "@")
	if s == "" || strings.ContainsAny(s, " /") {
		return ""
	}
	return "@" + s
}

// baseRows: data rows of a sheet (the header row and empty rows skipped).
func baseRows(rows [][]string, head string) [][]string {
	var out [][]string
	for i, r := range rows {
		empty := true
		for _, c := range r {
			if strings.TrimSpace(c) != "" {
				empty = false
				break
			}
		}
		if empty || (i == 0 && strings.EqualFold(baseCell(r, 0), head)) {
			continue
		}
		out = append(out, r)
	}
	return out
}

type baseCand struct {
	lead map[string]any
	from string // the family for the report: «CRM Лиды», «Бот»…
}

// CollectBase turns what the server has into candidate leads (not yet deduped).
func CollectBase(in BaseInput) []baseCand {
	var out []baseCand
	add := func(from string, l map[string]any) {
		out = append(out, baseCand{lead: l, from: from})
	}
	// «CRM Лиды»: Дата, Имя, Телефон, Telegram, Источник, Кампания, Ниша, Оборот, Запрос, Статус, Ответственный, Комментарий
	for _, r := range baseRows(in.Sheets[club.SheetLeads], "Дата") {
		name, phone := baseCell(r, 1), baseCell(r, 2)
		if name == "" && phone == "" {
			continue
		}
		src := baseCell(r, 4)
		if src == "" || src == "Не указан" {
			src = "База: CRM Лиды"
		}
		note := strings.TrimSpace(strings.Join(baseNonEmpty(baseCell(r, 8), baseCell(r, 11)), "\n"))
		l := map[string]any{"name": name, "phone": phone, "tg": baseTg(baseCell(r, 3)), "source": src, "niche": baseCell(r, 6),
			"note": note, "date": baseDate(baseCell(r, 0)), "col": sheetStatusCol[strings.ToLower(baseCell(r, 9))]}
		if c := baseCell(r, 5); c != "" {
			l["campaign"] = c
		}
		if v := baseCell(r, 7); v != "" {
			l["revenue"] = v
		}
		add("CRM Лиды", l)
	}
	// «Заявки на диагностику»: Дата, Имя, Телефон, Ниша, Запрос, Chat ID, Статус
	for _, r := range baseRows(in.Sheets[club.SheetDiagRequests], "Дата") {
		name, phone := baseCell(r, 1), baseCell(r, 2)
		if name == "" && phone == "" {
			continue
		}
		add("Заявки на диагностику", map[string]any{"name": name, "phone": phone, "niche": baseCell(r, 3), "note": baseCell(r, 4),
			"tgId": anyInt(baseCell(r, 5)), "source": "Заявка на диагностику", "date": baseDate(baseCell(r, 0)), "col": sheetStatusCol[strings.ToLower(baseCell(r, 6))]})
	}
	// «Диагностика»: Дата, Chat ID, Имя, Telegram, Общий %, Слабый орган, Сильный орган, Ответы JSON, Источник
	for _, r := range baseRows(in.Sheets[club.SheetDiagnostics], "Дата") {
		id, name := anyInt(baseCell(r, 1)), baseCell(r, 2)
		if id == 0 && name == "" {
			continue
		}
		src := "Диагностика в боте"
		if s := baseCell(r, 8); s != "" {
			src = "Диагностика: " + s
		}
		note := ""
		if p := baseCell(r, 4); p != "" {
			note = "Диагностика: " + p + "%, слабый орган " + baseCell(r, 5)
		}
		add("Диагностика", map[string]any{"name": name, "tgId": id, "tg": baseTg(baseCell(r, 3)), "source": src, "note": note, "date": baseDate(baseCell(r, 0))})
	}
	// Старты бота за 30 дней
	for _, s := range in.Starts {
		if s.ChatID <= 0 {
			continue
		}
		tg := ""
		if s.Username != "" {
			tg = "@" + s.Username
		}
		src := startSource(s.Param)
		add("Бот: старт", map[string]any{"name": strings.TrimSpace(s.First + " " + s.Last), "tgId": s.ChatID, "tg": tg, "source": src,
			"date": s.At.In(almaty).Format("02.01.2006"), "startAt": s.At.UTC().Format(time.RFC3339)})
	}
	// «История лид-магнитов»: Chat ID, Ключ, Дата, Название
	for _, r := range baseRows(in.Sheets[club.SheetLMHistory], "Chat ID") {
		id := anyInt(baseCell(r, 0))
		if id == 0 {
			continue
		}
		t := baseCell(r, 3)
		if t == "" {
			t = baseCell(r, 1)
		}
		add("Лид-магниты", map[string]any{"tgId": id, "source": "Лид-магнит: " + t, "date": baseDate(baseCell(r, 2))})
	}
	// «Акцепты»: Дата/время, Chat ID, Username, First Name, …, Источник
	for _, r := range baseRows(in.Sheets[club.SheetAcceptsLog], "Дата/время") {
		id := anyInt(baseCell(r, 1))
		if id == 0 {
			continue
		}
		src := "Telegram: бот"
		if s := baseCell(r, 6); s != "" {
			src = "Telegram: " + s
		}
		add("Бот: согласие", map[string]any{"name": baseCell(r, 3), "tgId": id, "tg": baseTg(baseCell(r, 2)), "source": src, "date": baseDate(baseCell(r, 0))})
	}
	// Чаты WhatsApp
	for _, c := range in.Chats {
		if phoneDigits(c.Phone) == "" {
			continue
		}
		add("WhatsApp", map[string]any{"name": c.Name, "phone": "+" + phoneDigits(c.Phone), "source": "WhatsApp", "date": c.At.In(almaty).Format("02.01.2006")})
	}
	return out
}

func baseNonEmpty(s ...string) []string {
	var out []string
	for _, x := range s {
		if strings.TrimSpace(x) != "" {
			out = append(out, x)
		}
	}
	return out
}

// baseKeys: the identities of a lead (phone digits, Telegram id, @nick; name+date when nothing else).
func baseKeys(l map[string]any) []string {
	var k []string
	if d := phoneDigits(pStr(l, "phone")); len(d) >= 10 {
		k = append(k, "ph:"+d)
	}
	if id := leadTg(l); id == 0 {
		if v := anyInt(l["tgId"]); v > 0 {
			k = append(k, fmt.Sprintf("tg%d", v))
		}
	} else {
		k = append(k, fmt.Sprintf("tg%d", id))
	}
	if t := strings.ToLower(strings.TrimPrefix(strings.TrimSpace(pStr(l, "tg")), "@")); t != "" {
		k = append(k, "@"+t)
	}
	if len(k) == 0 {
		if n := strings.ToLower(strings.TrimSpace(pStr(l, "name"))); n != "" {
			k = append(k, "nm:"+n+"|"+pStr(l, "date"))
		}
	}
	return k
}

// BaseReport: what the import did (shown on the platform).
type BaseReport struct {
	At      string         `json:"at"`
	Found   int            `json:"found"`   // people in all sources after gluing duplicates
	Added   int            `json:"added"`   // new leads in the CRM
	InCRM   int            `json:"inCrm"`   // already in the CRM (cards left as they are)
	Skipped int            `json:"skipped"` // residents, the team, deleted by the team
	BySrc   map[string]int `json:"bySource"`
	ByPipe  map[string]int `json:"byPipe"`
	Total   int            `json:"total"` // leads in the CRM after the import
}

// MergeBase glues the candidates and adds the new ones to the CRM document.
// Nothing already in the CRM changes; the deleted are not brought back.
func MergeBase(crm map[string]any, cands []baseCand, in BaseInput, now time.Time) BaseReport {
	rep := BaseReport{At: now.UTC().Format(time.RFC3339), BySrc: map[string]int{}, ByPipe: map[string]int{}}
	leads, _ := crm["leads"].([]any)
	exist := map[string]bool{}
	for _, x := range leads {
		if m, _ := x.(map[string]any); m != nil {
			for _, k := range baseKeys(m) {
				exist[k] = true
			}
			if id := pStr(m, "id"); id != "" {
				exist["id:"+id] = true
			}
		}
	}
	deleted := map[string]bool{}
	if del, _ := crm["deleted"].([]any); len(del) > 0 {
		for _, x := range del {
			s := strings.ToLower(fmt.Sprint(x))
			deleted[s] = true
			if strings.HasPrefix(s, "ph:") {
				deleted["ph:"+phoneDigits(s[3:])] = true
			}
		}
	}
	// Glue the candidates: the first one keeps its source, later ones fill gaps.
	type person struct {
		lead map[string]any
		from string
		keys map[string]bool
	}
	var people []*person
	byKey := map[string]*person{}
	for _, c := range cands {
		keys := baseKeys(c.lead)
		if len(keys) == 0 {
			continue
		}
		var p *person
		for _, k := range keys {
			if q := byKey[k]; q != nil {
				p = q
				break
			}
		}
		if p == nil {
			p = &person{lead: map[string]any{}, from: c.from, keys: map[string]bool{}}
			for k, v := range c.lead {
				p.lead[k] = v
			}
			people = append(people, p)
		} else {
			for k, v := range c.lead {
				if k == "source" || k == "date" {
					continue
				}
				if cur := p.lead[k]; cur == nil || cur == "" || cur == int64(0) {
					p.lead[k] = v
				}
			}
		}
		for _, k := range keys {
			p.keys[k] = true
			byKey[k] = p
		}
	}
	pipes := CrmPipesDefault()
	var fresh []any
	for _, p := range people {
		rep.Found++
		l := p.lead
		if id := anyInt(l["tgId"]); id > 0 && in.ClubIDs[id] {
			rep.Skipped++
			continue
		}
		if n := strings.ToLower(strings.TrimSpace(pStr(l, "name"))); n != "" && in.ClubNms[n] {
			rep.Skipped++
			continue
		}
		skip, dup := false, false
		for k := range p.keys {
			if deleted[strings.ToLower(k)] {
				skip = true
			}
			if exist[k] {
				dup = true
			}
		}
		if skip {
			rep.Skipped++
			continue
		}
		if dup {
			rep.InCRM++
			continue
		}
		// a stable id: the funnel's tg<id>, else by phone, else by name and date
		id := ""
		if v := anyInt(l["tgId"]); v > 0 {
			id = fmt.Sprintf("tg%d", v)
			l["tgId"] = v
		} else {
			delete(l, "tgId")
			if d := phoneDigits(pStr(l, "phone")); len(d) >= 10 {
				id = "ph" + d
			} else {
				h := sha1.Sum([]byte(strings.Join(baseKeys(l), "|")))
				id = "bn" + hex.EncodeToString(h[:5])
			}
		}
		if exist["id:"+id] || deleted[strings.ToLower(id)] {
			rep.InCRM++
			continue
		}
		exist["id:"+id] = true
		if pStr(l, "name") == "" {
			if t := pStr(l, "tg"); t != "" {
				l["name"] = t
			} else if ph := pStr(l, "phone"); ph != "" {
				l["name"] = ph
			} else {
				l["name"] = "Подписчик бота " + strconv.FormatInt(anyInt(l["tgId"]), 10)
			}
		}
		col := pStr(l, "col")
		if col == "" {
			col = "new"
		}
		lead := map[string]any{"id": id, "col": col, "sum": "", "imp": "base", "impFrom": p.from}
		for _, k := range []string{"name", "phone", "tg", "tgId", "source", "niche", "note", "date", "campaign", "revenue", "startAt"} {
			if v, ok := l[k]; ok && v != nil && v != "" {
				lead[k] = v
			} else if k == "phone" || k == "tg" || k == "niche" || k == "note" {
				lead[k] = ""
			}
		}
		if pStr(lead, "date") == "" {
			lead["date"] = now.In(almaty).Format("02.01.2006")
		}
		addLog(lead, now, "Загружен из базы сервера ("+p.from+"), источник: "+pStr(lead, "source"))
		fresh = append(fresh, lead)
		rep.Added++
		rep.BySrc[p.from]++
		rep.ByPipe[CrmPipeOf(lead, pipes)]++
	}
	crm["leads"] = append(leads, fresh...)
	rep.Total = len(leads) + len(fresh)
	return rep
}

func (r BaseReport) asMap() map[string]any {
	b, _ := json.Marshal(r)
	var m map[string]any
	_ = json.Unmarshal(b, &m)
	return m
}

// ImportBase runs the import (once unless force); returns the report.
func (p *Partners) ImportBase(ctx context.Context, force bool) (*BaseReport, error) {
	if p.Base == nil {
		return nil, fmt.Errorf("no sources")
	}
	if !force {
		if d, err := p.docs.GetDoc(ctx, "server", crmBaseMarker); err == nil && d != nil && !d.Deleted {
			var r BaseReport
			_ = json.Unmarshal([]byte(d.Value), &r)
			return &r, nil
		}
	}
	in := BaseInput{}
	var err error
	if in.Sheets, err = p.Base.SheetsByName(ctx, baseSheets); err != nil {
		return nil, err
	}
	if in.Starts, err = p.Base.BotStarts(ctx); err != nil {
		log.Printf("crm base import: bot starts: %v", err)
	}
	if in.Chats, err = p.Base.CrmChats(ctx); err != nil {
		log.Printf("crm base import: chats: %v", err)
	}
	if in.ClubIDs, in.ClubNms, err = p.Base.ClubPeople(ctx); err != nil {
		return nil, err
	}
	cands := CollectBase(in)
	var rep BaseReport
	err = p.f().mutate(ctx, "bs_crm", func(crm map[string]any) bool {
		rep = MergeBase(crm, cands, in, p.now())
		crm["baseImport"] = rep.asMap()
		return true
	})
	if err != nil {
		return nil, err
	}
	val, _ := json.Marshal(rep)
	base := 0
	if d, e := p.docs.GetDoc(ctx, "server", crmBaseMarker); e == nil && d != nil {
		base = d.Version
	}
	if _, e := p.docs.PutDoc(ctx, "server", crmBaseMarker, base, string(val), false, "server:crm-base"); e != nil {
		log.Printf("crm base import: marker: %v", e)
	}
	src := make([]string, 0, len(rep.BySrc))
	for k, v := range rep.BySrc {
		src = append(src, fmt.Sprintf("%s %d", k, v))
	}
	sort.Strings(src)
	log.Printf("crm base import: found %d, added %d, already in CRM %d, skipped %d (%s)", rep.Found, rep.Added, rep.InCRM, rep.Skipped, strings.Join(src, ", "))
	return &rep, nil
}

// BaseImport: POST /api/v1/platform/crm/base-import (the team) runs it again;
// the result is the same as the first run plus whatever came since.
func (p *Partners) BaseImport(c *gin.Context) {
	if !teamOnly(c) {
		return
	}
	rep, err := p.ImportBase(c.Request.Context(), true)
	if err != nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, rep)
}
