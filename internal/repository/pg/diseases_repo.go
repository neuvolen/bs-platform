package pg

import (
	"context"
	"errors"
	"strings"

	"github.com/bnursik/business_surgery_backend/internal/domain/diseases"
	"github.com/bnursik/business_surgery_backend/internal/domain/users"
	"github.com/jackc/pgerrcode"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

type DiseasesRepo struct {
	db *DB
}

func NewDiseasesRepo(db *DB) *DiseasesRepo {
	return &DiseasesRepo{db: db}
}

func (r *DiseasesRepo) List(ctx context.Context, organID, categoryID string) ([]diseases.Disease, error) {
	organID = strings.TrimSpace(organID)
	categoryID = strings.TrimSpace(categoryID)

	const q = `
SELECT
  d.id,
  d.organ_id,
  d.category_id,
  c.id,
  c.code,
  c.title,
  d.title,
  d.description,
  d.created_at,
  d.updated_at
FROM diseases d
JOIN disease_categories c ON c.id = d.category_id
WHERE ($1 = '' OR d.organ_id::text = $1)
  AND ($2 = '' OR d.category_id::text = $2)
ORDER BY d.created_at DESC;
`

	rows, err := r.db.Pool.Query(ctx, q, organID, categoryID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := make([]diseases.Disease, 0, 32)
	for rows.Next() {
		var d diseases.Disease
		if err := rows.Scan(
			&d.ID,
			&d.OrganID,
			&d.CategoryID,
			&d.Category.ID,
			&d.Category.Code,
			&d.Category.Title,
			&d.Title,
			&d.Description,
			&d.CreatedAt,
			&d.UpdatedAt,
		); err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

func (r *DiseasesRepo) GetByID(ctx context.Context, id string) (diseases.Disease, error) {
	id = strings.TrimSpace(id)
	if id == "" {
		return diseases.Disease{}, users.ErrInvalidArgument
	}

	const q = `
SELECT
  d.id,
  d.organ_id,
  d.category_id,
  c.id,
  c.code,
  c.title,
  d.title,
  d.description,
  d.created_at,
  d.updated_at
FROM diseases d
JOIN disease_categories c ON c.id = d.category_id
WHERE d.id = $1::uuid;
`
	var d diseases.Disease
	err := r.db.Pool.QueryRow(ctx, q, id).Scan(
		&d.ID,
		&d.OrganID,
		&d.CategoryID,
		&d.Category.ID,
		&d.Category.Code,
		&d.Category.Title,
		&d.Title,
		&d.Description,
		&d.CreatedAt,
		&d.UpdatedAt,
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return diseases.Disease{}, users.ErrNotFound
		}
		return diseases.Disease{}, err
	}
	return d, nil
}

func (r *DiseasesRepo) Create(ctx context.Context, d diseases.Disease) (diseases.Disease, error) {
	d.Title = strings.TrimSpace(d.Title)
	d.Description = strings.TrimSpace(d.Description)

	if d.OrganID.String() == "" || d.CategoryID.String() == "" || d.Title == "" {
		return diseases.Disease{}, users.ErrInvalidArgument
	}

	const q = `
WITH ins AS (
  INSERT INTO diseases (organ_id, category_id, title, description)
  VALUES ($1::uuid, $2::uuid, $3, $4)
  RETURNING id, organ_id, category_id, title, description, created_at, updated_at
)
SELECT
  ins.id,
  ins.organ_id,
  ins.category_id,
  c.id,
  c.code,
  c.title,
  ins.title,
  ins.description,
  ins.created_at,
  ins.updated_at
FROM ins
JOIN disease_categories c ON c.id = ins.category_id;
`
	var out diseases.Disease
	err := r.db.Pool.QueryRow(ctx, q, d.OrganID, d.CategoryID, d.Title, d.Description).Scan(
		&out.ID,
		&out.OrganID,
		&out.CategoryID,
		&out.Category.ID,
		&out.Category.Code,
		&out.Category.Title,
		&out.Title,
		&out.Description,
		&out.CreatedAt,
		&out.UpdatedAt,
	)
	if err != nil {
		var pgerr *pgconn.PgError
		if errors.As(err, &pgerr) && pgerr.Code == pgerrcode.ForeignKeyViolation {
			return diseases.Disease{}, users.ErrInvalidArgument
		}
		return diseases.Disease{}, err
	}
	return out, nil
}

func (r *DiseasesRepo) Update(ctx context.Context, id string, d diseases.Disease) (diseases.Disease, error) {
	id = strings.TrimSpace(id)
	d.Title = strings.TrimSpace(d.Title)
	d.Description = strings.TrimSpace(d.Description)

	if id == "" || d.OrganID.String() == "" || d.CategoryID.String() == "" || d.Title == "" {
		return diseases.Disease{}, users.ErrInvalidArgument
	}

	const q = `
WITH upd AS (
  UPDATE diseases
  SET organ_id = $2::uuid,
      category_id = $3::uuid,
      title = $4,
      description = $5,
      updated_at = now()
  WHERE id = $1::uuid
  RETURNING id, organ_id, category_id, title, description, created_at, updated_at
)
SELECT
  upd.id,
  upd.organ_id,
  upd.category_id,
  c.id,
  c.code,
  c.title,
  upd.title,
  upd.description,
  upd.created_at,
  upd.updated_at
FROM upd
JOIN disease_categories c ON c.id = upd.category_id;
`
	var out diseases.Disease
	err := r.db.Pool.QueryRow(ctx, q, id, d.OrganID, d.CategoryID, d.Title, d.Description).Scan(
		&out.ID,
		&out.OrganID,
		&out.CategoryID,
		&out.Category.ID,
		&out.Category.Code,
		&out.Category.Title,
		&out.Title,
		&out.Description,
		&out.CreatedAt,
		&out.UpdatedAt,
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return diseases.Disease{}, users.ErrNotFound
		}
		var pgerr *pgconn.PgError
		if errors.As(err, &pgerr) && pgerr.Code == pgerrcode.ForeignKeyViolation {
			return diseases.Disease{}, users.ErrInvalidArgument
		}
		return diseases.Disease{}, err
	}
	return out, nil
}

func (r *DiseasesRepo) Delete(ctx context.Context, id string) error {
	id = strings.TrimSpace(id)
	if id == "" {
		return users.ErrInvalidArgument
	}

	const q = `DELETE FROM diseases WHERE id=$1::uuid;`
	ct, err := r.db.Pool.Exec(ctx, q, id)
	if err != nil {
		return err
	}
	if ct.RowsAffected() == 0 {
		return users.ErrNotFound
	}
	return nil
}

func (r *DiseasesRepo) ListAssignedDiseaseIDs(ctx context.Context, userID string) ([]string, error) {
	userID = strings.TrimSpace(userID)
	if userID == "" {
		return nil, users.ErrInvalidArgument
	}

	const q = `
SELECT ud.disease_id::text
FROM user_diseases ud
WHERE ud.user_id = $1::uuid
  AND ud.status = 'active';
`
	rows, err := r.db.Pool.Query(ctx, q, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := make([]string, 0, 16)
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return out, nil
}
