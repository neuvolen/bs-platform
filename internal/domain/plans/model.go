package plans

import (
	"time"

	"github.com/google/uuid"
)

type Plan struct {
	ID          uuid.UUID
	DiseaseID   uuid.UUID
	Title       string
	Description string
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

type Step struct {
	ID          uuid.UUID
	PlanID      uuid.UUID
	OrderNo     int
	Title       string
	Description string
	CreatedAt   time.Time
	UpdatedAt   time.Time
}
