package diseasecategories

import (
	"time"

	"github.com/google/uuid"
)

type Category struct {
	ID        uuid.UUID
	Code      string
	Title     string
	CreatedAt time.Time
	UpdatedAt time.Time
}
