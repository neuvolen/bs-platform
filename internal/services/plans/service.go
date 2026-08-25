package plansvc

import (
	"context"
	"strings"

	"github.com/bnursik/business_surgery_backend/internal/domain/plans"
	"github.com/bnursik/business_surgery_backend/internal/domain/users"
)

type service struct {
	repo plans.Repository
}

func New(repo plans.Repository) plans.Service {
	return &service{repo: repo}
}

func (s *service) UpsertForDisease(ctx context.Context, diseaseID string, p plans.Plan) (plans.Plan, error) {
	diseaseID = strings.TrimSpace(diseaseID)
	p.Title = strings.TrimSpace(p.Title)
	p.Description = strings.TrimSpace(p.Description)

	if diseaseID == "" || p.Title == "" {
		return plans.Plan{}, users.ErrInvalidArgument
	}

	return s.repo.UpsertForDisease(ctx, diseaseID, p)
}

func (s *service) AddStep(ctx context.Context, planID string, st plans.Step) (plans.Step, error) {
	planID = strings.TrimSpace(planID)
	st.Title = strings.TrimSpace(st.Title)
	st.Description = strings.TrimSpace(st.Description)

	if planID == "" || st.OrderNo <= 0 || st.Title == "" {
		return plans.Step{}, users.ErrInvalidArgument
	}
	return s.repo.AddStep(ctx, planID, st)
}

func (s *service) UpdateStep(ctx context.Context, stepID string, st plans.Step) (plans.Step, error) {
	stepID = strings.TrimSpace(stepID)
	st.Title = strings.TrimSpace(st.Title)
	st.Description = strings.TrimSpace(st.Description)

	if stepID == "" || st.OrderNo <= 0 || st.Title == "" {
		return plans.Step{}, users.ErrInvalidArgument
	}
	return s.repo.UpdateStep(ctx, stepID, st)
}

func (s *service) DeleteStep(ctx context.Context, stepID string) error {
	stepID = strings.TrimSpace(stepID)
	if stepID == "" {
		return users.ErrInvalidArgument
	}
	return s.repo.DeleteStep(ctx, stepID)
}

func (s *service) GetPlanWithStepsByDiseaseID(ctx context.Context, diseaseID string) (plans.Plan, []plans.Step, error) {
	diseaseID = strings.TrimSpace(diseaseID)
	if diseaseID == "" {
		return plans.Plan{}, nil, users.ErrInvalidArgument
	}
	return s.repo.GetPlanWithStepsByDiseaseID(ctx, diseaseID)
}

func (s *service) GetPlanByID(ctx context.Context, planID string) (plans.Plan, error) {
	planID = strings.TrimSpace(planID)
	if planID == "" {
		return plans.Plan{}, users.ErrInvalidArgument
	}
	return s.repo.GetPlanByID(ctx, planID)
}

func (s *service) ListStepsByPlanID(ctx context.Context, planID string) ([]plans.Step, error) {
	planID = strings.TrimSpace(planID)
	if planID == "" {
		return nil, users.ErrInvalidArgument
	}
	return s.repo.ListStepsByPlanID(ctx, planID)
}
