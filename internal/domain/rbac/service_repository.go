package rbac

import "context"

type Repository interface {
	// Assign role by code
	AssignRoleByCode(ctx context.Context, userID, roleCode string) error
	RemoveRoleByCode(ctx context.Context, userID, roleCode string) error

	// Read roles/permissions for user
	GetUserRoles(ctx context.Context, userID string) ([]string, error)
	GetUserPermissions(ctx context.Context, userID string) ([]string, error)
}

type Service interface {
	AssignDefaultRole(ctx context.Context, userID string) error
	AssignRole(ctx context.Context, userID, roleCode string) error
	RemoveRole(ctx context.Context, userID, roleCode string) error

	GetRoles(ctx context.Context, userID string) ([]string, error)
	GetPermissions(ctx context.Context, userID string) ([]string, error)
}
