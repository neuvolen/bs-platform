package bsfiles

import (
	"os"
	"testing"
)

// The real token is not in the repository; with a wrong one nothing opens.
func TestWrongTokenOpensNothing(t *testing.T) {
	if _, err := Manifest("123:wrong"); err == nil {
		t.Fatal("a wrong token must not decrypt the files")
	}
	tok := os.Getenv("BS_FILES_TOKEN")
	if tok == "" {
		t.Skip("BS_FILES_TOKEN not set")
	}
	items, err := Manifest(tok)
	if err != nil || len(items) == 0 {
		t.Fatalf("manifest: %v", err)
	}
	for _, it := range items {
		b, err := Read(tok, it)
		if err != nil || len(b) < 1000 {
			t.Fatalf("%s: %v", it.Name, err)
		}
	}
	t.Logf("%d files", len(items))
}
