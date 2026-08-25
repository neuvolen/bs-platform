package dashboardusers

import (
	"context"
	"strings"

	"github.com/bnursik/business_surgery_backend/internal/domain/dashboardusers"
	"github.com/bnursik/business_surgery_backend/internal/domain/users"
)

type service struct {
	repo dashboardusers.Repository
}

func New(repo dashboardusers.Repository) dashboardusers.Service {
	return &service{repo: repo}
}

func (s *service) Get(ctx context.Context, id string) (dashboardusers.Participant, error) {
	id = strings.TrimSpace(id)
	if id == "" {
		return dashboardusers.Participant{}, users.ErrInvalidArgument
	}
	return s.repo.GetByID(ctx, id)
}

func (s *service) UpdateParticipant(ctx context.Context, id string, in dashboardusers.UpdateParticipantInput) (dashboardusers.Participant, error) {
	id = strings.TrimSpace(id)
	in.Name = strings.TrimSpace(in.Name)
	in.Surname = strings.TrimSpace(in.Surname)
	if id == "" || in.Name == "" || in.Surname == "" {
		return dashboardusers.Participant{}, users.ErrInvalidArgument
	}
	if len(in.Name) > 64 || len(in.Surname) > 64 {
		return dashboardusers.Participant{}, users.ErrInvalidArgument
	}
	return s.repo.UpdateParticipant(ctx, id, in)
}

func (s *service) DeleteParticipant(ctx context.Context, id string) error {
	id = strings.TrimSpace(id)
	if id == "" {
		return users.ErrInvalidArgument
	}
	return s.repo.DeleteParticipant(ctx, id)
}
