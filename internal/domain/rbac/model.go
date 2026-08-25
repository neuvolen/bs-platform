package rbac

import "github.com/google/uuid"

type Role struct {
	ID   uuid.UUID
	Code string
	Name string
}

type Permission struct {
	ID          uuid.UUID
	Code        string
	Description string
}
