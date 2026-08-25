CREATE EXTENSION IF NOT EXISTS pgcrypto;

CREATE TABLE IF NOT EXISTS users (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    email           TEXT NOT NULL,
    password_hash   TEXT NOT NULL,
    name            TEXT NOT NULL,
    surname         TEXT NOT NULL,
    role            TEXT NOT NULL DEFAULT 'participant',
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT users_role_chk CHECK (role IN ('admin', 'moderator', 'participant'))
);

CREATE UNIQUE INDEX IF NOT EXISTS users_email_lower_uq
    ON users (LOWER(email));

CREATE INDEX IF NOT EXISTS users_role_idx
    ON users (role);
