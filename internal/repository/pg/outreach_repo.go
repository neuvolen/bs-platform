package pg

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
)

// R38c: events with an RSVP, broadcasts and the WhatsApp queue
// (migrations/0023_r38c_outreach.sql).
type OutreachRepo struct{ db *DB }

func NewOutreachRepo(db *DB) *OutreachRepo { return &OutreachRepo{db: db} }

// ── resident channels ──

type ResidentChannel struct {
	NameKey   string    `json:"nameKey"`
	Name      string    `json:"name"`
	Channel   string    `json:"channel"` // tg | wa
	Phone     string    `json:"phone"`
	UpdatedAt time.Time `json:"updatedAt"`
	UpdatedBy string    `json:"updatedBy"`
}

func (r *OutreachRepo) Channels(ctx context.Context) (map[string]ResidentChannel, error) {
	rows, err := r.db.Pool.Query(ctx, `SELECT name_key, name, channel, phone, updated_at, updated_by FROM resident_channels`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]ResidentChannel{}
	for rows.Next() {
		var c ResidentChannel
		if err := rows.Scan(&c.NameKey, &c.Name, &c.Channel, &c.Phone, &c.UpdatedAt, &c.UpdatedBy); err != nil {
			return nil, err
		}
		out[c.NameKey] = c
	}
	return out, rows.Err()
}

func (r *OutreachRepo) PutChannel(ctx context.Context, c ResidentChannel) error {
	_, err := r.db.Pool.Exec(ctx, `INSERT INTO resident_channels (name_key, name, channel, phone, updated_by) VALUES ($1,$2,$3,$4,$5)
		ON CONFLICT (name_key) DO UPDATE SET name = EXCLUDED.name, channel = EXCLUDED.channel, phone = EXCLUDED.phone,
		updated_by = EXCLUDED.updated_by, updated_at = now()`, c.NameKey, c.Name, c.Channel, c.Phone, c.UpdatedBy)
	return err
}

// AddChannelOnce: the one-time merge: nothing changes when the resident has a row already.
func (r *OutreachRepo) AddChannelOnce(ctx context.Context, c ResidentChannel) (bool, error) {
	tag, err := r.db.Pool.Exec(ctx, `INSERT INTO resident_channels (name_key, name, channel, phone, updated_by) VALUES ($1,$2,$3,$4,$5)
		ON CONFLICT (name_key) DO NOTHING`, c.NameKey, c.Name, c.Channel, c.Phone, c.UpdatedBy)
	if err != nil {
		return false, err
	}
	return tag.RowsAffected() == 1, nil
}

// ── WhatsApp queue ──

type WAItem struct {
	ID        int64      `json:"id"`
	Dedup     string     `json:"-"`
	Resident  string     `json:"resident"`
	Phone     string     `json:"phone"`
	Kind      string     `json:"kind"`
	Text      string     `json:"text"`
	Status    string     `json:"status"`
	Error     string     `json:"error,omitempty"`
	CreatedAt time.Time  `json:"createdAt"`
	ExpiresAt *time.Time `json:"expiresAt,omitempty"`
	SentAt    *time.Time `json:"sentAt,omitempty"`
	SentBy    string     `json:"sentBy,omitempty"`
}

const waCols = `id, dedup, resident, phone, kind, text, status, error, created_at, expires_at, sent_at, sent_by`

func scanWA(row pgx.Row) (WAItem, error) {
	var w WAItem
	err := row.Scan(&w.ID, &w.Dedup, &w.Resident, &w.Phone, &w.Kind, &w.Text, &w.Status, &w.Error, &w.CreatedAt, &w.ExpiresAt, &w.SentAt, &w.SentBy)
	return w, err
}

// WAAdd queues a message once per dedup; inserted false: it was there already.
func (r *OutreachRepo) WAAdd(ctx context.Context, w WAItem) (int64, bool, error) {
	var id int64
	err := r.db.Pool.QueryRow(ctx, `INSERT INTO wa_outbox (dedup, resident, phone, kind, text, expires_at) VALUES ($1,$2,$3,$4,$5,$6)
		ON CONFLICT (dedup) DO NOTHING RETURNING id`, w.Dedup, w.Resident, w.Phone, w.Kind, w.Text, w.ExpiresAt).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, false, nil
	}
	return id, err == nil, err
}

func (r *OutreachRepo) WAGet(ctx context.Context, id int64) (*WAItem, error) {
	w, err := scanWA(r.db.Pool.QueryRow(ctx, `SELECT `+waCols+` FROM wa_outbox WHERE id = $1`, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &w, nil
}

// WAList: the pending ones (oldest first) and the latest others.
func (r *OutreachRepo) WAList(ctx context.Context, limit int) ([]WAItem, error) {
	rows, err := r.db.Pool.Query(ctx, `(SELECT `+waCols+` FROM wa_outbox WHERE status = 'pending' ORDER BY created_at LIMIT 200)
		UNION ALL (SELECT `+waCols+` FROM wa_outbox WHERE status <> 'pending' ORDER BY coalesce(sent_at, created_at) DESC LIMIT $1)`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []WAItem
	for rows.Next() {
		w, err := scanWA(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, w)
	}
	return out, rows.Err()
}

// WASet moves an item from one of the states in from to to; false when it was not in them.
func (r *OutreachRepo) WASet(ctx context.Context, id int64, from []string, to, by, errText string) (bool, error) {
	sent := to == "sent" || to == "auto"
	tag, err := r.db.Pool.Exec(ctx, `UPDATE wa_outbox SET status = $3, sent_by = CASE WHEN $4 <> '' THEN $4 ELSE sent_by END, error = $5,
		sent_at = CASE WHEN $6 THEN now() WHEN $3 = 'pending' THEN NULL ELSE sent_at END
		WHERE id = $1 AND status = ANY($2)`, id, from, to, by, errText, sent)
	if err != nil {
		return false, err
	}
	return tag.RowsAffected() == 1, nil
}

func (r *OutreachRepo) WASetError(ctx context.Context, id int64, errText string) error {
	_, err := r.db.Pool.Exec(ctx, `UPDATE wa_outbox SET error = $2 WHERE id = $1`, id, errText)
	return err
}

// WAToNotify: pending items the team was not told about yet (expired ones are closed first).
func (r *OutreachRepo) WAToNotify(ctx context.Context) ([]WAItem, error) {
	if _, err := r.db.Pool.Exec(ctx, `UPDATE wa_outbox SET status = 'expired' WHERE status = 'pending' AND expires_at IS NOT NULL AND expires_at < now()`); err != nil {
		return nil, err
	}
	rows, err := r.db.Pool.Query(ctx, `SELECT `+waCols+` FROM wa_outbox WHERE status = 'pending' AND notified_at IS NULL ORDER BY created_at LIMIT 50`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []WAItem
	for rows.Next() {
		w, err := scanWA(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, w)
	}
	return out, rows.Err()
}

func (r *OutreachRepo) WAMarkNotified(ctx context.Context, ids []int64) error {
	_, err := r.db.Pool.Exec(ctx, `UPDATE wa_outbox SET notified_at = now() WHERE id = ANY($1)`, ids)
	return err
}

func (r *OutreachRepo) WAPending(ctx context.Context) (int, error) {
	var n int
	err := r.db.Pool.QueryRow(ctx, `SELECT count(*) FROM wa_outbox WHERE status = 'pending' AND (expires_at IS NULL OR expires_at > now())`).Scan(&n)
	return n, err
}

// ── events ──

type ClubEvent struct {
	ID          string    `json:"id"`
	Title       string    `json:"title"`
	StartsAt    time.Time `json:"startsAt"`
	City        string    `json:"city"`
	Address     string    `json:"address"`
	AddressNote string    `json:"addressNote"`
	Price       int64     `json:"price"`
	Seats       int       `json:"seats"`
	PayLink     string    `json:"payLink"`
	About       string    `json:"about"`
	Status      string    `json:"status"`
	FullNoted   bool      `json:"-"`
	UpdatedAt   time.Time `json:"updatedAt"`
}

const evCols = `id, title, starts_at, city, address, address_note, price, seats, pay_link, about, status, full_noted, updated_at`

func scanEvent(row pgx.Row) (ClubEvent, error) {
	var e ClubEvent
	err := row.Scan(&e.ID, &e.Title, &e.StartsAt, &e.City, &e.Address, &e.AddressNote, &e.Price, &e.Seats, &e.PayLink, &e.About, &e.Status, &e.FullNoted, &e.UpdatedAt)
	return e, err
}

// EventAddOnce creates the event unless one with the id exists (or existed: see seeds).
func (r *OutreachRepo) EventAddOnce(ctx context.Context, e ClubEvent) (bool, error) {
	tag, err := r.db.Pool.Exec(ctx, `INSERT INTO club_events (id, title, starts_at, city, address, address_note, price, seats, pay_link, about, status)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11) ON CONFLICT (id) DO NOTHING`,
		e.ID, e.Title, e.StartsAt, e.City, e.Address, e.AddressNote, e.Price, e.Seats, e.PayLink, e.About, e.Status)
	if err != nil {
		return false, err
	}
	return tag.RowsAffected() == 1, nil
}

func (r *OutreachRepo) EventPut(ctx context.Context, e ClubEvent) error {
	_, err := r.db.Pool.Exec(ctx, `UPDATE club_events SET title=$2, starts_at=$3, city=$4, address=$5, address_note=$6, price=$7, seats=$8,
		pay_link=$9, about=$10, status=$11, full_noted=$12, updated_at=now() WHERE id=$1`,
		e.ID, e.Title, e.StartsAt, e.City, e.Address, e.AddressNote, e.Price, e.Seats, e.PayLink, e.About, e.Status, e.FullNoted)
	return err
}

func (r *OutreachRepo) EventGet(ctx context.Context, id string) (*ClubEvent, error) {
	e, err := scanEvent(r.db.Pool.QueryRow(ctx, `SELECT `+evCols+` FROM club_events WHERE id = $1`, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &e, nil
}

// Events: upcoming first, then the last past ones.
func (r *OutreachRepo) Events(ctx context.Context) ([]ClubEvent, error) {
	rows, err := r.db.Pool.Query(ctx, `SELECT `+evCols+` FROM club_events WHERE starts_at > now() - interval '60 days' ORDER BY starts_at`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ClubEvent
	for rows.Next() {
		e, err := scanEvent(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

type Rsvp struct {
	EventID   string    `json:"-"`
	Who       string    `json:"who"`
	Name      string    `json:"name"`
	Username  string    `json:"username,omitempty"`
	Phone     string    `json:"phone,omitempty"`
	Kind      string    `json:"kind"`
	Status    string    `json:"status"`
	WantPhone bool      `json:"wantPhone,omitempty"`
	CreatedAt time.Time `json:"createdAt"`
}

const rsvpCols = `event_id, who, name, username, phone, kind, status, want_phone, created_at`

func scanRsvp(row pgx.Row) (Rsvp, error) {
	var x Rsvp
	err := row.Scan(&x.EventID, &x.Who, &x.Name, &x.Username, &x.Phone, &x.Kind, &x.Status, &x.WantPhone, &x.CreatedAt)
	return x, err
}

func (r *OutreachRepo) Rsvps(ctx context.Context, event string) ([]Rsvp, error) {
	rows, err := r.db.Pool.Query(ctx, `SELECT `+rsvpCols+` FROM event_rsvps WHERE event_id = $1 ORDER BY created_at`, event)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Rsvp
	for rows.Next() {
		x, err := scanRsvp(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, x)
	}
	return out, rows.Err()
}

func (r *OutreachRepo) RsvpGet(ctx context.Context, event, who string) (*Rsvp, error) {
	x, err := scanRsvp(r.db.Pool.QueryRow(ctx, `SELECT `+rsvpCols+` FROM event_rsvps WHERE event_id = $1 AND who = $2`, event, who))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &x, nil
}

// RsvpJoin: «Иду». In one transaction, under a lock of the event: going
// while seats are left, the waitlist after. An existing going / waitlist
// row is returned as it is (pressing twice changes nothing).
func (r *OutreachRepo) RsvpJoin(ctx context.Context, x Rsvp, seats int) (Rsvp, bool, error) {
	tx, err := r.db.Pool.Begin(ctx)
	if err != nil {
		return x, false, err
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	if _, err := tx.Exec(ctx, `SELECT 1 FROM club_events WHERE id = $1 FOR UPDATE`, x.EventID); err != nil {
		return x, false, err
	}
	cur, err := scanRsvp(tx.QueryRow(ctx, `SELECT `+rsvpCols+` FROM event_rsvps WHERE event_id = $1 AND who = $2`, x.EventID, x.Who))
	if err == nil && cur.Status != "cancelled" {
		return cur, false, tx.Commit(ctx)
	}
	var going int
	if err := tx.QueryRow(ctx, `SELECT count(*) FROM event_rsvps WHERE event_id = $1 AND status = 'going'`, x.EventID).Scan(&going); err != nil {
		return x, false, err
	}
	x.Status = "going"
	if seats > 0 && going >= seats {
		x.Status = "waitlist"
	}
	if _, err := tx.Exec(ctx, `INSERT INTO event_rsvps (event_id, who, name, username, phone, kind, status, want_phone) VALUES ($1,$2,$3,$4,$5,$6,$7,$8)
		ON CONFLICT (event_id, who) DO UPDATE SET name = EXCLUDED.name, username = EXCLUDED.username,
		phone = CASE WHEN EXCLUDED.phone <> '' THEN EXCLUDED.phone ELSE event_rsvps.phone END, kind = EXCLUDED.kind,
		status = EXCLUDED.status, want_phone = EXCLUDED.want_phone, created_at = now(), updated_at = now()`,
		x.EventID, x.Who, x.Name, x.Username, x.Phone, x.Kind, x.Status, x.WantPhone); err != nil {
		return x, false, err
	}
	x.CreatedAt = time.Now()
	return x, true, tx.Commit(ctx)
}

// RsvpCancel: «Не смогу». The first of the waitlist takes the seat; it is returned.
func (r *OutreachRepo) RsvpCancel(ctx context.Context, event, who string, seats int) (cancelled bool, promoted *Rsvp, err error) {
	tx, err := r.db.Pool.Begin(ctx)
	if err != nil {
		return false, nil, err
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	if _, err := tx.Exec(ctx, `SELECT 1 FROM club_events WHERE id = $1 FOR UPDATE`, event); err != nil {
		return false, nil, err
	}
	tag, err := tx.Exec(ctx, `UPDATE event_rsvps SET status = 'cancelled', updated_at = now() WHERE event_id = $1 AND who = $2 AND status <> 'cancelled'`, event, who)
	if err != nil {
		return false, nil, err
	}
	if tag.RowsAffected() == 0 {
		return false, nil, tx.Commit(ctx)
	}
	var going int
	if err := tx.QueryRow(ctx, `SELECT count(*) FROM event_rsvps WHERE event_id = $1 AND status = 'going'`, event).Scan(&going); err != nil {
		return false, nil, err
	}
	if seats <= 0 || going < seats {
		p, err := scanRsvp(tx.QueryRow(ctx, `UPDATE event_rsvps SET status = 'going', updated_at = now() WHERE (event_id, who) =
			(SELECT event_id, who FROM event_rsvps WHERE event_id = $1 AND status = 'waitlist' ORDER BY created_at LIMIT 1)
			RETURNING `+rsvpCols, event))
		if err == nil {
			promoted = &p
		} else if !errors.Is(err, pgx.ErrNoRows) {
			return false, nil, err
		}
	}
	return true, promoted, tx.Commit(ctx)
}

// RsvpPromote: free seats go to the waitlist, oldest first; the promoted are returned.
func (r *OutreachRepo) RsvpPromote(ctx context.Context, event string, seats int) ([]Rsvp, error) {
	tx, err := r.db.Pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	if _, err := tx.Exec(ctx, `SELECT 1 FROM club_events WHERE id = $1 FOR UPDATE`, event); err != nil {
		return nil, err
	}
	var going int
	if err := tx.QueryRow(ctx, `SELECT count(*) FROM event_rsvps WHERE event_id = $1 AND status = 'going'`, event).Scan(&going); err != nil {
		return nil, err
	}
	free := 1 << 20
	if seats > 0 {
		free = seats - going
	}
	var out []Rsvp
	if free > 0 {
		rows, err := tx.Query(ctx, `UPDATE event_rsvps SET status = 'going', updated_at = now() WHERE (event_id, who) IN
			(SELECT event_id, who FROM event_rsvps WHERE event_id = $1 AND status = 'waitlist' ORDER BY created_at LIMIT $2)
			RETURNING `+rsvpCols, event, free)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			x, err := scanRsvp(rows)
			if err != nil {
				rows.Close()
				return nil, err
			}
			out = append(out, x)
		}
		rows.Close()
	}
	return out, tx.Commit(ctx)
}

// RsvpPhone: the phone a participant shared; false when nobody was waiting for it.
func (r *OutreachRepo) RsvpPhone(ctx context.Context, who, phone string) ([]string, error) {
	rows, err := r.db.Pool.Query(ctx, `UPDATE event_rsvps SET phone = $2, want_phone = false, updated_at = now()
		WHERE who = $1 AND want_phone RETURNING event_id`, who, phone)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ev []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ev = append(ev, id)
	}
	return ev, rows.Err()
}

// NoteOnce: true the first time (event, who, kind) is marked: send it then.
func (r *OutreachRepo) NoteOnce(ctx context.Context, event, who, kind string) (bool, error) {
	tag, err := r.db.Pool.Exec(ctx, `INSERT INTO event_notes (event_id, who, kind) VALUES ($1,$2,$3) ON CONFLICT DO NOTHING`, event, who, kind)
	if err != nil {
		return false, err
	}
	return tag.RowsAffected() == 1, nil
}

// ── campaigns ──

type Campaign struct {
	ID         string          `json:"id"`
	Title      string          `json:"title"`
	EventID    string          `json:"eventId,omitempty"`
	Text       string          `json:"text"`
	Audience   json.RawMessage `json:"audience"`
	Status     string          `json:"status"`
	Forced     bool            `json:"forced,omitempty"`
	UpdatedAt  time.Time       `json:"updatedAt"`
	UpdatedBy  string          `json:"updatedBy,omitempty"`
	StartedAt  *time.Time      `json:"startedAt,omitempty"`
	StartedBy  string          `json:"startedBy,omitempty"`
	FinishedAt *time.Time      `json:"finishedAt,omitempty"`
	TestedAt   *time.Time      `json:"testedAt,omitempty"`
}

const cmpCols = `id, title, event_id, text, audience, status, forced, updated_at, updated_by, started_at, started_by, finished_at, tested_at`

func scanCampaign(row pgx.Row) (Campaign, error) {
	var c Campaign
	err := row.Scan(&c.ID, &c.Title, &c.EventID, &c.Text, &c.Audience, &c.Status, &c.Forced, &c.UpdatedAt, &c.UpdatedBy, &c.StartedAt, &c.StartedBy, &c.FinishedAt, &c.TestedAt)
	return c, err
}

func (r *OutreachRepo) CampaignAddOnce(ctx context.Context, c Campaign) (bool, error) {
	if len(c.Audience) == 0 {
		c.Audience = json.RawMessage(`{}`)
	}
	tag, err := r.db.Pool.Exec(ctx, `INSERT INTO bc_campaigns (id, title, event_id, text, audience) VALUES ($1,$2,$3,$4,$5) ON CONFLICT (id) DO NOTHING`,
		c.ID, c.Title, c.EventID, c.Text, c.Audience)
	if err != nil {
		return false, err
	}
	return tag.RowsAffected() == 1, nil
}

func (r *OutreachRepo) CampaignGet(ctx context.Context, id string) (*Campaign, error) {
	c, err := scanCampaign(r.db.Pool.QueryRow(ctx, `SELECT `+cmpCols+` FROM bc_campaigns WHERE id = $1`, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &c, nil
}

func (r *OutreachRepo) Campaigns(ctx context.Context) ([]Campaign, error) {
	rows, err := r.db.Pool.Query(ctx, `SELECT `+cmpCols+` FROM bc_campaigns ORDER BY created_at DESC LIMIT 50`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Campaign
	for rows.Next() {
		c, err := scanCampaign(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// CampaignEdit changes a draft only.
func (r *OutreachRepo) CampaignEdit(ctx context.Context, id, text string, audience json.RawMessage, by string) (bool, error) {
	tag, err := r.db.Pool.Exec(ctx, `UPDATE bc_campaigns SET text = $2, audience = $3, updated_by = $4, updated_at = now() WHERE id = $1 AND status = 'draft'`,
		id, text, audience, by)
	if err != nil {
		return false, err
	}
	return tag.RowsAffected() == 1, nil
}

func (r *OutreachRepo) CampaignTested(ctx context.Context, id string) error {
	_, err := r.db.Pool.Exec(ctx, `UPDATE bc_campaigns SET tested_at = now() WHERE id = $1`, id)
	return err
}

// CampaignStart: draft → sending with its recipients, in one transaction;
// false when it is not a draft any more (a second click, another tab).
func (r *OutreachRepo) CampaignStart(ctx context.Context, id, by string, forced bool, targets []BcTarget) (bool, error) {
	tx, err := r.db.Pool.Begin(ctx)
	if err != nil {
		return false, err
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	tag, err := tx.Exec(ctx, `UPDATE bc_campaigns SET status = 'sending', started_at = now(), started_by = $2, forced = $3 WHERE id = $1 AND status = 'draft'`, id, by, forced)
	if err != nil {
		return false, err
	}
	if tag.RowsAffected() == 0 {
		return false, nil
	}
	for _, t := range targets {
		if _, err := tx.Exec(ctx, `INSERT INTO bc_sends (campaign_id, target, name, kind) VALUES ($1,$2,$3,$4) ON CONFLICT DO NOTHING`, id, t.Target, t.Name, t.Kind); err != nil {
			return false, err
		}
	}
	return true, tx.Commit(ctx)
}

func (r *OutreachRepo) CampaignsSending(ctx context.Context) ([]string, error) {
	rows, err := r.db.Pool.Query(ctx, `SELECT id FROM bc_campaigns WHERE status = 'sending' ORDER BY started_at`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

func (r *OutreachRepo) CampaignDone(ctx context.Context, id string) (bool, error) {
	tag, err := r.db.Pool.Exec(ctx, `UPDATE bc_campaigns SET status = 'done', finished_at = now() WHERE id = $1 AND status = 'sending'`, id)
	if err != nil {
		return false, err
	}
	return tag.RowsAffected() == 1, nil
}

type BcTarget struct {
	Target string `json:"target"`
	Name   string `json:"name"`
	Kind   string `json:"kind"`
	Status string `json:"status,omitempty"`
	Error  string `json:"error,omitempty"`
}

// SendClaim takes the next recipient of a campaign: pending → claimed.
func (r *OutreachRepo) SendClaim(ctx context.Context, id string) (*BcTarget, error) {
	var t BcTarget
	err := r.db.Pool.QueryRow(ctx, `UPDATE bc_sends SET status = 'claimed' WHERE (campaign_id, target) =
		(SELECT campaign_id, target FROM bc_sends WHERE campaign_id = $1 AND status = 'pending' ORDER BY target LIMIT 1 FOR UPDATE SKIP LOCKED)
		RETURNING target, name, kind`, id).Scan(&t.Target, &t.Name, &t.Kind)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &t, nil
}

func (r *OutreachRepo) SendMark(ctx context.Context, id, target, status, errText string) error {
	_, err := r.db.Pool.Exec(ctx, `UPDATE bc_sends SET status = $3, error = $4, sent_at = now() WHERE campaign_id = $1 AND target = $2`, id, target, status, errText)
	return err
}

// SendsOrphaned: claimed rows left by a restart: they may have gone out, so
// they are never sent again (unknown).
func (r *OutreachRepo) SendsOrphaned(ctx context.Context) error {
	_, err := r.db.Pool.Exec(ctx, `UPDATE bc_sends SET status = 'unknown' WHERE status = 'claimed'`)
	return err
}

func (r *OutreachRepo) SendStats(ctx context.Context, id string) (map[string]int, error) {
	rows, err := r.db.Pool.Query(ctx, `SELECT status, count(*) FROM bc_sends WHERE campaign_id = $1 GROUP BY status`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]int{}
	for rows.Next() {
		var s string
		var n int
		if err := rows.Scan(&s, &n); err != nil {
			return nil, err
		}
		out[s] = n
	}
	return out, rows.Err()
}

func (r *OutreachRepo) SendFailures(ctx context.Context, id string, limit int) ([]BcTarget, error) {
	rows, err := r.db.Pool.Query(ctx, `SELECT target, name, kind, status, error FROM bc_sends WHERE campaign_id = $1 AND status IN ('failed','unknown') ORDER BY name LIMIT $2`, id, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []BcTarget
	for rows.Next() {
		var t BcTarget
		if err := rows.Scan(&t.Target, &t.Name, &t.Kind, &t.Status, &t.Error); err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// BotChats: the private chats that wrote to the bot (kept 30 days) with a name.
func (r *OutreachRepo) BotChats(ctx context.Context) (map[int64]string, error) {
	rows, err := r.db.Pool.Query(ctx, `SELECT DISTINCT ON (chat_id) chat_id,
		trim(coalesce(body#>>'{message,from,first_name}', body#>>'{callback_query,from,first_name}', '') || ' ' ||
		     coalesce(body#>>'{message,from,last_name}', body#>>'{callback_query,from,last_name}', ''))
		FROM bot_updates WHERE chat_id > 0 ORDER BY chat_id, received_at DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[int64]string{}
	for rows.Next() {
		var id int64
		var n string
		if err := rows.Scan(&id, &n); err != nil {
			return nil, err
		}
		out[id] = n
	}
	return out, rows.Err()
}
