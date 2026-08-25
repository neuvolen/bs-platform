package pg

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/bnursik/business_surgery_backend/internal/domain/tracking"
	"github.com/bnursik/business_surgery_backend/internal/domain/users"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

func (r *TrackingRepo) AssignDiseaseToUser(ctx context.Context, userID, diseaseID, actorID string) (tracking.AssignDiseaseResult, error) {
	userID = stringsTrim(userID)
	diseaseID = stringsTrim(diseaseID)
	actorID = stringsTrim(actorID)

	if userID == "" || diseaseID == "" {
		return tracking.AssignDiseaseResult{}, users.ErrInvalidArgument
	}

	tx, err := r.db.Pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return tracking.AssignDiseaseResult{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	// 1) verify user exists
	{
		const q = `SELECT 1 FROM users WHERE id = $1::uuid;`
		var one int
		if err := tx.QueryRow(ctx, q, userID).Scan(&one); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return tracking.AssignDiseaseResult{}, users.ErrNotFound
			}
			return tracking.AssignDiseaseResult{}, err
		}
	}

	// 2) verify disease exists + get title for payload
	var diseaseTitle string
	{
		const q = `SELECT title FROM diseases WHERE id = $1::uuid;`
		if err := tx.QueryRow(ctx, q, diseaseID).Scan(&diseaseTitle); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return tracking.AssignDiseaseResult{}, users.ErrNotFound
			}
			return tracking.AssignDiseaseResult{}, err
		}
	}

	now := time.Now().UTC()

	// 3) insert user_diseases (assigned_at exists, started_at doesn't)
	var userDiseaseID string
	{
		const q = `
INSERT INTO user_diseases (user_id, disease_id, assigned_by, status, assigned_at, created_at, updated_at)
VALUES ($1::uuid, $2::uuid, NULLIF($3,'')::uuid, 'active', $4, $4, $4)
RETURNING id;
`
		if err := tx.QueryRow(ctx, q, userID, diseaseID, actorID, now).Scan(&userDiseaseID); err != nil {
			// unique active (user_id, disease_id) where status='active'
			var pgErr *pgconn.PgError
			if errors.As(err, &pgErr) && pgErr.Code == "23505" {
				return tracking.AssignDiseaseResult{}, users.ErrAlreadyExists
			}
			return tracking.AssignDiseaseResult{}, err
		}
	}

	// 4) get latest treatment plan for this disease (assume created_at exists in 0005)
	var planID string
	{
		const q = `
SELECT id
FROM treatment_plans
WHERE disease_id = $1::uuid
ORDER BY created_at DESC
LIMIT 1;
`
		if err := tx.QueryRow(ctx, q, diseaseID).Scan(&planID); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				// контентная ошибка: болезнь есть, плана нет
				return tracking.AssignDiseaseResult{}, users.ErrInvalidArgument
			}
			return tracking.AssignDiseaseResult{}, err
		}
	}

	// 5) create user_steps from treatment_steps
	var totalSteps int
	{
		const q = `
WITH s AS (
  SELECT id
  FROM treatment_steps
  WHERE plan_id = $1::uuid
)
INSERT INTO user_steps (user_disease_id, step_id, state, completed_at, created_at, updated_at)
SELECT $2::uuid, s.id, 'pending', NULL, $3, $3
FROM s
RETURNING 1;
`
		rows, err := tx.Query(ctx, q, planID, userDiseaseID, now)
		if err != nil {
			return tracking.AssignDiseaseResult{}, err
		}
		for rows.Next() {
			totalSteps++
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return tracking.AssignDiseaseResult{}, err
		}
	}

	// 6) activity log: kind must be one of
	{
		payload := map[string]any{
			"userDiseaseId": userDiseaseID,
			"diseaseId":     diseaseID,
			"title":         diseaseTitle,
			"totalSteps":    totalSteps,
		}
		b, _ := json.Marshal(payload)

		const q = `
INSERT INTO activity_logs (user_id, actor_id, kind, message, payload, user_disease_id, created_at)
VALUES ($1::uuid, NULLIF($2,'')::uuid, 'assignment', 'Disease assigned', $3::jsonb, $4::uuid, $5);
`
		if _, err := tx.Exec(ctx, q, userID, actorID, b, userDiseaseID, now); err != nil {
			return tracking.AssignDiseaseResult{}, err
		}
	}

	if err := tx.Commit(ctx); err != nil {
		return tracking.AssignDiseaseResult{}, err
	}

	return tracking.AssignDiseaseResult{
		UserDiseaseID: userDiseaseID,
		TotalSteps:    totalSteps,
	}, nil
}

func stringsTrim(s string) string {
	for len(s) > 0 {
		r := s[0]
		if r == ' ' || r == '\n' || r == '\t' || r == '\r' {
			s = s[1:]
			continue
		}
		break
	}
	for len(s) > 0 {
		r := s[len(s)-1]
		if r == ' ' || r == '\n' || r == '\t' || r == '\r' {
			s = s[:len(s)-1]
			continue
		}
		break
	}
	return s
}
