// Package buildinfo tells which build of the server is running: the git
// commit and the build time.
//
// Set at build time with
//
//	go build -ldflags "-X github.com/bnursik/business_surgery_backend/internal/buildinfo.Commit=$(git rev-parse HEAD) -X github.com/bnursik/business_surgery_backend/internal/buildinfo.BuildTime=$(date -u +%Y-%m-%dT%H:%M:%SZ)" ./cmd/api
//
// (make build). Without -ldflags: RAILWAY_GIT_COMMIT_SHA (Railway sets it
// for every deploy from git), then the VCS stamp Go puts into the binary,
// then the binary's modification time as the build time.
package buildinfo

import (
	"os"
	"runtime/debug"
	"strings"
	"sync"
	"time"
)

// Set with -ldflags -X.
var (
	Commit    string
	BuildTime string
)

// Info: what the status shows.
type Info struct {
	Commit string    // full SHA, "" when unknown
	Built  time.Time // zero when unknown
	Source string    // ldflags | railway | vcs | binary
}

var (
	once sync.Once
	info Info
)

// Get reads the build info once.
func Get() Info {
	once.Do(func() { info = read() })
	return info
}

func read() Info {
	var in Info
	if c := strings.TrimSpace(Commit); c != "" {
		in.Commit, in.Source = c, "ldflags"
	}
	if t, err := time.Parse(time.RFC3339, strings.TrimSpace(BuildTime)); err == nil {
		in.Built = t
	}
	if in.Commit == "" {
		if c := strings.TrimSpace(os.Getenv("RAILWAY_GIT_COMMIT_SHA")); c != "" {
			in.Commit, in.Source = c, "railway"
		}
	}
	if bi, ok := debug.ReadBuildInfo(); ok {
		for _, s := range bi.Settings {
			switch s.Key {
			case "vcs.revision":
				if in.Commit == "" && s.Value != "" {
					in.Commit, in.Source = s.Value, "vcs"
				}
			case "vcs.time":
				if in.Built.IsZero() {
					if t, err := time.Parse(time.RFC3339, s.Value); err == nil {
						in.Built = t
					}
				}
			}
		}
	}
	if in.Built.IsZero() {
		if exe, err := os.Executable(); err == nil {
			if st, err := os.Stat(exe); err == nil {
				in.Built = st.ModTime().UTC()
				if in.Source == "" {
					in.Source = "binary"
				}
			}
		}
	}
	return in
}

// Short: the first 7 characters of the commit.
func (i Info) Short() string {
	if len(i.Commit) > 7 {
		return i.Commit[:7]
	}
	return i.Commit
}

// ID: one deploy: the commit, else the build time. "" when neither is known.
func (i Info) ID() string {
	if i.Commit != "" {
		return i.Commit
	}
	if !i.Built.IsZero() {
		return "built:" + i.Built.UTC().Format(time.RFC3339)
	}
	return ""
}
