-- Files of the platform (books, templates, call recordings) and AI jobs
-- (call transcription and summary). Files are served only to signed-in users.

CREATE TABLE IF NOT EXISTS platform_files (
    id         TEXT PRIMARY KEY,
    name       TEXT        NOT NULL,
    mime       TEXT        NOT NULL,
    size       BIGINT      NOT NULL,
    data       BYTEA       NOT NULL,
    created_by TEXT        NOT NULL DEFAULT '',
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS platform_ai_jobs (
    id         TEXT PRIMARY KEY,
    kind       TEXT        NOT NULL,             -- call
    board_id   TEXT        NOT NULL DEFAULT '',
    resident   TEXT        NOT NULL DEFAULT '',
    status     TEXT        NOT NULL,             -- queued, running, done, error
    error      TEXT        NOT NULL DEFAULT '',
    result     JSONB,
    created_by TEXT        NOT NULL DEFAULT '',
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS platform_ai_jobs_board ON platform_ai_jobs (board_id, created_at DESC);
