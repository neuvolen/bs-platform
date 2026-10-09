package pg

import (
	"context"
	"errors"
	"time"

	"github.com/bnursik/business_surgery_backend/internal/gcal"
	"github.com/jackc/pgx/v5"
)

// GcalUserRepo (R71): the per-person Google Calendar connections, their
// cache of events and the log (migration 0026); also the work calendar
// (bs_mycal) the sync reads and writes.
type GcalUserRepo struct {
	db   *DB
	docs *PlatformRepo
}

func NewGcalUserRepo(db *DB, docs *PlatformRepo) *GcalUserRepo {
	return &GcalUserRepo{db: db, docs: docs}
}

const gcalConnCols = `scope, email, refresh_enc, granted, read_cal, write_cal, write_name, own_cal, sync_read, sync_write,
	COALESCE(last_sync, 'epoch'::timestamptz), last_error, need_consent, connected_at`

func scanConn(row pgx.Row) (*gcal.UserConn, error) {
	var c gcal.UserConn
	err := row.Scan(&c.Scope, &c.Email, &c.RefreshEnc, &c.Granted, &c.ReadCal, &c.WriteCal, &c.WriteName, &c.OwnCal,
		&c.SyncRead, &c.SyncWrite, &c.LastSync, &c.Error, &c.NeedConsent, &c.ConnectedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if c.LastSync.Unix() == 0 {
		c.LastSync = time.Time{}
	}
	return &c, nil
}

func (r *GcalUserRepo) GcalConns(ctx context.Context) ([]gcal.UserConn, error) {
	rows, err := r.db.Pool.Query(ctx, `SELECT `+gcalConnCols+` FROM gcal_users ORDER BY scope`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []gcal.UserConn
	for rows.Next() {
		c, err := scanConn(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *c)
	}
	return out, rows.Err()
}

func (r *GcalUserRepo) GcalConn(ctx context.Context, scope string) (*gcal.UserConn, error) {
	return scanConn(r.db.Pool.QueryRow(ctx, `SELECT `+gcalConnCols+` FROM gcal_users WHERE scope = $1`, scope))
}

func (r *GcalUserRepo) GcalSaveConn(ctx context.Context, c gcal.UserConn) error {
	var last any
	if !c.LastSync.IsZero() {
		last = c.LastSync
	}
	if c.ConnectedAt.IsZero() {
		c.ConnectedAt = time.Now()
	}
	_, err := r.db.Pool.Exec(ctx, `INSERT INTO gcal_users (scope, email, refresh_enc, granted, read_cal, write_cal, write_name, own_cal,
			sync_read, sync_write, last_sync, last_error, need_consent, connected_at, updated_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14, now())
		ON CONFLICT (scope) DO UPDATE SET email = EXCLUDED.email, refresh_enc = EXCLUDED.refresh_enc, granted = EXCLUDED.granted,
			read_cal = EXCLUDED.read_cal, write_cal = EXCLUDED.write_cal, write_name = EXCLUDED.write_name, own_cal = EXCLUDED.own_cal,
			sync_read = EXCLUDED.sync_read, sync_write = EXCLUDED.sync_write, last_sync = EXCLUDED.last_sync,
			last_error = EXCLUDED.last_error, need_consent = EXCLUDED.need_consent, connected_at = EXCLUDED.connected_at, updated_at = now()`,
		c.Scope, c.Email, c.RefreshEnc, c.Granted, c.ReadCal, c.WriteCal, c.WriteName, c.OwnCal,
		c.SyncRead, c.SyncWrite, last, c.Error, c.NeedConsent, c.ConnectedAt)
	return err
}

// GcalDeleteConn forgets the connection, its cache of Google events and the
// sync journal (it names the person's blocks and Google account): after
// «Отключить» nothing from Google stays on the server (privacy policy, /privacy).
func (r *GcalUserRepo) GcalDeleteConn(ctx context.Context, scope string) error {
	if _, err := r.db.Pool.Exec(ctx, `DELETE FROM gcal_users WHERE scope = $1`, scope); err != nil {
		return err
	}
	_, err := r.db.Pool.Exec(ctx, `DELETE FROM gcal_user_log WHERE scope = $1`, scope)
	return err
}

func (r *GcalUserRepo) GcalEvents(ctx context.Context, scope string) ([]gcal.UserEvent, error) {
	rows, err := r.db.Pool.Query(ctx, `SELECT cal, event_id, ours, sig, summary, starts, ends, all_day, g_updated
		FROM gcal_user_events WHERE scope = $1 ORDER BY starts`, scope)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []gcal.UserEvent
	for rows.Next() {
		var e gcal.UserEvent
		if err := rows.Scan(&e.Cal, &e.ID, &e.Ours, &e.Sig, &e.Summary, &e.Start, &e.End, &e.AllDay, &e.Updated); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

func (r *GcalUserRepo) GcalPutEvent(ctx context.Context, scope string, e gcal.UserEvent) error {
	_, err := r.db.Pool.Exec(ctx, `INSERT INTO gcal_user_events (scope, cal, event_id, ours, sig, summary, starts, ends, all_day, g_updated)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)
		ON CONFLICT (scope, cal, event_id) DO UPDATE SET ours = EXCLUDED.ours, sig = EXCLUDED.sig, summary = EXCLUDED.summary,
			starts = EXCLUDED.starts, ends = EXCLUDED.ends, all_day = EXCLUDED.all_day, g_updated = EXCLUDED.g_updated`,
		scope, e.Cal, e.ID, e.Ours, e.Sig, e.Summary, e.Start, e.End, e.AllDay, e.Updated)
	return err
}

func (r *GcalUserRepo) GcalDelEvent(ctx context.Context, scope, cal, id string) error {
	_, err := r.db.Pool.Exec(ctx, `DELETE FROM gcal_user_events WHERE scope = $1 AND cal = $2 AND event_id = $3`, scope, cal, id)
	return err
}

// GcalDelCal drops the cached events of a calendar: ours (true) or the others.
func (r *GcalUserRepo) GcalDelCal(ctx context.Context, scope, cal string, ours bool) error {
	_, err := r.db.Pool.Exec(ctx, `DELETE FROM gcal_user_events WHERE scope = $1 AND cal = $2 AND ours = $3`, scope, cal, ours)
	return err
}

func (r *GcalUserRepo) GcalLog(ctx context.Context, scope, kind, text string) error {
	if len(text) > 2000 {
		text = text[:2000]
	}
	_, err := r.db.Pool.Exec(ctx, `INSERT INTO gcal_user_log (scope, kind, text) VALUES ($1, $2, $3)`, scope, kind, text)
	return err
}

// LoadCal: the work calendar of the scope ({"slots":{}} when there is none).
func (r *GcalUserRepo) LoadCal(ctx context.Context, scope string) (string, int, time.Time, error) {
	d, err := r.docs.GetDoc(ctx, scope, "bs_mycal")
	if err != nil {
		return "", 0, time.Time{}, err
	}
	if d == nil || d.Deleted {
		v := 0
		if d != nil {
			v = d.Version
		}
		return `{"slots":{},"notes":{}}`, v, time.Time{}, nil
	}
	return d.Value, d.Version, d.UpdatedAt, nil
}

func (r *GcalUserRepo) SaveCal(ctx context.Context, scope string, version int, value string) error {
	_, err := r.docs.PutDoc(ctx, scope, "bs_mycal", version, value, false, "server:gcal")
	if errors.Is(err, ErrPlatformConflict) {
		return gcal.ErrCalConflict
	}
	return err
}
