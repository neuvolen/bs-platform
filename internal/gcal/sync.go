package gcal

import (
	"context"
	"fmt"
	"log"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/bnursik/business_surgery_backend/internal/calllink"
	"github.com/bnursik/business_surgery_backend/internal/club"
)

// Sync keeps the calendar in step with the club's meetings on the server.
type Sync struct {
	C *Client
	// Load: the club's data (meetings, «Лог встреч», residents).
	Load func(ctx context.Context) (*club.Snapshot, error)
	// SetLink keeps the Meet link and the event of a meeting the server created.
	SetLink func(ctx context.Context, res string, day time.Time, link, eventID string) error
	// Notify tells the team (one short message).
	Notify func(ctx context.Context, text string)

	mu sync.Mutex // one change of the calendar at a time
}

// BackfillDays: how far back the one-time catch-up paints held meetings.
const BackfillDays = 14

// appDate reads the app's "dd.MM.yyyy" or "dd.MM" (this year).
func appDate(s string, now time.Time) (time.Time, bool) {
	p := strings.Split(strings.TrimSpace(s), ".")
	if len(p) < 2 {
		if d, ok := club.Date(s); ok {
			return time.Date(d.Year(), d.Month(), d.Day(), 0, 0, 0, 0, club.Almaty), true
		}
		return time.Time{}, false
	}
	d, _ := strconv.Atoi(p[0])
	m, _ := strconv.Atoi(p[1])
	y := now.In(club.Almaty).Year()
	if len(p) > 2 && p[2] != "" {
		y, _ = strconv.Atoi(p[2])
		if y < 100 {
			y += 2000
		}
	}
	if d < 1 || d > 31 || m < 1 || m > 12 || y < 2000 {
		return time.Time{}, false
	}
	return time.Date(y, time.Month(m), d, 0, 0, 0, 0, club.Almaty), true
}

// at: day + "HH:MM" in Almaty; ok false without a readable time.
func at(day time.Time, hhmm string) (time.Time, bool) {
	c := club.ClockTime(hhmm)
	if c == "" {
		return day, false
	}
	h, _ := strconv.Atoi(c[:2])
	m, _ := strconv.Atoi(c[3:5])
	return time.Date(day.Year(), day.Month(), day.Day(), h, m, 0, 0, club.Almaty), true
}

func bare(title string) string {
	return strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(title), "✅"))
}

// IsGroup: the offline day's single event («Офлайн день BS. N резидентов»).
func IsGroup(e Event) bool {
	return strings.HasPrefix(strings.ToLower(bare(e.Summary)), "офлайн")
}

// IsClub: an event of the club (the script's and the server's titles).
func IsClub(e Event) bool {
	t := strings.ToLower(bare(e.Summary))
	return strings.Contains(t, "встреча bs") || strings.HasPrefix(t, "офлайн день") || strings.Contains(t, "business surgery") ||
		strings.HasPrefix(t, "трекинг bs")
}

// names: the full name and the first name (the script matched either).
func names(res string) (full, first string) {
	full = strings.ToLower(strings.ReplaceAll(strings.TrimSpace(res), "ё", "е"))
	if f := strings.Fields(full); len(f) > 0 {
		first = f[0]
	}
	return
}

func low(s string) string { return strings.ToLower(strings.ReplaceAll(s, "ё", "е")) }

// mentions: the event is the resident's (by the title, or the offline day's list of names).
func mentions(e Event, res string) bool {
	full, first := names(res)
	t := low(bare(e.Summary))
	if (len([]rune(full)) > 2 && strings.Contains(t, full)) || (len([]rune(first)) > 3 && strings.Contains(t, first)) {
		return true
	}
	if IsGroup(e) {
		d := low(e.Description)
		return len([]rune(full)) > 2 && strings.Contains(d, full)
	}
	return false
}

// FindEvent: the event of res's meeting among the day's events: the one at
// its time first, else the closest that day. offline also takes the offline
// day's event of that time.
func FindEvent(evs []Event, res string, start time.Time, hasTime, offline bool) (Event, bool) {
	var hits []Event
	for _, e := range evs {
		if mentions(e, res) || (offline && IsGroup(e) && hasTime && e.StartAt().Equal(start)) {
			hits = append(hits, e)
		}
	}
	if len(hits) == 0 {
		return Event{}, false
	}
	sort.SliceStable(hits, func(i, j int) bool {
		di, dj := hits[i].StartAt().Sub(start), hits[j].StartAt().Sub(start)
		if di < 0 {
			di = -di
		}
		if dj < 0 {
			dj = -dj
		}
		return di < dj
	})
	if hasTime && !hits[0].StartAt().Equal(start) {
		// a resident has one meeting a day: the closest one is it
		if d := hits[0].StartAt().Sub(start); d > 12*time.Hour || d < -12*time.Hour {
			return Event{}, false
		}
	}
	return hits[0], true
}

// doneFields: what turns an event into a held meeting (none when it already is).
func doneFields(e Event) map[string]any {
	f := map[string]any{}
	if e.ColorID != DoneColor {
		f["colorId"] = DoneColor
	}
	if !strings.HasPrefix(strings.TrimSpace(e.Summary), "✅") {
		f["summary"] = DonePrefix + strings.TrimSpace(e.Summary)
	}
	if len(f) > 0 && !e.IsQuiet() {
		f["reminders"] = Quiet()
	}
	return f
}

// Mark: one held meeting to paint.
type Mark struct {
	Res     string
	Day     time.Time // midnight, Almaty
	Time    string    // "HH:MM" or ""
	Offline bool
}

// MarkResult: what the painting did.
type MarkResult struct {
	Painted  []string // «Имя дд.мм»
	Already  int
	NotFound []string
}

// MarkDone paints the meetings' events (grouped by day: one listing per day).
func (s *Sync) MarkDone(ctx context.Context, marks []Mark) (MarkResult, error) {
	var r MarkResult
	if len(marks) == 0 {
		return r, nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	cal, err := s.C.Calendar(ctx)
	if err != nil {
		return r, err
	}
	byDay := map[string][]Mark{}
	var days []string
	for _, m := range marks {
		k := m.Day.Format("2006-01-02")
		if _, ok := byDay[k]; !ok {
			days = append(days, k)
		}
		byDay[k] = append(byDay[k], m)
	}
	sort.Strings(days)
	for _, k := range days {
		d := byDay[k][0].Day
		evs, err := s.C.Events(ctx, cal, d, d.Add(24*time.Hour))
		if err != nil {
			return r, err
		}
		seen := map[string]bool{}
		for _, m := range byDay[k] {
			label := m.Res + " " + m.Day.Format("02.01")
			start, hasTime := at(m.Day, m.Time)
			e, ok := FindEvent(evs, m.Res, start, hasTime, m.Offline)
			if !ok {
				r.NotFound = append(r.NotFound, label)
				continue
			}
			if seen[e.ID] {
				continue
			}
			seen[e.ID] = true
			f := doneFields(e)
			if len(f) == 0 {
				r.Already++
				continue
			}
			if err := s.C.Patch(ctx, cal, e.ID, f); err != nil {
				return r, err
			}
			r.Painted = append(r.Painted, label)
			for i := range evs { // the same event is not painted twice («✅ ✅»)
				if evs[i].ID == e.ID {
					evs[i].ColorID, evs[i].Summary = DoneColor, DonePrefix+bare(evs[i].Summary)
				}
			}
		}
	}
	return r, nil
}

func (s *Sync) offline(snap *club.Snapshot, res string) bool {
	if snap == nil {
		return false
	}
	n := club.NormName(res)
	for _, r := range snap.Residents {
		if club.NormName(r.Name) == n {
			return strings.TrimSpace(r.Format) == "" || strings.EqualFold(strings.TrimSpace(r.Format), "Офлайн")
		}
	}
	return false
}

func (s *Sync) meetingOf(snap *club.Snapshot, res string, day time.Time) *club.Meeting {
	if snap == nil {
		return nil
	}
	n := club.NormName(res)
	for i := range snap.Meetings {
		m := &snap.Meetings[i]
		md := m.Date.In(club.Almaty)
		if club.NormName(m.Resident) == n && md.Year() == day.Year() && md.YearDay() == day.YearDay() {
			return m
		}
	}
	return nil
}

func (s *Sync) note(ctx context.Context, what string, err error) {
	if err == nil {
		return
	}
	log.Printf("gcal: %s: %v", what, err)
	if NeedsConsent(err) {
		return // MetaError is set by the token step; /calendar shows it
	}
	_ = s.C.Store.SetMeta(ctx, MetaError, what+": "+err.Error())
}

// OnWrite follows a club write (the app or the platform): a held meeting is
// painted, a new one gets its event, a moved or deleted one follows.
func (s *Sync) OnWrite(ctx context.Context, action string, p map[string]string) {
	if s == nil || s.C == nil || !s.C.Connected(ctx) {
		return
	}
	ctx, cancel := context.WithTimeout(ctx, 90*time.Second)
	defer cancel()
	now := s.C.Now()
	var snap *club.Snapshot
	if s.Load != nil {
		snap, _ = s.Load(ctx)
	}
	switch action {
	case "confirmMeeting", "markAttendance":
		d, ok := appDate(p["date"], now)
		if !ok {
			d = time.Date(now.In(club.Almaty).Year(), now.In(club.Almaty).Month(), now.In(club.Almaty).Day(), 0, 0, 0, 0, club.Almaty)
		}
		var marks []Mark
		if action == "confirmMeeting" {
			marks = append(marks, Mark{Res: p["res"], Day: d, Time: p["time"], Offline: s.offline(snap, p["res"])})
		} else {
			for _, n := range strings.Split(p["names"], "|") {
				if n = strings.TrimSpace(n); n == "" {
					continue
				}
				tm := p["time"]
				if m := s.meetingOf(snap, n, d); m != nil && tm == "" {
					tm = m.Time
				}
				marks = append(marks, Mark{Res: n, Day: d, Time: tm, Offline: true})
			}
		}
		r, err := s.MarkDone(ctx, marks)
		s.note(ctx, "отметка встречи", err)
		if err == nil {
			log.Printf("gcal: %s %s: painted %v, already %d, not found %v", action, p["date"], r.Painted, r.Already, r.NotFound)
		}
	case "addSchedule":
		s.note(ctx, "новая встреча", s.create(ctx, snap, p["res"], p["date"], p["time"]))
	case "updateMeeting":
		s.note(ctx, "перенос встречи", s.move(ctx, snap, p))
	case "deleteSchedule":
		s.note(ctx, "удаление встречи", s.remove(ctx, p["res"], p["date"], p["time"]))
	}
}

// teamGuests: the team's addresses as the club's events already have them
// (so the event shows in their own calendars), the owner aside.
func teamGuests(evs []Event) []Attendee {
	seen := map[string]bool{}
	var out []Attendee
	for i := len(evs) - 1; i >= 0 && len(out) < 5; i-- {
		if !IsClub(evs[i]) {
			continue
		}
		for _, a := range evs[i].Attendees {
			em := strings.ToLower(strings.TrimSpace(a.Email))
			if em == "" || a.Self || a.Organizer || seen[em] || strings.HasSuffix(em, "calendar.google.com") {
				continue
			}
			seen[em] = true
			out = append(out, Attendee{Email: em, ResponseStatus: "accepted"})
		}
		if len(out) > 0 {
			break
		}
	}
	return out
}

func iso(t time.Time) string { return t.In(club.Almaty).Format("2006-01-02T15:04:05-07:00") }

// create: the server's own event for a meeting added in the app or on the
// platform (online: with a Meet link written back to the meeting).
func (s *Sync) create(ctx context.Context, snap *club.Snapshot, res, date, tm string) error {
	now := s.C.Now()
	d, ok := appDate(date, now)
	if !ok || strings.TrimSpace(res) == "" {
		return nil
	}
	if tm == "" {
		if m := s.meetingOf(snap, res, d); m != nil {
			tm = m.Time
		}
	}
	start, _ := at(d, firstNonEmpty(tm, "12:00"))
	s.mu.Lock()
	defer s.mu.Unlock()
	cal, err := s.C.Calendar(ctx)
	if err != nil {
		return err
	}
	evs, err := s.C.Events(ctx, cal, start.Add(-30*24*time.Hour), start.Add(24*time.Hour))
	if err != nil {
		return err
	}
	off := s.offline(snap, res)
	var dayEvs []Event
	for _, e := range evs {
		if st := e.StartAt(); !st.IsZero() && st.In(club.Almaty).YearDay() == d.YearDay() && st.Year() == d.Year() {
			dayEvs = append(dayEvs, e)
		}
	}
	if _, has := FindEvent(dayEvs, res, start, true, false); has {
		return nil // the event is already there
	}
	place := ""
	if m := s.meetingOf(snap, res, d); m != nil {
		place = firstNonEmpty(m.Place, m.AddrCell)
	}
	e := Event{Summary: "Встреча BS. " + strings.TrimSpace(res),
		Description: "🎯 Трекинг Business Surgery\n\n👤 Резидент: " + strings.TrimSpace(res) + "\n🗓 " + start.Format("02.01.2006 15:04") + " (Алматы)\n\nBusiness Surgery",
		Location:    place, ColorID: "11",
		Start:     EventTime{DateTime: iso(start), TimeZone: "Asia/Almaty"},
		End:       EventTime{DateTime: iso(start.Add(time.Hour)), TimeZone: "Asia/Almaty"},
		Attendees: teamGuests(evs)}
	if !off { // R75 call: созвон на платформе, Meet в событии остаётся запасным
		if link := calllink.URL(res); link != "" {
			e.Location, e.Description = link, CallDesc(e.Description, link)
		}
	}
	got, err := s.C.Insert(ctx, cal, e, !off)
	if err != nil {
		return err
	}
	log.Printf("gcal: event for %s %s created (meet %t)", res, start.Format("02.01 15:04"), got.HangoutLink != "")
	if s.SetLink != nil {
		return s.SetLink(ctx, res, d, got.HangoutLink, got.ID)
	}
	return nil
}

func firstNonEmpty(v ...string) string {
	for _, s := range v {
		if strings.TrimSpace(s) != "" {
			return strings.TrimSpace(s)
		}
	}
	return ""
}

func (s *Sync) find(ctx context.Context, cal, res string, d time.Time, tm string) (Event, bool, error) {
	evs, err := s.C.Events(ctx, cal, d, d.Add(24*time.Hour))
	if err != nil {
		return Event{}, false, err
	}
	start, hasTime := at(d, tm)
	var own []Event // an offline day's shared event is not moved or deleted for one person
	for _, e := range evs {
		if !IsGroup(e) {
			own = append(own, e)
		}
	}
	e, ok := FindEvent(own, res, start, hasTime, false)
	return e, ok, nil
}

func (s *Sync) move(ctx context.Context, snap *club.Snapshot, p map[string]string) error {
	now := s.C.Now()
	od, ok := appDate(p["oldDate"], now)
	if !ok {
		return nil
	}
	nd, ok := appDate(firstNonEmpty(p["newDate"], p["oldDate"]), now)
	if !ok {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	cal, err := s.C.Calendar(ctx)
	if err != nil {
		return err
	}
	e, found, err := s.find(ctx, cal, p["oldRes"], od, p["oldTime"])
	if err != nil || !found {
		return err
	}
	ns, _ := at(nd, firstNonEmpty(p["newTime"], p["oldTime"], "12:00"))
	dur := time.Hour
	if en, err := time.Parse(time.RFC3339, e.End.DateTime); err == nil && en.After(e.StartAt()) {
		dur = en.Sub(e.StartAt())
	}
	return s.C.Patch(ctx, cal, e.ID, map[string]any{
		"start":     EventTime{DateTime: iso(ns), TimeZone: "Asia/Almaty"},
		"end":       EventTime{DateTime: iso(ns.Add(dur)), TimeZone: "Asia/Almaty"},
		"reminders": Quiet()})
}

func (s *Sync) remove(ctx context.Context, res, date, tm string) error {
	d, ok := appDate(date, s.C.Now())
	if !ok {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	cal, err := s.C.Calendar(ctx)
	if err != nil {
		return err
	}
	e, found, err := s.find(ctx, cal, res, d, tm)
	if err != nil || !found || e.IsDone() {
		return err
	}
	return s.C.Delete(ctx, cal, e.ID)
}

// Backfill paints the meetings held in the last days (marked on the server
// while nothing painted the calendar).
func (s *Sync) Backfill(ctx context.Context, days int) (MarkResult, error) {
	if s.Load == nil {
		return MarkResult{}, nil
	}
	snap, err := s.Load(ctx)
	if err != nil {
		return MarkResult{}, err
	}
	now := s.C.Now().In(club.Almaty)
	from := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, club.Almaty).AddDate(0, 0, -days)
	seen := map[string]bool{}
	var marks []Mark
	add := func(res string, d time.Time, tm string) {
		d = d.In(club.Almaty)
		d = time.Date(d.Year(), d.Month(), d.Day(), 0, 0, 0, 0, club.Almaty)
		if d.Before(from) || d.After(now) || strings.TrimSpace(res) == "" {
			return
		}
		k := club.NormName(res) + "|" + d.Format("2006-01-02")
		if seen[k] {
			return
		}
		seen[k] = true
		if tm == "" {
			if m := s.meetingOf(snap, res, d); m != nil {
				tm = m.Time
			}
		}
		marks = append(marks, Mark{Res: strings.TrimSpace(res), Day: d, Time: club.ClockTime(tm), Offline: s.offline(snap, res)})
	}
	for _, m := range snap.Meetings {
		if m.Done {
			add(m.Resident, m.Date, m.Time)
		}
	}
	for _, e := range snap.MeetingLog {
		add(e.Resident, e.Date, "")
	}
	return s.MarkDone(ctx, marks)
}

// QuietFuture: the club's coming events lose their reminders (the 1-hour
// popup and e-mail the script set); the calendar's own defaults too.
func (s *Sync) QuietFuture(ctx context.Context) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	cal, err := s.C.Calendar(ctx)
	if err != nil {
		return 0, err
	}
	if err := s.C.QuietCalendar(ctx, cal); err != nil {
		log.Printf("gcal: calendar defaults: %v", err)
	}
	now := s.C.Now()
	evs, err := s.C.Events(ctx, cal, now, now.AddDate(1, 0, 0))
	if err != nil {
		return 0, err
	}
	n := 0
	for _, e := range evs {
		if !IsClub(e) || e.IsQuiet() || e.Recurring != "" {
			continue
		}
		if err := s.C.Patch(ctx, cal, e.ID, map[string]any{"reminders": Quiet()}); err != nil {
			return n, err
		}
		n++
	}
	return n, nil
}

// Catchup runs once after the owner connects the calendar (and at a start,
// if it has not run yet): the last two weeks painted, the coming events quiet.
func (s *Sync) Catchup(ctx context.Context) (string, error) {
	if s == nil || s.C == nil || !s.C.Connected(ctx) {
		return "", ErrNotConnected
	}
	if v, _ := s.C.Store.GetMeta(ctx, MetaBackfill); strings.HasPrefix(v, "done:") {
		return "", nil
	}
	r, err := s.Backfill(ctx, BackfillDays)
	if err != nil {
		s.note(ctx, "покраска прошедших встреч", err)
		return "", err
	}
	q, err := s.QuietFuture(ctx)
	if err != nil {
		s.note(ctx, "уведомления будущих встреч", err)
		return "", err
	}
	_ = s.C.Store.SetMeta(ctx, MetaBackfill, "done:"+s.C.Now().UTC().Format(time.RFC3339))
	_ = s.C.Store.SetMeta(ctx, MetaError, "")
	msg := fmt.Sprintf("📅 Google Календарь подключён к платформе.\n\n✅ Окрашено прошедших встреч за %d дней: %d (уже были зелёными: %d).\n🔕 Уведомления за час и письма убраны у будущих встреч: %d.",
		BackfillDays, len(r.Painted), r.Already, q)
	if len(r.NotFound) > 0 {
		msg += fmt.Sprintf("\nНе нашлось в календаре: %d (%s).", len(r.NotFound), strings.Join(limit(r.NotFound, 6), ", "))
	}
	msg += "\n\nДальше само: «Встреча прошла» в приложении или на платформе красит событие в зелёный и ставит ✅."
	log.Printf("gcal: catch-up: painted %d %v, already %d, not found %d %v, quiet %d", len(r.Painted), r.Painted, r.Already, len(r.NotFound), r.NotFound, q)
	return msg, nil
}

// MetaCallLinks: "done:<RFC3339>": the one-time R75 patch of the coming
// online events (the platform's call link in place and description) ran.
const MetaCallLinks = "gcal_r75_calllinks"

// CallDesc: the event's description led by the platform's call link (once).
func CallDesc(desc, link string) string {
	if link == "" || strings.Contains(desc, link) {
		return desc
	}
	line := "🎥 Созвон на платформе Business Surgery: " + link + "\nGoogle Meet в событии: запасной вариант, если платформа не открылась."
	if strings.TrimSpace(desc) == "" {
		return line
	}
	return line + "\n\n" + desc
}

// PatchCallLinks (R75 call, once): the coming online meetings' events get
// the platform's call link as their place and at the top of their
// description. Meet stays attached as the fallback.
func (s *Sync) PatchCallLinks(ctx context.Context) (int, error) {
	if s == nil || s.C == nil || !s.C.Connected(ctx) {
		return 0, ErrNotConnected
	}
	if v, _ := s.C.Store.GetMeta(ctx, MetaCallLinks); strings.HasPrefix(v, "done:") {
		return 0, nil
	}
	if s.Load == nil {
		return 0, nil
	}
	snap, err := s.Load(ctx)
	if err != nil {
		return 0, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	cal, err := s.C.Calendar(ctx)
	if err != nil {
		return 0, err
	}
	now := s.C.Now().In(club.Almaty)
	today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, club.Almaty)
	evs, err := s.C.Events(ctx, cal, today, today.AddDate(0, 6, 0))
	if err != nil {
		return 0, err
	}
	n, seen := 0, map[string]bool{}
	for _, m := range snap.Meetings {
		d := m.Date.In(club.Almaty)
		d = time.Date(d.Year(), d.Month(), d.Day(), 0, 0, 0, 0, club.Almaty)
		if d.Before(today) || m.Done || !(m.Online || calllink.IsOnline(m.Link, "")) || s.offline(snap, m.Resident) && !strings.Contains(m.Link, "http") {
			continue
		}
		var day []Event
		for _, e := range evs {
			if st := e.StartAt(); !st.IsZero() && !IsGroup(e) && st.In(club.Almaty).Year() == d.Year() && st.In(club.Almaty).YearDay() == d.YearDay() {
				day = append(day, e)
			}
		}
		start, hasTime := at(d, m.Time)
		e, ok := FindEvent(day, m.Resident, start, hasTime, false)
		if !ok || seen[e.ID] || e.Recurring != "" {
			continue
		}
		seen[e.ID] = true
		link := calllink.URL(m.Resident)
		desc := CallDesc(e.Description, link)
		if e.Location == link && desc == e.Description {
			continue
		}
		if err := s.C.Patch(ctx, cal, e.ID, map[string]any{"location": link, "description": desc}); err != nil {
			return n, err
		}
		n++
	}
	_ = s.C.Store.SetMeta(ctx, MetaCallLinks, "done:"+s.C.Now().UTC().Format(time.RFC3339))
	log.Printf("gcal: R75 call links: %d coming online events now lead to the platform", n)
	return n, nil
}

func limit(v []string, n int) []string {
	if len(v) > n {
		return append(append([]string{}, v[:n]...), "…")
	}
	return v
}
