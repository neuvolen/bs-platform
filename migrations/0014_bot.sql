-- Telegram updates received by the server. Telegram talks to the server,
-- which answers at once and passes each update on to the Google Apps Script
-- bot (the relay) until the bot itself moves here. Every update is kept for
-- 30 days: nothing is lost when the script is slow or down, and the server
-- bot can be checked against the old one on real traffic.

CREATE TABLE IF NOT EXISTS bot_updates (
    update_id     BIGINT PRIMARY KEY,
    received_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    kind          TEXT        NOT NULL DEFAULT '',
    chat_id       BIGINT,
    order_key     TEXT        NOT NULL DEFAULT '',
    body          JSONB       NOT NULL,
    relayed_at    TIMESTAMPTZ,
    relay_status  INTEGER,
    relay_ms      INTEGER,
    relay_tries   INTEGER     NOT NULL DEFAULT 0,
    relay_error   TEXT        NOT NULL DEFAULT '',
    next_try_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    claimed_until TIMESTAMPTZ,
    gave_up       BOOLEAN     NOT NULL DEFAULT false
);
CREATE INDEX IF NOT EXISTS bot_updates_waiting_idx ON bot_updates (update_id)
    WHERE relayed_at IS NULL AND NOT gave_up;
CREATE INDEX IF NOT EXISTS bot_updates_received_idx ON bot_updates (received_at);

CREATE TABLE IF NOT EXISTS bot_meta (
    key        TEXT PRIMARY KEY,
    value      TEXT        NOT NULL,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
