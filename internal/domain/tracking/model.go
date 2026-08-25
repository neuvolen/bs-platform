package tracking

import "time"

type DashboardStats struct {
	TotalParticipants  int
	AvgProgressPercent int
	ActiveProblems     int
}

type DashboardUserItem struct {
	ID                 string
	Email              string
	Name               string
	Surname            string
	LastActivityAt     *time.Time
	ActiveDiseases     int
	CompletedSteps     int
	TotalSteps         int
	OverallProgressPct int
}

type DashboardUsersPage struct {
	Total  int
	Limit  int
	Offset int
	Items  []DashboardUserItem
}

type UserProgress struct {
	UserID             string
	ActiveDiseases     int
	CompletedSteps     int
	TotalSteps         int
	OverallProgressPct int
	LastActivityAt     *time.Time
}

type UserDiseaseItem struct {
	UserDiseaseID   string
	DiseaseID       string
	DiseaseName     string
	OrganName       string
	CategoryName    string
	Status          string
	StartedAt       time.Time
	UpdatedAt       time.Time
	CompletedSteps  int
	TotalSteps      int
	ProgressPercent int
}

type UserDiseasesPage struct {
	Total  int
	Limit  int
	Offset int
	Items  []UserDiseaseItem
}

type ActivityItem struct {
	ID        string
	Type      string
	Payload   any
	CreatedAt time.Time
}

type ActivityPage struct {
	Total  int
	Limit  int
	Offset int
	Items  []ActivityItem
}

type AssignDiseaseResult struct {
	UserDiseaseID string
	TotalSteps    int
}

type UserStepItem struct {
	ID              string
	UserDiseaseID   string
	StepID          string
	StepTitle       string
	StepDiscription string
	State           string
	CompletedAt     *time.Time
	CreatedAt       time.Time
	UpdatedAt       time.Time
}

type UserStepsPage struct {
	Total  int
	Limit  int
	Offset int
	Items  []UserStepItem
}
