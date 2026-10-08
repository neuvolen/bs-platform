package pg

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
)

// R57: «Ссылка для клиента»: read-only links to a board (migrations/0024).

type BoardLink struct {
	ID         string         `json:"id"`
	BoardID    string         `json:"boardId"`
	CreatedBy  string         `json:"createdBy"`
	CreatedAt  time.Time      `json:"createdAt"`
	ExpiresAt  time.Time      `json:"expiresAt"`
	Revoked    bool           `json:"revoked"`
	Opens      int            `json:"opens"`
	FirstOpen  *time.Time     `json:"firstOpen,omitempty"`
	LastOpen   *time.Time     `json:"lastOpen,omitempty"`
	Seconds    int            `json:"seconds"`
	Sections   map[string]int `json:"sections"`
	Clicks     map[string]int `json:"clicks"`
	Intent     string         `json:"intent"`
	IntentAt   *time.Time     `json:"intentAt,omitempty"`
	JoinNotify *time.Time     `json:"joinNotified,omitempty"`
	// R58: WhatsApp send and the owner's follow-up reminders (migrations/0025)
	WAPhone  string     `json:"waPhone"`
	SentAt   *time.Time `json:"sentAt,omitempty"`
	FuStage  int        `json:"fuStage"`
	FuNextAt *time.Time `json:"fuNextAt,omitempty"`
	FuStop   string     `json:"fuStop"`
	FuStopAt *time.Time `json:"fuStopAt,omitempty"`
}

type BoardLinksRepo struct{ db *DB }

func NewBoardLinksRepo(db *DB) *BoardLinksRepo { return &BoardLinksRepo{db: db} }

const boardLinkCols = `id, board_id, created_by, created_at, expires_at, revoked, opens, first_open_at, last_open_at,
	seconds, sections, clicks, intent, intent_at, join_notified_at, wa_phone, sent_at, fu_stage, fu_next_at, fu_stop, fu_stop_at`

func scanBoardLink(row pgx.Row) (*BoardLink, error) {
	var l BoardLink
	var sec, clk []byte
	if err := row.Scan(&l.ID, &l.BoardID, &l.CreatedBy, &l.CreatedAt, &l.ExpiresAt, &l.Revoked, &l.Opens, &l.FirstOpen, &l.LastOpen,
		&l.Seconds, &sec, &clk, &l.Intent, &l.IntentAt, &l.JoinNotify, &l.WAPhone, &l.SentAt, &l.FuStage, &l.FuNextAt, &l.FuStop, &l.FuStopAt); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	l.Sections, l.Clicks = map[string]int{}, map[string]int{}
	_ = json.Unmarshal(sec, &l.Sections)
	_ = json.Unmarshal(clk, &l.Clicks)
	return &l, nil
}

// Get: a link by id (nil when there is none).
func (r *BoardLinksRepo) Get(ctx context.Context, id string) (*BoardLink, error) {
	return scanBoardLink(r.db.Pool.QueryRow(ctx, `SELECT `+boardLinkCols+` FROM board_links WHERE id=$1`, id))
}

// Live: the board's newest link that is neither revoked nor expired.
func (r *BoardLinksRepo) Live(ctx context.Context, boardID string, now time.Time) (*BoardLink, error) {
	return scanBoardLink(r.db.Pool.QueryRow(ctx, `SELECT `+boardLinkCols+` FROM board_links
		WHERE board_id=$1 AND NOT revoked AND expires_at > $2 ORDER BY created_at DESC LIMIT 1`, boardID, now))
}

// Latest: the board's newest link whatever its state (stats after a revoke).
func (r *BoardLinksRepo) Latest(ctx context.Context, boardID string) (*BoardLink, error) {
	return scanBoardLink(r.db.Pool.QueryRow(ctx, `SELECT `+boardLinkCols+` FROM board_links
		WHERE board_id=$1 ORDER BY created_at DESC LIMIT 1`, boardID))
}

func (r *BoardLinksRepo) Create(ctx context.Context, id, boardID, by string, now, expires time.Time) (*BoardLink, error) {
	if _, err := r.db.Pool.Exec(ctx, `INSERT INTO board_links (id, board_id, created_by, created_at, expires_at) VALUES ($1,$2,$3,$4,$5)`,
		id, boardID, by, now, expires); err != nil {
		return nil, err
	}
	return r.Get(ctx, id)
}

// Revoke switches off every live link of the board.
func (r *BoardLinksRepo) Revoke(ctx context.Context, boardID string, now time.Time) (int64, error) {
	t, err := r.db.Pool.Exec(ctx, `UPDATE board_links SET revoked=true, revoked_at=$2 WHERE board_id=$1 AND NOT revoked`, boardID, now)
	return t.RowsAffected(), err
}

// Open counts a visit; note is true when the team has not heard about an
// open of this link on `day` yet (the mark is set in the same statement).
func (r *BoardLinksRepo) Open(ctx context.Context, id string, now time.Time, day string) (opens int, note bool, err error) {
	var old string
	err = r.db.Pool.QueryRow(ctx, `
		UPDATE board_links b SET opens = b.opens + 1, last_open_at = $2,
			first_open_at = COALESCE(b.first_open_at, $2), open_note_day = $3
		FROM (SELECT id, open_note_day FROM board_links WHERE id=$1 FOR UPDATE) o
		WHERE b.id = o.id
		RETURNING b.opens, o.open_note_day`, id, now, day).Scan(&opens, &old)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, false, nil
	}
	return opens, err == nil && old != day, err
}

// Track adds time on the page, viewed sections and clicks.
func (r *BoardLinksRepo) Track(ctx context.Context, id string, seconds int, sections, clicks []string) error {
	tx, err := r.db.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	var sb, cb []byte
	if err := tx.QueryRow(ctx, `SELECT sections, clicks FROM board_links WHERE id=$1 FOR UPDATE`, id).Scan(&sb, &cb); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		return err
	}
	sec, clk := map[string]int{}, map[string]int{}
	_ = json.Unmarshal(sb, &sec)
	_ = json.Unmarshal(cb, &clk)
	for _, s := range sections {
		if _, ok := sec[s]; ok || len(sec) < 40 {
			sec[s]++
		}
	}
	for _, s := range clicks {
		if _, ok := clk[s]; ok || len(clk) < 40 {
			clk[s]++
		}
	}
	sb, _ = json.Marshal(sec)
	cb, _ = json.Marshal(clk)
	if _, err := tx.Exec(ctx, `UPDATE board_links SET seconds = seconds + $2, sections=$3::jsonb, clicks=$4::jsonb WHERE id=$1`,
		id, seconds, string(sb), string(cb)); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// Intent records join | ask | think; first is true only for the first «join»
// of the link (the team is told once).
func (r *BoardLinksRepo) Intent(ctx context.Context, id, intent string, now time.Time) (first bool, err error) {
	var was *time.Time
	err = r.db.Pool.QueryRow(ctx, `
		UPDATE board_links b SET intent=$2, intent_at=$3,
			join_notified_at = CASE WHEN $2='join' AND b.join_notified_at IS NULL THEN $3 ELSE b.join_notified_at END
		FROM (SELECT id, join_notified_at FROM board_links WHERE id=$1 FOR UPDATE) o
		WHERE b.id=o.id
		RETURNING o.join_notified_at`, id, intent, now).Scan(&was)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	return err == nil && intent == "join" && was == nil, err
}

// ── R58: WhatsApp и напоминания владельцу ──

// Sent marks the link as sent to WhatsApp (phone may be empty) and starts the
// reminders from stage 0 with the first check at next. A second send restarts them.
func (r *BoardLinksRepo) Sent(ctx context.Context, id, phone string, now, next time.Time) error {
	_, err := r.db.Pool.Exec(ctx, `UPDATE board_links SET wa_phone = CASE WHEN $2 <> '' THEN $2 ELSE wa_phone END,
		sent_at=$3, fu_stage=0, fu_next_at=$4, fu_stop='', fu_stop_at=NULL WHERE id=$1`, id, phone, now, next)
	return err
}

// Due: links whose reminder check is due (oldest first).
func (r *BoardLinksRepo) Due(ctx context.Context, now time.Time, limit int) ([]*BoardLink, error) {
	rows, err := r.db.Pool.Query(ctx, `SELECT `+boardLinkCols+` FROM board_links
		WHERE fu_next_at IS NOT NULL AND fu_next_at <= $1 AND fu_stop = '' ORDER BY fu_next_at LIMIT $2`, now, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*BoardLink
	for rows.Next() {
		l, err := scanBoardLink(rows)
		if err != nil {
			return nil, err
		}
		if l != nil {
			out = append(out, l)
		}
	}
	return out, rows.Err()
}

// Advance moves the reminders to stage; next nil: no more checks. It only
// applies when the link is still at stage `from` and not stopped (two servers
// never send the same reminder twice).
func (r *BoardLinksRepo) Advance(ctx context.Context, id string, from, stage int, next *time.Time) (bool, error) {
	t, err := r.db.Pool.Exec(ctx, `UPDATE board_links SET fu_stage=$3, fu_next_at=$4
		WHERE id=$1 AND fu_stage=$2 AND fu_stop=''`, id, from, stage, next)
	return t.RowsAffected() > 0, err
}

// StopFollowups ends the reminders of one link (id) or of every link of a
// board (boardID) with a reason; links already stopped keep their reason.
func (r *BoardLinksRepo) StopFollowups(ctx context.Context, id, boardID, reason string, now time.Time) (int64, error) {
	t, err := r.db.Pool.Exec(ctx, `UPDATE board_links SET fu_stop=$3, fu_stop_at=$4, fu_next_at=NULL
		WHERE (($1 <> '' AND id=$1) OR ($2 <> '' AND board_id=$2)) AND fu_stop='' AND sent_at IS NOT NULL`, id, boardID, reason, now)
	return t.RowsAffected(), err
}
