package pg

import (
	"context"
	"time"
)

// CallBoard: a board as the call link needs it (R75 call): whose it is and
// when it changed last, without its data.
type CallBoard struct {
	ID        string
	Resident  string
	Name      string
	UpdatedAt time.Time
}

// CallBoards: every live board, light (no data).
func (r *PlatformRepo) CallBoards(ctx context.Context) ([]CallBoard, error) {
	rows, err := r.db.Pool.Query(ctx, `SELECT id, resident, name, updated_at FROM platform_boards WHERE NOT deleted`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []CallBoard
	for rows.Next() {
		var b CallBoard
		if err := rows.Scan(&b.ID, &b.Resident, &b.Name, &b.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, b)
	}
	return out, rows.Err()
}

// CallResidentNames: the platform's residents (active or not).
func (r *PlatformRepo) CallResidentNames(ctx context.Context) ([]string, error) {
	rows, err := r.db.Pool.Query(ctx, `SELECT name FROM platform_residents WHERE name <> ''`)
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
