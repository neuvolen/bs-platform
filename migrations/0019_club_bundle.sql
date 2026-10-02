-- Moving the club off the Google Sheet, step 2: the server builds the
-- Telegram app's data bundle from its own tables and compares it with the
-- script's every day; club writes from the app go to the server first.

-- What the app's bundle shows exactly as the sheet has it.
ALTER TABLE club_residents   ADD COLUMN IF NOT EXISTS archived  BOOLEAN NOT NULL DEFAULT false; -- from «Бывшие резиденты»
ALTER TABLE club_fines       ADD COLUMN IF NOT EXISTS sheet_row INTEGER;
ALTER TABLE club_fines       ADD COLUMN IF NOT EXISTS status    TEXT    NOT NULL DEFAULT '';
ALTER TABLE club_meetings    ADD COLUMN IF NOT EXISTS sheet_row INTEGER;
ALTER TABLE club_meetings    ADD COLUMN IF NOT EXISTS addr_cell TEXT    NOT NULL DEFAULT '';
ALTER TABLE club_meetings    ADD COLUMN IF NOT EXISTS link_cell TEXT    NOT NULL DEFAULT '';
ALTER TABLE club_meetings    ADD COLUMN IF NOT EXISTS h_cell    TEXT    NOT NULL DEFAULT '';
ALTER TABLE club_meeting_log ADD COLUMN IF NOT EXISTS time_cell TEXT    NOT NULL DEFAULT '';
ALTER TABLE club_reports     ADD COLUMN IF NOT EXISTS shown_at  TIMESTAMPTZ; -- column A «Дата» as shown

-- Sheets kept as displayed: «PL» (the money stays read from the sheet) and
-- any sheet the import brings that the server does not parse («Профили»…).
CREATE TABLE IF NOT EXISTS club_sheets (
    name TEXT PRIMARY KEY,
    rows JSONB       NOT NULL,
    at   TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- Club writes from the app and the platform: applied to the server's tables
-- first, then passed on to the script so the sheet stays in step. A write the
-- script did not take stays here and is retried; nothing is lost.
--   pending   not yet in the sheet (retried from next_try_at)
--   unknown   the script did not answer in time: it may have written; the
--             next import tells (seen there = sent), otherwise it is resent
--   sent      the sheet has it
--   rejected  the script refused it; the server's change was undone
CREATE TABLE IF NOT EXISTS club_writes (
    id          BIGSERIAL PRIMARY KEY,
    at          TIMESTAMPTZ NOT NULL DEFAULT now(),
    source      TEXT        NOT NULL,            -- app | platform
    tg_id       BIGINT      NOT NULL DEFAULT 0,
    who         TEXT        NOT NULL DEFAULT '',
    action      TEXT        NOT NULL,
    params      JSONB       NOT NULL DEFAULT '{}',
    applied     BOOLEAN     NOT NULL DEFAULT false, -- the server's tables have it
    undo        JSONB       NOT NULL DEFAULT '[]',
    apply_error TEXT        NOT NULL DEFAULT '',
    status      TEXT        NOT NULL DEFAULT 'pending',
    maybe_sent  BOOLEAN     NOT NULL DEFAULT false, -- an earlier try may have reached the sheet
    tries       INTEGER     NOT NULL DEFAULT 0,
    next_try_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    tried_at    TIMESTAMPTZ,
    sent_at     TIMESTAMPTZ,
    last_error  TEXT        NOT NULL DEFAULT '',
    result      TEXT        NOT NULL DEFAULT ''
);
CREATE INDEX IF NOT EXISTS club_writes_open_idx ON club_writes (id) WHERE status IN ('pending', 'unknown');

-- One comparison of the app's bundle a day: the script's against the server's.
CREATE TABLE IF NOT EXISTS club_bundle_checks (
    day        DATE PRIMARY KEY,
    at         TIMESTAMPTZ NOT NULL DEFAULT now(),
    ok         BOOLEAN     NOT NULL,
    result     JSONB       NOT NULL
);
