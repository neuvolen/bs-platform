package pg

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
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
	fc := filesCache()
	if fc == nil {
		err := r.db.Pool.QueryRow(ctx, `SELECT id, name, mime, size, data, created_at FROM platform_files WHERE id=$1`, id).
			Scan(&f.ID, &f.Name, &f.Mime, &f.Size, &f.Data, &f.CreatedAt)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil
		}
		return &f, err
	}
	// R83e: a big file comes from the volume's copy (file_cache.go)
	err := r.db.Pool.QueryRow(ctx, `SELECT id, name, mime, size, CASE WHEN size >= $2 THEN NULL ELSE data END, created_at
		FROM platform_files WHERE id=$1`, id, fileCacheMin).
		Scan(&f.ID, &f.Name, &f.Mime, &f.Size, &f.Data, &f.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		fc.remove(id)
		return nil, nil
	}
	if err != nil || f.Data != nil {
		return &f, err
	}
	if b := fc.read(id, f.Size); b != nil {
		f.Data = b
		return &f, nil
	}
	if err := r.db.Pool.QueryRow(ctx, `SELECT data FROM platform_files WHERE id=$1`, id).Scan(&f.Data); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	f.Size = int64(len(f.Data))
	fc.write(id, f.Data)
	return &f, nil
}

// DeleteFile removes a kept file (R55: a video taken out of the funnel library).
func (r *PlatformRepo) DeleteFile(ctx context.Context, id string) error {
	_, err := r.db.Pool.Exec(ctx, `DELETE FROM platform_files WHERE id=$1`, id)
	filesCache().remove(id)
	return err
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

// FailStaleAIJobs marks jobs that a restart interrupted and that cannot be
// resumed (no stored recording). Jobs with a recording are resumed by the
// server (ClaimStaleCallJobs), never failed for a restart.
func (r *PlatformRepo) FailStaleAIJobs(ctx context.Context) {
	_, _ = r.db.Pool.Exec(ctx, `UPDATE platform_ai_jobs SET status='error', error='сервер перезапустился, загрузите запись ещё раз', updated_at=now()
		WHERE status IN ('queued','running') AND updated_at < now() - interval '40 minutes'
		  AND COALESCE(result->>'file','') = '' AND COALESCE(result->>'audio','') = ''`)
}

// SetAIJobBoard ties a job (a recovered recording) to a board.
func (r *PlatformRepo) SetAIJobBoard(ctx context.Context, id, board, resident string) (bool, error) {
	t, err := r.db.Pool.Exec(ctx, `UPDATE platform_ai_jobs SET board_id=$2, resident=COALESCE(NULLIF($3,''), resident) WHERE id=$1`, id, board, resident)
	return t.RowsAffected() > 0, err
}

// TouchAIJob: the job is alive (a heartbeat while a long call is processed).
func (r *PlatformRepo) TouchAIJob(ctx context.Context, id string) {
	_, _ = r.db.Pool.Exec(ctx, `UPDATE platform_ai_jobs SET updated_at=now() WHERE id=$1 AND status IN ('queued','running')`, id)
}

// ClaimStaleCallJobs takes call jobs nobody works on (a restart or a deploy
// stopped them): queued or running and silent for longer than idle.
func (r *PlatformRepo) ClaimStaleCallJobs(ctx context.Context, idle time.Duration) ([]string, error) {
	rows, err := r.db.Pool.Query(ctx, `UPDATE platform_ai_jobs SET status='queued', updated_at=now()
		WHERE kind='call' AND status IN ('queued','running') AND updated_at < now() - make_interval(secs => $1)
		RETURNING id`, idle.Seconds())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

// CallJobs: the latest call jobs of every board, without transcripts (a list).
func (r *PlatformRepo) CallJobs(ctx context.Context, limit int) ([]AIJob, error) {
	rows, err := r.db.Pool.Query(ctx, `SELECT id, kind, board_id, resident, status, error,
		CASE WHEN result IS NULL THEN NULL ELSE result - 'transcript' END, created_at, updated_at
		FROM platform_ai_jobs WHERE kind='call' ORDER BY created_at DESC LIMIT $1`, limit)
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

// CallFiles: stored call recordings and transcripts (no content), newest first.
func (r *PlatformRepo) CallFiles(ctx context.Context, limit int) ([]PlatformFile, error) {
	rows, err := r.db.Pool.Query(ctx, `SELECT id, name, mime, size, created_at FROM platform_files
		WHERE name LIKE 'Созвон %' OR name LIKE 'Запись разбора %' OR name LIKE 'Расшифровка разбора %'
		ORDER BY created_at DESC LIMIT $1`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []PlatformFile{}
	for rows.Next() {
		var f PlatformFile
		if err := rows.Scan(&f.ID, &f.Name, &f.Mime, &f.Size, &f.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, f)
	}
	return out, rows.Err()
}

// ResidentTgByName finds the Telegram id of an active resident by name: the
// full name, else the first name when only one resident has it.
func (r *PlatformRepo) ResidentTgByName(ctx context.Context, name string) (int64, string, error) {
	norm := func(s string) string { return strings.Join(strings.Fields(strings.ToLower(strings.ReplaceAll(s, "ё", "е"))), " ") }
	want := norm(name)
	if want == "" {
		return 0, "", nil
	}
	rows, err := r.db.Pool.Query(ctx, `SELECT tg_id, name FROM platform_residents WHERE active`)
	if err != nil {
		return 0, "", err
	}
	defer rows.Close()
	type rr struct {
		id   int64
		name string
	}
	var all []rr
	for rows.Next() {
		var x rr
		if err := rows.Scan(&x.id, &x.name); err != nil {
			return 0, "", err
		}
		all = append(all, x)
	}
	if err := rows.Err(); err != nil {
		return 0, "", err
	}
	for _, x := range all {
		if norm(x.name) == want {
			return x.id, x.name, nil
		}
	}
	first := strings.Fields(want)[0]
	var hit []rr
	for _, x := range all {
		if f := strings.Fields(norm(x.name)); len(f) > 0 && f[0] == first {
			hit = append(hit, x)
		}
	}
	if len(hit) == 1 {
		return hit[0].id, hit[0].name, nil
	}
	return 0, "", nil
}

// FileExists checks a file without loading its content.
func (r *PlatformRepo) FileExists(ctx context.Context, id string) bool {
	var one int
	return r.db.Pool.QueryRow(ctx, `SELECT 1 FROM platform_files WHERE id=$1`, id).Scan(&one) == nil
}
