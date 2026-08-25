package pg

import (
	"context"
	"strings"

	"github.com/bnursik/business_surgery_backend/internal/domain/users"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

type RBACRepo struct {
	db *DB
}

type pgxTx interface {
	Exec(ctx context.Context, sql string, arguments ...any) (pgconn.CommandTag, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

func NewRBACRepo(db *DB) *RBACRepo {
	return &RBACRepo{db: db}
}

func (r *RBACRepo) AssignRoleByCode(ctx context.Context, userID, roleCode string) error {
	userID = strings.TrimSpace(userID)
	roleCode = strings.TrimSpace(strings.ToLower(roleCode))
	if userID == "" || roleCode == "" {
		return users.ErrInvalidArgument
	}

	tx, err := r.db.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	const insertQ = `
INSERT INTO user_roles (user_id, role_id)
SELECT $1::uuid, roles.id
FROM roles
WHERE roles.code = $2
ON CONFLICT DO NOTHING;
`
	ct, err := tx.Exec(ctx, insertQ, userID, roleCode)
	if err != nil {
		return err
	}
	// если roleCode не существует в roles, SELECT вернёт 0 строк -> вставки не будет
	if ct.RowsAffected() == 0 {
		const existsQ = `
SELECT 1
FROM user_roles ur
JOIN roles r ON r.id = ur.role_id
WHERE ur.user_id = $1::uuid AND r.code = $2
LIMIT 1;
`
		var one int
		row := tx.QueryRow(ctx, existsQ, userID, roleCode)
		if scanErr := row.Scan(&one); scanErr == nil {
			// роль уже была назначена
			return users.ErrAlreadyExists
		}
		return users.ErrNotFound
	}

	if err := r.syncUserPrimaryRole(ctx, tx, userID); err != nil {
		return err
	}

	if err := tx.Commit(ctx); err != nil {
		return err
	}
	return nil
}

func (r *RBACRepo) GetUserPermissions(ctx context.Context, userID string) ([]string, error) {
	userID = strings.TrimSpace(userID)
	if userID == "" {
		return nil, users.ErrInvalidArgument
	}

	const q = `
SELECT DISTINCT p.code
FROM user_roles ur
JOIN role_permissions rp ON rp.role_id = ur.role_id
JOIN permissions p ON p.id = rp.permission_id
WHERE ur.user_id = $1::uuid
ORDER BY p.code;
`
	rows, err := r.db.Pool.Query(ctx, q, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := make([]string, 0, 16)
	for rows.Next() {
		var code string
		if err := rows.Scan(&code); err != nil {
			return nil, err
		}
		if code != "" {
			out = append(out, code)
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	return out, nil
}

func (r *RBACRepo) GetUserRoles(ctx context.Context, userID string) ([]string, error) {
	userID = strings.TrimSpace(userID)
	if userID == "" {
		return nil, users.ErrInvalidArgument
	}

	const q = `
SELECT r.code
FROM user_roles ur
JOIN roles r ON r.id = ur.role_id
WHERE ur.user_id = $1::uuid
ORDER BY r.code;
`
	rows, err := r.db.Pool.Query(ctx, q, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := make([]string, 0, 4)
	for rows.Next() {
		var code string
		if err := rows.Scan(&code); err != nil {
			return nil, err
		}
		if code != "" {
			out = append(out, code)
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	if len(out) == 0 {
		return nil, users.ErrNotFound
	}
	return out, nil
}

func (r *RBACRepo) RemoveRoleByCode(ctx context.Context, userID, roleCode string) error {
	userID = strings.TrimSpace(userID)
	roleCode = strings.TrimSpace(strings.ToLower(roleCode))
	if userID == "" || roleCode == "" {
		return users.ErrInvalidArgument
	}

	tx, err := r.db.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	const delQ = `
DELETE FROM user_roles ur
USING roles rl
WHERE ur.role_id = rl.id
  AND ur.user_id = $1::uuid
  AND rl.code = $2;
`
	ct, err := tx.Exec(ctx, delQ, userID, roleCode)
	if err != nil {
		return err
	}
	if ct.RowsAffected() == 0 {
		return users.ErrNotFound
	}

	// ВАЖНО: убедимся, что у юзера осталась хотя бы одна роль.
	const countQ = `SELECT COUNT(*) FROM user_roles WHERE user_id = $1::uuid;`
	var cnt int
	if err := tx.QueryRow(ctx, countQ, userID).Scan(&cnt); err != nil {
		return err
	}
	if cnt == 0 {
		return users.ErrInvalidArgument
	}

	// sync users.role
	if err := r.syncUserPrimaryRole(ctx, tx, userID); err != nil {
		return err
	}

	if err := tx.Commit(ctx); err != nil {
		return err
	}
	return nil
}

func (r *RBACRepo) syncUserPrimaryRole(ctx context.Context, tx pgxTx, userID string) error {
	const q = `
UPDATE users u
SET role = (
	CASE
		WHEN EXISTS (
			SELECT 1
			FROM user_roles ur
			JOIN roles r ON r.id = ur.role_id
			WHERE ur.user_id = $1::uuid AND r.code = 'admin'
		) THEN 'admin'
		WHEN EXISTS (
			SELECT 1
			FROM user_roles ur
			JOIN roles r ON r.id = ur.role_id
			WHERE ur.user_id = $1::uuid AND r.code = 'moderator'
		) THEN 'moderator'
		ELSE 'participant'
	END
),
updated_at = now()
WHERE u.id = $1::uuid;
`
	ct, err := tx.Exec(ctx, q, userID)
	if err != nil {
		return err
	}
	if ct.RowsAffected() == 0 {
		return users.ErrNotFound
	}
	return nil
}
