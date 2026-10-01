package http

import (
	"context"
	"os"
	"strings"
	"testing"

	pg "github.com/bnursik/business_surgery_backend/internal/repository/pg"
	"github.com/bnursik/business_surgery_backend/migrations"
)

func TestLoadEmbeddedFiles(t *testing.T) {
	dsn, tok := os.Getenv("BS_TEST_DSN"), os.Getenv("BS_FILES_TOKEN")
	if dsn == "" || tok == "" {
		t.Skip("BS_TEST_DSN / BS_FILES_TOKEN not set")
	}
	ctx := context.Background()
	db, err := pg.NewDB(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	_ = pg.Migrate(ctx, db, migrations.FS)
	_, _ = db.Pool.Exec(ctx, `DELETE FROM platform_docs WHERE key IN ('bs_bookfiles','bs_stickerpack_srv','bs_tools')`)
	repo := pg.NewPlatformRepo(db)
	_, _ = repo.PutDoc(ctx, "club", "bs_tools", 0, `[{"title":"Книга: «Scrum»","isBook":true,"match":"scrum|скрам|сазерленд"}]`, false, "t")
	h := NewPlatformAI(repo, nil)
	h.LoadEmbedded(ctx, tok)
	h.LoadEmbedded(ctx, tok) // second start changes nothing
	b, _ := repo.GetDoc(ctx, "club", "bs_bookfiles")
	s, _ := repo.GetDoc(ctx, "club", "bs_stickerpack_srv")
	tl, _ := repo.GetDoc(ctx, "club", "bs_tools")
	if b == nil || strings.Count(b.Value, `"id"`) != 10 || s == nil || !strings.Contains(s.Value, "facepalm") || !strings.Contains(tl.Value, "Сазерленд - Scrum.pdf") {
		t.Fatalf("books %v | stickers %v | tools %v", b, s, tl)
	}
}
