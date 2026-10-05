package pg

import (
	"context"
	"encoding/json"
	"strings"
	"time"
)

// R38b: what the CRM base import reads (handlers/http/crm_base.go).

// SheetsByName: the sheets the server keeps as displayed (club_sheets), only these names.
func (r *PlatformRepo) SheetsByName(ctx context.Context, names []string) (map[string][][]string, error) {
	rows, err := r.db.Pool.Query(ctx, `SELECT name, rows FROM club_sheets WHERE name = ANY($1)`, names)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string][][]string{}
	for rows.Next() {
		var name string
		var raw []byte
		if err := rows.Scan(&name, &raw); err != nil {
			return nil, err
		}
		var grid [][]string
		if json.Unmarshal(raw, &grid) == nil {
			out[name] = grid
		}
	}
	return out, rows.Err()
}

// BotStart: the first /start of a person in a private chat with the bot
// (the server keeps updates for 30 days).
type BotStart struct {
	ChatID   int64
	First    string
	Last     string
	Username string
	Param    string
	At       time.Time
}

func (r *PlatformRepo) BotStarts(ctx context.Context) ([]BotStart, error) {
	rows, err := r.db.Pool.Query(ctx, `
		SELECT DISTINCT ON (chat_id) chat_id, received_at,
		  COALESCE(body->'message'->'from'->>'first_name', ''), COALESCE(body->'message'->'from'->>'last_name', ''),
		  COALESCE(body->'message'->'from'->>'username', ''), COALESCE(body->'message'->>'text', '')
		FROM bot_updates
		WHERE kind = 'message' AND chat_id > 0 AND body->'message'->'chat'->>'type' = 'private'
		  AND body->'message'->>'text' LIKE '/start%'
		ORDER BY chat_id, received_at`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []BotStart{}
	for rows.Next() {
		var s BotStart
		var text string
		if err := rows.Scan(&s.ChatID, &s.At, &s.First, &s.Last, &s.Username, &text); err != nil {
			return nil, err
		}
		if _, p, ok := strings.Cut(strings.TrimSpace(text), " "); ok {
			s.Param = strings.TrimSpace(p)
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

// ClubPeople: Telegram ids and names of everyone in the club list (residents,
// former residents, team): they are not leads.
func (r *PlatformRepo) ClubPeople(ctx context.Context) (map[int64]bool, map[string]bool, error) {
	ids, names := map[int64]bool{}, map[string]bool{}
	rows, err := r.db.Pool.Query(ctx, `SELECT name, COALESCE(tg_id, 0) FROM club_residents`)
	if err != nil {
		return ids, names, err
	}
	defer rows.Close()
	for rows.Next() {
		var n string
		var id int64
		if err := rows.Scan(&n, &id); err != nil {
			return ids, names, err
		}
		if id > 0 {
			ids[id] = true
		}
		if n = strings.ToLower(strings.TrimSpace(n)); n != "" {
			names[n] = true
		}
	}
	return ids, names, rows.Err()
}
