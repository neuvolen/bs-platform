package pg

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
)

// ShadowReport is one group message the server bot judged.
type ShadowReport struct {
	UpdateID  int64
	MessageID int64
	At        time.Time
	Day       *time.Time
	Late      bool
	TgUserID  int64
	Resident  string
	Username  string
	Sender    string
	Thread    string
	TextLen   int
	Text      string
	Verdict   string
}

func (r *BotRepo) SaveShadowReport(ctx context.Context, s ShadowReport) error {
	var day any
	if s.Day != nil {
		day = s.Day.Format("2006-01-02")
	} else {
		day = s.At.Format("2006-01-02")
	}
	_, err := r.db.Pool.Exec(ctx, `INSERT INTO bot_reports (update_id, message_id, at, day, late, tg_user_id, resident,
		username, sender, thread, text_len, text, verdict) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13)
		ON CONFLICT (update_id) DO NOTHING`,
		s.UpdateID, s.MessageID, s.At, day, s.Late, s.TgUserID, s.Resident, s.Username, s.Sender, s.Thread, s.TextLen, s.Text, s.Verdict)
	return err
}

// ResidentByTgID is the club resident with this Telegram id; an active one
// wins over a former one.
func (r *BotRepo) ResidentByTgID(ctx context.Context, tg int64) (string, bool, error) {
	var name string
	err := r.db.Pool.QueryRow(ctx, `SELECT name FROM club_residents WHERE tg_id = $1 AND name <> ''
		ORDER BY former, id LIMIT 1`, tg).Scan(&name)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", false, nil
	}
	return name, err == nil, err
}

// ResidentsWithoutTgID: the active residents the bot cannot recognise by id.
func (r *BotRepo) ResidentsWithoutTgID(ctx context.Context) ([]string, error) {
	rows, err := r.db.Pool.Query(ctx, `SELECT name FROM club_residents WHERE tg_id IS NULL AND NOT former AND name <> ''`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			return nil, err
		}
		out = append(out, n)
	}
	return out, rows.Err()
}

// DayReportRow: a report counted for a day, by the server bot or the sheet.
type DayReportRow struct {
	TgID int64
	Name string
}

func (r *BotRepo) scanReports(ctx context.Context, q string, args ...any) ([]DayReportRow, error) {
	rows, err := r.db.Pool.Query(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []DayReportRow
	for rows.Next() {
		var x DayReportRow
		if err := rows.Scan(&x.TgID, &x.Name); err != nil {
			return nil, err
		}
		out = append(out, x)
	}
	return out, rows.Err()
}

// ServerReports: reports the server bot counted for day (not late).
func (r *BotRepo) ServerReports(ctx context.Context, day time.Time) ([]DayReportRow, error) {
	return r.scanReports(ctx, `SELECT tg_user_id, resident FROM bot_reports
		WHERE verdict = 'report' AND NOT late AND day = $1`, day.Format("2006-01-02"))
}

// SheetReports: rows of the sheet's «Лог отчётов» for day (not late), as imported.
func (r *BotRepo) SheetReports(ctx context.Context, day time.Time, loc *time.Location) ([]DayReportRow, error) {
	from := time.Date(day.Year(), day.Month(), day.Day(), 0, 0, 0, 0, loc)
	return r.scanReports(ctx, `SELECT COALESCE(tg_user_id, 0), name FROM club_reports
		WHERE NOT late AND at >= $1 AND at < $2`, from, from.AddDate(0, 0, 1))
}

// MetOn: residents with a meeting that day, from «Лог встреч» and «Расписание».
func (r *BotRepo) MetOn(ctx context.Context, day time.Time) ([]string, error) {
	rows, err := r.db.Pool.Query(ctx, `SELECT resident FROM club_meeting_log WHERE date = $1
		UNION SELECT resident FROM club_meetings WHERE date = $1`, day.Format("2006-01-02"))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			return nil, err
		}
		out = append(out, n)
	}
	return out, rows.Err()
}

// SheetReportFines: «Не сдан отчёт» fines in the sheet for day.
func (r *BotRepo) SheetReportFines(ctx context.Context, day time.Time) ([]string, error) {
	rows, err := r.db.Pool.Query(ctx, `SELECT resident FROM club_fines WHERE type = 'Не сдан отчёт' AND date = $1`,
		day.Format("2006-01-02"))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			return nil, err
		}
		out = append(out, n)
	}
	return out, rows.Err()
}

// FirstUpdateAt: when the server started receiving the bot's updates (within
// the 30 days it keeps them).
func (r *BotRepo) FirstUpdateAt(ctx context.Context) (*time.Time, error) {
	var t *time.Time
	err := r.db.Pool.QueryRow(ctx, `SELECT min(received_at) FROM bot_updates`).Scan(&t)
	return t, err
}

// LastImportAt: the last real import of the sheet's data.
func (r *BotRepo) LastImportAt(ctx context.Context) (*time.Time, error) {
	var t *time.Time
	err := r.db.Pool.QueryRow(ctx, `SELECT max(at) FROM club_imports WHERE NOT dry_run`).Scan(&t)
	return t, err
}

func (r *BotRepo) ShadowDayDone(ctx context.Context, day time.Time) (bool, error) {
	var n int
	err := r.db.Pool.QueryRow(ctx, `SELECT count(*) FROM bot_shadow_days WHERE day = $1`, day.Format("2006-01-02")).Scan(&n)
	return n > 0, err
}

func (r *BotRepo) SaveShadowDay(ctx context.Context, day time.Time, match bool, result any, message string) (bool, error) {
	b, err := json.Marshal(result)
	if err != nil {
		return false, err
	}
	tag, err := r.db.Pool.Exec(ctx, `INSERT INTO bot_shadow_days (day, match, result, message) VALUES ($1,$2,$3,$4)
		ON CONFLICT (day) DO NOTHING`, day.Format("2006-01-02"), match, b, message)
	if err != nil {
		return false, err
	}
	return tag.RowsAffected() == 1, nil
}

// ShadowDays: the last n daily comparisons, newest first.
func (r *BotRepo) ShadowDays(ctx context.Context, n int) ([]json.RawMessage, error) {
	rows, err := r.db.Pool.Query(ctx, `SELECT result FROM bot_shadow_days ORDER BY day DESC LIMIT $1`, n)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []json.RawMessage
	for rows.Next() {
		var b []byte
		if err := rows.Scan(&b); err != nil {
			return nil, err
		}
		out = append(out, b)
	}
	return out, rows.Err()
}

// Club gives the club data the bot checks against.
func (r *BotRepo) Club() *ClubRepo { return &ClubRepo{db: r.db} }
