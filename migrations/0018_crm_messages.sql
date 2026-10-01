-- WhatsApp conversations of the CRM (through Green-API): every message in and
-- out, linked to a lead by phone.
CREATE TABLE IF NOT EXISTS crm_messages (
    id      BIGSERIAL PRIMARY KEY,
    phone   TEXT        NOT NULL,          -- digits only, e.g. 77011234567
    dir     TEXT        NOT NULL,          -- in | out
    text    TEXT        NOT NULL DEFAULT '',
    name    TEXT        NOT NULL DEFAULT '',
    wa_id   TEXT        UNIQUE,
    at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    is_read BOOLEAN     NOT NULL DEFAULT false
);
CREATE INDEX IF NOT EXISTS crm_messages_phone ON crm_messages (phone, at);
