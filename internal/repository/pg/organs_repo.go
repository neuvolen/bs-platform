package pg

import (
	"context"
	"errors"
	"strings"

	"github.com/bnursik/business_surgery_backend/internal/domain/organs"
	"github.com/bnursik/business_surgery_backend/internal/domain/users"
	"github.com/jackc/pgerrcode"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

type OrgansRepo struct {
	db *DB
}

func NewOrgansRepo(db *DB) *OrgansRepo {
	return &OrgansRepo{db: db}
}

func (r *OrgansRepo) List(ctx context.Context) ([]organs.Organ, error) {
	const q = `
SELECT id, slug, title, description, created_at, updated_at
FROM organs
ORDER BY title;
`
	rows, err := r.db.Pool.Query(ctx, q)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := make([]organs.Organ, 0)
	for rows.Next() {
		var o organs.Organ
		if err := rows.Scan(&o.ID, &o.Slug, &o.Title, &o.Description, &o.CreatedAt, &o.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, o)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

func (r *OrgansRepo) GetByID(ctx context.Context, id string) (organs.Organ, error) {
	const q = `
SELECT id, slug, title, description, created_at, updated_at
FROM organs
WHERE id = $1;
`
	var o organs.Organ
	err := r.db.Pool.QueryRow(ctx, q, id).Scan(&o.ID, &o.Slug, &o.Title, &o.Description, &o.CreatedAt, &o.UpdatedAt)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return organs.Organ{}, users.ErrNotFound
		}
		return organs.Organ{}, err
	}
	return o, nil
}

func (r *OrgansRepo) Create(ctx context.Context, o organs.Organ) (organs.Organ, error) {
	o.Slug = strings.TrimSpace(strings.ToLower(o.Slug))
	o.Title = strings.TrimSpace(o.Title)

	if o.Slug == "" || o.Title == "" {
		return organs.Organ{}, users.ErrInvalidArgument
	}

	const q = `
INSERT INTO organs (slug, title, description)
VALUES ($1, $2, $3)
RETURNING id, slug, title, description, created_at, updated_at;
`
	var out organs.Organ
	err := r.db.Pool.QueryRow(ctx, q, o.Slug, o.Title, o.Description).
		Scan(&out.ID, &out.Slug, &out.Title, &out.Description, &out.CreatedAt, &out.UpdatedAt)
	if err != nil {
		var pgerr *pgconn.PgError
		if errors.As(err, &pgerr) && pgerr.Code == pgerrcode.UniqueViolation {
			return organs.Organ{}, users.ErrAlreadyExists
		}
		return organs.Organ{}, err
	}
	return out, nil
}

func (r *OrgansRepo) Update(ctx context.Context, id string, o organs.Organ) (organs.Organ, error) {
	o.Slug = strings.TrimSpace(strings.ToLower(o.Slug))
	o.Title = strings.TrimSpace(o.Title)

	if o.Slug == "" || o.Title == "" {
		return organs.Organ{}, users.ErrInvalidArgument
	}

	const q = `
UPDATE organs
SET slug=$2, title=$3, description=$4, updated_at=now()
WHERE id=$1
RETURNING id, slug, title, description, created_at, updated_at;
`
	var out organs.Organ
	err := r.db.Pool.QueryRow(ctx, q, id, o.Slug, o.Title, o.Description).
		Scan(&out.ID, &out.Slug, &out.Title, &out.Description, &out.CreatedAt, &out.UpdatedAt)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return organs.Organ{}, users.ErrNotFound
		}
		var pgerr *pgconn.PgError
		if errors.As(err, &pgerr) && pgerr.Code == pgerrcode.UniqueViolation {
			return organs.Organ{}, users.ErrAlreadyExists
		}
		return organs.Organ{}, err
	}
	return out, nil
}

func (r *OrgansRepo) Delete(ctx context.Context, id string) error {
	const q = `DELETE FROM organs WHERE id=$1;`
	ct, err := r.db.Pool.Exec(ctx, q, id)
	if err != nil {
		return err
	}
	if ct.RowsAffected() == 0 {
		return users.ErrNotFound
	}
	return nil
}
