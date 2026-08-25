package pg

import (
	"context"
	"errors"
	"strings"

	"github.com/bnursik/business_surgery_backend/internal/domain/diseasecategories"
	"github.com/bnursik/business_surgery_backend/internal/domain/users"
	"github.com/jackc/pgerrcode"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

type DiseaseCategoriesRepo struct {
	db *DB
}

func NewDiseaseCategoriesRepo(db *DB) *DiseaseCategoriesRepo {
	return &DiseaseCategoriesRepo{db: db}
}

func (r *DiseaseCategoriesRepo) List(ctx context.Context) ([]diseasecategories.Category, error) {
	const q = `
SELECT id, code, title, created_at, updated_at
FROM disease_categories
ORDER BY title;
`
	rows, err := r.db.Pool.Query(ctx, q)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := make([]diseasecategories.Category, 0, 32)
	for rows.Next() {
		var c diseasecategories.Category
		if err := rows.Scan(&c.ID, &c.Code, &c.Title, &c.CreatedAt, &c.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

func (r *DiseaseCategoriesRepo) GetByID(ctx context.Context, id string) (diseasecategories.Category, error) {
	id = strings.TrimSpace(id)
	if id == "" {
		return diseasecategories.Category{}, users.ErrInvalidArgument
	}

	const q = `
SELECT id, code, title, created_at, updated_at
FROM disease_categories
WHERE id = $1::uuid;
`
	var c diseasecategories.Category
	if err := r.db.Pool.QueryRow(ctx, q, id).Scan(&c.ID, &c.Code, &c.Title, &c.CreatedAt, &c.UpdatedAt); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return diseasecategories.Category{}, users.ErrNotFound
		}
		return diseasecategories.Category{}, err
	}
	return c, nil
}

func (r *DiseaseCategoriesRepo) Create(ctx context.Context, c diseasecategories.Category) (diseasecategories.Category, error) {
	c.Code = strings.TrimSpace(c.Code)
	c.Title = strings.TrimSpace(c.Title)

	if c.Code == "" || c.Title == "" {
		return diseasecategories.Category{}, users.ErrInvalidArgument
	}

	const q = `
INSERT INTO disease_categories (code, title)
VALUES ($1, $2)
RETURNING id, code, title, created_at, updated_at;
`

	var out diseasecategories.Category
	err := r.db.Pool.QueryRow(ctx, q, c.Code, c.Title).Scan(
		&out.ID,
		&out.Code,
		&out.Title,
		&out.CreatedAt,
		&out.UpdatedAt,
	)
	if err != nil {
		var pgerr *pgconn.PgError
		if errors.As(err, &pgerr) {
			if pgerr.Code == pgerrcode.UniqueViolation {
				return diseasecategories.Category{}, users.ErrConflict
			}
		}
		return diseasecategories.Category{}, err
	}
	return out, nil
}

func (r *DiseaseCategoriesRepo) Update(ctx context.Context, id string, c diseasecategories.Category) (diseasecategories.Category, error) {
	id = strings.TrimSpace(id)
	c.Code = strings.TrimSpace(c.Code)
	c.Title = strings.TrimSpace(c.Title)

	if id == "" || c.Code == "" || c.Title == "" {
		return diseasecategories.Category{}, users.ErrInvalidArgument
	}

	const q = `
UPDATE disease_categories
SET code = $2,
    title = $3,
    updated_at = now()
WHERE id = $1::uuid
RETURNING id, code, title, created_at, updated_at;
`

	var out diseasecategories.Category
	err := r.db.Pool.QueryRow(ctx, q, id, c.Code, c.Title).Scan(
		&out.ID,
		&out.Code,
		&out.Title,
		&out.CreatedAt,
		&out.UpdatedAt,
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return diseasecategories.Category{}, users.ErrNotFound
		}
		var pgerr *pgconn.PgError
		if errors.As(err, &pgerr) {
			if pgerr.Code == pgerrcode.UniqueViolation {
				return diseasecategories.Category{}, users.ErrConflict
			}
		}
		return diseasecategories.Category{}, err
	}
	return out, nil
}

func (r *DiseaseCategoriesRepo) Delete(ctx context.Context, id string) error {
	id = strings.TrimSpace(id)
	if id == "" {
		return users.ErrInvalidArgument
	}

	const q = `DELETE FROM disease_categories WHERE id = $1::uuid;`
	ct, err := r.db.Pool.Exec(ctx, q, id)
	if err != nil {
		var pgerr *pgconn.PgError
		if errors.As(err, &pgerr) && pgerr.Code == pgerrcode.ForeignKeyViolation {
			return users.ErrConflict
		}
		return err
	}

	if ct.RowsAffected() == 0 {
		return users.ErrNotFound
	}
	return nil
}
