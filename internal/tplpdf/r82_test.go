package tplpdf

import (
	"os"
	"testing"

	"github.com/bnursik/business_surgery_backend/internal/content"
)

// R82: the checklist template of «Пройти тест Gallup» renders as a branded PDF.
func TestR82GallupTemplate(t *testing.T) {
	tl := content.RichToolByID("tl_r82_gallup")
	if tl == nil || tl.Template == nil {
		t.Fatal("no tool")
	}
	b, err := Render(tl)
	if err != nil || len(b) < 5000 || string(b[:4]) != "%PDF" {
		t.Fatalf("pdf %d %v", len(b), err)
	}
	if p := os.Getenv("R82_PDF"); p != "" {
		_ = os.WriteFile(p, b, 0o644)
	}
}
