-- R71: «Календарь работы» каждого человека синхронизируется с его Google
-- Календарём. Подключение (refresh token зашифрован ключом от JWT_SECRET),
-- кэш событий Google (свои блоки и чужие события для показа), журнал
-- синхронизации и конфликтов («побеждает последняя правка»).
CREATE TABLE IF NOT EXISTS gcal_users (
    scope        TEXT PRIMARY KEY,                -- user:tg:<id> (документ bs_mycal)
    email        TEXT        NOT NULL DEFAULT '',
    refresh_enc  TEXT        NOT NULL DEFAULT '',
    granted      TEXT        NOT NULL DEFAULT '',
    read_cal     TEXT        NOT NULL DEFAULT 'primary',
    write_cal    TEXT        NOT NULL DEFAULT '',
    write_name   TEXT        NOT NULL DEFAULT '',
    own_cal      BOOLEAN     NOT NULL DEFAULT false,
    sync_read    TEXT        NOT NULL DEFAULT '',
    sync_write   TEXT        NOT NULL DEFAULT '',
    last_sync    TIMESTAMPTZ,
    last_error   TEXT        NOT NULL DEFAULT '',
    need_consent BOOLEAN     NOT NULL DEFAULT false,
    connected_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at   TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS gcal_user_events (
    scope     TEXT        NOT NULL REFERENCES gcal_users (scope) ON DELETE CASCADE,
    cal       TEXT        NOT NULL,
    event_id  TEXT        NOT NULL,
    ours      BOOLEAN     NOT NULL DEFAULT false,
    sig       TEXT        NOT NULL DEFAULT '',
    summary   TEXT        NOT NULL DEFAULT '',
    starts    TIMESTAMPTZ NOT NULL,
    ends      TIMESTAMPTZ NOT NULL,
    all_day   BOOLEAN     NOT NULL DEFAULT false,
    g_updated TEXT        NOT NULL DEFAULT '',
    PRIMARY KEY (scope, cal, event_id)
);

CREATE TABLE IF NOT EXISTS gcal_user_log (
    id    BIGSERIAL PRIMARY KEY,
    scope TEXT        NOT NULL,
    at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    kind  TEXT        NOT NULL,
    text  TEXT        NOT NULL
);
CREATE INDEX IF NOT EXISTS gcal_user_log_scope ON gcal_user_log (scope, at DESC);
