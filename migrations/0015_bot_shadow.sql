-- The server bot in "listen, don't fine" mode. It reads the same group
-- messages as the Apps Script bot and decides on its own which are reports;
-- once a day it works out whom it would fine and compares that with what the
-- sheet actually did. Nothing here is sent to residents.

CREATE TABLE IF NOT EXISTS bot_reports (
    update_id  BIGINT PRIMARY KEY,
    message_id BIGINT,
    at         TIMESTAMPTZ NOT NULL,
    day        DATE        NOT NULL,
    late       BOOLEAN     NOT NULL DEFAULT false,
    tg_user_id BIGINT      NOT NULL,
    resident   TEXT        NOT NULL DEFAULT '',
    username   TEXT        NOT NULL DEFAULT '',
    sender     TEXT        NOT NULL DEFAULT '',
    thread     TEXT        NOT NULL DEFAULT '',
    text_len   INTEGER     NOT NULL DEFAULT 0,
    text       TEXT        NOT NULL DEFAULT '',
    verdict    TEXT        NOT NULL  -- report, short, wrong_topic, not_resident
);
CREATE INDEX IF NOT EXISTS bot_reports_day_idx ON bot_reports (day);

CREATE TABLE IF NOT EXISTS bot_shadow_days (
    day     DATE PRIMARY KEY,
    at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    match   BOOLEAN     NOT NULL,
    result  JSONB       NOT NULL,
    message TEXT        NOT NULL DEFAULT ''
);
