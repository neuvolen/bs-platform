-- R38c: the club's own events with an RSVP in the bot, broadcasts the owner
-- launches from the platform, and WhatsApp for residents without Telegram.

-- A resident's channel: Telegram (default) or WhatsApp with a phone. The
-- residents sheet keeps its columns as they are (the app reads them by
-- index), so the choice lives here, by the resident's normalized name.
CREATE TABLE IF NOT EXISTS resident_channels (
    name_key   TEXT PRIMARY KEY,
    name       TEXT        NOT NULL,
    channel    TEXT        NOT NULL DEFAULT 'tg', -- tg | wa
    phone      TEXT        NOT NULL DEFAULT '',   -- digits, 77071144645
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_by TEXT        NOT NULL DEFAULT ''
);

-- «WhatsApp: к отправке»: personal messages for WhatsApp residents. Sent by
-- Green-API when it is connected, otherwise by one click (wa.me) of the team.
-- dedup makes a message go once, whatever retries or restarts happen.
CREATE TABLE IF NOT EXISTS wa_outbox (
    id          BIGSERIAL PRIMARY KEY,
    dedup       TEXT        NOT NULL UNIQUE,
    resident    TEXT        NOT NULL DEFAULT '',
    phone       TEXT        NOT NULL,
    kind        TEXT        NOT NULL DEFAULT '',
    text        TEXT        NOT NULL,
    status      TEXT        NOT NULL DEFAULT 'pending', -- pending | sent | auto | skipped | expired
    error       TEXT        NOT NULL DEFAULT '',
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    expires_at  TIMESTAMPTZ,
    notified_at TIMESTAMPTZ,
    sent_at     TIMESTAMPTZ,
    sent_by     TEXT        NOT NULL DEFAULT ''
);
CREATE INDEX IF NOT EXISTS wa_outbox_pending_idx ON wa_outbox (created_at) WHERE status = 'pending';

CREATE TABLE IF NOT EXISTS club_events (
    id           TEXT PRIMARY KEY,
    title        TEXT        NOT NULL,
    starts_at    TIMESTAMPTZ NOT NULL,
    city         TEXT        NOT NULL DEFAULT 'Алматы',
    address      TEXT        NOT NULL DEFAULT '',
    address_note TEXT        NOT NULL DEFAULT '',
    price        BIGINT      NOT NULL DEFAULT 0,
    seats        INTEGER     NOT NULL DEFAULT 15,
    pay_link     TEXT        NOT NULL DEFAULT '',
    about        TEXT        NOT NULL DEFAULT '',
    status       TEXT        NOT NULL DEFAULT 'open', -- open | closed
    full_noted   BOOLEAN     NOT NULL DEFAULT false,
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at   TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- who: tg:<chat id> (pressed «Иду» in the bot) or wa:<phone> (added by the team)
CREATE TABLE IF NOT EXISTS event_rsvps (
    event_id   TEXT        NOT NULL REFERENCES club_events(id) ON DELETE CASCADE,
    who        TEXT        NOT NULL,
    name       TEXT        NOT NULL DEFAULT '',
    username   TEXT        NOT NULL DEFAULT '',
    phone      TEXT        NOT NULL DEFAULT '',
    kind       TEXT        NOT NULL DEFAULT '', -- resident | lead | team | guest
    status     TEXT        NOT NULL DEFAULT 'going', -- going | waitlist | cancelled
    want_phone BOOLEAN     NOT NULL DEFAULT false,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (event_id, who)
);

-- one reminder of one kind to one participant, once
CREATE TABLE IF NOT EXISTS event_notes (
    event_id TEXT        NOT NULL,
    who      TEXT        NOT NULL,
    kind     TEXT        NOT NULL, -- address | eve | morning | promoted
    sent_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (event_id, who, kind)
);

CREATE TABLE IF NOT EXISTS bc_campaigns (
    id          TEXT PRIMARY KEY,
    title       TEXT        NOT NULL DEFAULT '',
    event_id    TEXT        NOT NULL DEFAULT '',
    text        TEXT        NOT NULL,
    audience    JSONB       NOT NULL DEFAULT '{}',
    status      TEXT        NOT NULL DEFAULT 'draft', -- draft | sending | done
    forced      BOOLEAN     NOT NULL DEFAULT false,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_by  TEXT        NOT NULL DEFAULT '',
    started_at  TIMESTAMPTZ,
    started_by  TEXT        NOT NULL DEFAULT '',
    finished_at TIMESTAMPTZ,
    tested_at   TIMESTAMPTZ
);

-- one row per recipient of a campaign: the primary key is the promise that
-- nobody gets the same campaign twice. claimed = being sent; a claimed row
-- found after a restart is not sent again (it may have gone out).
CREATE TABLE IF NOT EXISTS bc_sends (
    campaign_id TEXT        NOT NULL REFERENCES bc_campaigns(id) ON DELETE CASCADE,
    target      TEXT        NOT NULL, -- tg:<chat id> | wa:<phone>
    name        TEXT        NOT NULL DEFAULT '',
    kind        TEXT        NOT NULL DEFAULT '',
    status      TEXT        NOT NULL DEFAULT 'pending', -- pending | claimed | sent | wa | failed | unknown
    error       TEXT        NOT NULL DEFAULT '',
    sent_at     TIMESTAMPTZ,
    PRIMARY KEY (campaign_id, target)
);
