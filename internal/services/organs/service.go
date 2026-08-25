package organsvc

import (
	"context"
	"strings"

	"github.com/bnursik/business_surgery_backend/internal/domain/organs"
	"github.com/bnursik/business_surgery_backend/internal/domain/users"
)

type service struct {
	repo organs.Repository
}

func New(repo organs.Repository) organs.Service {
	return &service{repo: repo}
}

func (s *service) List(ctx context.Context) ([]organs.Organ, error) {
	return s.repo.List(ctx)
}

func (s *service) Get(ctx context.Context, id string) (organs.Organ, error) {
	id = strings.TrimSpace(id)
	if id == "" {
		return organs.Organ{}, users.ErrInvalidArgument
	}
	return s.repo.GetByID(ctx, id)
}

func (s *service) Create(ctx context.Context, o organs.Organ) (organs.Organ, error) {
	o.Slug = strings.TrimSpace(strings.ToLower(o.Slug))
	o.Title = strings.TrimSpace(o.Title)
	o.Description = strings.TrimSpace(o.Description)

	if o.Slug == "" || o.Title == "" {
		return organs.Organ{}, users.ErrInvalidArgument
	}
	return s.repo.Create(ctx, o)
}

func (s *service) Update(ctx context.Context, id string, o organs.Organ) (organs.Organ, error) {
	id = strings.TrimSpace(id)
	o.Slug = strings.TrimSpace(strings.ToLower(o.Slug))
	o.Title = strings.TrimSpace(o.Title)
	o.Description = strings.TrimSpace(o.Description)

	if id == "" || o.Slug == "" || o.Title == "" {
		return organs.Organ{}, users.ErrInvalidArgument
	}
	return s.repo.Update(ctx, id, o)
}

func (s *service) Delete(ctx context.Context, id string) error {
	id = strings.TrimSpace(id)
	if id == "" {
		return users.ErrInvalidArgument
	}
	return s.repo.Delete(ctx, id)
}
