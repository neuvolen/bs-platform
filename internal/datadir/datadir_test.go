package datadir

import (
	"os"
	"path/filepath"
	"testing"
)

func TestRoot(t *testing.T) {
	old := Default
	defer func() { Default = old }()
	Default = filepath.Join(t.TempDir(), "nodata")
	t.Setenv("BS_DATA_DIR", "")
	if Root() != "" || Path("bs-video") != filepath.Join(os.TempDir(), "bs-video") {
		t.Fatal("no volume: temp dir")
	}
	vol := t.TempDir()
	Default = vol
	if Root() != vol || !Persistent(Path("bs-video")) || Persistent(filepath.Join(os.TempDir(), "x")) {
		t.Fatal("volume not used")
	}
	own := filepath.Join(t.TempDir(), "own")
	t.Setenv("BS_DATA_DIR", own)
	if Root() != own || Path("bs-asr") != filepath.Join(own, "bs-asr") {
		t.Fatal("BS_DATA_DIR not used")
	}
}
