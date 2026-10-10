package pg

import (
	"context"
	"errors"
	"sync"

	"github.com/jackc/pgx/v5"
)

// R83e: the big sections (bs_crm is ~24 MB of JSON) were read from Postgres
// in full by every loop and every page: ~1 GB an hour through the database's
// public proxy, which Railway bills as egress. The server now keeps a copy of
// each big section and asks Postgres only for its rev (a few bytes): the copy
// is used while the rev is the same. Every write bumps rev
// (platform_rev_seq), so a changed section is always read again, and the
// check runs on every read, so a second instance (a deploy's overlap) or a
// write outside this process is seen at once.

const (
	docCacheMin = 32 << 10  // smaller sections are cheaper to read than to keep
	docCacheMax = 160 << 20 // all kept copies together (bytes of value)
)

type docKey struct{ scope, key string }

type docCache struct {
	mu    sync.Mutex
	m     map[docKey]PlatformDoc
	bytes int
}

func newDocCache() *docCache { return &docCache{m: map[docKey]PlatformDoc{}} }

func (c *docCache) get(scope, key string, rev int64) (PlatformDoc, bool) {
	if c == nil {
		return PlatformDoc{}, false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	d, ok := c.m[docKey{scope, key}]
	if !ok || d.Rev != rev {
		return PlatformDoc{}, false
	}
	return d, true
}

// put keeps a big section (a small one, or a deleted one, drops the copy).
func (c *docCache) put(d PlatformDoc) {
	if c == nil {
		return
	}
	k := docKey{d.Scope, d.Key}
	c.mu.Lock()
	defer c.mu.Unlock()
	if old, ok := c.m[k]; ok {
		if old.Rev > d.Rev {
			return // a newer copy is kept already
		}
		c.bytes -= len(old.Value)
		delete(c.m, k)
	}
	if d.Deleted || len(d.Value) < docCacheMin || len(d.Value) > docCacheMax {
		return
	}
	if c.bytes+len(d.Value) > docCacheMax {
		c.m, c.bytes = map[docKey]PlatformDoc{}, 0 // rare: start over rather than track ages
	}
	c.m[k] = d
	c.bytes += len(d.Value)
}

func (c *docCache) drop(scope, key string) {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if old, ok := c.m[docKey{scope, key}]; ok {
		c.bytes -= len(old.Value)
		delete(c.m, docKey{scope, key})
	}
}

// DocCacheStats: kept sections and their bytes (the /status line, tests).
func (r *PlatformRepo) DocCacheStats() (n, bytes int) {
	if r.docs == nil {
		return 0, 0
	}
	r.docs.mu.Lock()
	defer r.docs.mu.Unlock()
	return len(r.docs.m), r.docs.bytes
}

// cachedDoc returns the section at rev from the copy, or reads it (one row)
// and keeps it.
func (r *PlatformRepo) cachedDoc(ctx context.Context, scope, key string, rev int64) (*PlatformDoc, error) {
	if d, ok := r.docs.get(scope, key, rev); ok {
		return &d, nil
	}
	var d PlatformDoc
	err := r.db.Pool.QueryRow(ctx, `
		SELECT scope, key, value, version, rev, deleted, updated_at, updated_by
		FROM platform_docs WHERE scope = $1 AND key = $2`, scope, key).
		Scan(&d.Scope, &d.Key, &d.Value, &d.Version, &d.Rev, &d.Deleted, &d.UpdatedAt, &d.UpdatedBy)
	if errors.Is(err, pgx.ErrNoRows) {
		r.docs.drop(scope, key)
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	r.docs.put(d)
	return &d, nil
}
