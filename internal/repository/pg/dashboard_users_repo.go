package pg

import (
	"context"
	"errors"
	"time"

	"github.com/bnursik/business_surgery_backend/internal/domain/dashboardusers"
	"github.com/bnursik/business_surgery_backend/internal/domain/users"
	"github.com/jackc/pgx/v5"
)

type DashboardUsersRepo struct {
	db *DB
}

func NewDashboardUsersRepo(db *DB) *DashboardUsersRepo {
	return &DashboardUsersRepo{db: db}
}

func (r *DashboardUsersRepo) GetByID(ctx context.Context, id string) (dashboardusers.Participant, error) {
	const q = `
SELECT id, email, name, surname, role, created_at, updated_at
FROM users
WHERE id = $1::uuid;
`
	var p dashboardusers.Participant
	var createdAt, updatedAt time.Time
	if err := r.db.Pool.QueryRow(ctx, q, id).Scan(
		&p.ID, &p.Email, &p.Name, &p.Surname, &p.Role, &createdAt, &updatedAt,
	); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return dashboardusers.Participant{}, users.ErrNotFound
		}
		return dashboardusers.Participant{}, err
	}

	p.CreatedAt = createdAt
	p.UpdatedAt = updatedAt
	return p, nil
}

func (r *DashboardUsersRepo) UpdateParticipant(ctx context.Context, id string, in dashboardusers.UpdateParticipantInput) (dashboardusers.Participant, error) {
	// Only participants can be edited via dashboard
	const q = `
UPDATE users
SET name = $2,
    surname = $3,
    updated_at = now()
WHERE id = $1::uuid AND role = 'participant'
RETURNING id, email, name, surname, role, created_at, updated_at;
`
	var p dashboardusers.Participant
	var createdAt, updatedAt time.Time
	if err := r.db.Pool.QueryRow(ctx, q, id, in.Name, in.Surname).Scan(
		&p.ID, &p.Email, &p.Name, &p.Surname, &p.Role, &createdAt, &updatedAt,
	); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return dashboardusers.Participant{}, users.ErrNotFound
		}
		return dashboardusers.Participant{}, err
	}
	p.CreatedAt = createdAt
	p.UpdatedAt = updatedAt
	return p, nil
}

func (r *DashboardUsersRepo) DeleteParticipant(ctx context.Context, id string) error {
	// Only participants can be deleted via dashboard
	const q = `
DELETE FROM users
WHERE id = $1::uuid AND role = 'participant';
`
	ct, err := r.db.Pool.Exec(ctx, q, id)
	if err != nil {
		return err
	}
	if ct.RowsAffected() == 0 {
		return users.ErrNotFound
	}
	return nil
}
