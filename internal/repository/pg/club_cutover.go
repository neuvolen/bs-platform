package pg

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
)

// The sheet cutover (club.SheetMode): the server keeps everything itself.

// SetMaster records who keeps the club's data: "server" or "sheet".
func (r *ClubRepo) SetMaster(ctx context.Context, m string) error {
	_, err := r.db.Pool.Exec(ctx, `INSERT INTO club_meta (key, value) VALUES ('master', $1)
		ON CONFLICT (key) DO UPDATE SET value = EXCLUDED.value`, m)
	return err
}

// RawSheet is one sheet the server keeps as displayed (club_sheets).
func (r *ClubRepo) RawSheet(ctx context.Context, name string) ([][]string, bool, error) {
	var raw []byte
	err := r.db.Pool.QueryRow(ctx, `SELECT rows FROM club_sheets WHERE name = $1`, name).Scan(&raw)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	var rows [][]string
	if err := json.Unmarshal(raw, &rows); err != nil {
		return nil, false, err
	}
	return rows, true, nil
}

// ReportLog is one row of «Лог отчётов».
type ReportLog struct {
	At        time.Time
	Username  string
	Name      string
	Text      string
	TgUserID  int64
	Thread    string
	Late      bool
	MessageID int64
}

// AddReportLog adds a report to «Лог отчётов» once (by sender and message).
func (r *ClubRepo) AddReportLog(ctx context.Context, x ReportLog) error {
	_, err := r.db.Pool.Exec(ctx, `INSERT INTO club_reports (at, username, name, text, tg_user_id, thread, late, message_id, shown_at)
		SELECT $1,$2,$3,$4,$5,$6,$7,$8,$1
		WHERE NOT EXISTS (SELECT 1 FROM club_reports WHERE tg_user_id = $5 AND message_id = $8)`,
		x.At, x.Username, x.Name, x.Text, x.TgUserID, x.Thread, x.Late, x.MessageID)
	return err
}

// SyncReportLog puts back into «Лог отчётов» the reports the server bot
// counted since the given time and the log lacks (an import replaced it
// with the sheet's copy, which no longer gets the bot's messages).
func (r *ClubRepo) SyncReportLog(ctx context.Context, since time.Time) (int64, error) {
	tag, err := r.db.Pool.Exec(ctx, `INSERT INTO club_reports (at, username, name, text, tg_user_id, thread, late, message_id, shown_at)
		SELECT b.at, b.username, b.resident, b.text, b.tg_user_id, b.thread, b.late, b.message_id, b.at
		FROM bot_reports b
		WHERE b.verdict = 'report' AND b.resident <> '' AND b.at >= $1
		  AND NOT EXISTS (SELECT 1 FROM club_reports c WHERE c.tg_user_id = b.tg_user_id
		      AND (c.message_id = b.message_id OR (c.message_id IS NULL AND abs(extract(epoch FROM c.at - b.at)) < 120)))
		ORDER BY b.at`, since)
	if err != nil {
		return 0, err
	}
	return tag.RowsAffected(), nil
}

// SettleOpenWrites marks every write still on its way to the sheet as done
// on the server: after the cutover nothing goes to the sheet any more.
func (r *ClubRepo) SettleOpenWrites(ctx context.Context, why string) (int64, error) {
	tag, err := r.db.Pool.Exec(ctx, `UPDATE club_writes SET status = 'sent', sent_at = now(), result = $1, last_error = ''
		WHERE status IN ('pending','unknown')`, cut(why, 500))
	if err != nil {
		return 0, err
	}
	return tag.RowsAffected(), nil
}

// FineExists: a fine of this type for this resident on this day is there.
func (r *ClubRepo) FineExists(ctx context.Context, name, typ string, day time.Time) (bool, error) {
	var n int
	err := r.db.Pool.QueryRow(ctx, `SELECT count(*) FROM club_fines WHERE resident = $1 AND type = $2 AND date = $3`,
		name, typ, day.Format("2006-01-02")).Scan(&n)
	return n > 0, err
}

// AppliesHere: the write changes something the server keeps (club tables or
// a kept sheet), so the server can do it without the script.
func AppliesHere(action string) bool { return appliesHere(action) }

// Setting is a text of «Настройки» (club_settings).
func (r *ClubRepo) Setting(ctx context.Context, key string) (string, error) {
	var v string
	err := r.db.Pool.QueryRow(ctx, `SELECT value FROM club_settings WHERE key = $1`, key).Scan(&v)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", nil
	}
	return v, err
}
