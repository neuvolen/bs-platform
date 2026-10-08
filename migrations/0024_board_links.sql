-- R57: «Ссылка для клиента». The team sends a lead (not a resident yet) a
-- read-only link to their board: https://app.bxclub.kz/b/<id>, no login.
-- id is 128 random bits (hex); one live link per board, revocable, 30 days.
CREATE TABLE IF NOT EXISTS board_links (
    id               TEXT PRIMARY KEY,
    board_id         TEXT        NOT NULL,
    created_by       TEXT        NOT NULL DEFAULT '',
    created_at       TIMESTAMPTZ NOT NULL DEFAULT now(),
    expires_at       TIMESTAMPTZ NOT NULL,
    revoked          BOOLEAN     NOT NULL DEFAULT false,
    revoked_at       TIMESTAMPTZ,
    opens            INT         NOT NULL DEFAULT 0,
    first_open_at    TIMESTAMPTZ,
    last_open_at     TIMESTAMPTZ,
    open_note_day    TEXT        NOT NULL DEFAULT '', -- the Almaty day the team last got «Клиент открыл доску»
    seconds          INT         NOT NULL DEFAULT 0,  -- time on the page, summed over visits
    sections         JSONB       NOT NULL DEFAULT '{}'::jsonb, -- section -> times viewed
    clicks           JSONB       NOT NULL DEFAULT '{}'::jsonb, -- button -> times clicked
    intent           TEXT        NOT NULL DEFAULT '', -- join | ask | think (the last one)
    intent_at        TIMESTAMPTZ,
    join_notified_at TIMESTAMPTZ
);

CREATE INDEX IF NOT EXISTS board_links_board ON board_links (board_id, created_at DESC);
