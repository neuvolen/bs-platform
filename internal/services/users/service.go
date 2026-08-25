package usersvc

import (
	"context"
	"strings"

	"github.com/bnursik/business_surgery_backend/internal/domain/users"
)

type service struct {
	repo users.UserRepository
}

func New(repo users.UserRepository) users.UsersService {
	return &service{repo: repo}
}

func (s *service) GetMe(ctx context.Context, userID string) (users.User, error) {
	if userID == "" {
		return users.User{}, users.ErrUnauthorized
	}
	return s.repo.GetByID(ctx, userID)
}

func (s *service) UpdateMe(ctx context.Context, userID, name, surname string) (users.User, error) {
	if userID == "" {
		return users.User{}, users.ErrUnauthorized
	}

	name = strings.TrimSpace(name)
	surname = strings.TrimSpace(surname)

	if name == "" || surname == "" {
		return users.User{}, users.ErrInvalidArgument
	}

	return s.repo.UpdateProfile(ctx, userID, name, surname)
}
