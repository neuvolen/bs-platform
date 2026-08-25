package pg

import (
	"context"
	"errors"
	"strings"

	"github.com/bnursik/business_surgery_backend/internal/domain/plans"
	"github.com/bnursik/business_surgery_backend/internal/domain/users"
	"github.com/jackc/pgerrcode"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

type PlansRepo struct {
	db *DB
}

func NewPlansRepo(db *DB) *PlansRepo {
	return &PlansRepo{db: db}
}

// UpsertForDisease создаёт план для disease, либо обновляет если уже есть.
// В таблице treatment_plans disease_id UNIQUE, поэтому ON CONFLICT работает идеально.
func (r *PlansRepo) UpsertForDisease(ctx context.Context, diseaseID string, p plans.Plan) (plans.Plan, error) {
	diseaseID = strings.TrimSpace(diseaseID)
	p.Title = strings.TrimSpace(p.Title)

	if diseaseID == "" || p.Title == "" {
		return plans.Plan{}, users.ErrInvalidArgument
	}

	const q = `
INSERT INTO treatment_plans (disease_id, title, description)
VALUES ($1, $2, $3)
ON CONFLICT (disease_id)
DO UPDATE SET title=EXCLUDED.title, description=EXCLUDED.description, updated_at=now()
RETURNING id, disease_id, title, description, created_at, updated_at;
`
	var out plans.Plan
	err := r.db.Pool.QueryRow(ctx, q, diseaseID, p.Title, p.Description).
		Scan(&out.ID, &out.DiseaseID, &out.Title, &out.Description, &out.CreatedAt, &out.UpdatedAt)
	if err != nil {
		var pgerr *pgconn.PgError
		if errors.As(err, &pgerr) && pgerr.Code == pgerrcode.ForeignKeyViolation {
			return plans.Plan{}, users.ErrInvalidArgument
		}
		return plans.Plan{}, err
	}
	return out, nil
}

func (r *PlansRepo) AddStep(ctx context.Context, planID string, s plans.Step) (plans.Step, error) {
	planID = strings.TrimSpace(planID)
	s.Title = strings.TrimSpace(s.Title)

	if planID == "" || s.OrderNo <= 0 || s.Title == "" {
		return plans.Step{}, users.ErrInvalidArgument
	}

	const q = `
INSERT INTO treatment_steps (plan_id, order_no, title, description)
VALUES ($1, $2, $3, $4)
RETURNING id, plan_id, order_no, title, description, created_at, updated_at;
`
	var out plans.Step
	err := r.db.Pool.QueryRow(ctx, q, planID, s.OrderNo, s.Title, s.Description).
		Scan(&out.ID, &out.PlanID, &out.OrderNo, &out.Title, &out.Description, &out.CreatedAt, &out.UpdatedAt)
	if err != nil {
		var pgerr *pgconn.PgError
		if errors.As(err, &pgerr) {
			switch pgerr.Code {
			case pgerrcode.UniqueViolation:
				// (plan_id, order_no) UNIQUE => попытка добавить шаг с тем же order
				return plans.Step{}, users.ErrAlreadyExists
			case pgerrcode.ForeignKeyViolation:
				return plans.Step{}, users.ErrInvalidArgument
			}
		}
		return plans.Step{}, err
	}
	return out, nil
}

func (r *PlansRepo) UpdateStep(ctx context.Context, stepID string, s plans.Step) (plans.Step, error) {
	stepID = strings.TrimSpace(stepID)
	s.Title = strings.TrimSpace(s.Title)

	if stepID == "" || s.OrderNo <= 0 || s.Title == "" {
		return plans.Step{}, users.ErrInvalidArgument
	}

	const q = `
UPDATE treatment_steps
SET order_no=$2, title=$3, description=$4, updated_at=now()
WHERE id=$1
RETURNING id, plan_id, order_no, title, description, created_at, updated_at;
`
	var out plans.Step
	err := r.db.Pool.QueryRow(ctx, q, stepID, s.OrderNo, s.Title, s.Description).
		Scan(&out.ID, &out.PlanID, &out.OrderNo, &out.Title, &out.Description, &out.CreatedAt, &out.UpdatedAt)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return plans.Step{}, users.ErrNotFound
		}
		var pgerr *pgconn.PgError
		if errors.As(err, &pgerr) && pgerr.Code == pgerrcode.UniqueViolation {
			return plans.Step{}, users.ErrAlreadyExists
		}
		return plans.Step{}, err
	}
	return out, nil
}

func (r *PlansRepo) DeleteStep(ctx context.Context, stepID string) error {
	stepID = strings.TrimSpace(stepID)
	if stepID == "" {
		return users.ErrInvalidArgument
	}

	const q = `DELETE FROM treatment_steps WHERE id=$1;`
	ct, err := r.db.Pool.Exec(ctx, q, stepID)
	if err != nil {
		return err
	}
	if ct.RowsAffected() == 0 {
		return users.ErrNotFound
	}
	return nil
}

func (r *PlansRepo) GetPlanWithStepsByDiseaseID(ctx context.Context, diseaseID string) (plans.Plan, []plans.Step, error) {
	diseaseID = strings.TrimSpace(diseaseID)
	if diseaseID == "" {
		return plans.Plan{}, nil, users.ErrInvalidArgument
	}

	const qPlan = `
SELECT id, disease_id, title, description, created_at, updated_at
FROM treatment_plans
WHERE disease_id=$1;
`
	var p plans.Plan
	err := r.db.Pool.QueryRow(ctx, qPlan, diseaseID).Scan(
		&p.ID, &p.DiseaseID, &p.Title, &p.Description, &p.CreatedAt, &p.UpdatedAt,
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return plans.Plan{}, nil, users.ErrNotFound
		}
		return plans.Plan{}, nil, err
	}

	const qSteps = `
SELECT id, plan_id, order_no, title, description, created_at, updated_at
FROM treatment_steps
WHERE plan_id=$1
ORDER BY order_no ASC;
`
	rows, err := r.db.Pool.Query(ctx, qSteps, p.ID)
	if err != nil {
		return plans.Plan{}, nil, err
	}
	defer rows.Close()

	steps := make([]plans.Step, 0)
	for rows.Next() {
		var s plans.Step
		if err := rows.Scan(&s.ID, &s.PlanID, &s.OrderNo, &s.Title, &s.Description, &s.CreatedAt, &s.UpdatedAt); err != nil {
			return plans.Plan{}, nil, err
		}
		steps = append(steps, s)
	}
	if err := rows.Err(); err != nil {
		return plans.Plan{}, nil, err
	}

	return p, steps, nil
}

func (r *PlansRepo) GetPlanByID(ctx context.Context, planID string) (plans.Plan, error) {
	planID = strings.TrimSpace(planID)
	if planID == "" {
		return plans.Plan{}, users.ErrInvalidArgument
	}

	const q = `
SELECT id, disease_id, title, description, created_at, updated_at
FROM treatment_plans
WHERE id=$1;
`
	var p plans.Plan
	err := r.db.Pool.QueryRow(ctx, q, planID).Scan(
		&p.ID, &p.DiseaseID, &p.Title, &p.Description, &p.CreatedAt, &p.UpdatedAt,
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return plans.Plan{}, users.ErrNotFound
		}
		return plans.Plan{}, err
	}
	return p, nil
}

func (r *PlansRepo) ListStepsByPlanID(ctx context.Context, planID string) ([]plans.Step, error) {
	planID = strings.TrimSpace(planID)
	if planID == "" {
		return nil, users.ErrInvalidArgument
	}

	// Быстрая проверка что план существует (чтобы отличать "нет шагов" от "плана нет")
	if _, err := r.GetPlanByID(ctx, planID); err != nil {
		return nil, err
	}

	const q = `
SELECT id, plan_id, order_no, title, description, created_at, updated_at
FROM treatment_steps
WHERE plan_id=$1
ORDER BY order_no ASC;
`
	rows, err := r.db.Pool.Query(ctx, q, planID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := make([]plans.Step, 0)
	for rows.Next() {
		var s plans.Step
		if err := rows.Scan(&s.ID, &s.PlanID, &s.OrderNo, &s.Title, &s.Description, &s.CreatedAt, &s.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return out, nil
}
