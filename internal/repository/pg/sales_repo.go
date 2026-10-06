package pg

import (
	"context"
	"time"
)

// R51 (handlers/http/sales_*.go): the residents' daily reports by day.

// ReportDay: how many days in the range a person sent a report.
type ReportDay struct {
	TgID int64
	Name string
	Days int
}

// ReportDays counts the days (Almaty) with at least one report, per sender.
func (r *ClubRepo) ReportDays(ctx context.Context, from, to time.Time) ([]ReportDay, error) {
	rows, err := r.db.Pool.Query(ctx, `SELECT COALESCE(tg_user_id, 0), max(name),
		count(DISTINCT (at AT TIME ZONE 'Asia/Almaty')::date)
		FROM club_reports WHERE at >= $1 AND at <= $2
		GROUP BY COALESCE(tg_user_id, 0), CASE WHEN tg_user_id IS NULL THEN lower(name) ELSE '' END`, from, to)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ReportDay
	for rows.Next() {
		var d ReportDay
		if err := rows.Scan(&d.TgID, &d.Name, &d.Days); err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}
