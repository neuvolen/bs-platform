package users

import (
	"errors"
	"time"

	"github.com/google/uuid"
)

type Role string

const (
	RoleParticipant Role = "participant"
	RoleModerator   Role = "moderator"
	RoleAdmin       Role = "admin"
)

type User struct {
	ID        uuid.UUID
	Name      string
	Surname   string
	Email     string
	Role      Role
	CreatedAt time.Time
	UpdatedAt time.Time
}

// PasswordHash is a hashed password string
type PasswordHash string

type OAuthAccount struct {
	ID                uuid.UUID
	UserID            string
	Provider          string
	ProviderAccountID string
	Email             string
}

// Domain errors
var (
	ErrNotFound        = errors.New("not found")
	ErrInvalidArgument = errors.New("invalid argument")
	ErrAlreadyExists   = errors.New("already exists")
	ErrUnauthorized    = errors.New("unauthorized")
	ErrForbidden       = errors.New("forbidden")
	ErrConflict        = errors.New("conflict")
)
