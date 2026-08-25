CREATE EXTENSION IF NOT EXISTS pgcrypto;

-- 1) roles
CREATE TABLE IF NOT EXISTS roles (
    id         UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    code       TEXT NOT NULL UNIQUE, -- 'participant','moderator','admin'
    name       TEXT NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- 2) permissions
CREATE TABLE IF NOT EXISTS permissions (
    id          UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    code        TEXT NOT NULL UNIQUE, -- 'organs:manage', etc
    description TEXT NOT NULL DEFAULT ''
);

-- 3) role_permissions
CREATE TABLE IF NOT EXISTS role_permissions (
    role_id       UUID NOT NULL REFERENCES roles(id) ON DELETE CASCADE,
    permission_id UUID NOT NULL REFERENCES permissions(id) ON DELETE CASCADE,
    PRIMARY KEY (role_id, permission_id)
);

-- 4) user_roles
CREATE TABLE IF NOT EXISTS user_roles (
    user_id UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    role_id UUID NOT NULL REFERENCES roles(id) ON DELETE CASCADE,
    PRIMARY KEY (user_id, role_id)
);

CREATE INDEX IF NOT EXISTS user_roles_user_id_idx ON user_roles(user_id);
CREATE INDEX IF NOT EXISTS user_roles_role_id_idx ON user_roles(role_id);

-- --- SEED roles
INSERT INTO roles (code, name)
VALUES
  ('participant', 'Participant'),
  ('moderator',   'Moderator'),
  ('admin',       'Administrator')
ON CONFLICT (code) DO NOTHING;

-- --- SEED permissions
INSERT INTO permissions (code, description)
VALUES
  ('catalog:read',  'Read public catalog (organs, diseases, details)'),
  ('profile:read',  'Read own profile (/me)'),

  ('organs:manage',   'Create/Update/Delete organs'),
  ('diseases:manage', 'Create/Update/Delete diseases'),
  ('plans:manage',    'Create/Update treatment plans'),
  ('steps:manage',    'Create/Update/Delete treatment steps'),

  ('users:read',   'Read users'),
  ('users:manage', 'Manage users'),
  ('rbac:manage',  'Manage roles/permissions and assignments'),
  ('categories:manage', 'Create/Update/Delete categories')
ON CONFLICT (code) DO NOTHING;

-- --- Bind role -> permissions

-- participant
INSERT INTO role_permissions (role_id, permission_id)
SELECT r.id, p.id
FROM roles r
JOIN permissions p ON p.code IN ('catalog:read', 'profile:read')
WHERE r.code = 'participant'
ON CONFLICT DO NOTHING;

-- moderator
INSERT INTO role_permissions (role_id, permission_id)
SELECT r.id, p.id
FROM roles r
JOIN permissions p ON p.code IN (
  'catalog:read', 'profile:read',
  'organs:manage', 'diseases:manage', 'plans:manage', 'steps:manage',
  'users:read', 'users:manage', 'rbac:manage', 'categories:manage'
)
WHERE r.code = 'moderator'
ON CONFLICT DO NOTHING;

-- admin
INSERT INTO role_permissions (role_id, permission_id)
SELECT r.id, p.id
FROM roles r
JOIN permissions p ON p.code IN (
  'catalog:read', 'profile:read',
  'organs:manage', 'diseases:manage', 'plans:manage', 'steps:manage',
  'users:read', 'users:manage', 'rbac:manage', 'categories:manage'
)
WHERE r.code = 'admin'
ON CONFLICT DO NOTHING;

INSERT INTO user_roles (user_id, role_id)
SELECT u.id, r.id
FROM users u
JOIN roles r ON r.code = u.role
ON CONFLICT DO NOTHING;

INSERT INTO user_roles (user_id, role_id)
SELECT u.id, r.id
FROM users u
JOIN roles r ON r.code = 'participant'
LEFT JOIN user_roles ur ON ur.user_id = u.id
WHERE ur.user_id IS NULL
ON CONFLICT DO NOTHING;
