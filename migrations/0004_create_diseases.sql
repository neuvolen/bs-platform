CREATE TABLE IF NOT EXISTS diseases (
    id          UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    organ_id    UUID NOT NULL REFERENCES organs(id) ON DELETE CASCADE,
    category    TEXT NOT NULL,
    title       TEXT NOT NULL,
    description TEXT NOT NULL DEFAULT '',
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS diseases_organ_id_idx ON diseases (organ_id);
CREATE INDEX IF NOT EXISTS diseases_category_idx ON diseases (LOWER(category));
