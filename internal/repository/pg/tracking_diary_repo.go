package pg

import (
	"context"
	"encoding/json"
	"time"

	"github.com/bnursik/business_surgery_backend/internal/domain/tracking"
	"github.com/bnursik/business_surgery_backend/internal/domain/users"
)

// ListUserDiary returns activity_logs with kind IN ('diary','feedback') for the user's diary feed.
func (r *TrackingRepo) ListUserDiary(ctx context.Context, userID string, limit, offset int) (tracking.ActivityPage, error) {
	userID = stringsTrim(userID)
	if userID == "" {
		return tracking.ActivityPage{}, users.ErrInvalidArgument
	}

	const qCount = `
SELECT COUNT(*)
FROM activity_logs
WHERE user_id = $1::uuid AND kind IN ('diary','feedback');
`
	var total int
	if err := r.db.Pool.QueryRow(ctx, qCount, userID).Scan(&total); err != nil {
		return tracking.ActivityPage{}, err
	}

	const qList = `
SELECT id, kind, payload, created_at
FROM activity_logs
WHERE user_id = $1::uuid AND kind IN ('diary','feedback')
ORDER BY created_at DESC
LIMIT $2 OFFSET $3;
`
	rows, err := r.db.Pool.Query(ctx, qList, userID, limit, offset)
	if err != nil {
		return tracking.ActivityPage{}, err
	}
	defer rows.Close()

	items := make([]tracking.ActivityItem, 0, limit)
	for rows.Next() {
		var (
			id        string
			typ       string
			payloadB  []byte
			createdAt time.Time
		)
		if err := rows.Scan(&id, &typ, &payloadB, &createdAt); err != nil {
			return tracking.ActivityPage{}, err
		}

		var payload any
		if len(payloadB) > 0 {
			_ = json.Unmarshal(payloadB, &payload)
		}

		items = append(items, tracking.ActivityItem{
			ID:        id,
			Type:      typ,
			Payload:   payload,
			CreatedAt: createdAt,
		})
	}
	if err := rows.Err(); err != nil {
		return tracking.ActivityPage{}, err
	}

	return tracking.ActivityPage{
		Total:  total,
		Limit:  limit,
		Offset: offset,
		Items:  items,
	}, nil
}

// CreateDiaryEntry inserts a new diary record into activity_logs (kind='diary').
// Note: ListUserActivity doesn't return message field, so all meaningful content should go into payload.
func (r *TrackingRepo) CreateDiaryEntry(ctx context.Context, userID string, payload any) (tracking.ActivityItem, error) {
	userID = stringsTrim(userID)
	if userID == "" || payload == nil {
		return tracking.ActivityItem{}, users.ErrInvalidArgument
	}

	b, err := json.Marshal(payload)
	if err != nil {
		return tracking.ActivityItem{}, users.ErrInvalidArgument
	}

	now := time.Now().UTC()

	const q = `
INSERT INTO activity_logs (user_id, actor_id, kind, message, payload, created_at)
VALUES ($1::uuid, $1::uuid, 'diary', 'Diary entry', $2::jsonb, $3)
RETURNING id, created_at;
`
	var id string
	var createdAt time.Time
	if err := r.db.Pool.QueryRow(ctx, q, userID, b, now).Scan(&id, &createdAt); err != nil {
		return tracking.ActivityItem{}, err
	}

	return tracking.ActivityItem{
		ID:        id,
		Type:      "diary",
		Payload:   payload,
		CreatedAt: createdAt,
	}, nil
}
