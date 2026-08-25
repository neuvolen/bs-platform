CREATE TABLE IF NOT EXISTS treatment_plans (
    id          UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    disease_id  UUID NOT NULL UNIQUE REFERENCES diseases(id) ON DELETE CASCADE,
    title       TEXT NOT NULL,
    description TEXT NOT NULL DEFAULT '',
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS treatment_steps (
    id          UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    plan_id     UUID NOT NULL REFERENCES treatment_plans(id) ON DELETE CASCADE,
    order_no    INT NOT NULL,
    title       TEXT NOT NULL,
    description TEXT NOT NULL DEFAULT '',
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (plan_id, order_no)
);

CREATE INDEX IF NOT EXISTS treatment_steps_plan_id_idx ON treatment_steps (plan_id);
