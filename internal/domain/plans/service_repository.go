package plans

import "context"

type Repository interface {
	UpsertForDisease(ctx context.Context, diseaseID string, p Plan) (Plan, error)

	AddStep(ctx context.Context, planID string, s Step) (Step, error)
	UpdateStep(ctx context.Context, stepID string, s Step) (Step, error)
	DeleteStep(ctx context.Context, stepID string) error

	GetPlanWithStepsByDiseaseID(ctx context.Context, diseaseID string) (Plan, []Step, error)

	GetPlanByID(ctx context.Context, planID string) (Plan, error)
	ListStepsByPlanID(ctx context.Context, planID string) ([]Step, error)
}

type Service interface {
	UpsertForDisease(ctx context.Context, diseaseID string, p Plan) (Plan, error)

	AddStep(ctx context.Context, planID string, st Step) (Step, error)
	UpdateStep(ctx context.Context, stepID string, st Step) (Step, error)
	DeleteStep(ctx context.Context, stepID string) error

	GetPlanWithStepsByDiseaseID(ctx context.Context, diseaseID string) (Plan, []Step, error)

	GetPlanByID(ctx context.Context, planID string) (Plan, error)
	ListStepsByPlanID(ctx context.Context, planID string) ([]Step, error)
}
