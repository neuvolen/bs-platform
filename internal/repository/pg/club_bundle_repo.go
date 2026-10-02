package pg

import (
	"context"
	"encoding/json"
	"errors"
	"sort"
	"time"

	"github.com/bnursik/business_surgery_backend/internal/club"
	"github.com/jackc/pgx/v5"
)

// LoadBundle reads what the app's bundle is built from: the club tables, the
// reports of the last month, «Лог встреч» and the sheets kept as displayed.
func (r *ClubRepo) LoadBundle(ctx context.Context, since time.Time) (*club.Snapshot, error) {
	s, err := r.Load(ctx)
	if err != nil {
		return nil, err
	}
	// The bundle keeps the sheet's own order of the schedule.
	sort.SliceStable(s.Meetings, func(i, j int) bool {
		a, b := s.Meetings[i].Row, s.Meetings[j].Row
		if a == 0 || b == 0 {
			return a != 0 && b == 0
		}
		return a < b
	})
	rows, err := r.db.Pool.Query(ctx, `SELECT at, username, name, text, COALESCE(tg_user_id,0), thread, late, shown_at
		FROM club_reports WHERE COALESCE(shown_at, at) >= $1 ORDER BY id`, since)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var e club.ReportEntry
		if err := rows.Scan(&e.At, &e.Username, &e.Name, &e.Text, &e.TgUserID, &e.Thread, &e.Late, &e.ShownAt); err != nil {
			rows.Close()
			return nil, err
		}
		s.Reports = append(s.Reports, e)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	rows, err = r.db.Pool.Query(ctx, `SELECT date, resident, time_cell FROM club_meeting_log ORDER BY id`)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var e club.MeetingLogEntry
		var d time.Time
		if err := rows.Scan(&d, &e.Resident, &e.Time); err != nil {
			rows.Close()
			return nil, err
		}
		e.Date = time.Date(d.Year(), d.Month(), d.Day(), 0, 0, 0, 0, club.Almaty)
		s.MeetingLog = append(s.MeetingLog, e)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	s.Raw, err = r.Sheets(ctx)
	return s, err
}

// Sheets returns the sheets kept as displayed.
func (r *ClubRepo) Sheets(ctx context.Context) (club.Sheets, error) {
	rows, err := r.db.Pool.Query(ctx, `SELECT name, rows FROM club_sheets`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := club.Sheets{}
	for rows.Next() {
		var name string
		var raw []byte
		if err := rows.Scan(&name, &raw); err != nil {
			return nil, err
		}
		var grid [][]string
		if err := json.Unmarshal(raw, &grid); err != nil {
			return nil, err
		}
		out[name] = grid
	}
	return out, rows.Err()
}

// LastImportAt is when the sheet's data last came over (nil: never).
func (r *ClubRepo) LastImportAt(ctx context.Context) (*time.Time, error) {
	var t *time.Time
	err := r.db.Pool.QueryRow(ctx, `SELECT max(at) FROM club_imports WHERE NOT dry_run`).Scan(&t)
	return t, err
}

// LastWriteAt is when club data was last changed from the app or the
// platform (nil: never).
func (r *ClubRepo) LastWriteAt(ctx context.Context) (*time.Time, error) {
	var t *time.Time
	err := r.db.Pool.QueryRow(ctx, `SELECT max(at) FROM club_writes WHERE status <> 'rejected'`).Scan(&t)
	return t, err
}

// BundleCheck is one day's comparison of the app's bundle.
type BundleCheck struct {
	Day    time.Time       `json:"day"`
	At     time.Time       `json:"at"`
	OK     bool            `json:"ok"`
	Result json.RawMessage `json:"result"`
}

// SaveBundleCheck keeps the day's comparison; a later one the same day replaces it.
func (r *ClubRepo) SaveBundleCheck(ctx context.Context, day time.Time, ok bool, result any) error {
	b, err := json.Marshal(result)
	if err != nil {
		return err
	}
	_, err = r.db.Pool.Exec(ctx, `INSERT INTO club_bundle_checks (day, ok, result) VALUES ($1,$2,$3)
		ON CONFLICT (day) DO UPDATE SET at = now(), ok = EXCLUDED.ok, result = EXCLUDED.result`,
		day.Format("2006-01-02"), ok, b)
	return err
}

// BundleChecks: the last n days compared, newest first.
func (r *ClubRepo) BundleChecks(ctx context.Context, n int) ([]BundleCheck, error) {
	rows, err := r.db.Pool.Query(ctx, `SELECT day, at, ok, result FROM club_bundle_checks ORDER BY day DESC LIMIT $1`, n)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []BundleCheck
	for rows.Next() {
		var c BundleCheck
		if err := rows.Scan(&c.Day, &c.At, &c.OK, &c.Result); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// BundleCheckOn is the comparison of day, if there is one.
func (r *ClubRepo) BundleCheckOn(ctx context.Context, day time.Time) (*BundleCheck, error) {
	var c BundleCheck
	err := r.db.Pool.QueryRow(ctx, `SELECT day, at, ok, result FROM club_bundle_checks WHERE day = $1`, day.Format("2006-01-02")).
		Scan(&c.Day, &c.At, &c.OK, &c.Result)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &c, nil
}
