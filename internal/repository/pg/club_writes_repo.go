package pg

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/bnursik/business_surgery_backend/internal/club"
	"github.com/jackc/pgx/v5"
)

// ClubWrite is one change of club data on its way to the sheet.
type ClubWrite struct {
	ID         int64             `json:"id"`
	At         time.Time         `json:"at"`
	Source     string            `json:"source"`
	TgID       int64             `json:"tgId"`
	Who        string            `json:"who"`
	Action     string            `json:"action"`
	Params     map[string]string `json:"params"`
	Applied    bool              `json:"applied"`
	ApplyError string            `json:"applyError,omitempty"`
	Status     string            `json:"status"`
	MaybeSent  bool              `json:"maybeSent,omitempty"`
	Tries      int               `json:"tries"`
	NextTryAt  time.Time         `json:"nextTryAt"`
	TriedAt    *time.Time        `json:"triedAt,omitempty"`
	SentAt     *time.Time        `json:"sentAt,omitempty"`
	LastError  string            `json:"lastError,omitempty"`
	Result     string            `json:"result,omitempty"`
}

const (
	WritePending  = "pending"
	WriteUnknown  = "unknown"
	WriteSent     = "sent"
	WriteRejected = "rejected"
)

const writeCols = `id, at, source, tg_id, who, action, params, applied, apply_error, status, maybe_sent, tries, next_try_at,
	tried_at, sent_at, last_error, result`

func scanWrite(row pgx.Row) (*ClubWrite, error) {
	var w ClubWrite
	var p []byte
	if err := row.Scan(&w.ID, &w.At, &w.Source, &w.TgID, &w.Who, &w.Action, &p, &w.Applied, &w.ApplyError, &w.Status,
		&w.MaybeSent, &w.Tries, &w.NextTryAt, &w.TriedAt, &w.SentAt, &w.LastError, &w.Result); err != nil {
		return nil, err
	}
	_ = json.Unmarshal(p, &w.Params)
	return &w, nil
}

// applyIn runs the write's change inside a savepoint: a change that fails
// halfway leaves nothing behind.
func applyIn(ctx context.Context, tx pgx.Tx, w *ClubWrite) ([]undoStep, bool, string, error) {
	sp, err := tx.Begin(ctx)
	if err != nil {
		return nil, false, "", err
	}
	undo, aerr := applyClubAction(ctx, sp, w.Action, w.Params, w.At)
	if aerr != nil {
		_ = sp.Rollback(ctx)
		if errors.Is(aerr, ErrNothingToApply) {
			return nil, false, "", nil
		}
		return nil, false, aerr.Error(), nil
	}
	if err := sp.Commit(ctx); err != nil {
		return nil, false, "", err
	}
	return undo, true, "", nil
}

// NewWrite keeps a write and, when apply, puts its change into the club
// tables at once, in one transaction. A change the server cannot make
// (resident not found…) does not stop the write: it still goes to the sheet.
func (r *ClubRepo) NewWrite(ctx context.Context, w ClubWrite, apply bool) (*ClubWrite, error) {
	tx, err := r.db.Pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	if w.At.IsZero() {
		w.At = time.Now()
	}
	p, _ := json.Marshal(w.Params)
	if err := tx.QueryRow(ctx, `INSERT INTO club_writes (at, source, tg_id, who, action, params) VALUES ($1,$2,$3,$4,$5,$6) RETURNING id`,
		w.At, w.Source, w.TgID, w.Who, w.Action, p).Scan(&w.ID); err != nil {
		return nil, err
	}
	w.Status = WritePending
	if apply {
		undo, ok, aerr, err := applyIn(ctx, tx, &w)
		if err != nil {
			return nil, err
		}
		u, _ := json.Marshal(nzUndo(undo))
		w.Applied, w.ApplyError = ok, aerr
		if _, err := tx.Exec(ctx, `UPDATE club_writes SET applied = $2, apply_error = $3, undo = $4 WHERE id = $1`,
			w.ID, ok, aerr, u); err != nil {
			return nil, err
		}
	}
	return &w, tx.Commit(ctx)
}

func nzUndo(u []undoStep) []undoStep {
	if u == nil {
		return []undoStep{}
	}
	return u
}

// WriteSent: the sheet has it.
func (r *ClubRepo) WriteSent(ctx context.Context, id int64, result string) error {
	_, err := r.db.Pool.Exec(ctx, `UPDATE club_writes SET status = 'sent', sent_at = now(), tried_at = now(), tries = tries + 1,
		result = $2, last_error = '' WHERE id = $1`, id, cut(result, 500))
	return err
}

// WriteRejected: the script refused the write; the server's change is
// undone when undo.
func (r *ClubRepo) WriteRejected(ctx context.Context, id int64, why string, undo bool) error {
	return r.writeUndone(ctx, id, WriteRejected, why, undo)
}

// WriteDouble: the sheet already had it (the script answered "duplicate"):
// the server's own copy of the change is undone, the write counts as sent.
func (r *ClubRepo) WriteDouble(ctx context.Context, id int64, why string) error {
	return r.writeUndone(ctx, id, WriteSent, why, true)
}

func (r *ClubRepo) writeUndone(ctx context.Context, id int64, status, why string, undo bool) error {
	tx, err := r.db.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	var raw []byte
	var applied bool
	if err := tx.QueryRow(ctx, `SELECT undo, applied FROM club_writes WHERE id = $1 FOR UPDATE`, id).Scan(&raw, &applied); err != nil {
		return err
	}
	if undo && applied {
		var steps []undoStep
		if err := json.Unmarshal(raw, &steps); err != nil {
			return err
		}
		if err := undoAll(ctx, tx, steps); err != nil {
			return err
		}
		applied = false
	}
	if _, err := tx.Exec(ctx, `UPDATE club_writes SET status = $4, tried_at = now(), tries = tries + 1, result = $2,
		sent_at = CASE WHEN $4 = 'sent' THEN now() END, last_error = '',
		applied = $3, undo = CASE WHEN $3 THEN undo ELSE '[]'::jsonb END WHERE id = $1`, id, cut(why, 500), applied, status); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// WriteFailed: the script did not take it; tried again from next. unknown:
// the script may have written it (no answer in time).
func (r *ClubRepo) WriteFailed(ctx context.Context, id int64, why string, unknown bool, next time.Time) error {
	st := WritePending
	if unknown {
		st = WriteUnknown
	}
	_, err := r.db.Pool.Exec(ctx, `UPDATE club_writes SET status = $2, maybe_sent = maybe_sent OR $3, tried_at = now(),
		tries = tries + 1, next_try_at = $4, last_error = $5 WHERE id = $1`, id, st, unknown, next, cut(why, 500))
	return err
}

// OpenWrites: writes the sheet does not have yet, oldest first.
func (r *ClubRepo) OpenWrites(ctx context.Context) ([]ClubWrite, error) {
	rows, err := r.db.Pool.Query(ctx, `SELECT `+writeCols+` FROM club_writes WHERE status IN ('pending','unknown') ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ClubWrite
	for rows.Next() {
		w, err := scanWrite(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *w)
	}
	return out, rows.Err()
}

// Write returns one write.
func (r *ClubRepo) Write(ctx context.Context, id int64) (*ClubWrite, error) {
	return scanWrite(r.db.Pool.QueryRow(ctx, `SELECT `+writeCols+` FROM club_writes WHERE id = $1`, id))
}

// SeenInSheet: the club tables (as the last import left them) already have
// what the write adds. nil: cannot tell for this action.
func (r *ClubRepo) SeenInSheet(ctx context.Context, w *ClubWrite) (*bool, error) {
	tx, err := r.db.Pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	return seenInSheet(ctx, tx, w.Action, w.Params, w.At)
}

// sheetReadSpan: the script reads its sheets for a while before it stamps
// the import, so a write sent shortly before the stamp may be missing.
const sheetReadSpan = time.Minute

// ReapplyAfterImport puts back, on top of a fresh import, the changes the
// sheet did not have when it sent the import (taken at sheetAt):
//   - writes not yet in the sheet (pending, or unknown and not seen there);
//   - writes sent after the sheet took its copy, when the import does not
//     show them (or they only set a value).
//
// An unknown write the import shows is marked sent. Runs in the import's
// transaction.
func (r *ClubRepo) ReapplyAfterImport(ctx context.Context, tx pgx.Tx, sheetAt time.Time) (int, error) {
	rows, err := tx.Query(ctx, `SELECT `+writeCols+` FROM club_writes
		WHERE status IN ('pending','unknown') OR (status = 'sent' AND sent_at > $1) ORDER BY id`, sheetAt.Add(-sheetReadSpan))
	if err != nil {
		return 0, err
	}
	var list []ClubWrite
	for rows.Next() {
		w, err := scanWrite(rows)
		if err != nil {
			rows.Close()
			return 0, err
		}
		list = append(list, *w)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, err
	}
	n := 0
	for i := range list {
		w := &list[i]
		if !appliesHere(w.Action) || (!w.Applied && w.ApplyError == "") {
			continue // the server never changed its tables for this write
		}
		seen, err := seenInSheet(ctx, tx, w.Action, w.Params, w.At)
		if err != nil {
			return n, err
		}
		if seen != nil && *seen {
			if w.Status == WriteUnknown {
				if _, err := tx.Exec(ctx, `UPDATE club_writes SET status = 'sent', sent_at = now(), result = 'есть в таблице',
					undo = '[]', applied = false WHERE id = $1`, w.ID); err != nil {
					return n, err
				}
			}
			continue
		}
		if w.Status == WriteSent && seen == nil && !reapplyAfterSend[w.Action] && !club.SectionReapplyAfterSend[w.Action] {
			continue // cannot tell whether the copy has it; adding it twice would be worse
		}
		undo, ok, aerr, err := applyIn(ctx, tx, w)
		if err != nil {
			return n, err
		}
		u, _ := json.Marshal(nzUndo(undo))
		if _, err := tx.Exec(ctx, `UPDATE club_writes SET applied = $2, apply_error = $3, undo = $4 WHERE id = $1`,
			w.ID, ok, aerr, u); err != nil {
			return n, err
		}
		if ok {
			n++
		}
	}
	return n, nil
}

// WriteStats is how the writes are doing, for the migration status.
type WriteStats struct {
	Pending       int        `json:"pending"`
	Unknown       int        `json:"unknown"`
	OldestOpenAt  *time.Time `json:"oldestOpenAt,omitempty"`
	LastError     string     `json:"lastError,omitempty"`
	Sent24h       int        `json:"sent24h"`
	Rejected24h   int        `json:"rejected24h"`
	ApplyFailed24 int        `json:"applyFailed24h"`
	Total         int        `json:"total"`
	// Failed: refused by the script in the last 24 hours (the server's change undone)
	Failed   int        `json:"failed"`
	LastSent *time.Time `json:"lastSent,omitempty"`
}

func (r *ClubRepo) WriteStats(ctx context.Context) (WriteStats, error) {
	var s WriteStats
	err := r.db.Pool.QueryRow(ctx, `SELECT
		count(*) FILTER (WHERE status = 'pending'),
		count(*) FILTER (WHERE status = 'unknown'),
		min(at) FILTER (WHERE status IN ('pending','unknown')),
		COALESCE((SELECT last_error FROM club_writes WHERE status IN ('pending','unknown') AND last_error <> '' ORDER BY tried_at DESC NULLS LAST LIMIT 1), ''),
		count(*) FILTER (WHERE status = 'sent' AND at > now() - interval '24 hours'),
		count(*) FILTER (WHERE status = 'rejected' AND at > now() - interval '24 hours'),
		count(*) FILTER (WHERE apply_error <> '' AND at > now() - interval '24 hours'),
		count(*),
		max(sent_at)
		FROM club_writes`).Scan(&s.Pending, &s.Unknown, &s.OldestOpenAt, &s.LastError, &s.Sent24h, &s.Rejected24h, &s.ApplyFailed24, &s.Total, &s.LastSent)
	s.Failed = s.Rejected24h
	return s, err
}

func cut(s string, n int) string {
	if r := []rune(s); len(r) > n {
		return string(r[:n])
	}
	return s
}
