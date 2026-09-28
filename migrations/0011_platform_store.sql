-- Platform storage: what the BS platform used to keep in each browser's
-- localStorage now lives here, shared between devices and people.
--
-- rev   : a global, always-growing change number. Clients ask "what changed
--         since rev N" to pick up each other's edits within seconds.
-- version: per-record counter for optimistic locking. A write must name the
--         version it was based on; a stale write is refused with 409 and the
--         current copy, so one person's edit never silently overwrites another's.

CREATE SEQUENCE IF NOT EXISTS platform_rev_seq;

-- One row per breakdown board (разбор).
CREATE TABLE IF NOT EXISTS platform_boards (
    id          TEXT PRIMARY KEY,
    resident    TEXT        NOT NULL DEFAULT '',
    name        TEXT        NOT NULL DEFAULT '',
    data        JSONB       NOT NULL,
    version     INTEGER     NOT NULL DEFAULT 1,
    rev         BIGINT      NOT NULL DEFAULT nextval('platform_rev_seq'),
    deleted     BOOLEAN     NOT NULL DEFAULT false,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_by  TEXT        NOT NULL DEFAULT ''
);
CREATE INDEX IF NOT EXISTS platform_boards_rev_idx      ON platform_boards (rev);
CREATE INDEX IF NOT EXISTS platform_boards_resident_idx ON platform_boards (lower(resident));

-- Every previous state of a board. Cheap insurance: any overwrite can be undone.
CREATE TABLE IF NOT EXISTS platform_board_versions (
    board_id    TEXT        NOT NULL,
    version     INTEGER     NOT NULL,
    data        JSONB       NOT NULL,
    updated_by  TEXT        NOT NULL DEFAULT '',
    saved_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (board_id, version)
);

-- Every other section (libraries, CRM, kanban, contacts, …), keyed exactly as
-- in the browser ("bs_crm", "bs_tools", …). The value is stored byte-for-byte.
-- scope: 'club' = shared by the team; 'user:<id>' = personal (theme, tab order).
CREATE TABLE IF NOT EXISTS platform_docs (
    scope       TEXT        NOT NULL,
    key         TEXT        NOT NULL,
    value       TEXT        NOT NULL,
    version     INTEGER     NOT NULL DEFAULT 1,
    rev         BIGINT      NOT NULL DEFAULT nextval('platform_rev_seq'),
    deleted     BOOLEAN     NOT NULL DEFAULT false,
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_by  TEXT        NOT NULL DEFAULT '',
    PRIMARY KEY (scope, key)
);
CREATE INDEX IF NOT EXISTS platform_docs_rev_idx ON platform_docs (rev);
