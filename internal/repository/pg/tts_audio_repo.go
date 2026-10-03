package pg

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
)

// The voice guide's phrases (tts_audio, migration 0021): made once, kept
// across redeploys, served to every listener.

// GetTTS returns a kept phrase, nil when there is none.
func (r *PlatformRepo) GetTTS(ctx context.Context, key string) ([]byte, error) {
	var data []byte
	err := r.db.Pool.QueryRow(ctx, `SELECT data FROM tts_audio WHERE key = $1`, key).Scan(&data)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	return data, err
}

// PutTTS keeps a phrase (a second one for the same key changes nothing).
func (r *PlatformRepo) PutTTS(ctx context.Context, key, voice, style, text string, data []byte) error {
	_, err := r.db.Pool.Exec(ctx, `INSERT INTO tts_audio (key, voice, style, text, size, data) VALUES ($1,$2,$3,$4,$5,$6)
		ON CONFLICT (key) DO NOTHING`, key, voice, style, text, len(data), data)
	return err
}

// TTSHave tells which of the keys are kept.
func (r *PlatformRepo) TTSHave(ctx context.Context, keys []string) (map[string]bool, error) {
	out := map[string]bool{}
	if len(keys) == 0 {
		return out, nil
	}
	rows, err := r.db.Pool.Query(ctx, `SELECT key FROM tts_audio WHERE key = ANY($1)`, keys)
	if err != nil {
		return out, err
	}
	defer rows.Close()
	for rows.Next() {
		var k string
		if err := rows.Scan(&k); err != nil {
			return out, err
		}
		out[k] = true
	}
	return out, rows.Err()
}
