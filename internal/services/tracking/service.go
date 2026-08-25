package trackingsvc

import (
	"context"
	"strings"

	"github.com/bnursik/business_surgery_backend/internal/domain/tracking"
	"github.com/bnursik/business_surgery_backend/internal/domain/users"
)

type service struct {
	repo tracking.Repository
}

func New(repo tracking.Repository) tracking.Service {
	return &service{repo: repo}
}

func (s *service) GetDashboardStats(ctx context.Context) (tracking.DashboardStats, error) {
	return s.repo.GetDashboardStats(ctx)
}

func (s *service) ListDashboardUsers(ctx context.Context, q string, limit, offset int) (tracking.DashboardUsersPage, error) {
	q = strings.TrimSpace(q)

	if limit <= 0 || limit > 200 {
		limit = 20
	}
	if offset < 0 {
		offset = 0
	}

	return s.repo.ListDashboardUsers(ctx, q, limit, offset)
}

func (s *service) GetUserProgress(ctx context.Context, userID string) (tracking.UserProgress, error) {
	userID = strings.TrimSpace(userID)
	if userID == "" {
		return tracking.UserProgress{}, users.ErrInvalidArgument
	}
	return s.repo.GetUserProgress(ctx, userID)
}

func (s *service) ListUserDiseases(ctx context.Context, userID, status string, limit, offset int) (tracking.UserDiseasesPage, error) {
	userID = strings.TrimSpace(userID)
	status = strings.TrimSpace(strings.ToLower(status))

	if userID == "" {
		return tracking.UserDiseasesPage{}, users.ErrInvalidArgument
	}
	if status == "" {
		status = "active"
	}
	if limit <= 0 || limit > 200 {
		limit = 20
	}
	if offset < 0 {
		offset = 0
	}

	return s.repo.ListUserDiseases(ctx, userID, status, limit, offset)
}

func (s *service) ListUserActivity(ctx context.Context, userID string, limit, offset int) (tracking.ActivityPage, error) {
	userID = strings.TrimSpace(userID)
	if userID == "" {
		return tracking.ActivityPage{}, users.ErrInvalidArgument
	}
	if limit <= 0 || limit > 200 {
		limit = 20
	}
	if offset < 0 {
		offset = 0
	}

	return s.repo.ListUserActivity(ctx, userID, limit, offset)
}

func (s *service) AssignDiseaseToUser(ctx context.Context, userID, diseaseID, actorID string) (tracking.AssignDiseaseResult, error) {
	userID = strings.TrimSpace(userID)
	diseaseID = strings.TrimSpace(diseaseID)
	actorID = strings.TrimSpace(actorID)

	if userID == "" || diseaseID == "" {
		return tracking.AssignDiseaseResult{}, users.ErrInvalidArgument
	}

	return s.repo.AssignDiseaseToUser(ctx, userID, diseaseID, actorID)
}

func (s *service) ListUserDiseaseSteps(ctx context.Context, userDiseaseID string, limit, offset int) (tracking.UserStepsPage, error) {
	userDiseaseID = strings.TrimSpace(userDiseaseID)
	if userDiseaseID == "" {
		return tracking.UserStepsPage{}, users.ErrInvalidArgument
	}
	if limit <= 0 || limit > 200 {
		limit = 20
	}
	if offset < 0 {
		offset = 0
	}
	return s.repo.ListUserDiseaseSteps(ctx, userDiseaseID, limit, offset)
}

func (s *service) CompleteUserStep(ctx context.Context, userID, userStepID string) error {
	userID = strings.TrimSpace(userID)
	userStepID = strings.TrimSpace(userStepID)
	if userID == "" || userStepID == "" {
		return users.ErrInvalidArgument
	}
	return s.repo.CompleteUserStep(ctx, userID, userStepID)
}

func (s *service) UpdateUserStepState(ctx context.Context, userID, userStepID, state string) error {
	userID = strings.TrimSpace(userID)
	userStepID = strings.TrimSpace(userStepID)
	state = strings.TrimSpace(strings.ToLower(state))

	if userID == "" || userStepID == "" || state == "" {
		return users.ErrInvalidArgument
	}
	if state != "pending" && state != "active" {
		// completed is handled by CompleteUserStep (it also writes activity + auto-resolve)
		return users.ErrInvalidArgument
	}

	return s.repo.UpdateUserStepState(ctx, userID, userStepID, state)
}

func (s *service) ResolveUserDisease(ctx context.Context, userID, userDiseaseID, actorID string) error {
	userID = strings.TrimSpace(userID)
	userDiseaseID = strings.TrimSpace(userDiseaseID)
	actorID = strings.TrimSpace(actorID)

	if userID == "" || userDiseaseID == "" {
		return users.ErrInvalidArgument
	}
	return s.repo.ResolveUserDisease(ctx, userID, userDiseaseID, actorID)
}

func (s *service) ListUserDiary(ctx context.Context, userID string, limit, offset int) (tracking.ActivityPage, error) {
	userID = strings.TrimSpace(userID)
	if userID == "" {
		return tracking.ActivityPage{}, users.ErrInvalidArgument
	}
	if limit <= 0 || limit > 200 {
		limit = 20
	}
	if offset < 0 {
		offset = 0
	}
	return s.repo.ListUserDiary(ctx, userID, limit, offset)
}

func (s *service) CreateDiaryEntry(ctx context.Context, userID string, payload any) (tracking.ActivityItem, error) {
	userID = strings.TrimSpace(userID)
	if userID == "" {
		return tracking.ActivityItem{}, users.ErrInvalidArgument
	}
	if payload == nil {
		return tracking.ActivityItem{}, users.ErrInvalidArgument
	}
	return s.repo.CreateDiaryEntry(ctx, userID, payload)
}

func (s *service) CreateFeedbackEntry(ctx context.Context, userID, actorID string, payload any) (tracking.ActivityItem, error) {
	userID = strings.TrimSpace(userID)
	actorID = strings.TrimSpace(actorID)

	if userID == "" || actorID == "" {
		return tracking.ActivityItem{}, users.ErrInvalidArgument
	}
	if payload == nil {
		return tracking.ActivityItem{}, users.ErrInvalidArgument
	}

	return s.repo.CreateFeedbackEntry(ctx, userID, actorID, payload)
}
