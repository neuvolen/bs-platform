package pg

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/bnursik/business_surgery_backend/internal/club"
	"github.com/jackc/pgx/v5"
)

// R32d: the server is the source of truth; the sheet can only give it rows
// it does not have (club.Reconcile picks them). MergeFromSheet adds them in
// one transaction and changes nothing the server already keeps.

type forceKey struct{}

// WithForceReplace lets ReplaceAllThen replace the club tables although the
// server is the master: an admin's emergency import from the sheet only.
func WithForceReplace(ctx context.Context) context.Context {
	return context.WithValue(ctx, forceKey{}, true)
}

func forcedReplace(ctx context.Context) bool {
	v, _ := ctx.Value(forceKey{}).(bool)
	return v
}

// MergeFromSheet inserts what the sheet alone has; it returns rows added.
func (r *ClubRepo) MergeFromSheet(ctx context.Context, a *club.RecAdd, by string) (int, error) {
	if a.Empty() {
		return 0, nil
	}
	tx, err := r.db.Pool.Begin(ctx)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	n := 0
	date := func(t *time.Time) any {
		if t == nil || t.IsZero() {
			return nil
		}
		return t.Format("2006-01-02")
	}
	day := func(t time.Time) any {
		if t.IsZero() {
			return nil
		}
		return t.Format("2006-01-02")
	}
	nullID := func(v int64) any {
		if v <= 0 {
			return nil
		}
		return v
	}
	b := &pgx.Batch{}
	for _, p := range a.Residents {
		b.Queue(`INSERT INTO club_residents (name, tg_id, format, tariff, meetings_granted, meetings_done,
			paid_entry, rest_entry, renew_debt, former, exception, admin, source, joined_at, left_at, months, note, partner, updated_by, archived)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19,$20)`,
			p.Name, nullID(p.TgID), p.Format, p.Tariff, p.Granted, p.Done, p.PaidEntry, p.RestEntry, p.RenewDebt,
			p.Former, p.Exception, p.Admin, p.Source, date(p.JoinedAt), date(p.LeftAt), p.Months, p.Note, p.Partner, by, p.Archived)
	}
	for _, p := range a.Payments {
		b.Queue(`INSERT INTO club_payments (sheet_row, date, income, expense, income_cat, resident, expense_cat, applied, source, created_by)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8,'sheet',$9)`,
			p.Row, day(p.Date), p.Income, p.Expense, p.IncomeCat, p.Resident, p.ExpenseCat, p.Applied, by)
	}
	for _, f := range a.Fines {
		b.Queue(`INSERT INTO club_fines (resident, type, amount, date, paid, created_by, status) VALUES ($1,$2,$3,$4,$5,$6,$7)`,
			f.Name, f.Type, f.Amount, day(f.Date), f.Paid, by, f.Status)
	}
	for _, m := range a.Meetings {
		b.Queue(`INSERT INTO club_meetings (resident, date, time, place, link, online, done, event_id, addr_cell, link_cell, h_cell)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11)`,
			m.Resident, day(m.Date), m.Time, m.Place, m.Link, m.Online, m.Done, m.EventID, m.AddrCell, m.LinkCell, m.HCell)
	}
	for _, e := range a.Reports {
		b.Queue(`INSERT INTO club_reports (at, username, name, text, tg_user_id, thread, late, shown_at) VALUES ($1,$2,$3,$4,$5,$6,$7,$8)`,
			e.At, e.Username, e.Name, e.Text, nullID(e.TgUserID), e.Thread, e.Late, e.ShownAt)
	}
	for _, e := range a.MeetingLog {
		b.Queue(`INSERT INTO club_meeting_log (date, resident, time_cell) VALUES ($1,$2,$3)`, day(e.Date), e.Resident, e.Time)
	}
	for _, st := range a.Settings {
		b.Queue(`INSERT INTO club_settings (key, value) VALUES ($1,$2) ON CONFLICT (key) DO NOTHING`, st.Key, st.Value)
	}
	n = b.Len()
	for name, rows := range a.NewSheets {
		raw, err := json.Marshal(rows)
		if err != nil {
			return 0, err
		}
		b.Queue(`INSERT INTO club_sheets (name, rows) VALUES ($1,$2) ON CONFLICT (name) DO NOTHING`, name, raw)
		n += len(rows) - 1
	}
	br := tx.SendBatch(ctx, b)
	for i := 0; i < b.Len(); i++ {
		if _, err := br.Exec(); err != nil {
			br.Close()
			return 0, err
		}
	}
	if err := br.Close(); err != nil {
		return 0, err
	}
	for name, add := range a.AppendRows {
		if len(add) == 0 {
			continue
		}
		var raw []byte
		var rows [][]string
		err := tx.QueryRow(ctx, `SELECT rows FROM club_sheets WHERE name = $1 FOR UPDATE`, name).Scan(&raw)
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return 0, err
		}
		if len(raw) > 0 {
			if err := json.Unmarshal(raw, &rows); err != nil {
				return 0, err
			}
		}
		// after the last filled row, as the sheet appends
		last := len(rows)
		for last > 0 && blankCells(rows[last-1]) {
			last--
		}
		rows = append(rows[:last], add...)
		out, err := json.Marshal(rows)
		if err != nil {
			return 0, err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO club_sheets (name, rows) VALUES ($1,$2)
			ON CONFLICT (name) DO UPDATE SET rows = EXCLUDED.rows, at = now()`, name, out); err != nil {
			return 0, err
		}
		n += len(add)
	}
	if err := tx.Commit(ctx); err != nil {
		return 0, err
	}
	return n, nil
}

func blankCells(r []string) bool {
	for _, c := range r {
		if c != "" {
			return false
		}
	}
	return true
}

// AllSettings reads the bot texts («Настройки») the server keeps.
func (r *ClubRepo) AllSettings(ctx context.Context) ([]club.Setting, error) {
	rows, err := r.db.Pool.Query(ctx, `SELECT key, value FROM club_settings ORDER BY key`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []club.Setting
	for rows.Next() {
		var s club.Setting
		if err := rows.Scan(&s.Key, &s.Value); err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}
