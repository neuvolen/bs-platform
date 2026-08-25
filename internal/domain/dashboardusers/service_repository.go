package dashboardusers

import "context"

type Repository interface {
	GetByID(ctx context.Context, id string) (Participant, error)
	UpdateParticipant(ctx context.Context, id string, in UpdateParticipantInput) (Participant, error)
	DeleteParticipant(ctx context.Context, id string) error
}

type Service interface {
	Get(ctx context.Context, id string) (Participant, error)
	UpdateParticipant(ctx context.Context, id string, in UpdateParticipantInput) (Participant, error)
	DeleteParticipant(ctx context.Context, id string) error
}
