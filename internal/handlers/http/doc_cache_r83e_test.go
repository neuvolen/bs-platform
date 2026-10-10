package http

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"

	pg "github.com/bnursik/business_surgery_backend/internal/repository/pg"
	"github.com/bnursik/business_surgery_backend/migrations"
)

// R83e: big sections come from the server's copy while their rev is the same;
// a write anywhere (another instance, raw SQL) is seen on the next read.
func TestDocCacheBigSectionsR83e(t *testing.T) {
	dsn := os.Getenv("BS_TEST_DSN")
	if dsn == "" {
		t.Skip("BS_TEST_DSN not set")
	}
	ctx := context.Background()
	db, err := pg.NewDB(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Pool.Close()
	_ = pg.Migrate(ctx, db, migrations.FS)
	_, _ = db.Pool.Exec(ctx, `DELETE FROM platform_docs WHERE key LIKE 'r83e_%'; DELETE FROM platform_doc_versions WHERE key LIKE 'r83e_%'`)
	repo := pg.NewPlatformRepo(db)
	other := pg.NewPlatformRepo(db) // a second instance (a deploy's overlap)

	big1 := `{"leads":"` + strings.Repeat("a", 100<<10) + `"}`
	big2 := `{"leads":"` + strings.Repeat("b", 100<<10) + `"}`
	d, err := repo.PutDoc(ctx, "club", "r83e_crm", 0, big1, false, "tg:1")
	if err != nil || d.Value != big1 || d.Version != 1 || d.Rev == 0 {
		t.Fatalf("create: %+v %v", d, err)
	}
	if n, b := repo.DocCacheStats(); n < 1 || b < len(big1) {
		t.Fatalf("the written big section is kept: %d %d", n, b)
	}
	g, err := repo.GetDoc(ctx, "club", "r83e_crm")
	if err != nil || g == nil || g.Value != big1 || g.Rev != d.Rev {
		t.Fatalf("read: %v", err)
	}

	// the other instance writes: the first one sees it at once
	d2, err := other.PutDoc(ctx, "club", "r83e_crm", 1, big2, false, "tg:2")
	if err != nil || d2.Version != 2 {
		t.Fatalf("other write: %+v %v", d2, err)
	}
	g, err = repo.GetDoc(ctx, "club", "r83e_crm")
	if err != nil || g.Value != big2 || g.Version != 2 || g.UpdatedBy != "tg:2" {
		t.Fatalf("read after the other's write: %v %d", err, g.Version)
	}

	// raw SQL bumps rev too (PutServerDoc's way)
	if _, err := db.Pool.Exec(ctx, `UPDATE platform_docs SET value = $1, version = version + 1, rev = nextval('platform_rev_seq') WHERE scope='club' AND key='r83e_crm'`, big1); err != nil {
		t.Fatal(err)
	}
	g, _ = repo.GetDoc(ctx, "club", "r83e_crm")
	if g.Value != big1 || g.Version != 3 {
		t.Fatalf("raw update not seen: v%d", g.Version)
	}

	// history keeps the replaced states (copied inside Postgres now)
	list, err := repo.DocVersions(ctx, "club", "r83e_crm")
	if err != nil || len(list) != 1 || list[0].Version != 1 {
		t.Fatalf("history: %+v %v", list, err)
	}
	one, _ := repo.DocVersion(ctx, "club", "r83e_crm", 1)
	if one == nil || one.Value != big1 {
		t.Fatal("history value")
	}

	// a stale base version: conflict with the current copy
	cur, err := repo.PutDoc(ctx, "club", "r83e_crm", 1, big2, false, "tg:1")
	if !errors.Is(err, pg.ErrPlatformConflict) || cur == nil || cur.Value != big1 || cur.Version != 3 {
		t.Fatalf("conflict: %v", err)
	}
	// the same value: no new version
	same, err := repo.PutDoc(ctx, "club", "r83e_crm", 3, big1, false, "tg:1")
	if err != nil || same.Version != 3 || same.Value != big1 {
		t.Fatalf("same value: %+v %v", same, err)
	}

	// small sections are read as before
	s, err := repo.PutDoc(ctx, "club", "r83e_small", 0, `{"a":1}`, false, "tg:1")
	if err != nil || s.Value != `{"a":1}` {
		t.Fatal(err)
	}
	if g, _ := repo.GetDoc(ctx, "club", "r83e_small"); g == nil || g.Value != `{"a":1}` {
		t.Fatal("small read")
	}

	// sync: a big changed section comes whole
	_, docs, _, err := other.Changes(ctx, d.Rev-1, "tg:1")
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, x := range docs {
		if x.Key == "r83e_crm" {
			found = x.Value == big1 && x.Version == 3
		}
	}
	if !found {
		t.Fatal("sync must carry the big section's current value")
	}

	// a deleted section: an empty value, nil after the row goes
	del, err := repo.PutDoc(ctx, "club", "r83e_crm", 3, "", true, "tg:1")
	if err != nil || !del.Deleted {
		t.Fatalf("delete: %v", err)
	}
	if g, _ := other.GetDoc(ctx, "club", "r83e_crm"); g == nil || !g.Deleted || g.Value != "" {
		t.Fatal("deleted read")
	}
	_, _ = db.Pool.Exec(ctx, `DELETE FROM platform_docs WHERE key LIKE 'r83e_%'`)
	if g, _ := repo.GetDoc(ctx, "club", "r83e_crm"); g != nil {
		t.Fatal("gone row must read nil")
	}
	_, _ = db.Pool.Exec(ctx, `DELETE FROM platform_doc_versions WHERE key LIKE 'r83e_%'`)
}
