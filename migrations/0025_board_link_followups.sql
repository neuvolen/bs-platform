-- R58: «Отправить в WhatsApp» из «Ссылки для клиента» и прогрев: после отправки
-- владелец получает напоминания в бот (+1 день, если клиент не открыл доску;
-- +2 дня, если открыл, но не решил; +5 дней последнее) с цифрами доски и
-- готовым текстом для WhatsApp. Стоп: «Хочу в клуб», лид стал резидентом,
-- «Не напоминать», ссылка отключена или истекла.
ALTER TABLE board_links ADD COLUMN IF NOT EXISTS wa_phone    TEXT        NOT NULL DEFAULT '';
ALTER TABLE board_links ADD COLUMN IF NOT EXISTS sent_at     TIMESTAMPTZ;
ALTER TABLE board_links ADD COLUMN IF NOT EXISTS fu_stage    INT         NOT NULL DEFAULT 0; -- reminders already handled: 0..3
ALTER TABLE board_links ADD COLUMN IF NOT EXISTS fu_next_at  TIMESTAMPTZ;                    -- the next check (NULL: none)
ALTER TABLE board_links ADD COLUMN IF NOT EXISTS fu_stop     TEXT        NOT NULL DEFAULT ''; -- join | resident | owner | revoked | expired | done
ALTER TABLE board_links ADD COLUMN IF NOT EXISTS fu_stop_at  TIMESTAMPTZ;

CREATE INDEX IF NOT EXISTS board_links_followup ON board_links (fu_next_at) WHERE fu_next_at IS NOT NULL AND fu_stop = '';
