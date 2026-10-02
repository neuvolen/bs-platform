package http

import (
	"context"
	"os"
	"testing"

	pg "github.com/bnursik/business_surgery_backend/internal/repository/pg"
	"github.com/bnursik/business_surgery_backend/migrations"
)

func TestDocVersionsKeepEveryState(t *testing.T) {
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
	_, _ = db.Pool.Exec(ctx, `DELETE FROM platform_docs WHERE key='bs_kanban_vt'; DELETE FROM platform_doc_versions WHERE key='bs_kanban_vt'`)
	repo := pg.NewPlatformRepo(db)
	v := 0
	for _, val := range []string{`{"cards":[1]}`, `{"cards":[1,2]}`, `{"cards":[]}`} {
		d, err := repo.PutDoc(ctx, "club", "bs_kanban_vt", v, val, false, "tg:1")
		if err != nil {
			t.Fatal(err)
		}
		v = d.Version
	}
	list, err := repo.DocVersions(ctx, "club", "bs_kanban_vt")
	if err != nil || len(list) != 2 || list[0].Version != 2 || list[1].Version != 1 || list[0].UpdatedBy != "tg:1" {
		t.Fatalf("versions: %+v %v", list, err)
	}
	one, err := repo.DocVersion(ctx, "club", "bs_kanban_vt", 2)
	if err != nil || one == nil || one.Value != `{"cards":[1,2]}` {
		t.Fatalf("version 2: %+v %v", one, err)
	}
	if x, _ := repo.DocVersion(ctx, "club", "bs_kanban_vt", 9); x != nil {
		t.Fatal("missing version must be nil")
	}
}
