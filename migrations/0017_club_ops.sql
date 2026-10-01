-- Journal of every change made to the club's data from the app and the
-- platform: first step of moving the club off the Google Sheet. Each money,
-- fine, meeting or resident change is kept here with who did it and the
-- outcome, so the server holds the full history independent of the sheet.
CREATE TABLE IF NOT EXISTS club_ops (
    id      BIGSERIAL PRIMARY KEY,
    at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    source  TEXT        NOT NULL,            -- app | platform
    tg_id   BIGINT      NOT NULL DEFAULT 0,
    who     TEXT        NOT NULL DEFAULT '',
    action  TEXT        NOT NULL,
    params  JSONB       NOT NULL DEFAULT '{}',
    ok      BOOLEAN     NOT NULL,
    result  TEXT        NOT NULL DEFAULT ''
);
CREATE INDEX IF NOT EXISTS club_ops_at ON club_ops (at DESC);
