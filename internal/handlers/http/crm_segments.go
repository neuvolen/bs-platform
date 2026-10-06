package http

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"regexp"
	"sort"
	"strings"
	"time"
	"unicode"

	"github.com/bnursik/business_surgery_backend/internal/content"
	"github.com/gin-gonic/gin"
)

// R47: сегменты базы лидов.
//
// Владелец загрузил большую базу контактов («Импорт базы» платформы: лиды с
// base=<файл>, source «База: <файл>», колонка источника уходила в заметку
// «Источник в базе: …») и не знает, что с ней делать. Сервер сам делит всех
// лидов CRM на сегменты и пишет их в карточки:
//
//   - seg: что с человеком делать (одно значение на лида, по порядку):
//     resident (дубль резидента: тот же Telegram id, телефон или полное имя),
//     site (заявка с сайта Tilda), warm (был в боте, на разборе, в работе,
//     недавний контакт), phone (только телефон, без Telegram), cold (есть
//     @ник, в боте не был), none (нет контактов);
//   - segSrc: источник по правилам (content/crm_segs.json, команда правит их
//     в bs_crm.segRules): колонка источника, имя файла, источник, utm;
//     «без источника», если ничего не нашлось;
//   - segFile: файл импорта (или «База сервера: …»), segForm: форма Tilda.
//
// Отчёт (bs_crm.segReport) платформа показывает чипами с числами и панелью
// «Что делать с базой». Запускается: при старте сервера один раз, раз в
// 10 минут, если появились лиды без сегмента (импорт, бот, сайт), и по кнопке
// «Пересчитать» (POST /crm/segment). Карточка меняется только когда сегмент
// поменялся. Ничего никому не отправляется.

const (
	SegResident = "resident"
	SegSite     = "site"
	SegWarm     = "warm"
	SegPhone    = "phone"
	SegCold     = "cold"
	SegNone     = "none"
	segNoSrc    = "без источника"
	segVersion  = 1
	segWarmDays = 180
)

// SegRule: a source label and its tokens.
type SegRule struct {
	N   string   `json:"n"`
	Any []string `json:"any"`
}

// SegRulesDefault: content/crm_segs.json.
func SegRulesDefault() []SegRule {
	var r []SegRule
	_ = json.Unmarshal(content.CrmSegs, &r)
	return r
}

// SegPeople: the club's residents, to find their duplicates in the base.
type SegPeople struct {
	TgIDs  map[int64]bool
	Names  map[string]bool // full names of 2+ words, lower case
	Phones map[string]bool // last 10 digits
}

// segRulesOf: the team's rules from bs_crm.segRules, else the defaults.
func segRulesOf(crm map[string]any) []SegRule {
	if raw, ok := crm["segRules"].([]any); ok && len(raw) > 0 {
		var out []SegRule
		for _, x := range raw {
			m, _ := x.(map[string]any)
			n := strings.TrimSpace(pStr(m, "n"))
			if n == "" {
				continue
			}
			r := SegRule{N: n}
			for _, t := range asList(m["any"]) {
				if s := strings.TrimSpace(fmt.Sprint(t)); s != "" {
					r.Any = append(r.Any, s)
				}
			}
			out = append(out, r)
		}
		if len(out) > 0 {
			return out
		}
	}
	return SegRulesDefault()
}

var segSrcNoteRe = regexp.MustCompile(`(?m)^Источник в базе:\s*(.+)$`)

// segSrcTag: the lead's own source column (srcTag, or the import's note line).
func segSrcTag(l map[string]any) string {
	if s := strings.TrimSpace(pStr(l, "srcTag")); s != "" {
		return s
	}
	if m := segSrcNoteRe.FindStringSubmatch(pStr(l, "note")); m != nil {
		return strings.TrimSpace(m[1])
	}
	return ""
}

func segWords(s string) []string {
	return strings.FieldsFunc(strings.ToLower(s), func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsDigit(r) })
}

// segTokenIn: a token matches a word start (letters and digits only) or,
// with punctuation inside (business.surgery, тг-канал), anywhere.
func segTokenIn(tok string, text string, words []string) bool {
	tok = strings.ToLower(strings.TrimSpace(tok))
	if tok == "" {
		return false
	}
	if strings.IndexFunc(tok, func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsDigit(r) }) >= 0 {
		return strings.Contains(text, tok)
	}
	for _, w := range words {
		if strings.HasPrefix(w, tok) {
			return true
		}
	}
	return false
}

// SegSource: the source label of a lead by the rules.
func SegSource(l map[string]any, rules []SegRule) string {
	parts := []string{segSrcTag(l), pStr(l, "base"), pStr(l, "form"), strings.TrimPrefix(pStr(l, "source"), "База: ")}
	if u, ok := l["utm"].(map[string]any); ok {
		parts = append(parts, pStr(u, "utm_source"), pStr(u, "utm_medium"))
	}
	if pStr(l, "funnel") == "site" || pStr(l, "siteAt") != "" || pStr(l, "tildaAt") != "" {
		parts = append(parts, "tilda")
	}
	text := strings.ToLower(strings.Join(parts, " "))
	words := segWords(text)
	for _, r := range rules {
		for _, t := range r.Any {
			if segTokenIn(t, text, words) {
				return r.N
			}
		}
	}
	return segNoSrc
}

func segIsSite(l map[string]any, src string) bool {
	return pStr(l, "funnel") == "site" || pStr(l, "form") != "" || pStr(l, "siteAt") != "" || pStr(l, "tildaAt") != "" ||
		strings.HasPrefix(strings.ToLower(src), "сайт")
}

// segForm: the Tilda form of a site lead.
func segForm(l map[string]any) string {
	if f := strings.TrimSpace(pStr(l, "form")); f != "" {
		return f
	}
	s := pStr(l, "source")
	for _, p := range []string{"Сайт (Tilda): ", "Сайт: "} {
		if strings.HasPrefix(s, p) {
			return strings.TrimSpace(s[len(p):])
		}
	}
	return ""
}

// segFile: where the lead came into the CRM from as a base.
func segFile(l map[string]any) string {
	if b := strings.TrimSpace(pStr(l, "base")); b != "" {
		return b
	}
	if pStr(l, "imp") == "base" {
		if f := pStr(l, "impFrom"); f != "" {
			return "База сервера: " + f
		}
		return "База сервера"
	}
	if pStr(l, "imp") == "tilda" {
		return "Экспорт Tilda"
	}
	return ""
}

func segLastContact(l map[string]any, now time.Time) bool {
	v := strings.TrimSpace(pStr(l, "lastContact"))
	if v == "" {
		return false
	}
	if d := baseDate(v); d != "" {
		if t, err := time.ParseInLocation("02.01.2006", d, almaty); err == nil {
			return now.Sub(t) < segWarmDays*24*time.Hour
		}
	}
	return false
}

// SegmentOf: the segment of one lead.
func SegmentOf(l map[string]any, src string, ppl SegPeople, now time.Time) string {
	digits := phoneDigits(pStr(l, "phone"))
	key := digits
	if len(key) > 10 {
		key = key[len(key)-10:]
	}
	tgID := leadTg(l)
	if pStr(l, "col") != "won" {
		if tgID > 0 && ppl.TgIDs[tgID] {
			return SegResident
		}
		if len(key) == 10 && ppl.Phones[key] {
			return SegResident
		}
		if n := strings.Join(strings.Fields(strings.ToLower(pStr(l, "name"))), " "); strings.Contains(n, " ") && ppl.Names[n] {
			return SegResident
		}
	}
	if segIsSite(l, src) {
		return SegSite
	}
	col := pStr(l, "col")
	warm := tgID > 0 || pStr(l, "botReplyAt") != "" || pStr(l, "startAt") != "" || l["qz"] != nil ||
		col == "work" || col == "qual" || col == "meet" || col == "diag" || col == "won" || segLastContact(l, now)
	if !warm {
		b, _ := json.Marshal(l["log"])
		low := strings.ToLower(string(b))
		warm = strings.Contains(low, "разбор") || strings.Contains(low, "диагностик")
	}
	if warm {
		return SegWarm
	}
	tg := strings.TrimSpace(strings.TrimPrefix(pStr(l, "tg"), "@"))
	switch {
	case tg != "":
		return SegCold
	case len(digits) >= 10:
		return SegPhone
	}
	return SegNone
}

// SegReport: what the segmentation found (bs_crm.segReport).
type SegReport struct {
	At      string                    `json:"at"`
	V       int                       `json:"v"`
	Total   int                       `json:"total"`
	Changed int                       `json:"changed"`
	BySeg   map[string]int            `json:"bySeg"`
	BySrc   map[string]int            `json:"bySrc"`
	ByFile  map[string]map[string]int `json:"byFile"` // file → source label → leads
	Forms   map[string]int            `json:"forms"`  // Tilda form → leads
}

// SegmentCRM marks every lead of the CRM document; returns the report.
func SegmentCRM(crm map[string]any, ppl SegPeople, now time.Time) SegReport {
	rep := SegReport{At: now.UTC().Format(time.RFC3339), V: segVersion, BySeg: map[string]int{}, BySrc: map[string]int{},
		ByFile: map[string]map[string]int{}, Forms: map[string]int{}}
	rules := segRulesOf(crm)
	for _, x := range asList(crm["leads"]) {
		l, _ := x.(map[string]any)
		if l == nil {
			continue
		}
		rep.Total++
		if t := segSrcTag(l); t != "" && pStr(l, "srcTag") == "" {
			l["srcTag"] = t
		}
		src := SegSource(l, rules)
		seg := SegmentOf(l, src, ppl, now)
		file, form := segFile(l), ""
		if seg == SegSite || src == "Сайт (Tilda)" {
			form = segForm(l)
		}
		set := func(k, v string) {
			if pStr(l, k) == v {
				return
			}
			if v == "" {
				if _, ok := l[k]; !ok {
					return
				}
				delete(l, k)
			} else {
				l[k] = v
			}
			l["segAt"] = rep.At
		}
		before := pStr(l, "segAt")
		set("seg", seg)
		set("segSrc", src)
		set("segFile", file)
		set("segForm", form)
		if pStr(l, "segAt") != before {
			rep.Changed++
		}
		rep.BySeg[seg]++
		rep.BySrc[src]++
		if file != "" {
			if rep.ByFile[file] == nil {
				rep.ByFile[file] = map[string]int{}
			}
			rep.ByFile[file][src]++
		}
		if form != "" {
			rep.Forms[form]++
		}
	}
	return rep
}

func (r SegReport) asMap() map[string]any {
	b, _ := json.Marshal(r)
	var m map[string]any
	_ = json.Unmarshal(b, &m)
	return m
}

// segPeopleSource: what Partners.Base offers for the residents' duplicates.
type segPeopleSource interface {
	ClubPeople(ctx context.Context) (map[int64]bool, map[string]bool, error)
}

type segPhoneSource interface {
	ResidentPhones(ctx context.Context) (map[string]bool, error)
}

func (p *Partners) segPeople(ctx context.Context) SegPeople {
	ppl := SegPeople{TgIDs: map[int64]bool{}, Names: map[string]bool{}, Phones: map[string]bool{}}
	if p.Base == nil {
		return ppl
	}
	if s, ok := p.Base.(segPeopleSource); ok {
		ids, names, err := s.ClubPeople(ctx)
		if err != nil {
			log.Printf("crm segments: residents: %v", err)
		}
		for k := range ids {
			ppl.TgIDs[k] = true
		}
		for n := range names {
			if n = strings.Join(strings.Fields(n), " "); strings.Contains(n, " ") {
				ppl.Names[n] = true
			}
		}
	}
	if s, ok := p.Base.(segPhoneSource); ok {
		ph, err := s.ResidentPhones(ctx)
		if err != nil {
			log.Printf("crm segments: resident phones: %v", err)
		}
		for k := range ph {
			if len(k) > 10 {
				k = k[len(k)-10:]
			}
			ppl.Phones[k] = true
		}
	}
	return ppl
}

// SegmentNow runs the segmentation over bs_crm (force false: only when a
// lead has no segment yet or the rules' version moved).
func (p *Partners) SegmentNow(ctx context.Context, force bool) (*SegReport, error) {
	ppl := p.segPeople(ctx)
	var rep *SegReport
	err := p.f().mutate(ctx, "bs_crm", func(crm map[string]any) bool {
		if !force {
			need := false
			if r, _ := crm["segReport"].(map[string]any); r == nil || anyInt(r["v"]) != segVersion {
				need = true
			}
			for _, x := range asList(crm["leads"]) {
				if l, _ := x.(map[string]any); l != nil && pStr(l, "seg") == "" {
					need = true
					break
				}
			}
			if !need {
				return false
			}
		}
		r := SegmentCRM(crm, ppl, p.now())
		rep = &r
		if r.Changed == 0 && !force {
			if old, _ := crm["segReport"].(map[string]any); old != nil && anyInt(old["v"]) == segVersion {
				return false
			}
		}
		crm["segReport"] = r.asMap()
		return true
	})
	if err != nil {
		return nil, err
	}
	if rep != nil && rep.Changed > 0 {
		keys := make([]string, 0, len(rep.BySeg))
		for k, v := range rep.BySeg {
			keys = append(keys, fmt.Sprintf("%s %d", k, v))
		}
		sort.Strings(keys)
		log.Printf("crm segments: %d leads, %d changed (%s)", rep.Total, rep.Changed, strings.Join(keys, ", "))
	}
	return rep, nil
}

// segLoop: once at the start, then every 10 minutes for new leads.
func (p *Partners) segLoop(ctx context.Context) {
	for {
		c, cancel := context.WithTimeout(ctx, time.Minute)
		if _, err := p.SegmentNow(c, false); err != nil {
			log.Printf("crm segments: %v", err)
		}
		cancel()
		select {
		case <-ctx.Done():
			return
		case <-time.After(partnerEvery):
		}
	}
}

// Segment: POST /api/v1/platform/crm/segment (the team): «Пересчитать».
func (p *Partners) Segment(c *gin.Context) {
	if !teamOnly(c) {
		return
	}
	rep, err := p.SegmentNow(c.Request.Context(), true)
	if err != nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": err.Error()})
		return
	}
	if rep == nil {
		rep = &SegReport{}
	}
	c.JSON(http.StatusOK, rep)
}
