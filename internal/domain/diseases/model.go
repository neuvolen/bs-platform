package diseases

import (
	"time"

	"github.com/google/uuid"
)

type CategoryBrief struct {
	ID    uuid.UUID
	Code  string
	Title string
}

type Disease struct {
	ID          uuid.UUID
	OrganID     uuid.UUID
	CategoryID  uuid.UUID
	Category    CategoryBrief 
	Title       string
	Description string
	CreatedAt   time.Time
	UpdatedAt   time.Time
}
