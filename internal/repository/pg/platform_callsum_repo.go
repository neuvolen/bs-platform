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
