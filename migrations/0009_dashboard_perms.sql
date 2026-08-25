-- новые permissions для модераторской панели
INSERT INTO permissions (code, description)
VALUES
  ('dashboard:read',        'Read moderator dashboard stats and lists'),
  ('participants:read',     'Read participants profile, diseases, progress'),
  ('assignments:manage',    'Assign/close diseases for participants'),
  ('feedback:create',       'Leave feedback to participants'),
  ('participants:manage', 'Manage participants (edit profile, delete)')
ON CONFLICT (code) DO NOTHING;

-- moderator: добавить нужные права
INSERT INTO role_permissions (role_id, permission_id)
SELECT r.id, p.id
FROM roles r
JOIN permissions p ON p.code IN (
  'dashboard:read',
  'participants:read',
  'assignments:manage',
  'feedback:create',
  'participants:manage'
)
WHERE r.code = 'moderator'
ON CONFLICT DO NOTHING;

-- admin тоже должен иметь эти права
INSERT INTO role_permissions (role_id, permission_id)
SELECT r.id, p.id
FROM roles r
JOIN permissions p ON p.code IN (
  'dashboard:read',
  'participants:read',
  'assignments:manage',
  'feedback:create',
  'participants:manage'
)
WHERE r.code = 'admin'
ON CONFLICT DO NOTHING;
