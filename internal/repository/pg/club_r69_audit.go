package pg

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/bnursik/business_surgery_backend/internal/club"
)

// R69 audit: read only. What the logs need to reconstruct meetings and
// payments: per resident the stored counter, the meetings log, the schedule
// rows, the sheet's last counter before the cutover and the writes that
// touched meetings; the incomes since the cutover and the fines. Only names,
// dates, amounts and counts.

func r69Money(n int64) string { return club.FmtMoney(n) }

// R69Audit returns the log lines.
func (r *ClubRepo) R69Audit(ctx context.Context) ([]string, error) {
	var out []string
	add := func(f string, a ...any) { out = append(out, fmt.Sprintf(f, a...)) }
	var cut string
	_ = r.db.Pool.QueryRow(ctx, `SELECT value FROM club_meta WHERE key = 'sheet_cutover'`).Scan(&cut)
	add("cutover %q", cut)

	// The sheet's counters in the last real import (before the cutover).
	base := map[string]string{}
	var impAt time.Time
	var raw []byte
	if err := r.db.Pool.QueryRow(ctx, `SELECT at, raw FROM club_imports WHERE NOT dry_run ORDER BY id DESC LIMIT 1`).Scan(&impAt, &raw); err == nil {
		var w struct {
			Sheets club.Sheets `json:"sheets"`
		}
		if json.Unmarshal(raw, &w) == nil && w.Sheets != nil {
			if snap, _, err := club.Parse(w.Sheets); err == nil && snap != nil {
				for _, x := range snap.Residents {
					base[club.NormName(x.Name)] = fmt.Sprintf("%d/%d", x.Done, x.Granted)
				}
			}
			add("last import %s: %d residents", impAt.In(club.Almaty).Format("02.01.2006 15:04"), len(base))
		}
	}

	logs := map[string][]string{}
	rows, err := r.db.Pool.Query(ctx, `SELECT resident, date FROM club_meeting_log ORDER BY date, id`)
	if err != nil {
		return out, err
	}
	for rows.Next() {
		var n string
		var d time.Time
		if err := rows.Scan(&n, &d); err != nil {
			rows.Close()
			return out, err
		}
		k := club.NormName(n)
		logs[k] = append(logs[k], d.Format("02.01.06"))
	}
	rows.Close()

	sched := map[string][]string{}
	rows, err = r.db.Pool.Query(ctx, `SELECT resident, date, done FROM club_meetings WHERE date >= '2026-05-01' ORDER BY date, id`)
	if err != nil {
		return out, err
	}
	for rows.Next() {
		var n string
		var d time.Time
		var done bool
		if err := rows.Scan(&n, &d, &done); err != nil {
			rows.Close()
			return out, err
		}
		m := "?"
		if done {
			m = "+"
		}
		k := club.NormName(n)
		sched[k] = append(sched[k], d.Format("02.01")+m)
	}
	rows.Close()

	rows, err = r.db.Pool.Query(ctx, `SELECT name, format, tariff, meetings_granted, meetings_done, rest_entry, renew_debt, joined_at, former
		FROM club_residents WHERE NOT archived ORDER BY id`)
	if err != nil {
		return out, err
	}
	for rows.Next() {
		var n, f string
		var t, g, d, re, rd int64
		var j *time.Time
		var former bool
		if err := rows.Scan(&n, &f, &t, &g, &d, &re, &rd, &j, &former); err != nil {
			rows.Close()
			return out, err
		}
		js := ""
		if j != nil {
			js = j.Format("02.01.06")
		}
		k := club.NormName(n)
		lg := logs[k]
		if len(lg) > 40 {
			lg = lg[len(lg)-40:]
		}
		add("res «%s» %s former=%v tariff %s meet %d/%d (sheet %s) rest %s renew %s joined %s | log %d: %s | sched: %s",
			n, f, former, r69Money(t), d, g, base[k], r69Money(re), r69Money(rd), js, len(logs[k]), strings.Join(lg, " "), strings.Join(sched[k], " "))
	}
	rows.Close()

	rows, err = r.db.Pool.Query(ctx, `SELECT at, source, action, params, applied, apply_error FROM club_writes
		WHERE action IN ('setMeetings','renewMeetings','confirmMeeting','markAttendance','addPayment','setResidentField','updateFine','deleteFine','addFine')
		AND at >= now() - interval '40 days' ORDER BY id`)
	if err != nil {
		return out, err
	}
	for rows.Next() {
		var at time.Time
		var src, act, aerr string
		var p []byte
		var applied bool
		if err := rows.Scan(&at, &src, &act, &p, &applied, &aerr); err != nil {
			rows.Close()
			return out, err
		}
		var m map[string]string
		_ = json.Unmarshal(p, &m)
		var keys []string
		for k, v := range m {
			switch k {
			case "name", "res", "names", "resident", "done", "granted", "amount", "src", "type", "date", "field", "value", "status", "kind":
				if len([]rune(v)) > 40 {
					v = string([]rune(v)[:40])
				}
				keys = append(keys, k+"="+v)
			}
		}
		sort.Strings(keys)
		add("write %s %s %s applied=%v %s %s", at.In(club.Almaty).Format("02.01 15:04"), src, act, applied, aerr, strings.Join(keys, " "))
	}
	rows.Close()

	rows, err = r.db.Pool.Query(ctx, `SELECT id, date, income, income_cat, resident, applied, source FROM club_payments
		WHERE income > 0 AND date >= '2026-09-20' ORDER BY date, id`)
	if err != nil {
		return out, err
	}
	for rows.Next() {
		var id, inc int64
		var d time.Time
		var cat, res, src string
		var ap bool
		if err := rows.Scan(&id, &d, &inc, &cat, &res, &ap, &src); err != nil {
			rows.Close()
			return out, err
		}
		add("pay #%d %s «%s» %s «%s» applied=%v %s", id, d.Format("02.01.06"), res, r69Money(inc), cat, ap, src)
	}
	rows.Close()

	rows, err = r.db.Pool.Query(ctx, `SELECT id, resident, type, amount, date, paid, status FROM club_fines
		WHERE NOT paid OR date >= '2026-09-01' ORDER BY date, id`)
	if err != nil {
		return out, err
	}
	for rows.Next() {
		var id, a int64
		var n, t, st string
		var d *time.Time
		var paid bool
		if err := rows.Scan(&id, &n, &t, &a, &d, &paid, &st); err != nil {
			rows.Close()
			return out, err
		}
		ds := ""
		if d != nil {
			ds = d.Format("02.01.06")
		}
		add("fine #%d %s «%s» %s «%s» paid=%v «%s»", id, ds, n, r69Money(a), t, paid, st)
	}
	rows.Close()
	return out, nil
}
