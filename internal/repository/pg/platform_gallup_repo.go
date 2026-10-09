package pg

import "context"

// DocsByKey returns every live copy of a section across scopes (club and each
// person's own). R68: the Gallup audit reads every board's profile, including
// the ones residents saved in their own scope.
func (r *PlatformRepo) DocsByKey(ctx context.Context, key string) ([]PlatformDoc, error) {
	rows, err := r.db.Pool.Query(ctx, `
		SELECT scope, key, value, version, rev, deleted, updated_at, updated_by
		FROM platform_docs WHERE key = $1 AND NOT deleted ORDER BY scope`, key)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []PlatformDoc
	for rows.Next() {
		var d PlatformDoc
		if err := rows.Scan(&d.Scope, &d.Key, &d.Value, &d.Version, &d.Rev, &d.Deleted, &d.UpdatedAt, &d.UpdatedBy); err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}
