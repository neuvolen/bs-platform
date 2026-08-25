package quizzes

import "time"

type Quiz struct {
	ID          string
	LessonID    string
	Title       string
	PassScore   int
	MaxAttempts int
	IsPublished bool
	CreatedAt   time.Time
}

type Question struct {
	ID           string
	QuizID       string
	QuestionType string // single | multi
	Prompt       string
	Points       int
	Position     int
	Options      []Option
}

type Option struct {
	ID         string
	QuestionID string
	Text       string
	IsCorrect  bool
	Position   int
}

type Attempt struct {
	ID          string
	QuizID      string
	UserID      string
	AttemptNo   int
	StartedAt   time.Time
	SubmittedAt *time.Time
	Score       int
	Passed      bool
}

type Answer struct {
	QuestionID        string
	SelectedOptionIDs []string
}

type QuizFull struct {
	Quiz      Quiz
	Questions []Question
}
