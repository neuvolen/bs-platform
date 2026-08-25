package rbacsvc

import (
	"context"
	"strings"

	"github.com/bnursik/business_surgery_backend/internal/domain/rbac"
	"github.com/bnursik/business_surgery_backend/internal/domain/users"
)

type service struct {
	repo rbac.Repository
}

func New(repo rbac.Repository) rbac.Service {
	return &service{repo: repo}
}

func (s *service) AssignDefaultRole(ctx context.Context, userID string) error {
	return s.AssignRole(ctx, userID, string(users.RoleParticipant))
}

func (s *service) AssignRole(ctx context.Context, userID, roleCode string) error {
	userID = strings.TrimSpace(userID)
	roleCode = strings.TrimSpace(strings.ToLower(roleCode))
	if userID == "" || roleCode == "" {
		return users.ErrInvalidArgument
	}
	return s.repo.AssignRoleByCode(ctx, userID, roleCode)
}

func (s *service) RemoveRole(ctx context.Context, userID, roleCode string) error {
	userID = strings.TrimSpace(userID)
	roleCode = strings.TrimSpace(strings.ToLower(roleCode))
	if userID == "" || roleCode == "" {
		return users.ErrInvalidArgument
	}
	return s.repo.RemoveRoleByCode(ctx, userID, roleCode)
}

func (s *service) GetRoles(ctx context.Context, userID string) ([]string, error) {
	userID = strings.TrimSpace(userID)
	if userID == "" {
		return nil, users.ErrInvalidArgument
	}
	return s.repo.GetUserRoles(ctx, userID)
}

func (s *service) GetPermissions(ctx context.Context, userID string) ([]string, error) {
	userID = strings.TrimSpace(userID)
	if userID == "" {
		return nil, users.ErrInvalidArgument
	}
	return s.repo.GetUserPermissions(ctx, userID)
}
