package pg

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
)

// BotRepo keeps the Telegram updates the server received and whether each
// one reached the Apps Script bot.
type BotRepo struct{ db *DB }

func NewBotRepo(db *DB) *BotRepo { return &BotRepo{db: db} }

// BotUpdate is one stored Telegram update.
type BotUpdate struct {
	UpdateID int64
	Kind     string
	ChatID   int64
	OrderKey string
	Body     []byte
	Tries    int
	Received time.Time
}

// SaveUpdate stores an update once; a repeat from Telegram is ignored.
func (r *BotRepo) SaveUpdate(ctx context.Context, u BotUpdate) (bool, error) {
	var chat any
	if u.ChatID != 0 {
		chat = u.ChatID
	}
	tag, err := r.db.Pool.Exec(ctx, `INSERT INTO bot_updates (update_id, kind, chat_id, order_key, body)
		VALUES ($1, $2, $3, $4, $5) ON CONFLICT (update_id) DO NOTHING`,
		u.UpdateID, u.Kind, chat, u.OrderKey, u.Body)
	if err != nil {
		return false, err
	}
	return tag.RowsAffected() == 1, nil
}

// ClaimNext takes the oldest update that is due, keeping the order inside one
// conversation: an update waits while an earlier one of the same order key is
// still on its way. Different conversations go in parallel.
func (r *BotRepo) ClaimNext(ctx context.Context, lease time.Duration) (*BotUpdate, error) {
	var u BotUpdate
	err := r.db.Pool.QueryRow(ctx, `
		UPDATE bot_updates SET claimed_until = now() + $1::interval, relay_tries = relay_tries + 1
		WHERE update_id = (
			SELECT b.update_id FROM bot_updates b
			WHERE b.relayed_at IS NULL AND NOT b.gave_up AND b.next_try_at <= now()
			  AND (b.claimed_until IS NULL OR b.claimed_until < now())
			  AND NOT EXISTS (
			      SELECT 1 FROM bot_updates e
			      WHERE e.order_key = b.order_key AND e.order_key <> ''
			        AND e.update_id < b.update_id AND e.relayed_at IS NULL AND NOT e.gave_up)
			ORDER BY b.update_id
			LIMIT 1
			FOR UPDATE SKIP LOCKED)
		RETURNING update_id, kind, COALESCE(chat_id, 0), order_key, body, relay_tries, received_at`,
		lease.String()).
		Scan(&u.UpdateID, &u.Kind, &u.ChatID, &u.OrderKey, &u.Body, &u.Tries, &u.Received)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &u, nil
}

func (r *BotRepo) MarkRelayed(ctx context.Context, id int64, status int, took time.Duration) error {
	_, err := r.db.Pool.Exec(ctx, `UPDATE bot_updates SET relayed_at = now(), relay_status = $2, relay_ms = $3,
		relay_error = '', claimed_until = NULL WHERE update_id = $1`, id, status, int(took/time.Millisecond))
	return err
}

// MarkFailed schedules the next try, or gives up for good.
func (r *BotRepo) MarkFailed(ctx context.Context, id int64, status int, msg string, next time.Duration, giveUp bool) error {
	var st any
	if status > 0 {
		st = status
	}
	_, err := r.db.Pool.Exec(ctx, `UPDATE bot_updates SET relay_status = $2, relay_error = $3,
		next_try_at = now() + $4::interval, claimed_until = NULL, gave_up = $5 WHERE update_id = $1`,
		id, st, msg, next.String(), giveUp)
	return err
}

// Retry puts updates that were given up back in the queue.
func (r *BotRepo) Retry(ctx context.Context) (int64, error) {
	tag, err := r.db.Pool.Exec(ctx, `UPDATE bot_updates SET gave_up = false, relay_tries = 0, next_try_at = now()
		WHERE gave_up AND relayed_at IS NULL AND received_at > now() - interval '72 hours'`)
	if err != nil {
		return 0, err
	}
	return tag.RowsAffected(), nil
}

// Cleanup drops relayed updates older than 30 days.
func (r *BotRepo) Cleanup(ctx context.Context) error {
	_, err := r.db.Pool.Exec(ctx, `DELETE FROM bot_updates WHERE received_at < now() - interval '30 days'`)
	return err
}

func (r *BotRepo) GetMeta(ctx context.Context, key string) (string, error) {
	var v string
	err := r.db.Pool.QueryRow(ctx, `SELECT value FROM bot_meta WHERE key = $1`, key).Scan(&v)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", nil
	}
	return v, err
}

func (r *BotRepo) SetMeta(ctx context.Context, key, value string) error {
	_, err := r.db.Pool.Exec(ctx, `INSERT INTO bot_meta (key, value) VALUES ($1, $2)
		ON CONFLICT (key) DO UPDATE SET value = EXCLUDED.value, updated_at = now()`, key, value)
	return err
}

// BotStats is what the status page and the alert need.
type BotStats struct {
	Received24h     int        `json:"received24h"`
	Relayed24h      int        `json:"relayed24h"`
	Waiting         int        `json:"waiting"`
	GaveUp72h       int        `json:"gaveUp72h"`
	OldestWaitingS  int        `json:"oldestWaitingSec"`
	AvgRelayMs24h   int        `json:"avgRelayMs24h"`
	LastReceivedAt  *time.Time `json:"lastReceivedAt,omitempty"`
	LastRelayedAt   *time.Time `json:"lastRelayedAt,omitempty"`
	LastError       string     `json:"lastError,omitempty"`
	LastErrorUpdate int64      `json:"lastErrorUpdate,omitempty"`
}

func (r *BotRepo) Stats(ctx context.Context) (BotStats, error) {
	var s BotStats
	err := r.db.Pool.QueryRow(ctx, `
		SELECT
		  (SELECT count(*) FROM bot_updates WHERE received_at > now() - interval '24 hours'),
		  (SELECT count(*) FROM bot_updates WHERE relayed_at > now() - interval '24 hours'),
		  (SELECT count(*) FROM bot_updates WHERE relayed_at IS NULL AND NOT gave_up),
		  (SELECT count(*) FROM bot_updates WHERE gave_up AND relayed_at IS NULL AND received_at > now() - interval '72 hours'),
		  COALESCE((SELECT EXTRACT(EPOCH FROM now() - min(received_at))::int FROM bot_updates WHERE relayed_at IS NULL AND NOT gave_up), 0),
		  COALESCE((SELECT avg(relay_ms)::int FROM bot_updates WHERE relayed_at > now() - interval '24 hours'), 0),
		  (SELECT max(received_at) FROM bot_updates),
		  (SELECT max(relayed_at) FROM bot_updates)`).
		Scan(&s.Received24h, &s.Relayed24h, &s.Waiting, &s.GaveUp72h, &s.OldestWaitingS, &s.AvgRelayMs24h,
			&s.LastReceivedAt, &s.LastRelayedAt)
	if err != nil {
		return s, err
	}
	err = r.db.Pool.QueryRow(ctx, `SELECT update_id, relay_error FROM bot_updates
		WHERE relay_error <> '' AND relayed_at IS NULL ORDER BY update_id DESC LIMIT 1`).Scan(&s.LastErrorUpdate, &s.LastError)
	if errors.Is(err, pgx.ErrNoRows) {
		err = nil
	}
	return s, err
}
