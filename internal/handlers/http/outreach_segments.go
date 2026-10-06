package http

import (
	"context"
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	pg "github.com/bnursik/business_surgery_backend/internal/repository/pg"
	"github.com/gin-gonic/gin"
)

// R47: «Что делать с базой»: действие для сегмента в один клик, без автоотправки.
//
//	POST /outreach/segments/act {action, ids, text, title, limit}
//
//   - wa: лиды с телефоном встают в очередь «WhatsApp: к отправке» пачкой
//     (по умолчанию 50, не больше 200): у каждого готовый текст ({имя}
//     подставляется), один клик открывает wa.me. Green-API не используется,
//     уведомления в Telegram нет: очередь видна на платформе. Тот же текст
//     тому же номеру второй раз не встаёт.
//   - tg: черновик рассылки (bc_campaigns, id seg…) ровно этим лидам с
//     Telegram id; отправляет владелец кнопкой после теста, как R38c.
//   - breakfast: такой же черновик с кнопками «Иду» ближайшего мероприятия
//     клуба (бизнес-завтрак).

const (
	segCampaignPrefix = "seg"
	segWADefault      = 50
	segWAMax          = 200
	segWALife         = 14 * 24 * time.Hour
)

// segLeads: the CRM leads with these ids (the order of ids).
func (o *Outreach) segLeads(ctx context.Context, ids []string) []map[string]any {
	if o.docs == nil || len(ids) == 0 {
		return nil
	}
	d, err := o.docs.GetDoc(ctx, "club", "bs_crm")
	if err != nil || d == nil || d.Deleted {
		return nil
	}
	var crm map[string]any
	_ = json.Unmarshal([]byte(d.Value), &crm)
	byID := map[string]map[string]any{}
	for _, x := range asList(crm["leads"]) {
		if m, _ := x.(map[string]any); m != nil {
			byID[pStr(m, "id")] = m
		}
	}
	var out []map[string]any
	seen := map[string]bool{}
	for _, id := range ids {
		if m := byID[id]; m != nil && !seen[id] {
			seen[id] = true
			out = append(out, m)
		}
	}
	return out
}

func segPersonal(text, name string) string {
	first := strings.TrimSpace(name)
	if f := strings.Fields(first); len(f) > 0 && !strings.HasPrefix(f[0], "+") && !strings.HasPrefix(f[0], "@") && phoneDigits(f[0]) == "" {
		first = f[0]
	} else {
		first = ""
	}
	t := strings.ReplaceAll(text, "{имя}", first)
	t = strings.ReplaceAll(t, "Здравствуйте, !", "Здравствуйте!")
	t = strings.ReplaceAll(t, "Привет, !", "Привет!")
	t = strings.ReplaceAll(t, " ,", ",")
	return strings.TrimSpace(t)
}

// SegmentWA queues WhatsApp messages for the leads (never sent by itself).
func (o *Outreach) SegmentWA(ctx context.Context, leads []map[string]any, text string, limit int) (gin.H, error) {
	if limit <= 0 {
		limit = segWADefault
	}
	if limit > segWAMax {
		limit = segWAMax
	}
	sum := sha1.Sum([]byte(strings.TrimSpace(text)))
	tag := hex.EncodeToString(sum[:4])
	until := o.now().Add(segWALife)
	queued, already, noPhone, left := 0, 0, 0, 0
	var ids []int64
	for _, l := range leads {
		phone := waDigits(pStr(l, "phone"))
		if len(phone) < 11 {
			noPhone++
			continue
		}
		if queued >= limit {
			left++
			continue
		}
		name := pStr(l, "name")
		id, fresh, err := o.repo.WAAdd(ctx, pg.WAItem{Dedup: dedupKey("seg|" + phone + "|" + tag), Resident: name, Phone: phone, Kind: "lead",
			Text: segPersonal(text, name), ExpiresAt: &until})
		if err != nil {
			return nil, err
		}
		if !fresh {
			already++
			continue
		}
		queued++
		ids = append(ids, id)
	}
	if len(ids) > 0 { // the queue is on the platform: no Telegram note for a batch of the base
		_ = o.repo.WAMarkNotified(ctx, ids)
	}
	return gin.H{"queued": queued, "already": already, "noPhone": noPhone, "left": left}, nil
}

// segEvent: the nearest open event ahead (the business breakfast).
func (o *Outreach) segEvent(ctx context.Context) *pg.ClubEvent {
	list, err := o.repo.Events(ctx)
	if err != nil {
		return nil
	}
	var best *pg.ClubEvent
	for i := range list {
		e := list[i]
		if e.Status != "open" || !o.now().Before(e.StartsAt) {
			continue
		}
		if best == nil || e.StartsAt.Before(best.StartsAt) {
			best = &e
		}
	}
	return best
}

// SegmentCampaign: a draft broadcast to exactly these leads (Telegram only).
func (o *Outreach) SegmentCampaign(ctx context.Context, leads []map[string]any, title, text string, withEvent bool) (gin.H, error) {
	var ids []string
	noTg := 0
	for _, l := range leads {
		if leadTg(l) > 0 {
			ids = append(ids, pStr(l, "id"))
		} else {
			noTg++
		}
	}
	if len(ids) == 0 {
		return gin.H{"error": "empty", "message": "В сегменте нет лидов, которые писали боту: Telegram-рассылка им не дойдёт", "noTelegram": noTg}, nil
	}
	c := pg.Campaign{ID: fmt.Sprintf("%s%s", segCampaignPrefix, o.now().In(almaty).Format("060102150405")), Title: title, Text: text}
	if withEvent {
		e := o.segEvent(ctx)
		if e == nil {
			return gin.H{"error": "no_event", "message": "Нет открытого мероприятия впереди: создайте его в «Мероприятиях»"}, nil
		}
		c.EventID = e.ID
		if strings.TrimSpace(c.Text) == "" {
			c.Text = e.About
		}
		if strings.TrimSpace(c.Title) == "" {
			c.Title = "Приглашение: " + e.Title
		}
	}
	if strings.TrimSpace(c.Text) == "" || len([]rune(c.Text)) > 3800 {
		return gin.H{"error": "bad_text", "message": "Текст рассылки: от 1 до 3 800 символов"}, nil
	}
	if strings.TrimSpace(c.Title) == "" {
		c.Title = "Рассылка сегменту"
	}
	aud, _ := json.Marshal(bcAudience{Leads: ids})
	c.Audience = aud
	for try := 0; try < 3; try++ {
		ok, err := o.repo.CampaignAddOnce(ctx, c)
		if err != nil {
			return nil, err
		}
		if ok {
			break
		}
		c.ID += "x"
	}
	x, err := o.repo.CampaignGet(ctx, c.ID)
	if err != nil || x == nil {
		return nil, fmt.Errorf("campaign not stored")
	}
	v := o.campaignView(ctx, *x, true)
	v["noTelegram"] = noTg
	return v, nil
}

// SegmentAct: POST /outreach/segments/act.
func (o *Outreach) SegmentAct(c *gin.Context) {
	if !adminOnly(c) {
		return
	}
	var in struct {
		Action string   `json:"action"`
		IDs    []string `json:"ids"`
		Text   string   `json:"text"`
		Title  string   `json:"title"`
		Limit  int      `json:"limit"`
	}
	if err := c.ShouldBindJSON(&in); err != nil || len(in.IDs) == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "empty", "message": "Сегмент пустой"})
		return
	}
	ctx := c.Request.Context()
	leads := o.segLeads(ctx, in.IDs)
	if len(leads) == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "empty", "message": "Лиды сегмента не найдены в CRM"})
		return
	}
	var out gin.H
	var err error
	switch in.Action {
	case "wa":
		if strings.TrimSpace(in.Text) == "" {
			c.JSON(http.StatusBadRequest, gin.H{"error": "bad_text", "message": "Напишите текст сообщения"})
			return
		}
		out, err = o.SegmentWA(ctx, leads, in.Text, in.Limit)
	case "tg":
		out, err = o.SegmentCampaign(ctx, leads, in.Title, in.Text, false)
	case "breakfast":
		out, err = o.SegmentCampaign(ctx, leads, in.Title, in.Text, true)
	default:
		c.JSON(http.StatusBadRequest, gin.H{"error": "bad_action"})
		return
	}
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "store_failed", "message": "Не сохранилось, попробуйте ещё раз"})
		return
	}
	if out["error"] != nil {
		c.JSON(http.StatusBadRequest, out)
		return
	}
	c.JSON(http.StatusOK, out)
}
