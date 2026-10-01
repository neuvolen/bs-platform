package http

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/bnursik/business_surgery_backend/internal/ai"
	"github.com/bnursik/business_surgery_backend/internal/content"
	pg "github.com/bnursik/business_surgery_backend/internal/repository/pg"
	"github.com/bnursik/business_surgery_backend/migrations"
	"github.com/gin-gonic/gin"
)

func TestMarketingAnalysis(t *testing.T) {
	var m map[string]any
	if err := json.Unmarshal(content.Marketing, &m); err != nil || m["ladder"] == nil || m["competitors"] == nil {
		t.Fatalf("shipped marketing.json: %v", err)
	}
	if strings.Contains(string(content.Marketing), "—") {
		t.Fatal("em dash in marketing.json")
	}
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
	_, _ = db.Pool.Exec(ctx, `DELETE FROM platform_docs WHERE key='bs_mkt_analysis'`)
	gm := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var b bytes.Buffer
		_, _ = b.ReadFrom(r.Body)
		if !strings.Contains(b.String(), "google_search") || !strings.Contains(b.String(), "BizPride") {
			w.WriteHeader(400)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"candidates": []any{map[string]any{"content": map[string]any{"parts": []any{map[string]any{"text": "```json\n{\"competitors\":[{\"name\":\"Новый клуб\",\"type\":\"прямой\",\"url\":\"https://x.kz\"}]}\n```"}}}}}})
	}))
	defer gm.Close()
	repo := pg.NewPlatformRepo(db)
	h := NewPlatformAI(repo, &ai.Client{Gemini: "k", GeminiModel: "m", GeminiBase: gm.URL, HTTP: gm.Client()})
	h.SeedMarketing(ctx)
	h.SeedMarketing(ctx)
	d, _ := repo.GetDoc(ctx, "club", "bs_mkt_analysis")
	if d == nil || d.Version != 1 {
		t.Fatalf("seed: %+v", d)
	}
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(func(c *gin.Context) { c.Set("role", "admin") })
	r.POST("/ai/marketing", h.Marketing)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest("POST", "/ai/marketing", strings.NewReader(`{"section":"competitors"}`)))
	if !strings.Contains(w.Body.String(), "Новый клуб") || !strings.Contains(w.Body.String(), `"section":"competitors"`) {
		t.Fatalf("marketing: %s", w.Body.String())
	}
	w = httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest("POST", "/ai/marketing", strings.NewReader(`{"section":"x"}`)))
	if w.Code != 400 {
		t.Fatalf("bad section: %d", w.Code)
	}
}
