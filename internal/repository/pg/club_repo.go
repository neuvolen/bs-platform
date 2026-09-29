package pg

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/bnursik/business_surgery_backend/internal/club"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// ClubRepo stores the club data that used to live in the Google Sheet.
type ClubRepo struct{ db *DB }

func NewClubRepo(db *DB) *ClubRepo { return &ClubRepo{db: db} }

// ErrServerIsMaster: the server already owns the data; the sheet may no
// longer overwrite it.
var ErrServerIsMaster = errors.New("club: server is the source of truth, sheet import refused")

func (r *ClubRepo) Master(ctx context.Context) (string, error) {
	var v string
	err := r.db.Pool.QueryRow(ctx, `SELECT value FROM club_meta WHERE key = 'master'`).Scan(&v)
	if errors.Is(err, pgx.ErrNoRows) {
		return "sheet", nil
	}
	return v, err
}

// ClubCounts is how much is stored.
type ClubCounts struct {
	Residents  int        `json:"residents"`
	Former     int        `json:"former"`
	Payments   int        `json:"payments"`
	Fines      int        `json:"fines"`
	Meetings   int        `json:"meetings"`
	Reports    int        `json:"reports"`
	MeetingLog int        `json:"meetingLog"`
	Settings   int        `json:"settings"`
	LastImport *time.Time `json:"lastImport,omitempty"`
}

func (r *ClubRepo) Counts(ctx context.Context) (ClubCounts, error) {
	var c ClubCounts
	err := r.db.Pool.QueryRow(ctx, `
		SELECT (SELECT count(*) FROM club_residents WHERE NOT former),
		       (SELECT count(*) FROM club_residents WHERE former),
		       (SELECT count(*) FROM club_payments), (SELECT count(*) FROM club_fines),
		       (SELECT count(*) FROM club_meetings), (SELECT count(*) FROM club_reports),
		       (SELECT count(*) FROM club_meeting_log), (SELECT count(*) FROM club_settings),
		       (SELECT max(at) FROM club_imports WHERE NOT dry_run)`).
		Scan(&c.Residents, &c.Former, &c.Payments, &c.Fines, &c.Meetings, &c.Reports, &c.MeetingLog, &c.Settings, &c.LastImport)
	return c, err
}

// LogImport keeps the raw sheets and the report of an import attempt.
func (r *ClubRepo) LogImport(ctx context.Context, by string, dry bool, raw []byte, report any) error {
	rep, err := json.Marshal(report)
	if err != nil {
		return err
	}
	_, err = r.db.Pool.Exec(ctx, `INSERT INTO club_imports (by, dry_run, raw, report) VALUES ($1, $2, $3, $4)`, by, dry, raw, rep)
	if err == nil {
		// Keep the last 30 imports: enough to go back, not enough to bloat.
		_, _ = r.db.Pool.Exec(ctx, `DELETE FROM club_imports WHERE id NOT IN (SELECT id FROM club_imports ORDER BY id DESC LIMIT 30)`)
	}
	return err
}

// ReplaceAll puts a whole sheet snapshot in place, in one transaction.
func (r *ClubRepo) ReplaceAll(ctx context.Context, s *club.Snapshot, by string) error {
	tx, err := r.db.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	var master string
	if err := tx.QueryRow(ctx, `SELECT value FROM club_meta WHERE key = 'master' FOR UPDATE`).Scan(&master); err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return err
	}
	if master == "server" {
		return ErrServerIsMaster
	}
	if _, err := tx.Exec(ctx, `TRUNCATE club_residents, club_payments, club_fines, club_meetings,
		club_reports, club_meeting_log, club_settings, club_pl_rows RESTART IDENTITY`); err != nil {
		return err
	}

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
	for _, p := range s.Residents {
		b.Queue(`INSERT INTO club_residents (name, tg_id, format, tariff, meetings_granted, meetings_done,
			paid_entry, rest_entry, renew_debt, former, exception, admin, source, joined_at, left_at, months, note, partner, updated_by)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19)`,
			p.Name, nullID(p.TgID), p.Format, p.Tariff, p.Granted, p.Done, p.PaidEntry, p.RestEntry, p.RenewDebt,
			p.Former, p.Exception, p.Admin, p.Source, date(p.JoinedAt), date(p.LeftAt), p.Months, p.Note, p.Partner, by)
	}
	for _, p := range s.Payments {
		b.Queue(`INSERT INTO club_payments (sheet_row, date, income, expense, income_cat, resident, expense_cat, applied, source, created_by)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8,'sheet',$9)`,
			p.Row, day(p.Date), p.Income, p.Expense, p.IncomeCat, p.Resident, p.ExpenseCat, p.Applied, by)
	}
	for _, f := range s.Fines {
		b.Queue(`INSERT INTO club_fines (resident, type, amount, date, paid, created_by) VALUES ($1,$2,$3,$4,$5,$6)`,
			f.Name, f.Type, f.Amount, day(f.Date), f.Paid, by)
	}
	for _, m := range s.Meetings {
		b.Queue(`INSERT INTO club_meetings (resident, date, time, place, link, online, sent_3d, sent_1d, sent_1h, done, event_id)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11)`,
			m.Resident, day(m.Date), m.Time, m.Place, m.Link, m.Online, m.Sent3d, m.Sent1d, m.Sent1h, m.Done, m.EventID)
	}
	for _, e := range s.Reports {
		b.Queue(`INSERT INTO club_reports (at, username, name, text, tg_user_id, thread, late) VALUES ($1,$2,$3,$4,$5,$6,$7)`,
			e.At, e.Username, e.Name, e.Text, nullID(e.TgUserID), e.Thread, e.Late)
	}
	for _, e := range s.MeetingLog {
		b.Queue(`INSERT INTO club_meeting_log (date, resident) VALUES ($1,$2)`, day(e.Date), e.Resident)
	}
	for _, st := range s.Settings {
		b.Queue(`INSERT INTO club_settings (key, value) VALUES ($1,$2) ON CONFLICT (key) DO UPDATE SET value = EXCLUDED.value, updated_at = now()`, st.Key, st.Value)
	}
	if s.PL != nil {
		for i, row := range s.PL.Rows {
			b.Queue(`INSERT INTO club_pl_rows (position, name, section) VALUES ($1,$2,$3)`, i, row.Name, row.Section)
		}
		b.Queue(`INSERT INTO club_meta (key, value) VALUES ('pl_year', $1) ON CONFLICT (key) DO UPDATE SET value = EXCLUDED.value`,
			jsonInt(s.PL.Year))
	}
	br := tx.SendBatch(ctx, b)
	for i := 0; i < b.Len(); i++ {
		if _, err := br.Exec(); err != nil {
			br.Close()
			return err
		}
	}
	if err := br.Close(); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func jsonInt(v int) string { b, _ := json.Marshal(v); return string(b) }

// Load reads everything back as a snapshot, to compute debts and the P&L.
func (r *ClubRepo) Load(ctx context.Context) (*club.Snapshot, error) {
	s := &club.Snapshot{}
	loc := club.Almaty
	d2t := func(t *time.Time) *time.Time {
		if t == nil {
			return nil
		}
		v := time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, loc)
		return &v
	}

	rows, err := r.db.Pool.Query(ctx, `SELECT name, COALESCE(tg_id,0), format, tariff, meetings_granted, meetings_done,
		paid_entry, rest_entry, renew_debt, former, exception, admin, source, joined_at, left_at, months, note, partner
		FROM club_residents ORDER BY id`)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var p club.Resident
		var j, l *time.Time
		if err := rows.Scan(&p.Name, &p.TgID, &p.Format, &p.Tariff, &p.Granted, &p.Done, &p.PaidEntry, &p.RestEntry,
			&p.RenewDebt, &p.Former, &p.Exception, &p.Admin, &p.Source, &j, &l, &p.Months, &p.Note, &p.Partner); err != nil {
			rows.Close()
			return nil, err
		}
		p.JoinedAt, p.LeftAt = d2t(j), d2t(l)
		s.Residents = append(s.Residents, p)
	}
	rows.Close()

	rows, err = r.db.Pool.Query(ctx, `SELECT COALESCE(sheet_row,0), date, income, expense, income_cat, resident, expense_cat, applied
		FROM club_payments ORDER BY date, id`)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var p club.Payment
		var d time.Time
		if err := rows.Scan(&p.Row, &d, &p.Income, &p.Expense, &p.IncomeCat, &p.Resident, &p.ExpenseCat, &p.Applied); err != nil {
			rows.Close()
			return nil, err
		}
		p.Date = *d2t(&d)
		s.Payments = append(s.Payments, p)
	}
	rows.Close()

	rows, err = r.db.Pool.Query(ctx, `SELECT resident, type, amount, date, paid FROM club_fines ORDER BY id`)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var f club.Fine
		var d *time.Time
		if err := rows.Scan(&f.Name, &f.Type, &f.Amount, &d, &f.Paid); err != nil {
			rows.Close()
			return nil, err
		}
		if d != nil {
			f.Date = *d2t(d)
		}
		s.Fines = append(s.Fines, f)
	}
	rows.Close()

	rows, err = r.db.Pool.Query(ctx, `SELECT resident, date, time, place, link, online, sent_3d, sent_1d, sent_1h, done, event_id
		FROM club_meetings ORDER BY date, time, id`)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var m club.Meeting
		var d time.Time
		if err := rows.Scan(&m.Resident, &d, &m.Time, &m.Place, &m.Link, &m.Online, &m.Sent3d, &m.Sent1d, &m.Sent1h, &m.Done, &m.EventID); err != nil {
			rows.Close()
			return nil, err
		}
		m.Date = *d2t(&d)
		s.Meetings = append(s.Meetings, m)
	}
	rows.Close()

	rows, err = r.db.Pool.Query(ctx, `SELECT name, section FROM club_pl_rows ORDER BY position`)
	if err != nil {
		return nil, err
	}
	pl := &club.PLSheet{}
	for rows.Next() {
		var row club.PLRow
		if err := rows.Scan(&row.Name, &row.Section); err != nil {
			rows.Close()
			return nil, err
		}
		pl.Rows = append(pl.Rows, row)
	}
	rows.Close()
	var year string
	if err := r.db.Pool.QueryRow(ctx, `SELECT value FROM club_meta WHERE key = 'pl_year'`).Scan(&year); err == nil {
		_ = json.Unmarshal([]byte(year), &pl.Year)
	}
	if len(pl.Rows) > 0 {
		s.PL = pl
	}
	return s, nil
}

// DB gives direct access for small writes that mirror the sheet.
func (r *ClubRepo) DB() interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
} {
	return r.db.Pool
}

// DoneMeeting is a meeting that already happened: a row marked «Проведена»
// (Time is its time) or a line in «Лог встреч» (Time is "*"). Date is дд.мм.
type DoneMeeting struct {
	Resident string
	Date     string
	Time     string
}

// DoneMeetings lists finished meetings of the last week, from the last import.
func (r *ClubRepo) DoneMeetings(ctx context.Context) ([]DoneMeeting, error) {
	rows, err := r.db.Pool.Query(ctx, `
		SELECT resident, to_char(date, 'DD.MM'), time FROM club_meetings
		 WHERE done AND date >= current_date - 7
		UNION ALL
		SELECT resident, to_char(date, 'DD.MM'), '*' FROM club_meeting_log
		 WHERE date >= current_date - 7`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []DoneMeeting{}
	for rows.Next() {
		var d DoneMeeting
		if err := rows.Scan(&d.Resident, &d.Date, &d.Time); err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

// ClubTgIDs: Telegram ids of everyone in the club list (residents and team).
func (r *ClubRepo) ClubTgIDs(ctx context.Context) ([]int64, error) {
	rows, err := r.db.Pool.Query(ctx, `SELECT DISTINCT tg_id FROM club_residents WHERE tg_id IS NOT NULL AND tg_id > 0`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []int64{}
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}
