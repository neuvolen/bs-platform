package http

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/bnursik/business_surgery_backend/web"
	"html"
	"io"
	"log"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/bnursik/business_surgery_backend/internal/bot"
	"github.com/bnursik/business_surgery_backend/internal/club"
	pg "github.com/bnursik/business_surgery_backend/internal/repository/pg"
	"github.com/gin-gonic/gin"
)

// R38c: WhatsApp for residents who do not use Telegram.
//
// A resident's channel (Telegram by default, or WhatsApp with a phone) lives
// in resident_channels. Every personal message the system sends a resident
// (meeting, report and fine reminders, notices, the call summary, broadcasts,
// event invitations) goes through bot.Service.SendResident: for a WhatsApp
// resident it lands here. With Green-API connected (GREEN_API_ID and
// GREEN_API_TOKEN, the CRM's WhatsApp card) it is sent at once; otherwise it
// waits in «WhatsApp: к отправке»: the owner gets one Telegram note with a
// button per message, one click opens wa.me with the exact text and marks it
// sent. Nothing is queued twice (wa_outbox.dedup).
//
// Team only:
//
//	GET  /outreach/wa                 the queue, the pending count (the header badge)
//	POST /outreach/wa/:id/sent|skip|back
//	GET  /outreach/channels           residents with their channel, the setup hint
//	PUT  /outreach/channels {name, channel, phone}
//
// Public, signed: GET /api/v1/wa/open/:id/:sig → wa.me (the Telegram button).

// Outreach: events with an RSVP, broadcasts, WhatsApp (outreach_events.go too).
type Outreach struct {
	repo    *pg.OutreachRepo
	docs    funnelDocs // bs_crm
	club    residentLoader
	secret  []byte
	admins  []int64
	owner   int64
	now     func() time.Time
	httpc   *http.Client
	green   func() *greenAPI
	tgBase  string // the platform's address for the buttons
	pubBase func() string

	// Send, Contact: the bot (nil: no bot, nothing goes out).
	Send    func(ctx context.Context, chat int64, text string, kb map[string]any) error
	Contact func(ctx context.Context, chat int64, text string) error

	// Pace: the pause between two messages of a broadcast (Telegram allows
	// about 30 a second; 70 ms keeps well under it).
	Pace time.Duration

	mu        sync.Mutex
	chAt      time.Time
	ch        map[string]pg.ResidentChannel
	lastNote  time.Time
	running   map[string]bool
	tested    map[string]time.Time
	seedHint  string
	kick      chan struct{}
	notifyNow chan struct{}
}

// NewOutreach: repo for the tables, docs for bs_crm, residents for the club.
// SetClock: the clock events and broadcasts run on (a stand pins a day).
func (o *Outreach) SetClock(now func() time.Time) {
	if now != nil {
		o.now = now
	}
}

func NewOutreach(repo *pg.OutreachRepo, docs funnelDocs, residents residentLoader, secret []byte, admins []int64, owner int64) *Outreach {
	return &Outreach{repo: repo, docs: docs, club: residents, secret: secret, admins: admins, owner: owner, now: time.Now,
		httpc: &http.Client{Timeout: 20 * time.Second}, green: greenFromEnv, tgBase: ContentPlatformURL(), pubBase: publicBase,
		Pace: 70 * time.Millisecond, running: map[string]bool{}, tested: map[string]time.Time{},
		kick: make(chan struct{}, 1), notifyNow: make(chan struct{}, 1)}
}

func waDigits(s string) string {
	d := phoneDigits(s)
	if len(d) == 11 && d[0] == '8' {
		d = "7" + d[1:]
	}
	if len(d) == 10 && d[0] == '7' {
		d = "7" + d
	}
	return d
}

// WALink: wa.me with the exact text (spaces as %20, WhatsApp reads both).
func WALink(phone, text string) string {
	return "https://wa.me/" + waDigits(phone) + "?text=" + strings.ReplaceAll(url.QueryEscape(text), "+", "%20")
}

func prettyPhone(d string) string {
	d = waDigits(d)
	if len(d) == 11 && d[0] == '7' {
		return "+7 " + d[1:4] + " " + d[4:7] + " " + d[7:9] + " " + d[9:11]
	}
	return "+" + d
}

func (o *Outreach) channels(ctx context.Context) map[string]pg.ResidentChannel {
	o.mu.Lock()
	if o.ch != nil && o.now().Sub(o.chAt) < 30*time.Second {
		m := o.ch
		o.mu.Unlock()
		return m
	}
	o.mu.Unlock()
	m, err := o.repo.Channels(ctx)
	if err != nil {
		log.Printf("outreach: channels: %v", err)
		o.mu.Lock()
		defer o.mu.Unlock()
		return o.ch
	}
	o.mu.Lock()
	o.ch, o.chAt = m, o.now()
	o.mu.Unlock()
	return m
}

func (o *Outreach) dropChannels() {
	o.mu.Lock()
	o.ch = nil
	o.mu.Unlock()
}

// Route: the WhatsApp phone of a resident who chose WhatsApp ("" otherwise).
func (o *Outreach) Route(ctx context.Context, name string) string {
	c, ok := o.channels(ctx)[club.NormName(name)]
	if !ok || c.Channel != "wa" || len(waDigits(c.Phone)) < 11 {
		return ""
	}
	return waDigits(c.Phone)
}

func dedupKey(k string) string {
	s := sha256.Sum256([]byte(k))
	return hex.EncodeToString(s[:16])
}

// DeliverWA: Green-API at once when connected, else the queue (and one note
// to the owner, see notifyLoop). The same Key is never delivered twice.
func (o *Outreach) DeliverWA(ctx context.Context, m bot.WAMessage) error {
	phone := waDigits(m.Phone)
	if len(phone) < 11 {
		return errors.New("WhatsApp: нет номера телефона")
	}
	if strings.TrimSpace(m.Text) == "" {
		return nil
	}
	id, fresh, err := o.repo.WAAdd(ctx, pg.WAItem{Dedup: dedupKey(m.Key), Resident: m.Resident, Phone: phone, Kind: m.Kind, Text: m.Text, ExpiresAt: m.Expires})
	if err != nil || !fresh {
		return err
	}
	if g := o.green(); g != nil {
		if err := o.greenSend(ctx, g, phone, m.Text); err == nil {
			_, _ = o.repo.WASet(ctx, id, []string{"pending"}, "auto", "green-api", "")
			return nil
		} else {
			_ = o.repo.WASetError(ctx, id, "Green-API: "+err.Error())
			log.Printf("outreach: green-api %d: %v", id, err)
		}
	}
	select {
	case o.notifyNow <- struct{}{}:
	default:
	}
	return nil
}

func (o *Outreach) greenSend(ctx context.Context, g *greenAPI, phone, text string) error {
	b, _ := json.Marshal(map[string]any{"chatId": phone + "@c.us", "message": text})
	req, err := http.NewRequestWithContext(ctx, "POST", g.url("sendMessage"), bytes.NewReader(b))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	res, err := o.httpc.Do(req)
	if err != nil {
		return errors.New("нет связи")
	}
	defer res.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(res.Body, 1<<16))
	if res.StatusCode >= 300 {
		return fmt.Errorf("ответ %d", res.StatusCode)
	}
	var out struct {
		ID string `json:"idMessage"`
	}
	if json.Unmarshal(body, &out) != nil || out.ID == "" {
		return errors.New("нет id сообщения")
	}
	return nil
}

// HandleResidentWA: PlatformAI.WAResident (the call summary).
func (o *Outreach) HandleResidentWA(ctx context.Context, kind, key, name, text string) (bool, error) {
	phone := o.Route(ctx, name)
	if phone == "" {
		return false, nil
	}
	return true, o.DeliverWA(ctx, bot.WAMessage{Resident: name, Phone: phone, Kind: kind, Key: kind + "|" + club.NormName(name) + "|" + key, Text: text})
}

func (o *Outreach) openSig(id int64) string {
	m := hmac.New(sha256.New, o.secret)
	m.Write([]byte("wa-open|" + strconv.FormatInt(id, 10)))
	return hex.EncodeToString(m.Sum(nil))[:24]
}

// OpenURL: the signed one-click link of a queued message.
func (o *Outreach) OpenURL(id int64) string {
	return o.pubBase() + "/api/v1/wa/open/" + strconv.FormatInt(id, 10) + "/" + o.openSig(id)
}

func almatyHour(t time.Time) (int, int) {
	a := t.In(club.Almaty)
	return a.Hour(), a.Minute()
}

var waKindName = map[string]string{
	"meeting": "напоминание о встрече", "report": "напоминание об отчёте", "fine": "штраф", "notice": "уведомление",
	"summary": "саммари разбора", "broadcast": "рассылка", "event": "мероприятие", "lead": "лид из базы",
}

// notifyWA: one Telegram note to the owner about new queued messages: a
// button per message opens WhatsApp with the text. Not at night (23:00–09:00
// Almaty), not more than once in 10 minutes.
func (o *Outreach) notifyWA(ctx context.Context) {
	if o.Send == nil || o.owner == 0 {
		return
	}
	if h, _ := almatyHour(o.now()); h < 9 || h >= 23 {
		return
	}
	o.mu.Lock()
	recent := o.now().Sub(o.lastNote) < 10*time.Minute
	o.mu.Unlock()
	if recent {
		return
	}
	items, err := o.repo.WAToNotify(ctx)
	if err != nil || len(items) == 0 {
		return
	}
	pending, _ := o.repo.WAPending(ctx)
	var lines []string
	var rows [][]map[string]any
	for i, w := range items {
		who := w.Resident
		if who == "" {
			who = prettyPhone(w.Phone)
		}
		if i < 8 {
			lines = append(lines, "• "+who+": "+firstNonEmptyS(waKindName[w.Kind], w.Kind))
			rows = append(rows, []map[string]any{{"text": "📲 " + firstName(who) + ": открыть WhatsApp", "url": o.OpenURL(w.ID)}})
		}
	}
	if len(items) > 8 {
		lines = append(lines, fmt.Sprintf("и ещё %d", len(items)-8))
	}
	rows = append(rows, []map[string]any{{"text": "Очередь на платформе", "url": o.tgBase + "#waq"}})
	txt := fmt.Sprintf("📲 WhatsApp: к отправке %d\n\n%s\n\nКнопка откроет WhatsApp с готовым текстом: нажмите «Отправить» в WhatsApp. Сообщение отметится отправленным.", pending, strings.Join(lines, "\n"))
	if err := o.Send(ctx, o.owner, txt, map[string]any{"inline_keyboard": rows}); err != nil {
		log.Printf("outreach: wa note: %v", err)
		return
	}
	ids := make([]int64, 0, len(items))
	for _, w := range items {
		ids = append(ids, w.ID)
	}
	_ = o.repo.WAMarkNotified(ctx, ids)
	o.mu.Lock()
	o.lastNote = o.now()
	o.mu.Unlock()
}

func firstNonEmptyS(a ...string) string {
	for _, s := range a {
		if s != "" {
			return s
		}
	}
	return ""
}

// ── Елена: the one-time merge (idempotent) ──

const (
	elenaName  = "Елена"
	elenaPhone = "77071144645"
)

// SeedElena: the resident «Елена» reads WhatsApp at +7 707 114 46 45. Done
// once: a row that exists already (the owner may have changed it) is kept.
// Several or no Елена: nothing is guessed, the platform shows the hint.
func (o *Outreach) SeedElena(ctx context.Context) bool {
	list, err := o.club.LoadResidents(ctx)
	if err != nil || len(list) == 0 {
		return false
	}
	var cands []string
	seen := map[string]bool{}
	for _, r := range list {
		if r.Name == "" || r.Former || r.Archived {
			continue
		}
		for _, w := range strings.Fields(club.NormName(r.Name)) {
			if w == "елена" && !seen[club.NormName(r.Name)] {
				seen[club.NormName(r.Name)] = true
				cands = append(cands, r.Name)
			}
		}
	}
	chs, _ := o.repo.Channels(ctx)
	for _, c := range cands {
		if x, ok := chs[club.NormName(c)]; ok && x.Phone != "" {
			o.setHint("")
			return true // set already (by the merge or by the owner)
		}
	}
	switch len(cands) {
	case 1:
		added, err := o.repo.AddChannelOnce(ctx, pg.ResidentChannel{NameKey: club.NormName(cands[0]), Name: cands[0], Channel: "wa", Phone: elenaPhone, UpdatedBy: "r38c:merge"})
		if err != nil {
			return false
		}
		if added {
			log.Printf("outreach: %s reads WhatsApp %s (one-time merge)", cands[0], prettyPhone(elenaPhone))
		}
		o.dropChannels()
		o.setHint("")
		return true
	case 0:
		o.setHint("Резидент «Елена» не найдена в списке резидентов. Когда она появится, выберите её ниже и включите WhatsApp: " + prettyPhone(elenaPhone))
	default:
		sort.Strings(cands)
		o.setHint("Резидентов с именем Елена несколько (" + strings.Join(cands, ", ") + "): выберите нужную ниже и включите WhatsApp, номер " + prettyPhone(elenaPhone))
	}
	return false
}

func (o *Outreach) setHint(s string) {
	o.mu.Lock()
	o.seedHint = s
	o.mu.Unlock()
}

// ── HTTP ──

func (o *Outreach) waView(w pg.WAItem) gin.H {
	v := gin.H{"id": w.ID, "resident": w.Resident, "phone": w.Phone, "phonePretty": prettyPhone(w.Phone), "kind": w.Kind,
		"kindName": firstNonEmptyS(waKindName[w.Kind], w.Kind), "text": w.Text, "status": w.Status, "createdAt": w.CreatedAt,
		"link": WALink(w.Phone, w.Text)}
	if w.Error != "" {
		v["error"] = w.Error
	}
	if w.SentAt != nil {
		v["sentAt"], v["sentBy"] = w.SentAt, w.SentBy
	}
	return v
}

// WAQueue: GET /outreach/wa
func (o *Outreach) WAQueue(c *gin.Context) {
	if !teamOnly(c) {
		return
	}
	ctx := c.Request.Context()
	_, _ = o.repo.WAToNotify(ctx) // closes the expired ones
	list, err := o.repo.WAList(ctx, 30)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "load_failed"})
		return
	}
	items := []gin.H{}
	pending := 0
	for _, w := range list {
		if w.Status == "pending" {
			pending++
		}
		items = append(items, o.waView(w))
	}
	c.Header("Cache-Control", "no-store")
	c.JSON(http.StatusOK, gin.H{"items": items, "pending": pending, "green": o.green() != nil})
}

// WAMark: POST /outreach/wa/:id/sent|skip|back
func (o *Outreach) WAMark(c *gin.Context) {
	if !teamOnly(c) {
		return
	}
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "bad_id"})
		return
	}
	var from []string
	to := ""
	switch c.Param("op") {
	case "sent":
		from, to = []string{"pending"}, "sent"
	case "skip":
		from, to = []string{"pending"}, "skipped"
	case "back":
		from, to = []string{"sent", "skipped", "expired"}, "pending"
	default:
		c.JSON(http.StatusBadRequest, gin.H{"error": "bad_op"})
		return
	}
	ok, err := o.repo.WASet(c.Request.Context(), id, from, to, platformUser(c), "")
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "store_failed"})
		return
	}
	w, _ := o.repo.WAGet(c.Request.Context(), id)
	n, _ := o.repo.WAPending(c.Request.Context())
	out := gin.H{"ok": ok, "pending": n}
	if w != nil {
		out["item"] = o.waView(*w)
	}
	c.JSON(http.StatusOK, out)
}

// WAOpen: GET /api/v1/wa/open/:id/:sig: the Telegram button: marks the
// message sent and opens WhatsApp with its text.
func (o *Outreach) WAOpen(c *gin.Context) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || !hmac.Equal([]byte(c.Param("sig")), []byte(o.openSig(id))) {
		c.String(http.StatusNotFound, "not found")
		return
	}
	w, err := o.repo.WAGet(c.Request.Context(), id)
	if err != nil || w == nil {
		c.String(http.StatusNotFound, "not found")
		return
	}
	if w.Status == "skipped" || w.Status == "expired" {
		c.Header("Content-Type", "text/html; charset=utf-8")
		c.String(http.StatusOK, `<!doctype html><meta charset="utf-8"><meta name="viewport" content="width=device-width">`+web.IconLinks+`<body style="font:16px system-ui;padding:24px">Это сообщение уже не нужно отправлять (`+html.EscapeString(map[string]string{"skipped": "пропущено", "expired": "устарело"}[w.Status])+`).</body>`)
		return
	}
	if w.Status == "pending" {
		_, _ = o.repo.WASet(c.Request.Context(), id, []string{"pending"}, "sent", "telegram", "")
	}
	c.Header("Cache-Control", "no-store")
	c.Redirect(http.StatusFound, WALink(w.Phone, w.Text))
}

// Channels: GET /outreach/channels
func (o *Outreach) Channels(c *gin.Context) {
	if !teamOnly(c) {
		return
	}
	ctx := c.Request.Context()
	list, err := o.club.LoadResidents(ctx)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "load_failed"})
		return
	}
	o.dropChannels()
	chs := o.channels(ctx)
	out := []gin.H{}
	seen := map[string]bool{}
	for _, r := range list {
		k := club.NormName(r.Name)
		if r.Name == "" || r.Former || r.Archived || seen[k] {
			continue
		}
		seen[k] = true
		ch, phone := "tg", ""
		if x, ok := chs[k]; ok {
			ch, phone = x.Channel, x.Phone
		}
		out = append(out, gin.H{"name": r.Name, "tg": r.TgID != 0, "channel": ch, "phone": phone, "phonePretty": map[bool]string{true: prettyPhone(phone), false: ""}[phone != ""]})
	}
	sort.Slice(out, func(i, j int) bool { return out[i]["name"].(string) < out[j]["name"].(string) })
	o.mu.Lock()
	hint := o.seedHint
	o.mu.Unlock()
	c.Header("Cache-Control", "no-store")
	c.JSON(http.StatusOK, gin.H{"residents": out, "hint": hint, "green": o.green() != nil})
}

// PutChannel: PUT /outreach/channels {name, channel, phone}
func (o *Outreach) PutChannel(c *gin.Context) {
	if !adminOnly(c) {
		return
	}
	var in struct {
		Name    string `json:"name"`
		Channel string `json:"channel"`
		Phone   string `json:"phone"`
	}
	if err := c.ShouldBindJSON(&in); err != nil || strings.TrimSpace(in.Name) == "" || (in.Channel != "tg" && in.Channel != "wa") {
		c.JSON(http.StatusBadRequest, gin.H{"error": "bad_channel"})
		return
	}
	phone := waDigits(in.Phone)
	if in.Channel == "wa" && len(phone) < 11 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "bad_phone", "message": "Укажите номер WhatsApp полностью, например +7 707 114 46 45"})
		return
	}
	if err := o.repo.PutChannel(c.Request.Context(), pg.ResidentChannel{NameKey: club.NormName(in.Name), Name: strings.TrimSpace(in.Name), Channel: in.Channel, Phone: phone, UpdatedBy: platformUser(c)}); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "store_failed"})
		return
	}
	o.dropChannels()
	log.Printf("outreach: %s reads %s (%s)", in.Name, in.Channel, platformUser(c))
	c.JSON(http.StatusOK, gin.H{"ok": true, "name": in.Name, "channel": in.Channel, "phone": phone})
}
