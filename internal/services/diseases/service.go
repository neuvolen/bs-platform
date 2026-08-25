package diseasesvc

import (
	"context"
	"strings"

	"github.com/bnursik/business_surgery_backend/internal/domain/diseases"
	"github.com/bnursik/business_surgery_backend/internal/domain/users"
	"github.com/google/uuid"
)

type service struct {
	repo diseases.Repository
}

func New(repo diseases.Repository) diseases.Service {
	return &service{repo: repo}
}

func (s *service) List(ctx context.Context, organID, categoryID string) ([]diseases.Disease, error) {
	organID = strings.TrimSpace(organID)
	categoryID = strings.TrimSpace(categoryID)
	return s.repo.List(ctx, organID, categoryID)
}

func (s *service) Get(ctx context.Context, id string) (diseases.Disease, error) {
	id = strings.TrimSpace(id)
	if id == "" {
		return diseases.Disease{}, users.ErrInvalidArgument
	}
	return s.repo.GetByID(ctx, id)
}

func (s *service) Create(ctx context.Context, d diseases.Disease) (diseases.Disease, error) {
	d.Title = strings.TrimSpace(d.Title)
	d.Description = strings.TrimSpace(d.Description)

	if d.OrganID == uuid.Nil || d.CategoryID == uuid.Nil || d.Title == "" {
		return diseases.Disease{}, users.ErrInvalidArgument
	}

	return s.repo.Create(ctx, d)
}

func (s *service) Update(ctx context.Context, id string, d diseases.Disease) (diseases.Disease, error) {
	id = strings.TrimSpace(id)
	d.Title = strings.TrimSpace(d.Title)
	d.Description = strings.TrimSpace(d.Description)

	if id == "" || d.OrganID == uuid.Nil || d.CategoryID == uuid.Nil || d.Title == "" {
		return diseases.Disease{}, users.ErrInvalidArgument
	}

	return s.repo.Update(ctx, id, d)
}

func (s *service) Delete(ctx context.Context, id string) error {
	id = strings.TrimSpace(id)
	if id == "" {
		return users.ErrInvalidArgument
	}
	return s.repo.Delete(ctx, id)
}

func (s *service) ListAssignedDiseaseIDs(ctx context.Context, userID string) (map[string]struct{}, error) {
	userID = strings.TrimSpace(userID)
	if userID == "" {
		return nil, users.ErrInvalidArgument
	}

	ids, err := s.repo.ListAssignedDiseaseIDs(ctx, userID)
	if err != nil {
		return nil, err
	}

	set := make(map[string]struct{}, len(ids))
	for _, id := range ids {
		set[id] = struct{}{}
	}
	return set, nil
}
