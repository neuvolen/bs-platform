-- Доступ ассистента и вход без Telegram.
--
-- platform_sessions: каждый вход на платформу (Telegram, личная ссылка,
-- ассистент) получает id сессии в токене; сервер проверяет, что сессия не
-- отозвана. Список входов и «Выйти на устройстве» читают эту таблицу.
-- platform_assistants: резидент приглашает ассистента одноразовой ссылкой
-- (72 часа), ассистент входит своим Telegram и работает в кабинете резидента
-- с набором прав (preset). Токены хранятся только хешем SHA-256.
-- platform_login_links: личная ссылка входа без Telegram (30 дней), её
-- выпускает команда из карточки резидента, отзывается в любой момент.
-- platform_assist_log: кто из ассистентов что сделал (видит резидент).
-- platform_session_cutoff: «выйти на всех устройствах» для старых токенов
-- без id сессии.

CREATE TABLE IF NOT EXISTS platform_assistants (
    id              BIGSERIAL PRIMARY KEY,
    resident_tg     BIGINT      NOT NULL,             -- tg id резидента; меньше нуля: резидент без Telegram (-club_residents.id)
    resident_name   TEXT        NOT NULL,
    assistant_tg    BIGINT      NOT NULL DEFAULT 0,   -- 0: ещё не принял приглашение или входит без Telegram
    assistant_name  TEXT        NOT NULL DEFAULT '',
    label           TEXT        NOT NULL DEFAULT '',  -- как резидент подписал ассистента
    preset          TEXT        NOT NULL DEFAULT 'tasks',
    invite_hash     TEXT UNIQUE,
    invite_expires  TIMESTAMPTZ,
    created_by      TEXT        NOT NULL DEFAULT '',
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    accepted_at     TIMESTAMPTZ,
    last_used       TIMESTAMPTZ,
    revoked_at      TIMESTAMPTZ,
    revoked_by      TEXT        NOT NULL DEFAULT ''
);
CREATE INDEX IF NOT EXISTS platform_assistants_resident ON platform_assistants (resident_tg);
CREATE INDEX IF NOT EXISTS platform_assistants_assistant ON platform_assistants (assistant_tg) WHERE assistant_tg <> 0;

CREATE TABLE IF NOT EXISTS platform_login_links (
    id            BIGSERIAL PRIMARY KEY,
    token_hash    TEXT        NOT NULL UNIQUE,
    resident_tg   BIGINT      NOT NULL,
    resident_name TEXT        NOT NULL,
    assistant_id  BIGINT      NOT NULL DEFAULT 0,     -- не 0: ссылка ассистента без Telegram
    created_by    TEXT        NOT NULL DEFAULT '',
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    expires_at    TIMESTAMPTZ NOT NULL,
    last_used     TIMESTAMPTZ,
    uses          INTEGER     NOT NULL DEFAULT 0,
    revoked_at    TIMESTAMPTZ,
    revoked_by    TEXT        NOT NULL DEFAULT ''
);
CREATE INDEX IF NOT EXISTS platform_login_links_resident ON platform_login_links (resident_tg);

CREATE TABLE IF NOT EXISTS platform_sessions (
    id           TEXT        PRIMARY KEY,
    sub          TEXT        NOT NULL,                -- чей кабинет: tg:<id>
    actor        TEXT        NOT NULL,                -- кто вошёл: tg:<id>, link:<id>, assist:<id>
    actor_name   TEXT        NOT NULL DEFAULT '',
    kind         TEXT        NOT NULL,                -- telegram | link | assistant
    assistant_id BIGINT      NOT NULL DEFAULT 0,
    link_id      BIGINT      NOT NULL DEFAULT 0,
    device       TEXT        NOT NULL DEFAULT '',
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    last_seen    TIMESTAMPTZ NOT NULL DEFAULT now(),
    expires_at   TIMESTAMPTZ NOT NULL,
    revoked_at   TIMESTAMPTZ
);
CREATE INDEX IF NOT EXISTS platform_sessions_sub ON platform_sessions (sub);
CREATE INDEX IF NOT EXISTS platform_sessions_assistant ON platform_sessions (assistant_id) WHERE assistant_id <> 0;
CREATE INDEX IF NOT EXISTS platform_sessions_link ON platform_sessions (link_id) WHERE link_id <> 0;

CREATE TABLE IF NOT EXISTS platform_session_cutoff (
    sub TEXT        PRIMARY KEY,
    at  TIMESTAMPTZ NOT NULL
);

CREATE TABLE IF NOT EXISTS platform_assist_log (
    id           BIGSERIAL PRIMARY KEY,
    resident_tg  BIGINT      NOT NULL,
    assistant_id BIGINT      NOT NULL,
    actor_name   TEXT        NOT NULL DEFAULT '',
    action       TEXT        NOT NULL,
    detail       TEXT        NOT NULL DEFAULT '',
    n            INTEGER     NOT NULL DEFAULT 1,
    first_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
    at           TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS platform_assist_log_resident ON platform_assist_log (resident_tg, at DESC);
