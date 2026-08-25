CREATE EXTENSION IF NOT EXISTS pgcrypto;

CREATE TABLE IF NOT EXISTS disease_categories (
    id          UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    code        TEXT NOT NULL,
    title       TEXT NOT NULL,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT disease_categories_code_chk CHECK (code <> '')
);

CREATE UNIQUE INDEX IF NOT EXISTS disease_categories_code_lower_uq
    ON disease_categories (LOWER(code));

ALTER TABLE diseases
    ADD COLUMN IF NOT EXISTS category_id UUID;


INSERT INTO disease_categories (code, title)
SELECT DISTINCT LOWER(TRIM(category)) AS code, TRIM(category) AS title
FROM diseases
WHERE category IS NOT NULL AND TRIM(category) <> ''
ON CONFLICT DO NOTHING;

UPDATE diseases d
SET category_id = c.id
FROM disease_categories c
WHERE LOWER(TRIM(d.category)) = LOWER(c.code)
  AND d.category_id IS NULL;

  -- safety check 
DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM diseases WHERE category_id IS NULL) THEN
    RAISE EXCEPTION 'diseases.category_id has NULLs after backfill';
  END IF;
END$$;


ALTER TABLE diseases
    ALTER COLUMN category_id SET NOT NULL;

ALTER TABLE diseases
    ADD CONSTRAINT diseases_category_id_fk
    FOREIGN KEY (category_id) REFERENCES disease_categories(id) ON DELETE RESTRICT;

CREATE INDEX IF NOT EXISTS diseases_category_id_idx ON diseases (category_id);

ALTER TABLE diseases DROP COLUMN IF EXISTS category;
