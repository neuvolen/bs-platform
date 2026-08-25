package pg

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/bnursik/business_surgery_backend/internal/domain/tracking"
	"github.com/bnursik/business_surgery_backend/internal/domain/users"
	"github.com/jackc/pgx/v5"
)

func (r *TrackingRepo) ListUserDiseaseSteps(ctx context.Context, userDiseaseID string, limit, offset int) (tracking.UserStepsPage, error) {
	userDiseaseID = stringsTrim(userDiseaseID)
	if userDiseaseID == "" {
		return tracking.UserStepsPage{}, users.ErrInvalidArgument
	}

	if limit <= 0 || limit > 200 {
		limit = 50
	}
	if offset < 0 {
		offset = 0
	}

	const qCount = `
SELECT COUNT(*)
FROM user_steps
WHERE user_disease_id = $1::uuid;
`
	var total int
	if err := r.db.Pool.QueryRow(ctx, qCount, userDiseaseID).Scan(&total); err != nil {
		return tracking.UserStepsPage{}, err
	}

	const qList = `
SELECT
  us.id,
  us.user_disease_id,
  us.step_id,
  COALESCE(ts.title, '') AS title,
  COALESCE(ts.description, '') AS description,
  us.state,
  us.completed_at,
  us.created_at,
  us.updated_at
FROM user_steps us
LEFT JOIN treatment_steps ts ON ts.id = us.step_id
WHERE us.user_disease_id = $1::uuid
ORDER BY us.created_at ASC
LIMIT $2 OFFSET $3;
`
	rows, err := r.db.Pool.Query(ctx, qList, userDiseaseID, limit, offset)
	if err != nil {
		return tracking.UserStepsPage{}, err
	}
	defer rows.Close()

	items := make([]tracking.UserStepItem, 0, limit)
	for rows.Next() {
		var it tracking.UserStepItem
		if err := rows.Scan(
			&it.ID,
			&it.UserDiseaseID,
			&it.StepID,
			&it.StepTitle,
			&it.StepDiscription,
			&it.State,
			&it.CompletedAt,
			&it.CreatedAt,
			&it.UpdatedAt,
		); err != nil {
			return tracking.UserStepsPage{}, err
		}
		items = append(items, it)
	}
	if err := rows.Err(); err != nil {
		return tracking.UserStepsPage{}, err
	}

	return tracking.UserStepsPage{
		Total:  total,
		Limit:  limit,
		Offset: offset,
		Items:  items,
	}, nil
}

func (r *TrackingRepo) CompleteUserStep(ctx context.Context, userID, userStepID string) error {
	userID = stringsTrim(userID)
	userStepID = stringsTrim(userStepID)

	if userID == "" || userStepID == "" {
		return users.ErrInvalidArgument
	}

	tx, err := r.db.Pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	// 1) verify step belongs to user, get ids for activity
	var userDiseaseID string
	var stepID string
	{
		const q = `
SELECT us.user_disease_id, us.step_id
FROM user_steps us
JOIN user_diseases ud ON ud.id = us.user_disease_id
WHERE us.id = $1::uuid AND ud.user_id = $2::uuid;
`
		if err := tx.QueryRow(ctx, q, userStepID, userID).Scan(&userDiseaseID, &stepID); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return users.ErrNotFound
			}
			return err
		}
	}

	now := time.Now().UTC()

	// 2) mark completed
	// IMPORTANT: user_steps has CHECK requiring completed_at when state='completed'
	{
		const q = `
UPDATE user_steps
SET state = 'completed',
    completed_at = $2,
    updated_at = $2
WHERE id = $1::uuid
  AND state <> 'completed';
`
		if _, err := tx.Exec(ctx, q, userStepID, now); err != nil {
			return err
		}
	}

	// 3) activity: step_completed
	{
		payload := map[string]any{
			"userStepId":    userStepID,
			"userDiseaseId": userDiseaseID,
			"stepId":        stepID,
		}
		b, _ := json.Marshal(payload)

		const q = `
INSERT INTO activity_logs (user_id, actor_id, kind, message, payload, user_disease_id, step_id, created_at)
VALUES ($1::uuid, $1::uuid, 'step_completed', 'Step completed', $2::jsonb, $3::uuid, $4::uuid, $5);
`
		if _, err := tx.Exec(ctx, q, userID, b, userDiseaseID, stepID, now); err != nil {
			return err
		}
	}

	// 4) auto-resolve if all completed
	var total, completed int
	{
		const q = `
SELECT
  COUNT(*) AS total,
  COUNT(*) FILTER (WHERE state = 'completed') AS completed
FROM user_steps
WHERE user_disease_id = $1::uuid;
`
		if err := tx.QueryRow(ctx, q, userDiseaseID).Scan(&total, &completed); err != nil {
			return err
		}
	}

	if total > 0 && total == completed {
		// update disease to resolved
		{
			const q = `
UPDATE user_diseases
SET status = 'resolved',
    resolved_at = $2,
    updated_at = $2
WHERE id = $1::uuid AND user_id = $3::uuid AND status = 'active';
`
			if _, err := tx.Exec(ctx, q, userDiseaseID, now, userID); err != nil {
				return err
			}
		}

		// activity: status_change
		{
			payload := map[string]any{
				"userDiseaseId": userDiseaseID,
				"to":            "resolved",
				"reason":        "all_steps_completed",
			}
			b, _ := json.Marshal(payload)

			const q = `
INSERT INTO activity_logs (user_id, actor_id, kind, message, payload, user_disease_id, created_at)
VALUES ($1::uuid, $1::uuid, 'status_change', 'Disease resolved', $2::jsonb, $3::uuid, $4);
`
			if _, err := tx.Exec(ctx, q, userID, b, userDiseaseID, now); err != nil {
				return err
			}
		}
	}

	return tx.Commit(ctx)
}

// UpdateUserStepState updates state for a user's step.
// Allowed transitions in service: pending <-> active. Completion is handled by CompleteUserStep.
func (r *TrackingRepo) UpdateUserStepState(ctx context.Context, userID, userStepID, state string) error {
	userID = stringsTrim(userID)
	userStepID = stringsTrim(userStepID)
	state = stringsTrim(state)
	if userID == "" || userStepID == "" || state == "" {
		return users.ErrInvalidArgument
	}

	now := time.Now().UTC()

	// Ensure the step belongs to the user.
	// Also don't allow changing already completed steps back.
	const q = `
UPDATE user_steps us
SET state = $3,
    completed_at = NULL,
    updated_at = $4
FROM user_diseases ud
WHERE us.id = $1::uuid
  AND us.user_disease_id = ud.id
  AND ud.user_id = $2::uuid
  AND us.state <> 'completed';
`
	ct, err := r.db.Pool.Exec(ctx, q, userStepID, userID, state, now)
	if err != nil {
		return err
	}
	if ct.RowsAffected() == 0 {
		return users.ErrNotFound
	}
	return nil
}

func (r *TrackingRepo) ResolveUserDisease(ctx context.Context, userID, userDiseaseID, actorID string) error {
	userID = stringsTrim(userID)
	userDiseaseID = stringsTrim(userDiseaseID)
	actorID = stringsTrim(actorID)

	if userID == "" || userDiseaseID == "" {
		return users.ErrInvalidArgument
	}

	tx, err := r.db.Pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	now := time.Now().UTC()

	// update disease
	ct, err := tx.Exec(ctx, `
UPDATE user_diseases
SET status = 'resolved',
    resolved_at = $3,
    updated_at = $3
WHERE id = $1::uuid AND user_id = $2::uuid AND status <> 'resolved';
`, userDiseaseID, userID, now)
	if err != nil {
		return err
	}
	if ct.RowsAffected() == 0 {
		return users.ErrNotFound
	}

	// activity
	payload := map[string]any{
		"userDiseaseId": userDiseaseID,
		"to":            "resolved",
		"reason":        "manual",
	}
	b, _ := json.Marshal(payload)

	if _, err := tx.Exec(ctx, `
INSERT INTO activity_logs (user_id, actor_id, kind, message, payload, user_disease_id, created_at)
VALUES ($1::uuid, NULLIF($2,'')::uuid, 'status_change', 'Disease resolved (manual)', $3::jsonb, $4::uuid, $5);
`, userID, actorID, b, userDiseaseID, now); err != nil {
		return err
	}

	return tx.Commit(ctx)
}
