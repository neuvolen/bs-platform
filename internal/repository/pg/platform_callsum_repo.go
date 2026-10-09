package pg

import (
	"context"
	"strings"
)

// R32e: «Саммари разбора» — what the resident gate needs.

func normResName(s string) string {
	s = strings.ToLower(strings.TrimSpace(strings.ReplaceAll(strings.ReplaceAll(s, "ё", "е"), "Ё", "Е")))
	return strings.Join(strings.Fields(s), " ")
}

// ResidentFormat: «Онлайн» / «Офлайн» of a resident in the club data ("" when unknown).
func (r *PlatformRepo) ResidentFormat(ctx context.Context, name string) string {
	want := normResName(name)
	if want == "" {
		return ""
	}
	rows, err := r.db.Pool.Query(ctx, `SELECT name, COALESCE(format, '') FROM club_residents`)
	if err != nil {
		return ""
	}
	defer rows.Close()
	for rows.Next() {
		var n, f string
		if rows.Scan(&n, &f) == nil && normResName(n) == want {
			return strings.TrimSpace(f)
		}
	}
	return ""
}

// CallSumStatuses: job id → its summary state ("draft", "published", or ""
// for a call processed before R32e). Ids without a job are not in the map.
func (r *PlatformRepo) CallSumStatuses(ctx context.Context, ids []string) (map[string]string, error) {
	out := map[string]string{}
	if len(ids) == 0 {
		return out, nil
	}
	rows, err := r.db.Pool.Query(ctx, `SELECT id, COALESCE(result->'sumState'->>'status', '') FROM platform_ai_jobs WHERE kind = 'call' AND id = ANY($1)`, ids)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var id, st string
		if err := rows.Scan(&id, &st); err != nil {
			return nil, err
		}
		out[id] = st
	}
	return out, rows.Err()
}

// BoardCallsGone: R63: the ids of the calls the team deleted from a board
// (data.callsGone), so an older copy of the board does not bring them back.
func (r *PlatformRepo) BoardCallsGone(ctx context.Context, id string) []string {
	var out []string
	rows, err := r.db.Pool.Query(ctx, `SELECT jsonb_array_elements_text(CASE WHEN jsonb_typeof(data->'callsGone') = 'array'
		THEN data->'callsGone' ELSE '[]'::jsonb END) FROM platform_boards WHERE id = $1`, id)
	if err != nil {
		return nil
	}
	defer rows.Close()
	for rows.Next() {
		var s string
		if rows.Scan(&s) == nil && s != "" {
			out = append(out, s)
		}
	}
	return out
}

// DeleteCallJob: R63: a call's job goes away with its recording and summary.
func (r *PlatformRepo) DeleteCallJob(ctx context.Context, id string) error {
	_, err := r.db.Pool.Exec(ctx, `DELETE FROM platform_ai_jobs WHERE id = $1 AND kind = 'call'`, id)
	return err
}

// ResidentPhone: R63: the resident's WhatsApp phone (resident_channels,
// digits), "" when none is saved. The name is matched by case and ё/е.
func (r *PlatformRepo) ResidentPhone(ctx context.Context, name string) string {
	want := normResName(name)
	if want == "" {
		return ""
	}
	rows, err := r.db.Pool.Query(ctx, `SELECT name, phone FROM resident_channels WHERE phone <> ''`)
	if err != nil {
		return ""
	}
	defer rows.Close()
	first := ""
	for rows.Next() {
		var n, p string
		if rows.Scan(&n, &p) != nil {
			continue
		}
		d := strings.Map(func(c rune) rune {
			if c >= '0' && c <= '9' {
				return c
			}
			return -1
		}, p)
		if len(d) < 10 {
			continue
		}
		if normResName(n) == want {
			return d
		}
		if f := strings.Fields(want); first == "" && len(f) > 1 && normResName(n) == f[0] {
			first = d
		}
	}
	return first
}
