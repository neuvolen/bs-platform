package http

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/bnursik/business_surgery_backend/internal/bot"
	pg "github.com/bnursik/business_surgery_backend/internal/repository/pg"
	"github.com/gin-gonic/gin"
)

// Реферальная программа: резидент зовёт предпринимателя по своей ссылке
// t.me/bsurgery_bot?start=ref_<chatId>. Если тот станет резидентом, резиденту
// 100 000 ₸.
//
//   - /start ref_<chatId> нового человека отвечает сервер: то же приветствие,
//     что и всем, лид в CRM с ref, refName и источником «Реферал: <имя>»;
//     пригласившему (резиденту или команде) одно сообщение.
//   - GET /api/v1/app/referral: ссылка, готовый текст и кого человек позвал.
//   - Раз в 10 минут сервер смотрит CRM: реферал в колонке won → поздравление
//     пригласившему и команде, один раз (что уже отправлено, хранит server doc
//     ref_won).

const (
	refBotName  = "bsurgery_bot"
	RefBonus    = 100000
	refWonDoc   = "ref_won"
	refWonEvery = 10 * time.Minute
)

// RefLink is the personal invitation link of a resident.
func RefLink(chatID int64) string {
	return fmt.Sprintf("https://t.me/%s?start=ref_%d", refBotName, chatID)
}

func refText(link string) string {
	return "Привет! Делюсь находкой: Business Surgery, клуб бизнес-трекинга в Алматы.\n" +
		"В их боте бесплатно открыты 99 гайдов по финансам, продажам, команде и маркетингу, с цифрами и пошаговыми планами.\n" +
		"Если захочешь разобрать свой бизнес лично, там же можно записаться на разбор с основателями клуба.\n" +
		"Ссылка: " + link
}

// refInviter: who is behind ref_<chatId>.
type refInviter interface {
	ResidentByTg(ctx context.Context, tgID int64) (string, bool, error)
}

func anyInt(v any) int64 {
	switch x := v.(type) {
	case float64:
		return int64(x)
	case int64:
		return x
	case int:
		return int64(x)
	case json.Number:
		n, _ := x.Int64()
		return n
	case string:
		n, _ := strconv.ParseInt(strings.TrimSpace(x), 10, 64)
		return n
	}
	return 0
}

// WithReferrals wraps the funnel's start hook: /start ref_<chatId> is a referral.
// residents tells who is a resident, team is the platform team (PLATFORM_TEAM).
func (f *LeadFunnel) WithReferrals(residents refInviter, team map[int64]string) bot.StartHook {
	return func(ctx context.Context, st bot.StartUpdate) bool {
		if strings.HasPrefix(st.Param, "ref_") {
			if inv, err := strconv.ParseInt(strings.TrimPrefix(st.Param, "ref_"), 10, 64); err == nil && inv > 0 && inv != st.ChatID {
				return f.handleRefStart(ctx, st, inv, residents, team)
			}
			st.Param = "" // a broken or own link: an ordinary start
		}
		return f.HandleStart(ctx, st)
	}
}

func (f *LeadFunnel) handleRefStart(ctx context.Context, st bot.StartUpdate, inviter int64, residents refInviter, team map[int64]string) bool {
	refName, member := "", false
	if n, ok := team[inviter]; ok {
		refName, member = n, true
	}
	if !member && residents != nil {
		if n, _, err := residents.ResidentByTg(ctx, inviter); err == nil && n != "" {
			refName, member = n, true
		}
	}
	if refName == "" { // not one of ours: perhaps a lead in the CRM
		if d, err := f.docs.GetDoc(ctx, "club", "bs_crm"); err == nil && d != nil {
			var crm map[string]any
			_ = json.Unmarshal([]byte(d.Value), &crm)
			leads, _ := crm["leads"].([]any)
			if l := findLeadByTg(leads, inviter); l != nil {
				refName, _ = l["name"].(string)
			}
		}
	}
	label := refName
	if label == "" {
		label = "id " + strconv.FormatInt(inviter, 10)
	}
	src := "Реферал: " + label
	isNew, repeat, err := f.ensureLead(ctx, st.ChatID, st.FirstName, st.LastName, st.Username, src,
		"Нажал Старт по реферальной ссылке ("+label+"), позван в приложение к 99 гайдам", "Снова нажал Старт по реферальной ссылке ("+label+")", true)
	if err != nil {
		log.Printf("funnel: crm (ref): %v", err)
		return false
	}
	if repeat {
		return true
	}
	if err := f.sendWelcome(ctx, st.ChatID, st.FirstName, ""); err != nil {
		log.Printf("funnel: welcome %d: %v", st.ChatID, err)
		return false
	}
	if !isNew {
		return true // already a lead: the first source stays
	}
	name := strings.TrimSpace(st.FirstName + " " + st.LastName)
	notify := false
	_ = f.mutate(ctx, "bs_crm", func(crm map[string]any) bool {
		notify = false
		leads, _ := crm["leads"].([]any)
		lead := findLeadByTg(leads, st.ChatID)
		if lead == nil || (lead["ref"] != nil && anyInt(lead["ref"]) != inviter) {
			return false
		}
		lead["ref"] = inviter
		lead["refName"] = refName
		if !member {
			lead["refGuest"] = true // the inviter is not a resident: no bonus promised
		}
		if n, _ := lead["name"].(string); n != "" {
			name = n
		}
		if member && lead["refNotified"] == nil {
			lead["refNotified"] = f.now().UTC().Format(time.RFC3339)
			notify = true
		}
		return true
	})
	if notify {
		if name == "" {
			name = "Новый человек"
		}
		text := "🔥 " + name + " перешёл по твоей ссылке и запустил бота. Если станет резидентом, твой бонус 100 000 ₸."
		if err := f.send(ctx, inviter, text, nil); err != nil {
			log.Printf("funnel: ref note %d: %v", inviter, err)
		}
	}
	return true
}

// ── GET /api/v1/app/referral ──

type refInvited struct {
	Name  string `json:"name"`
	At    string `json:"at"`
	Stage string `json:"stage"`
	Bonus int    `json:"bonus"`
	Paid  bool   `json:"paid"`
}

func refStage(col string) string {
	switch col {
	case "meet", "diag":
		return "razbor"
	case "won":
		return "resident"
	case "lost":
		return "lost"
	}
	return "new"
}

// invitedBy lists the CRM leads one person invited.
func invitedBy(crm map[string]any, inviter int64) (out []refInvited, total, paid int) {
	out = []refInvited{}
	leads, _ := crm["leads"].([]any)
	for _, l := range leads {
		m, _ := l.(map[string]any)
		if m == nil || m["ref"] == nil || anyInt(m["ref"]) != inviter {
			continue
		}
		at, _ := m["startAt"].(string)
		if at == "" {
			at, _ = m["date"].(string)
		}
		name, _ := m["name"].(string)
		it := refInvited{Name: name, At: at, Stage: refStage(fmt.Sprint(m["col"]))}
		if it.Stage == "resident" {
			it.Bonus = RefBonus
			it.Paid = m["refPaid"] == true
			total += RefBonus
			if it.Paid {
				paid += RefBonus
			}
		}
		out = append(out, it)
	}
	return
}

// Referral godoc
// @Summary  The resident's referral link, ready text and invited people
// @Description  Query _tg = initData. Residents and the team only (403 residents_only). Answer {link, text, invited:[{name,at,stage:new|razbor|resident|lost,bonus,paid}], bonusTotal, bonusPaid}.
// @Tags     app
// @Router   /api/v1/app/referral [get]
func (g *AppGateway) Referral(c *gin.Context) {
	u, ok := g.identify(c, c.Query("_tg"))
	if !ok {
		return
	}
	_, member := g.Admins[u.ID]
	if !member && g.Boards != nil {
		if n, active, err := g.Boards.ResidentByTg(c.Request.Context(), u.ID); err == nil && n != "" && active {
			member = true
		}
	}
	if !member {
		c.JSON(http.StatusForbidden, gin.H{"error": "residents_only"})
		return
	}
	link := RefLink(u.ID)
	out := gin.H{"link": link, "text": refText(link), "invited": []refInvited{}, "bonusTotal": 0, "bonusPaid": 0}
	if g.Funnel != nil {
		if d, err := g.Funnel.docs.GetDoc(c.Request.Context(), "club", "bs_crm"); err == nil && d != nil && !d.Deleted {
			var crm map[string]any
			_ = json.Unmarshal([]byte(d.Value), &crm)
			inv, total, paid := invitedBy(crm, u.ID)
			out["invited"], out["bonusTotal"], out["bonusPaid"] = inv, total, paid
		}
	}
	c.JSON(http.StatusOK, out)
}

// ── Реферал стал резидентом ──

// RefWonOnce congratulates the inviters of referred leads that reached «won»
// since the last check (each lead once). Returns how many were announced.
func (f *LeadFunnel) RefWonOnce(ctx context.Context) int {
	d, err := f.docs.GetDoc(ctx, "club", "bs_crm")
	if err != nil || d == nil || d.Deleted {
		return 0
	}
	var crm map[string]any
	if json.Unmarshal([]byte(d.Value), &crm) != nil {
		return 0
	}
	type win struct {
		key, name, refName string
		inviter            int64
		guest              bool
	}
	var wins []win
	leads, _ := crm["leads"].([]any)
	for _, l := range leads {
		m, _ := l.(map[string]any)
		if m == nil || m["ref"] == nil || fmt.Sprint(m["col"]) != "won" {
			continue
		}
		inv := anyInt(m["ref"])
		if inv == 0 {
			continue
		}
		key, _ := m["id"].(string)
		if key == "" {
			key = fmt.Sprintf("tg%d", leadTg(m))
		}
		name, _ := m["name"].(string)
		rn, _ := m["refName"].(string)
		wins = append(wins, win{key, name, rn, inv, m["refGuest"] == true})
	}
	if len(wins) == 0 {
		return 0
	}
	var fresh []win
	now := f.now().UTC().Format(time.RFC3339)
	err = f.mutateIn(ctx, "server", refWonDoc, "server:referral", func(doc map[string]any) bool {
		fresh = nil
		seen, _ := doc["notified"].(map[string]any)
		if seen == nil {
			seen = map[string]any{}
		}
		for _, w := range wins {
			if seen[w.key] != nil {
				continue
			}
			seen[w.key] = now
			fresh = append(fresh, w)
		}
		doc["notified"] = seen
		return len(fresh) > 0
	})
	if err != nil {
		log.Printf("referral: state: %v", err)
		return 0
	}
	for _, w := range fresh {
		if !w.guest {
			text := "Поздравляем: " + w.name + " стал резидентом BS. Твой бонус 100 000 ₸, команда свяжется по выплате."
			if err := f.send(ctx, w.inviter, text, nil); err != nil {
				log.Printf("referral: won note %d: %v", w.inviter, err)
			}
		}
		who := w.refName
		if who == "" {
			who = "id " + strconv.FormatInt(w.inviter, 10)
		}
		note := fmt.Sprintf("🎉 Реферал стал резидентом: %s.\nПригласил: %s (🆔 %d).\nБонус 100 000 ₸ к выплате, отметьте выплату в карточке лида (refPaid).", w.name, who, w.inviter)
		if w.guest {
			note = fmt.Sprintf("🎉 %s стал резидентом. Пришёл по ссылке %s (🆔 %d), это не резидент: бонус не обещан.", w.name, who, w.inviter)
		}
		for _, a := range f.admins {
			_ = f.send(ctx, a, note, nil)
		}
	}
	return len(fresh)
}

// RefWonLoop checks every 10 minutes.
func (f *LeadFunnel) RefWonLoop(ctx context.Context) {
	t := time.NewTicker(refWonEvery)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		c, cancel := context.WithTimeout(ctx, 2*time.Minute)
		if n := f.RefWonOnce(c); n > 0 {
			log.Printf("referral: %d became residents", n)
		}
		cancel()
	}
}

// mutateIn: like mutate, for any scope, with more tries (bookings race).
func (f *LeadFunnel) mutateIn(ctx context.Context, scope, key, by string, fn func(doc map[string]any) bool) error {
	for try := 0; try < 12; try++ {
		doc := map[string]any{}
		base := 0
		d, err := f.docs.GetDoc(ctx, scope, key)
		if err != nil {
			return err
		}
		if d != nil {
			base = d.Version
			if !d.Deleted {
				_ = json.Unmarshal([]byte(d.Value), &doc)
			}
			if doc == nil {
				doc = map[string]any{}
			}
		}
		if !fn(doc) {
			return nil
		}
		val, _ := json.Marshal(doc)
		_, err = f.docs.PutDoc(ctx, scope, key, base, string(val), false, by)
		if err == nil {
			return nil
		}
		// a version conflict, or two first writes of a new doc at once: read again
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(time.Duration(5+try*7) * time.Millisecond):
		}
	}
	return pg.ErrPlatformConflict
}
