package pg

import (
	"context"
	"encoding/json"
	"time"

	"github.com/bnursik/business_surgery_backend/internal/club"
)

// AuditInput reads what the data audit needs: the club tables now, the
// journal of app and platform actions (those the script did not refuse), and
// the debet sheet of every import kept.
func (r *ClubRepo) AuditInput(ctx context.Context, now time.Time) (club.AuditInput, error) {
	in := club.AuditInput{Now: now}
	s, err := r.LoadBundle(ctx, now)
	if err != nil {
		return in, err
	}
	in.Residents, in.Payments, in.MeetingLog, in.DebetRaw = s.Residents, s.Payments, s.MeetingLog, s.Raw[club.SheetDebet]
	rows, err := r.db.Pool.Query(ctx, `SELECT at, action, who, params FROM club_ops WHERE ok ORDER BY at, id`)
	if err != nil {
		return in, err
	}
	for rows.Next() {
		var o club.AuditOp
		var p []byte
		if err := rows.Scan(&o.At, &o.Action, &o.By, &p); err != nil {
			rows.Close()
			return in, err
		}
		_ = json.Unmarshal(p, &o.Params)
		if o.Params == nil {
			o.Params = map[string]string{}
		}
		in.Ops = append(in.Ops, o)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return in, err
	}
	rows, err = r.db.Pool.Query(ctx, `SELECT at, raw->'sheets'->$1 FROM club_imports
		WHERE NOT dry_run AND raw->'sheets' ? $1 ORDER BY at`, club.SheetDebet)
	if err != nil {
		return in, err
	}
	defer rows.Close()
	for rows.Next() {
		var at time.Time
		var raw []byte
		if err := rows.Scan(&at, &raw); err != nil {
			return in, err
		}
		var grid [][]string
		if json.Unmarshal(raw, &grid) != nil {
			continue
		}
		res, err := club.ParseDebetRows(grid)
		if err != nil {
			continue
		}
		in.History = append(in.History, club.AuditState{At: at, Residents: res})
	}
	return in, rows.Err()
}

// AuditMarks: when the club data last changed (an import or a write), so
// the audit is recomputed only then.
func (r *ClubRepo) AuditMarks(ctx context.Context) (string, error) {
	var imp, w *time.Time
	if err := r.db.Pool.QueryRow(ctx, `SELECT (SELECT max(at) FROM club_imports WHERE NOT dry_run), (SELECT max(at) FROM club_ops)`).Scan(&imp, &w); err != nil {
		return "", err
	}
	f := func(t *time.Time) string {
		if t == nil {
			return "-"
		}
		return t.UTC().Format(time.RFC3339Nano)
	}
	return f(imp) + "|" + f(w), nil
}
