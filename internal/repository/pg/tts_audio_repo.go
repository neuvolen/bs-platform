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

// TTSEntry: one kept phrase without its sound.
type TTSEntry struct {
	Key, Voice, Text string
	Size             int
}

// TTSIndex lists the kept phrases of a style (R32d: the tour's manifest picks
// the current voice and falls back to an earlier one while the new is made).
func (r *PlatformRepo) TTSIndex(ctx context.Context, style string) ([]TTSEntry, error) {
	rows, err := r.db.Pool.Query(ctx, `SELECT key, voice, text, size FROM tts_audio WHERE style = $1 AND text <> '' ORDER BY created_at`, style)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []TTSEntry
	for rows.Next() {
		var e TTSEntry
		if err := rows.Scan(&e.Key, &e.Voice, &e.Text, &e.Size); err != nil {
			return out, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// PutTTSMime keeps a phrase with its type (R36: the premium voice's MP3).
func (r *PlatformRepo) PutTTSMime(ctx context.Context, key, voice, style, text, mime string, data []byte) error {
	_, err := r.db.Pool.Exec(ctx, `INSERT INTO tts_audio (key, voice, style, text, mime, size, data) VALUES ($1,$2,$3,$4,$5,$6,$7)
		ON CONFLICT (key) DO NOTHING`, key, voice, style, text, mime, len(data), data)
	return err
}

// GetTTSMime: a kept phrase and its type, nil when there is none.
func (r *PlatformRepo) GetTTSMime(ctx context.Context, key string) ([]byte, string, error) {
	var data []byte
	var mime string
	err := r.db.Pool.QueryRow(ctx, `SELECT data, mime FROM tts_audio WHERE key = $1`, key).Scan(&data, &mime)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, "", nil
	}
	return data, mime, err
}
