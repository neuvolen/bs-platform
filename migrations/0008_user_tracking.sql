CREATE EXTENSION IF NOT EXISTS pgcrypto;

-- 1) Назначенные болезни пользователям
CREATE TABLE IF NOT EXISTS user_diseases (
    id           UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id       UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    disease_id    UUID NOT NULL REFERENCES diseases(id) ON DELETE RESTRICT,

    assigned_by   UUID REFERENCES users(id) ON DELETE SET NULL,
    status        TEXT NOT NULL DEFAULT 'active',
    note          TEXT NOT NULL DEFAULT '',

    assigned_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    resolved_at   TIMESTAMPTZ,

    created_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at    TIMESTAMPTZ NOT NULL DEFAULT now(),

    CONSTRAINT user_diseases_status_chk CHECK (status IN ('active','resolved','archived'))
);

-- Один и тот же disease не должен быть назначен одному user несколько раз "активно"
CREATE UNIQUE INDEX IF NOT EXISTS user_diseases_user_disease_active_uq
    ON user_diseases (user_id, disease_id)
    WHERE status = 'active';

CREATE INDEX IF NOT EXISTS user_diseases_user_id_idx
    ON user_diseases (user_id);

CREATE INDEX IF NOT EXISTS user_diseases_user_id_status_idx
    ON user_diseases (user_id, status);

CREATE INDEX IF NOT EXISTS user_diseases_disease_id_idx
    ON user_diseases (disease_id);

CREATE INDEX IF NOT EXISTS user_diseases_assigned_at_idx
    ON user_diseases (assigned_at DESC);


-- 2) Прогресс выполнения шагов (на уровне назначения user_disease)
CREATE TABLE IF NOT EXISTS user_steps (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    user_disease_id UUID NOT NULL REFERENCES user_diseases(id) ON DELETE CASCADE,
    step_id          UUID NOT NULL REFERENCES treatment_steps(id) ON DELETE CASCADE,

    state           TEXT NOT NULL DEFAULT 'pending',
    completed_at    TIMESTAMPTZ,

    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT now(),

    CONSTRAINT user_steps_state_chk CHECK (state IN ('pending','completed','skipped')),
    CONSTRAINT user_steps_completed_at_chk CHECK (
        (state = 'completed' AND completed_at IS NOT NULL)
        OR (state <> 'completed' AND completed_at IS NULL)
    ),

    UNIQUE (user_disease_id, step_id)
);

CREATE INDEX IF NOT EXISTS user_steps_user_disease_id_idx
    ON user_steps (user_disease_id);

CREATE INDEX IF NOT EXISTS user_steps_step_id_idx
    ON user_steps (step_id);

CREATE INDEX IF NOT EXISTS user_steps_state_idx
    ON user_steps (state);


-- 3) Дневники/история активности (универсальный лог)
CREATE TABLE IF NOT EXISTS activity_logs (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id          UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE, -- "чья" лента
    actor_id         UUID REFERENCES users(id) ON DELETE SET NULL,         -- кто сделал действие

    kind            TEXT NOT NULL,
    message         TEXT NOT NULL DEFAULT '',
    payload         JSONB NOT NULL DEFAULT '{}'::jsonb,

    user_disease_id UUID REFERENCES user_diseases(id) ON DELETE SET NULL,
    step_id         UUID REFERENCES treatment_steps(id) ON DELETE SET NULL,

    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),

    CONSTRAINT activity_logs_kind_chk CHECK (
        kind IN ('diary','step_completed','feedback','assignment','status_change')
    )
);

CREATE INDEX IF NOT EXISTS activity_logs_user_id_created_at_idx
    ON activity_logs (user_id, created_at DESC);

CREATE INDEX IF NOT EXISTS activity_logs_actor_id_created_at_idx
    ON activity_logs (actor_id, created_at DESC);

CREATE INDEX IF NOT EXISTS activity_logs_user_disease_id_idx
    ON activity_logs (user_disease_id);

CREATE INDEX IF NOT EXISTS activity_logs_kind_idx
    ON activity_logs (kind);
