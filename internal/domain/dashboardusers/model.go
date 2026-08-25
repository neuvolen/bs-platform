package dashboardusers

import "time"

// Participant is a simplified user model for moderator dashboard management.
// It is intentionally separate from users.User to avoid leaking auth fields.
type Participant struct {
	ID        string    `json:"id"`
	Email     string    `json:"email"`
	Name      string    `json:"name"`
	Surname   string    `json:"surname"`
	Role      string    `json:"role"`
	CreatedAt time.Time `json:"createdAt"`
	UpdatedAt time.Time `json:"updatedAt"`
}

type UpdateParticipantInput struct {
	Name    string
	Surname string
}
