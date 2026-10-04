-- R32a: «Учёт» edits the cash journal (ДДС) as a spreadsheet on the
-- platform. Two columns the sheet never had: the account the money went
-- through (Kaspi, Наличные…) and a free comment; plus who changed the row
-- last. The sheet import leaves them empty.
ALTER TABLE club_payments ADD COLUMN IF NOT EXISTS account    TEXT        NOT NULL DEFAULT '';
ALTER TABLE club_payments ADD COLUMN IF NOT EXISTS comment    TEXT        NOT NULL DEFAULT '';
ALTER TABLE club_payments ADD COLUMN IF NOT EXISTS updated_at TIMESTAMPTZ;
ALTER TABLE club_payments ADD COLUMN IF NOT EXISTS updated_by TEXT        NOT NULL DEFAULT '';
