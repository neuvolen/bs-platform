// Package datadir: where the server keeps files that must outlive a deploy.
//
// Railway gives the service a volume mounted at /data (R55: «bs-data», 5 GB).
// BS_DATA_DIR names another folder; without it /data is used when it exists
// and is writable. Root is "" when there is no such folder: the callers then
// fall back to the container's temp dir (gone after the next deploy).
//
// What lives there (each with its own cap, so the volume cannot fill up):
//
//	bs-video/  Reels uploads and renders (VIDEO_MAX_MB, 2500 MB, oldest first)
//	bs-asr/    Whisper model, sherpa-onnx and ffmpeg (about 0.6 GB, downloaded once)
package datadir

import (
	"os"
	"path/filepath"
	"strings"
)

// Default: the Railway volume's mount path.
var Default = "/data"

// Root: the persistent folder, or "".
func Root() string {
	if d := strings.TrimSpace(os.Getenv("BS_DATA_DIR")); d != "" {
		if os.MkdirAll(d, 0o755) == nil && Writable(d) {
			return d
		}
	}
	if st, err := os.Stat(Default); err == nil && st.IsDir() && Writable(Default) {
		return Default
	}
	return ""
}

// Path: Root()/name, or the temp dir's name when there is no volume.
func Path(name string) string {
	if r := Root(); r != "" {
		return filepath.Join(r, name)
	}
	return filepath.Join(os.TempDir(), name)
}

// Persistent: p is on the volume.
func Persistent(p string) bool {
	r := Root()
	return r != "" && (p == r || strings.HasPrefix(p, r+string(os.PathSeparator)))
}

// Writable: a file can be made in dir.
func Writable(dir string) bool {
	f, err := os.CreateTemp(dir, ".w")
	if err != nil {
		return false
	}
	f.Close()
	os.Remove(f.Name())
	return true
}
