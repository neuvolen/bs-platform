package pg

import (
	"context"
	"encoding/json"
	"time"

	"github.com/bnursik/business_surgery_backend/internal/domain/tracking"
	"github.com/bnursik/business_surgery_backend/internal/domain/users"
)

// CreateFeedbackEntry inserts a feedback record into activity_logs (kind='feedback').
// Note: ListUserActivity doesn't return message field, so all meaningful content should go into payload.
func (r *TrackingRepo) CreateFeedbackEntry(ctx context.Context, userID, actorID string, payload any) (tracking.ActivityItem, error) {
	userID = stringsTrim(userID)
	actorID = stringsTrim(actorID)

	if userID == "" || actorID == "" || payload == nil {
		return tracking.ActivityItem{}, users.ErrInvalidArgument
	}

	b, err := json.Marshal(payload)
	if err != nil {
		return tracking.ActivityItem{}, users.ErrInvalidArgument
	}

	now := time.Now().UTC()

	const q = `
INSERT INTO activity_logs (user_id, actor_id, kind, message, payload, created_at)
VALUES ($1::uuid, $2::uuid, 'feedback', 'Feedback', $3::jsonb, $4)
RETURNING id, created_at;
`
	var id string
	var createdAt time.Time
	if err := r.db.Pool.QueryRow(ctx, q, userID, actorID, b, now).Scan(&id, &createdAt); err != nil {
		return tracking.ActivityItem{}, err
	}

	return tracking.ActivityItem{
		ID:        id,
		Type:      "feedback",
		Payload:   payload,
		CreatedAt: createdAt,
	}, nil
}
