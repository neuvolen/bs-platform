package organs

import (
	"time"

	"github.com/google/uuid"
)

type Organ struct {
	ID          uuid.UUID
	Slug        string
	Title       string
	Description string
	CreatedAt   time.Time
	UpdatedAt   time.Time
}
