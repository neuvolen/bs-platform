package pg

import (
	"context"
	"encoding/json"
	"time"
)

// ClubOp is one change of club data (payment, fine, meeting, resident…).
type ClubOp struct {
	ID     int64             `json:"id"`
	At     time.Time         `json:"at"`
	Source string            `json:"source"`
	TgID   int64             `json:"tgId"`
	Who    string            `json:"who"`
	Action string            `json:"action"`
	Params map[string]string `json:"params"`
	OK     bool              `json:"ok"`
	Result string            `json:"result"`
}

func (r *ClubRepo) LogOp(ctx context.Context, op ClubOp) error {
	p, _ := json.Marshal(op.Params)
	_, err := r.db.Pool.Exec(ctx, `INSERT INTO club_ops (source, tg_id, who, action, params, ok, result)
		VALUES ($1,$2,$3,$4,$5,$6,$7)`, op.Source, op.TgID, op.Who, op.Action, p, op.OK, op.Result)
	return err
}

func (r *ClubRepo) Ops(ctx context.Context, limit int) ([]ClubOp, error) {
	if limit <= 0 || limit > 1000 {
		limit = 300
	}
	rows, err := r.db.Pool.Query(ctx, `SELECT id, at, source, tg_id, who, action, params, ok, result
		FROM club_ops ORDER BY at DESC LIMIT $1`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []ClubOp{}
	for rows.Next() {
		var o ClubOp
		var p []byte
		if err := rows.Scan(&o.ID, &o.At, &o.Source, &o.TgID, &o.Who, &o.Action, &p, &o.OK, &o.Result); err != nil {
			return nil, err
		}
		_ = json.Unmarshal(p, &o.Params)
		out = append(out, o)
	}
	return out, rows.Err()
}
