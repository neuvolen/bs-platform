-- The voice guide's phrases (onboarding tour), synthesised once and kept here
-- so they survive redeploys and restarts. key = hash of style version, voice
-- and text (ttsKey in platform_tts.go). The server makes every tour phrase
-- ahead of time on start; listeners get them from here at once.
CREATE TABLE IF NOT EXISTS tts_audio (
    key         TEXT        PRIMARY KEY,
    voice       TEXT        NOT NULL DEFAULT '',
    style       TEXT        NOT NULL DEFAULT '',
    text        TEXT        NOT NULL DEFAULT '',
    mime        TEXT        NOT NULL DEFAULT 'audio/wav',
    size        INTEGER     NOT NULL DEFAULT 0,
    data        BYTEA       NOT NULL,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- Phrases made before this table were kept among the platform's files.
INSERT INTO tts_audio (key, mime, size, data, created_at)
SELECT id, mime, size, data, created_at FROM platform_files WHERE id LIKE 'tts\_%'
ON CONFLICT (key) DO NOTHING;
