-- Club data moved from the Google Sheet: residents and debts, the cash
-- journal (ДДС), fines, meetings, report and meeting logs, bot texts, and
-- the P&L line structure. While club_meta.master = 'sheet' the sheet is the
-- source of truth and every import replaces these tables; once the server
-- takes over (master = 'server') imports are refused.

CREATE TABLE IF NOT EXISTS club_meta (
    key   TEXT PRIMARY KEY,
    value TEXT NOT NULL
);
INSERT INTO club_meta (key, value) VALUES ('master', 'sheet') ON CONFLICT DO NOTHING;

CREATE TABLE IF NOT EXISTS club_residents (
    id               SERIAL PRIMARY KEY,
    name             TEXT        NOT NULL,
    tg_id            BIGINT,
    format           TEXT        NOT NULL DEFAULT '',
    tariff           BIGINT      NOT NULL DEFAULT 0,
    meetings_granted BIGINT      NOT NULL DEFAULT 0,
    meetings_done    BIGINT      NOT NULL DEFAULT 0,
    paid_entry       BIGINT      NOT NULL DEFAULT 0,
    rest_entry       BIGINT      NOT NULL DEFAULT 0,
    renew_debt       BIGINT      NOT NULL DEFAULT 0,
    former           BOOLEAN     NOT NULL DEFAULT false,
    exception        BOOLEAN     NOT NULL DEFAULT false,
    admin            BOOLEAN     NOT NULL DEFAULT false,
    source           TEXT        NOT NULL DEFAULT '',
    joined_at        DATE,
    left_at          DATE,
    months           BIGINT      NOT NULL DEFAULT 0,
    note             TEXT        NOT NULL DEFAULT '',
    partner          TEXT        NOT NULL DEFAULT '',
    updated_at       TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_by       TEXT        NOT NULL DEFAULT ''
);
CREATE INDEX IF NOT EXISTS club_residents_name_idx ON club_residents (lower(name));
CREATE INDEX IF NOT EXISTS club_residents_tg_idx   ON club_residents (tg_id);

CREATE TABLE IF NOT EXISTS club_payments (
    id          SERIAL PRIMARY KEY,
    sheet_row   INTEGER,
    date        DATE        NOT NULL,
    income      BIGINT      NOT NULL DEFAULT 0,
    expense     BIGINT      NOT NULL DEFAULT 0,
    income_cat  TEXT        NOT NULL DEFAULT '',
    resident    TEXT        NOT NULL DEFAULT '',
    expense_cat TEXT        NOT NULL DEFAULT '',
    applied     BOOLEAN     NOT NULL DEFAULT false,
    source      TEXT        NOT NULL DEFAULT 'sheet',
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    created_by  TEXT        NOT NULL DEFAULT ''
);
CREATE INDEX IF NOT EXISTS club_payments_date_idx ON club_payments (date);

CREATE TABLE IF NOT EXISTS club_fines (
    id         SERIAL PRIMARY KEY,
    resident   TEXT        NOT NULL,
    type       TEXT        NOT NULL DEFAULT '',
    amount     BIGINT      NOT NULL,
    date       DATE,
    paid       BOOLEAN     NOT NULL DEFAULT false,
    paid_at    TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    created_by TEXT        NOT NULL DEFAULT ''
);
CREATE INDEX IF NOT EXISTS club_fines_resident_idx ON club_fines (lower(resident));

CREATE TABLE IF NOT EXISTS club_meetings (
    id         SERIAL PRIMARY KEY,
    resident   TEXT        NOT NULL,
    date       DATE        NOT NULL,
    time       TEXT        NOT NULL DEFAULT '',
    place      TEXT        NOT NULL DEFAULT '',
    link       TEXT        NOT NULL DEFAULT '',
    online     BOOLEAN     NOT NULL DEFAULT false,
    sent_3d    BOOLEAN     NOT NULL DEFAULT false,
    sent_1d    BOOLEAN     NOT NULL DEFAULT false,
    sent_1h    BOOLEAN     NOT NULL DEFAULT false,
    done       BOOLEAN     NOT NULL DEFAULT false,
    event_id   TEXT        NOT NULL DEFAULT '',
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS club_meetings_date_idx ON club_meetings (date);

CREATE TABLE IF NOT EXISTS club_reports (
    id         BIGSERIAL PRIMARY KEY,
    at         TIMESTAMPTZ NOT NULL,
    username   TEXT        NOT NULL DEFAULT '',
    name       TEXT        NOT NULL DEFAULT '',
    text       TEXT        NOT NULL DEFAULT '',
    tg_user_id BIGINT,
    thread     TEXT        NOT NULL DEFAULT '',
    late       BOOLEAN     NOT NULL DEFAULT false,
    message_id BIGINT
);
CREATE INDEX IF NOT EXISTS club_reports_at_idx ON club_reports (at);

CREATE TABLE IF NOT EXISTS club_meeting_log (
    id       SERIAL PRIMARY KEY,
    date     DATE NOT NULL,
    resident TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS club_settings (
    key        TEXT PRIMARY KEY,
    value      TEXT        NOT NULL,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS club_pl_rows (
    position INTEGER PRIMARY KEY,
    name     TEXT NOT NULL,
    section  TEXT NOT NULL
);

-- Every import keeps the raw sheets and the check report: any import can be
-- looked at or replayed later.
CREATE TABLE IF NOT EXISTS club_imports (
    id       SERIAL PRIMARY KEY,
    at       TIMESTAMPTZ NOT NULL DEFAULT now(),
    by       TEXT        NOT NULL DEFAULT '',
    dry_run  BOOLEAN     NOT NULL DEFAULT false,
    raw      JSONB       NOT NULL,
    report   JSONB       NOT NULL
);
