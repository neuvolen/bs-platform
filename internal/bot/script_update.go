package bot

import (
	"context"
	"strconv"
	"time"
)

// LatestScript is the version of the sheet's script the server ships
// (internal/content/script/Code.js, BS_VERSION). The sheet installs it itself:
// GET /api/v1/script/latest every hour, then POST /api/v1/script/updated.
const LatestScript = "2026-10-02-35"

// bot_meta keys of the script's self-update.
const (
	MetaScriptVersion      = "script_version" // the version that asked last (NoteScript)
	MetaScriptUpdVersion   = "script_update_version"
	MetaScriptUpdFrom      = "script_update_from"
	MetaScriptUpdAt        = "script_update_at"
	MetaScriptUpdDeploy    = "script_update_deployment"
	MetaScriptUpdVersionNo = "script_update_version_number"
	MetaScriptUpdError     = "script_update_error"
	MetaScriptUpdErrorAt   = "script_update_error_at"
)

// ScriptUpdated is what the sheet reports after trying to update itself.
type ScriptUpdated struct {
	TS            int64  `json:"ts"`
	Version       string `json:"version"`
	From          string `json:"from"`
	DeploymentID  string `json:"deploymentId"`
	VersionNumber int64  `json:"versionNumber"`
	Error         string `json:"error"`
}

// NoteScriptUpdated keeps the outcome in bot_meta.
func (s *Service) NoteScriptUpdated(ctx context.Context, u ScriptUpdated) error {
	now := time.Now().UTC().Format(time.RFC3339)
	set := map[string]string{}
	if u.Error != "" {
		set[MetaScriptUpdError] = u.Error
		set[MetaScriptUpdErrorAt] = now
	} else {
		set[MetaScriptUpdVersion] = u.Version
		set[MetaScriptUpdFrom] = u.From
		set[MetaScriptUpdAt] = now
		set[MetaScriptUpdDeploy] = u.DeploymentID
		set[MetaScriptUpdVersionNo] = strconv.FormatInt(u.VersionNumber, 10)
		set[MetaScriptUpdError] = ""
		set[MetaScriptUpdErrorAt] = ""
	}
	for k, v := range set {
		if err := s.repo.SetMeta(ctx, k, v); err != nil {
			return err
		}
	}
	if u.Error == "" {
		s.NoteScript(ctx, u.Version)
	}
	return nil
}

// ScriptUpdateStatus: shown in /api/v1/club/migration.
type ScriptUpdateStatus struct {
	Latest        string `json:"latest"`
	Running       string `json:"running,omitempty"`
	UpToDate      bool   `json:"upToDate"`
	UpdatedTo     string `json:"updatedTo,omitempty"`
	UpdatedFrom   string `json:"updatedFrom,omitempty"`
	UpdatedAt     string `json:"updatedAt,omitempty"`
	DeploymentID  string `json:"deploymentId,omitempty"`
	VersionNumber string `json:"versionNumber,omitempty"`
	LastError     string `json:"lastError,omitempty"`
	LastErrorAt   string `json:"lastErrorAt,omitempty"`
	// Error: the last self-update error, if the last try failed (= LastError)
	Error string `json:"error,omitempty"`
}

// ReadScriptUpdate reads the self-update state from bot_meta.
func ReadScriptUpdate(ctx context.Context, meta interface {
	GetMeta(ctx context.Context, key string) (string, error)
}) ScriptUpdateStatus {
	g := func(k string) string { v, _ := meta.GetMeta(ctx, k); return v }
	st := ScriptUpdateStatus{Latest: LatestScript, Running: g(MetaScriptVersion),
		UpdatedTo: g(MetaScriptUpdVersion), UpdatedFrom: g(MetaScriptUpdFrom), UpdatedAt: g(MetaScriptUpdAt),
		DeploymentID: g(MetaScriptUpdDeploy), VersionNumber: g(MetaScriptUpdVersionNo),
		LastError: g(MetaScriptUpdError), LastErrorAt: g(MetaScriptUpdErrorAt)}
	st.UpToDate = st.Running == LatestScript
	st.Error = st.LastError
	return st
}
