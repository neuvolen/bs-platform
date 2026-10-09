package pg

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
)

// PlatformRepo stores what the BS platform used to keep in browser storage.
type PlatformRepo struct{ db *DB }

func NewPlatformRepo(db *DB) *PlatformRepo { return &PlatformRepo{db: db} }

type PlatformBoard struct {
	ID        string          `json:"id"`
	Resident  string          `json:"resident"`
	Name      string          `json:"name"`
	Data      json.RawMessage `json:"data,omitempty"`
	Version   int             `json:"version"`
	Rev       int64           `json:"rev"`
	Deleted   bool            `json:"deleted"`
	UpdatedAt time.Time       `json:"updatedAt"`
	UpdatedBy string          `json:"updatedBy"`
}

type PlatformDoc struct {
	Scope     string    `json:"scope"`
	Key       string    `json:"key"`
	Value     string    `json:"value"`
	Version   int       `json:"version"`
	Rev       int64     `json:"rev"`
	Deleted   bool      `json:"deleted"`
	UpdatedAt time.Time `json:"updatedAt"`
	UpdatedBy string    `json:"updatedBy"`
}

type PlatformBoardVersion struct {
	Version   int       `json:"version"`
	SavedAt   time.Time `json:"savedAt"`
	UpdatedBy string    `json:"updatedBy"`
}

// ErrPlatformConflict: the write was based on an outdated version.
var ErrPlatformConflict = errors.New("platform: version conflict")

// Changes returns every board and doc (club + this user's personal ones)
// changed after `since`, plus the newest rev the caller has now seen.
func (r *PlatformRepo) Changes(ctx context.Context, since int64, userScope string) ([]PlatformBoard, []PlatformDoc, int64, error) {
	var maxRev int64 = since

	rows, err := r.db.Pool.Query(ctx, `
		SELECT id, resident, name, CASE WHEN deleted THEN NULL ELSE data END,
		       version, rev, deleted, updated_at, updated_by
		FROM platform_boards WHERE rev > $1 ORDER BY rev`, since)
	if err != nil {
		return nil, nil, 0, err
	}
	boards := []PlatformBoard{}
	for rows.Next() {
		var b PlatformBoard
		var data []byte
		if err := rows.Scan(&b.ID, &b.Resident, &b.Name, &data, &b.Version, &b.Rev, &b.Deleted, &b.UpdatedAt, &b.UpdatedBy); err != nil {
			rows.Close()
			return nil, nil, 0, err
		}
		if data != nil {
			b.Data = data
		}
		if b.Rev > maxRev {
			maxRev = b.Rev
		}
		boards = append(boards, b)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, nil, 0, err
	}

	rows, err = r.db.Pool.Query(ctx, `
		SELECT scope, key, CASE WHEN deleted THEN '' ELSE value END,
		       version, rev, deleted, updated_at, updated_by
		FROM platform_docs WHERE rev > $1 AND scope IN ('club', $2) ORDER BY rev`, since, userScope)
	if err != nil {
		return nil, nil, 0, err
	}
	docs := []PlatformDoc{}
	for rows.Next() {
		var d PlatformDoc
		if err := rows.Scan(&d.Scope, &d.Key, &d.Value, &d.Version, &d.Rev, &d.Deleted, &d.UpdatedAt, &d.UpdatedBy); err != nil {
			rows.Close()
			return nil, nil, 0, err
		}
		if d.Rev > maxRev {
			maxRev = d.Rev
		}
		docs = append(docs, d)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, nil, 0, err
	}

	// Nothing new for this caller, but others may have moved the counter
	// (e.g. someone else's personal doc). Report the global head so the
	// client does not re-scan the same range forever.
	var head int64
	if err := r.db.Pool.QueryRow(ctx,
		`SELECT GREATEST(COALESCE((SELECT max(rev) FROM platform_boards),0),
		                 COALESCE((SELECT max(rev) FROM platform_docs),0))`).Scan(&head); err == nil && head > maxRev {
		maxRev = head
	}
	return boards, docs, maxRev, nil
}

// GetBoard returns the current copy (deleted boards included).
func (r *PlatformRepo) GetBoard(ctx context.Context, id string) (*PlatformBoard, error) {
	var b PlatformBoard
	var data []byte
	err := r.db.Pool.QueryRow(ctx, `
		SELECT id, resident, name, data, version, rev, deleted, updated_at, updated_by
		FROM platform_boards WHERE id = $1`, id).
		Scan(&b.ID, &b.Resident, &b.Name, &data, &b.Version, &b.Rev, &b.Deleted, &b.UpdatedAt, &b.UpdatedBy)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	b.Data = data
	return &b, nil
}

// PutBoard writes a board if baseVersion matches the stored one
// (0 = "I believe it does not exist yet"). On mismatch it returns the
// current copy and ErrPlatformConflict.
func (r *PlatformRepo) PutBoard(ctx context.Context, id string, baseVersion int, data json.RawMessage, by string) (*PlatformBoard, error) {
	tx, err := r.db.Pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	var curVersion int
	var curData []byte
	var curDeleted bool
	err = tx.QueryRow(ctx, `SELECT version, data, deleted FROM platform_boards WHERE id = $1 FOR UPDATE`, id).
		Scan(&curVersion, &curData, &curDeleted)
	exists := err == nil
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return nil, err
	}

	resident, name := BoardLabels(data)

	if !exists {
		// Includes baseVersion != 0: the client knew a version the server
		// never saw (data from before the move). Keep it as a new board
		// rather than lose it.
		var out PlatformBoard
		err = tx.QueryRow(ctx, `
			INSERT INTO platform_boards (id, resident, name, data, version, updated_by)
			VALUES ($1, $2, $3, $4, 1, $5)
			RETURNING id, resident, name, version, rev, deleted, updated_at, updated_by`,
			id, resident, name, []byte(data), by).
			Scan(&out.ID, &out.Resident, &out.Name, &out.Version, &out.Rev, &out.Deleted, &out.UpdatedAt, &out.UpdatedBy)
		if err != nil {
			return nil, err
		}
		return &out, tx.Commit(ctx)
	}

	// A board deleted on another device and now edited here: the edit wins
	// only if it was based on the version that got deleted.
	if curVersion != baseVersion {
		cur, gerr := r.getBoardTx(ctx, tx, id)
		if gerr != nil {
			return nil, gerr
		}
		return cur, ErrPlatformConflict
	}

	// Same content: nothing to do, no new version.
	if !curDeleted && jsonEqual(curData, data) {
		cur, gerr := r.getBoardTx(ctx, tx, id)
		if gerr != nil {
			return nil, gerr
		}
		return cur, tx.Commit(ctx)
	}

	if _, err := tx.Exec(ctx, `
		INSERT INTO platform_board_versions (board_id, version, data, updated_by)
		SELECT id, version, data, updated_by FROM platform_boards WHERE id = $1
		ON CONFLICT DO NOTHING`, id); err != nil {
		return nil, err
	}

	var out PlatformBoard
	err = tx.QueryRow(ctx, `
		UPDATE platform_boards
		SET data = $2, resident = $3, name = $4, version = version + 1,
		    rev = nextval('platform_rev_seq'), deleted = false,
		    updated_at = now(), updated_by = $5
		WHERE id = $1
		RETURNING id, resident, name, version, rev, deleted, updated_at, updated_by`,
		id, []byte(data), resident, name, by).
		Scan(&out.ID, &out.Resident, &out.Name, &out.Version, &out.Rev, &out.Deleted, &out.UpdatedAt, &out.UpdatedBy)
	if err != nil {
		return nil, err
	}
	return &out, tx.Commit(ctx)
}

// DeleteBoard marks a board deleted (kept in history, restorable).
func (r *PlatformRepo) DeleteBoard(ctx context.Context, id string, baseVersion int, by string) (*PlatformBoard, error) {
	tx, err := r.db.Pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	var curVersion int
	var curDeleted bool
	err = tx.QueryRow(ctx, `SELECT version, deleted FROM platform_boards WHERE id = $1 FOR UPDATE`, id).Scan(&curVersion, &curDeleted)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if curVersion != baseVersion {
		cur, gerr := r.getBoardTx(ctx, tx, id)
		if gerr != nil {
			return nil, gerr
		}
		return cur, ErrPlatformConflict
	}
	if curDeleted {
		cur, gerr := r.getBoardTx(ctx, tx, id)
		if gerr != nil {
			return nil, gerr
		}
		return cur, tx.Commit(ctx)
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO platform_board_versions (board_id, version, data, updated_by)
		SELECT id, version, data, updated_by FROM platform_boards WHERE id = $1
		ON CONFLICT DO NOTHING`, id); err != nil {
		return nil, err
	}
	var out PlatformBoard
	err = tx.QueryRow(ctx, `
		UPDATE platform_boards
		SET deleted = true, version = version + 1, rev = nextval('platform_rev_seq'),
		    updated_at = now(), updated_by = $2
		WHERE id = $1
		RETURNING id, resident, name, version, rev, deleted, updated_at, updated_by`, id, by).
		Scan(&out.ID, &out.Resident, &out.Name, &out.Version, &out.Rev, &out.Deleted, &out.UpdatedAt, &out.UpdatedBy)
	if err != nil {
		return nil, err
	}
	return &out, tx.Commit(ctx)
}

// BoardVersions lists the saved history of a board, newest first.
func (r *PlatformRepo) BoardVersions(ctx context.Context, id string) ([]PlatformBoardVersion, error) {
	rows, err := r.db.Pool.Query(ctx, `
		SELECT version, saved_at, updated_by FROM platform_board_versions
		WHERE board_id = $1 ORDER BY version DESC LIMIT 200`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []PlatformBoardVersion{}
	for rows.Next() {
		var v PlatformBoardVersion
		if err := rows.Scan(&v.Version, &v.SavedAt, &v.UpdatedBy); err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

// BoardVersion returns one saved copy.
func (r *PlatformRepo) BoardVersion(ctx context.Context, id string, version int) (json.RawMessage, error) {
	var data []byte
	err := r.db.Pool.QueryRow(ctx,
		`SELECT data FROM platform_board_versions WHERE board_id = $1 AND version = $2`, id, version).Scan(&data)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	return data, err
}

// PutDoc writes a keyed section with the same version rule as boards.
func (r *PlatformRepo) PutDoc(ctx context.Context, scope, key string, baseVersion int, value string, deleted bool, by string) (*PlatformDoc, error) {
	tx, err := r.db.Pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	var curVersion int
	var curValue, curBy string
	var curDeleted bool
	var curAt time.Time
	err = tx.QueryRow(ctx, `SELECT version, value, deleted, updated_at, updated_by FROM platform_docs WHERE scope = $1 AND key = $2 FOR UPDATE`, scope, key).
		Scan(&curVersion, &curValue, &curDeleted, &curAt, &curBy)
	exists := err == nil
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return nil, err
	}

	if !exists {
		var out PlatformDoc
		err = tx.QueryRow(ctx, `
			INSERT INTO platform_docs (scope, key, value, version, deleted, updated_by)
			VALUES ($1, $2, $3, 1, $4, $5)
			RETURNING scope, key, value, version, rev, deleted, updated_at, updated_by`,
			scope, key, value, deleted, by).
			Scan(&out.Scope, &out.Key, &out.Value, &out.Version, &out.Rev, &out.Deleted, &out.UpdatedAt, &out.UpdatedBy)
		if err != nil {
			return nil, err
		}
		return &out, tx.Commit(ctx)
	}

	if curVersion != baseVersion {
		cur, gerr := r.getDocTx(ctx, tx, scope, key)
		if gerr != nil {
			return nil, gerr
		}
		return cur, ErrPlatformConflict
	}
	if curDeleted == deleted && curValue == value {
		cur, gerr := r.getDocTx(ctx, tx, scope, key)
		if gerr != nil {
			return nil, gerr
		}
		return cur, tx.Commit(ctx)
	}

	// The state being replaced goes to history first.
	if _, err = tx.Exec(ctx, `
		INSERT INTO platform_doc_versions (scope, key, version, value, deleted, updated_at, updated_by)
		VALUES ($1, $2, $3, $4, $5, $6, $7) ON CONFLICT DO NOTHING`,
		scope, key, curVersion, curValue, curDeleted, curAt, curBy); err != nil {
		return nil, err
	}
	if _, err = tx.Exec(ctx, `
		DELETE FROM platform_doc_versions WHERE scope = $1 AND key = $2 AND version <= $3 - 100`,
		scope, key, curVersion); err != nil {
		return nil, err
	}
	var out PlatformDoc
	err = tx.QueryRow(ctx, `
		UPDATE platform_docs
		SET value = $3, deleted = $4, version = version + 1,
		    rev = nextval('platform_rev_seq'), updated_at = now(), updated_by = $5
		WHERE scope = $1 AND key = $2
		RETURNING scope, key, value, version, rev, deleted, updated_at, updated_by`,
		scope, key, value, deleted, by).
		Scan(&out.Scope, &out.Key, &out.Value, &out.Version, &out.Rev, &out.Deleted, &out.UpdatedAt, &out.UpdatedBy)
	if err != nil {
		return nil, err
	}
	return &out, tx.Commit(ctx)
}

// GetDoc returns the current copy of a keyed section or nil.
func (r *PlatformRepo) GetDoc(ctx context.Context, scope, key string) (*PlatformDoc, error) {
	tx, err := r.db.Pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	return r.getDocTx(ctx, tx, scope, key)
}

func (r *PlatformRepo) getBoardTx(ctx context.Context, tx pgx.Tx, id string) (*PlatformBoard, error) {
	var b PlatformBoard
	var data []byte
	err := tx.QueryRow(ctx, `
		SELECT id, resident, name, data, version, rev, deleted, updated_at, updated_by
		FROM platform_boards WHERE id = $1`, id).
		Scan(&b.ID, &b.Resident, &b.Name, &data, &b.Version, &b.Rev, &b.Deleted, &b.UpdatedAt, &b.UpdatedBy)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if !b.Deleted {
		b.Data = data
	}
	return &b, nil
}

func (r *PlatformRepo) getDocTx(ctx context.Context, tx pgx.Tx, scope, key string) (*PlatformDoc, error) {
	var d PlatformDoc
	err := tx.QueryRow(ctx, `
		SELECT scope, key, value, version, rev, deleted, updated_at, updated_by
		FROM platform_docs WHERE scope = $1 AND key = $2`, scope, key).
		Scan(&d.Scope, &d.Key, &d.Value, &d.Version, &d.Rev, &d.Deleted, &d.UpdatedAt, &d.UpdatedBy)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &d, nil
}

// BoardLabels pulls the resident name and board title out of the board JSON
// so they can be indexed and shown without loading the whole board.
func BoardLabels(data json.RawMessage) (resident, name string) {
	var probe struct {
		Name string `json:"name"`
		Info struct {
			Res  string `json:"res"`
			Name string `json:"name"`
			Last string `json:"last"`
		} `json:"info"`
	}
	if json.Unmarshal(data, &probe) != nil {
		return "", ""
	}
	resident = probe.Info.Res
	if resident == "" {
		resident = trimJoin(probe.Info.Name, probe.Info.Last)
	}
	return resident, probe.Name
}

func trimJoin(a, b string) string {
	s := a
	if b != "" {
		if s != "" {
			s += " "
		}
		s += b
	}
	return s
}

// jsonEqual compares two JSON documents by value, ignoring key order and
// whitespace (Postgres JSONB does not keep either).
func jsonEqual(a, b []byte) bool {
	if bytes.Equal(a, b) {
		return true
	}
	var x, y any
	if json.Unmarshal(a, &x) != nil || json.Unmarshal(b, &y) != nil {
		return false
	}
	xa, _ := json.Marshal(x)
	ya, _ := json.Marshal(y)
	return bytes.Equal(xa, ya)
}

// PlatformResident is one row of the resident list sent by the Apps Script.
type PlatformResident struct {
	TgID   int64  `json:"tg"`
	Name   string `json:"name"`
	Active bool   `json:"active"`
}

// ReplaceResidents stores the full list: given rows are upserted, everyone
// else is marked inactive (a former resident loses access, keeps history).
func (r *PlatformRepo) ReplaceResidents(ctx context.Context, list []PlatformResident) (int, error) {
	tx, err := r.db.Pool.Begin(ctx)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	ids := make([]int64, 0, len(list))
	for _, p := range list {
		if p.TgID <= 0 || p.Name == "" {
			continue
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO platform_residents (tg_id, name, active, updated_at) VALUES ($1, $2, $3, now())
			ON CONFLICT (tg_id) DO UPDATE SET name = EXCLUDED.name, active = EXCLUDED.active, updated_at = now()`,
			p.TgID, p.Name, p.Active); err != nil {
			return 0, err
		}
		ids = append(ids, p.TgID)
	}
	if _, err := tx.Exec(ctx, `UPDATE platform_residents SET active = false, updated_at = now()
		WHERE active AND NOT (tg_id = ANY($1))`, ids); err != nil {
		return 0, err
	}
	return len(ids), tx.Commit(ctx)
}

// LiveBoards returns every board that is not deleted.
func (r *PlatformRepo) LiveBoards(ctx context.Context) ([]PlatformBoard, error) {
	rows, err := r.db.Pool.Query(ctx, `
		SELECT id, resident, name, data, version, rev, deleted, updated_at, updated_by
		FROM platform_boards WHERE NOT deleted`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []PlatformBoard{}
	for rows.Next() {
		var b PlatformBoard
		if err := rows.Scan(&b.ID, &b.Resident, &b.Name, &b.Data, &b.Version, &b.Rev, &b.Deleted, &b.UpdatedAt, &b.UpdatedBy); err != nil {
			return nil, err
		}
		out = append(out, b)
	}
	return out, rows.Err()
}

// ResidentByTg returns the resident's name if they are active.
func (r *PlatformRepo) ResidentByTg(ctx context.Context, tgID int64) (string, bool, error) {
	var name string
	var active bool
	q := `SELECT name, active FROM platform_residents WHERE tg_id = $1`
	if tgID < 0 {
		// резидент без Telegram (вход личной ссылкой): кабинет -id строки клуба
		q, tgID = `SELECT name, NOT former FROM club_residents WHERE id = $1 AND (tg_id IS NULL OR tg_id <= 0)`, -tgID
	}
	err := r.db.Pool.QueryRow(ctx, q, tgID).Scan(&name, &active)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	return name, active, nil
}

// PutServerDoc writes a doc the server owns (clients cannot write it):
// creates it, or bumps it only when the value really changed.
func (r *PlatformRepo) PutServerDoc(ctx context.Context, key, value string) error {
	_, err := r.db.Pool.Exec(ctx, `
		INSERT INTO platform_docs (scope, key, value, version, updated_by) VALUES ('club', $1, $2, 1, 'server')
		ON CONFLICT (scope, key) DO UPDATE
		SET value = EXCLUDED.value, version = platform_docs.version + 1,
		    rev = nextval('platform_rev_seq'), deleted = false, updated_at = now(), updated_by = 'server'
		WHERE platform_docs.value IS DISTINCT FROM EXCLUDED.value OR platform_docs.deleted`, key, value)
	return err
}

// PlatformDocVersion is one earlier state of a section.
type PlatformDocVersion struct {
	Version   int       `json:"version"`
	Value     string    `json:"value,omitempty"`
	Deleted   bool      `json:"deleted"`
	Size      int       `json:"size"`
	UpdatedAt time.Time `json:"updatedAt"`
	UpdatedBy string    `json:"updatedBy"`
}

// DocVersions lists the earlier states of a section, newest first (no values).
func (r *PlatformRepo) DocVersions(ctx context.Context, scope, key string) ([]PlatformDocVersion, error) {
	rows, err := r.db.Pool.Query(ctx, `
		SELECT version, deleted, length(value), updated_at, updated_by FROM platform_doc_versions
		WHERE scope = $1 AND key = $2 ORDER BY version DESC LIMIT 100`, scope, key)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []PlatformDocVersion{}
	for rows.Next() {
		var v PlatformDocVersion
		if err := rows.Scan(&v.Version, &v.Deleted, &v.Size, &v.UpdatedAt, &v.UpdatedBy); err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

// DocVersion returns one earlier state of a section, or nil.
func (r *PlatformRepo) DocVersion(ctx context.Context, scope, key string, version int) (*PlatformDocVersion, error) {
	var v PlatformDocVersion
	err := r.db.Pool.QueryRow(ctx, `
		SELECT version, value, deleted, length(value), updated_at, updated_by FROM platform_doc_versions
		WHERE scope = $1 AND key = $2 AND version = $3`, scope, key, version).
		Scan(&v.Version, &v.Value, &v.Deleted, &v.Size, &v.UpdatedAt, &v.UpdatedBy)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &v, nil
}
