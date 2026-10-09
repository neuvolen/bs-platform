-- R70: «Почему у Альтаира опять стоит, что он должен 100к?»
--
-- club_debt_adjust: «Установить долг» in «Учёт → Долги и штрафы» (or a
-- manual edit of the debt in the resident card): the debt the team set, with
-- the reason. A payment line written before the latest adjustment is never
-- given back to the debt (editing or deleting that payment does not bring
-- the old debt back): the adjustment holds.
CREATE TABLE IF NOT EXISTS club_debt_adjust (
    id           BIGSERIAL PRIMARY KEY,
    resident_id  BIGINT      NOT NULL,
    rest_before  BIGINT      NOT NULL,
    renew_before BIGINT      NOT NULL,
    rest_after   BIGINT      NOT NULL,
    renew_after  BIGINT      NOT NULL,
    reason       TEXT        NOT NULL,
    by           TEXT        NOT NULL DEFAULT '',
    key          TEXT,
    at           TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS club_debt_adjust_res_idx ON club_debt_adjust (resident_id);
CREATE UNIQUE INDEX IF NOT EXISTS club_debt_adjust_key_idx ON club_debt_adjust (key) WHERE key IS NOT NULL;

-- club_balance_log: every change of a resident's debt (rest of the entry fee,
-- renewal debt), whoever made it, with the reason the code gave
-- (set_config('bs.reason', ...) in the same transaction). Nothing changes a
-- balance silently: the server prints the new lines at start, the tab shows
-- them in the resident's history.
CREATE TABLE IF NOT EXISTS club_balance_log (
    id           BIGSERIAL PRIMARY KEY,
    resident_id  BIGINT      NOT NULL,
    name         TEXT        NOT NULL DEFAULT '',
    rest_before  BIGINT,
    rest_after   BIGINT,
    renew_before BIGINT,
    renew_after  BIGINT,
    reason       TEXT        NOT NULL DEFAULT '',
    at           TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS club_balance_log_res_idx ON club_balance_log (resident_id);

CREATE OR REPLACE FUNCTION club_balance_log_fn() RETURNS trigger AS $$
BEGIN
    IF NEW.rest_entry IS DISTINCT FROM OLD.rest_entry OR NEW.renew_debt IS DISTINCT FROM OLD.renew_debt THEN
        INSERT INTO club_balance_log (resident_id, name, rest_before, rest_after, renew_before, renew_after, reason)
        VALUES (NEW.id, NEW.name, OLD.rest_entry, NEW.rest_entry, OLD.renew_debt, NEW.renew_debt,
            COALESCE(NULLIF(current_setting('bs.reason', true), ''), 'без указанной причины (' || COALESCE(NEW.updated_by, '') || ')'));
    END IF;
    RETURN NEW;
END
$$ LANGUAGE plpgsql;

DROP TRIGGER IF EXISTS club_balance_log_tr ON club_residents;
CREATE TRIGGER club_balance_log_tr AFTER UPDATE ON club_residents
    FOR EACH ROW EXECUTE FUNCTION club_balance_log_fn();
