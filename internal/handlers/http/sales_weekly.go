package http

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
)

// R51 (4): «Стабильность: еженедельная автопроверка и отчёт в бот».
//
// По понедельникам в 10:05 по Алматы сервер запускает полную проверку
// системы (SysCheck.Run, тот же /status) и собирает цифры недели: лиды по
// источникам, разборы, оплаты, отчёты резидентов, ошибки сервера (строки
// журнала с ошибками, ErrCounter), запросы к ИИ. Владельцу уходит ОДНО
// короткое сообщение: сначала только то, что требует действия, потом одна
// строка цифр. Отметка недели в bot meta: повторно не уходит.

const weeklyMetaKey = "sales:weekly"

// ── ошибки сервера из журнала ──

// ErrCounter counts the log lines that report an error, by day and by the
// first word of the line (the part of the server: «booking», «funnel»…).
type ErrCounter struct {
	mu      sync.Mutex
	pending map[string]map[string]int // day → kind → n
	now     func() time.Time
}

func NewErrCounter() *ErrCounter {
	return &ErrCounter{pending: map[string]map[string]int{}, now: time.Now}
}

var errLineRe = regexp.MustCompile(`(?i)(error|failed|fail:|panic|timeout|refused|не удал|ошибк|\| 5\d\d \|)`)
var logPrefixRe = regexp.MustCompile(`^\d{4}/\d{2}/\d{2} \d{2}:\d{2}:\d{2}(\.\d+)? `)

// Write: log.SetOutput(io.MultiWriter(os.Stderr, counter)).
func (e *ErrCounter) Write(p []byte) (int, error) {
	for _, line := range bytes.Split(p, []byte("\n")) {
		if len(line) == 0 || !errLineRe.Match(line) {
			continue
		}
		s := logPrefixRe.ReplaceAllString(string(line), "")
		kind := "другое"
		if i := strings.IndexAny(s, ": "); i > 0 && i < 30 {
			kind = strings.ToLower(s[:i])
		}
		day := e.now().In(almaty).Format("2006-01-02")
		e.mu.Lock()
		if e.pending[day] == nil {
			e.pending[day] = map[string]int{}
		}
		e.pending[day][kind]++
		e.mu.Unlock()
	}
	return len(p), nil
}

type metaStore interface {
	GetMeta(ctx context.Context, key string) (string, error)
	SetMeta(ctx context.Context, key, value string) error
}

// Flush adds what was counted to the day's total in the meta.
func (e *ErrCounter) Flush(ctx context.Context, meta metaStore, now time.Time) {
	if meta == nil {
		return
	}
	e.mu.Lock()
	p := e.pending
	e.pending = map[string]map[string]int{}
	e.mu.Unlock()
	for day, kinds := range p {
		key := "sales:errs:" + day
		cur := map[string]int{}
		if v, err := meta.GetMeta(ctx, key); err == nil && v != "" {
			_ = json.Unmarshal([]byte(v), &cur)
		}
		for k, n := range kinds {
			cur[k] += n
		}
		b, _ := json.Marshal(cur)
		if err := meta.SetMeta(ctx, key, string(b)); err != nil {
			e.mu.Lock() // keep it for the next flush
			if e.pending[day] == nil {
				e.pending[day] = map[string]int{}
			}
			for k, n := range kinds {
				e.pending[day][k] += n
			}
			e.mu.Unlock()
		}
	}
}

// salesMemMeta: the meta in memory (tests, no bot).
type salesMemMeta struct {
	mu sync.Mutex
	m  map[string]string
}

func (m *salesMemMeta) GetMeta(ctx context.Context, key string) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.m[key], nil
}

func (m *salesMemMeta) SetMeta(ctx context.Context, key, value string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.m == nil {
		m.m = map[string]string{}
	}
	m.m[key] = value
	return nil
}

func (s *ClubSales) meta() metaStore {
	if s.Meta != nil {
		return s.Meta
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.salesMemMeta == nil {
		s.salesMemMeta = &salesMemMeta{}
	}
	return s.salesMemMeta
}

// sampleAI keeps the day's highest count of requests per provider.
func (s *ClubSales) sampleAI(ctx context.Context) {
	if s.AIUsed == nil {
		return
	}
	used := s.AIUsed()
	if len(used) == 0 {
		return
	}
	m := s.meta()
	key := "sales:ai:" + s.now().In(almaty).Format("2006-01-02")
	cur := map[string]int{}
	if v, err := m.GetMeta(ctx, key); err == nil && v != "" {
		_ = json.Unmarshal([]byte(v), &cur)
	}
	changed := false
	for k, n := range used {
		if n > cur[k] {
			cur[k], changed = n, true
		}
	}
	if changed {
		b, _ := json.Marshal(cur)
		_ = m.SetMeta(ctx, key, string(b))
	}
}

// sumDays adds the per-day meta maps of the week.
func (s *ClubSales) sumDays(ctx context.Context, prefix string, from time.Time, days int) map[string]int {
	out := map[string]int{}
	m := s.meta()
	for i := 0; i < days; i++ {
		key := prefix + from.AddDate(0, 0, i).In(almaty).Format("2006-01-02")
		v, err := m.GetMeta(ctx, key)
		if err != nil || v == "" {
			continue
		}
		cur := map[string]int{}
		_ = json.Unmarshal([]byte(v), &cur)
		for k, n := range cur {
			out[k] += n
		}
	}
	return out
}

func sumMap(m map[string]int) int {
	n := 0
	for _, v := range m {
		n += v
	}
	return n
}

// ── отчёт ──

func leadAt(l map[string]any) time.Time {
	for _, k := range []string{"startAt", "created"} {
		if t := sTime(l, k); !t.IsZero() {
			return t
		}
	}
	if t, err := time.ParseInLocation("02.01.2006", sStr(l, "date"), almaty); err == nil {
		return t
	}
	return time.Time{}
}

func shortSource(src string) string {
	src = strings.TrimSpace(src)
	if src == "" {
		return "без источника"
	}
	if i := strings.Index(src, ":"); i > 0 {
		src = src[:i]
	}
	return strings.TrimSpace(src)
}

// WeeklyReport: the owner's message for the 7 days before now. n: actionable lines.
func (s *ClubSales) WeeklyReport(ctx context.Context, now time.Time) (string, int) {
	to := dayOf(now)
	from := to.AddDate(0, 0, -7)
	inWeek := func(t time.Time) bool { return !t.IsZero() && !t.Before(from) && t.Before(to) }
	var act []string
	// 1. the system check: only what is not fine
	if s.Check != nil {
		r := s.Check(ctx)
		for _, it := range r.Items {
			if it.State == "fail" || it.State == "warn" {
				act = append(act, stateMark(it.State)+" "+it.Title+": "+it.Text)
			}
		}
	}
	// 2. leads
	crm := s.read(ctx, "club", "bs_crm")
	bySrc := map[string]int{}
	leads, stale, decide := 0, 0, 0
	var later []string
	for _, x := range asList(crm["leads"]) {
		l, _ := x.(map[string]any)
		if l == nil {
			continue
		}
		at := leadAt(l)
		if inWeek(at) {
			leads++
			bySrc[shortSource(sStr(l, "source"))]++
		}
		col := sStr(l, "col")
		if col == "new" && !at.IsZero() && now.Sub(at) > 24*time.Hour && now.Sub(at) < 30*24*time.Hour && sStr(l, "handledAt") == "" {
			stale++
		}
		if col == "decide" {
			decide++
		}
		if r := sTime(sMap(l, "pz"), "remindAt"); !r.IsZero() && r.Before(now.AddDate(0, 0, 7)) && r.After(now) {
			later = append(later, sStr(l, "name"))
		}
	}
	if stale > 0 {
		act = append(act, fmt.Sprintf("📥 %d %s без ответа больше суток: CRM → «Новый»", stale, plural(stale, "новый лид", "новых лида", "новых лидов")))
	}
	if decide > 0 {
		act = append(act, fmt.Sprintf("🔥 %d в «Решение»: хотят в клуб, свяжитесь и закройте оплату", decide))
	}
	// 3. разборы
	slots := s.read(ctx, "club", slotsDoc)
	held, paid, unpaidNext := 0, 0, 0
	for _, v := range asList(slots["slots"]) {
		sl, ok := readSlot(v)
		if !ok || sl.str("status") != "booked" || sl.booking() == nil {
			continue
		}
		if inWeek(sl.Start) {
			held++
			if sl.booking()["paid"] == true {
				paid++
			}
		}
		if sl.Start.After(now) && sl.Start.Before(now.AddDate(0, 0, 7)) && sl.booking()["paid"] != true {
			unpaidNext++
		}
	}
	if unpaidNext > 0 {
		act = append(act, fmt.Sprintf("💳 %d %s на этой неделе без оплаты: напомните про Kaspi", unpaidNext, plural(unpaidNext, "запись на разбор", "записи на разбор", "записей на разбор")))
	}
	// 4. money and reports
	var income int64
	payN := 0
	repLine := ""
	if s.Club != nil {
		if snap, err := s.Club.Load(ctx); err == nil && snap != nil {
			for _, p := range snap.Payments {
				if p.Income > 0 && inWeek(p.Date) {
					income += p.Income
					payN++
				}
			}
		}
		if list, err := s.Club.LoadResidents(ctx); err == nil {
			days, _ := s.Club.ReportDays(ctx, from, to.Add(-time.Second))
			got, plan := 0, 0
			var weak []string
			for _, r := range activeResidents(list) {
				if r.Exception {
					continue
				}
				n := 0
				for _, d := range days {
					if (r.TgID != 0 && d.TgID == r.TgID) || (d.TgID == 0 && normName(d.Name) == normName(r.Name)) {
						n += d.Days
					}
				}
				if n > 7 {
					n = 7
				}
				got += n
				plan += 7
				if n < 4 {
					weak = append(weak, fmt.Sprintf("%s %d из 7", firstName(r.Name), n))
				}
			}
			if plan > 0 {
				repLine = fmt.Sprintf("отчёты %d%%", got*100/plan)
			}
			if len(weak) > 0 {
				if len(weak) > 6 {
					weak = append(weak[:6], fmt.Sprintf("ещё %d", len(weak)-6))
				}
				act = append(act, "📝 Мало отчётов: "+strings.Join(weak, ", "))
			}
			// renewals: ends within 14 days without a request, requests waiting for money
			_, ren := s.reportsDoc(ctx)
			var ending, waiting []string
			for _, r := range activeResidents(list) {
				st := ren[normName(r.Name)]
				if p := sMap(st, "pending"); p != nil {
					waiting = append(waiting, firstName(r.Name)+" (с "+sTime(p, "at").In(almaty).Format("02.01")+")")
					continue
				}
				if end, ok := packEnd(r, st, now); ok {
					if d := int(end.Sub(to).Hours() / 24); d >= 0 && d <= 14 {
						ending = append(ending, firstName(r.Name)+" до "+end.Format("02.01"))
					}
				}
			}
			if len(waiting) > 0 {
				act = append(act, "🔁 Ждут оплату продления: "+strings.Join(waiting, ", ")+". Пришла оплата: внесите в ДДС, пакет продлится сам")
			}
			if len(ending) > 0 {
				act = append(act, "⏳ Пакет заканчивается, продления нет: "+strings.Join(ending, ", "))
			}
		}
	}
	// 5. drafts waiting for the team
	dc := 0
	for _, c := range s.cases(ctx) {
		if c.Status == "draft" {
			dc++
		}
	}
	items, _ := s.reportsDoc(ctx)
	dr := 0
	for _, r := range items {
		if r.Status == "draft" {
			dr++
		}
	}
	if dc+dr > 0 {
		var p []string
		if dr > 0 {
			p = append(p, fmt.Sprintf("итоги резидентов %d", dr))
		}
		if dc > 0 {
			p = append(p, fmt.Sprintf("кейсы %d", dc))
		}
		act = append(act, "📄 Черновики ждут проверки: "+strings.Join(p, ", "))
	}
	if len(later) > 0 {
		act = append(act, "⏸ На этой неделе бот напомнит о клубе: "+strings.Join(later, ", "))
	}
	// 6. server errors and AI
	errs := s.sumDays(ctx, "sales:errs:", from, 7)
	prev := s.sumDays(ctx, "sales:errs:", from.AddDate(0, 0, -7), 7)
	ne := sumMap(errs)
	if ne >= 50 || (ne >= 10 && ne > 2*sumMap(prev)) {
		var top []string
		for i, k := range sortedKeys(errs) {
			if i == 3 {
				break
			}
			top = append(top, fmt.Sprintf("%s %d", k, errs[k]))
		}
		act = append(act, fmt.Sprintf("⚠️ Ошибок сервера за неделю %d (прошлая %d), чаще всего: %s. Логи Railway", ne, sumMap(prev), strings.Join(top, ", ")))
	}
	ai := sumMap(s.sumDays(ctx, "sales:ai:", from, 7))

	var b strings.Builder
	b.WriteString(fmt.Sprintf("📊 Итоги недели %s - %s\n\n", from.Format("02.01"), to.AddDate(0, 0, -1).Format("02.01")))
	if len(act) == 0 {
		b.WriteString("Что сделать: ничего срочного ✅\n")
	} else {
		b.WriteString("Что сделать:\n")
		for i, a := range act {
			b.WriteString(fmt.Sprintf("%d. %s\n", i+1, a))
		}
	}
	var src []string
	for i, k := range sortedKeys(bySrc) {
		if i == 4 {
			src = append(src, "другие")
			break
		}
		src = append(src, fmt.Sprintf("%s %d", k, bySrc[k]))
	}
	nums := []string{fmt.Sprintf("лиды %d", leads)}
	if len(src) > 0 {
		nums[0] += " (" + strings.Join(src, ", ") + ")"
	}
	nums = append(nums, fmt.Sprintf("разборы %d, оплачено %d", held, paid))
	nums = append(nums, fmt.Sprintf("оплаты %s (%d)", tenge(income), payN))
	if repLine != "" {
		nums = append(nums, repLine)
	}
	nums = append(nums, fmt.Sprintf("ошибки сервера %d", ne))
	if ai > 0 {
		nums = append(nums, fmt.Sprintf("ИИ %d запросов", ai))
	}
	b.WriteString("\nЦифры: " + strings.Join(nums, " · "))
	for _, f := range WeeklyExtra { // R53: «Тренды Threads» and the like: one line each
		if l := f(ctx, from, to); l != "" {
			b.WriteString("\n\n" + l)
		}
	}
	return noLongDash(b.String()), len(act)
}

// WeeklyExtra: more lines for the Monday report (empty: nothing to say).
var WeeklyExtra []func(ctx context.Context, from, to time.Time) string

// weeklyDue: Monday from 10:05 Almaty, once per week.
func weeklyDue(now time.Time) (string, bool) {
	a := now.In(almaty)
	if a.Weekday() != time.Monday || a.Hour() < 10 || (a.Hour() == 10 && a.Minute() < 5) || a.Hour() >= salesUntil {
		return "", false
	}
	y, w := a.ISOWeek()
	return fmt.Sprintf("%d-W%02d", y, w), true
}

// WeeklyTick sends the report once on Monday morning.
func (s *ClubSales) WeeklyTick(ctx context.Context) {
	if s.Settings(ctx).WeeklyOff || s.Send == nil || s.Owner == 0 {
		return
	}
	now := s.now()
	week, ok := weeklyDue(now)
	if !ok {
		return
	}
	m := s.meta()
	if v, _ := m.GetMeta(ctx, weeklyMetaKey); v == week {
		return
	}
	if err := m.SetMeta(ctx, weeklyMetaKey, week); err != nil {
		return
	}
	text, _ := s.WeeklyReport(ctx, now)
	if err := s.Send(ctx, s.Owner, text, nil); err != nil {
		log.Printf("sales: weekly: %v", err)
		_ = m.SetMeta(ctx, weeklyMetaKey, "")
	}
}

// WeeklyNow: POST /sales/weekly {send?}: the report now (preview, or to the owner).
func (s *ClubSales) WeeklyNow(c *gin.Context) {
	var r struct {
		Send bool `json:"send"`
	}
	_ = c.ShouldBindJSON(&r)
	ctx := c.Request.Context()
	text, n := s.WeeklyReport(ctx, s.now())
	if r.Send && s.Send != nil && s.Owner != 0 {
		if err := s.Send(ctx, s.Owner, text, nil); err != nil {
			c.JSON(http.StatusBadGateway, gin.H{"error": "send", "message": err.Error(), "text": text})
			return
		}
	}
	c.JSON(http.StatusOK, gin.H{"text": text, "actions": n, "sent": r.Send})
}
