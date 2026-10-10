package pg

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"sync"
	"time"

	"github.com/bnursik/business_surgery_backend/internal/datadir"
)

// R83e: a kept file never changes (platform_files: insert or delete only),
// so a big one is read from Postgres once and then served from the volume
// (/data/bs-files). Before, every play of a funnel video (5–25 MB, the player
// asks by ranges) and every recording pass pulled the whole file through the
// database's public proxy, billed as egress. Only on the persistent volume:
// without it (tests, local runs) everything is read from Postgres as before.

const (
	fileCacheMin = 256 << 10 // smaller files: one row is cheap
	fileCacheCap = 400 << 20 // the folder's cap (the volume is 5 GB, bs-video takes up to 2.5 GB)
)

var fileCacheIDRe = regexp.MustCompile(`^[A-Za-z0-9_.-]{1,120}$`)

type fileCache struct {
	mu  sync.Mutex
	dir string
}

var (
	fcOnce sync.Once
	fcDir  *fileCache
)

func filesCache() *fileCache {
	fcOnce.Do(func() {
		if datadir.Root() == "" {
			return
		}
		d := datadir.Path("bs-files")
		if os.MkdirAll(d, 0o755) == nil && datadir.Writable(d) {
			fcDir = &fileCache{dir: d}
		}
	})
	return fcDir
}

func (c *fileCache) path(id string) string { return filepath.Join(c.dir, id) }

// read: the kept copy when its size is the file's.
func (c *fileCache) read(id string, size int64) []byte {
	if c == nil || !fileCacheIDRe.MatchString(id) {
		return nil
	}
	p := c.path(id)
	st, err := os.Stat(p)
	if err != nil || st.Size() != size {
		return nil
	}
	b, err := os.ReadFile(p)
	if err != nil || int64(len(b)) != size {
		return nil
	}
	now := time.Now()
	_ = os.Chtimes(p, now, now) // recently used stays longest
	return b
}

func (c *fileCache) write(id string, data []byte) {
	if c == nil || !fileCacheIDRe.MatchString(id) || len(data) < fileCacheMin || len(data) > fileCacheCap/4 {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	tmp := c.path(id) + ".part"
	if os.WriteFile(tmp, data, 0o644) != nil || os.Rename(tmp, c.path(id)) != nil {
		os.Remove(tmp)
		return
	}
	c.trim()
}

func (c *fileCache) remove(id string) {
	if c == nil || !fileCacheIDRe.MatchString(id) {
		return
	}
	os.Remove(c.path(id))
}

// trim drops the least recently used files over the cap.
func (c *fileCache) trim() {
	ents, err := os.ReadDir(c.dir)
	if err != nil {
		return
	}
	type f struct {
		name string
		size int64
		at   time.Time
	}
	var list []f
	var total int64
	for _, e := range ents {
		if info, err := e.Info(); err == nil && info.Mode().IsRegular() {
			list = append(list, f{e.Name(), info.Size(), info.ModTime()})
			total += info.Size()
		}
	}
	if total <= fileCacheCap {
		return
	}
	sort.Slice(list, func(i, j int) bool { return list[i].at.Before(list[j].at) })
	for _, x := range list {
		if total <= fileCacheCap {
			break
		}
		if os.Remove(filepath.Join(c.dir, x.name)) == nil {
			total -= x.size
		}
	}
}
