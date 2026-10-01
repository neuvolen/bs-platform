package http

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/bnursik/business_surgery_backend/internal/ai"
	pg "github.com/bnursik/business_surgery_backend/internal/repository/pg"
	"github.com/bnursik/business_surgery_backend/migrations"
)

func TestThreadsAutopost(t *testing.T) {
	dsn := os.Getenv("BS_TEST_DSN")
	if dsn == "" {
		t.Skip("BS_TEST_DSN not set")
	}
	ctx := context.Background()
	db, err := pg.NewDB(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	_ = pg.Migrate(ctx, db, migrations.FS)
	_, _ = db.Pool.Exec(ctx, `DELETE FROM platform_docs WHERE key IN ('bs_useful','bs_threads_status','threads')`)
	var posted []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/refresh_access_token":
			_, _ = w.Write([]byte(`{"access_token":"tok2"}`))
		case r.URL.Path == "/v1.0/me":
			if r.URL.Query().Get("access_token") != "tok2" {
				w.WriteHeader(400)
				_, _ = w.Write([]byte(`{"error":{"message":"bad token"}}`))
				return
			}
			_, _ = w.Write([]byte(`{"id":"42","username":"bsurgery"}`))
		case strings.HasSuffix(r.URL.Path, "/threads"):
			posted = append(posted, r.URL.Query().Get("text"))
			_, _ = w.Write([]byte(`{"id":"c1"}`))
		case strings.HasSuffix(r.URL.Path, "/threads_publish"):
			_, _ = w.Write([]byte(`{"id":"p1"}`))
		}
	}))
	defer srv.Close()
	t.Setenv("THREADS_API_BASE", srv.URL)
	t.Setenv("THREADS_TOKEN", "tok1")
	repo := pg.NewPlatformRepo(db)
	long := strings.Repeat("Длинный текст про деньги. ", 40)
	_, _ = repo.PutDoc(ctx, "club", "bs_useful", 0, `{"items":[{"t":"Платёжный календарь","txt":"`+long+`","done":true},{"t":"Второй","txt":"Коротко"}]}`, false, "t")
	h := NewPlatformAI(repo, &ai.Client{HTTP: srv.Client()})
	title, err := h.PublishNextThreads(ctx)
	if err != nil || title != "Платёжный календарь" || len(posted) != 1 || len([]rune(posted[0])) > 500 || !strings.HasPrefix(posted[0], "Платёжный календарь\n\n") {
		t.Fatalf("first: %q %v %q", title, err, posted)
	}
	if title, _ = h.PublishNextThreads(ctx); title != "Второй" {
		t.Fatalf("second: %q", title)
	}
	if title, _ = h.PublishNextThreads(ctx); title != "" {
		t.Fatalf("queue must be empty: %q", title)
	}
	d, _ := repo.GetDoc(ctx, "club", "bs_useful")
	st, _ := repo.GetDoc(ctx, "club", "bs_threads_status")
	if strings.Count(d.Value, `"threadsId":"p1"`) != 2 || !strings.Contains(st.Value, `"username":"bsurgery"`) {
		t.Fatalf("doc %s status %s", d.Value, st.Value)
	}
}
