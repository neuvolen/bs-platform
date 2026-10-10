package http

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/bnursik/business_surgery_backend/internal/bot"
	"github.com/bnursik/business_surgery_backend/internal/club"
	"github.com/gin-gonic/gin"
	"github.com/golang-jwt/jwt/v5"
)

// R83: «После двух дней с момента офлайн-разбора нужно, чтобы бот отправил
// задачу, что нужно выбрать и утвердить новое время онлайн и офлайн».
//
// 48 hours after an offline day the owner gets a task in the bot:
// «Утвердите время следующих встреч (онлайн и офлайн)» with the proposals of
// the next 10-day cycle: the offline group's next day (the same time, 10
// days later) and every online resident's next meeting (10 days after his
// last one, at his usual time), each on a free hour of the club's schedule.
// «✅ Утвердить» creates the meetings (the club's schedule, the Google
// Calendar event with the platform's call link, the residents' reminders as
// for any meeting); «✏️ Другое время» opens the planner page where each date
// and time can be changed; «Не нужно» closes the task. Not answered: the
// task comes again every 24 hours (at most 4 times).
//
// Storage: server/bs_cycleplan.

const (
	cyDocKey     = "bs_cycleplan"
	cyAfter      = 48 * time.Hour
	cyWindow     = 7 * 24 * time.Hour // an older offline day is not asked about
	cyCycle      = 10                 // days
	cyRepeat     = 24 * time.Hour
	cyMaxSends   = 4
	cyDayFrom    = 10 // a proposal is between 10:00
	cyDayTo      = 19 // and 19:00
	CyclePrefix  = "cy:"
	cyOfflineTag = "offline"
	cyOnlineTag  = "online"
)

type CyItem struct {
	Res  string `json:"res"`
	Kind string `json:"kind"` // offline | online
	Date string `json:"date"` // YYYY-MM-DD
	Time string `json:"time"` // HH:MM
	Done bool   `json:"done,omitempty"`
	Err  string `json:"err,omitempty"`
}

type CyTask struct {
	ID       string   `json:"id"`
	OffDate  string   `json:"offDate"` // the offline day it follows
	Group    []string `json:"group"`
	Items    []CyItem `json:"items"`
	Status   string   `json:"status"` // pending | approved | dismissed
	Created  string   `json:"created"`
	SentAt   string   `json:"sentAt,omitempty"`
	Sends    int      `json:"sends,omitempty"`
	MsgID    int64    `json:"msgId,omitempty"`
	DoneAt   string   `json:"doneAt,omitempty"`
	DoneBy   string   `json:"doneBy,omitempty"`
	Approved []CyItem `json:"approved,omitempty"`
}

type cyDoc struct {
	Tasks []CyTask `json:"tasks"`
}

func (d *cyDoc) byID(id string) *CyTask {
	for i := range d.Tasks {
		if d.Tasks[i].ID == id {
			return &d.Tasks[i]
		}
	}
	return nil
}

// CyclePlanner: the task, its proposals, the approval.
type CyclePlanner struct {
	Docs  funnelDocs
	Load  func(ctx context.Context) (*club.Snapshot, error)
	Write func(ctx context.Context, action string, p map[string]string) error
	Send  func(ctx context.Context, chat int64, text string, kb map[string]any) (int64, error)
	Edit  func(ctx context.Context, chat, msgID int64, text string, kb map[string]any) error
	Owner int64
	Team  map[int64]string
	BotToken string // the planner page: Telegram initData
	JWT   []byte               // the planner page: the platform's session cookie
	Now   func() time.Time
}

func (p *CyclePlanner) now() time.Time {
	if p.Now != nil {
		return p.Now()
	}
	return time.Now()
}

func cyStart(m club.Meeting) time.Time {
	d := m.Date.In(club.Almaty)
	h, mi := 12, 0
	if c := club.ClockTime(m.Time); c != "" {
		h, _ = strconv.Atoi(c[:2])
		mi, _ = strconv.Atoi(c[3:5])
	}
	return time.Date(d.Year(), d.Month(), d.Day(), h, mi, 0, 0, club.Almaty)
}

func cyOffline(m club.Meeting) bool { return !(m.Online || strings.Contains(m.Link, "http")) }

func cyHHMM(t time.Time) string { return t.Format("15:04") }

func cyActive(r club.Resident) bool {
	return r.Name != "" && !r.Former && !r.Archived && !r.Admin && !strings.HasPrefix(strings.TrimSpace(r.Name), "🎬")
}

// noSunday: the club does not meet on Sunday.
func noSunday(d time.Time) time.Time {
	if d.Weekday() == time.Sunday {
		return d.AddDate(0, 0, 1)
	}
	return d
}

// Propose: the next cycle after the offline day off (YYYY-MM-DD).
func Propose(snap *club.Snapshot, off string, now time.Time) (group []string, items []CyItem) {
	offDay, err := time.ParseInLocation("2006-01-02", off, club.Almaty)
	if err != nil || snap == nil {
		return nil, nil
	}
	a := now.In(club.Almaty)
	tomorrow := time.Date(a.Year(), a.Month(), a.Day(), 0, 0, 0, 0, club.Almaty).AddDate(0, 0, 1)
	busy := map[string]bool{} // YYYY-MM-DD HH:MM taken in the schedule
	last := map[string]club.Meeting{}
	future := map[string]bool{} // a resident with a meeting after the offline day already
	offTime := ""
	for _, m := range snap.Meetings {
		n := club.NormName(m.Resident)
		if n == "" {
			continue
		}
		st := cyStart(m)
		busy[st.Format("2006-01-02 15:04")] = true
		day := st.Format("2006-01-02")
		if day == off && cyOffline(m) && !strings.HasPrefix(strings.TrimSpace(m.Resident), "🎬") {
			group = append(group, strings.TrimSpace(m.Resident))
			if offTime == "" || cyHHMM(st) < offTime {
				offTime = cyHHMM(st)
			}
		}
		if day > off {
			future[n] = true
		} else if l, ok := last[n]; !ok || st.After(cyStart(l)) {
			last[n] = m
		}
	}
	if len(group) == 0 {
		return nil, nil
	}
	sort.Strings(group)
	if offTime == "" {
		offTime = "11:00"
	}
	active := map[string]bool{}
	known := map[string]bool{}
	var online []club.Resident
	inGroup := map[string]bool{}
	for _, g := range group {
		inGroup[club.NormName(g)] = true
	}
	for _, r := range snap.Residents {
		known[club.NormName(r.Name)] = true
		if !cyActive(r) {
			continue
		}
		active[club.NormName(r.Name)] = true
		if strings.EqualFold(strings.TrimSpace(r.Format), "Онлайн") && !inGroup[club.NormName(r.Name)] {
			online = append(online, r)
		}
	}
	// the offline group: the same time, a cycle later
	nextOff := noSunday(offDay.AddDate(0, 0, cyCycle))
	if nextOff.Before(tomorrow) {
		nextOff = noSunday(tomorrow)
	}
	offKey := nextOff.Format("2006-01-02")
	for _, g := range group {
		n := club.NormName(g)
		if future[n] || (known[n] && !active[n]) {
			continue // the next meeting is set already, or no longer a resident
		}
		items = append(items, CyItem{Res: g, Kind: cyOfflineTag, Date: offKey, Time: offTime})
	}
	busy[offKey+" "+offTime] = true
	// online residents: 10 days after the last meeting, at the usual time, on a free hour
	sort.Slice(online, func(i, j int) bool { return online[i].Name < online[j].Name })
	for _, r := range online {
		n := club.NormName(r.Name)
		if future[n] {
			continue
		}
		base := offDay.AddDate(0, 0, cyCycle)
		tm := "11:00"
		if l, ok := last[n]; ok {
			st := cyStart(l)
			base = time.Date(st.Year(), st.Month(), st.Day(), 0, 0, 0, 0, club.Almaty).AddDate(0, 0, cyCycle)
			if c := club.ClockTime(l.Time); c != "" {
				tm = c
			}
		}
		if base.Before(tomorrow) {
			base = tomorrow
		}
		d, t := cyFree(busy, noSunday(base), tm)
		busy[d.Format("2006-01-02")+" "+t] = true
		items = append(items, CyItem{Res: strings.TrimSpace(r.Name), Kind: cyOnlineTag, Date: d.Format("2006-01-02"), Time: t})
	}
	return group, items
}

// cyFree: the first free hour from (day, tm): later the same day, then the
// next days (never Sunday).
func cyFree(busy map[string]bool, day time.Time, tm string) (time.Time, string) {
	h, _ := strconv.Atoi(strings.SplitN(tm, ":", 2)[0])
	mm := "00"
	if p := strings.SplitN(tm, ":", 2); len(p) == 2 && len(p[1]) == 2 {
		mm = p[1]
	}
	if h < cyDayFrom || h > cyDayTo {
		h = 11
	}
	for i := 0; i < 14; i++ {
		start := h
		if i > 0 {
			start = cyDayFrom
		}
		for x := start; x <= cyDayTo; x++ {
			t := fmt.Sprintf("%02d:%s", x, mm)
			if !busy[day.Format("2006-01-02")+" "+t] {
				return day, t
			}
		}
		day = noSunday(day.AddDate(0, 0, 1))
	}
	return day, tm
}

func cyRuDay(date string) string {
	d, err := time.ParseInLocation("2006-01-02", date, club.Almaty)
	if err != nil {
		return date
	}
	wd := []string{"вс", "пн", "вт", "ср", "чт", "пт", "сб"}[d.Weekday()]
	return wd + ", " + d.Format("02.01")
}

// Text: the task as the owner reads it.
func (t *CyTask) Text() string {
	var b strings.Builder
	off := t.OffDate
	if d, err := time.Parse("2006-01-02", off); err == nil {
		off = d.Format("02.01")
	}
	who := strings.Join(t.Group, ", ")
	if len(t.Group) > 4 {
		who = strings.Join(t.Group[:4], ", ") + fmt.Sprintf(" и ещё %d", len(t.Group)-4)
	}
	fmt.Fprintf(&b, "🗓 Утвердите время следующих встреч (онлайн и офлайн)\n\nОфлайн-разбор %s прошёл (%s). Следующий цикл, свободное время в расписании:\n", off, who)
	var offl, onl []CyItem
	for _, it := range t.Items {
		if it.Kind == cyOfflineTag {
			offl = append(offl, it)
		} else {
			onl = append(onl, it)
		}
	}
	if len(offl) > 0 {
		names := make([]string, 0, len(offl))
		for _, it := range offl {
			names = append(names, it.Res)
		}
		fmt.Fprintf(&b, "\n🏢 Офлайн: %s, %s\n%s\n", cyRuDay(offl[0].Date), offl[0].Time, strings.Join(names, ", "))
	}
	if len(onl) > 0 {
		b.WriteString("\n💻 Онлайн (ссылка на созвон платформы):\n")
		for _, it := range onl {
			fmt.Fprintf(&b, "• %s: %s, %s\n", it.Res, cyRuDay(it.Date), it.Time)
		}
	}
	if t.Sends > 1 {
		b.WriteString("\n⏳ Напоминаю: время ещё не утверждено.")
	}
	return strings.TrimSpace(b.String())
}

func (p *CyclePlanner) pageURL(id string) string {
	b := publicBase()
	if b == "" {
		b = "https://app.bxclub.kz"
	}
	return b + "/cycle/" + id
}

func (p *CyclePlanner) kb(t *CyTask) map[string]any {
	return map[string]any{"inline_keyboard": [][]map[string]any{
		{{"text": "✅ Утвердить", "callback_data": CyclePrefix + "ok:" + t.ID}},
		{{"text": "✏️ Другое время", "web_app": map[string]string{"url": p.pageURL(t.ID)}}},
		{{"text": "Не нужно", "callback_data": CyclePrefix + "no:" + t.ID}},
	}}
}

func (p *CyclePlanner) mutate(ctx context.Context, fn func(d *cyDoc) bool) error {
	return r83Mutate(ctx, p.Docs, "server", cyDocKey, "server:cycle", fn)
}

// Tick: new tasks 48 hours after an offline day, a reminder every 24 hours.
func (p *CyclePlanner) Tick(ctx context.Context) error {
	if p.Load == nil || p.Send == nil || p.Owner == 0 {
		return nil
	}
	snap, err := p.Load(ctx)
	if err != nil {
		return err
	}
	now := p.now()
	days := map[string]time.Time{}
	for _, m := range snap.Meetings {
		if strings.TrimSpace(m.Resident) == "" || !cyOffline(m) {
			continue
		}
		st := cyStart(m)
		k := st.Format("2006-01-02")
		if cur, ok := days[k]; !ok || st.Before(cur) {
			days[k] = st
		}
	}
	type out struct {
		t   CyTask
		new bool
	}
	var send []out
	err = p.mutate(ctx, func(d *cyDoc) bool {
		send = send[:0]
		ch := false
		for k, st := range days {
			since := now.Sub(st)
			if since < cyAfter || since > cyWindow {
				continue
			}
			id := "off" + strings.ReplaceAll(k, "-", "")
			if d.byID(id) != nil {
				continue
			}
			group, items := Propose(snap, k, now)
			t := CyTask{ID: id, OffDate: k, Group: group, Items: items, Created: now.UTC().Format(time.RFC3339), Status: "pending"}
			if len(items) == 0 {
				t.Status, t.DoneAt, t.DoneBy = "dismissed", now.UTC().Format(time.RFC3339), "всё уже в расписании"
			}
			d.Tasks = append(d.Tasks, t)
			ch = true
		}
		for i := range d.Tasks {
			t := &d.Tasks[i]
			if t.Status != "pending" || t.Sends >= cyMaxSends {
				continue
			}
			if t.SentAt != "" && now.Sub(sTimeStr(t.SentAt)) < cyRepeat {
				continue
			}
			t.Sends++
			t.SentAt = now.UTC().Format(time.RFC3339)
			send = append(send, out{*t, t.Sends == 1})
			ch = true
		}
		// keep 60 days
		keep := d.Tasks[:0]
		for _, t := range d.Tasks {
			if now.Sub(sTimeStr(t.Created)) < 60*24*time.Hour {
				keep = append(keep, t)
			}
		}
		if len(keep) != len(d.Tasks) {
			d.Tasks, ch = keep, true
		}
		return ch
	})
	if err != nil {
		return err
	}
	for _, o := range send {
		t := o.t
		id, err := p.Send(ctx, p.Owner, t.Text(), p.kb(&t))
		if err != nil {
			log.Printf("cycle: task %s → owner: %v", t.ID, err)
			continue
		}
		_ = p.mutate(ctx, func(d *cyDoc) bool {
			if x := d.byID(t.ID); x != nil {
				x.MsgID = id
				return true
			}
			return false
		})
	}
	return nil
}

// Loop: every 10 minutes.
func (p *CyclePlanner) Loop(ctx context.Context) {
	t := time.NewTicker(10 * time.Minute)
	defer t.Stop()
	for {
		c, cancel := context.WithTimeout(ctx, 2*time.Minute)
		if err := p.Tick(c); err != nil {
			log.Printf("cycle: %v", err)
		}
		cancel()
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

func cyValid(it CyItem) bool {
	if _, err := time.Parse("2006-01-02", it.Date); err != nil {
		return false
	}
	return hhmmRe(it.Time) && strings.TrimSpace(it.Res) != "" && (it.Kind == cyOfflineTag || it.Kind == cyOnlineTag)
}

// Approve creates the meetings. items nil: the proposals as they are.
func (p *CyclePlanner) Approve(ctx context.Context, id, by string, items []CyItem) (*CyTask, error) {
	var t CyTask
	found, already := false, false
	err := p.mutate(ctx, func(d *cyDoc) bool {
		x := d.byID(id)
		if x == nil {
			return false
		}
		found = true
		if x.Status != "pending" {
			already, t = true, *x
			return false
		}
		if items == nil {
			items = x.Items
		}
		x.Status, x.DoneAt, x.DoneBy = "approved", p.now().UTC().Format(time.RFC3339), by
		t = *x
		return true
	})
	if err != nil {
		return nil, err
	}
	if !found {
		return nil, fmt.Errorf("задача не найдена")
	}
	if already {
		return &t, nil
	}
	var done []CyItem
	for _, it := range items {
		if !cyValid(it) {
			continue
		}
		d, _ := time.Parse("2006-01-02", it.Date)
		err := error(nil)
		if p.Write != nil {
			err = p.Write(ctx, "addSchedule", map[string]string{"res": it.Res, "date": d.Format("02.01.2006"), "time": it.Time})
		}
		it.Done = err == nil
		if err != nil {
			it.Err = err.Error()
			log.Printf("cycle: %s %s: %v", it.Res, it.Date, err)
		}
		done = append(done, it)
	}
	_ = p.mutate(ctx, func(d *cyDoc) bool {
		if x := d.byID(id); x != nil {
			x.Approved = done
			t = *x
			return true
		}
		return false
	})
	if p.Edit != nil && t.MsgID != 0 {
		_ = p.Edit(ctx, p.Owner, t.MsgID, t.ApprovedText(), nil)
	}
	return &t, nil
}

func (t *CyTask) ApprovedText() string {
	var b strings.Builder
	b.WriteString("✅ Время следующих встреч утверждено\n")
	for _, it := range t.Approved {
		mark := "•"
		if !it.Done {
			mark = "⚠️"
		}
		kind := "онлайн"
		if it.Kind == cyOfflineTag {
			kind = "офлайн"
		}
		fmt.Fprintf(&b, "\n%s %s: %s, %s, %s", mark, it.Res, cyRuDay(it.Date), it.Time, kind)
		if !it.Done {
			b.WriteString(" (не создана: " + it.Err + ")")
		}
	}
	b.WriteString("\n\nВстречи в расписании, события в Google Календаре со ссылкой на созвон платформы, резидентам придут обычные напоминания.")
	return b.String()
}

func (p *CyclePlanner) dismiss(ctx context.Context, id, by string) (*CyTask, error) {
	var t *CyTask
	err := p.mutate(ctx, func(d *cyDoc) bool {
		x := d.byID(id)
		if x == nil || x.Status != "pending" {
			if x != nil {
				cp := *x
				t = &cp
			}
			return false
		}
		x.Status, x.DoneAt, x.DoneBy = "dismissed", p.now().UTC().Format(time.RFC3339), by
		cp := *x
		t = &cp
		return true
	})
	return t, err
}

// HandleCallback: the owner's buttons (team callback hook, prefix "cy:").
func (p *CyclePlanner) HandleCallback(ctx context.Context, cb bot.CallbackUpdate) (string, bool) {
	rest := strings.TrimPrefix(cb.Data, CyclePrefix)
	act, id, _ := strings.Cut(rest, ":")
	by := p.Team[cb.FromID]
	if by == "" {
		by = strings.TrimSpace(cb.FirstName + " " + cb.LastName)
	}
	switch act {
	case "ok":
		t, err := p.Approve(ctx, id, by, nil)
		if err != nil {
			return "Не получилось: " + err.Error(), true
		}
		if t.MsgID == 0 || t.MsgID != cb.MessageID {
			if p.Edit != nil {
				_ = p.Edit(ctx, cb.ChatID, cb.MessageID, t.ApprovedText(), nil)
			}
		}
		return "Встречи созданы", true
	case "no":
		t, err := p.dismiss(ctx, id, by)
		if err != nil || t == nil {
			return "Задача не найдена", true
		}
		if p.Edit != nil {
			_ = p.Edit(ctx, cb.ChatID, cb.MessageID, "🗓 Встречи после офлайн-разбора "+cyRuDay(t.OffDate)+": отмечено «не нужно». Их можно назначить на платформе в «Расписании».", nil)
		}
		return "Задача закрыта", true
	}
	return "", false
}

// ── the planner page (/cycle/:id): the team only ──

func (p *CyclePlanner) teamOf(c *gin.Context) string {
	if init := c.GetHeader("X-TG-Init"); init != "" && p.BotToken != "" {
		if u, err := verifyTelegramInitData(init, p.BotToken, time.Now()); err == nil {
			if n, ok := p.Team[u.ID]; ok {
				return firstNonBlank(n, "команда")
			}
		}
	}
	if raw, err := c.Cookie(PlatformSessionCookie); err == nil && raw != "" && len(p.JWT) > 0 {
		tkn, err := jwt.Parse(raw, func(t *jwt.Token) (any, error) {
			if _, ok := t.Method.(*jwt.SigningMethodHMAC); !ok {
				return nil, jwt.ErrSignatureInvalid
			}
			return p.JWT, nil
		})
		if err == nil && tkn.Valid {
			cl, _ := tkn.Claims.(jwt.MapClaims)
			role, _ := cl["role"].(string)
			sub, _ := cl["sub"].(string)
			if (role == "admin" || role == "moderator") && cl["as"] == nil {
				id, _ := strconv.ParseInt(strings.TrimPrefix(sub, "tg:"), 10, 64)
				return firstNonBlank(p.Team[id], "команда")
			}
		}
	}
	return ""
}

func (p *CyclePlanner) GetTask(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	if p.teamOf(c) == "" {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "team_only", "detail": "Откройте из бота или войдите на платформу"})
		return
	}
	d, err := r83Read[cyDoc](c.Request.Context(), p.Docs, "server", cyDocKey)
	if err != nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "try_again"})
		return
	}
	t := d.byID(c.Param("id"))
	if t == nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "not_found"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"task": t})
}

func (p *CyclePlanner) PostApprove(c *gin.Context) {
	by := p.teamOf(c)
	if by == "" {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "team_only"})
		return
	}
	var r struct {
		Items []CyItem `json:"items"`
	}
	if err := c.ShouldBindJSON(&r); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "bad_request"})
		return
	}
	items := []CyItem{}
	for _, it := range r.Items {
		if !cyValid(it) {
			c.JSON(http.StatusBadRequest, gin.H{"error": "check", "detail": "Проверьте дату и время: " + it.Res})
			return
		}
		items = append(items, it)
	}
	t, err := p.Approve(c.Request.Context(), c.Param("id"), by, items)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "not_found", "detail": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"task": t})
}

func (p *CyclePlanner) PostDismiss(c *gin.Context) {
	by := p.teamOf(c)
	if by == "" {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "team_only"})
		return
	}
	t, err := p.dismiss(c.Request.Context(), c.Param("id"), by)
	if err != nil || t == nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "not_found"})
		return
	}
	if p.Edit != nil && t.MsgID != 0 {
		_ = p.Edit(c.Request.Context(), p.Owner, t.MsgID, "🗓 Встречи после офлайн-разбора "+cyRuDay(t.OffDate)+": отмечено «не нужно».", nil)
	}
	c.JSON(http.StatusOK, gin.H{"task": t})
}

func (p *CyclePlanner) Register(r *gin.Engine) {
	r.GET("/cycle/:id", serveCyclePage)
	r.GET("/api/v1/cycle/:id", p.GetTask)
	r.POST("/api/v1/cycle/:id/approve", p.PostApprove)
	r.POST("/api/v1/cycle/:id/dismiss", p.PostDismiss)
}

// ServerWriteNotify: a club write the server makes itself, followed by the
// same hooks as the team's (the Google Calendar event with the call link).
func (w *ClubWrites) ServerWriteNotify(ctx context.Context, owner int64, who, action string, p map[string]string) error {
	if err := w.ServerWrite(ctx, owner, who, action, p); err != nil {
		return err
	}
	if !club.SheetLegacy() {
		w.after(ctx, action, p)
	}
	return nil
}
