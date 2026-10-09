package http

import (
	"context"
	"encoding/json"
	"log"
	"net/http"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/bnursik/business_surgery_backend/internal/tplpdf"
	"github.com/gin-gonic/gin"
)

// R68: «Gallup-тест резиденты загружают, а он один на всех отправляет».
//
// Причина: детерминированный разбор (R52) брал порядок тем по ПЕРВОМУ
// упоминанию названия в тексте. В отчёте Gallup до личного списка идёт
// одинаковый для всех текст (введение, легенда доменов, описания), где
// слова focus, context, input, belief, развитие, мышление… стоят на одних и
// тех же местах. У разных людей получался один и тот же порядок, а по нему
// из общего кэша (gal2o_, ключ по порядку) приходил один и тот же глубокий
// разбор и PDF. Когда ИИ не отвечал (а он часто отвечал 502), чужой шаблонный
// порядок оставался насовсем.
//
// Теперь порядок берётся только из пронумерованного списка «1. Тема, 2. Тема…»
// (цепочка мест подряд), без списка отчёт не разбирается: «не удалось
// распознать, отправьте PDF из Gallup». Короткий вставленный список без
// номеров принимается как есть.

// GallupUnrecognized: what the person sees when a report has no ranked list.
const GallupUnrecognized = "Не удалось распознать отчёт. Отправьте PDF из Gallup (CliftonStrengths), где таланты пронумерованы по местам"

// gallupShortList: a pasted list of names (no numbers) is read in its order
// only when the text is this short, so a report's prose never counts as one.
const gallupShortList = 900

type gallupCand struct {
	pos, rank int
	key       string
}

var (
	gallupReOnce sync.Once
	gallupReBef  []*regexp.Regexp // «12. Strategic», «12 Стратегия», «12) …»
	gallupReAft  []*regexp.Regexp // «Strategic 12»
	gallupReAny  []*regexp.Regexp // the bare name
)

func gallupNames(th [4]string) []string {
	c := []string{th[1], th[2]}
	for a, k := range gallupAliases {
		if k == th[0] {
			c = append(c, a)
		}
	}
	sort.Strings(c[2:])
	return c
}

func gallupNamePat(names []string) string {
	var alt []string
	for _, c := range names {
		c = strings.ReplaceAll(strings.ToLower(c), "ё", "е")
		p := regexp.QuoteMeta(c)
		p = strings.ReplaceAll(p, `\-`, `[- ]?`)
		p = strings.ReplaceAll(p, "-", `[- ]?`)
		alt = append(alt, p)
	}
	sort.Slice(alt, func(i, j int) bool { return len(alt[i]) > len(alt[j]) })
	return "(?:" + strings.Join(alt, "|") + ")"
}

func gallupRes() {
	gallupReOnce.Do(func() {
		for _, th := range gallupThemes {
			n := gallupNamePat(gallupNames(th))
			gallupReBef = append(gallupReBef, regexp.MustCompile(`(?:^|[^\p{L}\d])(\d{1,2})\s{0,3}[.):]?\s{0,3}`+n+`(?:[^\p{L}]|$)`))
			gallupReAft = append(gallupReAft, regexp.MustCompile(`(?:^|[^\p{L}])`+n+`\s{0,3}[:.-]?\s{0,3}(\d{1,2})(?:[^\d]|$)`))
			gallupReAny = append(gallupReAny, regexp.MustCompile(`(?:^|[^\p{L}])(`+n+`)(?:[^\p{L}]|$)`))
		}
	})
}

// gallupChain: the longest run 1, 2, 3… of different themes in reading order.
func gallupChain(c []gallupCand) []string {
	sort.SliceStable(c, func(i, j int) bool { return c[i].pos < c[j].pos })
	var best []string
	for s := range c {
		if c[s].rank != 1 {
			continue
		}
		used := map[string]bool{c[s].key: true}
		chain, at := []string{c[s].key}, c[s].pos
		for r := 2; r <= len(gallupThemes); r++ {
			nx := -1
			for j := range c {
				if c[j].rank == r && c[j].pos > at && c[j].pos-at <= 8000 && !used[c[j].key] {
					nx = j
					break
				}
			}
			if nx < 0 {
				break
			}
			used[c[nx].key] = true
			chain = append(chain, c[nx].key)
			at = c[nx].pos
		}
		if len(chain) > len(best) {
			best = chain
		}
	}
	return best
}

// gallupRankedOrder reads the person's own ranking from a report: themes by
// their numbers. ok=false: no ranked list (at least 5 places) in the text.
func gallupRankedOrder(text string) ([]string, bool) {
	gallupRes()
	low := strings.ReplaceAll(strings.ToLower(text), "ё", "е")
	var bef, aft []gallupCand
	for i, th := range gallupThemes {
		for _, m := range gallupReBef[i].FindAllStringSubmatchIndex(low, -1) {
			if r := atoiSafe(low[m[2]:m[3]]); r >= 1 && r <= 34 {
				bef = append(bef, gallupCand{m[2], r, th[0]})
			}
		}
		for _, m := range gallupReAft[i].FindAllStringSubmatchIndex(low, -1) {
			if r := atoiSafe(low[m[2]:m[3]]); r >= 1 && r <= 34 {
				aft = append(aft, gallupCand{m[2], r, th[0]})
			}
		}
	}
	out := gallupChain(bef)
	if a := gallupChain(aft); len(a) > len(out) {
		out = a
	}
	if len(out) < 5 {
		out = nil
		// a short pasted list without numbers: «Стратегия, Ученик, Собиратель…»
		if utf8.RuneCountInString(strings.TrimSpace(text)) <= gallupShortList {
			out = gallupFirstSeen(low)
		}
	}
	if len(out) < 5 || gallupCanonical(out) {
		return nil, false
	}
	return out, true
}

func atoiSafe(s string) int {
	n := 0
	for _, r := range s {
		if r < '0' || r > '9' {
			return 0
		}
		n = n*10 + int(r-'0')
	}
	return n
}

func gallupFirstSeen(low string) []string {
	gallupRes()
	type hit struct {
		k   string
		pos int
	}
	var hits []hit
	for i, th := range gallupThemes {
		if m := gallupReAny[i].FindStringSubmatchIndex(low); m != nil {
			hits = append(hits, hit{th[0], m[2]})
		}
	}
	sort.SliceStable(hits, func(i, j int) bool { return hits[i].pos < hits[j].pos })
	out := make([]string, len(hits))
	for i, h := range hits {
		out[i] = h.k
	}
	return out
}

// gallupCanonical: the order of the theme list itself (a legend of the report
// or of the platform), not a person's ranking.
func gallupCanonical(order []string) bool {
	n := min(len(order), 10)
	if n < 5 {
		return false
	}
	var list []string
	for _, t := range gallupThemes {
		list = append(list, t[0])
	}
	for _, ref := range [][]string{list, gallupDBOrder} {
		same := true
		for i := 0; i < n; i++ {
			if order[i] != ref[i] {
				same = false
				break
			}
		}
		if same {
			return true
		}
	}
	return false
}

// gallupDBOrder: the key order of the platform's theme base (GALLUP_DB).
var gallupDBOrder = []string{"context", "strategic", "individualization", "analytical", "connectedness", "significance", "focus", "ideation", "responsibility", "maximizer"}

// ── Аудит: у кого разбор построен не по своему отчёту ──

// GallupFixKey: the audit result the platform shows in «Замеры» → Gallup
// (team only; written only by the server).
const GallupFixKey = "bs_gallup_fix"

type gallupFixItem struct {
	Name   string   `json:"name"`
	Status string   `json:"status"` // ready: исправленный порядок из своего отчёта; upload: отчёт не сохранился
	Why    string   `json:"why"`
	Was    []string `json:"was"`              // топ-5, который человек получил
	Themes []string `json:"themes,omitempty"` // исправленный порядок
	At     string   `json:"at"`
	SentAt string   `json:"sentAt,omitempty"`
}

type gallupFixDoc struct {
	V     int                       `json:"v"`
	At    string                    `json:"at"`
	Items map[string]*gallupFixItem `json:"items"`
}

type gallupStored struct {
	Date   string   `json:"date"`
	Themes []string `json:"themes"`
	Manual any      `json:"manual"`
	AI     *struct {
		Talents []struct {
			Key string `json:"key"`
		} `json:"talents"`
	} `json:"ai"`
}

func (g *gallupStored) manual() bool {
	switch v := g.Manual.(type) {
	case bool:
		return v
	case float64:
		return v != 0
	case string:
		return v != "" && v != "0"
	}
	return false
}

func gallupTop(t []string, n int) []string {
	if len(t) > n {
		t = t[:n]
	}
	return append([]string(nil), t...)
}

// GallupAudit finds boards whose Gallup analysis is not built from the
// person's own report and stores what to fix in bs_gallup_fix. Residents are
// never messaged here: the team sends the corrected analysis with one click.
func (h *PlatformAI) GallupAudit(ctx context.Context) (*gallupFixDoc, error) {
	boards, err := h.repo.LiveBoards(ctx)
	if err != nil {
		return nil, err
	}
	person := map[string]string{} // board id -> resident
	prof := map[string]*gallupStored{}
	for _, b := range boards {
		var d struct {
			Name   string            `json:"name"`
			Info   map[string]string `json:"info"`
			Gallup *gallupStored     `json:"gallup"`
		}
		_ = json.Unmarshal(b.Data, &d)
		n := strings.TrimSpace(b.Resident)
		if n == "" {
			n = strings.TrimSpace(d.Info["res"])
		}
		if n == "" {
			n = strings.TrimSpace(strings.TrimPrefix(d.Name, "Разбор · "))
		}
		person[b.ID] = n
		if d.Gallup != nil && len(d.Gallup.Themes) > 0 {
			prof[b.ID] = d.Gallup
		}
	}
	// the section copies (club and each resident's own) are newer than the board copy
	docs, err := h.repo.DocsByKey(ctx, "bs_gallup")
	if err != nil {
		return nil, err
	}
	for _, d := range docs {
		var m map[string]*gallupStored
		if json.Unmarshal([]byte(d.Value), &m) != nil {
			continue
		}
		for id, g := range m {
			if g == nil || len(g.Themes) == 0 {
				continue
			}
			if cur := prof[id]; cur == nil || g.Date >= cur.Date {
				prof[id] = g
			}
		}
	}
	texts := map[string]string{}
	if tds, err := h.repo.DocsByKey(ctx, "bs_gallup_txt"); err == nil {
		for _, d := range tds {
			var m map[string]string
			if json.Unmarshal([]byte(d.Value), &m) == nil {
				for id, t := range m {
					if strings.TrimSpace(t) != "" {
						texts[id] = t
					}
				}
			}
		}
	}
	prev := &gallupFixDoc{}
	if d, _ := h.repo.GetDoc(ctx, "club", GallupFixKey); d != nil && !d.Deleted {
		_ = json.Unmarshal([]byte(d.Value), prev)
	}
	now := time.Now().UTC().Format(time.RFC3339)
	out := &gallupFixDoc{V: 1, At: now, Items: map[string]*gallupFixItem{}}
	// one order shared by different people: a template, not a ranking (34! orders)
	share := map[string]map[string]bool{}
	for id, g := range prof {
		if g.manual() || len(g.Themes) < 5 {
			continue
		}
		k := strings.Join(gallupTop(g.Themes, 10), ",")
		if share[k] == nil {
			share[k] = map[string]bool{}
		}
		share[k][normName(person[id])+"|"+id] = true
	}
	persons := func(k string) int {
		seen := map[string]bool{}
		for p := range share[k] {
			n := strings.SplitN(p, "|", 2)[0]
			if n == "" {
				n = p
			}
			seen[n] = true
		}
		return len(seen)
	}
	ids := make([]string, 0, len(prof))
	for id := range prof {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		g := prof[id]
		if g.manual() || len(g.Themes) < 5 {
			continue
		}
		var why string
		k := strings.Join(gallupTop(g.Themes, 10), ",")
		if persons(k) > 1 {
			why = "такой же порядок талантов ещё у других резидентов"
		} else if gallupCanonical(g.Themes) {
			why = "порядок списка тем, а не места из отчёта"
		}
		own, ok := []string(nil), false
		if t := texts[id]; t != "" {
			own, ok = gallupRankedOrder(t)
			if ok && why == "" && !sameOrder(gallupTop(own, 10), gallupTop(g.Themes, 10)) {
				why = "порядок не совпадает с отчётом, который загрузили"
			}
		}
		if why == "" {
			continue
		}
		it := &gallupFixItem{Name: person[id], Why: why, Was: gallupTop(g.Themes, 5), At: now, Status: "upload"}
		if ok {
			it.Status, it.Themes = "ready", own
		}
		if p := prev.Items[id]; p != nil && p.SentAt != "" && sameOrder(p.Was, it.Was) {
			it.SentAt = p.SentAt
		}
		out.Items[id] = it
	}
	// sent fixes stay listed as done
	for id, p := range prev.Items {
		if _, ok := out.Items[id]; !ok && p != nil && p.SentAt != "" {
			out.Items[id] = p
		}
	}
	b, _ := json.Marshal(out)
	if err := h.repo.PutServerDoc(ctx, GallupFixKey, string(b)); err != nil {
		return out, err
	}
	var names []string
	ready, up := 0, 0
	for _, it := range out.Items {
		if it.SentAt != "" {
			continue
		}
		names = append(names, it.Name)
		if it.Status == "ready" {
			ready++
		} else {
			up++
		}
	}
	sort.Strings(names)
	log.Printf("gallup audit: affected %d (ready %d, need upload %d): %s", len(names), ready, up, strings.Join(names, ", "))
	return out, nil
}

// GallupAuditLoop: at start (after the boot rush) and every 6 hours.
func (h *PlatformAI) GallupAuditLoop(ctx context.Context, first, every time.Duration) {
	t := time.NewTimer(first)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		if _, err := h.GallupAudit(ctx); err != nil {
			log.Printf("gallup audit: %v", err)
		}
		t.Reset(every)
	}
}

// POST /api/v1/platform/gallup/fix/send {board, doc} (team): the corrected
// analysis as a PDF to the resident through the bot, one click per resident.
func (h *PlatformAI) GallupFixSend(c *gin.Context) {
	if isResident(c) || isLead(c) {
		forbidden(c, "team_only")
		return
	}
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, gallupPDFMax)
	var req struct {
		Board string           `json:"board"`
		Doc   tplpdf.GallupDoc `json:"doc"`
	}
	if err := c.ShouldBindJSON(&req); err != nil || req.Board == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "bad_request"})
		return
	}
	ctx := c.Request.Context()
	fix := &gallupFixDoc{}
	if d, _ := h.repo.GetDoc(ctx, "club", GallupFixKey); d != nil {
		_ = json.Unmarshal([]byte(d.Value), fix)
	}
	it := fix.Items[req.Board]
	if it == nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "not_affected", "message": "Этого резидента нет в списке исправлений"})
		return
	}
	sanitizeGallupDoc(&req.Doc)
	if len(req.Doc.Talents) < 5 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "empty", "message": "Сначала загрузите отчёт резидента: разбора ещё нет"})
		return
	}
	if req.Doc.Name == "" {
		req.Doc.Name = gpStr(it.Name, 80)
	}
	if h.SendDoc == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "no_bot", "message": "Бот не подключён"})
		return
	}
	chat, name, _ := h.repo.ResidentTgByName(ctx, it.Name)
	if chat == 0 {
		c.JSON(http.StatusConflict, gin.H{"error": "no_chat", "message": "Резидент не найден в клубе или без Telegram"})
		return
	}
	pdf, err := tplpdf.RenderGallup(&req.Doc)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "render"})
		return
	}
	_, file := tplFileNames("BS Gallup " + req.Doc.Name)
	caption := "Исправленный разбор Gallup. В прошлый раз разбор собрался не по вашему отчёту, извините. Здесь ваши таланты по вашим местам."
	if f := firstName(name); f != "" && f != "Резидент" {
		caption = f + ", привет! " + caption
	}
	if err := h.SendDoc(ctx, chat, file+".pdf", pdf, caption); err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": "send", "message": "Telegram не принял файл: " + err.Error()})
		return
	}
	it.SentAt = time.Now().UTC().Format(time.RFC3339)
	b, _ := json.Marshal(fix)
	_ = h.repo.PutServerDoc(context.Background(), GallupFixKey, string(b))
	log.Printf("gallup fix: corrected analysis sent to %s", it.Name)
	c.JSON(http.StatusOK, gin.H{"ok": true, "name": it.Name, "sentAt": it.SentAt})
}

// POST /api/v1/platform/gallup/fix/audit (team): run the audit now.
func (h *PlatformAI) GallupFixAudit(c *gin.Context) {
	if isResident(c) || isLead(c) {
		forbidden(c, "team_only")
		return
	}
	d, err := h.GallupAudit(c.Request.Context())
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "audit"})
		return
	}
	c.JSON(http.StatusOK, d)
}
