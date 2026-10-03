package http

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/bnursik/business_surgery_backend/internal/content"
	"github.com/gin-gonic/gin"
)

// Главная лида на платформе (R27). Лид входит через Telegram и видит только
// это: экспресс-диагностику (8 ключевых диагнозов библиотеки, по 3 вопроса),
// открытую часть библиотеки (эти 8 диагнозов и 3 инструмента целиком,
// остальное только названиями «Доступно резидентам»), 99 чек-листов в боте,
// ближайшие открытые мероприятия, запись на экспресс-разбор и «Как устроен
// клуб». Команда видит ту же главную в режиме «Лид» (предпросмотр: без
// записи в CRM и без брони).
//
//   GET  /api/v1/platform/lead/home     всё для главной
//   POST /api/v1/platform/lead/diag     {answers:{id:[1,0,1]}} → баллы по органам
//   POST /api/v1/platform/lead/book     {slotId, phone, niche, question}
//   POST /api/v1/platform/lead/request  {phone, niche, question}: свободных окон нет

// LeadExpress: the 8 diagnoses of the express test, one per organ.
var LeadExpress = []string{"seed_dx_0", "seed_dx_16", "seed_dx_7", "seed_dx_4", "seed_dx_18", "seed_dx_6", "seed_dx_9", "seed_dx_26"}

// LeadOpenTools: the tools a lead may read in full.
var LeadOpenTools = []string{"seed_tl_0", "seed_tl_11", "seed_tl_10"}

// leadQuestions: questions per diagnosis in the express test.
const leadQuestions = 3

type LeadHome struct {
	f   *LeadFunnel
	bot func() string // the bot's username
}

var (
	leadLibOnce sync.Once
	leadLibByID map[string]map[string]any
	leadLibAll  []map[string]any
)

func leadLib() (map[string]map[string]any, []map[string]any) {
	leadLibOnce.Do(func() {
		plain, _, _, err := content.LibRich()
		if err != nil {
			log.Printf("lead home: library: %v", err)
			return
		}
		var d struct {
			Tools []map[string]any `json:"tools"`
			Diag  []map[string]any `json:"diag"`
		}
		if err := json.Unmarshal(plain, &d); err != nil {
			log.Printf("lead home: library: %v", err)
			return
		}
		leadLibByID = map[string]map[string]any{}
		for _, it := range append(append([]map[string]any{}, d.Diag...), d.Tools...) {
			id, _ := it["id"].(string)
			if id == "" {
				continue
			}
			delete(it, "_seed")
			leadLibByID[id] = it
			leadLibAll = append(leadLibAll, it)
		}
	})
	return leadLibByID, leadLibAll
}

func lhStr(m map[string]any, k string) string {
	v, _ := m[k].(string)
	return v
}

// leadExpressItems: the express test as the page shows it (no answers' weights).
func leadExpressItems() []gin.H {
	by, _ := leadLib()
	var out []gin.H
	for _, id := range LeadExpress {
		it := by[id]
		if it == nil {
			continue
		}
		var qs []string
		if t, _ := it["test"].(map[string]any); t != nil {
			list, _ := t["questions"].([]any)
			for _, q := range list {
				if m, _ := q.(map[string]any); m != nil && lhStr(m, "q") != "" && len(qs) < leadQuestions {
					qs = append(qs, lhStr(m, "q"))
				}
			}
		}
		out = append(out, gin.H{"id": id, "organ": lhStr(it, "organ"), "title": lhStr(it, "title"), "subtitle": lhStr(it, "subtitle"), "questions": qs})
	}
	return out
}

// LeadDiagResult is one diagnosis of the express test.
type LeadDiagResult struct {
	ID    string `json:"id"`
	Organ string `json:"organ"`
	Title string `json:"title"`
	Score int    `json:"score"`
	Max   int    `json:"max"`
	Level string `json:"level"` // ok | risk | sick
}

// ScoreLeadExpress counts the answers (1 = «да», the sign is there).
func ScoreLeadExpress(answers map[string][]int) ([]LeadDiagResult, error) {
	by, _ := leadLib()
	var out []LeadDiagResult
	for _, id := range LeadExpress {
		it := by[id]
		if it == nil {
			continue
		}
		a, ok := answers[id]
		if !ok || len(a) != leadQuestions {
			return nil, fmt.Errorf("нет ответов на «%s»", lhStr(it, "title"))
		}
		score := 0
		for _, v := range a {
			if v != 0 && v != 1 {
				return nil, fmt.Errorf("ответ только да или нет")
			}
			score += v
		}
		lvl := "ok"
		switch {
		case score >= 2:
			lvl = "sick"
		case score == 1:
			lvl = "risk"
		}
		out = append(out, LeadDiagResult{ID: id, Organ: lhStr(it, "organ"), Title: lhStr(it, "title"), Score: score, Max: leadQuestions, Level: lvl})
	}
	return out, nil
}

func (h *LeadHome) botName() string {
	if h.bot != nil {
		if n := h.bot(); n != "" {
			return n
		}
	}
	return "bsurgery_bot"
}

func (h *LeadHome) leadCard(ctx context.Context, tg int64) map[string]any {
	d, err := h.f.docs.GetDoc(ctx, "club", "bs_crm")
	if err != nil || d == nil || d.Deleted {
		return nil
	}
	var crm map[string]any
	_ = json.Unmarshal([]byte(d.Value), &crm)
	leads, _ := crm["leads"].([]any)
	return findLeadByTg(leads, tg)
}

func (h *LeadHome) events(ctx context.Context, now time.Time) []gin.H {
	out := []gin.H{}
	d, err := h.f.docs.GetDoc(ctx, "club", eventsFeedKey)
	if err != nil || d == nil || d.Deleted {
		return out
	}
	var feed struct {
		Items []map[string]any `json:"items"`
	}
	_ = json.Unmarshal([]byte(d.Value), &feed)
	today := now.In(almaty).Format("2006-01-02")
	sort.SliceStable(feed.Items, func(i, j int) bool {
		return lhStr(feed.Items[i], "date")+lhStr(feed.Items[i], "time") < lhStr(feed.Items[j], "date")+lhStr(feed.Items[j], "time")
	})
	for _, it := range feed.Items {
		if lhStr(it, "date") < today || lhStr(it, "title") == "" {
			continue
		}
		out = append(out, gin.H{"title": lhStr(it, "title"), "date": lhStr(it, "date"), "time": lhStr(it, "time"),
			"place": lhStr(it, "place"), "url": lhStr(it, "url"), "price": lhStr(it, "price")})
		if len(out) >= 4 {
			break
		}
	}
	return out
}

// Home godoc
// @Summary  The lead's home on the platform
// @Description  For role lead (and the team's preview). {user, preview, express:{items}, lib:{open, locked, tools, diag}, events, razbor:{price,kaspiLink,slots,mine}, bot:{username, checklists, razbor}, result}
// @Tags     platform
// @Security BearerAuth
// @Router   /api/v1/platform/lead/home [get]
func (h *LeadHome) Home(c *gin.Context) {
	ctx := c.Request.Context()
	now := h.f.now()
	tg := platformTgID(c)
	by, all := leadLib()
	open := []map[string]any{}
	isOpen := map[string]bool{}
	for _, id := range append(append([]string{}, LeadExpress...), LeadOpenTools...) {
		if it := by[id]; it != nil {
			open = append(open, it)
			isOpen[id] = true
		}
	}
	locked := []gin.H{}
	nTools, nDiag := 0, 0
	for _, it := range all {
		id := lhStr(it, "id")
		if lhStr(it, "kind") == "diag" {
			nDiag++
		} else {
			nTools++
		}
		if isOpen[id] {
			continue
		}
		locked = append(locked, gin.H{"id": id, "kind": lhStr(it, "kind"), "organ": lhStr(it, "organ"), "title": lhStr(it, "title"), "subtitle": lhStr(it, "subtitle")})
	}
	// разбор: price, Kaspi, free slots and the lead's own booking
	razbor := gin.H{"price": razborPrice, "kaspiLink": "", "slots": []gin.H{}, "mine": nil}
	if doc, err := h.f.readSlots(ctx); err == nil {
		price, kaspi := slotsPrice(doc)
		var free []slot
		var mine any
		list, _ := doc["slots"].([]any)
		for _, v := range list {
			s, ok := readSlot(v)
			if !ok {
				continue
			}
			if tg > 0 && s.bookedBy(tg) && s.End().After(now) {
				mine = mineView(s, price, kaspi)
			}
			if s.free() && s.Start.After(now) && s.Start.Before(now.Add(slotsAhead)) {
				free = append(free, s)
			}
		}
		sort.Slice(free, func(i, j int) bool { return free[i].Start.Before(free[j].Start) })
		sl := make([]gin.H, 0, len(free))
		for _, s := range free {
			sl = append(sl, s.public(false))
		}
		razbor = gin.H{"price": price, "kaspiLink": kaspi, "slots": sl, "mine": mine}
	}
	var result any
	name := ""
	if card := h.leadCard(ctx, tg); card != nil {
		result = card["express"]
		name = lhStr(card, "name")
		if name == "Без имени" {
			name = ""
		}
		if rq, ok := card["razborRequest"]; ok {
			razbor["request"] = rq
		}
	}
	bn := h.botName()
	c.JSON(http.StatusOK, gin.H{
		"user":    gin.H{"name": name, "role": platformRole(c)},
		"preview": !isLead(c),
		"express": gin.H{"items": leadExpressItems(), "perDiag": leadQuestions},
		"lib":     gin.H{"open": open, "locked": locked, "tools": nTools, "diag": nDiag},
		"events":  h.events(ctx, now),
		"razbor":  razbor,
		"bot": gin.H{"username": bn, "checklists": "https://t.me/" + bn + "?start=99",
			"razbor": "https://t.me/" + bn + "?start=razbor", "app": "https://t.me/" + bn},
		"result": result,
	})
}

// Diag godoc
// @Summary  Express diagnostic of a lead
// @Description  Body {answers:{<diag id>:[1,0,1]}} for the 8 diagnoses of /lead/home. Answer {results:[{id,organ,title,score,max,level}]}. A lead's result goes to their CRM card (no message to the team).
// @Tags     platform
// @Security BearerAuth
// @Router   /api/v1/platform/lead/diag [post]
func (h *LeadHome) Diag(c *gin.Context) {
	var req struct {
		Answers map[string][]int `json:"answers"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "bad_body"})
		return
	}
	res, err := ScoreLeadExpress(req.Answers)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "bad_answers", "detail": err.Error()})
		return
	}
	if isLead(c) {
		tg := platformTgID(c)
		now := h.f.now()
		scores := map[string]any{}
		var sick, risk []string
		for _, r := range res {
			scores[r.Organ] = r.Score
			switch r.Level {
			case "sick":
				sick = append(sick, r.Organ)
			case "risk":
				risk = append(risk, r.Organ)
			}
		}
		err := h.f.mutate(c.Request.Context(), "bs_crm", func(crm map[string]any) bool {
			leads, _ := crm["leads"].([]any)
			lead := findLeadByTg(leads, tg)
			if lead == nil {
				return false
			}
			lead["express"] = map[string]any{"at": now.UTC().Format(time.RFC3339), "scores": scores, "sick": sick, "risk": risk, "results": res}
			txt := "Прошёл экспресс-диагностику на платформе"
			if len(sick) > 0 {
				txt += ": болит " + strings.Join(sick, ", ")
			} else {
				txt += ": явных болезней нет"
			}
			if len(sick) >= 2 {
				lead["hot"] = true
			}
			addLog(lead, now, txt)
			return true
		})
		if err != nil {
			log.Printf("lead diag %d: %v", tg, err)
		}
	}
	c.JSON(http.StatusOK, gin.H{"results": res})
}

func (h *LeadHome) leadUser(c *gin.Context) (*platformTgUser, bool) {
	if !isLead(c) {
		c.JSON(http.StatusForbidden, gin.H{"error": "preview", "detail": "запись доступна только лиду, в режиме «Лид» это предпросмотр"})
		return nil, false
	}
	tg := platformTgID(c)
	if tg <= 0 {
		c.JSON(http.StatusForbidden, gin.H{"error": "not_telegram_user"})
		return nil, false
	}
	u := &platformTgUser{ID: tg}
	if card := h.leadCard(c.Request.Context(), tg); card != nil {
		parts := strings.Fields(lhStr(card, "name"))
		if len(parts) > 0 && parts[0] != "Без" {
			u.FirstName, u.LastName = parts[0], strings.Join(parts[1:], " ")
		}
		u.Username = strings.TrimPrefix(lhStr(card, "tg"), "@")
	}
	return u, true
}

// Book godoc
// @Summary  A lead books экспресс-разбор on the platform
// @Description  Body {slotId, phone, niche, question}. Same booking as the app: the slot, the CRM card, the bot's confirmation with the Kaspi button. Answer {ok, booking:{slot, price, kaspiLink}} or {error: taken|past|already|not_found|preview}.
// @Tags     platform
// @Security BearerAuth
// @Router   /api/v1/platform/lead/book [post]
func (h *LeadHome) Book(c *gin.Context) {
	r, okBody := readBookReq(c)
	u, ok := h.leadUser(c)
	if !ok {
		return
	}
	if !okBody {
		c.JSON(http.StatusBadRequest, gin.H{"error": "bad_body"})
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	code, got, price, kaspi, err := h.f.BookSlot(ctx, u, r, "платформа")
	if err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": "storage"})
		return
	}
	if code != "" {
		bookErr(c, code)
		return
	}
	c.JSON(http.StatusOK, gin.H{"ok": true, "booking": gin.H{"slot": got.public(true), "price": price, "kaspiLink": kaspi}})
}

// Request godoc
// @Summary  A lead asks for экспресс-разбор when no slot is free
// @Description  Body {phone, niche, question}. The CRM card gets the request and the team one note.
// @Tags     platform
// @Security BearerAuth
// @Router   /api/v1/platform/lead/request [post]
func (h *LeadHome) Request(c *gin.Context) {
	var req struct {
		Phone    string `json:"phone"`
		Niche    string `json:"niche"`
		Question string `json:"question"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "bad_body"})
		return
	}
	u, ok := h.leadUser(c)
	if !ok {
		return
	}
	req.Phone, req.Niche, req.Question = clip(req.Phone, 40), clip(req.Niche, 200), clip(req.Question, 1000)
	if len(phoneKey(req.Phone)) < 10 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "phone", "detail": "нужен телефон, чтобы команда связалась и подобрала время"})
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	now := h.f.now()
	repeat := false
	err := h.f.mutate(ctx, "bs_crm", func(crm map[string]any) bool {
		leads, _ := crm["leads"].([]any)
		lead := findLeadByTg(leads, u.ID)
		if lead == nil {
			return false
		}
		if prev, _ := lead["razborRequest"].(map[string]any); prev != nil {
			if t, err := time.Parse(time.RFC3339, lhStr(prev, "at")); err == nil && now.Sub(t) < 12*time.Hour {
				repeat = true
			}
		}
		lead["razborRequest"] = map[string]any{"at": now.UTC().Format(time.RFC3339), "phone": req.Phone, "niche": req.Niche, "question": req.Question}
		if s, _ := lead["phone"].(string); s == "" {
			lead["phone"] = req.Phone
		}
		if s, _ := lead["niche"].(string); s == "" && req.Niche != "" {
			lead["niche"] = req.Niche
		}
		if c := fmt.Sprint(lead["col"]); c == "new" || c == "work" {
			lead["col"] = "qual"
		}
		lead["hot"], lead["warmStop"] = true, true
		if !repeat {
			addLog(lead, now, "Оставил заявку на экспресс-разбор на платформе (свободных окон не было). Телефон "+req.Phone)
		}
		return true
	})
	if err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": "storage"})
		return
	}
	if !repeat && h.f.send != nil {
		who := strings.TrimSpace(u.FirstName + " " + u.LastName)
		if u.Username != "" {
			who += " @" + u.Username
		}
		note := fmt.Sprintf("📅 Заявка на экспресс-разбор с платформы: %s\nТелефон: %s\nНиша: %s\nВопрос: %s\n🆔 %d\n\nСвободных окон не было, подберите время.",
			strings.TrimSpace(who), req.Phone, dash(req.Niche), dash(req.Question), u.ID)
		for _, a := range h.f.admins {
			_ = h.f.send(ctx, a, note, nil)
		}
	}
	c.JSON(http.StatusOK, gin.H{"ok": true})
}
