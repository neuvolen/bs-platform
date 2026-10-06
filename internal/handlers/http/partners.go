package http

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/bnursik/business_surgery_backend/internal/bot"
	"github.com/bnursik/business_surgery_backend/internal/content"
	"github.com/gin-gonic/gin"
)

// R38b: Маркетинг → «Партнёрства и UGC».
//
// Партнёры (Gallup-коучи, бухгалтерии, юристы, коворкинги, HR, агентства,
// ассоциации, медиа) живут в документе клуба bs_partners:
//
//	{partners:[{id, key, code, name, type, stage, owner, next, nextAt, notes, …}],
//	 settings:{rewardClub, razborMode, razborPercent, razborPrice, razborFixed, tiers:[…]},
//	 ledger:[{id, kind:club|razbor, partner, partnerName, lead, leadName, amount, status:accrued|paid, at, paidAt}],
//	 seeded:[keys], deleted:[ids]}
//
//   - Найденные партнёры (content/partners_seed.json) дописываются по ключу,
//     один раз: удалённое или изменённое командой не возвращается.
//   - У каждого партнёра свой код: ссылка на бота t.me/<bot>?start=pt_<code>
//     и ссылка платформы <сервер>/p/<code> (или ?ref=<code> на сайте Tilda).
//     Лид из такой ссылки получает источник «Партнёр: <имя>» и partner=<code>.
//   - Раз в 10 минут сервер смотрит CRM: лид партнёра стал резидентом (won)
//     → в журнал выплат строка «к выплате 100 000 ₸»; разбор проведён (diag)
//     → вознаграждение за разбор, если оно включено. Каждая строка один раз,
//     выплату отмечает команда (status paid).

const (
	partnersKey        = "bs_partners"
	PartnerRewardClub  = 100000 // ₸ за клиента партнёра, оплатившего участие в клубе
	partnerRazborPrice = 50000  // цена разбора (price_migrate.go)
	partnerRazborPct   = 10     // предложение: 10% от разбора, 5 000 ₸
	partnerEvery       = 10 * time.Minute
)

// partnerDefaults: the program the platform shows and the server pays by.
func partnerDefaults() map[string]any {
	return map[string]any{
		"rewardClub":    PartnerRewardClub,
		"razborMode":    "percent", // percent | fixed | off
		"razborPercent": partnerRazborPct,
		"razborPrice":   partnerRazborPrice,
		"razborFixed":   5000,
		"tiers": []any{
			map[string]any{"id": "base", "n": "Партнёр", "from": 0, "reward": PartnerRewardClub, "perks": "Ссылка с меткой, готовые тексты, отчёт по лидам"},
			map[string]any{"id": "silver", "n": "Серебро", "from": 3, "reward": 120000, "perks": "От 3 резидентов за полгода: совместный эфир раз в квартал, логотип на бизнес-завтраке"},
			map[string]any{"id": "gold", "n": "Золото", "from": 6, "reward": 150000, "perks": "От 6 резидентов за полгода: гостевой разбор для клиентов партнёра, место спикера на мероприятиях BS"},
		},
	}
}

var partnerCodeRe = regexp.MustCompile(`^[a-z0-9_]{2,32}$`)

// PartnerCode cleans a code from a link: latin, digits, underscore.
func PartnerCode(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	if !partnerCodeRe.MatchString(s) {
		return ""
	}
	return s
}

func pStr(m map[string]any, k string) string {
	s, _ := m[k].(string)
	return s
}

func pNum(v any) float64 {
	switch x := v.(type) {
	case float64:
		return x
	case int:
		return float64(x)
	case int64:
		return float64(x)
	case json.Number:
		f, _ := x.Float64()
		return f
	case string:
		f, _ := strconv.ParseFloat(strings.ReplaceAll(strings.ReplaceAll(x, " ", ""), " ", ""), 64)
		return f
	}
	return 0
}

// partnersSeedList: the researched partners shipped with the server.
func partnersSeedList() []map[string]any {
	var list []map[string]any
	_ = json.Unmarshal(content.PartnersSeed, &list)
	return list
}

// MergePartnersSeed adds the seed partners the document has never had.
// A key in «seeded» is never added again (the team may have deleted it).
func MergePartnersSeed(doc map[string]any, seed []map[string]any, now time.Time) int {
	parts, _ := doc["partners"].([]any)
	seeded := map[string]bool{}
	var seededL []any
	if s, ok := doc["seeded"].([]any); ok {
		seededL = s
		for _, x := range s {
			seeded[fmt.Sprint(x)] = true
		}
	}
	have := map[string]bool{}
	for _, x := range parts {
		if m, _ := x.(map[string]any); m != nil {
			have["k:"+pStr(m, "key")] = true
			have["c:"+pStr(m, "code")] = true
			have["n:"+strings.ToLower(strings.TrimSpace(pStr(m, "name")))] = true
		}
	}
	n := 0
	for _, s := range seed {
		key := pStr(s, "key")
		if key == "" || seeded[key] {
			continue
		}
		seeded[key] = true
		seededL = append(seededL, key)
		if have["k:"+key] || have["c:"+pStr(s, "code")] || have["n:"+strings.ToLower(pStr(s, "name"))] {
			continue
		}
		p := map[string]any{}
		for k, v := range s {
			p[k] = v
		}
		p["id"] = "p_" + key
		if PartnerCode(pStr(p, "code")) == "" {
			p["code"] = key
		}
		p["stage"] = "found"
		p["owner"] = ""
		p["next"] = ""
		p["notes"] = ""
		p["seed"] = true
		p["created"] = now.UTC().Format(time.RFC3339)
		parts = append(parts, p)
		n++
	}
	doc["partners"] = parts
	doc["seeded"] = seededL
	if _, ok := doc["settings"].(map[string]any); !ok {
		doc["settings"] = partnerDefaults()
	}
	if _, ok := doc["ledger"].([]any); !ok {
		doc["ledger"] = []any{}
	}
	return n
}

// partnerFind: the partner with this code (or name, case-insensitive).
func partnerFind(doc map[string]any, code, name string) map[string]any {
	parts, _ := doc["partners"].([]any)
	name = strings.ToLower(strings.TrimSpace(name))
	for _, x := range parts {
		m, _ := x.(map[string]any)
		if m == nil {
			continue
		}
		if code != "" && strings.EqualFold(pStr(m, "code"), code) {
			return m
		}
		if name != "" && strings.ToLower(strings.TrimSpace(pStr(m, "name"))) == name {
			return m
		}
	}
	return nil
}

func partnerDeleted(doc map[string]any, p map[string]any) bool {
	del, _ := doc["deleted"].([]any)
	for _, x := range del {
		if fmt.Sprint(x) == pStr(p, "id") {
			return true
		}
	}
	return false
}

// PartnerSource: the CRM source of a lead that came by a partner's link.
func PartnerSource(name, code string) string {
	if strings.TrimSpace(name) == "" {
		name = code
	}
	return "Партнёр: " + name
}

// leadPartner: which partner a lead belongs to (the partner field, or the
// source «Партнёр: <имя>»); "" when none.
func leadPartner(doc map[string]any, lead map[string]any) map[string]any {
	if c := pStr(lead, "partner"); c != "" {
		if p := partnerFind(doc, c, ""); p != nil {
			return p
		}
	}
	src := strings.TrimSpace(pStr(lead, "source"))
	for _, pre := range []string{"Партнёр:", "Партнер:"} {
		if rest, ok := strings.CutPrefix(src, pre); ok {
			rest = strings.TrimSpace(rest)
			if p := partnerFind(doc, rest, rest); p != nil {
				return p
			}
		}
	}
	return nil
}

func partnerSettings(doc map[string]any) map[string]any {
	st, _ := doc["settings"].(map[string]any)
	if st == nil {
		st = partnerDefaults()
	}
	return st
}

// PartnerClubReward: the payout for a partner's client who paid for the club:
// the partner's own amount, else its tier's, else the program's (100 000 ₸).
func PartnerClubReward(doc map[string]any, p map[string]any) int {
	if v := pNum(p["reward"]); v > 0 {
		return int(v)
	}
	st := partnerSettings(doc)
	if t := pStr(p, "tier"); t != "" {
		if tiers, ok := st["tiers"].([]any); ok {
			for _, x := range tiers {
				if m, _ := x.(map[string]any); m != nil && pStr(m, "id") == t && pNum(m["reward"]) > 0 {
					return int(pNum(m["reward"]))
				}
			}
		}
	}
	if v := pNum(st["rewardClub"]); v > 0 {
		return int(v)
	}
	return PartnerRewardClub
}

// PartnerRazborReward: the payout for a paid разбор (0: off).
func PartnerRazborReward(doc map[string]any) int {
	st := partnerSettings(doc)
	switch pStr(st, "razborMode") {
	case "off":
		return 0
	case "fixed":
		return int(pNum(st["razborFixed"]))
	}
	pct, price := pNum(st["razborPercent"]), pNum(st["razborPrice"])
	if price <= 0 {
		price = partnerRazborPrice
	}
	if pct <= 0 {
		return 0
	}
	return int(price * pct / 100)
}

// AccruePartnerPayouts adds a ledger row for every partner lead that became
// a resident (and, with the разбор reward on, had the разбор). Each row once,
// keyed by kind and lead; rows already there are never changed. Returns the
// new rows.
func AccruePartnerPayouts(pdoc, crm map[string]any, now time.Time) []map[string]any {
	ledger, _ := pdoc["ledger"].([]any)
	seen := map[string]bool{}
	for _, x := range ledger {
		if m, _ := x.(map[string]any); m != nil {
			seen[pStr(m, "id")] = true
		}
	}
	razbor := PartnerRazborReward(pdoc)
	var fresh []map[string]any
	leads, _ := crm["leads"].([]any)
	for _, x := range leads {
		l, _ := x.(map[string]any)
		if l == nil {
			continue
		}
		p := leadPartner(pdoc, l)
		if p == nil || partnerDeleted(pdoc, p) {
			continue
		}
		lid := pStr(l, "id")
		if lid == "" {
			lid = fmt.Sprintf("tg%d", leadTg(l))
		}
		col := pStr(l, "col")
		add := func(kind string, amount int) {
			id := kind + ":" + lid
			if seen[id] || amount <= 0 {
				return
			}
			seen[id] = true
			row := map[string]any{"id": id, "kind": kind, "partner": pStr(p, "code"), "partnerName": pStr(p, "name"),
				"lead": lid, "leadName": pStr(l, "name"), "amount": amount, "status": "accrued", "at": now.UTC().Format(time.RFC3339)}
			fresh = append(fresh, row)
			ledger = append(ledger, row)
		}
		if col == "won" {
			add("club", PartnerClubReward(pdoc, p))
		}
		if razbor > 0 && (col == "diag" || col == "decide" || col == "later" || col == "won" || l["razborPaid"] == true) {
			add("razbor", razbor)
		}
	}
	if len(fresh) > 0 {
		pdoc["ledger"] = ledger
	}
	return fresh
}

// Partners: the server side of the tab (seed, links, payouts, base import).
type Partners struct {
	docs funnelDocs
	now  func() time.Time
	// Bot: the bot's username for the links (the platform auth knows it).
	Bot func() string
	// Base: where the CRM base import reads from (crm_base.go); nil: off.
	Base BaseSources
}

func NewPartners(docs funnelDocs) *Partners {
	return &Partners{docs: docs, now: time.Now}
}

func (p *Partners) f() *LeadFunnel { return &LeadFunnel{docs: p.docs, now: p.now} }

func (p *Partners) botName() string {
	if p.Bot != nil {
		if n := strings.TrimPrefix(p.Bot(), "@"); n != "" {
			return n
		}
	}
	return refBotName
}

// PartnerBotLink: the partner's link to the bot.
func PartnerBotLink(bot, code string) string {
	return "https://t.me/" + bot + "?start=pt_" + code
}

// SeedOnce merges the researched partners into bs_partners.
func (p *Partners) SeedOnce(ctx context.Context) (int, error) {
	seed := partnersSeedList()
	added := 0
	err := p.f().mutate(ctx, partnersKey, func(doc map[string]any) bool {
		before, _ := json.Marshal(doc)
		added = MergePartnersSeed(doc, seed, p.now())
		after, _ := json.Marshal(doc)
		return string(before) != string(after)
	})
	if err == nil && added > 0 {
		log.Printf("partners: %d added from the seed", added)
	}
	return added, err
}

// AccrueOnce writes the new payout rows; returns how many.
func (p *Partners) AccrueOnce(ctx context.Context) int {
	d, err := p.docs.GetDoc(ctx, "club", "bs_crm")
	if err != nil || d == nil || d.Deleted {
		return 0
	}
	var crm map[string]any
	if json.Unmarshal([]byte(d.Value), &crm) != nil {
		return 0
	}
	n := 0
	err = p.f().mutate(ctx, partnersKey, func(doc map[string]any) bool {
		n = len(AccruePartnerPayouts(doc, crm, p.now()))
		return n > 0
	})
	if err != nil {
		log.Printf("partners: ledger: %v", err)
		return 0
	}
	if n > 0 {
		log.Printf("partners: %d payouts accrued", n)
	}
	return n
}

// Start: the seed, the base import once, then the payouts every 10 minutes.
func (p *Partners) Start(ctx context.Context) {
	go func() {
		c, cancel := context.WithTimeout(ctx, time.Minute)
		if _, err := p.SeedOnce(c); err != nil {
			log.Printf("partners: seed: %v", err)
		}
		cancel()
		if p.Base != nil {
			c, cancel = context.WithTimeout(ctx, 3*time.Minute)
			if _, err := p.ImportBase(c, false); err != nil {
				log.Printf("crm base import: %v", err)
			}
			cancel()
			go p.segLoop(ctx) // R47: the base's segments, at once and for new leads
		}
		t := time.NewTicker(partnerEvery)
		defer t.Stop()
		for {
			c, cancel := context.WithTimeout(ctx, 2*time.Minute)
			p.AccrueOnce(c)
			cancel()
			select {
			case <-ctx.Done():
				return
			case <-t.C:
			}
		}
	}()
}

// partnerName: the partner's name by code ("" if unknown).
func partnerName(ctx context.Context, docs funnelDocs, code string) string {
	d, err := docs.GetDoc(ctx, "club", partnersKey)
	if err != nil || d == nil || d.Deleted {
		return ""
	}
	var doc map[string]any
	if json.Unmarshal([]byte(d.Value), &doc) != nil {
		return ""
	}
	if p := partnerFind(doc, code, ""); p != nil {
		return pStr(p, "name")
	}
	return ""
}

// handlePartnerStart: /start pt_<code>. The lead gets the partner's source
// and the partner field; an old lead keeps its first source.
func (f *LeadFunnel) handlePartnerStart(ctx context.Context, st bot.StartUpdate, code string) bool {
	name := partnerName(ctx, f.docs, code)
	src := PartnerSource(name, code)
	label := name
	if label == "" {
		label = code
	}
	isNew, repeat, err := f.ensureLead(ctx, st.ChatID, st.FirstName, st.LastName, st.Username, src,
		"Нажал Старт по ссылке партнёра ("+label+"), позван в приложение к чек-листам", "Снова нажал Старт по ссылке партнёра ("+label+")", true)
	if err != nil {
		log.Printf("funnel: crm (partner): %v", err)
		return false
	}
	if repeat {
		return true
	}
	if isNew {
		_ = f.mutate(ctx, "bs_crm", func(crm map[string]any) bool {
			leads, _ := crm["leads"].([]any)
			lead := findLeadByTg(leads, st.ChatID)
			if lead == nil || lead["partner"] != nil {
				return false
			}
			lead["partner"] = code
			if name != "" {
				lead["partnerName"] = name
			}
			return true
		})
	}
	if err := f.sendWelcome(ctx, st.ChatID, st.FirstName, ""); err != nil {
		log.Printf("funnel: welcome %d: %v", st.ChatID, err)
		f.replyFail(ctx, st.ChatID, err.Error())
		return false
	}
	what := "приветствие (ссылка партнёра)"
	if isNew {
		if err := f.sendPainAsk(ctx, st.ChatID); err != nil {
			log.Printf("funnel: pain ask %d: %v", st.ChatID, err)
		} else {
			what = "приветствие и выбор чек-листа (ссылка партнёра)"
		}
	}
	f.replyOK(ctx, st.ChatID, what) // R47: the card shows the reply
	return true
}

// ── Ссылка платформы: GET /p/<code>, GET /r?ref=<code> → бот с меткой ──

func (p *Partners) Redirect(c *gin.Context) {
	code := PartnerCode(c.Param("code"))
	if code == "" {
		code = PartnerCode(c.Query("ref"))
	}
	if code == "" {
		c.Redirect(http.StatusFound, "https://t.me/"+p.botName())
		return
	}
	go p.click(code)
	c.Redirect(http.StatusFound, PartnerBotLink(p.botName(), code))
}

// click counts a visit of the partner's link (clicks on the partner card).
func (p *Partners) click(code string) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_ = p.f().mutate(ctx, partnersKey, func(doc map[string]any) bool {
		pt := partnerFind(doc, code, "")
		if pt == nil {
			return false
		}
		pt["clicks"] = int(pNum(pt["clicks"])) + 1
		pt["clickAt"] = p.now().UTC().Format(time.RFC3339)
		return true
	})
}

// Links: GET /api/v1/platform/partners/links → {bot, base} for the cards.
func (p *Partners) Links(c *gin.Context) {
	if !teamOnly(c) {
		return
	}
	base := strings.TrimSuffix(ContentPlatformURL(), "/platform")
	c.JSON(http.StatusOK, gin.H{"bot": p.botName(), "base": base, "site": "https://bxclub.kz"})
}

// Register: the public links and the team's calls.
func (p *Partners) Register(r *gin.Engine, g *gin.RouterGroup) {
	r.GET("/p/:code", p.Redirect)
	r.GET("/r", p.Redirect)
	g.GET("/partners/links", p.Links)
	g.POST("/crm/base-import", p.BaseImport)   // crm_base.go
	g.POST("/crm/segment", p.Segment)          // R47: crm_segments.go
	g.POST("/crm/tilda-import", p.TildaImport) // R47: crm_tilda_import.go
}

// sortPartners: by priority, then score (for logs and tests).
func sortPartners(list []map[string]any) {
	sort.SliceStable(list, func(i, j int) bool {
		a, b := pStr(list[i], "priority"), pStr(list[j], "priority")
		if a != b {
			return a < b
		}
		return pNum(list[i]["score"]) > pNum(list[j]["score"])
	})
}
