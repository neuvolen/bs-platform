package pg

import (
	"context"
	"errors"
	"strings"

	"github.com/bnursik/business_surgery_backend/internal/domain/users"
	"github.com/google/uuid"
	"github.com/jackc/pgerrcode"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

type UsersRepo struct {
	db *DB
}

func NewUsersRepo(db *DB) *UsersRepo {
	return &UsersRepo{db: db}
}

func (r *UsersRepo) GetByEmail(ctx context.Context, email string) (users.User, users.PasswordHash, error) {
	const q = `
SELECT id, email, name, surname, role, password_hash, created_at, updated_at
FROM users
WHERE LOWER(email) = LOWER($1);
`
	var u users.User
	var hash string
	err := r.db.Pool.QueryRow(ctx, q, strings.ToLower(email)).
		Scan(&u.ID, &u.Email, &u.Name, &u.Surname, &u.Role, &hash, &u.CreatedAt, &u.UpdatedAt)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return users.User{}, "", users.ErrNotFound
		}
		return users.User{}, "", err
	}
	return u, users.PasswordHash(hash), nil
}

func (r *UsersRepo) GetByID(ctx context.Context, id string) (users.User, error) {
	const q = `
SELECT id, email, name, surname, role, created_at, updated_at
FROM users
WHERE id = $1;
`
	var u users.User
	err := r.db.Pool.QueryRow(ctx, q, id).Scan(
		&u.ID, &u.Email, &u.Name, &u.Surname, &u.Role, &u.CreatedAt, &u.UpdatedAt,
	)
	if err != nil {
		if err == pgx.ErrNoRows {
			return users.User{}, users.ErrNotFound
		}
		return users.User{}, err
	}
	return u, nil
}

func (r *UsersRepo) Create(ctx context.Context, u users.User, hash users.PasswordHash) (users.User, error) {
	const q = `
INSERT INTO users (email, password_hash, name, surname, role)
VALUES (LOWER($1), $2, $3, $4, $5)
RETURNING id, email, name, surname, role, created_at, updated_at;
`
	var out users.User
	err := r.db.Pool.QueryRow(ctx, q,
		u.Email, string(hash), u.Name, u.Surname, string(u.Role),
	).Scan(
		&out.ID,
		&out.Email,
		&out.Name,
		&out.Surname,
		&out.Role,
		&out.CreatedAt,
		&out.UpdatedAt,
	)
	if err != nil {
		var pgerr *pgconn.PgError
		if ok := errors.As(err, &pgerr); ok && pgerr.Code == pgerrcode.UniqueViolation {
			return users.User{}, users.ErrAlreadyExists
		}
		return users.User{}, err
	}
	return out, nil
}

func (r *UsersRepo) FindByProviderID(ctx context.Context, provider, providerID string) (users.OAuthAccount, users.User, error) {
	const q = `
SELECT
    oa.id,
    oa.user_id,
    oa.provider,
    oa.provider_account_id,
    COALESCE(oa.email, ''),
    u.id,
    u.email,
    u.name,
    u.surname,
    u.role,
    u.created_at,
    u.updated_at
FROM oauth_accounts oa
JOIN users u ON u.id = oa.user_id
WHERE oa.provider = $1 AND oa.provider_account_id = $2;
`
	var (
		oauth  users.OAuthAccount
		u      users.User
		userID uuid.UUID
	)

	err := r.db.Pool.QueryRow(ctx, q, strings.TrimSpace(provider), strings.TrimSpace(providerID)).Scan(
		&oauth.ID,
		&userID,
		&oauth.Provider,
		&oauth.ProviderAccountID,
		&oauth.Email,
		&u.ID,
		&u.Email,
		&u.Name,
		&u.Surname,
		&u.Role,
		&u.CreatedAt,
		&u.UpdatedAt,
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return users.OAuthAccount{}, users.User{}, users.ErrNotFound
		}
		return users.OAuthAccount{}, users.User{}, err
	}

	oauth.UserID = userID.String()
	return oauth, u, nil
}

func (r *UsersRepo) CreateOAuthAccount(ctx context.Context, provider, providerID, email string, user users.User) (users.OAuthAccount, users.User, error) {
	if user.ID == uuid.Nil {
		return users.OAuthAccount{}, users.User{}, users.ErrInvalidArgument
	}

	const q = `
WITH inserted AS (
    INSERT INTO oauth_accounts (user_id, provider, provider_account_id, email)
    VALUES ($1, $2, $3, $4)
    RETURNING id, user_id, provider, provider_account_id, COALESCE(email, '') AS email
)
SELECT
    i.id,
    i.user_id,
    i.provider,
    i.provider_account_id,
    i.email,
    u.id,
    u.email,
    u.name,
    u.surname,
    u.role,
    u.created_at,
    u.updated_at
FROM inserted i
JOIN users u ON u.id = i.user_id;
`
	var (
		oauth  users.OAuthAccount
		out    users.User
		userID uuid.UUID
	)

	err := r.db.Pool.QueryRow(ctx, q,
		user.ID,
		strings.TrimSpace(provider),
		strings.TrimSpace(providerID),
		strings.ToLower(strings.TrimSpace(email)),
	).Scan(
		&oauth.ID,
		&userID,
		&oauth.Provider,
		&oauth.ProviderAccountID,
		&oauth.Email,
		&out.ID,
		&out.Email,
		&out.Name,
		&out.Surname,
		&out.Role,
		&out.CreatedAt,
		&out.UpdatedAt,
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return users.OAuthAccount{}, users.User{}, users.ErrNotFound
		}

		var pgerr *pgconn.PgError
		if ok := errors.As(err, &pgerr); ok && pgerr.Code == pgerrcode.UniqueViolation {
			return users.OAuthAccount{}, users.User{}, users.ErrAlreadyExists
		}

		return users.OAuthAccount{}, users.User{}, err
	}

	oauth.UserID = userID.String()
	return oauth, out, nil
}

func (r *UsersRepo) List(ctx context.Context, q string, limit, offset int) ([]users.User, int, error) {
	if limit <= 0 || limit > 200 {
		limit = 20
	}
	if offset < 0 {
		offset = 0
	}

	q = strings.TrimSpace(strings.ToLower(q))

	// count
	const qc = `
SELECT COUNT(*)
FROM users
WHERE ($1 = '' OR LOWER(email) LIKE '%' || $1 || '%'
              OR LOWER(name) LIKE '%' || $1 || '%'
              OR LOWER(surname) LIKE '%' || $1 || '%');
`
	var total int
	if err := r.db.Pool.QueryRow(ctx, qc, q).Scan(&total); err != nil {
		return nil, 0, err
	}

	// list
	const ql = `
SELECT id, email, name, surname, role, created_at, updated_at
FROM users
WHERE ($1 = '' OR LOWER(email) LIKE '%' || $1 || '%'
              OR LOWER(name) LIKE '%' || $1 || '%'
              OR LOWER(surname) LIKE '%' || $1 || '%')
ORDER BY created_at DESC
LIMIT $2 OFFSET $3;
`
	rows, err := r.db.Pool.Query(ctx, ql, q, limit, offset)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()

	out := make([]users.User, 0, limit)
	for rows.Next() {
		var u users.User
		if err := rows.Scan(&u.ID, &u.Email, &u.Name, &u.Surname, &u.Role, &u.CreatedAt, &u.UpdatedAt); err != nil {
			return nil, 0, err
		}
		out = append(out, u)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, err
	}

	return out, total, nil
}

func (r *UsersRepo) UpdateProfile(ctx context.Context, id, name, surname string) (users.User, error) {
	const q = `
UPDATE users
SET name = $2,
    surname = $3,
    updated_at = now()
WHERE id = $1
RETURNING id, email, name, surname, role, created_at, updated_at;
`

	var u users.User
	err := r.db.Pool.QueryRow(ctx, q, id, name, surname).Scan(
		&u.ID,
		&u.Email,
		&u.Name,
		&u.Surname,
		&u.Role,
		&u.CreatedAt,
		&u.UpdatedAt,
	)
	if err != nil {
		if err == pgx.ErrNoRows {
			return users.User{}, users.ErrNotFound
		}
		return users.User{}, err
	}
	return u, nil
}

func (r *UsersRepo) Delete(ctx context.Context, id string) error {
	const q = `
DELETE FROM users
WHERE id = $1;
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
