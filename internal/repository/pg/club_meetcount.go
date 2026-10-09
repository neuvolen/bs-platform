package pg

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/bnursik/business_surgery_backend/internal/club"
)

// R69: «Азамат: почему у него всего 1 встреча из 36, если уже минимум 6?»
// «Проведено» was a stored number: the sheet's script cleared it on every
// payment of the resident (an annual resident's instalment wiped the year's
// count), the server added one per confirmation and nothing ever checked it
// against the meetings log. Now the log is the single source of truth:
//
//	проведено = distinct days in «Лог встреч» from package_from on + meetings_adjust
//
// package_from moves when a package ends (3/3: the day after that meeting),
// is renewed or extended by a payment; meetings_adjust keeps what the log
// cannot show (the sheet's count at the cutover, a manual «Встречи» edit).
// meetings_done keeps the computed value for everyone who reads it.

type logDay struct {
	name string
	day  string // YYYY-MM-DD
}

func (a *applier) logDays() ([]logDay, error) {
	rows, err := a.tx.Query(a.ctx, `SELECT resident, date FROM club_meeting_log`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []logDay
	for rows.Next() {
		var n string
		var d time.Time
		if err := rows.Scan(&n, &d); err != nil {
			return nil, err
		}
		out = append(out, logDay{club.NormName(n), d.Format("2006-01-02")})
	}
	return out, rows.Err()
}

// residentNames: the resident's name and aliases, normalized.
func residentNames(r *resRow) map[string]bool {
	m := map[string]bool{club.NormName(r.name): true}
	for _, x := range strings.Split(r.aliases, ",") {
		if x = club.NormName(x); x != "" {
			m[x] = true
		}
	}
	return m
}

// meetDays: the resident's distinct days in the log from (YYYY-MM-DD, "" for all).
func meetDays(log []logDay, r *resRow, from string) []string {
	names := residentNames(r)
	seen := map[string]bool{}
	var out []string
	for _, l := range log {
		if names[l.name] && l.day >= from && !seen[l.day] {
			seen[l.day] = true
			out = append(out, l.day)
		}
	}
	sort.Strings(out)
	return out
}

// ensureBase: a resident never counted from the log yet (package_from
// empty: the sheet's data, a new resident) keeps the stored counter: the log
// counts from day on, the adjustment holds the rest.
func (a *applier) ensureBase(r *resRow, day time.Time) error {
	var from *time.Time
	var stored int64
	if err := a.tx.QueryRow(a.ctx, `SELECT package_from, meetings_done FROM club_residents WHERE id = $1`, r.id).Scan(&from, &stored); err != nil {
		return err
	}
	if from != nil {
		return nil
	}
	log, err := a.logDays()
	if err != nil {
		return err
	}
	f := day.Format("2006-01-02")
	return a.update("club_residents", r.id, `package_from = $2, meetings_adjust = $3`, f, stored-int64(len(meetDays(log, r, f))))
}

// countDone computes «проведено» and stores it in meetings_done.
func (a *applier) countDone(r *resRow) (int64, error) {
	var from *time.Time
	var adj, stored int64
	if err := a.tx.QueryRow(a.ctx, `SELECT package_from, meetings_adjust, meetings_done FROM club_residents WHERE id = $1`, r.id).Scan(&from, &adj, &stored); err != nil {
		return 0, err
	}
	log, err := a.logDays()
	if err != nil {
		return 0, err
	}
	f := ""
	if from != nil {
		f = from.Format("2006-01-02")
	}
	done := int64(len(meetDays(log, r, f))) + adj
	if done < 0 {
		done = 0
	}
	if done != stored {
		if err := a.update("club_residents", r.id, `meetings_done = $2`, done); err != nil {
			return 0, err
		}
	}
	r.done = done
	return done, nil
}

// setDone: «проведено» is x from now on (a manual edit, a renewal): the
// package keeps its start, the adjustment absorbs the difference.
func (a *applier) setDone(r *resRow, x int64) error {
	if err := a.ensureBase(r, a.day()); err != nil {
		return err
	}
	var from *time.Time
	if err := a.tx.QueryRow(a.ctx, `SELECT package_from FROM club_residents WHERE id = $1`, r.id).Scan(&from); err != nil {
		return err
	}
	log, err := a.logDays()
	if err != nil {
		return err
	}
	f := ""
	if from != nil {
		f = from.Format("2006-01-02")
	}
	adj := x - int64(len(meetDays(log, r, f)))
	if err := a.update("club_residents", r.id, `meetings_adjust = $2, meetings_done = GREATEST($3::bigint, 0)`, adj, x); err != nil {
		return err
	}
	r.done = x
	return nil
}

// newPackage: a new package starts on day (its meetings count from then).
func (a *applier) newPackage(r *resRow, day time.Time) error {
	if err := a.update("club_residents", r.id, `package_from = $2, meetings_adjust = 0`, day.Format("2006-01-02")); err != nil {
		return err
	}
	_, err := a.countDone(r)
	return err
}

// resetToday: «проведено» becomes 0 now: the package starts today, minus the
// meetings already logged today.
func (a *applier) resetToday(r *resRow) error {
	d := a.day()
	if err := a.update("club_residents", r.id, `package_from = $2`, d.Format("2006-01-02")); err != nil {
		return err
	}
	return a.setDone(r, 0)
}

// ── Recount and the one-off reconstruction ──

// MeetChange: one resident's counter before and after.
type MeetChange struct {
	Resident string
	Before   int64
	After    int64
	Granted  int64
	Days     []string // the log days counted (DD.MM)
	Adjust   int64
	From     string
	Why      string
}

// RecountMeetings recomputes every resident's «проведено» from the log.
func (r *ClubRepo) RecountMeetings(ctx context.Context) ([]MeetChange, error) {
	tx, err := r.db.Pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	a := &applier{ctx: ctx, tx: tx, at: time.Now()}
	list, err := a.residents()
	if err != nil {
		return nil, err
	}
	var out []MeetChange
	for i := range list {
		x := &list[i]
		if x.archive {
			continue
		}
		before := x.done
		if err := a.ensureBase(x, a.day()); err != nil {
			return nil, err
		}
		after, err := a.countDone(x)
		if err != nil {
			return nil, err
		}
		if after != before {
			out = append(out, MeetChange{Resident: x.name, Before: before, After: after, Granted: x.granted})
		}
	}
	return out, tx.Commit(ctx)
}

// MeetRestore: a meeting the log lost, put back by the reconstruction.
type MeetRestore struct {
	Resident string
	Day      string // YYYY-MM-DD
	Why      string
}

// R69 reconstruction (owner's question about Азамат and Азат, 09.10.2026).
// Sources: «Лог встреч» on the server (complete from 27.05.2026), the
// sheet's counter at the cutover (04.10.2026), the 28.09 snapshot of the
// sheet, «Расписание». The offline group of 29.09.2026 15:00 took place:
// the sheet counted it for Азат (3/36 at the cutover: 09.09, 18.09, 29.09)
// and Арлан (5/36), Алишер (4/9); the log has none of the offline residents
// that day. Азат's 09.09 was counted by the sheet (2/36 on 28.09 with only
// 18.09 in the log) and the offline group met that day.
var R69Restore = []MeetRestore{
	{"Азамат TV", "2026-09-29", "офлайн-группа 29.09 15:00: была, у резидентов группы засчитана в таблице, в журнал не попала"},
	{"Азат", "2026-09-29", "офлайн-группа 29.09 15:00: засчитана в таблице (3/36 на перенос)"},
	{"Азат", "2026-09-09", "офлайн-группа 09.09: засчитана в таблице (2/36 на 28.09 при одной записи 18.09)"},
	{"Арлан", "2026-09-29", "офлайн-группа 29.09 15:00: засчитана в таблице (5/36 на перенос)"},
	{"Алишер", "2026-09-29", "офлайн-группа 29.09 15:00: засчитана в таблице (4/9 на перенос)"},
}

// logStart: the server's meetings log is complete from this day on.
const logStart = "2026-05-27"

// MeetBackfill sets package_from and meetings_adjust once for everyone:
//   - a year's package (36 meetings) that started inside the log's span
//     counts the log from its start (the anniversary of joining): the sheet's
//     counter there was wiped by the script on every instalment;
//   - a year's package that started before the log: the log's days of the
//     package if they are more than the counter (the counter is a lower
//     bound that was wiped), else the counter;
//   - any other package keeps its counter: the counter from today on, the
//     log adds the meetings that follow.
// The lost log days above are put back first. dry: nothing is kept.
func (r *ClubRepo) MeetBackfill(ctx context.Context, dry bool) ([]MeetChange, []string, error) {
	tx, err := r.db.Pool.Begin(ctx)
	if err != nil {
		return nil, nil, err
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	a := &applier{ctx: ctx, tx: tx, at: time.Now()}
	var notes []string
	for _, m := range R69Restore {
		res, err := a.matchResident(m.Resident)
		if err != nil {
			notes = append(notes, m.Resident+": "+err.Error())
			continue
		}
		log, err := a.logDays()
		if err != nil {
			return nil, nil, err
		}
		have := false
		for _, d := range meetDays(log, res, m.Day) {
			if d == m.Day {
				have = true
			}
		}
		if have {
			continue
		}
		d, _ := time.Parse("2006-01-02", m.Day)
		if _, err := a.insert("club_meeting_log", `INSERT INTO club_meeting_log (date, resident, time_cell) VALUES ($1,$2,$3)`,
			m.Day, res.name, "восстановлено R69"); err != nil {
			return nil, nil, err
		}
		notes = append(notes, fmt.Sprintf("%s: встреча %s добавлена в журнал (%s)", res.name, d.Format("02.01.2006"), m.Why))
	}
	list, err := a.residents()
	if err != nil {
		return nil, nil, err
	}
	log, err := a.logDays()
	if err != nil {
		return nil, nil, err
	}
	today := a.day()
	var out []MeetChange
	for i := range list {
		x := &list[i]
		if x.archive || x.former {
			continue
		}
		var joined *time.Time
		if err := tx.QueryRow(ctx, `SELECT joined_at FROM club_residents WHERE id = $1`, x.id).Scan(&joined); err != nil {
			return nil, nil, err
		}
		ch := MeetChange{Resident: x.name, Before: x.done, Granted: x.granted}
		from, adj := today.Format("2006-01-02"), int64(0)
		year := x.granted >= 36 || tariffMonths(x.tariff) == 12
		if year && joined != nil {
			start := *joined
			for start.AddDate(1, 0, 0).Before(today) || start.AddDate(1, 0, 0).Equal(today) {
				start = start.AddDate(1, 0, 0)
			}
			s := start.Format("2006-01-02")
			days := meetDays(log, x, s)
			switch {
			case s >= logStart:
				from, adj = s, 0
				ch.Why = "годовой пакет с " + start.Format("02.01.2006") + ": все встречи из журнала"
			case int64(len(meetDays(log, x, logStart))) > x.done:
				from, adj = logStart, 0
				ch.Why = "годовой пакет с " + start.Format("02.01.2006") + ", журнал полный с 27.05.2026: встречи из журнала (счётчик таблицы стирался при оплатах)"
				days = meetDays(log, x, logStart)
			default:
				ch.Why = "годовой пакет до начала журнала: счётчик сохранён"
				days = nil
			}
			if days != nil {
				for _, d := range days {
					t, _ := time.Parse("2006-01-02", d)
					ch.Days = append(ch.Days, t.Format("02.01"))
				}
			}
		}
		if ch.Why == "" || ch.Days == nil {
			// keep the counter: it counts from today on, the log adds what follows
			adj = x.done - int64(len(meetDays(log, x, from)))
			if ch.Why == "" {
				ch.Why = "счётчик сохранён, дальше считает журнал"
			}
		}
		if err := a.update("club_residents", x.id, `package_from = $2, meetings_adjust = $3`, from, adj); err != nil {
			return nil, nil, err
		}
		after, err := a.countDone(x)
		if err != nil {
			return nil, nil, err
		}
		ch.After, ch.Adjust, ch.From = after, adj, from
		out = append(out, ch)
	}
	if dry {
		return out, notes, nil
	}
	if _, err := tx.Exec(ctx, `INSERT INTO club_meta (key, value) VALUES ('r69_meet', $1)
		ON CONFLICT (key) DO UPDATE SET value = EXCLUDED.value`, "done:"+time.Now().UTC().Format(time.RFC3339)); err != nil {
		return nil, nil, err
	}
	return out, notes, tx.Commit(ctx)
}

// MeetBackfillDone: the reconstruction has been applied.
func (r *ClubRepo) MeetBackfillDone(ctx context.Context) bool {
	var v string
	_ = r.db.Pool.QueryRow(ctx, `SELECT value FROM club_meta WHERE key = 'r69_meet'`).Scan(&v)
	return strings.HasPrefix(v, "done:")
}
