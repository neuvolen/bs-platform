package tplpdf

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"github.com/bnursik/business_surgery_backend/internal/content"
)

// Every tool of the rich library renders to a valid PDF with the club footer
// font embedded; TPL_OUT=<dir> also writes the files for a visual check.
func TestRenderAll(t *testing.T) {
	tools := content.RichTools()
	if len(tools) < 100 {
		t.Fatalf("tools %d", len(tools))
	}
	out := os.Getenv("TPL_OUT")
	for i := range tools {
		tl := &tools[i]
		b, err := Render(tl)
		if err != nil {
			t.Fatalf("%s: %v", tl.ID, err)
		}
		if !bytes.HasPrefix(b, []byte("%PDF-")) || !bytes.Contains(b, []byte("%%EOF")) || len(b) < 20000 {
			t.Fatalf("%s: not a pdf (%d bytes)", tl.ID, len(b))
		}
		if out != "" {
			if err := os.WriteFile(filepath.Join(out, tl.ID+".pdf"), b, 0o644); err != nil {
				t.Fatal(err)
			}
		}
	}
}

// A tool without a template is an error, not an empty PDF.
func TestRenderStable(t *testing.T) {
	tl := content.RichToolByID("seed_tl_0")
	if tl == nil {
		t.Fatal("no seed_tl_0")
	}
	a, err := Render(tl)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(a, []byte("/Count ")) {
		t.Fatal("no pages")
	}
	if _, err := Render(&content.RichTool{ID: "x"}); err == nil {
		t.Fatal("no template must fail")
	}
}
