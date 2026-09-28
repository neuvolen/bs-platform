-- Residents allowed into the platform. The Google Sheet stays the source of
-- truth: the Apps Script sends the full list here every hour, signed with
-- the bot token. A resident sees only boards made for their name.
CREATE TABLE IF NOT EXISTS platform_residents (
    tg_id       BIGINT      PRIMARY KEY,
    name        TEXT        NOT NULL,
    active      BOOLEAN     NOT NULL DEFAULT true,
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);
