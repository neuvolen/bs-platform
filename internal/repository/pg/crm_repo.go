package pg

import (
	"context"
	"time"
)

type CrmMessage struct {
	ID    int64     `json:"id"`
	Phone string    `json:"phone"`
	Dir   string    `json:"dir"`
	Text  string    `json:"text"`
	Name  string    `json:"name,omitempty"`
	At    time.Time `json:"at"`
	Read  bool      `json:"read"`
}

type CrmChat struct {
	Phone  string    `json:"phone"`
	Name   string    `json:"name"`
	Last   string    `json:"last"`
	LastIn bool      `json:"lastIn"`
	At     time.Time `json:"at"`
	Unread int       `json:"unread"`
}

// AddCrmMessage stores a message once (wa_id is unique); returns false for a repeat.
func (r *PlatformRepo) AddCrmMessage(ctx context.Context, m CrmMessage, waID string) (bool, error) {
	var id *string
	if waID != "" {
		id = &waID
	}
	tag, err := r.db.Pool.Exec(ctx, `INSERT INTO crm_messages (phone, dir, text, name, wa_id, at, is_read)
		VALUES ($1,$2,$3,$4,$5,$6,$7) ON CONFLICT (wa_id) DO NOTHING`,
		m.Phone, m.Dir, m.Text, m.Name, id, m.At, m.Dir == "out")
	if err != nil {
		return false, err
	}
	return tag.RowsAffected() > 0, nil
}

func (r *PlatformRepo) CrmChats(ctx context.Context) ([]CrmChat, error) {
	rows, err := r.db.Pool.Query(ctx, `
		SELECT DISTINCT ON (phone) phone,
		  COALESCE((SELECT name FROM crm_messages n WHERE n.phone = m.phone AND n.name <> '' ORDER BY at DESC LIMIT 1), ''),
		  text, dir = 'in', at,
		  (SELECT count(*) FROM crm_messages u WHERE u.phone = m.phone AND u.dir = 'in' AND NOT u.is_read)
		FROM crm_messages m ORDER BY phone, at DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []CrmChat{}
	for rows.Next() {
		var c CrmChat
		if err := rows.Scan(&c.Phone, &c.Name, &c.Last, &c.LastIn, &c.At, &c.Unread); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

func (r *PlatformRepo) CrmMessages(ctx context.Context, phone string) ([]CrmMessage, error) {
	rows, err := r.db.Pool.Query(ctx, `SELECT id, phone, dir, text, name, at, is_read FROM crm_messages
		WHERE phone = $1 ORDER BY at, id LIMIT 500`, phone)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []CrmMessage{}
	for rows.Next() {
		var m CrmMessage
		if err := rows.Scan(&m.ID, &m.Phone, &m.Dir, &m.Text, &m.Name, &m.At, &m.Read); err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

func (r *PlatformRepo) CrmMarkRead(ctx context.Context, phone string) error {
	_, err := r.db.Pool.Exec(ctx, `UPDATE crm_messages SET is_read = true WHERE phone = $1 AND dir = 'in'`, phone)
	return err
}
