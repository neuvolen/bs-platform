package tracking

import "context"

type Repository interface {
	GetDashboardStats(ctx context.Context) (DashboardStats, error)
	ListDashboardUsers(ctx context.Context, q string, limit, offset int) (DashboardUsersPage, error)

	GetUserProgress(ctx context.Context, userID string) (UserProgress, error)
	ListUserDiseases(ctx context.Context, userID, status string, limit, offset int) (UserDiseasesPage, error)
	ListUserActivity(ctx context.Context, userID string, limit, offset int) (ActivityPage, error)

	AssignDiseaseToUser(ctx context.Context, userID, diseaseID, actorID string) (AssignDiseaseResult, error)
	ListUserDiseaseSteps(ctx context.Context, userDiseaseID string, limit, offset int) (UserStepsPage, error)
	CompleteUserStep(ctx context.Context, userID, userStepID string) error
	UpdateUserStepState(ctx context.Context, userID, userStepID, state string) error

	ResolveUserDisease(ctx context.Context, userID, userDiseaseID, actorID string) error

	// Diary: kind='diary' entries
	ListUserDiary(ctx context.Context, userID string, limit, offset int) (ActivityPage, error)
	CreateDiaryEntry(ctx context.Context, userID string, payload any) (ActivityItem, error)
	// Feedback: kind='feedback' entries
	CreateFeedbackEntry(ctx context.Context, userID, actorID string, payload any) (ActivityItem, error)
}

type Service interface {
	GetDashboardStats(ctx context.Context) (DashboardStats, error)
	ListDashboardUsers(ctx context.Context, q string, limit, offset int) (DashboardUsersPage, error)

	GetUserProgress(ctx context.Context, userID string) (UserProgress, error)
	ListUserDiseases(ctx context.Context, userID, status string, limit, offset int) (UserDiseasesPage, error)
	ListUserActivity(ctx context.Context, userID string, limit, offset int) (ActivityPage, error)

	AssignDiseaseToUser(ctx context.Context, userID, diseaseID, actorID string) (AssignDiseaseResult, error)
	ListUserDiseaseSteps(ctx context.Context, userDiseaseID string, limit, offset int) (UserStepsPage, error)
	CompleteUserStep(ctx context.Context, userID, userStepID string) error
	UpdateUserStepState(ctx context.Context, userID, userStepID, state string) error
	ResolveUserDisease(ctx context.Context, userID, userDiseaseID, actorID string) error

	// Diary: kind='diary' entries
	ListUserDiary(ctx context.Context, userID string, limit, offset int) (ActivityPage, error)
	CreateDiaryEntry(ctx context.Context, userID string, payload any) (ActivityItem, error)
	// Feedback: kind='feedback' entries
	CreateFeedbackEntry(ctx context.Context, userID, actorID string, payload any) (ActivityItem, error)
}
