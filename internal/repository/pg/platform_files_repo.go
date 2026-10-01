package pg

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
)

// PlatformFile is a file kept by the platform: a book, a template, a recording.
type PlatformFile struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	Mime      string    `json:"mime"`
	Size      int64     `json:"size"`
	Data      []byte    `json:"-"`
	CreatedAt time.Time `json:"createdAt"`
}

// AIJob is one background AI task: a call to transcribe and summarise.
type AIJob struct {
	ID        string          `json:"id"`
	Kind      string          `json:"kind"`
	BoardID   string          `json:"boardId"`
	Resident  string          `json:"resident"`
	Status    string          `json:"status"`
	Error     string          `json:"error,omitempty"`
	Result    json.RawMessage `json:"result,omitempty"`
	CreatedAt time.Time       `json:"createdAt"`
	UpdatedAt time.Time       `json:"updatedAt"`
}

func (r *PlatformRepo) PutFile(ctx context.Context, f PlatformFile, by string) error {
	_, err := r.db.Pool.Exec(ctx, `INSERT INTO platform_files (id, name, mime, size, data, created_by)
		VALUES ($1,$2,$3,$4,$5,$6)`, f.ID, f.Name, f.Mime, int64(len(f.Data)), f.Data, by)
	return err
}

func (r *PlatformRepo) GetFile(ctx context.Context, id string) (*PlatformFile, error) {
	var f PlatformFile
	err := r.db.Pool.QueryRow(ctx, `SELECT id, name, mime, size, data, created_at FROM platform_files WHERE id=$1`, id).
		Scan(&f.ID, &f.Name, &f.Mime, &f.Size, &f.Data, &f.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	return &f, err
}

func (r *PlatformRepo) CreateAIJob(ctx context.Context, j AIJob, by string) error {
	_, err := r.db.Pool.Exec(ctx, `INSERT INTO platform_ai_jobs (id, kind, board_id, resident, status, created_by)
		VALUES ($1,$2,$3,$4,$5,$6)`, j.ID, j.Kind, j.BoardID, j.Resident, j.Status, by)
	return err
}

func (r *PlatformRepo) UpdateAIJob(ctx context.Context, id, status, errText string, result json.RawMessage) error {
	_, err := r.db.Pool.Exec(ctx, `UPDATE platform_ai_jobs SET status=$2, error=$3,
		result=COALESCE($4, result), updated_at=now() WHERE id=$1`, id, status, errText, nullJSON(result))
	return err
}

func nullJSON(b json.RawMessage) any {
	if len(b) == 0 {
		return nil
	}
	return []byte(b)
}

func (r *PlatformRepo) GetAIJob(ctx context.Context, id string) (*AIJob, error) {
	var j AIJob
	var res []byte
	err := r.db.Pool.QueryRow(ctx, `SELECT id, kind, board_id, resident, status, error, result, created_at, updated_at
		FROM platform_ai_jobs WHERE id=$1`, id).
		Scan(&j.ID, &j.Kind, &j.BoardID, &j.Resident, &j.Status, &j.Error, &res, &j.CreatedAt, &j.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	j.Result = res
	return &j, err
}

// AIJobsOf: the latest jobs of a board (results the page may not have picked up yet).
func (r *PlatformRepo) AIJobsOf(ctx context.Context, boardID string) ([]AIJob, error) {
	rows, err := r.db.Pool.Query(ctx, `SELECT id, kind, board_id, resident, status, error, result, created_at, updated_at
		FROM platform_ai_jobs WHERE board_id=$1 ORDER BY created_at DESC LIMIT 20`, boardID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []AIJob{}
	for rows.Next() {
		var j AIJob
		var res []byte
		if err := rows.Scan(&j.ID, &j.Kind, &j.BoardID, &j.Resident, &j.Status, &j.Error, &res, &j.CreatedAt, &j.UpdatedAt); err != nil {
			return nil, err
		}
		j.Result = res
		out = append(out, j)
	}
	return out, rows.Err()
}

// FailStaleAIJobs marks jobs that a restart interrupted.
func (r *PlatformRepo) FailStaleAIJobs(ctx context.Context) {
	_, _ = r.db.Pool.Exec(ctx, `UPDATE platform_ai_jobs SET status='error', error='сервер перезапустился, загрузите запись ещё раз', updated_at=now()
		WHERE status IN ('queued','running') AND updated_at < now() - interval '40 minutes'`)
}
