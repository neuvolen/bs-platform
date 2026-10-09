-- R69: one model for applying payments and counting meetings.
--
-- club_pay_alloc is the ledger of what each income row paid: a fine
-- (fine_id), the rest of the entry fee (rest), the renewal debt (renew) or
-- the meetings package (package). A row is applied by writing its ledger
-- lines; editing or deleting the row first takes its lines back. Nothing
-- depends on a time window: the ledger says what is applied.
CREATE TABLE IF NOT EXISTS club_pay_alloc (
    id          BIGSERIAL PRIMARY KEY,
    payment_id  BIGINT      NOT NULL,
    resident_id BIGINT      NOT NULL,
    kind        TEXT        NOT NULL,          -- fine | rest | renew | package
    fine_id     BIGINT,
    amount      BIGINT      NOT NULL,
    at          TIMESTAMPTZ NOT NULL DEFAULT now(),
    by          TEXT        NOT NULL DEFAULT ''
);
CREATE INDEX IF NOT EXISTS club_pay_alloc_pay_idx  ON club_pay_alloc (payment_id);
CREATE INDEX IF NOT EXISTS club_pay_alloc_res_idx  ON club_pay_alloc (resident_id);
CREATE INDEX IF NOT EXISTS club_pay_alloc_fine_idx ON club_pay_alloc (fine_id);

-- The resident a payment belongs to, by id (the name is only the fallback);
-- other spellings of a resident's name, comma separated.
ALTER TABLE club_payments  ADD COLUMN IF NOT EXISTS resident_id BIGINT;
ALTER TABLE club_residents ADD COLUMN IF NOT EXISTS aliases     TEXT   NOT NULL DEFAULT '';

-- Meetings: «проведено» is computed from the meetings log: the distinct days
-- in «Лог встреч» from package_from on, plus meetings_adjust (a count the
-- log does not have: the sheet's counter at the cutover, a manual edit).
-- meetings_done keeps the computed value for the readers.
ALTER TABLE club_residents ADD COLUMN IF NOT EXISTS package_from    DATE;
ALTER TABLE club_residents ADD COLUMN IF NOT EXISTS meetings_adjust BIGINT NOT NULL DEFAULT 0;

-- A fine written off (with the reason) or noted; a meeting the resident
-- missed without warning.
ALTER TABLE club_fines    ADD COLUMN IF NOT EXISTS note       TEXT NOT NULL DEFAULT '';
ALTER TABLE club_fines    ADD COLUMN IF NOT EXISTS meeting_id BIGINT;
ALTER TABLE club_meetings ADD COLUMN IF NOT EXISTS status     TEXT NOT NULL DEFAULT '';
-- The resident was told about the fine (one bot message for a no-show).
ALTER TABLE club_fines    ADD COLUMN IF NOT EXISTS notified_at TIMESTAMPTZ;
