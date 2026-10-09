package pg

import (
	"context"
	"encoding/json"
	"time"
)

// R65: записи разборов не хранятся после саммари.

// FileSize: the size of a kept file without loading it; false when there is none.
func (r *PlatformRepo) FileSize(ctx context.Context, id string) (int64, bool) {
	var n int64
	if err := r.db.Pool.QueryRow(ctx, `SELECT size FROM platform_files WHERE id=$1`, id).Scan(&n); err != nil {
		return 0, false
	}
	return n, true
}

// CallRecJob: a call job that still points at its audio recording.
type CallRecJob struct {
	ID        string
	BoardID   string
	Status    string
	Result    json.RawMessage // without the transcript
	HasText   bool            // the transcript is there
	CreatedAt time.Time
	UpdatedAt time.Time
}

// CallJobsWithAudio: call jobs whose result still names an audio file.
func (r *PlatformRepo) CallJobsWithAudio(ctx context.Context, limit int) ([]CallRecJob, error) {
	rows, err := r.db.Pool.Query(ctx, `SELECT id, board_id, status, result - 'transcript',
		COALESCE(btrim(result->>'transcript'), '') <> '', created_at, updated_at
		FROM platform_ai_jobs WHERE kind='call' AND COALESCE(result->>'audio','') <> ''
		ORDER BY created_at LIMIT $1`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []CallRecJob
	for rows.Next() {
		var j CallRecJob
		var res []byte
		if err := rows.Scan(&j.ID, &j.BoardID, &j.Status, &res, &j.HasText, &j.CreatedAt, &j.UpdatedAt); err != nil {
			return nil, err
		}
		j.Result = res
		out = append(out, j)
	}
	return out, rows.Err()
}
